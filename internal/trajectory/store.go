package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"database/sql"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// factStore owns canonical facts and the disposable search acceleration in one
// database. Content is stored once, separately from metadata so verification of
// a text candidate does not decode entities, contexts or usage counters.
type factStore struct {
	db         *sql.DB
	path       string
	authorized bool
}

const factStoreVersion = 1
const factSchema = `
CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL) WITHOUT ROWID;
INSERT INTO meta VALUES('revision','0');
CREATE TABLE metadata(path BLOB PRIMARY KEY,sequence TEXT NOT NULL,body BLOB,bucket INTEGER NOT NULL) WITHOUT ROWID;
CREATE TABLE symbols(id INTEGER PRIMARY KEY,value TEXT NOT NULL UNIQUE,refs INTEGER NOT NULL DEFAULT 0);
INSERT INTO symbols VALUES(0,'',0);
CREATE INDEX symbols_unreferenced ON symbols(id) WHERE refs=0;
CREATE TABLE sources(rowid INTEGER PRIMARY KEY,id TEXT NOT NULL,generation TEXT NOT NULL,active INTEGER NOT NULL DEFAULT 1,agent INTEGER NOT NULL,mtime INTEGER NOT NULL,missing INTEGER NOT NULL DEFAULT 0,checkpoint BLOB NOT NULL,search_count INTEGER NOT NULL DEFAULT 0,search_offset INTEGER NOT NULL DEFAULT -1,search_block INTEGER NOT NULL DEFAULT -1);
CREATE UNIQUE INDEX source_active ON sources(id) WHERE active=1;
CREATE INDEX sources_retired ON sources(rowid) WHERE active=0 OR missing=1;
CREATE TABLE events(rowid INTEGER PRIMARY KEY,source INTEGER NOT NULL REFERENCES sources(rowid) ON DELETE CASCADE,offset INTEGER NOT NULL,block INTEGER NOT NULL,
 kind INTEGER NOT NULL,role INTEGER NOT NULL,actor INTEGER NOT NULL,actor_kind INTEGER NOT NULL,tool INTEGER NOT NULL,tool_fold INTEGER NOT NULL,call INTEGER NOT NULL,
 has_tool INTEGER NOT NULL,search_ready INTEGER NOT NULL DEFAULT 0,entity_complete INTEGER NOT NULL,fts_unsafe INTEGER NOT NULL,logical_size INTEGER NOT NULL,digest TEXT NOT NULL,id_override TEXT,UNIQUE(source,offset,block));
CREATE TABLE blocks(rowid INTEGER PRIMARY KEY,kind INTEGER NOT NULL,refs INTEGER NOT NULL DEFAULT 0,body BLOB NOT NULL);
CREATE TABLE event_facts(rowid INTEGER PRIMARY KEY REFERENCES events(rowid) ON DELETE CASCADE,block INTEGER NOT NULL REFERENCES blocks(rowid),offset INTEGER NOT NULL,length INTEGER NOT NULL);
CREATE TRIGGER facts_delete AFTER DELETE ON event_facts BEGIN UPDATE blocks SET refs=refs-1 WHERE rowid=old.block; DELETE FROM blocks WHERE rowid=old.block AND refs=0; END;
CREATE INDEX events_kind ON events(kind,rowid);
CREATE INDEX events_role ON events(role,rowid);
CREATE INDEX events_tool ON events(tool_fold,rowid) WHERE tool_fold<>0;
CREATE INDEX events_actor ON events(actor,actor_kind,rowid) WHERE actor<>0 OR actor_kind<>0;
CREATE INDEX events_unsafe ON events(rowid) WHERE fts_unsafe=1;
CREATE INDEX events_search_scope ON events(rowid,source) WHERE search_ready=1;
CREATE TABLE contents(rowid INTEGER PRIMARY KEY REFERENCES events(rowid) ON DELETE CASCADE,block INTEGER NOT NULL REFERENCES blocks(rowid),offset INTEGER NOT NULL,length INTEGER NOT NULL);
CREATE TRIGGER contents_delete AFTER DELETE ON contents BEGIN UPDATE blocks SET refs=refs-1 WHERE rowid=old.block; DELETE FROM blocks WHERE rowid=old.block AND refs=0; END;
CREATE TABLE occurrence_index(rowid INTEGER PRIMARY KEY,event INTEGER NOT NULL REFERENCES events(rowid) ON DELETE CASCADE);
CREATE INDEX occurrence_event ON occurrence_index(event);
CREATE VIRTUAL TABLE selector_fts USING fts5(selector,content='',contentless_delete=1,tokenize='ascii',detail='none');
CREATE TRIGGER occurrence_delete AFTER DELETE ON occurrence_index BEGIN DELETE FROM selector_fts WHERE rowid=old.rowid; END;
CREATE TABLE pairs(source INTEGER NOT NULL REFERENCES sources(rowid) ON DELETE CASCADE,call INTEGER NOT NULL,call_count INTEGER NOT NULL DEFAULT 0,result_count INTEGER NOT NULL DEFAULT 0,call1 INTEGER REFERENCES events(rowid) ON DELETE SET NULL,call2 INTEGER REFERENCES events(rowid) ON DELETE SET NULL,result1 INTEGER REFERENCES events(rowid) ON DELETE SET NULL,result2 INTEGER REFERENCES events(rowid) ON DELETE SET NULL,PRIMARY KEY(source,call)) WITHOUT ROWID;
CREATE INDEX pairs_call ON pairs(call);
CREATE VIRTUAL TABLE text_fts USING fts5(text,content='',contentless_delete=1,tokenize='trigram case_sensitive 1',detail='none');
CREATE TRIGGER events_delete AFTER DELETE ON events BEGIN
 DELETE FROM text_fts WHERE rowid=old.rowid;
 UPDATE symbols SET refs=refs-(id=old.kind)-(id=old.role)-(id=old.actor)-(id=old.actor_kind)-(id=old.tool)-(id=old.tool_fold)-(id=old.call) WHERE id<>0 AND id IN(old.kind,old.role,old.actor,old.actor_kind,old.tool,old.tool_fold,old.call);
END;
`

