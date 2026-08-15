package trajectory

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"agentload/internal/snapshot"
)

func packingFixture(t *testing.T, count int) *sourceStore {
	t.Helper()
	f, err := openSourceStore(context.Background(), filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.db.Close() })
	f.checkCapacity = func(string, uint64) error { return nil }
	if _, err = f.db.Exec("INSERT INTO sources(rowid,id,generation,agent,mtime,missing,checkpoint) VALUES(1,'0011223344556677','0011223344556677','claude',0,1,'{}')"); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	random := rand.New(rand.NewSource(7))
	for i := 0; i < count; i++ {
		data := make([]byte, 3072)
		_, _ = random.Read(data)
		e := snapshot.TrajectoryEvent{ID: "retained", Text: base64.StdEncoding.EncodeToString(data)}
		if err = putSourceException(tx, 1, sourceException{Offset: int64(i), Event: &e}); err != nil {
			t.Fatal(err)
		}
	}
	// Model the leaf gaps left by independently proved old-row retirement.
	if _, err = tx.Exec("DELETE FROM exceptions WHERE rowid%4!=1"); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSourcePackingReclaimsLeavesAndRestarts(t *testing.T) {
	ctx := context.Background()
	f := packingFixture(t, 2400)
	before, _ := os.Stat(f.path)
	digest, records, err := sourceExceptionStoreDigest(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	controls, err := sourceUpgradeControlDigest(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	state, err := beginSourcePacking(ctx, f, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = packSourceBlockBatch(ctx, f, &state); err != nil || state.Packed != 64 {
		t.Fatal("first bounded batch", state.Packed, err)
	}
	path := f.path
	f.db.Close()
	f, err = openSourceStore(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	f.checkCapacity = func(string, uint64) error { return nil }
	result, err := optimizeSourceBlockPacking(ctx, f, state.RequestHash, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Phase != "ready" || result.Packed != result.Blocks || result.Records != records || result.Digest != digest || result.Controls != controls {
		t.Fatal("complete packing proof differs", result)
	}
	for i := int64(0); i < 2400; i += 4 {
		got, e := exceptionAt(ctx, f.db, 1, i, 0)
		if e != nil || got.Event == nil || got.Event.ID != "retained" {
			t.Fatal("public slot lost after physical move", i, e)
		}
	}
	var free int
	f.db.QueryRow("PRAGMA freelist_count").Scan(&free)
	after, _ := os.Stat(path)
	if free != 0 || after.Size() >= before.Size() {
		t.Fatal("leaf space not returned to disk", before.Size(), after.Size(), free)
	}
	hash, _, _ := sourceFileSeal(ctx, path)
	again, err := optimizeSourceBlockPacking(ctx, f, hash, false, nil)
	current, _, _ := sourceFileSeal(ctx, path)
	if err != nil || current != hash || *again != *result {
		t.Fatal("unchanged maintenance rewrote the store", err)
	}
}

func TestSourcePackingRollsBackAndProtectsCapacity(t *testing.T) {
	ctx := context.Background()
	f := packingFixture(t, 320)
	before, _, _ := sourceFileSeal(ctx, f.path)
	denied := errors.New("capacity denied")
	f.checkCapacity = func(string, uint64) error { return denied }
	if _, err := beginSourcePacking(ctx, f, before, false); !errors.Is(err, denied) {
		t.Fatal("capacity admission", err)
	}
	current, _, _ := sourceFileSeal(ctx, f.path)
	if current != before {
		t.Fatal("capacity refusal changed the original")
	}
	f.checkCapacity = func(string, uint64) error { return nil }
	state, err := beginSourcePacking(ctx, f, before, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.db.Exec("CREATE TRIGGER reject_packing_cursor BEFORE UPDATE ON meta WHEN NEW.key='source-block-packing' BEGIN SELECT RAISE(ABORT,'cursor fault'); END")
	if err != nil {
		t.Fatal(err)
	}
	if err = packSourceBlockBatch(ctx, f, &state); err == nil {
		t.Fatal("cursor fault accepted")
	}
	stored, _, err := readSourcePacking(ctx, f, sourcePackingKey)
	var moved int
	f.db.QueryRow("SELECT count(*) FROM exceptions WHERE rowid>?", state.MaxOldRow).Scan(&moved)
	if err != nil || moved != 0 || stored != state || state.Packed != 0 {
		t.Fatal("moves/progress escaped rollback", moved, stored, err)
	}
	_, _ = f.db.Exec("DROP TRIGGER reject_packing_cursor")
	f.checkCapacity = func(string, uint64) error { return denied }
	if err = packSourceBlockBatch(ctx, f, &state); !errors.Is(err, denied) || state.Packed != 0 {
		t.Fatal("bounded write ignored capacity", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = optimizeSourceBlockPacking(cancelled, f, before, false, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	f.checkCapacity = func(string, uint64) error { return nil }
	if _, err = optimizeSourceBlockPacking(ctx, f, before, false, nil); err != nil {
		t.Fatal("failed transaction did not resume", err)
	}
}

func TestSourcePackingRefusesWrongInputAndExhaustedNamespace(t *testing.T) {
	ctx := context.Background()
	f := packingFixture(t, 4)
	before, _, _ := sourceFileSeal(ctx, f.path)
	if _, err := beginSourcePacking(ctx, f, "wrong-input", false); err == nil {
		t.Fatal("different original accepted")
	}
	if _, err := beginSourcePacking(ctx, f, "wrong-input", true); err == nil {
		t.Fatal("migration activity fabricated input provenance")
	}
	if _, err := f.db.Exec("UPDATE exceptions SET rowid=?", int64(math.MaxInt64)); err != nil {
		t.Fatal(err)
	}
	if _, err := beginSourcePacking(ctx, f, "", false); err == nil {
		t.Fatal("rowid overflow accepted")
	}
	var raw string
	if err := f.db.QueryRow("SELECT value FROM meta WHERE key=?", sourcePackingKey).Scan(&raw); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("refused input left migration state", err)
	}
	if current, _, _ := sourceFileSeal(ctx, f.path); current == before {
		t.Fatal("namespace fixture did not change")
	}
}

func TestSourcePackingBoundMigrationStillChecksExpectedInput(t *testing.T) {
	ctx := context.Background()
	f := packingFixture(t, 4)
	hash, _, err := sourceFileSeal(ctx, f.path)
	if err != nil {
		t.Fatal(err)
	}
	identity, _, err := persistentPathIdentity(f.path)
	if err != nil {
		t.Fatal(err)
	}
	s := NewPersistent(nil, f.path)
	defer s.Close()
	s.expectedInputSHA256 = "wrong-input"
	state := sourceMigration{InputIdentity: identity, InputSHA256: hash}
	if err = s.bindMigrationInput(ctx, nil, &state); err == nil {
		t.Fatal("bound identity bypassed expected SHA")
	}
	if current, _, _ := sourceFileSeal(ctx, f.path); current != hash {
		t.Fatal("wrong expected input altered original")
	}
}

func TestSourcePackingServiceResumesBeforeOrdinaryWrites(t *testing.T) {
	ctx := context.Background()
	f := packingFixture(t, 320)
	state, err := beginSourcePacking(ctx, f, "", false)
	if err != nil {
		t.Fatal(err)
	}
	path := f.path
	f.db.Close()
	bad := NewPersistent(nil, path)
	bad.expectedInputSHA256 = "wrong-input"
	if err = bad.openIndexContext(ctx); err == nil {
		t.Fatal("resume moved blocks before validating input")
	}
	bad.Close()
	s := NewPersistent(nil, path)
	defer s.Close()
	s.expectedInputSHA256 = state.RequestHash
	for {
		err = s.openIndexContext(ctx)
		if errors.Is(err, errStorageMigration) {
			if s.store != nil {
				t.Fatal("ordinary store admitted during packing")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	result, has, err := readSourcePacking(ctx, s.store, sourcePackingResultKey)
	if err != nil || !has || result.Phase != "ready" || result.Packed != state.Blocks {
		t.Fatal("pending packing not recovered", result, err)
	}
}

func TestSourcePackingRefusesForeignRevision(t *testing.T) {
	ctx := context.Background()
	f := packingFixture(t, 8)
	state, err := beginSourcePacking(ctx, f, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.write(ctx, 0, func(tx *sql.Tx) error {
		_, e := tx.Exec("INSERT INTO meta(key,value) VALUES('foreign','must remain')")
		return e
	}); err != nil {
		t.Fatal(err)
	}
	if err = stepSourceBlockPacking(ctx, f, &state); err == nil || state.Packed != 0 {
		t.Fatal("foreign write accepted", err)
	}
	var value string
	if err = f.db.QueryRow("SELECT value FROM meta WHERE key='foreign'").Scan(&value); err != nil || value != "must remain" {
		t.Fatal("foreign evidence changed", err)
	}
}
