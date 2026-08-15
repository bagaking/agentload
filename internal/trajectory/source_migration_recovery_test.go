package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func publishSQLiteRecoveryFixture(t *testing.T) (*Service, *sourceState, []sourceFact, string) {
	t.Helper()
	fixture, st, facts, path := gappedSourceMigrationFixture(t)
	m := NewPersistent(fixture.provider, path)
	defer m.Close()
	for n := 0; n < 300; n++ {
		if err := m.migrateSourceStore(context.Background()); !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err := sourceMigrationState(context.Background(), m.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == "ready" {
			if err = m.Close(); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(path, path+".source-legacy"); err != nil {
				t.Fatal(err)
			}
			if err = os.Rename(path+".source-migrating", path); err != nil {
				t.Fatal(err)
			}
			return fixture, st, facts, path
		}
	}
	t.Fatal("fixture did not publish sealed target")
	return nil, nil, nil, ""
}

func withdrawnMigrationProvider(context.Context) SourceSet {
	return SourceSet{CatalogComplete: true, Coverage: coverage("authorized catalog")}
}

func rewriteMigrationRaw(t *testing.T, path string) [32]byte {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil || len(body) == 0 {
		t.Fatal("fixture source unavailable", err)
	}
	// Rewrite the checkpointed prefix as well as the middle. This fixture
	// requires whole-source quarantine, not a reproducible range plus a single
	// preserved canonical exception for a changed record.
	body[0] ^= 1
	body[len(body)/2] ^= 1
	body = append(body, '\n')
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(body)
}

func TestTrajectorySourceMigrationPublishedRawChangeRestoresSameShadow(t *testing.T) {
	for _, mode := range []string{"rewrite", "withdraw"} {
		t.Run(mode, func(t *testing.T) {
			fixture, st, facts, path := publishSQLiteRecoveryFixture(t)
			original, err := os.ReadFile(path + ".source-legacy")
			if err != nil {
				t.Fatal(err)
			}
			provider := fixture.provider
			var rawHash [32]byte
			if mode == "rewrite" {
				rawHash = rewriteMigrationRaw(t, st.Path)
			} else {
				provider = withdrawnMigrationProvider
			}
			m := NewPersistent(provider, path)
			defer m.Close()
			if err = m.openIndex(); !errors.Is(err, errStorageMigration) {
				t.Fatal("changed dependencies did not return to bounded migration", err)
			}
			restored, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(restored, original) {
				t.Fatal("canonical original was not restored unchanged", err)
			}
			if _, err = os.Stat(path + ".source-migrating"); err != nil {
				t.Fatal("same small shadow not preserved", err)
			}
			finishSourceFixtureMigration(t, m)
			verifyStoredFixtureFacts(t, m, st, facts)
			for _, suffix := range []string{".source-legacy", ".source-migrating", ".source-ready.json"} {
				if _, err = os.Stat(path + suffix); !os.IsNotExist(err) {
					t.Fatal("completed recovery structure remains", suffix, err)
				}
			}
			if mode == "rewrite" {
				body, e := os.ReadFile(st.Path)
				if e != nil || sha256.Sum256(body) != rawHash {
					t.Fatal("migration modified current source", e)
				}
			}
		})
	}
}

func TestTrajectorySourceMigrationRecordedRetirementCleanup(t *testing.T) {
	for _, mode := range []string{"intent", "completed", "unrecorded"} {
		t.Run(mode, func(t *testing.T) {
			fixture, st, facts, path := publishSQLiteRecoveryFixture(t)
			seal, err := readSourceSeal(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "intent" {
				seal.Retiring = "source-legacy"
			} else if mode == "completed" {
				seal.Retired = []string{"source-legacy"}
			}
			if err = saveSourceSeal(path, seal); err != nil {
				t.Fatal(err)
			}
			if err = os.Remove(path + ".source-legacy"); err != nil {
				t.Fatal(err)
			}
			m := NewPersistent(fixture.provider, path)
			defer m.Close()
			err = m.openIndex()
			if mode == "unrecorded" {
				if err == nil || errors.Is(err, errStorageMigration) {
					t.Fatal("unexplained absent original admitted", err)
				}
				if _, e := os.Stat(path + ".source-ready.json"); e != nil {
					t.Fatal("refusal removed recovery evidence", e)
				}
				return
			}
			if err != nil {
				t.Fatal("recorded retirement did not finish", err)
			}
			for _, want := range facts {
				got, e := m.store.event(context.Background(), st, want.offset, want.block)
				if e != nil || !reflect.DeepEqual(got, want.event) {
					t.Fatal("cleanup changed complete fact", e)
				}
			}
			if _, e := os.Stat(path + ".source-ready.json"); !os.IsNotExist(e) {
				t.Fatal("cleanup not completed", e)
			}
		})
	}
}

func verifyDirectStoredRecovery(t *testing.T, s *Service, events []snapshot.TrajectoryEvent) {
	t.Helper()
	var row int64
	var count, ranges, missing int
	if err := s.store.db.QueryRow("SELECT rowid,missing FROM sources WHERE id=? AND active=1", events[0].Source.ID).Scan(&row, &missing); err != nil || missing != 1 {
		t.Fatal("original generation not quarantined", err)
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM exceptions WHERE source=?", row).Scan(&count); err != nil || count != len(events) {
		t.Fatal("complete exception count differs", count, err)
	}
	if err := s.store.db.QueryRow("SELECT count(*) FROM ranges WHERE source=?", row).Scan(&ranges); err != nil || ranges != 0 {
		t.Fatal("obsolete replay dependencies survive", ranges, err)
	}
	for _, want := range events {
		var body []byte
		if err := s.store.db.QueryRow("SELECT body FROM exceptions WHERE source=? AND offset=? AND block=?", row, want.Source.Offset, want.Source.Block).Scan(&body); err != nil {
			t.Fatal(err)
		}
		raw, err := decodeSourceValue(body, maxSourceRangeLogicalBytes)
		var got sourceException
		if err != nil || json.Unmarshal(raw, &got) != nil || got.Event == nil || !reflect.DeepEqual(*got.Event, want) {
			t.Fatal("preserved original DTO differs", err)
		}
	}
}

func TestTrajectorySourceDirectPublishedRawChangeAndRetiredSearchRecovery(t *testing.T) {
	for _, mode := range []string{"rewrite", "withdraw", "search_intent_rewrite", "search_completed_withdraw", "search_progress_tamper"} {
		t.Run(mode, func(t *testing.T) {
			f := makeFactMigrationFixture(t, 5, false)
			s, _ := directReadyFixture(t, f)
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(f.path+".source-migrating", f.path); err != nil {
				t.Fatal(err)
			}
			original, _ := os.ReadFile(f.legacy)
			if strings.HasPrefix(mode, "search_") {
				seal, err := readSourceSeal(f.path)
				if err != nil || seal.ProgressHash == "" {
					t.Fatal("missing independent retirement progress seal", err)
				}
				if mode == "search_intent_rewrite" {
					seal.Retiring = "search.sqlite"
				} else {
					seal.Retired = []string{"search.sqlite"}
				}
				if err = saveSourceSeal(f.path, seal); err != nil {
					t.Fatal(err)
				}
				if err = os.Remove(f.search); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(mode, "withdraw") {
				f.provider = withdrawnMigrationProvider
			} else {
				rewriteMigrationRaw(t, f.source)
			}
			s = migrationService(f)
			defer s.Close()
			if err := s.openIndex(); !errors.Is(err, errStorageMigration) {
				t.Fatal("published source change did not restore shadow", err)
			}
			if _, err := os.Stat(f.path + ".source-migrating"); err != nil {
				t.Fatal("same bounded shadow absent", err)
			}
			if mode == "search_progress_tamper" {
				shadow, err := openSourceStore(context.Background(), f.path+".source-migrating")
				if err != nil {
					t.Fatal(err)
				}
				_, err = shadow.db.Exec("UPDATE metadata SET body=? WHERE path=?", []byte(`{"Generation":"tampered","Count":0}`), legacyProgressPath(f.events[0].Source.ID))
				shadow.db.Close()
				if err != nil {
					t.Fatal(err)
				}
				if err = s.openIndex(); err == nil || errors.Is(err, errStorageMigration) {
					t.Fatal("unverified retired progress admitted", err)
				}
			} else {
				finishDirectMigration(t, s)
				verifyDirectStoredRecovery(t, s, f.events)
				if _, err := os.Stat(f.legacy); !os.IsNotExist(err) {
					t.Fatal("verified original not retired", err)
				}
				return
			}
			after, err := os.ReadFile(f.legacy)
			if err != nil || !bytes.Equal(after, original) {
				t.Fatal("failed recovery modified canonical original", err)
			}
		})
	}
}

func TestTrajectorySourceDirectPairProofLinearAndResumable(t *testing.T) {
	f := makeFactMigrationFixture(t, 5, false)
	const id = "!pair-proof"
	const calls = 80
	var retained []snapshot.TrajectoryEvent
	db, err := bolt.Open(f.legacy, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.Bucket(sourceBucket).CreateBucket([]byte(id))
		if err != nil {
			return err
		}
		data, err := b.CreateBucket(eventBucket)
		if err != nil {
			return err
		}
		pairs, err := b.CreateBucket(pairBucket)
		if err != nil {
			return err
		}
		for n := 0; n < calls; n++ {
			call := fmt.Sprintf("ambiguous-%03d", n)
			ledger := indexedPair{}
			for part := 0; part < 5; part++ {
				kind := "tool_result"
				if part < 2 {
					kind = "tool_call"
				}
				e := snapshot.TrajectoryEvent{ID: fmt.Sprintf("original-pair-%d-%d", n, part), SessionID: id, Role: "assistant", Kind: kind, Tool: &snapshot.TrajectoryTool{Name: "exec", CallID: call}, Source: snapshot.TrajectorySourceRef{ID: id, Generation: "retained", Offset: int64((n*5 + part) * 10)}}
				frame, err := encodeEventStored(e)
				if err != nil {
					return err
				}
				if err = data.Put(eventKey(e.Source.Offset, 0), frame); err != nil {
					return err
				}
				if part < 2 {
					ledger.Calls = append(ledger.Calls, e.ID)
				} else if len(ledger.Results) < 2 {
					ledger.Results = append(ledger.Results, e.ID)
				}
				retained = append(retained, e)
			}
			if err = putJSON(pairs, []byte(call), ledger); err != nil {
				return err
			}
		}
		return putJSON(tx.Bucket(checkpointBucket), []byte(id), sourceCheckpoint{Version: projectionVersion, Generation: "retained", EventCount: len(retained), Offset: int64(len(retained) * 10), Coverage: coverage("canonical evidence")})
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	s := migrationService(f)
	var committed sourceMigration
	var decodedBefore int64
	for n := 0; n < 2000; n++ {
		if err = s.migrateDirectLegacy(context.Background()); !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err := sourceMigrationState(context.Background(), s.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == "legacy-verify" && state.Legacy.Records >= 64 {
			var pending int
			if err = s.sourceMigration.shadow.db.QueryRow("SELECT count(*) FROM migration_pairs").Scan(&pending); err != nil || pending == 0 {
				t.Fatal("fixture has no durable pair witnesses", pending, err)
			}
			committed, decodedBefore = state, s.sourceMigration.decodedFacts
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if err = s.migrateDirectLegacy(ctx); !errors.Is(err, context.Canceled) {
				t.Fatal("cancelled proof ignored", err)
			}
			unchanged, _, e := sourceMigrationState(context.Background(), s.sourceMigration.shadow)
			if e != nil || !reflect.DeepEqual(unchanged, committed) {
				t.Fatal("cancellation changed acknowledged cursor", e)
			}
			break
		}
	}
	if committed.Legacy == nil {
		t.Fatal("no committed proof checkpoint")
	}
	s.Close()
	s = migrationService(f)
	defer s.Close()
	var decodedAfter int64
	for n := 0; n < 2000; n++ {
		if err = s.migrateDirectLegacy(context.Background()); !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, e := sourceMigrationState(context.Background(), s.sourceMigration.shadow)
		if e != nil {
			t.Fatal(e)
		}
		if n == 0 && (state.Legacy.Records <= committed.Legacy.Records || bytes.Equal(metadataPath(state.Legacy.After), metadataPath(committed.Legacy.After))) {
			t.Fatal("resumed proof restarted instead of advancing acknowledged cursor")
		}
		if state.Phase == "ready" {
			decodedAfter = s.sourceMigration.decodedFacts
			var tables int
			if e = s.sourceMigration.shadow.db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='migration_pairs'").Scan(&tables); e != nil || tables != 0 {
				t.Fatal("temporary pair proof was not retired", e)
			}
			break
		}
	}
	events := len(f.events) + len(retained)
	if decodedAfter == 0 || decodedBefore+decodedAfter > int64(6*events) || f.decoder.calls.Load() > int64(10*events) {
		t.Fatal("pair proof is not a bounded linear traversal", decodedBefore, decodedAfter, events, f.decoder.calls.Load())
	}
	t.Logf("%d original events, %d calls: canonical decodes=%d, target raw decodes=%d", events, calls+7, decodedBefore+decodedAfter, f.decoder.calls.Load())
	finishDirectMigration(t, s)
	verifyDirectStoredRecovery(t, s, retained)
}

func TestTrajectorySourceDirectDeepMetadataCapacityBudget(t *testing.T) {
	f := makeFactMigrationFixture(t, 5, false)
	db, err := bolt.Open(f.legacy, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := [][]byte{[]byte("!deep-capacity")}
	err = db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket(path[0])
		if err != nil {
			return err
		}
		for n := 0; n < 12; n++ {
			key := bytes.Repeat([]byte{byte('a' + n)}, 32768)
			path = append(path, key)
			b, err = b.CreateBucket(key)
			if err != nil {
				return err
			}
		}
		return b.SetSequence(18446744073709551615)
	})
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	encodedPath := metadataPath(path)
	if len(encodedPath) < 512*1024 {
		t.Fatal("fixture does not exercise deep encoded path")
	}
	original, _ := os.ReadFile(f.legacy)
	s := migrationService(f)
	defer s.Close()
	var state sourceMigration
	for n := 0; n < 10; n++ {
		if err = s.migrateDirectLegacy(context.Background()); !errors.Is(err, errStorageMigration) {
			t.Fatal(err)
		}
		state, _, err = sourceMigrationState(context.Background(), s.sourceMigration.shadow)
		if err != nil {
			t.Fatal(err)
		}
		if state.Phase == "legacy-copy" {
			break
		}
	}
	before, _, _ := sourceMigrationState(context.Background(), s.sourceMigration.shadow)
	var rowsBefore, rowsAfter int
	if err = s.sourceMigration.shadow.db.QueryRow("SELECT count(*) FROM metadata").Scan(&rowsBefore); err != nil {
		t.Fatal(err)
	}
	var asked uint64
	const available = uint64(1<<30 + 1<<20)
	s.sourceMigration.shadow.checkCapacity = func(_ string, additional uint64) error {
		asked = additional
		if additional+(1<<30) > available {
			return historyfile.ErrStorageBudget
		}
		return nil
	}
	if err = migrateDirectEntries(context.Background(), s, s.sourceMigration, &state); !errors.Is(err, historyfile.ErrStorageBudget) {
		t.Fatal("near-reserve deep keys were admitted", err)
	}
	if asked < 2*uint64(len(encodedPath)) {
		t.Fatal("encoded keys omitted from data/journal allowance", asked, len(encodedPath))
	}
	after, _, err := sourceMigrationState(context.Background(), s.sourceMigration.shadow)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("denied write advanced committed recovery cursor", err)
	}
	if err = s.sourceMigration.shadow.db.QueryRow("SELECT count(*) FROM metadata").Scan(&rowsAfter); err != nil || rowsAfter != rowsBefore {
		t.Fatal("denied write changed target metadata", err)
	}
	body, err := os.ReadFile(f.legacy)
	if err != nil || !bytes.Equal(body, original) {
		t.Fatal("denied write changed original", err)
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	t.Logf(`CAPACITY_REFUSAL {"kind":"migration","before_checkpoint_sha256":"%x","after_checkpoint_sha256":"%x","before_original_sha256":"%x","after_original_sha256":"%x","actual_rejection":true}`, sha256.Sum256(beforeJSON), sha256.Sum256(afterJSON), sha256.Sum256(original), sha256.Sum256(body))
	s.sourceMigration.shadow.checkCapacity = historyfile.CheckStorageCapacity
	finishDirectMigration(t, s)
	var seq string
	var value []byte
	var bucket int
	if err = s.store.db.QueryRow("SELECT sequence,body,bucket FROM metadata WHERE path=?", encodedPath).Scan(&seq, &value, &bucket); err != nil || seq != "18446744073709551615" || value != nil || bucket != 1 {
		t.Fatal("deep opaque bucket was truncated or changed", err)
	}
	t.Logf("deepest encoded path=%dB, denied batch peak=%dB, no committed writes", len(encodedPath), asked)
}
