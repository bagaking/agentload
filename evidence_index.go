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
	// AgedOutFiles counts transcripts the last walk found but excluded as older
	// than the configured history horizon. Those are out of scope rather than
	// deferred, so this is diagnostic only and must not reach DeferredFiles --
	// on this dev machine it is ~11k files against 2.8k genuinely deferred.
	AgedOutFiles int
	Reconciled   bool
	Elapsed      time.Duration
}

type transcriptEvidenceSnapshot struct {
	Files    []discoveredTranscriptFile
	Errors   []string
	Complete bool
	Stats    transcriptEvidenceIndexStats
	// FilteredByCutoff counts files the index holds -- so within the history
	// horizon -- that the foreground cutoff excluded from Files. These are the
	// genuinely deferred ones: in scope, on disk, and not scanned this pass.
	// Files the walk never admitted are outside the horizon, not a gap.
	FilteredByCutoff int
	// Revision identifies the exact index state represented by Files. Consumers
	// compare it after parsing so a watcher mutation cannot be published as a
	// complete scan when it landed between collection and parse completion.
	Revision uint64
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
	revision          uint64
	errors            []string
	lastStats         transcriptEvidenceIndexStats

	lifecycleMu    sync.Mutex
	running        bool
	stop           chan struct{}
	done           chan struct{}
	watcher        evidenceWatcher
	watcherFactory func([]string) evidenceWatcher
	watcherEnabled bool
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
	index.startWithWatcher(newEvidenceWatcher)
}

// startWithWatcher owns the watcher lifecycle decision with the platform
// constructor injected, so the no-watcher branch is reachable in tests on every
// platform.
func (index *transcriptEvidenceIndex) startWithWatcher(construct func([]string) evidenceWatcher) {
	if index == nil || construct == nil {
		return
	}
	index.syncRoots()
	index.installWatcher(construct, true)
}

// restartWatcherIfNeeded restores incremental coverage after a watcher exits
// unexpectedly. An explicit stop disables this path; a later snapshot may only
// restart a watcher that was previously enabled by startWithWatcher.
func (index *transcriptEvidenceIndex) restartWatcherIfNeeded() {
	if index == nil {
		return
	}
	index.installWatcher(nil, false)
}

func (index *transcriptEvidenceIndex) installWatcher(construct func([]string) evidenceWatcher, enable bool) {
	if index == nil {
		return
	}
	index.lifecycleMu.Lock()
	if construct != nil {
		index.watcherFactory = construct
		index.watcherEnabled = enable
	}
	if index.running {
		index.lifecycleMu.Unlock()
		return
	}
	if !index.watcherEnabled || index.watcherFactory == nil {
		index.lifecycleMu.Unlock()
		return
	}
	factory := index.watcherFactory
	watchPaths := canonicalEvidenceWatchPaths(index.adapters.roots())
	var watcher evidenceWatcher
	constructed := false
	// A platform watcher constructor is an external boundary. Keep a bad
	// constructor from escaping through Snapshot, while preserving the enabled
	// state so the next snapshot can retry construction.
	runBackgroundStep("transcript evidence watcher start", func() {
		watcher = factory(watchPaths)
		constructed = true
	})
	if !constructed {
		index.lifecycleMu.Unlock()
		index.markGap("transcript evidence watcher failed to start")
		return
	}
	if watcher == nil {
		// No platform watcher: claim no lifecycle state at all. Marking the
		// index running with no goroutine behind it would leave running=true
		// forever, so a later start could never install a watcher, and it would
		// hide the fact that incremental coverage is unavailable. Also disable
		// automatic restart: a constructor that cannot provide a watcher is a
		// capability gap, not an unexpected exit, and retrying it on every token
		// poll would force needless full reconciliations.
		index.watcherEnabled = false
		index.lifecycleMu.Unlock()
		index.markGap("transcript evidence watcher is unavailable on this platform")
		return
	}
	index.running = true
	index.watcher = watcher
	index.stop = make(chan struct{})
	index.done = make(chan struct{})
	stop := index.stop
	done := index.done
	index.lifecycleMu.Unlock()
	go func() {
		// Two layers, and both are load-bearing. The outer recover is the
		// backstop for the parts of this boundary that are not a batch step:
		// watcher.Events() receive, markGap, and the deferred release itself.
		// The inner per-batch step is what actually restores service, by
		// dropping one bad batch instead of ending the watcher.
		defer recoverBackgroundPanic("transcript evidence index watcher")
		defer index.releaseWatcherLifecycle(done)
		for {
			select {
			case batch, ok := <-watcher.Events():
				if !ok {
					index.markGap("transcript evidence watcher closed")
					return
				}
				index.recordWatchBatchContained(batch)
			case <-stop:
				return
			}
		}
	}()
}

