package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestTranscriptEvidenceIndexReconcilesOnceThenAppliesDirtyPaths(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	firstPath := filepath.Join(day, "first.jsonl")
	writeDiscoveryFixture(t, firstPath, now)

	registry, discovery := countingCodexRegistry(Config{CodexRoots: []string{root}}, nil)
	index := newTranscriptEvidenceIndex(registry)
	cutoff := now.Add(-time.Hour)
	cold := index.snapshot(context.Background(), cutoff, nil)
	if !cold.Complete || !cold.Stats.Reconciled || cold.Stats.VisitedEntries == 0 || len(cold.Files) != 1 {
		t.Fatalf("cold index snapshot = %+v", cold)
	}
	warm := index.snapshot(context.Background(), cutoff, nil)
	if !warm.Complete || warm.Stats.Reconciled || warm.Stats.VisitedEntries != 0 || len(warm.Files) != 1 {
		t.Fatalf("warm index snapshot = %+v", warm)
	}
	if calls := discovery.callCount(); calls != 1 {
		t.Fatalf("warm snapshot launched %d discovery walks, want 1 total", calls)
	}

	secondPath := filepath.Join(day, "second.jsonl")
	writeDiscoveryFixture(t, secondPath, now.Add(time.Minute))
	index.recordWatchBatch(evidenceWatchBatch{Complete: true, Paths: []string{secondPath}})
	updated := index.snapshot(context.Background(), cutoff, nil)
	if !updated.Complete || updated.Stats.Reconciled || len(updated.Files) != 2 {
		t.Fatalf("dirty-path index snapshot = %+v", updated)
	}
	if calls := discovery.callCount(); calls != 1 {
		t.Fatalf("dirty-path update launched a second walk: %d", calls)
	}
}

func TestTranscriptEvidenceIndexWatchGapTriggersOneReconciliation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	path := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), "session.jsonl")
	writeDiscoveryFixture(t, path, now)
	registry, discovery := countingCodexRegistry(Config{CodexRoots: []string{root}}, nil)
	index := newTranscriptEvidenceIndex(registry)
	cutoff := now.Add(-time.Hour)
	if initial := index.snapshot(context.Background(), cutoff, nil); !initial.Complete {
		t.Fatalf("initial index snapshot = %+v", initial)
	}

	index.recordWatchBatch(evidenceWatchBatch{Complete: false})
	recovered := index.snapshot(context.Background(), cutoff, nil)
	if recovered.Complete || !recovered.Stats.Reconciled {
		t.Fatalf("gap recovery did not expose incomplete coverage: %+v", recovered)
	}
	stable := index.snapshot(context.Background(), cutoff, nil)
	if !stable.Complete || stable.Stats.Reconciled {
		t.Fatalf("post-recovery index snapshot = %+v", stable)
	}
	if calls := discovery.callCount(); calls != 2 {
		t.Fatalf("watch gap launched %d walks, want initial plus one recovery", calls)
	}
}

func TestTranscriptEvidenceIndexRetriesDiscoveryErrors(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	path := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), "session.jsonl")
	writeDiscoveryFixture(t, path, now)
	registry := defaultCodingAgentRegistry(Config{CodexRoots: []string{root}})
	registry.mu.Lock()
	adapterIndex := registry.byID["codex"]
	flaky := &flakyTranscriptDiscovery{delegate: registry.adapters[adapterIndex].Capabilities.Discovery}
	registry.adapters[adapterIndex].Capabilities.Discovery = flaky
	registry.mu.Unlock()
	index := newTranscriptEvidenceIndex(registry)
	cutoff := now.Add(-time.Hour)

	first := index.snapshot(context.Background(), cutoff, nil)
	if first.Complete || !first.Stats.Reconciled || len(first.Errors) == 0 {
		t.Fatalf("discovery error did not fail closed: %+v", first)
	}
	second := index.snapshot(context.Background(), cutoff, nil)
	if second.Complete || !second.Stats.Reconciled || len(second.Files) != 1 || len(second.Errors) == 0 {
		t.Fatalf("discovery error did not retry: %+v", second)
	}
	third := index.snapshot(context.Background(), cutoff, nil)
	if !third.Complete || third.Stats.Reconciled {
		t.Fatalf("healthy recovery did not become warm: %+v", third)
	}
	if calls := flaky.callCount(); calls != 2 {
		t.Fatalf("discovery attempts = %d, want failed attempt plus recovery", calls)
	}
}

