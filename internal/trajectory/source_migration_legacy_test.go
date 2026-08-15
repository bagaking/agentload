package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func finishDirectMigration(t *testing.T, s *Service) {
	t.Helper()
	for n := 0; n < 2000; n++ {
		err := s.openIndex()
		if err == nil {
			return
		}
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
	}
	t.Fatal("direct migration did not converge")
}

func directReadyFixture(t *testing.T, f migrationFixture) (*Service, sourceMigration) {
	t.Helper()
	s := migrationService(f)
	for n := 0; n < 2000; n++ {
		if err := s.migrateDirectLegacy(context.Background()); !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err := sourceMigrationState(context.Background(), s.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == "ready" {
			return s, state
		}
	}
	t.Fatal("direct migration did not reach ready")
	return nil, sourceMigration{}
}

func TestTrajectorySourceDirectLegacyAllCodecs(t *testing.T) {
	for codec := 0; codec <= 5; codec++ {
		t.Run(fmt.Sprint(codec), func(t *testing.T) {
			f := makeFactMigrationFixture(t, codec, codec%2 == 0)
			s := migrationService(f)
			before, _ := os.ReadFile(f.legacy)
			searchBefore, _ := os.ReadFile(f.search)
			if err := s.migrateDirectLegacy(context.Background()); !errors.Is(err, errStorageMigration) {
				t.Fatal(err)
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(f.legacy)
			searchAfter, _ := os.ReadFile(f.search)
			if !bytes.Equal(before, after) || !bytes.Equal(searchBefore, searchAfter) {
				t.Fatal("batch mutated originals")
			}
			s = migrationService(f)
			defer s.Close()
			finishDirectMigration(t, s)
			cp, ok, err := s.store.checkpoint(context.Background(), f.events[0].Source.ID)
			if err != nil || !ok || !reflect.DeepEqual(cp, f.cp) {
				t.Fatal("checkpoint changed", err)
			}
			states, _ := s.collect(context.Background())
			got := []snapshot.TrajectoryEvent{}
			if err = s.store.walk(context.Background(), states[0], func(e snapshot.TrajectoryEvent, _ int) error { got = append(got, e); return nil }); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, f.events) {
				t.Fatal("complete canonical DTOs changed")
			}
			q := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "bagakit-researcher", Count: true})
			if len(q.Sessions) != 1 || q.Sessions[0].MatchedCount != 120 || q.MatchedTotal == nil || *q.MatchedTotal != 1 {
				t.Fatal("exact query changed", q)
			}
			if q.Sessions[0].MatchedIDs[0] != f.events[0].ID {
				t.Fatal("public match identity changed")
			}
			raw, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: f.events[0].ID, View: "raw"})
			if err != nil || raw.RawChunk == nil {
				t.Fatal("raw source evidence unavailable", err)
			}
			body, err := base64.StdEncoding.DecodeString(raw.RawChunk.Data)
			originalBody, e := os.ReadFile(f.source)
			if err != nil || e != nil || !bytes.Equal(body, originalBody[:f.events[0].Source.Length]) {
				t.Fatal("raw evidence changed", err, e)
			}
			for _, p := range []string{f.legacy, f.search, f.path + ".migrating", f.path + ".source-migrating"} {
				if _, err = os.Stat(p); !os.IsNotExist(err) {
					t.Fatal("retired/intermediate layout remains", p, err)
				}
			}
			for p, want := range f.protected {
				body, err := os.ReadFile(p)
				if err != nil || sha256.Sum256(body) != want {
					t.Fatal("useful source/history/notes changed", p, err)
				}
			}
		})
	}
}

