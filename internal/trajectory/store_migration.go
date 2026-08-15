package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"
)

// A single shadow owns copy and verification progress. Originals are read-only
// until the complete canonical and search boundaries have been checked.
type factMigration struct {
	Version         int      `json:"version"`
	SourceTx        int      `json:"source_tx"`
	SourceIdentity  string   `json:"source_identity"`
	SearchIdentity  string   `json:"search_identity"`
	SearchRevision  uint64   `json:"search_revision"`
	Phase           string   `json:"phase"`
	After           [][]byte `json:"after,omitempty"`
	Hash            []byte   `json:"hash,omitempty"`
	Expected        []byte   `json:"expected,omitempty"`
	Records         int64    `json:"records"`
	ExpectedSources int64    `json:"expected_sources"`
	ExpectedRecords int64    `json:"expected_records,omitempty"`
	Events          int64    `json:"events"`
	Metadata        int64    `json:"metadata"`
	Pairs           int64    `json:"pairs"`
	TextDocs        int64    `json:"text_docs"`
	Occurrences     int64    `json:"occurrences"`
	SearchAfter     int64    `json:"search_after"`
	SearchRows      int64    `json:"search_rows"`
	BytesBefore     int64    `json:"bytes_before"`
}
type factMigrationReader struct {
	canonical *bolt.DB
	search    *sql.DB
	progress  map[string]searchProgress
	eligible  map[string]bool
	state     factMigration
}