func TestTranscriptEvidenceIndexConcurrentReadersShareOneReconciliation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	path := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), "session.jsonl")
	writeDiscoveryFixture(t, path, now)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	registry, discovery := countingCodexRegistry(Config{CodexRoots: []string{root}}, &discoveryBlock{entered: entered, release: release})
	index := newTranscriptEvidenceIndex(registry)
	cutoff := now.Add(-time.Hour)

	results := make(chan transcriptEvidenceSnapshot, 2)
	go func() { results <- index.snapshot(context.Background(), cutoff, nil) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not start")
	}
	go func() { results <- index.snapshot(context.Background(), cutoff, nil) }()
	time.Sleep(20 * time.Millisecond)
	if calls := discovery.callCount(); calls != 1 {
		t.Fatalf("concurrent reader launched %d walks before release", calls)
	}
	close(release)
	for range 2 {
		if result := <-results; !result.Complete || len(result.Files) != 1 {
			t.Fatalf("concurrent index result = %+v", result)
		}
	}
	if calls := discovery.callCount(); calls != 1 {
		t.Fatalf("concurrent readers launched %d walks, want 1", calls)
	}
}

func TestTranscriptEvidenceIndexAppliesMutationThatArrivesDuringReconciliation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	firstPath := filepath.Join(day, "first.jsonl")
	secondPath := filepath.Join(day, "second.jsonl")
	writeDiscoveryFixture(t, firstPath, now)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	registry, _ := countingCodexRegistry(Config{CodexRoots: []string{root}}, &discoveryBlock{
		entered: entered,
		release: release,
		after:   true,
	})
	index := newTranscriptEvidenceIndex(registry)

	result := make(chan transcriptEvidenceSnapshot, 1)
	go func() {
		result <- index.snapshot(context.Background(), now.Add(-time.Hour), nil)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not finish its discovery walk")
	}
	writeDiscoveryFixture(t, secondPath, now.Add(time.Minute))
	index.recordWatchBatch(evidenceWatchBatch{Complete: true, Paths: []string{secondPath}})
	close(release)

	got := <-result
	if !got.Complete || len(got.Files) != 2 {
		t.Fatalf("reconciliation lost an in-flight mutation: %+v", got)
	}
}

func TestTranscriptEvidenceIndexBoundsMutationsDuringReconciliation(t *testing.T) {
	registry := defaultCodingAgentRegistry(Config{})
	index := newTranscriptEvidenceIndex(registry)
	index.mu.Lock()
	index.reconciling = make(chan struct{})
	index.complete = true
	index.mu.Unlock()

	for mutationIndex := 0; mutationIndex < transcriptEvidenceMaxPendingMutations+128; mutationIndex++ {
		path := fmt.Sprintf("evidence-%05d.jsonl", mutationIndex)
		index.applyMutation(path, transcriptEvidenceMutation{Deleted: true})
	}

	index.mu.Lock()
	pending := len(index.mutations)
	reconcileRequired := index.reconcileRequired
	complete := index.complete
	errors := append([]string(nil), index.errors...)
	index.mu.Unlock()
	if pending != transcriptEvidenceMaxPendingMutations {
		t.Fatalf("pending mutations = %d, want bounded capacity %d", pending, transcriptEvidenceMaxPendingMutations)
	}
	if !reconcileRequired || complete || len(errors) == 0 {
		t.Fatalf("mutation overflow did not fail closed: required=%t complete=%t errors=%v", reconcileRequired, complete, errors)
	}
}

func TestTranscriptEvidenceIndexWiderInitialCoverageAvoidsSecondWalk(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	path := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), "session.jsonl")
	writeDiscoveryFixture(t, path, now)
	registry, discovery := countingCodexRegistry(Config{CodexRoots: []string{root}}, nil)
	index := newTranscriptEvidenceIndex(registry)

	first := index.snapshot(context.Background(), now.Add(-foregroundTranscriptMaxLookback), nil)
	second := index.snapshot(context.Background(), now.Add(-foregroundTranscriptMinLookback), nil)
	if !first.Complete || !first.Stats.Reconciled || !second.Complete || second.Stats.Reconciled {
		t.Fatalf("shared coverage snapshots: first=%+v second=%+v", first, second)
	}
	if calls := discovery.callCount(); calls != 1 {
		t.Fatalf("wider initial coverage launched %d discovery walks, want 1", calls)
	}
}