func TestTrajectorySourceDirectLegacyTwoSourcesOpaqueRecovery(t *testing.T) {
	f := makeFactMigrationFixture(t, 5, false)
	db, err := bolt.Open(f.legacy, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	const id = "!withdrawn-source"
	var retained []snapshot.TrajectoryEvent
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket(sourceBucket).CreateBucket([]byte(id))
		if err != nil {
			return err
		}
		events, err := b.CreateBucket(eventBucket)
		if err != nil {
			return err
		}
		cp := sourceCheckpoint{Version: projectionVersion, Generation: "retained", EventCount: 70, Offset: 700, Line: 70, Coverage: coverage("retained evidence")}
		raw, _ := json.Marshal(cp)
		raw = append(raw[:len(raw)-1], []byte(`,"future_recovery":{"keep":true}}`)...)
		frame, err := encodeStored(raw)
		if err != nil {
			return err
		}
		if err = tx.Bucket(checkpointBucket).Put([]byte(id), frame); err != nil {
			return err
		}
		for n := 0; n < 70; n++ {
			e := snapshot.TrajectoryEvent{ID: fmt.Sprintf("original-id-%d", n), SessionID: "original-session", Role: "assistant", Kind: "text", Text: fmt.Sprintf("irreproducible knowledge %d", n), Source: snapshot.TrajectorySourceRef{ID: id, Generation: "retained", Offset: int64(n * 10)}}
			if n == 10 {
				e.Context = &snapshot.TrajectoryContextEvidence{NativeID: "original-context", NativeField: "future/context"}
			}
			body, err := encodeEventStored(e)
			if err != nil {
				return err
			}
			if err = events.Put(eventKey(e.Source.Offset, 0), body); err != nil {
				return err
			}
			retained = append(retained, e)
		}
		extra, err := tx.Bucket([]byte("recovery-evidence")).CreateBucket([]byte("unknown-nested"))
		if err != nil {
			return err
		}
		if err = extra.SetSequence(47); err != nil {
			return err
		}
		if _, err = extra.CreateBucket([]byte("empty")); err != nil {
			return err
		}
		return extra.Put([]byte("opaque"), []byte{0, 1, 2, 3})
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s := migrationService(f)
	defer s.Close()
	// Stop at the source-copy boundary, write one real stored batch, then
	// retain the pre-batch cursor to exercise a crash before its durable update.
	var state sourceMigration
	for n := 0; n < 2000; n++ {
		if err = s.migrateDirectLegacy(context.Background()); !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err = sourceMigrationState(context.Background(), s.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == "sources" {
			break
		}
	}
	if state.Phase != "sources" {
		t.Fatal("missing source boundary")
	}
	input, err := s.sourceMigration.inputSource(context.Background(), 1)
	if err != nil || input.id != id {
		t.Fatal("unstable source catalog", err)
	}
	raw, err := decodeStored(input.body)
	if err != nil {
		t.Fatal(err)
	}
	var cp sourceCheckpoint
	if err = json.Unmarshal(raw, &cp); err != nil {
		t.Fatal(err)
	}
	state.Source, state.Mode = 1, "stored"
	if err = ensureMigrationSource(context.Background(), s.sourceMigration, state, 1, input.id, input.generation, input.agent, input.active, input.missing, input.body, cp, input.count, input.offset, input.block); err != nil {
		t.Fatal(err)
	}
	committed := state
	done, err := migrateStoredSource(context.Background(), s.sourceMigration, &committed, 1, input.id, input.agent, input.active, input.missing, cp, input.count, input.offset, input.block)
	if err != nil || done || committed.Records != 64 {
		t.Fatal("fixture did not commit first stored batch", done, err)
	}
	if err = saveSourceMigration(context.Background(), s.sourceMigration.shadow, state); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = migrationService(f)
	defer s.Close()
	finishDirectMigration(t, s)
	var row int64
	if err = s.store.db.QueryRow("SELECT rowid FROM sources WHERE id=?", id).Scan(&row); err != nil {
		t.Fatal(err)
	}
	for _, want := range retained {
		var frame []byte
		if err = s.store.db.QueryRow("SELECT body FROM exceptions WHERE source=? AND offset=? AND block=0", row, want.Source.Offset).Scan(&frame); err != nil {
			t.Fatal(err)
		}
		raw, e := decodeSourceValue(frame, maxSourceRangeLogicalBytes)
		if e != nil {
			t.Fatal(e)
		}
		var saved sourceException
		if err = json.Unmarshal(raw, &saved); err != nil || saved.Event == nil || !reflect.DeepEqual(*saved.Event, want) {
			t.Fatal("complete retained DTO differs", err)
		}
	}
	var seq string
	var body []byte
	var bucket int
	path := metadataPath([][]byte{[]byte("recovery-evidence"), []byte("unknown-nested")})
	if err = s.store.db.QueryRow("SELECT sequence,body,bucket FROM metadata WHERE path=?", path).Scan(&seq, &body, &bucket); err != nil || seq != "47" || body != nil || bucket != 1 {
		t.Fatal("unknown bucket sequence lost", err)
	}
	q := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "irreproducible"})
	if len(q.Sessions) != 0 {
		t.Fatal("withdrawn evidence became authorized")
	}
}

