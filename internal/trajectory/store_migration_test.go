package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

type migrationFixture struct {
	path, source, legacy, search string
	provider                     Provider
	cp                           sourceCheckpoint
	events                       []snapshot.TrajectoryEvent
	decoder                      *countingDecoder
	protected                    map[string][32]byte
}

func TestFactStoreVerificationCountsBothReferenceKinds(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	ctx := context.Background()
	err = f.write(ctx, func(w *factWriter) error {
		source, err := w.source("source", "codex", sourceCheckpoint{Generation: "gen"})
		if err != nil {
			return err
		}
		for i := 0; i < 32; i++ {
			e := snapshot.TrajectoryEvent{Kind: "text", Role: "user", Source: snapshot.TrajectorySourceRef{ID: "source", Generation: "gen", Offset: int64(i)}}
			if i%2 == 0 {
				e.Text = "shared body reference"
			}
			if _, err := w.event(source, e, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyFactStore(ctx, f); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []int{0, 1} {
		for _, delta := range []int{-1, 1} {
			if _, err = f.db.Exec("UPDATE blocks SET refs=refs+? WHERE kind=?", delta, kind); err != nil {
				t.Fatal(err)
			}
			if err = verifyFactStore(ctx, f); err == nil || !strings.Contains(err.Error(), "block references differ") {
				t.Fatalf("kind %d delta %d drift accepted: %v", kind, delta, err)
			}
			if _, err = f.db.Exec("UPDATE blocks SET refs=refs-? WHERE kind=?", delta, kind); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = f.db.Exec("INSERT INTO blocks(kind,refs,body) VALUES(0,0,x'')"); err != nil {
		t.Fatal(err)
	}
	if err = verifyFactStore(ctx, f); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE blocks SET refs=1 WHERE refs=0"); err != nil {
		t.Fatal(err)
	}
	if err = verifyFactStore(ctx, f); err == nil || !strings.Contains(err.Error(), "block references differ") {
		t.Fatalf("unreferenced block drift accepted: %v", err)
	}
}

func makeFactMigrationFixture(t *testing.T, codec int, nested bool) migrationFixture {
	t.Helper()
	var body strings.Builder
	for i := 0; i < 120; i++ {
		body.WriteString(request("bagakit-researcher 中文词 " + strings.Repeat("recorded trajectory source evidence ", 20)))
		if i%20 == 0 {
			body.WriteString(call(fmt.Sprint(i)))
			body.WriteString(result(fmt.Sprint(i)))
		}
	}
	body.WriteString(call("ambiguous"))
	body.WriteString(call("ambiguous"))
	body.WriteString(result("ambiguous"))
	s, source, path, decoder, _ := indexFixture(t, body.String())
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "bagakit-researcher"})
	src := s.provider(context.Background()).Sources[0]
	id := sourceID(src)
	cp, _, err := s.store.checkpoint(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	states, _ := s.collect(context.Background())
	var events []snapshot.TrajectoryEvent
	if err = s.store.walk(context.Background(), states[0], func(e snapshot.TrajectoryEvent, _ int) error { events = append(events, e); return nil }); err != nil {
		t.Fatal(err)
	}
	provider := s.provider
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(filepath.Dir(path), "index.bbolt")
	db, err := bolt.Open(legacy, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucket(sourceBucket)
		if err != nil {
			return err
		}
		b, err := root.CreateBucket([]byte(id))
		if err != nil {
			return err
		}
		if nested {
			if err = putJSON(b, metaKey, cp); err != nil {
				return err
			}
		} else {
			c, err := tx.CreateBucket(checkpointBucket)
			if err != nil {
				return err
			}
			if err = putJSON(c, []byte(id), cp); err != nil {
				return err
			}
			if err = c.Put(checkpointLayoutKey, []byte("1")); err != nil {
				return err
			}
			tombstone := sourceCheckpoint{Version: 1, Missing: true, Generation: "missing-generation", Coverage: coverage("missing source")}
			gap(&tombstone.Coverage, "source_missing")
			if err = putJSON(c, []byte("missing-source"), tombstone); err != nil {
				return err
			}
		}
		data, err := b.CreateBucket(eventBucket)
		if err != nil {
			return err
		}
		pairs, err := b.CreateBucket(pairBucket)
		if err != nil {
			return err
		}
		ledger := map[string]indexedPair{}
		for _, e := range events {
			full, _ := json.Marshal(e)
			compact, _ := encodeEventStored(e)
			compactJSON, err := decodeStored(compact)
			if err != nil {
				return err
			}
			var value []byte
			switch codec {
			case 0:
				value = full
			case 1:
				value = oldDictionarylessFrame(t, full, 1, len(full))
			case 2:
				value = oldDictionarylessFrame(t, compactJSON, 2, len(full))
			case 3:
				value = make([]byte, 12+len(compactJSON))
				copy(value, []byte{0, 'A', 'L', 3})
				binary.BigEndian.PutUint32(value[4:8], uint32(len(compactJSON)))
				binary.BigEndian.PutUint32(value[8:12], uint32(len(full)))
				copy(value[12:], compactJSON)
			case 4:
				value, err = encodeStored(full)
			case 5:
				value = compact
			}
			if err != nil {
				return err
			}
			if err = data.Put(eventKey(e.Source.Offset, e.Source.Block), value); err != nil {
				return err
			}
			if e.Tool != nil && e.Tool.CallID != "" {
				p := ledger[e.Tool.CallID]
				if e.Kind == "tool_call" && len(p.Calls) < 2 {
					p.Calls = append(p.Calls, e.ID)
				}
				if e.Kind == "tool_result" && len(p.Results) < 2 {
					p.Results = append(p.Results, e.ID)
				}
				ledger[e.Tool.CallID] = p
			}
		}
		for call, p := range ledger {
			if err = putJSON(pairs, []byte(call), p); err != nil {
				return err
			}
		}
		extra, err := tx.CreateBucket([]byte("recovery-evidence"))
		if err != nil {
			return err
		}
		if err = extra.SetSequence(19); err != nil {
			return err
		}
		return extra.Put([]byte("resume-note"), []byte("retain this useful metadata"))
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	search := filepath.Join(filepath.Dir(path), "search.sqlite")
	sqlDB, err := sql.Open("sqlite", searchDatabaseDSN(search))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sqlDB.Exec(searchSchema + fmt.Sprintf("PRAGMA user_version=%d", searchSchemaVersion)); err != nil {
		t.Fatal(err)
	}
	tx, err := sqlDB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if _, err = tx.Exec("INSERT INTO sources VALUES(?,?,?,?,?,?,?)", id, cp.Generation, "codex", cp.Mtime, len(events), last.Source.Offset, last.Source.Block); err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		text, tool, entities, complete := searchProjection(e)
		storedText, _ := encodeTextStored(text)
		storedEntities, _ := encodeTextStored(entities)
		if _, err = tx.Exec("INSERT INTO docs_storage(event_id,source_id,generation,offset,block,kind,role,actor_id,actor_kind,tool_fold,entities,entity_complete,fts_unsafe,search_text) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)", e.ID, id, cp.Generation, e.Source.Offset, e.Source.Block, e.Kind, e.Role, e.Actor.ID, e.Actor.Kind, tool, storedEntities, complete, 0, storedText); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec("UPDATE search_meta SET revision=37 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	sqlDB.Close()
	notes := filepath.Join(filepath.Dir(path), "annotations.bbolt")
	if err = os.WriteFile(notes, []byte("authored notes stay outside migration"), 0600); err != nil {
		t.Fatal(err)
	}
	out := migrationFixture{path: path, source: source, legacy: legacy, search: search, provider: provider, cp: cp, events: events, decoder: decoder, protected: map[string][32]byte{}}
	for _, p := range []string{source, notes} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out.protected[p] = sha256.Sum256(b)
	}
	decoder.calls.Store(0)
	return out
}
func migrationService(f migrationFixture) *Service {
	s := NewPersistent(f.provider, f.path)
	s.capacityCheck = func(string, uint64) error { return nil }
	return s
}