func TestCanonicalEvidenceWatchPathsCollapseNestedAndSymlinkedRoots(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	paths := canonicalEvidenceWatchPaths(map[string][]string{
		"claude": {root, nested},
		"codex":  {root, alias},
	})
	if len(paths) != 1 || paths[0] != canonicalEvidencePath(root) {
		t.Fatalf("canonical watch paths = %#v, want one physical root", paths)
	}
}

func TestUniquePhysicalEvidenceRootsPreservesFirstConfiguredPath(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	got := uniquePhysicalEvidenceRoots([]string{alias, root, alias})
	if len(got) != 1 || got[0] != alias {
		t.Fatalf("unique physical roots = %#v, want first configured alias %q", got, alias)
	}
}

func BenchmarkTranscriptEvidenceIndexCold(b *testing.B) {
	now, registry := benchmarkEvidenceIndexFixture(b)
	cutoff := now.Add(-time.Hour)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		index := newTranscriptEvidenceIndex(registry)
		result := index.snapshot(context.Background(), cutoff, nil)
		if !result.Complete || len(result.Files) != 256 || !result.Stats.Reconciled {
			b.Fatalf("cold index result = %+v", result)
		}
		b.ReportMetric(float64(result.Stats.CandidateCount), "candidates/op")
		b.ReportMetric(float64(result.Stats.VisitedEntries), "entries/op")
	}
}

func BenchmarkTranscriptEvidenceIndexWarm(b *testing.B) {
	now, registry := benchmarkEvidenceIndexFixture(b)
	cutoff := now.Add(-time.Hour)
	index := newTranscriptEvidenceIndex(registry)
	if result := index.snapshot(context.Background(), cutoff, nil); !result.Complete {
		b.Fatalf("initial index result = %+v", result)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		result := index.snapshot(context.Background(), cutoff, nil)
		if !result.Complete || len(result.Files) != 256 || result.Stats.Reconciled {
			b.Fatalf("warm index result = %+v", result)
		}
		b.ReportMetric(float64(result.Stats.CandidateCount), "candidates/op")
		b.ReportMetric(float64(result.Stats.VisitedEntries), "entries/op")
	}
}

type discoveryBlock struct {
	entered chan struct{}
	release chan struct{}
	after   bool
}

type countingTranscriptDiscovery struct {
	delegate transcriptDiscoveryCapability
	block    *discoveryBlock
	mu       sync.Mutex
	calls    int
}

type flakyTranscriptDiscovery struct {
	delegate transcriptDiscoveryCapability
	mu       sync.Mutex
	calls    int
}

func (discovery *flakyTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	discovery.mu.Lock()
	discovery.calls++
	call := discovery.calls
	discovery.mu.Unlock()
	if call == 1 {
		return transcriptDiscoveryResult{Errors: []string{"temporary discovery failure"}}
	}
	return discovery.delegate.Discover(ctx, agentID, roots, cutoff)
}

func (discovery *flakyTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	return discovery.delegate.Classify(agentID, roots, path)
}

func (discovery *flakyTranscriptDiscovery) callCount() int {
	discovery.mu.Lock()
	defer discovery.mu.Unlock()
	return discovery.calls
}

func (discovery *countingTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	discovery.mu.Lock()
	discovery.calls++
	discovery.mu.Unlock()
	if discovery.block != nil && !discovery.block.after {
		discovery.block.wait(ctx)
	}
	result := discovery.delegate.Discover(ctx, agentID, roots, cutoff)
	if discovery.block != nil && discovery.block.after {
		discovery.block.wait(ctx)
	}
	return result
}

func (block *discoveryBlock) wait(ctx context.Context) {
	if block == nil {
		return
	}
	select {
	case block.entered <- struct{}{}:
	default:
	}
	select {
	case <-block.release:
	case <-ctx.Done():
	}
}

func (discovery *countingTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	return discovery.delegate.Classify(agentID, roots, path)
}

func (discovery *countingTranscriptDiscovery) callCount() int {
	discovery.mu.Lock()
	defer discovery.mu.Unlock()
	return discovery.calls
}