func TestTrajectorySourceDirectLegacyRetirementCrashAndTamper(t *testing.T) {
	for _, mode := range []string{"after_intent", "after_first", "unrecorded_absence", "target_tamper"} {
		t.Run(mode, func(t *testing.T) {
			f := makeFactMigrationFixture(t, 5, false)
			s, state := directReadyFixture(t, f)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(f.path+".source-migrating", f.path); err != nil {
				t.Fatal(err)
			}
			seal, err := readSourceSeal(f.path)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "after_intent":
				seal.Retiring = "search.sqlite"
				if err = saveSourceSeal(f.path, seal); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(f.search); err != nil {
					t.Fatal(err)
				}
			case "after_first":
				seal.Retired = []string{"search.sqlite"}
				if err = saveSourceSeal(f.path, seal); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(f.search); err != nil {
					t.Fatal(err)
				}
			case "unrecorded_absence":
				if err = os.Remove(f.legacy); err != nil {
					t.Fatal(err)
				}
			case "target_tamper":
				fstore, e := openSourceStore(context.Background(), f.path)
				if e != nil {
					t.Fatal(e)
				}
				_, err = fstore.db.Exec("UPDATE metadata SET body=? WHERE bucket=0", []byte("corrupt after seal"))
				fstore.db.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			s = migrationService(f)
			defer s.Close()
			err = s.openIndex()
			if mode == "target_tamper" || mode == "unrecorded_absence" {
				if err == nil {
					t.Fatal("unsafe retirement accepted")
				}
				if _, e := os.Stat(f.search); e != nil {
					t.Fatal("remaining original lost", e)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, e := os.Stat(f.search); !os.IsNotExist(e) {
					t.Fatal("retirement failed to resume", e)
				}
				st, _, e := sourceMigrationState(context.Background(), s.store)
				if e != nil || st.Records != state.Records {
					t.Fatal("verified identity changed", e)
				}
			}
		})
	}
}

func TestTrajectorySourceDirectLegacyTamperCapacityAndCancellation(t *testing.T) {
	for _, mode := range []string{"shadow", "source", "search", "pair_before", "search_before", "space", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			f := makeFactMigrationFixture(t, 5, false)
			mutateOriginal := func() {
				if mode == "search" || mode == "search_before" {
					db, e := sql.Open("sqlite", searchDatabaseDSN(f.search))
					if e != nil {
						t.Fatal(e)
					}
					_, e = db.Exec("UPDATE docs_storage SET search_text=?", []byte("different evidence"))
					db.Close()
					if e != nil {
						t.Fatal(e)
					}
				} else {
					db, e := bolt.Open(f.legacy, 0600, nil)
					if e != nil {
						t.Fatal(e)
					}
					e = db.Update(func(tx *bolt.Tx) error {
						if mode == "pair_before" {
							return tx.Bucket(sourceBucket).Bucket([]byte(f.events[0].Source.ID)).Bucket(pairBucket).Put([]byte("ambiguous"), []byte(`{"calls":["wrong"],"results":[]}`))
						}
						return tx.Bucket([]byte("recovery-evidence")).Put([]byte("changed"), []byte("new recovery evidence"))
					})
					db.Close()
					if e != nil {
						t.Fatal(e)
					}
				}
			}
			if mode == "pair_before" || mode == "search_before" {
				mutateOriginal()
			}
			s := migrationService(f)
			defer s.Close()
			if mode == "space" {
				s.capacityCheck = func(_ string, additional uint64) error {
					if additional < 256*1024*1024 {
						t.Fatal("missing small-shadow peak reserve")
					}
					return errors.New("fixture capacity blocked")
				}
				if err := s.openIndex(); err == nil {
					t.Fatal("unsafe copy started")
				}
			} else if mode == "cancel" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := s.openIndexContext(ctx); !errors.Is(err, context.Canceled) {
					t.Fatal("cancel ignored", err)
				}
			} else {
				for n := 0; n < 2000; n++ {
					if err := s.migrateDirectLegacy(context.Background()); !errors.Is(err, errStorageMigration) {
						t.Fatal(err)
					}
					state, _, e := sourceMigrationState(context.Background(), s.sourceMigration.shadow)
					if e != nil {
						t.Fatal(e)
					}
					if state.Phase == "sources" {
						break
					}
				}
				if mode == "shadow" {
					if _, e := s.sourceMigration.shadow.db.Exec("UPDATE metadata SET body=? WHERE path=?", []byte("changed"), metadataPath([][]byte{[]byte("recovery-evidence"), []byte("resume-note")})); e != nil {
						t.Fatal(e)
					}
				} else if mode == "source" || mode == "search" {
					if e := s.Close(); e != nil {
						t.Fatal(e)
					}
					mutateOriginal()
					s = migrationService(f)
					defer s.Close()
				}
				before, _ := os.ReadFile(f.legacy)
				searchBefore, _ := os.ReadFile(f.search)
				rejected := false
				for n := 0; n < 2000; n++ {
					err := s.openIndex()
					if err == nil {
						t.Fatal("mismatched migration installed")
					}
					if !errors.Is(err, errStorageMigration) {
						rejected = true
						break
					}
				}
				if !rejected {
					t.Fatal("mismatch did not surface")
				}
				after, _ := os.ReadFile(f.legacy)
				searchAfter, _ := os.ReadFile(f.search)
				if !bytes.Equal(before, after) || !bytes.Equal(searchBefore, searchAfter) {
					t.Fatal("failure changed originals")
				}
			}
			if mode == "space" || mode == "cancel" {
				if _, e := os.Stat(f.path + ".source-migrating"); !os.IsNotExist(e) {
					t.Fatal("shadow created before gate", e)
				}
			}
		})
	}
}

