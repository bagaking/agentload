package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const transcriptEvidenceMaxPendingMutations = 4096

type transcriptEvidenceIndexStats struct {
	CandidateCount    int
	VisitedEntries    int
	PrunedDirectories int
	Reconciled        bool
	Elapsed           time.Duration
}

type transcriptEvidenceSnapshot struct {
	Files    []discoveredTranscriptFile
	Errors   []string
	Complete bool
	Stats    transcriptEvidenceIndexStats
}

type transcriptEvidenceMutation struct {
	File    discoveredTranscriptFile
	Deleted bool
}

type transcriptEvidenceIndex struct {
	adapters *codingAgentRegistry

	mu                sync.Mutex
	files             map[string]discoveredTranscriptFile
	mutations         map[string]transcriptEvidenceMutation
	rootsKey          string
	coverageCutoff    time.Time
	initialized       bool
	complete          bool
	reconcileRequired bool
	reconciling       chan struct{}
	gapGeneration     uint64
	errors            []string
	lastStats         transcriptEvidenceIndexStats

	lifecycleMu sync.Mutex
	running     bool
	stop        chan struct{}
	done        chan struct{}
	watcher     evidenceWatcher
}

func newTranscriptEvidenceIndex(adapters *codingAgentRegistry) *transcriptEvidenceIndex {
	if adapters == nil {
		panic("coding agent registry is required")
	}
	return &transcriptEvidenceIndex{
		adapters:  adapters,
		files:     map[string]discoveredTranscriptFile{},
		mutations: map[string]transcriptEvidenceMutation{},
	}
}

func canonicalEvidencePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func (index *transcriptEvidenceIndex) start() {
	if index == nil {
		return
	}
	index.syncRoots()
	index.lifecycleMu.Lock()
	if index.running {
		index.lifecycleMu.Unlock()
		return
	}
	watcher := newEvidenceWatcher(canonicalEvidenceWatchPaths(index.adapters.roots()))
	index.running = true
	index.watcher = watcher
	index.stop = make(chan struct{})
	index.done = make(chan struct{})
	stop := index.stop
	done := index.done
	index.lifecycleMu.Unlock()
	if watcher == nil {
		close(done)
		return
	}
	go func() {
		defer close(done)
		for {
			select {
			case batch, ok := <-watcher.Events():
				if !ok {
					index.markGap("transcript evidence watcher closed")
					return
				}
				index.recordWatchBatch(batch)
			case <-stop:
				return
			}
		}
	}()
}

func (index *transcriptEvidenceIndex) stopIndex() {
	if index == nil {
		return
	}
	index.lifecycleMu.Lock()
	if !index.running {
		index.lifecycleMu.Unlock()
		return
	}
	stop := index.stop
	done := index.done
	watcher := index.watcher
	index.running = false
	index.stop = nil
	index.done = nil
	index.watcher = nil
	close(stop)
	index.lifecycleMu.Unlock()
	if watcher != nil {
		watcher.Stop()
	}
	<-done
}

