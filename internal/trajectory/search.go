package trajectory

import (
	"agentload/internal/snapshot"
	"database/sql"
	"encoding/json"
	"strings"
	"unicode"
)

// searchIndex is a lifecycle descriptor for acceleration in the canonical store.
// The owning factStore closes the connection; no second content database exists.
type searchIndex struct {
	db       *sql.DB
	path     string
	revision uint64
	scope    string
}

const priorSearchSchemaVersion = 300 + projectionVersion
const searchSchemaVersion = 400 + projectionVersion
const searchBatchEvents = 256

// SQLite tokenizes the whole batch and updates positional postings. Keep this
// smaller than the canonical decoder chunk so query traffic can interleave
// with backfill without a long write/rollback occupying the lifecycle lock.
const searchBatchBytes = 256 * 1024

const searchSchema = `
CREATE TABLE sources (
 id TEXT PRIMARY KEY, generation TEXT NOT NULL, agent TEXT NOT NULL,
 mtime INTEGER NOT NULL, indexed_count INTEGER NOT NULL DEFAULT 0,
 last_offset INTEGER NOT NULL DEFAULT -1, last_block INTEGER NOT NULL DEFAULT -1
);
CREATE TABLE docs_storage (
 rowid INTEGER PRIMARY KEY, event_id TEXT NOT NULL UNIQUE,
 source_id TEXT NOT NULL, generation TEXT NOT NULL, offset INTEGER NOT NULL,
 block INTEGER NOT NULL, kind TEXT NOT NULL, role TEXT NOT NULL,
 actor_id TEXT NOT NULL, actor_kind TEXT NOT NULL, tool_fold TEXT NOT NULL,
	entities BLOB NOT NULL, entity_complete INTEGER NOT NULL,
	fts_unsafe INTEGER NOT NULL, search_text BLOB NOT NULL
);
CREATE INDEX docs_source ON docs_storage(source_id,offset,block);
CREATE INDEX docs_role ON docs_storage(role);
CREATE INDEX docs_kind ON docs_storage(kind);
CREATE INDEX docs_tool ON docs_storage(tool_fold);
CREATE INDEX docs_unsafe ON docs_storage(fts_unsafe);
CREATE INDEX docs_match ON docs_storage(rowid,source_id,generation,fts_unsafe);
CREATE VIEW docs AS SELECT rowid,traj_text(search_text) AS search_text FROM docs_storage;
CREATE VIRTUAL TABLE text_fts USING fts5(search_text,content='docs',content_rowid='rowid',tokenize='trigram case_sensitive 1',detail='none');
CREATE TRIGGER docs_insert AFTER INSERT ON docs_storage BEGIN
 INSERT INTO text_fts(rowid,search_text) VALUES(new.rowid,traj_text(new.search_text));
END;
CREATE TRIGGER docs_delete AFTER DELETE ON docs_storage BEGIN
 INSERT INTO text_fts(text_fts,rowid,search_text) VALUES('delete',old.rowid,traj_text(old.search_text));
END;
CREATE TABLE search_meta (id INTEGER PRIMARY KEY CHECK(id=1), revision INTEGER NOT NULL);
INSERT INTO search_meta VALUES(1,0);`

type searchProgress struct {
	generation string
	count      int
	offset     int64
	block      int
	mtime      int64
	agent      string
}

func searchFold(value string) string {
	return strings.Map(func(r rune) rune {
		smallest := r
		for next := unicode.SimpleFold(r); next != r; next = unicode.SimpleFold(next) {
			if next < smallest {
				smallest = next
			}
		}
		return unicode.ToLower(smallest)
	}, value)
}

type searchEntity struct {
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Predicate string `json:"predicate"`
	LabelFold string `json:"label_fold"`
	Literal   string `json:"literal"`
}

func searchProjection(e snapshot.TrajectoryEvent) (string, string, string, int) {
	text := strings.ToLower(e.Text)
	tool := ""
	if e.Tool != nil {
		text += " " + strings.ToLower(e.Tool.Name+" "+string(e.Tool.Arguments))
		tool = searchFold(e.Tool.Name)
	}
	entities := make([]searchEntity, 0, len(e.Entities))
	for _, o := range e.Entities {
		entities = append(entities, searchEntity{o.Kind, o.EntityID, o.Predicate, searchFold(o.Label), o.Literal})
	}
	encoded, _ := json.Marshal(entities)
	complete := 1
	if e.EntityCoverage != nil && !e.EntityCoverage.Complete {
		complete = 0
	}
	return text, tool, string(encoded), complete
}
func searchGlobLiteral(text string) string {
	return strings.NewReplacer("[", "[[]", "*", "[*]", "?", "[?]").Replace(text)
}

func searchTrigrams(text string) string {
	runes := []rune(text)
	terms := []string{}
	seen := map[string]bool{}
	for i := 0; i+3 <= len(runes); i++ {
		gram := string(runes[i : i+3])
		if !seen[gram] {
			seen[gram] = true
			terms = append(terms, `"`+strings.ReplaceAll(gram, `"`, `""`)+`"`)
		}
	}
	return strings.Join(terms, " AND ")
}

type searchHit struct {
	factRef
	id    string
	count int
}
