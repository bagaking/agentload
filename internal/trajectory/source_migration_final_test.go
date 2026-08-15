package trajectory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"agentload/internal/historyfile"
)

func completedMigrationFixture(t *testing.T) (*Service, []*sourceState, [][]sourceFact, string, *countingDecoder) {
	t.Helper()
	_, first, facts, path := gappedSourceMigrationFixture(t)
	stable, middle := replayFixture(t, "codex", CodexDecoder{}, request("stable task")+call("pair")+result("pair"))
	last, final := replayFixture(t, "codex", CodexDecoder{}, request("another live task"))
	states := []*sourceState{first, middle, final}
	all := [][]sourceFact{facts, canonicalFixtureFacts(t, stable, middle), canonicalFixtureFacts(t, last, final)}
	f, err := openFactStore(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.write(context.Background(), func(w *factWriter) error {
		for i, row := range []int64{19, 43} {
			st := states[i+1]
			inserted, e := w.source(st.ID, st.Agent, st.checkpoint)
			if e != nil {
				return e
			}
			if _, e = w.tx.Exec("UPDATE sources SET rowid=? WHERE rowid=?", row, inserted); e != nil {
				return e
			}
			for _, fact := range all[i+1] {
				if _, e = w.event(row, fact.event, true); e != nil {
					return e
				}
			}
		}
		return nil
	})
	f.db.Close()
	if err != nil {
		t.Fatal(err)
	}
	d := &countingDecoder{}
	srcs := []Source{first.Source, middle.Source, final.Source}
	srcs[1].Decoder = d
	provider := func(context.Context) SourceSet {
		return SourceSet{Sources: srcs, CatalogComplete: true, Coverage: coverage("completed source fixture")}
	}
	m := NewPersistent(provider, path)
	t.Cleanup(func() { _ = m.Close() })
	for n := 0; n < 300; n++ {
		if e := m.migrateSourceStore(context.Background()); !errors.Is(e, errStorageMigration) {
			t.Fatal(e)
		}
		state, _, e := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
		if e != nil {
			t.Fatal(e)
		}
		if state.Phase == "ready" {
			d.calls.Store(0)
			return m, states, all, path, d
		}
	}
	t.Fatal("fixture did not reach sealed publication boundary")
	return nil, nil, nil, "", nil
}

func stableMigrationRows(t *testing.T, d *sql.DB) [32]byte {
	t.Helper()
	h := sha256.New()
	for _, q := range []string{
		"SELECT json_array(rowid,id,generation,active,agent,mtime,missing,hex(checkpoint),complete,search_count,search_offset,search_block,search_entity_gaps,verified_size,verified_mtime,audit_size,audit_mtime,audit_after) FROM sources WHERE rowid=19",
		"SELECT json_array(rowid,source,start,end,hex(body),hex(filter)) FROM ranges WHERE source=19 ORDER BY rowid",
		"SELECT json_array(rowid,source,offset,block,hex(body)) FROM exceptions WHERE source=19 ORDER BY rowid",
	} {
		rows, e := d.Query(q)
		if e != nil {
			t.Fatal(e)
		}
		for rows.Next() {
			var v string
			if e = rows.Scan(&v); e != nil {
				t.Fatal(e)
			}
			h.Write([]byte(v + "\n"))
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

func TestTrajectorySourceFinalRepairPreservesOtherCompletedSources(t *testing.T) {
	m, states, facts, path, decoder := completedMigrationFixture(t)
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	shadow := m.sourceMigration.shadow
	stable := stableMigrationRows(t, shadow.db)
	for _, st := range []*sourceState{states[0], states[2]} {
		f, e := os.OpenFile(st.Path, os.O_APPEND|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		_, e = f.WriteString(request("appended after source completion"))
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			t.Fatal(e, closeErr)
		}
	}
	if e = m.migrateSourceStore(context.Background()); !errors.Is(e, errStorageMigration) {
		t.Fatal(e)
	}
	committed, _, e := sourceMigrationState(context.Background(), shadow)
	if e != nil || committed.Source != 7 || committed.Mode != "stored" || committed.Records != int64(len(facts[1])+len(facts[2])) {
		t.Fatal("final source change restarted verified corpus", committed, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = m.migrateSourceStore(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	capacity := shadow.checkCapacity
	shadow.checkCapacity = func(string, uint64) error { return historyfile.ErrStorageBudget }
	if e = m.migrateSourceStore(context.Background()); !errors.Is(e, historyfile.ErrStorageBudget) {
		t.Fatal(e)
	}
	current, _, e := sourceMigrationState(context.Background(), shadow)
	if e != nil || !reflect.DeepEqual(current, committed) {
		t.Fatal("failed repair changed acknowledged cursor", current, e)
	}
	shadow.checkCapacity = capacity
	if got, e := os.ReadFile(path); e != nil || !bytes.Equal(original, got) {
		t.Fatal("interrupted repair changed canonical original", e)
	}
	if e = m.Close(); e != nil {
		t.Fatal(e)
	}
	m = NewPersistent(m.provider, path)
	defer m.Close()
	finishSourceFixtureMigration(t, m)
	if decoder.calls.Load() != 0 || stableMigrationRows(t, m.store.db) != stable {
		t.Fatal("final repair decoded or rewrote an unchanged source", decoder.calls.Load())
	}
	verifyStoredFixtureFacts(t, m, states[0], facts[0])
	for _, fact := range facts[2] {
		var body []byte
		if e = m.store.db.QueryRow("SELECT body FROM exceptions WHERE source=43 AND offset=? AND block=?", fact.offset, fact.block).Scan(&body); e != nil {
			t.Fatal(e)
		}
		raw, e := decodeSourceValue(body, maxSourceRangeLogicalBytes)
		var got sourceException
		if e != nil || json.Unmarshal(raw, &got) != nil || !reflect.DeepEqual(got.Event, &fact.event) {
			t.Fatal("second source lost canonical DTO", e)
		}
	}
	if after, e := os.ReadFile(states[0].Path); e != nil || !bytes.Contains(after, []byte("appended after source completion")) {
		t.Fatal("migration changed live source", e)
	}
	// The canonical old input is retained until complete source facts and the
	// final seal are verified; retirement occurs only through normal cutover.
	for _, suffix := range []string{".source-migrating", ".source-legacy", ".source-ready.json"} {
		if _, e := os.Stat(path + suffix); !os.IsNotExist(e) {
			t.Fatal("recovery structure survived successful cutover", suffix, e)
		}
	}
}

func TestTrajectorySourceFinalCorruptCheckpointIsNotSourceRepair(t *testing.T) {
	for _, field := range []string{"generation", "identity", "prefix"} {
		t.Run(field, func(t *testing.T) { finalCorruptCheckpoint(t, field) })
	}
}

func finalCorruptCheckpoint(t *testing.T, field string) {
	t.Helper()
	m, _, _, path, _ := completedMigrationFixture(t)
	shadow := m.sourceMigration.shadow
	before, _, e := sourceMigrationState(context.Background(), shadow)
	if e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var body []byte
	if e = shadow.db.QueryRow("SELECT checkpoint FROM sources WHERE rowid=7").Scan(&body); e != nil {
		t.Fatal(e)
	}
	raw, e := decodeSourceValue(body, maxSourceRangeLogicalBytes)
	if e != nil {
		t.Fatal(e)
	}
	var cp sourceCheckpoint
	if e = json.Unmarshal(raw, &cp); e != nil {
		t.Fatal(e)
	}
	switch field {
	case "generation":
		cp.Generation = "corrupt"
	case "identity":
		cp.Identity = "corrupt"
	case "prefix":
		if len(cp.Prefix) == 0 {
			t.Fatal("fixture lacks prefix")
		}
		cp.Prefix[0] ^= 1
	}
	raw, _ = json.Marshal(cp)
	body, e = encodeSourceValue(raw)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = shadow.db.Exec("UPDATE sources SET checkpoint=? WHERE rowid=7", body); e != nil {
		t.Fatal(e)
	}
	if e = m.validateMigrationSources(context.Background(), shadow); !errors.Is(e, ErrStale) {
		t.Fatal("damaged checkpoint passed owner validation", e)
	}
	if e = m.migrateSourceStore(context.Background()); e == nil || errors.Is(e, errStorageMigration) {
		t.Fatal("damaged proof became source repair", e)
	}
	after, _, e := sourceMigrationState(context.Background(), shadow)
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("damaged proof changed cursor", e)
	}
	if got, e := os.ReadFile(path); e != nil || !bytes.Equal(original, got) {
		t.Fatal("damaged proof changed canonical original", e)
	}
}

func TestTrajectorySourceReadySealAndCatalogPause(t *testing.T) {
	for _, format := range []string{"sqlite", "bbolt"} {
		for _, change := range []string{"unrelated_tamper_and_append", "catalog_pending"} {
			t.Run(format+"/"+change, func(t *testing.T) {
				var m *Service
				var raw, path string
				var originals []string
				if format == "sqlite" {
					var states []*sourceState
					m, states, _, path, _ = completedMigrationFixture(t)
					raw, originals = states[0].Path, []string{path}
				} else {
					f := makeFactMigrationFixture(t, 4, false)
					m, _ = directReadyFixture(t, f)
					path, raw, originals = f.path, f.source, []string{f.legacy, f.search}
				}
				defer m.Close()
				oldHashes := map[string][32]byte{}
				for _, p := range originals {
					b, e := os.ReadFile(p)
					if e != nil {
						t.Fatal(e)
					}
					oldHashes[p] = sha256.Sum256(b)
				}
				seal, e := os.ReadFile(path + ".source-ready.json")
				if e != nil {
					t.Fatal(e)
				}
				shadow := m.sourceMigration.shadow
				before, _, e := sourceMigrationState(context.Background(), shadow)
				if e != nil {
					t.Fatal(e)
				}
				provider := m.provider
				if change == "catalog_pending" {
					m.provider = func(context.Context) SourceSet {
						c := coverage("catalog still preparing")
						c.Complete, c.Gaps = false, []string{"index_pending"}
						return SourceSet{CatalogComplete: false, Coverage: c}
					}
				} else {
					// Keep cardinality and checkpoint valid, but damage a different
					// opaque fact before a legitimate raw append requests repair.
					var key []byte
					if e = shadow.db.QueryRow("SELECT path FROM metadata ORDER BY path LIMIT 1").Scan(&key); e != nil {
						t.Fatal(e)
					}
					if _, e = shadow.db.Exec("UPDATE metadata SET body=? WHERE path=?", []byte("damaged unrelated opaque evidence"), key); e != nil {
						t.Fatal(e)
					}
					f, e := os.OpenFile(raw, os.O_WRONLY|os.O_APPEND, 0600)
					if e != nil {
						t.Fatal(e)
					}
					_, e = f.WriteString(request("legitimate live append"))
					closeErr := f.Close()
					if e != nil || closeErr != nil {
						t.Fatal(e, closeErr)
					}
				}
				body, e := os.ReadFile(shadow.path)
				if e != nil {
					t.Fatal(e)
				}
				shadowHash := sha256.Sum256(body)
				e = m.openIndex()
				if change == "catalog_pending" {
					if !errors.Is(e, errStorageMigration) {
						t.Fatal("pending catalog did not pause", e)
					}
				} else if e == nil || errors.Is(e, errStorageMigration) {
					t.Fatal("source append resealed damaged unrelated facts", e)
				}
				current, _, e := sourceMigrationState(context.Background(), shadow)
				if e != nil || !reflect.DeepEqual(before, current) {
					t.Fatal("unverified attempt changed acknowledged cursor", e)
				}
				if b, e := os.ReadFile(shadow.path); e != nil || sha256.Sum256(b) != shadowHash {
					t.Fatal("unverified attempt wrote target", e)
				}
				if b, e := os.ReadFile(path + ".source-ready.json"); e != nil || !bytes.Equal(b, seal) {
					t.Fatal("unverified attempt discarded authoritative seal", e)
				}
				for p, digest := range oldHashes {
					if b, e := os.ReadFile(p); e != nil || sha256.Sum256(b) != digest {
						t.Fatal("unverified attempt changed original", p, e)
					}
				}
				if change == "catalog_pending" {
					m.provider = provider
					if format == "sqlite" {
						finishSourceFixtureMigration(t, m)
					} else {
						finishDirectMigration(t, m)
					}
				}
			})
		}
	}
}
