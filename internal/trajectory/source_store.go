package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// sourceStore owns canonical locators and exact source reconstruction.
// Only sparse ranges and non-reproducible exceptions persist event content.
type sourceStore struct {
	db              *sql.DB
	path            string
	checkCapacity   func(string, uint64) error
	checkpointMu    sync.Mutex
	checkpointCache map[string]*list.Element
	checkpointLRU   list.List
	checkpointBytes int
	rangeMu         sync.Mutex
	rangeCache      map[[sha256.Size]byte]*list.Element
	rangeLRU        list.List
	rangeBytes      int
}

const sourceStoreVersion = 3
const sourceStoreSchema = `
CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL) WITHOUT ROWID;
INSERT INTO meta VALUES('revision','0');
CREATE TABLE metadata(path BLOB PRIMARY KEY,sequence TEXT NOT NULL,body BLOB,bucket INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE sources(rowid INTEGER PRIMARY KEY,id TEXT NOT NULL,generation TEXT NOT NULL,active INTEGER NOT NULL DEFAULT 1,agent TEXT NOT NULL,mtime INTEGER NOT NULL,missing INTEGER NOT NULL DEFAULT 0,checkpoint BLOB NOT NULL,complete INTEGER NOT NULL DEFAULT 0,search_count INTEGER NOT NULL DEFAULT 0,search_offset INTEGER NOT NULL DEFAULT -1,search_block INTEGER NOT NULL DEFAULT -1,search_entity_gaps INTEGER NOT NULL DEFAULT 0,verified_size INTEGER NOT NULL DEFAULT -1,verified_mtime INTEGER NOT NULL DEFAULT -1,audit_size INTEGER NOT NULL DEFAULT -1,audit_mtime INTEGER NOT NULL DEFAULT -1,audit_after INTEGER NOT NULL DEFAULT -1);
CREATE UNIQUE INDEX source_active ON sources(id) WHERE active=1;
CREATE INDEX sources_retired ON sources(rowid) WHERE active=0 OR missing=1;
CREATE TABLE ranges(rowid INTEGER PRIMARY KEY,source INTEGER NOT NULL REFERENCES sources(rowid) ON DELETE CASCADE,start INTEGER NOT NULL,end INTEGER NOT NULL,body BLOB NOT NULL,filter BLOB NOT NULL,UNIQUE(source,start));
CREATE TABLE exceptions(rowid INTEGER PRIMARY KEY,source INTEGER NOT NULL REFERENCES sources(rowid) ON DELETE CASCADE,offset INTEGER NOT NULL,block INTEGER NOT NULL,body BLOB NOT NULL,end_offset INTEGER NOT NULL,end_block INTEGER NOT NULL,records INTEGER NOT NULL,UNIQUE(source,offset,block));
`

func openSourceStore(ctx context.Context, path string) (*sourceStore, error) {
	return openSourceStoreFormat(ctx, path, false)
}

// Only the cutover/upgrade owner may inspect v2 metadata. Public readers never
// accept the old body representation.
func openMigrationSourceStore(ctx context.Context, path string) (*sourceStore, error) {
	return openSourceStoreFormat(ctx, path, true)
}
func openSourceStoreFormat(ctx context.Context, path string, migration bool) (*sourceStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = file.Chmod(0600)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: path}
	p := url.Values{}
	for _, pragma := range []string{"busy_timeout(200)", "temp_store(MEMORY)", "foreign_keys(ON)", "cache_size(-4096)"} {
		p.Add("_pragma", pragma)
	}
	u.RawQuery = p.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(err error) (*sourceStore, error) { _ = db.Close(); return nil, err }
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version == 0 {
		var tables int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&tables); err != nil {
			return fail(err)
		}
		if tables != 0 {
			return fail(errors.New("unrecognized trajectory source store; preserved"))
		}
		if err = historyfile.CheckStorageCapacity(path, 256*1024); err != nil {
			return fail(err)
		}
		if _, err = db.ExecContext(ctx, "PRAGMA page_size=16384; PRAGMA auto_vacuum=INCREMENTAL; PRAGMA journal_mode=DELETE;"); err != nil {
			return fail(err)
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		if _, err = tx.ExecContext(ctx, sourceStoreSchema+fmt.Sprintf("PRAGMA user_version=%d;", sourceStoreVersion)); err == nil {
			err = commitSearchTransaction(tx, db)
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return fail(err)
		}
	} else if version == 2 && !migration {
		return fail(errStorageMigration)
	} else if version != sourceStoreVersion && !(migration && version == 2) {
		return fail(errors.New("unrecognized trajectory source store version; preserved"))
	}
	return &sourceStore{db: db, path: path, checkCapacity: historyfile.CheckStorageCapacity}, nil
}