func countingCodexRegistry(cfg Config, block *discoveryBlock) (*codingAgentRegistry, *countingTranscriptDiscovery) {
	registry := defaultCodingAgentRegistry(cfg)
	registry.mu.Lock()
	index := registry.byID["codex"]
	discovery := &countingTranscriptDiscovery{
		delegate: registry.adapters[index].Capabilities.Discovery,
		block:    block,
	}
	registry.adapters[index].Capabilities.Discovery = discovery
	registry.mu.Unlock()
	return registry, discovery
}

func benchmarkEvidenceIndexFixture(b *testing.B) (time.Time, *codingAgentRegistry) {
	b.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(b.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	for index := 0; index < 256; index++ {
		path := filepath.Join(day, fileName(index))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	return now, defaultCodingAgentRegistry(Config{CodexRoots: []string{root}})
}

// panickingTranscriptDiscovery panics on its first Discover call and then
// delegates, so tests can prove a reconciliation fault releases its in-flight
// guard instead of stranding later callers.
type panickingTranscriptDiscovery struct {
	delegate transcriptDiscoveryCapability
	mu       sync.Mutex
	calls    int
}

func (discovery *panickingTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	discovery.mu.Lock()
	discovery.calls++
	call := discovery.calls
	discovery.mu.Unlock()
	if call == 1 {
		panic("discovery failed")
	}
	return discovery.delegate.Discover(ctx, agentID, roots, cutoff)
}

func (discovery *panickingTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	return discovery.delegate.Classify(agentID, roots, path)
}

func panickingCodexRegistry(cfg Config) *codingAgentRegistry {
	registry := defaultCodingAgentRegistry(cfg)
	registry.mu.Lock()
	adapterIndex := registry.byID["codex"]
	registry.adapters[adapterIndex].Capabilities.Discovery = &panickingTranscriptDiscovery{
		delegate: registry.adapters[adapterIndex].Capabilities.Discovery,
	}
	registry.mu.Unlock()
	return registry
}

func TestTranscriptEvidenceIndexRecoversFromPanickingReconciliation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	writeDiscoveryFixture(t, filepath.Join(day, "first.jsonl"), now)

	index := newTranscriptEvidenceIndex(panickingCodexRegistry(Config{CodexRoots: []string{root}}))
	cutoff := now.Add(-time.Hour)

	// The first reconcile panics inside discoverTranscripts. snapshot() must
	// absorb it and report incomplete coverage instead of propagating a panic
	// into the tray refresh loop or the token sampler that called it.
	aborted := make(chan transcriptEvidenceSnapshot, 1)
	go func() {
		aborted <- index.snapshot(context.Background(), cutoff, nil)
	}()
	var first transcriptEvidenceSnapshot
	select {
	case first = <-aborted:
	case <-time.After(10 * time.Second):
		t.Fatal("a panicking reconciliation never returned a snapshot")
	}
	if first.Complete {
		t.Fatal("expected an aborted reconciliation to report incomplete coverage")
	}
	if first.Stats.Reconciled {
		t.Fatal("expected an aborted reconciliation not to be reported as reconciled")
	}

	// The in-flight guard must be cleared and a gap recorded so the next call
	// retries rather than trusting the stale index.
	index.mu.Lock()
	inFlight := index.reconciling
	reconcileRequired := index.reconcileRequired
	index.mu.Unlock()
	if inFlight != nil {
		t.Fatal("expected the aborted reconcile to clear its in-flight guard")
	}
	if !reconcileRequired {
		t.Fatal("expected the aborted reconcile to mark a gap for retry")
	}

	// A caller with no deadline must not block on the dead flight, and the
	// index has to actually reconcile: service restored, not merely survived.
	done := make(chan transcriptEvidenceSnapshot, 1)
	go func() {
		done <- index.snapshot(context.Background(), cutoff, nil)
	}()
	select {
	case recovered := <-done:
		if !recovered.Stats.Reconciled {
			t.Fatalf("expected the index to reconcile after the aborted attempt, got %+v", recovered.Stats)
		}
		if len(recovered.Files) != 1 {
			t.Fatalf("expected the recovered snapshot to index 1 file, got %d", len(recovered.Files))
		}
		if !recovered.Complete {
			t.Fatalf("expected the recovered snapshot to be complete, got errors %v", recovered.Errors)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot with a context that never cancels blocked on the aborted reconcile flight")
	}
}

func TestTranscriptEvidenceIndexStartWithoutPlatformWatcherStaysRestartable(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{CodexRoots: []string{root}}))

	// startWithWatcher is start()'s lifecycle decision with the platform
	// constructor injected, so the no-watcher branch is testable on every
	// platform (newEvidenceWatcher returns nil only on non-darwin builds).
	index.startWithWatcher(func([]string) evidenceWatcher { return nil })

	// Without a watcher, start must claim no lifecycle state: running=true with
	// no goroutine behind it would make every later start a silent no-op and
	// hide that incremental coverage is unavailable.
	index.lifecycleMu.Lock()
	running := index.running
	stop := index.stop
	done := index.done
	watcher := index.watcher
	index.lifecycleMu.Unlock()
	if running || stop != nil || done != nil || watcher != nil {
		t.Fatalf("expected no lifecycle state without a platform watcher, got running=%v stop=%v done=%v watcher=%v",
			running, stop != nil, done != nil, watcher != nil)
	}
	index.mu.Lock()
	reconcileRequired := index.reconcileRequired
	index.mu.Unlock()
	if !reconcileRequired {
		t.Fatal("expected a missing platform watcher to mark a coverage gap")
	}

	// stopIndex must be safe when start installed nothing.
	index.stopIndex()

	// A later start that does get a watcher must still arm the index.
	fake := newFakeEvidenceWatcher()
	index.startWithWatcher(func([]string) evidenceWatcher { return fake })
	waitForIndexRunning(t, index, true)
	index.stopIndex()
	waitForIndexRunning(t, index, false)
	if !fake.wasStopped() {
		t.Fatal("expected stopIndex to stop the installed watcher")
	}
}

