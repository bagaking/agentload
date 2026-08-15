package main

import (
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type archiveCountingDecoder struct {
	trajectory.Decoder
	found *atomic.Bool
}

func TestTrajectoryArchiveRootsRejectRedirectedFiles(t *testing.T) {
	for _, redirect := range []string{"leaf", "parent"} {
		t.Run(redirect, func(t *testing.T) {
			app, _, _ := trajectoryTestApp(t)
			set := app.archiveSources(context.Background())
			if len(set.Sources) != 1 {
				t.Fatal("fixture discovery", set.Coverage)
			}
			path := set.Sources[0].Path
			outside := t.TempDir()
			target := filepath.Join(outside, filepath.Base(path))
			if err := os.WriteFile(target, []byte("outside private content"), 0600); err != nil {
				t.Fatal(err)
			}
			if redirect == "leaf" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			} else {
				dir := filepath.Dir(path)
				if err := os.Rename(dir, dir+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, dir); err != nil {
					t.Fatal(err)
				}
			}
			set = app.archiveSources(context.Background())
			if len(set.Sources) != 0 || set.Coverage.Complete {
				t.Fatal("redirected archive escaped configured roots", set)
			}
		})
	}
}

func (d archiveCountingDecoder) Decode(b []byte, c trajectory.DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	if bytes.Contains(b, []byte("missed-audit")) {
		d.found.Store(true)
	}
	return d.Decoder.Decode(b, c)
}
func TestTrajectoryArchiveAuditFindsMissedSessionWithoutQueries(t *testing.T) {
	app, _, _ := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	var found atomic.Bool
	registry := app.observer.adapters
	registry.mu.Lock()
	i := registry.byID["codex"]
	registry.adapters[i].Capabilities.Trajectory = archiveCountingDecoder{registry.adapters[i].Capabilities.Trajectory, &found}
	registry.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); app.recoverArchive(ctx, 25*time.Millisecond) }()
	defer func() { cancel(); <-done }()
	// Wait until discovery has already run, then add a session without a hint.
	deadline := time.Now().Add(2 * time.Second)
	for !app.observer.evidenceIndex.snapshot(context.Background(), time.Time{}, nil).Complete && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	dir := filepath.Join(app.cfg.CodexRoots[0], "sessions", "2026", "01", "02")
	os.MkdirAll(dir, 0700)
	body := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"missed-audit"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-missed.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for !found.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !found.Load() {
		t.Fatal("periodic audit did not discover and normalize missed session")
	}
}
