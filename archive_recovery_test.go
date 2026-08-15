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

func TestTrajectoryArchiveReconcilesMissingCachedSource(t *testing.T) {
	for _, change := range []string{"removed", "moved"} {
		t.Run(change, func(t *testing.T) {
			app, _, _ := trajectoryTestApp(t)
			initial := app.archiveSources(context.Background())
			if !initial.CatalogComplete || len(initial.Sources) != 1 {
				t.Fatal("initial archive discovery", initial)
			}
			path := initial.Sources[0].Path
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sibling := filepath.Join(filepath.Dir(path), "rollout-retained.jsonl")
			if err := os.WriteFile(sibling, body, 0600); err != nil {
				t.Fatal(err)
			}
			app.observer.evidenceIndex.requestReconcile()
			baseline := app.archiveSources(context.Background())
			if !baseline.CatalogComplete || len(baseline.Sources) != 2 {
				t.Fatal("two-source archive discovery", baseline)
			}
			moved := filepath.Join(filepath.Dir(path), "rollout-moved.jsonl")
			if change == "moved" {
				err = os.Rename(path, moved)
			} else {
				err = os.Remove(path)
			}
			if err != nil {
				t.Fatal(err)
			}
			// No watcher runs in this fixture. A failed stat cannot prove that
			// discovery is complete, but must schedule authoritative discovery.
			gap := app.archiveSources(context.Background())
			if gap.CatalogComplete || gap.Coverage.Complete || len(gap.Sources) != 1 || gap.Sources[0].Path != sibling {
				t.Fatal("missing cached source did not retain the discovery gap", gap)
			}
			reconciled := app.archiveSources(context.Background())
			wantCount := 1
			if change == "moved" {
				wantCount = 2
			}
			if !reconciled.CatalogComplete || !reconciled.Coverage.Complete || len(reconciled.Sources) != wantCount {
				t.Fatal("archive remained stuck on an absent cached path", reconciled)
			}
			found := map[string]bool{}
			for _, source := range reconciled.Sources {
				found[source.Path] = true
			}
			if found[path] || !found[sibling] || (change == "moved" && !found[moved]) {
				t.Fatal("reconciled archive did not reflect the actual directory", found)
			}
		})
	}
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

func TestTrajectoryArchiveCoalescesHintsWhileIdleAndStillReplays(t *testing.T) {
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
	var catalogs atomic.Int32
	var maintenance atomic.Int32
	app.archiveSourcesFunc = func(ctx context.Context) trajectory.SourceSet { catalogs.Add(1); return app.archiveSources(ctx) }
	app.archiveCatalogFunc = func(ctx context.Context, set trajectory.SourceSet, enabled func() bool) (bool, error) {
		maintenance.Add(1)
		return app.trajectory.PrepareCatalog(ctx, set, enabled)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); app.recoverArchive(ctx, time.Hour) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(2 * time.Second)
	for catalogs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if catalogs.Load() != 1 {
		t.Fatal("initial catalog was not discovered")
	}
	// Metadata noise must not rescan the entire archive for every wake, even
	// when the queue has already drained. Native replay remains enabled.
	for n := 0; n < 40; n++ {
		app.notifyArchive()
		time.Sleep(5 * time.Millisecond)
	}
	if catalogs.Load() != 1 {
		t.Fatalf("idle notifications rescanned catalog %d times", catalogs.Load())
	}
	set := app.archiveSources(context.Background())
	path := set.Sources[0].Path
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"missed-audit coalesced evidence\"}]}}\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	app.notifyArchive()
	deadline = time.Now().Add(archiveCatalogInterval + 2*time.Second)
	for !found.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !found.Load() || catalogs.Load() < 2 {
		t.Fatal("coalesced wake lost appended evidence", catalogs.Load())
	}
	if maintenance.Load() != 1 {
		t.Fatalf("ordinary append reloaded all checkpoints %d times", maintenance.Load())
	}
	// A new member still requires real catalog maintenance, even though ordinary
	// writes to existing members only advance their own source checkpoints.
	newPath := filepath.Join(filepath.Dir(path), "rollout-added.jsonl")
	if err := os.WriteFile(newPath, []byte("{\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"role\":\"user\",\"content\":[{\"type\":\"input_text\",\"text\":\"new catalog member\"}]}}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	app.observer.evidenceIndex.requestReconcile()
	app.notifyArchive()
	deadline = time.Now().Add(archiveCatalogInterval + 2*time.Second)
	for maintenance.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if maintenance.Load() != 2 {
		t.Fatal("new member did not maintain authorized catalog", maintenance.Load())
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("coalesced timer delayed shutdown")
	}
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