// fakeEvidenceWatcher is a controllable evidenceWatcher so watcher lifecycle
// tests never depend on real filesystem events.
type fakeEvidenceWatcher struct {
	events  chan evidenceWatchBatch
	mu      sync.Mutex
	stopped bool
}

func newFakeEvidenceWatcher() *fakeEvidenceWatcher {
	return &fakeEvidenceWatcher{events: make(chan evidenceWatchBatch, 8)}
}

func (watcher *fakeEvidenceWatcher) Events() <-chan evidenceWatchBatch { return watcher.events }

func (watcher *fakeEvidenceWatcher) Update([]string) bool { return true }

func (watcher *fakeEvidenceWatcher) Stop() {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	watcher.stopped = true
}

func (watcher *fakeEvidenceWatcher) wasStopped() bool {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return watcher.stopped
}

// startIndexWithFakeWatcher installs a fake watcher and runs the index watcher
// loop exactly as start() does, without needing a platform watcher.
// startIndexWithFakeWatcher starts the index through the production
// startWithWatcher seam so tests exercise the real watcher loop rather than a
// copy of it.
func startIndexWithFakeWatcher(index *transcriptEvidenceIndex, watcher evidenceWatcher) {
	index.startWithWatcher(func([]string) evidenceWatcher { return watcher })
}

func waitForIndexRunning(t *testing.T, index *transcriptEvidenceIndex, want bool) {
	t.Helper()
	for i := 0; i < 500; i++ {
		index.lifecycleMu.Lock()
		got := index.running
		index.lifecycleMu.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for evidence index running=%v", want)
}

func TestTranscriptEvidenceIndexWatcherReleasesRunningFlagWhenWatcherCloses(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{CodexRoots: []string{root}}))
	watcher := newFakeEvidenceWatcher()
	startIndexWithFakeWatcher(index, watcher)
	waitForIndexRunning(t, index, true)

	// A closed event channel ends the watcher loop. The running flag must be
	// released or start() would no-op forever and incremental indexing would
	// never come back.
	close(watcher.events)
	waitForIndexRunning(t, index, false)
	if !watcher.wasStopped() {
		t.Fatal("expected the released watcher lifecycle to stop the watcher")
	}
	index.mu.Lock()
	reconcileRequired := index.reconcileRequired
	index.mu.Unlock()
	if !reconcileRequired {
		t.Fatal("expected the watcher close to mark a coverage gap for reconciliation")
	}

	// stopIndex must be safe after the loop already exited on its own.
	index.stopIndex()

	// The index must be restartable with a fresh watcher.
	next := newFakeEvidenceWatcher()
	startIndexWithFakeWatcher(index, next)
	waitForIndexRunning(t, index, true)
	t.Cleanup(index.stopIndex)
}

