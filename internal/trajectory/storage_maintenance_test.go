package trajectory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	}, "")
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

func TestTrajectoryMaintenanceResumesInstalledCutoverWithOriginalInput(t *testing.T) {
	for _, version := range []int{sourceStoreVersion, 2} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			fixture, st, facts, path := publishSQLiteRecoveryFixture(t)
			if version == 2 {
				downgradeSealedSourceFixture(t, path, path)
			}
			input, _, err := sourceFileSeal(context.Background(), path+".source-legacy")
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := readSourceSeal(path)
			if err != nil {
				t.Fatal(err)
			}
			var ready StorageProgress
			err = OptimizeStorage(context.Background(), fixture.provider, path, func(p StorageProgress) {
				if p.Phase == "ready" {
					ready = p
				}
			}, input)
			if err != nil {
				t.Fatal("correct original request rejected across cutover", err)
			}
			if version == sourceStoreVersion {
				if ready.TargetHash != sealed.Hash || ready.BlockPacking != nil {
					t.Fatal("dense sealed target unnecessarily rewritten", ready)
				}
			} else if ready.BlockUpgrade == nil || ready.BlockUpgrade.InputSHA256 != sealed.Hash || ready.BlockUpgrade.InputSHA256 == input {
				t.Fatal("upgrade lost distinct physical stage input", ready)
			}
			f, err := openSourceStore(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.db.Close()
			for _, want := range facts {
				got, err := f.event(context.Background(), st, want.offset, want.block)
				if err != nil || !reflect.DeepEqual(got, want.event) {
					t.Fatal("resumed cutover changed complete fact", err)
				}
			}
			for _, suffix := range []string{".source-legacy", ".source-migrating", ".source-ready.json"} {
				if _, err := os.Stat(path + suffix); !os.IsNotExist(err) {
					t.Fatal("finished cutover retained obsolete structure", suffix, err)
				}
			}
		})
	}
}