func openFactStore(path string) (*factStore, error) {
	return openFactStoreContext(context.Background(), path)
}

func openFactStoreContext(ctx context.Context, path string) (*factStore, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: path}
	pragmas := url.Values{}
	for _, v := range []string{"busy_timeout(200)", "temp_store(MEMORY)", "foreign_keys(ON)", "cache_size(-8192)"} {
		pragmas.Add("_pragma", v)
	}
	uri.RawQuery = pragmas.Encode()
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	fail := func(e error) (*factStore, error) { _ = db.Close(); return nil, e }
	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys=ON; PRAGMA page_size=4096; PRAGMA journal_mode=DELETE; PRAGMA cache_size=-8192;"); err != nil {
		return fail(err)
	}
	var version int
	if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version == 0 {
		var count int
		if err = db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master").Scan(&count); err != nil {
			return fail(err)
		}
		if count != 0 {
			return fail(errors.New("unrecognized trajectory fact store; preserved"))
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return fail(e)
		}
		if _, err = tx.ExecContext(ctx, factSchema+fmt.Sprintf("PRAGMA user_version=%d;", factStoreVersion)); err == nil {
			err = commitSearchTransaction(tx, db)
		} else {
			_ = tx.Rollback()
		}
		if err != nil {
			return fail(err)
		}
	} else if version != factStoreVersion {
		return fail(errors.New("unrecognized trajectory fact store version; preserved"))
	}
	if _, err = db.ExecContext(ctx, "CREATE INDEX IF NOT EXISTS events_unsafe ON events(rowid) WHERE fts_unsafe=1"); err != nil {
		return fail(err)
	}
	var scopeIndex bool
	if err = db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type='index' AND name='events_search_scope')").Scan(&scopeIndex); err != nil {
		return fail(err)
	}
	if !scopeIndex {
		var events uint64
		if err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&events); err != nil {
			return fail(err)
		}
		// Covering keys are two integers. Reserve both their conservative page
		// footprint and the native sort, plus journal/VM headroom. SQLite's atomic
		// DDL rolls back on cancellation; no canonical record or cursor is changed.
		if events > (^uint64(0)-(2<<30)-(128<<20))/64 {
			return fail(errors.New("trajectory scope index exceeds capacity bound"))
		}
		if err = historyfile.CheckStorageCapacity(path, events*64+(2<<30)+(128<<20)); err != nil {
			return fail(err)
		}
		if _, err = db.ExecContext(ctx, "PRAGMA temp_store=FILE; CREATE INDEX events_search_scope ON events(rowid,source) WHERE search_ready=1; PRAGMA temp_store=MEMORY"); err != nil {
			return fail(err)
		}
	}
	return &factStore{db: db, path: path}, nil
}

