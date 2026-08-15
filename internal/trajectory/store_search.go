package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"io"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"
)

func factWhere(q snapshot.TrajectorySelector) (string, []any) {
	clauses := []string{"s.active=1", "s.missing=0", "d.search_ready=1"}
	args := []any{}
	add := func(field, value string) {
		if value != "" {
			clauses = append(clauses, field+"=(SELECT id FROM symbols WHERE value=?)")
			args = append(args, value)
		}
	}
	add("s.agent", q.Agent)
	add("d.kind", q.Kind)
	add("d.role", q.Role)
	add("d.actor", q.ActorID)
	add("d.actor_kind", q.ActorKind)
	add("d.tool_fold", searchFold(q.Tool))
	if q.SessionID != "" {
		clauses = append(clauses, "('s.'||s.id||'.'||s.generation)=?")
		args = append(args, q.SessionID)
	}
	if q.Skill != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != "" {
		entity := []string{}
		addEntity := func(category, value string) {
			if value != "" {
				entity = append(entity, entitySelectorToken(category, value))
			}
		}
		addEntity("k", q.EntityKind)
		addEntity("i", q.EntityID)
		addEntity("p", q.Predicate)
		if q.Skill != "" {
			entity = append(entity, entitySelectorToken("k", "skill"), "("+entitySelectorToken("l", searchFold(q.Skill))+" OR "+entitySelectorToken("v", q.Skill)+")")
		}
		clauses = append(clauses, "d.rowid IN (SELECT o.event FROM occurrence_index o JOIN selector_fts ON selector_fts.rowid=o.rowid WHERE selector_fts MATCH ?)")
		args = append(args, strings.Join(entity, " AND "))
	}
	return strings.Join(clauses, " AND "), args
}
func (f *factStore) textCandidateQuery(q snapshot.TrajectorySelector, sourceIDs []string, candidateIDs []int64) (string, []any) {
	indexed := []string{}
	for _, term := range strings.Fields(strings.ToLower(q.Text)) {
		if utf8.RuneCountInString(term) >= 3 && !strings.ContainsRune(term, 0) {
			indexed = append(indexed, searchTrigrams(term))
		}
	}
	metadata := q
	metadata.Text = ""
	where, args := factWhere(metadata)
	if len(sourceIDs) > 0 {
		where += " AND EXISTS (SELECT 1 FROM sources p WHERE p.rowid=d.source AND p.id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(sourceIDs)), ",") + "))"
		for _, id := range sourceIDs {
			args = append(args, id)
		}
	}
	if f.authorized {
		where += " AND EXISTS (SELECT 1 FROM temp.trajectory_allowed_sources a WHERE a.rowid=d.source)"
	}
	// Metadata-free text search reads the narrow covering index. SQLite otherwise
	// prefers random reads of wide event rows, even though only source identity is
	// needed to dispatch candidates. Event predicates keep their own useful indexes.
	index := ""
	if q.Kind == "" && q.Role == "" && q.ActorID == "" && q.ActorKind == "" && q.Tool == "" && q.Skill == "" && q.EntityKind == "" && q.EntityID == "" && q.Predicate == "" {
		index = " INDEXED BY events_search_scope"
	}
	// Read current source eligibility once. Repeated random lookups of wide
	// source rows dominated full-archive candidate verification.
	candidateSQL := "WITH source_scope AS MATERIALIZED (SELECT rowid,id,generation,agent,active,missing,mtime FROM sources WHERE active=1 AND missing=0) SELECT d.rowid,d.source FROM events d" + index + " JOIN source_scope s ON s.rowid=d.source WHERE " + where
	candidateArgs := append([]any(nil), args...)
	if len(indexed) > 0 {
		candidateSQL += " AND d.rowid IN (SELECT rowid FROM text_fts WHERE text_fts MATCH ? UNION ALL SELECT rowid FROM events WHERE fts_unsafe=1 AND search_ready=1)"
		candidateArgs = append(candidateArgs, strings.Join(indexed, " AND "))
	}
	if len(candidateIDs) > 0 {
		candidateSQL += " AND d.rowid IN (" + strings.TrimSuffix(strings.Repeat("?,", len(candidateIDs)), ",") + ")"
		for _, id := range candidateIDs {
			candidateArgs = append(candidateArgs, id)
		}
	}
	return candidateSQL, candidateArgs
}

func (f *factStore) prepareTextMatches(parent context.Context, q snapshot.TrajectorySelector) (err error) {
	return f.prepareTextMatchesForSources(parent, q, nil)
}

