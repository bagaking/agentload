package main

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Metadata only. No database reads, child processes or persistent sampling log.
type selfResourceSampler struct {
	mu        sync.Mutex
	latest    snapshot.SelfResourceSnapshot
	at        time.Time
	cpu       uint64
	cpuAt     time.Time
	storageAt time.Time
}

func selfCPUPercent(previous, current uint64, elapsed time.Duration) *float64 {
	if elapsed <= 0 || current < previous {
		return nil
	}
	value := float64(current-previous) / float64(elapsed) * 100
	return &value
}

func (s *selfResourceSampler) sample(history string) snapshot.SelfResourceSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	started := time.Now()
	if !s.at.IsZero() && started.Sub(s.at) < 2*time.Second {
		return s.latest
	}
	out := snapshot.SelfResourceSnapshot{PID: os.Getpid(), SampledAt: started.Format(time.RFC3339Nano)}
	cpu, rss, ok := selfProcessCounters()
	if ok {
		out.RSSBytes = &rss
		if !s.cpuAt.IsZero() {
			window := started.Sub(s.cpuAt)
			out.CPUPercent = selfCPUPercent(s.cpu, cpu, window)
			out.CPUWindowSeconds = window.Seconds()
		}
		s.cpu, s.cpuAt = cpu, started
	} else {
		s.cpuAt = time.Time{}
	}
	if s.storageAt.IsZero() || started.Sub(s.storageAt) >= time.Minute {
		out.Storage = scanSelfStorage(history, started, 4096, 150*time.Millisecond)
		s.storageAt = started
	} else {
		out.Storage = s.latest.Storage
	}
	out.Monitor.SampleMS = float64(time.Since(started)) / float64(time.Millisecond)
	s.latest, s.at = out, started
	return out
}

func scanSelfStorage(history string, now time.Time, maxEntries int, budget time.Duration) snapshot.SelfStorageSnapshot {
	out := snapshot.SelfStorageSnapshot{SampledAt: now.Format(time.RFC3339Nano), Complete: true}
	root := filepath.Dir(history)
	if history == "" {
		out.Complete = false
		out.Gaps = []string{"storage_path_unavailable"}
		return out
	}
	var volume unix.Statfs_t
	if unix.Statfs(root, &volume) == nil {
		available := uint64(volume.Bavail) * uint64(volume.Bsize)
		out.AvailableBytes = &available
	}
	components := map[string]*snapshot.SelfStorageComponent{}
	for _, key := range []string{"trajectory", "throughput", "history", "other", "self_monitor"} {
		components[key] = &snapshot.SelfStorageComponent{Key: key, Complete: true, Files: []snapshot.SelfStorageFile{}}
	}
	gap := func(key, code string) {
		out.Complete = false
		components[key].Complete = false
		for _, existing := range out.Gaps {
			if existing == code {
				return
			}
		}
		out.Gaps = append(out.Gaps, code)
	}
	dedicated := filepath.Clean(root) == filepath.Clean(filepath.Dir(defaultHistoryFile()))
	classify := func(name string) string {
		if name == filepath.Base(history)+".trajectory" {
			return "trajectory"
		}
		if name == filepath.Base(history)+".throughput-index" || name == "throughput.jsonl" || name == "throughput.jsonl.lock" || strings.HasPrefix(name, "throughput.") && strings.HasSuffix(name, ".jsonl.gz") {
			return "throughput"
		}
		stem := strings.TrimSuffix(filepath.Base(history), ".jsonl")
		if name == filepath.Base(history) || strings.HasPrefix(name, filepath.Base(history)+".") || name == "watcher" || name == "lifecycle.jsonl" || name == "lifecycle.jsonl.lock" || strings.HasPrefix(name, "lifecycle.") && strings.HasSuffix(name, ".jsonl.gz") || strings.HasPrefix(name, stem+".") && strings.HasSuffix(name, ".jsonl.gz") {
			return "history"
		}
		if dedicated {
			return "other"
		}
		return ""
	}
	deadline := time.Now().Add(budget)
	directory, err := os.Open(root)
	var entries []fs.DirEntry
	if err == nil {
		entries, err = directory.ReadDir(maxEntries + 1)
		_ = directory.Close()
		if errors.Is(err, io.EOF) {
			err = nil
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	}
	if err != nil {
		gap("other", "storage_inventory_unavailable")
		for key := range components {
			if key != "self_monitor" {
				components[key].Complete = false
			}
		}
	}
	if len(entries) > maxEntries {
		gap("other", "storage_scan_budget")
		for key := range components {
			if key != "self_monitor" {
				components[key].Complete = false
			}
		}
		entries = entries[:maxEntries]
	}
	visited := 0
	seen := map[[2]uint64]bool{}
	for _, entry := range entries {
		key := classify(entry.Name())
		if key == "" {
			continue
		}
		component := components[key]
		err := filepath.WalkDir(filepath.Join(root, entry.Name()), func(path string, entry fs.DirEntry, err error) error {
			visited++
			if visited > maxEntries || time.Now().After(deadline) {
				gap(key, "storage_scan_budget")
				return fs.SkipAll
			}
			if err != nil {
				gap(key, "storage_metadata_unavailable")
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				gap(key, "storage_metadata_unavailable")
				return nil
			}
			if info.Mode()&os.ModeSymlink != 0 {
				gap(key, "storage_symlink_excluded")
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok || stat.Blocks < 0 {
				gap(key, "storage_allocation_unavailable")
				return nil
			}
			identity := [2]uint64{uint64(stat.Dev), uint64(stat.Ino)}
			if seen[identity] {
				return nil
			}
			seen[identity] = true
			relative, _ := filepath.Rel(root, path)
			component.Files = append(component.Files, snapshot.SelfStorageFile{Name: relative, LogicalBytes: info.Size(), AllocatedBytes: stat.Blocks * 512})
			return nil
		})
		if err != nil {
			gap(key, "storage_metadata_unavailable")
		}
	}
	var totalAllocated, totalLogical int64
	for _, key := range []string{"trajectory", "throughput", "history", "other", "self_monitor"} {
		component := components[key]
		sort.Slice(component.Files, func(i, j int) bool { return component.Files[i].AllocatedBytes > component.Files[j].AllocatedBytes })
		var allocated, logical int64
		for _, file := range component.Files {
			allocated += file.AllocatedBytes
			logical += file.LogicalBytes
		}
		if component.Complete {
			component.AllocatedBytes = &allocated
			component.LogicalBytes = &logical
		}
		totalAllocated += allocated
		totalLogical += logical
		out.Components = append(out.Components, *component)
	}
	if out.Complete {
		out.AllocatedBytes = &totalAllocated
		out.LogicalBytes = &totalLogical
	}
	return out
}

func (a *trayApp) handleSelfResourcesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(a.selfResources.sample(a.cfg.HistoryFile))
}