type factWriter struct {
	tx                         *sql.Tx
	ids                        map[string]int64
	symbolInsert, symbolLookup *sql.Stmt
	blocks                     [2]*factBlock
	symbolCounts               map[int64]int64
	scopeChanged               bool
}

func (w *factWriter) symbol(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if id, ok := w.ids[value]; ok {
		return id, nil
	}
	if _, err := w.symbolInsert.Exec(value); err != nil {
		return 0, err
	}
	var id int64
	if err := w.symbolLookup.QueryRow(value).Scan(&id); err != nil {
		return 0, err
	}
	if len(w.ids) < 4096 && len(value) <= 512 {
		w.ids[value] = id
	}
	return id, nil
}
func (f *factStore) write(ctx context.Context, apply func(*factWriter) error) error {
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	insert, err := tx.Prepare("INSERT OR IGNORE INTO symbols(value) VALUES(?)")
	if err != nil {
		return err
	}
	defer insert.Close()
	lookup, err := tx.Prepare("SELECT id FROM symbols WHERE value=?")
	if err != nil {
		return err
	}
	defer lookup.Close()
	w := &factWriter{tx: tx, ids: map[string]int64{}, symbolInsert: insert, symbolLookup: lookup, symbolCounts: map[int64]int64{}}
	if err = apply(w); err != nil {
		return err
	}
	if err = w.flushBlocks(); err != nil {
		return err
	}
	if err = w.flushSymbolCounts(); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE meta SET value=CAST(value AS INTEGER)+1 WHERE key='revision'"); err != nil {
		return err
	}
	err = commitSearchTransaction(tx, f.db)
	if err != nil || w.scopeChanged {
		f.authorized = false
	}
	return err
}
func (w *factWriter) source(id, agent string, c sourceCheckpoint) (int64, error) {
	key, err := w.symbol(agent)
	if err != nil {
		return 0, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	body, err := encodeStored(raw)
	if err != nil {
		return 0, err
	}
	var row int64
	var prior string
	err = w.tx.QueryRow("SELECT rowid,generation FROM sources WHERE id=? AND active=1", id).Scan(&row, &prior)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	if err == nil && prior == c.Generation {
		_, err = w.tx.Exec("UPDATE sources SET agent=?,mtime=?,missing=?,checkpoint=? WHERE rowid=?", key, c.Mtime, c.Missing, body, row)
		return row, err
	}
	// Keep the old generation immutable and inaccessible while bounded cleanup
	// reclaims its pages. An event can never inherit a different generation.
	if _, err = w.tx.Exec("UPDATE sources SET active=0 WHERE id=? AND active=1", id); err != nil {
		return 0, err
	}
	w.scopeChanged = true
	result, err := w.tx.Exec("INSERT INTO sources(id,generation,agent,mtime,missing,checkpoint) VALUES(?,?,?,?,?,?)", id, c.Generation, key, c.Mtime, c.Missing, body)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
func (f *factStore) checkpoint(ctx context.Context, id string) (sourceCheckpoint, bool, error) {
	var c sourceCheckpoint
	var raw []byte
	err := f.db.QueryRowContext(ctx, "SELECT checkpoint FROM sources WHERE id=? AND active=1", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	body, err := decodeStored(raw)
	if err == nil {
		err = json.Unmarshal(body, &c)
	}
	return c, err == nil, err
}

// Two lengths distinguish the original text and raw argument bytes. The tool
// name is an interned fact. There is no lower-cased second body in the store.
func factContentBytes(text string, args []byte) ([]byte, error) {
	if len(text)+len(args) > maxStoredValue-8 {
		return nil, errors.New("trajectory content exceeds storage bound")
	}
	raw := make([]byte, 8+len(text)+len(args))
	binary.BigEndian.PutUint32(raw[:4], uint32(len(text)))
	binary.BigEndian.PutUint32(raw[4:8], uint32(len(args)))
	copy(raw[8:], text)
	copy(raw[8+len(text):], args)
	return raw, nil
}
func parseFactContent(raw []byte) (string, json.RawMessage, error) {
	if len(raw) < 8 {
		return "", nil, errors.New("invalid trajectory content frame")
	}
	a, b := int(binary.BigEndian.Uint32(raw[:4])), int(binary.BigEndian.Uint32(raw[4:8]))
	if a+b != len(raw)-8 {
		return "", nil, errors.New("trajectory content length mismatch")
	}
	var args json.RawMessage
	if b > 0 {
		args = bytes.Clone(raw[8+a:])
	}
	return string(raw[8 : 8+a]), args, nil
}
func factSearchText(text, name string, args []byte, tool bool) string {
	result := strings.ToLower(text)
	if tool {
		result += " " + strings.ToLower(name+" "+string(args))
	}
	return result
}

func (w *factWriter) event(source int64, e snapshot.TrajectoryEvent, searchable bool) (int64, error) {
	var parent, generation string
	if err := w.tx.QueryRow("SELECT id,generation FROM sources WHERE rowid=? AND active=1", source).Scan(&parent, &generation); err != nil {
		return 0, err
	}
	if parent != e.Source.ID || generation != e.Source.Generation {
		return 0, ErrStale
	}
	logical, err := json.Marshal(e)
	if err != nil {
		return 0, err
	}
	if len(logical) > maxStoredValue {
		return 0, errors.New("trajectory event exceeds storage bound")
	}
	// Match the existing normalized JSON evidence contract (including RawMessage escaping).
	if err = json.Unmarshal(logical, &e); err != nil {
		return 0, err
	}
	values := []string{e.Kind, e.Role, e.Actor.ID, e.Actor.Kind, "", "", ""}
	if e.Tool != nil {
		values[4] = e.Tool.Name
		values[5] = searchFold(e.Tool.Name)
		values[6] = e.Tool.CallID
	}
	dims := make([]int64, len(values))
	for i, v := range values {
		dims[i], err = w.symbol(v)
		if err != nil {
			return 0, err
		}
	}
	for _, id := range dims {
		if id != 0 {
			w.symbolCounts[id]++
		}
	}
	stored := storedEvent{TrajectoryEvent: e, ID: storedOverride(e.ID, sourceEventID(e.Source)), SessionID: storedOverride(e.SessionID, "s."+e.Source.ID+"."+e.Source.Generation)}
	stored.TrajectoryEvent.ID = ""
	stored.TrajectoryEvent.SessionID = ""
	stored.Source.ID = ""
	stored.Source.Generation = ""
	stored.Source.Offset = 0
	stored.Source.Block = 0
	stored.Source.Digest = ""
	stored.Kind = ""
	stored.Role = ""
	stored.Actor = snapshot.TrajectoryActor{}
	stored.Text = ""
	stored.TrajectoryEvent.Entities = nil
	for _, o := range e.Entities {
		item := storedOccurrence{ID: storedOverride(o.ID, occurrenceID(e.ID, o.EntityID, o.Predicate, o.NativeField)), EntityID: storedOverride(o.EntityID, occurrenceEntityID(o.Kind, o.Scope, o.Literal)), EventID: storedOverride(o.EventID, e.ID), SessionID: storedOverride(o.SessionID, e.SessionID), Kind: o.Kind, Literal: o.Literal, Label: storedOverride(o.Label, o.Literal), Scope: storedOverride(o.Scope, "session:"+e.SessionID), Predicate: o.Predicate, NativeField: o.NativeField}
		if o.Source != e.Source {
			copy := o.Source
			item.Source = &copy
		}
		stored.Entities = append(stored.Entities, item)
	}
	var args []byte
	if e.Tool != nil {
		copy := *e.Tool
		args = copy.Arguments
		copy.Name = ""
		copy.CallID = ""
		copy.Arguments = nil
		stored.Tool = &copy
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return 0, err
	}
	facts := raw
	text := factSearchText(e.Text, values[4], args, e.Tool != nil)
	unsafe := 0
	if strings.ContainsRune(text, 0) {
		unsafe = 1
	}
	complete := 1
	if e.EntityCoverage != nil && !e.EntityCoverage.Complete {
		complete = 0
	}
	result, err := w.tx.Exec(`INSERT INTO events(source,offset,block,kind,role,actor,actor_kind,tool,tool_fold,call,has_tool,search_ready,entity_complete,fts_unsafe,logical_size,digest,id_override) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, source, e.Source.Offset, e.Source.Block, dims[0], dims[1], dims[2], dims[3], dims[4], dims[5], dims[6], e.Tool != nil, false, complete, unsafe, len(logical), e.Source.Digest, stored.ID)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	factBlock, factOffset, err := w.appendBlock(0, facts)
	if err != nil {
		return 0, err
	}
	if _, err = w.tx.Exec("INSERT INTO event_facts VALUES(?,?,?,?)", id, factBlock, factOffset, len(facts)); err != nil {
		return 0, err
	}
	if e.Text != "" || len(args) > 0 {
		content, err := factContentBytes(e.Text, args)
		if err != nil {
			return 0, err
		}
		block, offset, err := w.appendBlock(1, content)
		if err != nil {
			return 0, err
		}
		if _, err = w.tx.Exec("INSERT INTO contents VALUES(?,?,?,?)", id, block, offset, len(content)); err != nil {
			return 0, err
		}
	}
	if searchable {
		if err = w.project(id, e); err != nil {
			return 0, err
		}
	}
	if e.Tool != nil && e.Tool.CallID != "" && len(e.Tool.CallID) <= 512 && (e.Kind == "tool_call" || e.Kind == "tool_result") {
		if _, err = w.tx.Exec("INSERT OR IGNORE INTO pairs(source,call) VALUES(?,?)", source, dims[6]); err != nil {
			return 0, err
		}
		first, second, count := "call1", "call2", "call_count"
		if e.Kind == "tool_result" {
			first, second, count = "result1", "result2", "result_count"
		}
		if _, err = w.tx.Exec("UPDATE pairs SET "+second+"=CASE WHEN "+count+"=1 THEN ? ELSE "+second+" END,"+first+"=CASE WHEN "+count+"=0 THEN ? ELSE "+first+" END,"+count+"=min(2,"+count+"+1) WHERE source=? AND call=?", id, id, source, dims[6]); err != nil {
			return 0, err
		}
	}
	return id, nil
}

// The tiny fact row and its optional body are read once. Entity occurrences
// retain their order, evidence overrides and unknown fields on hydration.
func (f *factStore) event(ctx context.Context, source string, offset int64, block int) (snapshot.TrajectoryEvent, error) {
	return f.readEvent(ctx, source, offset, block, false)
}
func (f *factStore) readEvent(ctx context.Context, source string, offset int64, block int, allowMissing bool) (snapshot.TrajectoryEvent, error) {
	return f.readEventCached(ctx, source, offset, block, allowMissing, nil)
}

func (f *factStore) readEventCached(ctx context.Context, source string, offset int64, block int, allowMissing bool, cache *factBlockReadCache) (snapshot.TrajectoryEvent, error) {
	return f.readEventRow(ctx, source, offset, block, allowMissing, cache, 0)
}
func (f *factStore) readEventRow(ctx context.Context, source string, offset int64, block int, allowMissing bool, cache *factBlockReadCache, eventRow int64) (snapshot.TrajectoryEvent, error) {
	var e snapshot.TrajectoryEvent
	var fact, body []byte
	var factOffset, factLength int
	var bodyOffset, bodyLength sql.NullInt64
	var generation, kind, role, actor, actorKind, name, call, digest string
	var row int64
	query := `SELECT e.rowid,fb.body,ef.offset,ef.length,cb.body,c.offset,c.length,s.generation,e.digest,k.value,r.value,a.value,ak.value,t.value,ca.value FROM events e JOIN event_facts ef ON ef.rowid=e.rowid JOIN blocks fb ON fb.rowid=ef.block JOIN sources s ON s.rowid=e.source LEFT JOIN contents c ON c.rowid=e.rowid LEFT JOIN blocks cb ON cb.rowid=c.block JOIN symbols k ON k.id=e.kind JOIN symbols r ON r.id=e.role JOIN symbols a ON a.id=e.actor JOIN symbols ak ON ak.id=e.actor_kind JOIN symbols t ON t.id=e.tool JOIN symbols ca ON ca.id=e.call`
	sqlArgs := []any{source, allowMissing, offset, block}
	if eventRow > 0 {
		query += " WHERE e.rowid=? AND s.id=?"
		sqlArgs = []any{eventRow, source}
	} else {
		query += " WHERE s.id=? AND s.active=1 AND (s.missing=0 OR ?) AND e.offset=? AND e.block=?"
	}
	err := f.db.QueryRowContext(ctx, query, sqlArgs...).Scan(&row, &fact, &factOffset, &factLength, &body, &bodyOffset, &bodyLength, &generation, &digest, &kind, &role, &actor, &actorKind, &name, &call)
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	if err != nil {
		return e, err
	}
	raw, err := cache.slice(ctx, fact, factOffset, factLength)
	if err != nil {
		return e, err
	}
	var stored storedEvent
	if err = json.Unmarshal(raw, &stored); err != nil {
		return e, err
	}
	e = stored.TrajectoryEvent
	e.Source.ID = source
	e.Source.Generation = generation
	e.Source.Offset = offset
	e.Source.Block = block
	e.Source.Digest = digest
	e.ID = storedString(stored.ID, sourceEventID(e.Source))
	e.SessionID = storedString(stored.SessionID, "s."+source+"."+generation)
	e.Kind = kind
	e.Role = role
	e.Actor = snapshot.TrajectoryActor{ID: actor, Kind: actorKind}
	var args json.RawMessage
	if len(body) > 0 {
		var raw []byte
		raw, err = cache.slice(ctx, body, int(bodyOffset.Int64), int(bodyLength.Int64))
		if err == nil {
			e.Text, args, err = parseFactContent(raw)
		}
		if err != nil {
			return e, err
		}
	}
	if e.Tool != nil {
		e.Tool.Name = name
		e.Tool.CallID = call
		e.Tool.Arguments = args
	}
	e.Entities = nil
	for _, item := range stored.Entities {
		scope := storedString(item.Scope, "session:"+e.SessionID)
		identity := storedString(item.EntityID, occurrenceEntityID(item.Kind, scope, item.Literal))
		o := snapshot.TrajectoryEntityOccurrence{ID: storedString(item.ID, occurrenceID(e.ID, identity, item.Predicate, item.NativeField)), EntityID: identity, EventID: storedString(item.EventID, e.ID), SessionID: storedString(item.SessionID, e.SessionID), Kind: item.Kind, Literal: item.Literal, Label: storedString(item.Label, item.Literal), Scope: scope, Predicate: item.Predicate, NativeField: item.NativeField, Source: e.Source}
		if item.Source != nil {
			o.Source = *item.Source
		}
		e.Entities = append(e.Entities, o)
	}
	return e, nil
}

// Reversible ASCII tokens make selectors exact without a second entity JSON
// or a row for every interned literal. One FTS document is one occurrence, so
// conjunctions cannot accidentally combine evidence from different occurrences.
func entitySelectorToken(category, value string) string {
	return category + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString([]byte(value)))
}
func entitySelectorTokens(o snapshot.TrajectoryEntityOccurrence) string {
	return strings.Join([]string{entitySelectorToken("k", o.Kind), entitySelectorToken("i", o.EntityID), entitySelectorToken("p", o.Predicate), entitySelectorToken("l", searchFold(o.Label)), entitySelectorToken("v", o.Literal)}, " ")
}

func encodeFactContent(text string, args []byte) ([]byte, error) {
	raw, err := factContentBytes(text, args)
	if err != nil {
		return nil, err
	}
	return encodeTextStored(string(raw))
}
func decodeFactContent(body []byte) (string, json.RawMessage, error) {
	raw, err := decodeStored(body)
	if err != nil {
		return "", nil, err
	}
	return parseFactContent(raw)
}

func (w *factWriter) flushSymbolCounts() error {
	stmt, err := w.tx.Prepare("UPDATE symbols SET refs=refs+? WHERE id=?")
	if err != nil {
		return err
	}
	defer stmt.Close()
	for id, count := range w.symbolCounts {
		if _, err = stmt.Exec(count, id); err != nil {
			return err
		}
	}
	return nil
}

func (f *factStore) checkpointIn(tx *sql.Tx, id string) (sourceCheckpoint, bool, error) {
	var c sourceCheckpoint
	var raw []byte
	err := tx.QueryRow("SELECT checkpoint FROM sources WHERE id=? AND active=1", id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return c, false, nil
	}
	if err != nil {
		return c, false, err
	}
	raw, err = decodeStored(raw)
	if err == nil {
		err = json.Unmarshal(raw, &c)
	}
	return c, true, err
}
func (f *factStore) checkpoints(ctx context.Context) (result map[string]sourceCheckpoint, err error) {
	decoded := map[string]sourceCheckpoint{}
	rows, err := f.db.QueryContext(ctx, "SELECT id,checkpoint FROM sources WHERE active=1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type checkpointRow struct {
		id   string
		body []byte
	}
	// Checkpoints are independent immutable values in this read snapshot. Decode
	// a bounded number concurrently; keep SQL iteration on its owning connection.
	const workers = 8
	jobs := make(chan checkpointRow, workers)
	var readers sync.WaitGroup
	var resultMu sync.Mutex
	for range workers {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for row := range jobs {
				if ctx.Err() != nil {
					continue
				}
				raw, err := decodeStored(row.body)
				if err != nil {
					continue
				}
				var c sourceCheckpoint
				if json.Unmarshal(raw, &c) == nil {
					resultMu.Lock()
					decoded[row.id] = c
					resultMu.Unlock()
				}
			}
		}()
	}
	defer func() {
		close(jobs)
		readers.Wait()
		if ctx.Err() != nil {
			result, err = nil, ctx.Err()
		}
	}()
	for rows.Next() {
		var id string
		var body []byte
		if err = rows.Scan(&id, &body); err != nil {
			return nil, err
		}
		select {
		case jobs <- checkpointRow{id, body}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return decoded, rows.Err()
}

// project changes only acceleration and its durable readiness boundary. Empty
// text is still a decoded, searchable event and advances the same cursor.
func (w *factWriter) project(id int64, e snapshot.TrajectoryEvent) error {
	var ready bool
	if err := w.tx.QueryRow("SELECT search_ready FROM events WHERE rowid=?", id).Scan(&ready); err != nil {
		return err
	}
	if ready {
		return nil
	}
	text, _, _, _ := searchProjection(e)
	if text != "" {
		if _, err := w.tx.Exec("INSERT INTO text_fts(rowid,text) VALUES(?,?)", id, text); err != nil {
			return err
		}
	}
	for _, o := range e.Entities {
		result, err := w.tx.Exec("INSERT INTO occurrence_index(event) VALUES(?)", id)
		if err != nil {
			return err
		}
		occ, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = w.tx.Exec("INSERT INTO selector_fts(rowid,selector) VALUES(?,?)", occ, entitySelectorTokens(o)); err != nil {
			return err
		}
	}
	if _, err := w.tx.Exec("UPDATE events SET search_ready=1 WHERE rowid=?", id); err != nil {
		return err
	}
	_, err := w.tx.Exec("UPDATE sources SET search_count=search_count+1,search_offset=?,search_block=? WHERE rowid=(SELECT source FROM events WHERE rowid=?)", e.Source.Offset, e.Source.Block, id)
	return err
}