// Default retrieval verifies ordered candidate windows until it has the page
// and one actual lookahead match. FTS membership alone never becomes a hit or
// a count. The owning connection pins authorization and the temporary rowids;
// no content or query proof survives this operation.
func (f *factStore) prepareTextPage(ctx context.Context, q snapshot.TrajectorySelector, start int) (total int, err error) {
	conn, err := f.db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS temp.trajectory_text_matches")
		}
	}()
	if _, err = conn.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS trajectory_text_matches(rowid INTEGER PRIMARY KEY); DELETE FROM temp.trajectory_text_matches"); err != nil {
		return 0, err
	}
	candidateSQL, args := f.textCandidateQuery(q, nil, nil)
	if q.Collection == "sessions" {
		candidateSQL = strings.Replace(candidateSQL, "SELECT d.rowid,d.source FROM", "SELECT s.id,d.source FROM", 1)
		candidateSQL += " GROUP BY d.source ORDER BY s.mtime DESC,s.id"
	} else {
		candidateSQL += " ORDER BY s.mtime DESC,s.id,d.offset,d.block"
	}
	rows, err := conn.QueryContext(ctx, candidateSQL, args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	selected := make([]int64, 0, q.Limit+1)
	matched := 0
	exhausted := false
	for !exhausted && len(selected) <= q.Limit {
		sources := []string{}
		ids := []int64{}
		// Source existence needs only one exact hit; event windows verify all
		// candidates. Each window is bounded, even when every trigram is false.
		window := 128
		if q.Collection == "sessions" {
			window = 32
		}
		for i := 0; i < window; i++ {
			if !rows.Next() {
				exhausted = true
				break
			}
			var source int64
			if q.Collection == "sessions" {
				var id string
				err = rows.Scan(&id, &source)
				sources = append(sources, id)
			} else {
				var id int64
				err = rows.Scan(&id, &source)
				ids = append(ids, id)
			}
			if err != nil {
				return 0, err
			}
		}
		if err = rows.Err(); err != nil {
			return 0, err
		}
		if len(sources)+len(ids) == 0 {
			break
		}
		if err = f.prepareTextMatchesOn(ctx, q, sources, ids, q.Collection == "sessions", conn); err != nil {
			return 0, err
		}
		verified, err := conn.QueryContext(ctx, "SELECT d.rowid,s.id FROM temp.trajectory_text_matches m CROSS JOIN events d ON d.rowid=m.rowid JOIN sources s ON s.rowid=d.source ORDER BY s.mtime DESC,s.id,d.offset,d.block")
		if err != nil {
			return 0, err
		}
		for verified.Next() {
			var id int64
			var source string
			if err = verified.Scan(&id, &source); err != nil {
				break
			}
			if matched >= start && len(selected) <= q.Limit {
				selected = append(selected, id)
			}
			matched++
		}
		if err == nil {
			err = verified.Err()
		}
		verified.Close()
		if err != nil {
			return 0, err
		}
	}
	if err = rows.Close(); err != nil {
		return 0, err
	}
	if _, err = conn.ExecContext(ctx, "DELETE FROM temp.trajectory_text_matches"); err != nil {
		return 0, err
	}
	if len(selected) > 0 {
		values := make([]any, len(selected))
		for i, id := range selected {
			values[i] = id
		}
		_, err = conn.ExecContext(ctx, "INSERT INTO temp.trajectory_text_matches(rowid) VALUES "+strings.TrimSuffix(strings.Repeat("(?),", len(values)), ","), values...)
	}
	// This is a pagination boundary, never an exposed estimate of the total.
	return start + len(selected), err
}

// Session pagination needs exact existence for every source, then all matches
// only for the selected page. Event queries still verify every candidate.
func (f *factStore) prepareTextMatchesForSources(parent context.Context, q snapshot.TrajectorySelector, sourceIDs []string) (err error) {
	conn, err := f.db.Conn(parent)
	if err != nil {
		return err
	}
	defer conn.Close()
	return f.prepareTextMatchesOn(parent, q, sourceIDs, nil, q.Collection == "sessions" && len(sourceIDs) == 0, conn)
}