// releaseWatcherLifecycle closes the watcher's done channel and clears the
// running flag when the watcher goroutine exits for any reason. Without the flag
// reset a panic that escaped the loop would leave running=true, so start() would
// no-op forever and the index would never receive incremental updates again.
// close(done) is exactly-once because this runs as a single deferred call per
// watcher goroutine, and stopIndex only ever waits on done — it never closes it.
// Stop() is contained and skipped when stopIndex already claimed the watcher, so
// a panicking watcher cannot strand the close and Stop is never called twice.
func (index *transcriptEvidenceIndex) releaseWatcherLifecycle(done chan struct{}) {
	// Deferred: stopIndex blocks on <-done, so a panic in the bookkeeping below
	// must not skip the close or shutdown would hang forever.
	defer close(done)
	unexpected := false
	index.lifecycleMu.Lock()
	watcher := index.watcher
	if index.running && index.done == done {
		index.running = false
		index.stop = nil
		index.done = nil
		index.watcher = nil
		unexpected = index.watcherEnabled
	} else {
		// stopIndex already took ownership and stops the watcher itself;
		// stopping it again here would be a double Stop.
		watcher = nil
	}
	index.lifecycleMu.Unlock()
	if unexpected {
		index.markGap("transcript evidence watcher exited unexpectedly")
	}
	if watcher != nil {
		runBackgroundStep("transcript evidence watcher stop", watcher.Stop)
	}
}

func (index *transcriptEvidenceIndex) stopIndex() {
	if index == nil {
		return
	}
	index.lifecycleMu.Lock()
	if !index.running {
		index.watcherEnabled = false
		index.lifecycleMu.Unlock()
		return
	}
	stop := index.stop
	done := index.done
	watcher := index.watcher
	index.running = false
	index.watcherEnabled = false
	index.stop = nil
	index.done = nil
	index.watcher = nil
	close(stop)
	index.lifecycleMu.Unlock()
	// Stop is contained and the wait is deferred: a panicking watcher must not
	// skip <-done, or stopIndex returns while the watcher goroutine is still
	// live and onExit records shutdown_complete over a running goroutine.
	defer func() { <-done }()
	if watcher != nil {
		runBackgroundStep("transcript evidence watcher stop", watcher.Stop)
	}
}