func TestTranscriptEvidenceIndexRestartsWatcherAfterUnexpectedExit(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{CodexRoots: []string{root}}))
	first := newFakeEvidenceWatcher()
	second := newFakeEvidenceWatcher()
	var factoryMu sync.Mutex
	calls := 0
	factory := func([]string) evidenceWatcher {
		factoryMu.Lock()
		defer factoryMu.Unlock()
		calls++
		if calls == 1 {
			return first
		}
		return second
	}
	index.startWithWatcher(factory)
	waitForIndexRunning(t, index, true)

	// A watcher can terminate without an explicit stop (for example, a closed
	// FSEvents stream). The next consumer read must re-arm it before trusting
	// incremental coverage.
	close(first.events)
	waitForIndexRunning(t, index, false)
	recovered := index.snapshot(context.Background(), time.Time{}, nil)
	if !recovered.Complete {
		t.Fatalf("watcher restart left coverage incomplete: %+v", recovered)
	}
	waitForIndexRunning(t, index, true)
	factoryMu.Lock()
	gotCalls := calls
	factoryMu.Unlock()
	if gotCalls != 2 {
		t.Fatalf("watcher factory calls = %d, want initial plus one restart", gotCalls)
	}
	t.Cleanup(index.stopIndex)
}

func TestTranscriptEvidenceIndexWatcherKeepsRunningAfterBatchPanics(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	path := filepath.Join(day, "first.jsonl")
	writeDiscoveryFixture(t, path, now)

	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{CodexRoots: []string{root}}))
	if cold := index.snapshot(context.Background(), now.Add(-time.Hour), nil); !cold.Stats.Reconciled {
		t.Fatalf("expected a cold reconcile, got %+v", cold.Stats)
	}
	watcher := newFakeEvidenceWatcher()
	startIndexWithFakeWatcher(index, watcher)
	t.Cleanup(index.stopIndex)
	waitForIndexRunning(t, index, true)

	// Corrupt the pending-mutation state only for the first real watcher batch.
	// applyMutation then panics at the production assignment boundary, so this
	// test proves recordWatchBatchContained catches an actual batch panic and the
	// same watcher loop still handles the following good batch.
	index.mu.Lock()
	initialGapGeneration := index.gapGeneration
	savedMutations := index.mutations
	index.mutations = nil
	index.reconciling = make(chan struct{})
	index.mu.Unlock()
	watcher.events <- evidenceWatchBatch{Paths: []string{path}, Complete: true}
	for i := 0; i < 500; i++ {
		index.mu.Lock()
		gapAdvanced := index.gapGeneration > initialGapGeneration
		index.mu.Unlock()
		if gapAdvanced {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	index.mu.Lock()
	index.mutations = savedMutations
	index.reconciling = nil
	index.mu.Unlock()
	watcher.events <- evidenceWatchBatch{Paths: []string{path}, Complete: true}

	indexed := false
	for i := 0; i < 500 && !indexed; i++ {
		index.mu.Lock()
		_, indexed = index.files[canonicalEvidencePath(path)]
		index.mu.Unlock()
		if !indexed {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !indexed {
		t.Fatal("expected the watcher loop to keep processing a batch after a panic")
	}
	waitForIndexRunning(t, index, true)
}

func TestTranscriptEvidenceIndexSurvivesEntryMissingFileInfo(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	writeDiscoveryFixture(t, filepath.Join(day, "first.jsonl"), now)

	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{CodexRoots: []string{root}}))
	cutoff := now.Add(-time.Hour)
	if cold := index.snapshot(context.Background(), cutoff, nil); !cold.Stats.Reconciled {
		t.Fatalf("expected a cold reconcile, got %+v", cold.Stats)
	}

	// An indexed entry with no stat info cannot be ordered or filtered. Reading
	// it must not panic under index.mu: a stranded index.mu would deadlock every
	// later snapshot, the watcher mutation path and the reconcile release.
	badPath := canonicalEvidencePath(filepath.Join(day, "missing-info.jsonl"))
	index.mu.Lock()
	index.files[badPath] = discoveredTranscriptFile{File: TranscriptFile{Tool: "codex", Path: badPath}}
	index.mu.Unlock()

	got := index.currentSnapshot(cutoff, nil, false)
	for _, file := range got.Files {
		if file.Info == nil {
			t.Fatal("expected entries without file info to be dropped from the snapshot")
		}
	}

	acquired := make(chan struct{})
	go func() {
		index.mu.Lock()
		index.mu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("index.mu was left locked while reading an entry without file info")
	}

	// The bad entry must be dropped and a gap marked so the next snapshot
	// reconciles rather than serving a knowingly incomplete index.
	index.mu.Lock()
	_, stillIndexed := index.files[badPath]
	reconcileRequired := index.reconcileRequired
	index.mu.Unlock()
	if stillIndexed {
		t.Fatal("expected the entry without file info to be dropped from the index")
	}
	if !reconcileRequired {
		t.Fatal("expected the dropped entry to mark a coverage gap")
	}
}

func TestTranscriptEvidenceIndexMarksGapWhenBatchRecordingPanics(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	writeDiscoveryFixture(t, filepath.Join(day, "first.jsonl"), now)

	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{CodexRoots: []string{root}}))
	if cold := index.snapshot(context.Background(), now.Add(-time.Hour), nil); !cold.Stats.Reconciled {
		t.Fatalf("expected a cold reconcile, got %+v", cold.Stats)
	}
	// Clear the gap state the cold reconcile may have left so the assertion
	// below can only be satisfied by the aborted batch.
	index.mu.Lock()
	index.reconcileRequired = false
	index.complete = true
	generation := index.gapGeneration
	index.mu.Unlock()

	// A nil mutations map makes applyMutation panic on assignment while
	// recording the batch. Losing a batch is lost coverage, so the abort must
	// mark an evidence gap rather than silently dropping the events and then
	// serving an incomplete index as complete.
	index.mu.Lock()
	saved := index.mutations
	index.mutations = nil
	index.reconciling = make(chan struct{})
	index.mu.Unlock()

	index.recordWatchBatchContained(evidenceWatchBatch{
		Paths:    []string{filepath.Join(day, "first.jsonl")},
		Complete: true,
	})

	index.mu.Lock()
	index.mutations = saved
	index.reconciling = nil
	reconcileRequired := index.reconcileRequired
	complete := index.complete
	gapAdvanced := index.gapGeneration != generation
	index.mu.Unlock()
	if !reconcileRequired || complete || !gapAdvanced {
		t.Fatalf("expected an aborted batch to mark a gap, got reconcileRequired=%v complete=%v gapAdvanced=%v",
			reconcileRequired, complete, gapAdvanced)
	}
}