func migrationFileIdentity(path string) (string, int64, error) {
	st, err := os.Stat(path)
	if os.IsNotExist(err) {
		return "absent", 0, nil
	}
	if err != nil {
		return "", 0, err
	}
	if !st.Mode().IsRegular() {
		return "", 0, errors.New("migration source is not regular; preserved")
	}
	return fmt.Sprintf("%s:%d:%d", statIdentity(st), st.Size(), st.ModTime().UnixNano()), st.Size(), nil
}
func openFactMigrationReader(path string) (*factMigrationReader, error) {
	root := filepath.Dir(path)
	original := filepath.Join(root, "index.bbolt")
	identity, size, err := migrationFileIdentity(original)
	if err != nil {
		return nil, err
	}
	if identity == "absent" {
		return nil, os.ErrNotExist
	}
	db, err := bolt.Open(original, 0600, &bolt.Options{ReadOnly: true, Timeout: 200 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	r := &factMigrationReader{canonical: db, progress: map[string]searchProgress{}, state: factMigration{Version: factStoreVersion, SourceIdentity: identity, BytesBefore: size, Phase: "copy"}}
	fail := func(err error) (*factMigrationReader, error) { r.close(); return nil, err }
	if err = db.View(func(tx *bolt.Tx) error {
		r.state.SourceTx = tx.ID()
		ids := map[string]bool{}
		if sources := tx.Bucket(sourceBucket); sources != nil {
			if err := sources.ForEach(func(k, v []byte) error {
				if v == nil {
					ids[string(k)] = true
				}
				return nil
			}); err != nil {
				return err
			}
		}
		if cps := tx.Bucket(checkpointBucket); cps != nil {
			if err := cps.ForEach(func(k, v []byte) error {
				if len(k) > 0 && k[0] != '_' && v != nil {
					ids[string(k)] = true
				}
				return nil
			}); err != nil {
				return err
			}
		}
		r.state.ExpectedSources = int64(len(ids))
		if tx.Bucket(sourceBucket) == nil {
			return errors.New("unrecognized canonical trajectory index; preserved")
		}
		return nil
	}); err != nil {
		return fail(err)
	}
	searchPath := filepath.Join(root, "search.sqlite")
	r.state.SearchIdentity, size, err = migrationFileIdentity(searchPath)
	if err != nil {
		return fail(err)
	}
	r.state.BytesBefore += size
	if r.state.SearchIdentity != "absent" {
		r.search, err = sql.Open("sqlite", "file:"+searchPath+"?mode=ro")
		if err != nil {
			return fail(err)
		}
		r.search.SetMaxOpenConns(1)
		var version int
		if err = r.search.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
			return fail(err)
		}
		if version != searchSchemaVersion && version != priorSearchSchemaVersion {
			return fail(errors.New("unrecognized legacy search schema; preserved"))
		}
		if err = r.search.QueryRow("SELECT revision FROM search_meta WHERE id=1").Scan(&r.state.SearchRevision); err != nil {
			return fail(err)
		}
		rows, err := r.search.Query("SELECT id,generation,indexed_count,last_offset,last_block,mtime,agent FROM sources")
		if err != nil {
			return fail(err)
		}
		for rows.Next() {
			var id string
			var p searchProgress
			if err = rows.Scan(&id, &p.generation, &p.count, &p.offset, &p.block, &p.mtime, &p.agent); err != nil {
				break
			}
			r.progress[id] = p
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return fail(err)
		}
	}
	r.eligible = map[string]bool{}
	if err = r.canonical.View(func(tx *bolt.Tx) error {
		for id, p := range r.progress {
			c, err := r.checkpoint(tx, []byte(id))
			if err != nil {
				continue
			}
			r.eligible[id] = !c.Missing && c.Generation == p.generation && p.count >= 0 && p.count <= c.EventCount
		}
		return nil
	}); err != nil {
		return fail(err)
	}

	return r, nil
}
func (r *factMigrationReader) close() {
	if r.search != nil {
		r.search.Close()
	}
	if r.canonical != nil {
		r.canonical.Close()
	}
}
func (r *factMigrationReader) checkpoint(tx *bolt.Tx, id []byte) (sourceCheckpoint, error) {
	var c sourceCheckpoint
	var body []byte
	if b := tx.Bucket(checkpointBucket); b != nil {
		body = b.Get(id)
	}
	if body == nil {
		if b := tx.Bucket(sourceBucket).Bucket(id); b != nil {
			body = b.Get(metaKey)
		}
	}
	if body == nil {
		return c, errors.New("source checkpoint absent; original preserved")
	}
	raw, err := decodeStored(body)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal(raw, &c)
	return c, err
}
func (r *factMigrationReader) ready(e snapshot.TrajectoryEvent) bool {
	p, ok := r.progress[e.Source.ID]
	return ok && r.eligible[e.Source.ID] && p.generation == e.Source.Generation && p.count > 0 && (e.Source.Offset < p.offset || e.Source.Offset == p.offset && e.Source.Block <= p.block)
}
func migrationPairEntry(e storageEntry) bool {
	return len(e.path) == 4 && bytes.Equal(e.path[0], sourceBucket) && bytes.Equal(e.path[2], pairBucket) && e.value != nil
}
func metadataPath(path [][]byte) []byte { raw, _ := json.Marshal(path); return raw }
func verifyFactStore(ctx context.Context, f *factStore) error {
	var result string
	if err := f.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("trajectory integrity: %s", result)
	}
	rows, err := f.db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	bad := rows.Next()
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if bad {
		return errors.New("trajectory foreign key check failed")
	}
	for _, table := range []string{"text_fts", "selector_fts"} {
		if _, err = f.db.ExecContext(ctx, "INSERT INTO "+table+"("+table+") VALUES('integrity-check')"); err != nil {
			return err
		}
	}
	var badRefs bool
	// Aggregate both reference tables once. A correlated count per block would
	// rescan every event for every block in a large archive.
	if err = f.db.QueryRowContext(ctx, `WITH refs AS (
		SELECT block,COUNT(*) AS n FROM (
			SELECT block FROM event_facts UNION ALL SELECT block FROM contents
		) GROUP BY block
	) SELECT EXISTS(SELECT 1 FROM blocks b LEFT JOIN refs r ON r.block=b.rowid
		WHERE b.refs<>COALESCE(r.n,0))`).Scan(&badRefs); err != nil {
		return err
	}
	if badRefs {
		return errors.New("trajectory block references differ")
	}
	return nil
}

type legacySearchProgress struct {
	Generation string `json:"generation"`
	Count      int    `json:"count"`
	Offset     int64  `json:"offset"`
	Block      int    `json:"block"`
	Mtime      int64  `json:"mtime"`
	Agent      string `json:"agent"`
}

func marshalLegacyProgress(p searchProgress) ([]byte, error) {
	return json.Marshal(legacySearchProgress{p.generation, p.count, p.offset, p.block, p.mtime, p.agent})
}
func legacyProgressPath(id string) []byte {
	return metadataPath([][]byte{[]byte("legacy-search-progress"), []byte(id)})
}
func verifyLegacyProgress(ctx context.Context, f *factStore, progress map[string]searchProgress) error {
	return verifyLegacyProgressDB(ctx, f.db, progress)
}
func verifyLegacyProgressDB(ctx context.Context, db *sql.DB, progress map[string]searchProgress) error {
	for id, p := range progress {
		var got []byte
		want, err := marshalLegacyProgress(p)
		if err != nil {
			return err
		}
		if err = db.QueryRowContext(ctx, "SELECT body FROM metadata WHERE path=?", legacyProgressPath(id)).Scan(&got); err != nil {
			return err
		}
		if !bytes.Equal(want, got) {
			return errors.New("legacy search recovery progress differs; preserved")
		}
	}
	return nil
}
