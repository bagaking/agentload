package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A cancelled scan must release SQLite statements, including the driver Rows
// handoff. Closing only the Go worker does not prove that another owner can write.
func TestTrajectoryCancelledScanReleasesReadLocks(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	const count = 128
	err = f.write(context.Background(), func(w *factWriter) error {
		source, err := w.source("src", "codex", sourceCheckpoint{Generation: "gen", EventCount: count})
		if err != nil {
			return err
		}
		for i := range count {
			ref := snapshot.TrajectorySourceRef{ID: "src", Generation: "gen", Offset: int64(i + 1), Digest: "hash"}
			e := snapshot.TrajectoryEvent{ID: sourceEventID(ref), SessionID: "s.src.gen", Source: ref, Kind: "text", Role: "user", Text: strings.Repeat("research evidence ", 4096)}
			if _, err := w.event(source, e, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", searchDatabaseDSN(f.path))
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	other.SetMaxOpenConns(1)
	if _, err := other.Exec("PRAGMA busy_timeout=0"); err != nil {
		t.Fatal(err)
	}
	for i := range 24 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(1+i%3)*time.Millisecond)
		err := f.prepareTextMatches(ctx, snapshot.TrajectorySelector{Text: "research"})
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("scan must reach cancellation: %v", err)
		}
		if _, err := other.Exec("BEGIN EXCLUSIVE; ROLLBACK"); err != nil {
			t.Fatalf("cancelled scan %d left a SQLite read lock: %v", i, err)
		}
		var remains bool
		if err := f.db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_temp_master WHERE name='trajectory_text_matches')").Scan(&remains); err != nil || remains {
			t.Fatalf("cancelled scan retained matches: %v %v", remains, err)
		}
		if err := f.write(context.Background(), func(w *factWriter) error { return nil }); err != nil {
			t.Fatalf("same owner cannot commit after cancellation: %v", err)
		}
	}
	if err := f.prepareTextMatches(context.Background(), snapshot.TrajectorySelector{Text: "research"}); err != nil {
		t.Fatal(err)
	}
	var found int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM temp.trajectory_text_matches").Scan(&found); err != nil || found != count {
		t.Fatalf("subsequent query changed identities/count: %d %v", found, err)
	}
}