func TestTrajectorySourceDirectLegacyStaleSearchRecovery(t *testing.T) {
	for _, missing := range []bool{false, true} {
		t.Run(fmt.Sprint(missing), func(t *testing.T) {
			f := makeFactMigrationFixture(t, 5, false)
			id := f.events[0].Source.ID
			if missing {
				db, e := bolt.Open(f.legacy, 0600, nil)
				if e != nil {
					t.Fatal(e)
				}
				e = db.Update(func(tx *bolt.Tx) error {
					b := tx.Bucket(sourceBucket).Bucket([]byte(id))
					if e := b.DeleteBucket(eventBucket); e != nil {
						return e
					}
					if e := b.DeleteBucket(pairBucket); e != nil {
						return e
					}
					return putJSON(tx.Bucket(checkpointBucket), []byte(id), sourceCheckpoint{Version: 1, Missing: true, Generation: f.cp.Generation, Coverage: coverage("missing")})
				})
				db.Close()
				if e != nil {
					t.Fatal(e)
				}
			} else {
				db, e := sql.Open("sqlite", searchDatabaseDSN(f.search))
				if e != nil {
					t.Fatal(e)
				}
				_, e = db.Exec("UPDATE sources SET indexed_count=indexed_count+1")
				db.Close()
				if e != nil {
					t.Fatal(e)
				}
			}
			r, e := openFactMigrationReader(f.path)
			if e != nil {
				t.Fatal(e)
			}
			p := r.progress[id]
			if r.eligible[id] {
				t.Fatal("stale prefix eligible")
			}
			r.close()
			s := migrationService(f)
			defer s.Close()
			finishDirectMigration(t, s)
			if e = verifyLegacyProgressDB(context.Background(), s.store.db, map[string]searchProgress{id: p}); e != nil {
				t.Fatal(e)
			}
			var count, block int
			var offset int64
			if e = s.store.db.QueryRow("SELECT search_count,search_offset,search_block FROM sources WHERE id=? AND active=1", id).Scan(&count, &offset, &block); e != nil || count != 0 || offset != -1 || block != -1 {
				t.Fatal("stale readiness advertised", e)
			}
			cp, ok, e := s.store.checkpoint(context.Background(), id)
			if e != nil || !ok || cp.Missing != missing {
				t.Fatal("checkpoint missing truth changed", e)
			}
			if !missing {
				q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "bagakit-researcher"})
				if len(q.Sessions) != 1 || q.Sessions[0].MatchedCount != 120 || q.Sessions[0].MatchedIDs[0] != f.events[0].ID {
					t.Fatal("stale repair changed facts", q)
				}
			}
		})
	}
}

func TestTrajectorySourceDirectLegacyReadyRestartAndCancellation(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		t.Run(fmt.Sprint(tamper), func(t *testing.T) {
			f := makeFactMigrationFixture(t, 5, false)
			s, state := directReadyFixture(t, f)
			original, _ := os.ReadFile(f.legacy)
			searchOriginal, _ := os.ReadFile(f.search)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if e := s.installDirectLegacy(ctx, state); !errors.Is(e, context.Canceled) {
				t.Fatal("cancelled cutover proceeded", e)
			}
			if _, e := os.Stat(f.path); !os.IsNotExist(e) {
				t.Fatal("cancel published target", e)
			}
			if tamper {
				if _, e := s.sourceMigration.shadow.db.Exec("UPDATE metadata SET body=? WHERE path=?", []byte("tampered after ready"), metadataPath([][]byte{[]byte("recovery-evidence"), []byte("resume-note")})); e != nil {
					t.Fatal(e)
				}
			}
			if e := s.Close(); e != nil {
				t.Fatal(e)
			}
			s = migrationService(f)
			defer s.Close()
			if tamper {
				if e := s.openIndex(); e == nil || errors.Is(e, errStorageMigration) {
					t.Fatal("tampered ready shadow not rejected", e)
				}
				got, _ := os.ReadFile(f.legacy)
				searchGot, _ := os.ReadFile(f.search)
				if !bytes.Equal(got, original) || !bytes.Equal(searchGot, searchOriginal) {
					t.Fatal("rejected cutover changed originals")
				}
			} else {
				finishDirectMigration(t, s)
			}
			if _, e := os.Stat(filepath.Join(filepath.Dir(f.path), "trajectory.sqlite.migrating")); !os.IsNotExist(e) {
				t.Fatal("whole fact intermediate created", e)
			}
		})
	}
}
