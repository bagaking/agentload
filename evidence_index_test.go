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
		path := fmt.Sprintf("/tmp/evidence-%05d.jsonl", mutationIndex)
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