func (f *factStore) prepareTextMatchesOn(parent context.Context, q snapshot.TrajectorySelector, sourceIDs []string, candidateIDs []int64, firstPerSource bool, conn *sql.Conn) (err error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() {
		if err != nil {
			_, _ = conn.ExecContext(context.Background(), "DROP TABLE IF EXISTS temp.trajectory_text_matches")
		}
	}()
	var verified []int64
	if len(sourceIDs) > 0 {
		values := make([]any, len(sourceIDs))
		for i, id := range sourceIDs {
			values[i] = id
		}
		// The verified set is small. Pin it as the outer loop rather than letting
		// SQLite scan the complete archive to rediscover these query-local rows.
		seed, e := conn.QueryContext(ctx, "SELECT m.rowid FROM temp.trajectory_text_matches m CROSS JOIN events d ON d.rowid=m.rowid JOIN sources s ON s.rowid=d.source WHERE s.id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(sourceIDs)), ",")+")", values...)
		if e != nil {
			return e
		}
		for seed.Next() {
			var id int64
			if e = seed.Scan(&id); e != nil {
				break
			}
			verified = append(verified, id)
		}
		if e == nil {
			e = seed.Err()
		}
		seed.Close()
		if e != nil {
			return e
		}
	}
	if _, err = conn.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS trajectory_text_matches(rowid INTEGER PRIMARY KEY); DELETE FROM temp.trajectory_text_matches"); err != nil {
		return err
	}
	// These rowids were verified under this query's unchanged authorization and
	// source boundary. Reuse their proof instead of reading the first hit twice.
	if len(verified) > 0 {
		values := make([]any, len(verified))
		for i, id := range verified {
			values[i] = id
		}
		if _, err = conn.ExecContext(ctx, "INSERT INTO temp.trajectory_text_matches(rowid) VALUES "+strings.TrimSuffix(strings.Repeat("(?),", len(verified)), ","), values...); err != nil {
			return err
		}
	}
	terms := strings.Fields(strings.ToLower(q.Text))
	needles := make([][]byte, len(terms))
	for i, term := range terms {
		needles[i] = []byte(term)
	}
	candidateSQL, candidateArgs := f.textCandidateQuery(q, sourceIDs, candidateIDs)
	if len(verified) > 0 {
		candidateSQL += " AND d.rowid NOT IN (SELECT rowid FROM temp.trajectory_text_matches)"
	}
	rows, err := conn.QueryContext(ctx, candidateSQL, candidateArgs...)
	if err != nil {
		return err
	}
	defer rows.Close()
	// Candidate rowids were authorized on the owning connection before dispatch.
	// Readers fetch only these already filtered immutable rowids. The owning
	// Service operation prevents a writer from changing their source boundary.
	uri := url.URL{Scheme: "file", Path: f.path, RawQuery: "mode=ro"}
	const workers = 16
	const batchSize = 256
	type candidateRef struct{ id, source int64 }
	// Source affinity gives one reader ownership of each existence proof. A
	// shared queue can decode the same large source on every worker before its
	// first positive result becomes visible. Exhaustive queries keep one queue.
	queues := 1
	if firstPerSource {
		queues = workers
	}
	jobs := make([]chan []candidateRef, queues)
	for i := range jobs {
		jobs[i] = make(chan []candidateRef, workers/queues)
	}
	results := make(chan []int64, workers)
	failures := make(chan error, 1)
	var matchedSources sync.Map
	fail := func(e error) {
		select {
		case failures <- e:
		default:
		}
		cancel()
	}
	var readers sync.WaitGroup
	for i := 0; i < workers; i++ {
		queue := jobs[i%len(jobs)]
		readers.Add(1)
		go func() {
			defer readers.Done()
			db, e := sql.Open("sqlite", uri.String())
			if e != nil {
				fail(e)
				return
			}
			defer db.Close()
			db.SetMaxOpenConns(1)

			var cachedID int64 = -1
			var cached []byte
			var decodeBuffer []byte
			for pending := range queue {
				// Batch metadata even when candidates share a source. Literal verification
				// below stops at its first exact hit; false positives need no one-row SQL
				// round trip or repeated scan of the remaining batch.
				ids := make([]int64, 0, len(pending))
				for _, ref := range pending {
					if firstPerSource {
						if _, ok := matchedSources.Load(ref.source); ok {
							continue
						}
					}
					ids = append(ids, ref.id)
				}
				if len(ids) == 0 {
					continue
				}
				placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
				values := []any{}
				for _, id := range ids {
					values = append(values, id)
				}
				records, e := db.QueryContext(ctx, "SELECT d.rowid,d.source,c.block,c.offset,c.length,t.value,d.has_tool FROM events d LEFT JOIN contents c ON c.rowid=d.rowid JOIN symbols t ON t.id=d.tool WHERE d.rowid IN ("+placeholders+") ORDER BY c.block,d.rowid", values...)
				if e != nil {
					fail(e)
					return
				}
				type candidate struct {
					id, source            int64
					block, offset, length sql.NullInt64
					name                  string
					tool                  bool
				}
				batch := []candidate{}
				for records.Next() {
					var c candidate
					if e = records.Scan(&c.id, &c.source, &c.block, &c.offset, &c.length, &c.name, &c.tool); e != nil {
						break
					}
					batch = append(batch, c)
				}
				if e == nil {
					e = records.Err()
				}
				records.Close()
				if e != nil {
					fail(e)
					return
				}
				matched := make([]int64, 0, len(ids))
				for _, c := range batch {
					if firstPerSource {
						if _, ok := matchedSources.Load(c.source); ok {
							continue
						}
					}
					var raw []byte
					if c.block.Valid {
						if cachedID != c.block.Int64 {
							var body []byte
							if e = db.QueryRowContext(ctx, "SELECT body FROM blocks WHERE rowid=?", c.block.Int64).Scan(&body); e != nil {
								break
							}
							cached, e = decodeFactBlockInto(ctx, body, decodeBuffer)
							if e != nil {
								break
							}
							if body[3] == 4 && len(cached) <= factBlockBytes {
								decodeBuffer = cached
							}
							cachedID = c.block.Int64
						}
						offset, length := int(c.offset.Int64), int(c.length.Int64)
						if offset < 0 || length < 0 || offset > len(cached) || length > len(cached)-offset {
							e = errors.New("trajectory block extent mismatch")
							break
						}
						raw = cached[offset : offset+length]
					}
					found, err := factPlainTextMatches(ctx, raw, c.name, c.tool, needles)
					if err != nil {
						e = err
						break
					}
					if found {
						if firstPerSource {
							matchedSources.Store(c.source, true)
						}
						matched = append(matched, c.id)
					}
				}
				if e != nil {
					fail(e)
					return
				}
				select {
				case results <- matched:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		defer func() {
			for _, queue := range jobs {
				close(queue)
			}
		}()
		defer rows.Close()
		batches := make([][]candidateRef, len(jobs))
		for rows.Next() {
			var id, source int64
			if e := rows.Scan(&id, &source); e != nil {
				fail(e)
				return
			}
			if firstPerSource {
				if _, ok := matchedSources.Load(source); ok {
					continue
				}
			}
			route := int(uint64(source) % uint64(len(jobs)))
			batches[route] = append(batches[route], candidateRef{id: id, source: source})
			if len(batches[route]) == batchSize {
				select {
				case jobs[route] <- batches[route]:
				case <-ctx.Done():
					return
				}
				batches[route] = nil
			}
		}
		if e := rows.Err(); e != nil {
			fail(e)
			return
		}
		for route, ids := range batches {
			if len(ids) > 0 {
				select {
				case jobs[route] <- ids:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	go func() { producer.Wait(); readers.Wait(); close(results) }()
	// SQLite permits stepping a TEMP-table insert while the independent source
	// cursor is open. database/sql serializes calls on this one connection.
	for ids := range results {
		if len(ids) == 0 || ctx.Err() != nil {
			continue
		}
		values := make([]any, len(ids))
		for i, id := range ids {
			values[i] = id
		}
		placeholders := strings.TrimSuffix(strings.Repeat("(?),", len(ids)), ",")
		if _, e := conn.ExecContext(ctx, "INSERT OR IGNORE INTO temp.trajectory_text_matches(rowid) VALUES "+placeholders, values...); e != nil {
			fail(e)
		}
	}
	select {
	case e := <-failures:
		return e
	default:
	}
	return parent.Err()
}

// Candidate verification streams only the canonical content, preserving the
// exact normalization across UTF-8 and segment boundaries. Every frame is read
// through its checksum even after all terms have been found.
func factTextMatches(ctx context.Context, value []byte, name string, tool bool, terms [][]byte, scratch []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	seen := make([]bool, len(terms))
	overlap := 0
	for _, t := range terms {
		overlap = max(overlap, len(t)-1)
	}
	if overlap >= len(scratch) {
		return false, ErrInvalid
	}
	tail := 0
	emit := func(part string) {
		body := append(scratch[:tail], []byte(part)...)
		for i, t := range terms {
			if !seen[i] && bytes.Contains(body, t) {
				seen[i] = true
			}
		}
		tail = min(overlap, len(body))
		copy(scratch[:tail], body[len(body)-tail:])
	}
	if len(value) > 0 {
		if len(value) < 8 || value[0] != 0 || !bytes.Equal(value[1:3], valueMagic[1:3]) || (value[3] != 0 && value[3] != 4) {
			return false, errors.New("invalid trajectory content frame")
		}
		size := int(binary.BigEndian.Uint32(value[4:8]))
		if size < 8 || size > maxStoredValue {
			return false, errors.New("invalid trajectory content length")
		}
		var reader io.Reader
		if value[3] == 0 {
			if len(value)-8 != size {
				return false, errors.New("trajectory content length mismatch")
			}
			reader = bytes.NewReader(value[8:])
		} else {
			pooled, err := openStoredReader(value[8:], storageDictionary)
			if err != nil {
				return false, err
			}
			defer releaseStoredReader(pooled)
			reader = pooled.reader
		}
		var header [8]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			return false, err
		}
		a, b := int(binary.BigEndian.Uint32(header[:4])), int(binary.BigEndian.Uint32(header[4:]))
		if a+b != size-8 || (!tool && b != 0) {
			return false, errors.New("trajectory content segment mismatch")
		}
		scan := func(length int) error {
			buf := make([]byte, 32*1024+4)
			pending := 0
			for length > 0 {
				if err := ctx.Err(); err != nil {
					return err
				}
				n := min(length, len(buf)-pending)
				if _, err := io.ReadFull(reader, buf[pending:pending+n]); err != nil {
					return err
				}
				length -= n
				end := pending + n
				cut := end
				if length > 0 {
					start := end - 1
					for start > 0 && !utf8.RuneStart(buf[start]) {
						start--
					}
					if !utf8.FullRune(buf[start:end]) {
						cut = start
					}
				}
				emit(strings.ToLower(string(buf[:cut])))
				pending = end - cut
				copy(buf[:pending], buf[cut:end])
			}
			if pending > 0 {
				emit(strings.ToLower(string(buf[:pending])))
			}
			return nil
		}
		if err := scan(a); err != nil {
			return false, err
		}
		if tool {
			emit(" " + strings.ToLower(name) + " ")
		}
		if err := scan(b); err != nil {
			return false, err
		}
		var extra [1]byte
		n, err := reader.Read(extra[:])
		if n != 0 || err != io.EOF {
			if err != nil {
				return false, err
			}
			return false, errors.New("trajectory content length mismatch")
		}
	} else if tool {
		emit(" " + strings.ToLower(name) + " ")
	}
	for _, v := range seen {
		if !v {
			return false, nil
		}
	}
	return true, ctx.Err()
}

func factPlainTextMatches(ctx context.Context, raw []byte, name string, tool bool, terms [][]byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var text, args []byte
	if len(raw) > 0 {
		if len(raw) < 8 {
			return false, errors.New("invalid trajectory content frame")
		}
		a, b := int(binary.BigEndian.Uint32(raw[:4])), int(binary.BigEndian.Uint32(raw[4:8]))
		if a+b != len(raw)-8 {
			return false, errors.New("trajectory content length mismatch")
		}
		if !tool && b > 0 {
			return false, errors.New("trajectory content tool mismatch")
		}
		text, args = raw[8:8+a], raw[8+a:]
	}
	// The complete block and content extents have already been validated. Avoid
	// copying and lower-casing a multi-megabyte envelope after its terms are found.
	// Malformed UTF-8 keeps strings.ToLower's whole-string replacement semantics.
	if !utf8.Valid(text) || !utf8.Valid(args) || !utf8.ValidString(name) {
		normalized := factSearchText(string(text), name, args, tool)
		for _, term := range terms {
			if !strings.Contains(normalized, string(term)) {
				return false, nil
			}
		}
		return true, ctx.Err()
	}
	seen := make([]bool, len(terms))
	remaining, overlap := len(terms), 0
	for i, term := range terms {
		overlap = max(overlap, len(term)-1)
		if len(term) == 0 {
			seen[i] = true
			remaining--
		}
	}
	tail := ""
	emit := func(part string) {
		body := tail + strings.ToLower(part)
		for i, term := range terms {
			if !seen[i] && strings.Contains(body, string(term)) {
				seen[i] = true
				remaining--
			}
		}
		tail = body[max(0, len(body)-overlap):]
	}
	scan := func(part []byte) error {
		for len(part) > 0 && remaining > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			end := min(len(part), 32*1024)
			for end < len(part) && !utf8.RuneStart(part[end]) {
				end--
			}
			emit(string(part[:end]))
			part = part[end:]
		}
		return nil
	}
	if err := scan(text); err != nil {
		return false, err
	}
	if tool && remaining > 0 {
		emit(" " + name + " ")
		if err := scan(args); err != nil {
			return false, err
		}
	}
	return remaining == 0, ctx.Err()
}
