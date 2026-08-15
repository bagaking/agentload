package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestTrajectoryMaintenanceReportsVerifiedSealBeforeRetirement(t *testing.T) {
	fixture, st := replayFixture(t, "codex", CodexDecoder{}, request("maintenance evidence")+call("paired")+result("paired"))
	facts := canonicalFixtureFacts(t, fixture, st)
	path := filepath.Join(t.TempDir(), "trajectory.sqlite")
	old, err := openFactStore(path)
	if err != nil {
		t.Fatal(err)
	}
	err = old.write(context.Background(), func(w *factWriter) error {
		row, err := w.source(st.ID, st.Agent, st.checkpoint)
		if err != nil {
			return err
		}
		for _, fact := range facts {
			if _, err = w.event(row, fact.event, true); err != nil {
				return err
			}
		}
		return nil
	})
	if closeErr := old.db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	input, _, err := migrationFileIdentity(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reports []StorageProgress
	err = OptimizeStorage(context.Background(), fixture.provider, path, func(progress StorageProgress) {
		reports = append(reports, progress)
		if progress.Phase != "sealed" {
			return
		}
		original, e := os.ReadFile(path)
		if e != nil || sha256.Sum256(original) != sha256.Sum256(before) {
			t.Fatal("original retired or changed before observable verified seal", e)
		}
		shadow, e := os.ReadFile(path + ".source-migrating")
		if e != nil {
			t.Fatal(e)
		}
		hash := sha256.Sum256(shadow)
		if progress.Input != input || progress.TargetHash != hex.EncodeToString(hash[:]) || progress.Sources != 1 || progress.Records != int64(len(facts)) {
			t.Fatalf("seal does not identify independently verified target: %+v", progress)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	var sealed *StorageProgress
	for i := range reports {
		if reports[i].Phase == "sealed" {
			sealed = &reports[i]
		}
	}
	if sealed == nil || reports[len(reports)-1].Phase != "ready" {
		t.Fatalf("missing required pre-retirement or final report: %+v", reports)
	}
	ready := reports[len(reports)-1]
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(final)
	if ready.TargetHash != sealed.TargetHash || ready.TargetHash != hex.EncodeToString(hash[:]) || ready.Input != sealed.Input || ready.Sources != sealed.Sources || ready.Records != sealed.Records {
		t.Fatalf("final ready is not the same verified target: %+v %+v", sealed, ready)
	}
	if _, err := os.Stat(path + ".source-legacy"); !os.IsNotExist(err) {
		t.Fatal("completed maintenance retained old layout", err)
	}
}