// Checksumming even uncompressed frames prevents a damaged candidate/anchor
// from silently excluding a true event. Decoder allocation is bounded first.
func encodeSourceValue(raw []byte) ([]byte, error) {
	return encodeSourceValueBounded(raw, maxStoredValue)
}
func encodeSourceValueBounded(raw []byte, max int) ([]byte, error) {
	body, err := encodeTextStoredBounded(string(raw), max)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(body)
	return append(hash[:], body...), nil
}

func decodeSourceValue(value []byte, max int) ([]byte, error) {
	if len(value) < sha256.Size+8 || len(value) > sha256.Size+8+max {
		return nil, errors.New("invalid source store value size")
	}
	body := value[sha256.Size:]
	hash := sha256.Sum256(body)
	if !bytes.Equal(hash[:], value[:sha256.Size]) || binary.BigEndian.Uint32(body[4:8]) > uint32(max) {
		return nil, errors.New("invalid source store checksum/length")
	}
	return decodeStoredBounded(body, max)
}

func (f *sourceStore) write(ctx context.Context, additional uint64, apply func(*sql.Tx) error) error {
	// The bounded 64-record batches may touch separate 16 KiB leaf/branch
	// pages. Include those pages and their rollback journal, independently
	// of the caller's variable-sized encoded values.
	additional += 4 * 1024 * 1024
	if err := f.checkCapacity(f.path, additional); err != nil {
		return err
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = apply(tx); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='revision'"); err != nil {
		return err
	}
	return commitSearchTransaction(tx, f.db)
}

// A failed partial replay must not turn a bounded fact batch into an unbounded
// DELETE and rollback journal. Only derived shadow rows are cleared here; the
// original facts and migration cursor remain untouched until replay resumes.
func (f *sourceStore) clearMigrationSource(ctx context.Context, source int64) (bool, error) {
	for _, table := range []string{"ranges", "exceptions"} {
		valueSize := "length(body)"
		if table == "ranges" {
			valueSize += "+length(filter)"
		}
		rows, err := f.db.QueryContext(ctx, "SELECT rowid,"+valueSize+" FROM "+table+" WHERE source=? LIMIT 64", source)
		if err != nil {
			return false, err
		}
		var ids []int64
		var additional uint64
		for rows.Next() {
			var id, bytes int64
			if err = rows.Scan(&id, &bytes); err != nil {
				break
			}
			if bytes < 0 || bytes > maxSourceRangeLogicalBytes {
				err = errors.New("invalid shadow cleanup value; originals preserved")
				break
			}
			ids = append(ids, id)
			additional += 2 * uint64(bytes)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return false, err
		}
		if len(ids) == 0 {
			continue
		}
		if err = f.write(ctx, additional, func(tx *sql.Tx) error {
			for _, id := range ids {
				if _, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE rowid=? AND source=?", id, source); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return false, err
		}
		var remaining bool
		if err = f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM "+table+" WHERE source=?)", source).Scan(&remaining); err != nil {
			return false, err
		}
		if remaining {
			// A restart discovers remaining rows without another cursor.
			return false, nil
		}
	}
	return true, nil
}

