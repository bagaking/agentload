package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

func legacySourceUpgradeFixture(t *testing.T, n int) (*sourceStore, []sourceException) {
	t.Helper()
	f := newSourceStoreFixture(t)
	_, err := f.db.Exec("DROP TABLE exceptions; CREATE TABLE exceptions(rowid INTEGER PRIMARY KEY,source INTEGER NOT NULL REFERENCES sources(rowid),offset INTEGER NOT NULL,block INTEGER NOT NULL,body BLOB NOT NULL,UNIQUE(source,offset,block));PRAGMA user_version=2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.db.Exec("INSERT INTO sources(rowid,id,generation,agent,mtime,checkpoint,missing) VALUES(1,'original','generation','unknown',0,?,1); INSERT INTO metadata(path,sequence,body,bucket) VALUES('private','71',NULL,1)", []byte("opaque checkpoint"))
	if err != nil {
		t.Fatal(err)
	}
	var facts []sourceException
	for i := 0; i < n; i++ {
		ref := snapshot.TrajectorySourceRef{ID: "original", Generation: "generation", Offset: int64(i * 100), Block: i % 3, Digest: "truth"}
		e := snapshot.TrajectoryEvent{ID: sourceEventID(ref), SessionID: "s.original.generation", Source: ref, Role: "assistant", Kind: "text", Text: strings.Repeat("same measured text ", 40)}
		if i%5 == 0 {
			e.ID = ""
			e.SessionID = "foreign session"
			e.Entities = []snapshot.TrajectoryEntityOccurrence{{ID: "foreign", EventID: "", SessionID: "", EntityID: "", Kind: "skill", Literal: "bagakit-researcher", Label: "", Scope: "", Predicate: "uses", NativeField: "text", Source: snapshot.TrajectorySourceRef{ID: "foreign", Offset: 7}}}
		}
		fact := sourceException{Offset: ref.Offset, Block: ref.Block, Event: &e}
		if i%11 == 0 {
			fact.Event = nil
		}
		raw, _ := json.Marshal(fact)
		body, err := encodeSourceValue(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Exec("INSERT INTO exceptions(source,offset,block,body) VALUES(1,?,?,?)", fact.Offset, fact.Block, body); err != nil {
			t.Fatal(err)
		}
		facts = append(facts, fact)
	}
	return f, facts
}

func TestSourceBlockUpgradeRestartsExactAndReclaims(t *testing.T) {
	f, want := legacySourceUpgradeFixture(t, 1100)
	// Interleave physical slots across old rowid batches. Point reads must remain
	// reachable when the original insertion order was unrelated to offsets.
	if _, err := f.db.Exec("UPDATE exceptions SET rowid=-rowid; UPDATE exceptions SET rowid=(-rowid*499)%1101"); err != nil {
		t.Fatal(err)
	}
	before, err := sourceUpgradeControlDigest(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	size, _ := os.Stat(f.path)
	if err = initializeSourceBlockUpgrade(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	state, _, err := readSourceBlockUpgrade(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if err = upgradeSourceBlockBatch(context.Background(), f, &state); err != nil {
		t.Fatal(err)
	}
	if state.Records == 0 || state.Records >= state.Total {
		t.Fatal("fixture did not stop between batches", state)
	}
	path := f.path
	f.db.Close()
	if current, e := openSourceStore(context.Background(), path); e == nil {
		current.db.Close()
		t.Fatal("public v2 reader admitted")
	}
	f, err = openMigrationSourceStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	state, _, err = readSourceBlockUpgrade(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	for state.Phase == "exception_blocks" {
		if err = upgradeSourceBlockBatch(context.Background(), f, &state); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 500; i++ {
		err = reclaimSourceBlockPages(context.Background(), f, state)
		if err == nil {
			break
		}
		if !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
	}
	if err != nil {
		t.Fatal("bounded page reclaim did not finish", err)
	}
	var count, blocks, version, free int
	if err = f.db.QueryRow("SELECT sum(records),count(*) FROM exceptions").Scan(&count, &blocks); err != nil || count != len(want) || blocks >= count {
		t.Fatal(count, blocks, err)
	}
	for _, old := range want {
		got, e := exceptionAt(context.Background(), f.db, 1, old.Offset, old.Block)
		if e != nil || !reflect.DeepEqual(old, got) {
			t.Fatal("complete fact changed", old.Offset, e)
		}
	}
	after, e := sourceUpgradeControlDigest(context.Background(), f)
	if e != nil || after != before {
		t.Fatal("opaque/control state changed", e)
	}
	_ = f.db.QueryRow("PRAGMA user_version").Scan(&version)
	_ = f.db.QueryRow("PRAGMA freelist_count").Scan(&free)
	end, _ := os.Stat(path)
	if version != 3 || free != 0 || end.Size() >= size.Size() {
		t.Fatal("physical reclaim incomplete", version, free, size.Size(), end.Size())
	}
	var receipt string
	if e = f.db.QueryRow("SELECT value FROM meta WHERE key='source-block-upgrade-result'").Scan(&receipt); e != nil {
		t.Fatal(e)
	}
	var proof sourceBlockUpgrade
	if e = json.Unmarshal([]byte(receipt), &proof); e != nil || proof.Records != int64(len(want)) || proof.Controls != before || len(proof.Digest) != 64 {
		t.Fatal("full upgrade proof missing", proof, e)
	}
}

func TestSourceBlockUpgradeRollbackAndCapacityPreserveProgress(t *testing.T) {
	f, want := legacySourceUpgradeFixture(t, 130)
	if e := initializeSourceBlockUpgrade(context.Background(), f); e != nil {
		t.Fatal(e)
	}
	state, _, _ := readSourceBlockUpgrade(context.Background(), f)
	before, _ := os.ReadFile(f.path)
	f.checkCapacity = func(string, uint64) error { return historyfile.ErrStorageBudget }
	if e := upgradeSourceBlockBatch(context.Background(), f, &state); !errors.Is(e, historyfile.ErrStorageBudget) {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(f.path)
	if !bytes.Equal(before, after) || state.Records != 0 {
		t.Fatal("capacity changed data/progress")
	}
	f.checkCapacity = historyfile.CheckStorageCapacity
	// The first block has already been updated and decoded when DELETE fails.
	// SQLite must roll back both it and the progress cursor.
	_, e := f.db.Exec("CREATE TEMP TRIGGER stop_delete BEFORE DELETE ON exceptions BEGIN SELECT RAISE(ABORT,'interrupted deletion'); END")
	if e != nil {
		t.Fatal(e)
	}
	if e = upgradeSourceBlockBatch(context.Background(), f, &state); e == nil {
		t.Fatal("fault not injected")
	}
	durable, _, e := readSourceBlockUpgrade(context.Background(), f)
	if e != nil || durable.Records != 0 || state.Records != 0 {
		t.Fatal("failed batch advanced progress", e)
	}
	var old int
	_ = f.db.QueryRow("SELECT count(*) FROM exceptions WHERE records=0").Scan(&old)
	if old != len(want) {
		t.Fatal("rollback lost old rows", old)
	}
	_, _ = f.db.Exec("DROP TRIGGER stop_delete")
	if e = upgradeSourceBlockBatch(context.Background(), f, &state); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e = upgradeSourceBlockBatch(ctx, f, &state); !errors.Is(e, context.Canceled) {
		t.Fatal("cancellation ignored", e)
	}
}

func TestSourceExceptionBlockOversizeAndLogicalBudget(t *testing.T) {
	e := snapshot.TrajectoryEvent{ID: "foreign", Text: strings.Repeat("raw evidence", 5000)}
	facts := []sourceException{{0, 0, &e}, {1, 0, nil}}
	blocks, err := encodeSourceExceptionBlocks(facts)
	if err != nil || len(blocks) != 2 || blocks[0].records != 1 {
		t.Fatal("oversized single was dropped or merged", len(blocks), err)
	}
	for _, b := range blocks {
		got, size, e := decodeSourceExceptionBlock(b.body, b.first, b.last, b.records)
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range got {
			raw, _ := json.Marshal(f.Event)
			if f.Event != nil && size != len(raw) {
				t.Fatal("logical budget used compressed bytes")
			}
		}
	}
	r := []storedSourceException{{Offset: 0, Block: 0, LogicalBytes: 1, Event: json.RawMessage(`{"id":"foreign"}`)}}
	raw, _ := json.Marshal(r)
	body, _ := encodeSourceValue(raw)
	if _, _, e := decodeSourceExceptionBlock(body, sourcePosition{}, sourcePosition{}, 1); e == nil {
		t.Fatal("false expansion size accepted")
	}
}

func downgradeSealedSourceFixture(t *testing.T, path, sealPath string) {
	t.Helper()
	f, err := openSourceStore(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	// These cutover fixtures normally have no exceptions. Rewrite any current
	// blocks independently into their old per-fact schema for old-version entry.
	rows, err := f.db.Query("SELECT source,offset,block,end_offset,end_block,records,body FROM exceptions")
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		source int64
		fact   sourceException
	}
	var all []item
	for rows.Next() {
		var source int64
		var first, last sourcePosition
		var count int
		var body []byte
		if err = rows.Scan(&source, &first.Offset, &first.Block, &last.Offset, &last.Block, &count, &body); err != nil {
			t.Fatal(err)
		}
		facts, _, e := decodeSourceExceptionBlock(body, first, last, count)
		if e != nil {
			t.Fatal(e)
		}
		for _, fact := range facts {
			all = append(all, item{source, fact})
		}
	}
	rows.Close()
	tx, e := f.db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec("DROP TABLE exceptions;CREATE TABLE exceptions(rowid INTEGER PRIMARY KEY,source INTEGER NOT NULL REFERENCES sources(rowid),offset INTEGER NOT NULL,block INTEGER NOT NULL,body BLOB NOT NULL,UNIQUE(source,offset,block));PRAGMA user_version=2")
	if e != nil {
		t.Fatal(e)
	}
	for _, v := range all {
		raw, _ := json.Marshal(v.fact)
		body, e := encodeSourceValue(raw)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec("INSERT INTO exceptions(source,offset,block,body) VALUES(?,?,?,?)", v.source, v.fact.Offset, v.fact.Block, body); e != nil {
			t.Fatal(e)
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	f.db.Close()
	if sealPath != "" {
		seal, e := readSourceSeal(sealPath)
		if e != nil {
			t.Fatal(e)
		}
		seal.Hash, seal.Size, e = sourceFileSeal(context.Background(), path)
		if e != nil {
			t.Fatal(e)
		}
		if e = saveSourceSeal(sealPath, seal); e != nil {
			t.Fatal(e)
		}
	}
}

func TestSourceBlockUpgradeReconcilesOldSealedCutover(t *testing.T) {
	for _, mode := range []string{"shadow", "original_renamed", "installed", "retirement_intent"} {
		t.Run(mode, func(t *testing.T) {
			m, states, facts, path, _ := completedMigrationFixture(t)
			provider := m.provider
			if e := m.Close(); e != nil {
				t.Fatal(e)
			}
			shadow := path + ".source-migrating"
			downgradeSealedSourceFixture(t, shadow, path)
			if mode != "shadow" {
				if e := os.Rename(path, path+".source-legacy"); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "installed" || mode == "retirement_intent" {
				if e := os.Rename(shadow, path); e != nil {
					t.Fatal(e)
				}
			}
			if mode == "retirement_intent" {
				seal, e := readSourceSeal(path)
				if e != nil {
					t.Fatal(e)
				}
				seal.Retiring = "source-legacy"
				if e = saveSourceSeal(path, seal); e != nil {
					t.Fatal(e)
				}
				if e = os.Remove(path + ".source-legacy"); e != nil {
					t.Fatal(e)
				}
			}
			m = NewPersistent(provider, path)
			defer m.Close()
			finishSourceFixtureMigration(t, m)
			for i, st := range states {
				for _, want := range facts[i] {
					got, e := m.store.event(context.Background(), st, want.offset, want.block)
					if e != nil || !reflect.DeepEqual(got, want.event) {
						t.Fatal("old cutover lost original fact", e)
					}
				}
			}
			for _, suffix := range []string{".source-migrating", ".source-legacy", ".source-ready.json"} {
				if _, e := os.Stat(path + suffix); !os.IsNotExist(e) {
					t.Fatal("old recovery structure left", suffix, e)
				}
			}
		})
	}
}

func TestSourceBlockUpgradeRejectsLegacyTail(t *testing.T) {
	f, _ := legacySourceUpgradeFixture(t, 1)
	var body []byte
	if e := f.db.QueryRow("SELECT body FROM exceptions").Scan(&body); e != nil {
		t.Fatal(e)
	}
	raw, e := decodeSourceValue(body, maxStoredValue)
	if e != nil {
		t.Fatal(e)
	}
	body, e = encodeSourceValue(append(raw, []byte(` {"extra":"fact"}`)...))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.db.Exec("UPDATE exceptions SET body=?", body); e != nil {
		t.Fatal(e)
	}
	if e = initializeSourceBlockUpgrade(context.Background(), f); e != nil {
		t.Fatal(e)
	}
	state, _, _ := readSourceBlockUpgrade(context.Background(), f)
	if e = upgradeSourceBlockBatch(context.Background(), f, &state); e == nil {
		t.Fatal("malformed original silently rewritten")
	}
	var after []byte
	_ = f.db.QueryRow("SELECT body FROM exceptions").Scan(&after)
	if !bytes.Equal(after, body) || state.Records != 0 {
		t.Fatal("malformed original or progress changed")
	}
}

func TestSourceBlockUpgradeUnsealedAndInterruptedRepair(t *testing.T) {
	for _, mode := range []string{"unsealed", "repair_cursor"} {
		t.Run(mode, func(t *testing.T) {
			m, states, facts, path, _ := completedMigrationFixture(t)
			provider := m.provider
			state, _, e := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
			if e != nil {
				t.Fatal(e)
			}
			if mode == "repair_cursor" {
				file, e := os.OpenFile(states[0].Path, os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					t.Fatal(e)
				}
				_, _ = file.WriteString(request("new human instruction"))
				_ = file.Close()
				e = m.validateMigrationSources(context.Background(), m.sourceMigration.shadow)
				if e == nil {
					t.Fatal("source change not observed")
				}
				if e = repairMigrationSource(context.Background(), m.sourceMigration, &state, e); e != nil {
					t.Fatal(e)
				}
				// Crash before seal removal; durable phase carries the invalidation.
			} else {
				state.Phase = "sources"
				if e = saveSourceMigration(context.Background(), m.sourceMigration.shadow, state); e != nil {
					t.Fatal(e)
				}
				if e = os.Remove(path + ".source-ready.json"); e != nil {
					t.Fatal(e)
				}
			}
			if e = m.Close(); e != nil {
				t.Fatal(e)
			}
			downgradeSealedSourceFixture(t, path+".source-migrating", "")
			m = NewPersistent(provider, path)
			defer m.Close()
			finishSourceFixtureMigration(t, m)
			if mode == "repair_cursor" {
				verifyStoredFixtureFacts(t, m, states[0], facts[0])
			} else {
				for i, st := range states {
					for _, want := range facts[i] {
						got, e := m.store.event(context.Background(), st, want.offset, want.block)
						if e != nil || !reflect.DeepEqual(got, want.event) {
							t.Fatal("unsealed v2 resume changed fact", e)
						}
					}
				}
			}
		})
	}
}

func TestSourceBlockUpgradeOfflineDirectRepairProgress(t *testing.T) {
	f := makeFactMigrationFixture(t, 5, false)
	m, _ := directReadyFixture(t, f)
	if e := m.Close(); e != nil {
		t.Fatal(e)
	}
	downgradeSealedSourceFixture(t, f.path+".source-migrating", f.path)
	// The next quantum observes withdrawal, commits repair and releases the old
	// immutable-target claim. Subsequent offline progress must survive the
	// shadow being temporarily owned only by the in-place upgrade.
	provider := withdrawnMigrationProvider
	var phases []string
	e := OptimizeStorage(context.Background(), provider, f.path, func(p StorageProgress) { phases = append(phases, p.Phase) }, "")
	if e != nil {
		t.Fatal(e)
	}
	m = NewPersistent(provider, f.path)
	defer m.Close()
	if e = m.openIndex(); e != nil {
		t.Fatal(e)
	}
	var version int
	_ = m.store.db.QueryRow("PRAGMA user_version").Scan(&version)
	if version != 3 {
		t.Fatal(version, phases)
	}
}

func TestSourceExceptionBlockPreservesOldMaximumEnvelope(t *testing.T) {
	event := snapshot.TrajectoryEvent{ID: "foreign", SessionID: "foreign", Text: "x"}
	fact := sourceException{Offset: 0, Block: 0, Event: &event}
	raw, e := json.Marshal(fact)
	if e != nil {
		t.Fatal(e)
	}
	event.Text = strings.Repeat("x", maxStoredValue-len(raw)+1)
	raw, e = json.Marshal(fact)
	if e != nil || len(raw) != maxStoredValue {
		t.Fatal(len(raw), e)
	}
	body, e := encodeSourceValue(raw)
	if e != nil {
		t.Fatal("legal old envelope not accepted", e)
	}
	original, _, e := decodeLegacySourceException(body, 0, 0)
	if e != nil {
		t.Fatal(e)
	}
	blocks, e := encodeSourceExceptionBlocks([]sourceException{original})
	if e != nil || len(blocks) != 1 {
		t.Fatal("legal old maximum stranded", e)
	}
	got, logical, e := decodeSourceExceptionBlock(blocks[0].body, blocks[0].first, blocks[0].last, 1)
	if e != nil || len(got) != 1 || !reflect.DeepEqual(got[0], fact) || logical > maxStoredValue {
		t.Fatal("legal maximum changed or budget inflated", logical, e)
	}
}
