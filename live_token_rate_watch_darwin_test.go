//go:build darwin

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveTokenRateWatcherReportsNestedJSONLChanges(t *testing.T) {
	root := t.TempDir()
	watcher := newLiveTokenRateWatcher([]liveTokenRateRoot{{Tool: "codex", Path: root}})
	if watcher == nil {
		t.Fatal("failed to start FSEvents watcher")
	}
	t.Cleanup(watcher.Stop)

	time.Sleep(100 * time.Millisecond)
	path := filepath.Join(root, "sessions", "2026", "08", "02", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	want := canonicalLiveTokenRatePath(path)

	timeout := time.After(6 * time.Second)
	seen := []string{}
	for {
		select {
		case batch := <-watcher.Events():
			seen = append(seen, batch.Paths...)
			for _, changed := range batch.Paths {
				if changed == want {
					return
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for JSONL event for %s; saw %v", want, seen)
		}
	}
}