func putSourceCheckpoint(tx *sql.Tx, id, agent string, c sourceCheckpoint) (int64, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	body, err := encodeSourceValue(raw)
	if err != nil {
		return 0, err
	}
	var row int64
	var generation string
	err = tx.QueryRow("SELECT rowid,generation FROM sources WHERE id=? AND active=1", id).Scan(&row, &generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && generation == c.Generation {
		_, err = tx.Exec("UPDATE sources SET agent=?,mtime=?,missing=?,checkpoint=? WHERE rowid=?", agent, c.Mtime, c.Missing, body, row)
		return row, err
	}
	if _, err = tx.Exec("UPDATE sources SET active=0 WHERE id=? AND active=1", id); err != nil {
		return 0, err
	}
	result, err := tx.Exec("INSERT INTO sources(id,generation,agent,mtime,missing,checkpoint) VALUES(?,?,?,?,?,?)", id, c.Generation, agent, c.Mtime, c.Missing, body)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (f *sourceStore) checkpoint(ctx context.Context, id string) (sourceCheckpoint, bool, error) {
	var c sourceCheckpoint
	var body []byte
	var generation string
	var missing bool
	err := f.db.QueryRowContext(ctx, "SELECT generation,checkpoint,missing FROM sources WHERE id=? AND active=1", id).Scan(&generation, &body, &missing)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	c, err = f.decodeCheckpoint(id, body, 2*maxRecordBytes)
	if err == nil && c.Generation != generation {
		err = ErrStale
	}
	if missing {
		c.Missing = true
	}
	return c, true, err
}

// Replay count is a decoder validation boundary; Facts is the canonical count
// after applying exceptions, including events no longer emitted by a decoder.
type sourceRange struct {
	Chunk            replayChunk     `json:"chunk"`
	Facts            int             `json:"facts"`
	LogicalBytes     int             `json:"logical_bytes"`
	First            *sourcePosition `json:"first,omitempty"`
	Last             *sourcePosition `json:"last,omitempty"`
	EntityIncomplete int             `json:"entity_incomplete,omitempty"`
}

type sourcePosition struct {
	Offset int64 `json:"offset"`
	Block  int   `json:"block"`
}

func putSourceRange(tx *sql.Tx, source int64, c sourceRange, filter *sourceFilter) error {
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	body, err := encodeSourceValue(raw)
	if err != nil {
		return err
	}
	bits, err := filter.encode()
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO ranges(source,start,end,body,filter) VALUES(?,?,?,?,?)", source, c.Chunk.Start.Offset, c.Chunk.End.Offset, body, bits)
	return err
}

// A nil Event suppresses a newly decoded fact absent from the canonical store.
// Coordinates are physical keys, separate from the fully preserved public DTO.
type sourceException struct {
	Offset int64                     `json:"offset"`
	Block  int                       `json:"block"`
	Event  *snapshot.TrajectoryEvent `json:"event"`
}

type storedRange struct {
	row    int64
	value  sourceRange
	filter *sourceFilter
}

func (f *sourceStore) sourceRanges(ctx context.Context, st *sourceState, after int64, limit int) ([]storedRange, error) {
	rows, err := f.db.QueryContext(ctx, "SELECT r.rowid,r.start,r.end,r.body,r.filter FROM ranges r JOIN sources s ON s.rowid=r.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND r.start>? ORDER BY r.start LIMIT ?", st.ID, st.Generation, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []storedRange{}
	for rows.Next() {
		var r storedRange
		var start, end int64
		var body, bits []byte
		if err = rows.Scan(&r.row, &start, &end, &body, &bits); err != nil {
			return nil, err
		}
		r.value, err = f.decodeRange(body)
		if err != nil {
			return nil, err
		}
		if r.value.Chunk.Start.Offset != start || r.value.Chunk.End.Offset != end || r.value.Facts < 0 || r.value.LogicalBytes < 0 {
			return nil, ErrStale
		}
		r.filter, err = decodeSourceFilter(bits)
		if err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, rows.Err()
}

func (f *sourceStore) rangeExceptions(ctx context.Context, st *sourceState, start, end int64) ([]sourceException, error) {
	rows, err := f.db.QueryContext(ctx, "SELECT e.offset,e.block,e.end_offset,e.end_block,e.records,e.body FROM exceptions e JOIN sources s ON s.rowid=e.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND e.offset>=? AND e.offset<? ORDER BY e.offset,e.block", st.ID, st.Generation, start, end)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []sourceException{}
	allocated := 0
	for rows.Next() {
		var first, last sourcePosition
		var count int
		var body []byte
		if err = rows.Scan(&first.Offset, &first.Block, &last.Offset, &last.Block, &count, &body); err != nil {
			return nil, err
		}
		if last.Offset >= end {
			return nil, ErrStale
		}
		facts, logical, e := decodeSourceExceptionBlock(body, first, last, count)
		if e != nil {
			return nil, e
		}
		allocated += logical
		if allocated > maxSourceRangeLogicalBytes {
			return nil, errors.New("source range exceptions exceed bounded read budget; preserved")
		}
		result = append(result, facts...)
	}
	return result, rows.Err()
}

// Every range remains independently seekable; this owns only one range's
// exceptions and regenerated events at a time, never a historical session.
func (f *sourceStore) walkQuery(ctx context.Context, st *sourceState, q *snapshot.TrajectorySelector, visit func(snapshot.TrajectoryEvent, int) error) error {
	var candidate func(*sourceFilter) bool
	if q != nil {
		candidate = func(filter *sourceFilter) bool { return filter.maybe(*q) }
	}
	return f.walkSourceCandidates(ctx, st, candidate, nil, func(fact sourceFact) error { return visit(fact.event, fact.size) })
}

const maxSourceRangeLogicalBytes = 128 * 1024 * 1024

type sourceFact struct {
	offset int64
	block  int
	event  snapshot.TrajectoryEvent
	size   int
}

func physicalLess(aOffset int64, aBlock int, bOffset int64, bBlock int) bool {
	return aOffset < bOffset || aOffset == bOffset && aBlock < bBlock
}

// Replay completes both hash passes and pathname validation before any caller
// can publish an event or return early. Residency is bounded to one range.
func replayFacts(ctx context.Context, st *sourceState, c replayChunk) ([]sourceFact, error) {
	return replayFactsFrom(ctx, st, c, nil)
}

func replayFactsFrom(ctx context.Context, st *sourceState, c replayChunk, source *os.File) ([]sourceFact, error) {
	facts := []sourceFact{}
	allocated := 0
	visit := func(e snapshot.TrajectoryEvent, n int) error {
		allocated += n
		if allocated > maxSourceRangeLogicalBytes {
			return errors.New("source range facts exceed bounded read budget; preserved")
		}
		facts = append(facts, sourceFact{e.Source.Offset, e.Source.Block, e, n})
		return nil
	}
	var err error
	if source == nil {
		_, err = replaySourceChunk(ctx, st, c, visit)
	} else {
		_, err = replaySourceChunkFrom(ctx, st, c, source, visit)
	}
	if err != nil {
		return nil, err
	}
	return facts, nil
}

func mergeSourceExceptions(facts []sourceFact, exceptions []sourceException) ([]sourceFact, error) {
	// The physical slot, not an exception's possibly overridden public ID or
	// Source fields, establishes order and the point-read locator.
	bySlot := make(map[string]sourceFact, len(facts)+len(exceptions))
	for _, fact := range facts {
		bySlot[string(eventKey(fact.offset, fact.block))] = fact
	}
	for _, exception := range exceptions {
		key := string(eventKey(exception.Offset, exception.Block))
		if exception.Event == nil {
			delete(bySlot, key)
			continue
		}
		raw, err := json.Marshal(exception.Event)
		if err != nil {
			return nil, err
		}
		bySlot[key] = sourceFact{exception.Offset, exception.Block, *exception.Event, len(raw)}
	}
	result := make([]sourceFact, 0, len(bySlot))
	allocated := 0
	for _, fact := range bySlot {
		allocated += fact.size
		if allocated > maxSourceRangeLogicalBytes {
			return nil, errors.New("canonical source range exceeds bounded read budget; preserved")
		}
		result = append(result, fact)
	}
	sort.Slice(result, func(i, j int) bool {
		return physicalLess(result[i].offset, result[i].block, result[j].offset, result[j].block)
	})
	return result, nil
}

func (f *sourceStore) readRange(ctx context.Context, st *sourceState, r storedRange) ([]sourceFact, error) {
	return f.readRangeFrom(ctx, st, r, nil)
}

func (f *sourceStore) readRangeFrom(ctx context.Context, st *sourceState, r storedRange, source *os.File) ([]sourceFact, error) {
	exceptions, err := f.rangeExceptions(ctx, st, r.value.Chunk.Start.Offset, r.value.Chunk.End.Offset)
	if err != nil {
		return nil, err
	}
	facts, err := replayFactsFrom(ctx, st, r.value.Chunk, source)
	if err != nil {
		return nil, err
	}
	facts, err = mergeSourceExceptions(facts, exceptions)
	if err != nil {
		return nil, err
	}
	logical := 0
	incomplete := 0
	for _, fact := range facts {
		logical += fact.size
		if fact.event.EntityCoverage != nil && !fact.event.EntityCoverage.Complete {
			incomplete++
		}
	}
	if len(facts) != r.value.Facts || logical != r.value.LogicalBytes || incomplete != r.value.EntityIncomplete {
		return nil, ErrStale
	}
	if len(facts) > 0 {
		first, last := facts[0], facts[len(facts)-1]
		if r.value.First == nil || r.value.Last == nil || *r.value.First != (sourcePosition{first.offset, first.block}) || *r.value.Last != (sourcePosition{last.offset, last.block}) {
			return nil, ErrStale
		}
	} else if r.value.First != nil || r.value.Last != nil {
		return nil, ErrStale
	}
	return facts, nil
}

// An incomplete migration, missing range or damaged count must never become a
// successful empty session. Check only sparse metadata, without loading DTOs.
func (f *sourceStore) validateRanges(ctx context.Context, st *sourceState) error {
	if err := f.validateCheckpoint(ctx, st); err != nil {
		return err
	}
	return f.checkRangeSequence(ctx, st)
}

func (f *sourceStore) validateCheckpoint(ctx context.Context, st *sourceState) error {
	var complete bool
	var checkpoint []byte
	err := f.db.QueryRowContext(ctx, "SELECT complete,checkpoint FROM sources WHERE id=? AND generation=? AND active=1 AND missing=0", st.ID, st.Generation).Scan(&complete, &checkpoint)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrStale
	}
	if err != nil {
		return err
	}
	if !complete {
		return errStorageMigration
	}
	c, err := f.decodeCheckpoint(st.ID, checkpoint, 2*maxRecordBytes)
	if err != nil {
		return err
	}
	if c.Generation != st.Generation || c.Offset != st.checkpoint.Offset || c.EventCount != st.checkpoint.EventCount {
		return ErrStale
	}
	return nil
}

// buildRange compares the entire canonical DTO, not a subset of display fields.
// A nil oracle is the normal online path; a non-nil empty oracle deliberately
// suppresses all regenerated events during a lossless physical migration.
func buildSourceRange(ctx context.Context, st *sourceState, chunk replayChunk, oracle *[]sourceFact) (sourceRange, *sourceFilter, []sourceException, error) {
	value := sourceRange{Chunk: chunk}
	replayed, err := replayFacts(ctx, st, chunk)
	if err != nil {
		return value, nil, nil, err
	}
	facts := replayed
	exceptions := []sourceException{}
	if oracle != nil {
		facts = *oracle
		remaining := make(map[string]sourceFact, len(facts))
		for _, fact := range facts {
			if fact.offset < chunk.Start.Offset || fact.offset >= chunk.End.Offset || fact.block < 0 {
				return value, nil, nil, ErrInvalid
			}
			key := string(eventKey(fact.offset, fact.block))
			if _, ok := remaining[key]; ok {
				return value, nil, nil, ErrInvalid
			}
			remaining[key] = fact
		}
		for _, fact := range replayed {
			key := string(eventKey(fact.offset, fact.block))
			original, ok := remaining[key]
			if !ok {
				exceptions = append(exceptions, sourceException{Offset: fact.offset, Block: fact.block})
				continue
			}
			delete(remaining, key)
			left, e := json.Marshal(original.event)
			if e != nil {
				return value, nil, nil, e
			}
			right, e := json.Marshal(fact.event)
			if e != nil {
				return value, nil, nil, e
			}
			if !bytes.Equal(left, right) {
				copy := original.event
				exceptions = append(exceptions, sourceException{fact.offset, fact.block, &copy})
			}
		}
		for _, fact := range remaining {
			copy := fact.event
			exceptions = append(exceptions, sourceException{fact.offset, fact.block, &copy})
		}
	}
	filter := sizedSourceFilter(chunk.End.Offset - chunk.Start.Offset)
	for _, fact := range facts {
		if err = filter.add(ctx, fact.event); err != nil {
			return value, nil, nil, err
		}
		raw, e := json.Marshal(fact.event)
		if e != nil {
			return value, nil, nil, e
		}
		value.Facts++
		value.LogicalBytes += len(raw)
		position := sourcePosition{fact.offset, fact.block}
		if value.First == nil || physicalLess(position.Offset, position.Block, value.First.Offset, value.First.Block) {
			copy := position
			value.First = &copy
		}
		if value.Last == nil || physicalLess(value.Last.Offset, value.Last.Block, position.Offset, position.Block) {
			copy := position
			value.Last = &copy
		}
		if fact.event.EntityCoverage != nil && !fact.event.EntityCoverage.Complete {
			value.EntityIncomplete++
		}
		if value.LogicalBytes > maxSourceRangeLogicalBytes {
			return value, nil, nil, errors.New("canonical source range exceeds bounded migration budget; preserved")
		}
	}
	return value, filter, exceptions, nil
}

// importRange is idempotent at one physical range. Existing ranges and their
// exceptions are replaced together, and an interrupted transaction does not
// publish either a checkpoint or a completion flag. Source paths are never
// persisted here as authorization capabilities.
func (f *sourceStore) importRange(ctx context.Context, st *sourceState, chunk replayChunk, oracle *[]sourceFact) error {
	sourceFile, before, err := openReplaySource(st)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	value, filter, exceptions, err := buildSourceRange(ctx, st, chunk, oracle)
	if err != nil {
		return err
	}
	additional := uint64(512 * 1024)
	for _, exception := range exceptions {
		raw, e := json.Marshal(exception)
		if e != nil {
			return e
		}
		additional += uint64(len(raw)) * 2 // new data plus rollback journal
	}
	return f.write(ctx, additional, func(tx *sql.Tx) error {
		source, err := putSourceCheckpoint(tx, st.ID, st.Agent, st.checkpoint)
		if err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE sources SET complete=0 WHERE rowid=?", source); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM ranges WHERE source=? AND start=?", source, chunk.Start.Offset); err != nil {
			return err
		}
		if _, err = tx.Exec("DELETE FROM exceptions WHERE source=? AND offset>=? AND offset<?", source, chunk.Start.Offset, chunk.End.Offset); err != nil {
			return err
		}
		if err = putSourceRange(tx, source, value, filter); err != nil {
			return err
		}
		if err = putSourceExceptions(tx, source, exceptions); err != nil {
			return err
		}
		return finishSourceOperation(ctx, st, sourceFile, before)
	})
}

func (f *sourceStore) completeSource(ctx context.Context, st *sourceState, searchCount int, searchOffset int64, searchBlock int) error {
	// Check the complete sparse sequence before publishing the flag. The owner
	// serializes source writes; no partial range becomes a readable session.
	if searchCount < 0 || searchCount > st.checkpoint.EventCount {
		return ErrInvalid
	}
	if st.checkpoint.Offset == 0 && st.checkpoint.EventCount == 0 {
		if err := f.write(ctx, 128*1024, func(tx *sql.Tx) error { _, err := putSourceCheckpoint(tx, st.ID, st.Agent, st.checkpoint); return err }); err != nil {
			return err
		}
	}
	if err := f.checkRangeSequence(ctx, st); err != nil {
		return err
	}
	entityGaps, err := f.readinessBounds(ctx, st, searchCount, searchOffset, searchBlock)
	if err != nil {
		return err
	}
	sourceFile, before, err := openReplaySource(st)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	if err = f.verifyRanges(ctx, st, sourceFile); err != nil {
		return err
	}
	return f.write(ctx, 128*1024, func(tx *sql.Tx) error {
		result, err := tx.Exec("UPDATE sources SET complete=1,search_count=?,search_offset=?,search_block=?,search_entity_gaps=?,verified_size=?,verified_mtime=? WHERE id=? AND generation=? AND active=1 AND missing=0", searchCount, searchOffset, searchBlock, entityGaps, before.Size(), before.ModTime().UnixNano(), st.ID, st.Generation)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err == nil && n != 1 {
			return ErrStale
		}
		if err != nil {
			return err
		}
		return finishSourceOperation(ctx, st, sourceFile, before)
	})
}

func (f *sourceStore) checkRangeSequence(ctx context.Context, st *sourceState) error {
	rows, err := f.db.QueryContext(ctx, "SELECT r.start,r.end,r.body FROM ranges r JOIN sources s ON s.rowid=r.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 ORDER BY r.start", st.ID, st.Generation)
	if err != nil {
		return err
	}
	defer rows.Close()
	anchor := replayAnchor{}
	count := 0
	for rows.Next() {
		var start, end int64
		var body []byte
		if err = rows.Scan(&start, &end, &body); err != nil {
			return err
		}
		value, err := f.decodeRange(body)
		if err != nil {
			return err
		}
		c := value.Chunk
		if c.Start.Offset != start || c.End.Offset != end || c.Start != anchor || end <= start || end > st.checkpoint.Offset || c.Version != projectionVersion || value.Facts < 0 || value.LogicalBytes < 0 {
			return ErrStale
		}
		count += value.Facts
		anchor = c.End
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if anchor.Offset != st.checkpoint.Offset || anchor.Line != st.checkpoint.Line || anchor.WorkingDirectory != st.checkpoint.WorkingDirectory || count != st.checkpoint.EventCount {
		return ErrStale
	}
	return nil
}

func (f *sourceStore) event(ctx context.Context, st *sourceState, offset int64, block int) (event snapshot.TrajectoryEvent, result error) {
	source, before, err := openReplaySource(st)
	if err != nil {
		return event, err
	}
	defer source.Close()
	defer func() {
		if err := finishSourceOperation(ctx, st, source, before); err != nil {
			event = snapshot.TrajectoryEvent{}
			result = err
		}
	}()
	if err := f.validateRanges(ctx, st); err != nil {
		return snapshot.TrajectoryEvent{}, err
	}
	var start int64
	err = f.db.QueryRowContext(ctx, "SELECT r.start FROM ranges r JOIN sources s ON s.rowid=r.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND r.start<=? AND r.end>? ORDER BY r.start DESC LIMIT 1", st.ID, st.Generation, offset, offset).Scan(&start)
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot.TrajectoryEvent{}, ErrNotFound
	}
	if err != nil {
		return snapshot.TrajectoryEvent{}, err
	}
	ranges, err := f.sourceRanges(ctx, st, start-1, 1)
	if err != nil {
		return snapshot.TrajectoryEvent{}, err
	}
	if len(ranges) != 1 || ranges[0].value.Chunk.Start.Offset != start {
		return snapshot.TrajectoryEvent{}, ErrStale
	}
	facts, err := f.readRangeFrom(ctx, st, ranges[0], source)
	if err != nil {
		return snapshot.TrajectoryEvent{}, err
	}
	for _, fact := range facts {
		if fact.offset == offset && fact.block == block {
			return fact.event, nil
		}
	}
	return snapshot.TrajectoryEvent{}, ErrNotFound
}

// Verify all previously committed raw ranges before sealing negative candidate
// decisions for a file observation. Prefix/last-line anchors alone cannot prove
// that a writer left the middle of a file unchanged when appending.
func (f *sourceStore) verifyRanges(ctx context.Context, st *sourceState, file *os.File) error {
	after := int64(-1)
	buf := make([]byte, 64*1024)
	for {
		ranges, err := f.sourceRanges(ctx, st, after, 16)
		if err != nil {
			return err
		}
		if len(ranges) == 0 {
			return ctx.Err()
		}
		for _, r := range ranges {
			c := r.value.Chunk
			h := sha256.New()
			reader := io.NewSectionReader(file, c.Start.Offset, c.End.Offset-c.Start.Offset)
			for {
				if err = ctx.Err(); err != nil {
					return err
				}
				n, readErr := reader.Read(buf)
				_, _ = h.Write(buf[:n])
				if readErr == io.EOF {
					break
				}
				if readErr != nil {
					return readErr
				}
			}
			if hex.EncodeToString(h.Sum(nil)) != c.Hash {
				return ErrStale
			}
			after = c.Start.Offset
		}
	}
}

func (f *sourceStore) readinessBounds(ctx context.Context, st *sourceState, count int, offset int64, block int) (int, error) {
	if count == 0 {
		if offset != -1 || block != -1 {
			return 0, ErrInvalid
		}
		return 0, nil
	}
	if offset < 0 || block < 0 {
		return 0, ErrInvalid
	}
	after := int64(-1)
	seen, gaps := 0, 0
	for {
		ranges, err := f.sourceRanges(ctx, st, after, 16)
		if err != nil {
			return 0, err
		}
		if len(ranges) == 0 {
			return 0, ErrStale
		}
		for _, r := range ranges {
			after = r.value.Chunk.Start.Offset
			if r.value.EntityIncomplete < 0 || r.value.EntityIncomplete > r.value.Facts {
				return 0, ErrStale
			}
			if r.value.Facts == 0 {
				if r.value.First != nil || r.value.Last != nil {
					return 0, ErrStale
				}
				continue
			}
			last := r.value.Last
			if r.value.First == nil || last == nil {
				return 0, ErrStale
			}
			if physicalLess(last.Offset, last.Block, offset, block) || last.Offset == offset && last.Block == block {
				seen += r.value.Facts
				gaps += r.value.EntityIncomplete
				if last.Offset == offset && last.Block == block {
					if seen != count {
						return 0, ErrStale
					}
					return gaps, nil
				}
				continue
			}
			facts, err := f.readRange(ctx, st, r)
			if err != nil {
				return 0, err
			}
			for _, fact := range facts {
				if physicalLess(offset, block, fact.offset, fact.block) {
					return 0, ErrStale
				}
				seen++
				if fact.event.EntityCoverage != nil && !fact.event.EntityCoverage.Complete {
					gaps++
				}
				if fact.offset == offset && fact.block == block {
					if seen != count {
						return 0, ErrStale
					}
					return gaps, nil
				}
			}
		}
	}
}

func (f *sourceStore) walk(ctx context.Context, st *sourceState, visit func(snapshot.TrajectoryEvent, int) error) error {
	return f.walkQuery(ctx, st, nil, visit)
}
