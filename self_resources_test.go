package main

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

func TestSelfCPUIntervalAndUnknown(t *testing.T) {
	if value := selfCPUPercent(1e9, 4e9, 2*time.Second); value == nil || *value != 150 {
		t.Fatalf("one-core CPU = %v", value)
	}
	if value := selfCPUPercent(1, 1, time.Second); value == nil || *value != 0 {
		t.Fatal("measured zero was missing")
	}
	if selfCPUPercent(2, 1, time.Second) != nil || selfCPUPercent(1, 2, 0) != nil {
		t.Fatal("invalid sample fabricated CPU")
	}
}

func TestSelfStorageOwnershipAllocationAndDedup(t *testing.T) {
	root := t.TempDir()
	history := filepath.Join(root, "history.jsonl")
	files := []string{"history.jsonl", "history.2026-09.jsonl.gz", "lifecycle.jsonl", "throughput.jsonl", "history.jsonl.trajectory/trajectory.sqlite", "history.jsonl.throughput-index/usage.bbolt", "watcher/state"}
	var expectedAllocated, expectedLogical int64
	for _, name := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("useful retained data"), 0600); err != nil {
			t.Fatal(err)
		}
		info, _ := os.Lstat(path)
		expectedAllocated += info.Sys().(*syscall.Stat_t).Blocks * 512
		expectedLogical += info.Size()
	}
	// Unrelated sources in a custom parent are not app-owned, and a hard link
	// is one physical file rather than two copies of the index.
	if err := os.WriteFile(filepath.Join(root, "session.jsonl"), []byte("original session"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, files[4]), filepath.Join(root, "history.jsonl.trajectory/linked")); err != nil {
		t.Fatal(err)
	}
	got := scanSelfStorage(history, time.Now(), 4096, time.Second)
	if !got.Complete || got.AllocatedBytes == nil || *got.AllocatedBytes != expectedAllocated || *got.LogicalBytes != expectedLogical {
		t.Fatalf("allocation mismatch: %+v", got)
	}
	count := 0
	for _, component := range got.Components {
		if component.Key == "self_monitor" && (component.AllocatedBytes == nil || *component.AllocatedBytes != 0) {
			t.Fatal("sampler invented disk use")
		}
		for _, file := range component.Files {
			count++
			if filepath.IsAbs(file.Name) || file.Name == "session.jsonl" {
				t.Fatal("source/path leak")
			}
		}
	}
	if count != len(files) {
		t.Fatalf("file attribution: %d", count)
	}
}

func TestSelfStorageMissingSymlinkAndBudgetRemainUnknown(t *testing.T) {
	root := t.TempDir()
	history := filepath.Join(root, "history.jsonl")
	missing := scanSelfStorage(filepath.Join(root, "missing", "history.jsonl"), time.Now(), 4096, time.Second)
	if missing.Complete || missing.AllocatedBytes != nil {
		t.Fatal("missing folder fabricated zero")
	}
	for _, c := range missing.Components {
		if c.Key != "self_monitor" && c.AllocatedBytes != nil {
			t.Fatal("missing component fabricated zero")
		}
	}
	if err := os.Symlink(root, history+".trajectory"); err != nil {
		t.Fatal(err)
	}
	linked := scanSelfStorage(history, time.Now(), 4096, time.Second)
	if linked.Complete || linked.AllocatedBytes != nil || len(linked.Gaps) != 1 || linked.Gaps[0] != "storage_symlink_excluded" {
		t.Fatalf("followed symlink: %+v", linked)
	}
	limited := scanSelfStorage(history, time.Now(), 0, time.Second)
	if limited.Complete || limited.AllocatedBytes != nil {
		t.Fatal("budget counted partial as complete")
	}
}

func TestSelfSamplerDoesNotWriteAndKeepsStorageCache(t *testing.T) {
	root := t.TempDir()
	history := filepath.Join(root, "history.jsonl")
	if err := os.WriteFile(history, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadDir(root)
	s := selfResourceSampler{}
	first := s.sample(history)
	if first.CPUPercent != nil || first.Monitor.PersistentBytes != 0 {
		t.Fatal("first sample fabricated CPU or wrote monitoring data")
	}
	second := s.sample(history)
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two-second cache resampled")
	}
	s.at = time.Now().Add(-3 * time.Second)
	third := s.sample(history)
	if third.SampledAt == first.SampledAt || third.Storage.SampledAt != first.Storage.SampledAt {
		t.Fatal("storage cache period differs")
	}
	after, _ := os.ReadDir(root)
	if len(before) != len(after) {
		t.Fatal("self monitoring wrote files")
	}
	data, _ := os.ReadFile(history)
	if string(data) != "retained" {
		t.Fatal("self monitoring changed history")
	}
}

func TestSelfResourceAPIReadOnlyAndHostGuard(t *testing.T) {
	a := &trayApp{cfg: Config{HistoryFile: filepath.Join(t.TempDir(), "history.jsonl")}}
	for _, item := range []struct {
		method, host string
		status       int
	}{{http.MethodPost, "127.0.0.1:8642", 405}, {http.MethodHead, "127.0.0.1:8642", 200}, {http.MethodGet, "evil.example", 403}} {
		r := httptest.NewRequest(item.method, "http://"+item.host+"/api/self-resources", nil)
		w := httptest.NewRecorder()
		a.handler().ServeHTTP(w, r)
		if w.Code != item.status {
			t.Fatalf("%s %s: %d", item.method, item.host, w.Code)
		}
	}
	if !a.selfResources.at.IsZero() {
		t.Fatal("HEAD or rejected request collected data")
	}
	w := httptest.NewRecorder()
	a.handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8642/api/self-resources", nil))
	var got snapshot.SelfResourceSnapshot
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.PID != os.Getpid() || got.CPUPercent != nil {
		t.Fatal("first API sample identity/CPU differs")
	}
}