func (index *transcriptEvidenceIndex) snapshot(ctx context.Context, cutoff time.Time, priority []TranscriptFile) transcriptEvidenceSnapshot {
	if index == nil {
		return transcriptEvidenceSnapshot{Errors: []string{"transcript evidence index is unavailable"}}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	index.syncRoots()
	if ctx.Err() == nil {
		index.restartWatcherIfNeeded()
	}
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

		// The reconcile work runs contained: discoverTranscripts walks the
		// filesystem and the commit merges maps, both real panic surfaces. A
		// panic must not reach snapshot()'s callers (a tray refresh or the token
		// sampler), and the published flight must always be released, or callers
		// whose context never cancels would block forever and the index would
		// never reconcile again. An aborted attempt releases the flight as a
		// gap so the next snapshot retries instead of trusting stale coverage.
		committed := false
		func() {
			defer func() {
				if committed {
					return
				}
				index.releaseReconcileFlight(flight)
			}()
			runBackgroundStep("transcript evidence reconciliation", func() {
				committed = index.reconcileLocked(ctx, cutoff, flight, rootsKey, gapGeneration)
			})
		}()
		if !committed {
			// Report the aborted attempt as incomplete coverage rather than
			// presenting the pre-existing index as a fresh, trustworthy scan.
			reportGap = true
			break
		}
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

// reconcileLocked runs one full reconciliation and commits it, reporting whether
// the commit happened. The caller publishes `flight` before calling and releases
// it when this returns false, so an aborted attempt never leaves waiters stuck.
func (index *transcriptEvidenceIndex) reconcileLocked(ctx context.Context, cutoff time.Time, flight chan struct{}, rootsKey string, gapGeneration uint64) bool {
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

	committed := false
	index.mu.Lock()
	// Deferred unlock so a panic while merging cannot strand index.mu; the
	// caller's release then still gets the lock it needs to clear the guard.
	defer index.mu.Unlock()
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
		PrunedDirectories: discovered.PrunedDirectories, AgedOutFiles: discovered.AgedOutFiles,
		Reconciled: true, Elapsed: elapsed,
	}
	index.reconciling = nil
	close(flight)
	committed = true
	return committed
}

// releaseReconcileFlight clears an in-flight reconciliation that ended without
// committing and wakes everyone waiting on it. Callers waiting on the flight
// channel have no other signal, so an aborted reconcile that never released it
// would block callers whose context never cancels and leave the index unable to
// reconcile again. The flight is closed exactly once: reconcileLocked closes it
// on the committed path and this runs only when that did not happen, and the
// ownership check means a flight already superseded by another owner is left
// alone rather than closed twice.
func (index *transcriptEvidenceIndex) releaseReconcileFlight(flight chan struct{}) {
	index.mu.Lock()
	owns := index.reconciling == flight
	if owns {
		index.reconciling = nil
		index.markGapLocked("transcript evidence reconciliation aborted")
	}
	index.mu.Unlock()
	if !owns {
		// Another owner published a different flight; closing this one is not
		// ours to do and could close an already-closed channel.
		return
	}
	close(flight)
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

	var files []discoveredTranscriptFile
	var errors []string
	var complete bool
	var stats transcriptEvidenceIndexStats
	var revision uint64
	filteredByCutoff := 0
	index.mu.Lock()
	// Deferred unlock: this block dereferences file.Info while holding index.mu,
	// so a malformed entry must not strand the lock every snapshot reader, the
	// watcher's mutation path and the reconcile release all need.
	func() {
		defer index.mu.Unlock()
		if index.files == nil {
			index.files = make(map[string]discoveredTranscriptFile)
		}
		for path, file := range priorityFiles {
			previous, existed := index.files[path]
			if !existed || previous.Info == nil || file.Info == nil ||
				!os.SameFile(previous.Info, file.Info) ||
				previous.Info.Size() != file.Info.Size() ||
				!previous.Info.ModTime().Equal(file.Info.ModTime()) {
				index.revision++
			}
			index.files[path] = file
		}
		files = make([]discoveredTranscriptFile, 0, len(index.files)+len(priorityFiles))
		for path, file := range index.files {
			_, isPriority := priorityFiles[path]
			if file.Info == nil {
				// An entry without stat info cannot be ordered or filtered;
				// drop it and force a reconcile instead of panicking here.
				delete(index.files, path)
				index.markGapLocked("transcript evidence entry is missing file info")
				continue
			}
			if !isPriority && !cutoff.IsZero() && file.Info.ModTime().Before(cutoff) {
				// Counted here because the index still holds the entry: the walk
				// admitted it and it has aged since. A file the walk itself
				// skipped is counted there instead, so neither is missed nor
				// double counted.
				filteredByCutoff++
				continue
			}
			files = append(files, file)
		}
		errors = append([]string(nil), index.errors...)
		complete = index.complete
		stats = index.lastStats
		stats.CandidateCount = len(files)
		stats.Reconciled = reconciled
		if !reconciled {
			stats.VisitedEntries = 0
			stats.PrunedDirectories = 0
			stats.Elapsed = 0
		}
		revision = index.revision
	}()

	sort.Slice(files, func(i, j int) bool {
		if !files[i].Info.ModTime().Equal(files[j].Info.ModTime()) {
			return files[i].Info.ModTime().After(files[j].Info.ModTime())
		}
		if files[i].File.Tool == files[j].File.Tool {
			return files[i].File.Path < files[j].File.Path
		}
		return files[i].File.Tool < files[j].File.Tool
	})
	return transcriptEvidenceSnapshot{Files: files, Errors: errors, Complete: complete, Stats: stats, FilteredByCutoff: filteredByCutoff, Revision: revision}
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
		index.revision++
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
		updated := false
		runBackgroundStep("transcript evidence watcher update", func() {
			updated = watcher.Update(watchPaths)
		})
		if !updated {
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

// recordWatchBatchContained keeps a panic while recording one batch from ending
// the watcher loop. A dropped batch is lost coverage, so the abort marks an
// evidence gap: the next snapshot then reconciles by full scan instead of
// silently serving an index that is missing those events.
func (index *transcriptEvidenceIndex) recordWatchBatchContained(batch evidenceWatchBatch) {
	recorded := false
	defer func() {
		if !recorded {
			index.markGap("transcript evidence watcher batch aborted")
		}
	}()
	runBackgroundStep("transcript evidence index watcher", func() {
		index.recordWatchBatch(batch)
		recorded = true
	})
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
		index.revision++
		return
	}
	if mutation.Deleted {
		delete(index.files, path)
	} else {
		index.files[path] = mutation.File
	}
	index.revision++
}

func (index *transcriptEvidenceIndex) markGap(reason string) {
	index.mu.Lock()
	index.markGapLocked(reason)
	index.mu.Unlock()
}

func (index *transcriptEvidenceIndex) markGapLocked(reason string) {
	index.gapGeneration++
	index.revision++
	index.reconcileRequired = true
	index.complete = false
	reason = strings.TrimSpace(reason)
	if reason != "" {
		index.errors = []string{reason}
	}
}

func (index *transcriptEvidenceIndex) cacheRevision() uint64 {
	if index == nil {
		return 0
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	return index.revision
}