// panickingStopEvidenceWatcher panics from Stop(), the watcher-owned call the
// release path makes while shutting the boundary down.
type panickingStopEvidenceWatcher struct {
	events chan evidenceWatchBatch
}

func (watcher *panickingStopEvidenceWatcher) Events() <-chan evidenceWatchBatch {
	return watcher.events
}

func (watcher *panickingStopEvidenceWatcher) Update([]string) bool { return true }

func (watcher *panickingStopEvidenceWatcher) Stop() { panic("watcher stop failed") }

// slowExitEvidenceWatcher panics from Stop while holding the index's own
// lifecycleMu, which is exactly the lock the watcher goroutine needs to finish
// releasing. That keeps `done` open across the panic, so "did the caller await
// the goroutine?" is decided by the code under test rather than by a race the
// goroutine almost always wins.
type slowExitEvidenceWatcher struct {
	index  *transcriptEvidenceIndex
	events chan evidenceWatchBatch
}

func (watcher *slowExitEvidenceWatcher) Events() <-chan evidenceWatchBatch {
	return watcher.events
}

func (watcher *slowExitEvidenceWatcher) Update([]string) bool { return true }

func (watcher *slowExitEvidenceWatcher) Stop() {
	watcher.index.lifecycleMu.Lock()
	go func() {
		time.Sleep(200 * time.Millisecond)
		watcher.index.lifecycleMu.Unlock()
	}()
	panic("watcher stop failed")
}

func TestTranscriptEvidenceIndexReleasesLifecycleWhenWatcherStopPanics(t *testing.T) {
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{}))
	watcher := &panickingStopEvidenceWatcher{events: make(chan evidenceWatchBatch, 1)}
	startIndexWithFakeWatcher(index, watcher)
	waitForIndexRunning(t, index, true)

	// Closing Events() ends the loop, which releases the lifecycle and calls the
	// panicking Stop. A panic there must not strand running=true or skip
	// close(done): stopIndex waits on done, so shutdown would hang forever.
	close(watcher.events)
	waitForIndexRunning(t, index, false)

	// start() must be able to re-arm the boundary afterwards.
	replacement := newFakeEvidenceWatcher()
	startIndexWithFakeWatcher(index, replacement)
	waitForIndexRunning(t, index, true)
	index.stopIndex()
	waitForIndexRunning(t, index, false)
}