func (index *transcriptEvidenceIndex) snapshot(ctx context.Context, cutoff time.Time, priority []TranscriptFile) transcriptEvidenceSnapshot {
	if index == nil {
		return transcriptEvidenceSnapshot{Errors: []string{"transcript evidence index is unavailable"}}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	index.syncRoots()
	reconciled := false
	reportGap := false
	for {
		index.mu.Lock()
		needsReconcile := !index.initialized || index.reconcileRequired ||
			(!cutoff.IsZero() && (index.coverageCutoff.IsZero() || cutoff.Before(index.coverageCutoff)))
		if !needsReconcile {
			index.mu.Unlock()
			break
		}
		if flight := index.reconciling; flight != nil {
			index.mu.Unlock()
			select {
			case <-flight:
				continue
			case <-ctx.Done():
				return transcriptEvidenceSnapshot{
					Errors: []string{fmt.Sprintf("transcript evidence reconciliation wait cancelled: %v", ctx.Err())},
				}
			}
		}
		flight := make(chan struct{})
		reportGap = reportGap || (index.initialized && !index.complete)
		index.reconciling = flight
		rootsKey := index.rootsKey
		gapGeneration := index.gapGeneration
		index.mu.Unlock()

		started := time.Now()
		discovered := index.adapters.discoverTranscripts(ctx, cutoff)
		elapsed := time.Since(started)
		nextFiles := make(map[string]discoveredTranscriptFile, len(discovered.Files))
		for _, file := range discovered.Files {
			path := canonicalEvidencePath(file.File.Path)
			if path == "" || file.Info == nil || !index.adapters.hasTranscript(file.File.Tool) {
				continue
			}
			file.File.Path = filepath.Clean(file.File.Path)
			if _, owned := nextFiles[path]; !owned {
				nextFiles[path] = file
			}
		}

		index.mu.Lock()
		for path, mutation := range index.mutations {
			if mutation.Deleted {
				delete(nextFiles, path)
			} else {
				nextFiles[path] = mutation.File
			}
		}
		index.mutations = map[string]transcriptEvidenceMutation{}
		rootStable := rootsKey == index.rootsKey
		gapStable := gapGeneration == index.gapGeneration
		complete := ctx.Err() == nil && len(discovered.Errors) == 0 && rootStable && gapStable
		index.files = nextFiles
		index.coverageCutoff = cutoff
		index.initialized = true
		index.complete = complete
		index.reconcileRequired = !rootStable || !gapStable || ctx.Err() != nil || len(discovered.Errors) > 0
		index.errors = append([]string(nil), discovered.Errors...)
		if !rootStable {
			index.errors = append(index.errors, "transcript evidence roots changed during reconciliation")
		}
		if !gapStable {
			index.errors = append(index.errors, "transcript evidence watcher reported an incomplete interval during reconciliation")
		}
		index.lastStats = transcriptEvidenceIndexStats{
			CandidateCount: len(nextFiles), VisitedEntries: discovered.VisitedEntries,
			PrunedDirectories: discovered.PrunedDirectories, Reconciled: true, Elapsed: elapsed,
		}
		index.reconciling = nil
		close(flight)
		index.mu.Unlock()
		reconciled = true
		break
	}
	snapshot := index.currentSnapshot(cutoff, priority, reconciled)
	if reportGap {
		snapshot.Complete = false
		snapshot.Errors = append(snapshot.Errors, "transcript evidence index recovered after incomplete coverage")
	}
	return snapshot
}

func (index *transcriptEvidenceIndex) currentSnapshot(cutoff time.Time, priority []TranscriptFile, reconciled bool) transcriptEvidenceSnapshot {
	priorityFiles := make(map[string]discoveredTranscriptFile, len(priority))
	for _, file := range priority {
		if !index.adapters.hasTranscript(file.Tool) {
			continue
		}
		file.Path = filepath.Clean(file.Path)
		path := canonicalEvidencePath(file.Path)
		info, err := os.Stat(file.Path)
		if err != nil || info.IsDir() {
			continue
		}
		priorityFiles[path] = discoveredTranscriptFile{File: file, Info: info}
	}

	index.mu.Lock()
	for path, file := range priorityFiles {
		index.files[path] = file
	}
	files := make([]discoveredTranscriptFile, 0, len(index.files)+len(priorityFiles))
	for path, file := range index.files {
		_, isPriority := priorityFiles[path]
		if !isPriority && !cutoff.IsZero() && file.Info.ModTime().Before(cutoff) {
			continue
		}
		files = append(files, file)
	}
	errors := append([]string(nil), index.errors...)
	complete := index.complete
	stats := index.lastStats
	stats.CandidateCount = len(files)
	stats.Reconciled = reconciled
	if !reconciled {
		stats.VisitedEntries = 0
		stats.PrunedDirectories = 0
		stats.Elapsed = 0
	}
	index.mu.Unlock()

	sort.Slice(files, func(i, j int) bool {
		if !files[i].Info.ModTime().Equal(files[j].Info.ModTime()) {
			return files[i].Info.ModTime().After(files[j].Info.ModTime())
		}
		if files[i].File.Tool == files[j].File.Tool {
			return files[i].File.Path < files[j].File.Path
		}
		return files[i].File.Tool < files[j].File.Tool
	})
	return transcriptEvidenceSnapshot{Files: files, Errors: errors, Complete: complete, Stats: stats}
}

func (index *transcriptEvidenceIndex) syncRoots() {
	if index == nil {
		return
	}
	roots := index.adapters.roots()
	key := registryRootsCacheKey(canonicalEvidenceRoots(roots))
	index.mu.Lock()
	changed := key != index.rootsKey
	if changed {
		index.rootsKey = key
		index.reconcileRequired = true
		index.complete = false
		index.gapGeneration++
	}
	index.mu.Unlock()
	if !changed {
		return
	}
	watchPaths := canonicalEvidenceWatchPaths(roots)
	index.lifecycleMu.Lock()
	watcher := index.watcher
	index.lifecycleMu.Unlock()
	if watcher != nil {
		if !watcher.Update(watchPaths) {
			index.markGap("transcript evidence watcher could not cover all configured roots")
		}
	}
}

func canonicalEvidenceRoots(roots map[string][]string) map[string][]string {
	canonical := make(map[string][]string, len(roots))
	for agentID, agentRoots := range roots {
		for _, root := range agentRoots {
			if path := canonicalEvidencePath(root); path != "" && path != "." {
				canonical[agentID] = append(canonical[agentID], path)
			}
		}
		canonical[agentID] = mergeStringSets(nil, canonical[agentID])
	}
	return canonical
}

func (index *transcriptEvidenceIndex) recordWatchBatch(batch evidenceWatchBatch) {
	if index == nil {
		return
	}
	if !batch.Complete {
		index.markGap("transcript evidence watcher reported incomplete coverage")
	}
	for _, rawPath := range batch.Paths {
		path := canonicalEvidencePath(rawPath)
		file, ok := index.adapters.transcriptFileForEvidencePath(path)
		if !ok {
			continue
		}
		info, err := os.Stat(path)
		mutation := transcriptEvidenceMutation{Deleted: os.IsNotExist(err)}
		if err == nil && !info.IsDir() {
			mutation.File = discoveredTranscriptFile{File: file, Info: info}
		} else if !mutation.Deleted {
			index.markGap(fmt.Sprintf("transcript evidence stat failed: %v", err))
			continue
		}
		index.applyMutation(path, mutation)
	}
}

func (index *transcriptEvidenceIndex) applyMutation(path string, mutation transcriptEvidenceMutation) {
	index.mu.Lock()
	defer index.mu.Unlock()
	if index.reconciling != nil {
		if _, exists := index.mutations[path]; exists || len(index.mutations) < transcriptEvidenceMaxPendingMutations {
			index.mutations[path] = mutation
		} else {
			index.markGapLocked("transcript evidence mutation capacity exceeded during reconciliation")
		}
	}
	if !index.initialized {
		return
	}
	if mutation.Deleted {
		delete(index.files, path)
	} else {
		index.files[path] = mutation.File
	}
}

func (index *transcriptEvidenceIndex) markGap(reason string) {
	index.mu.Lock()
	index.markGapLocked(reason)
	index.mu.Unlock()
}

func (index *transcriptEvidenceIndex) markGapLocked(reason string) {
	index.gapGeneration++
	index.reconcileRequired = true
	index.complete = false
	reason = strings.TrimSpace(reason)
	if reason != "" {
		index.errors = []string{reason}
	}
}