func TestTranscriptEvidenceIndexStopIndexAfterWatcherSelfExitDoesNotDoubleStop(t *testing.T) {
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{}))
	watcher := newFakeEvidenceWatcher()
	startIndexWithFakeWatcher(index, watcher)
	waitForIndexRunning(t, index, true)

	// The watcher exits on its own first, then stopIndex runs. Exactly one of the
	// two may close done and stop the watcher; a double close would panic here.
	close(watcher.events)
	waitForIndexRunning(t, index, false)
	index.stopIndex()
	index.stopIndex()
	if !watcher.wasStopped() {
		t.Fatal("expected the watcher to be stopped exactly once by the release path")
	}
}

func TestReleaseReconcileFlightLeavesAForeignFlightAlone(t *testing.T) {
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{}))
	owned := make(chan struct{})
	foreign := make(chan struct{})
	index.mu.Lock()
	index.reconciling = owned
	index.mu.Unlock()

	// A release for a flight this index no longer owns must not close it: the
	// owner still holds it, and closing twice would panic.
	index.releaseReconcileFlight(foreign)
	select {
	case <-foreign:
		t.Fatal("releaseReconcileFlight closed a flight it does not own")
	default:
	}
	index.mu.Lock()
	stillOwned := index.reconciling == owned
	index.mu.Unlock()
	if !stillOwned {
		t.Fatal("expected the owning flight to remain published")
	}

	// The owner's own release closes exactly once and clears the guard.
	index.releaseReconcileFlight(owned)
	select {
	case <-owned:
	case <-time.After(5 * time.Second):
		t.Fatal("expected the owned flight to be closed")
	}
	index.mu.Lock()
	cleared := index.reconciling == nil
	index.mu.Unlock()
	if !cleared {
		t.Fatal("expected the in-flight guard to be cleared")
	}
}

func TestReleaseWatcherLifecycleClosesDoneEvenWhenStopPanics(t *testing.T) {
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{}))
	done := make(chan struct{})
	index.lifecycleMu.Lock()
	index.running = true
	index.stop = make(chan struct{})
	index.done = done
	index.watcher = &panickingStopEvidenceWatcher{events: make(chan evidenceWatchBatch)}
	index.lifecycleMu.Unlock()

	// stopIndex blocks on <-done, so the close must survive a panicking Stop or
	// shutdown hangs forever waiting on a watcher that already exited.
	func() {
		defer func() { _ = recover() }()
		index.releaseWatcherLifecycle(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("releaseWatcherLifecycle skipped close(done) when the watcher Stop panicked")
	}
	index.lifecycleMu.Lock()
	running := index.running
	index.lifecycleMu.Unlock()
	if running {
		t.Fatal("expected the running flag to be cleared")
	}
}

func TestStopIndexStillAwaitsWatcherExitWhenStopPanics(t *testing.T) {
	index := newTranscriptEvidenceIndex(defaultCodingAgentRegistry(Config{}))
	watcher := &slowExitEvidenceWatcher{index: index, events: make(chan evidenceWatchBatch)}
	startIndexWithFakeWatcher(index, watcher)
	waitForIndexRunning(t, index, true)

	index.lifecycleMu.Lock()
	done := index.done
	index.lifecycleMu.Unlock()

	// Called exactly as onExit calls it: contained, so the panic is recovered by
	// the step rather than by the caller. stopIndex must absorb the panicking
	// Stop itself and still await the goroutine before returning.
	returned := make(chan struct{})
	awaited := false
	go func() {
		defer close(returned)
		runBackgroundStep("test shutdown evidence index", index.stopIndex)
		// Sampled the instant stopIndex returns: if it skipped <-done, the
		// watcher goroutine may still be running here.
		select {
		case <-done:
			awaited = true
		default:
		}
	}()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("stopIndex hung when the watcher Stop panicked")
	}

	// Skipping the wait lets onExit record shutdown_complete over a live goroutine.
	if !awaited {
		t.Fatal("stopIndex returned without awaiting the watcher goroutine's exit")
	}
	waitForIndexRunning(t, index, false)
}
