package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// resilienceTranscriptParser is deliberately small and injectable so the
// worker-pool tests can exercise parser failures without touching filesystem
// parsing code.
type resilienceTranscriptParser struct {
	parse       func(TranscriptFile) (*SessionTrace, error)
	parseAppend func(TranscriptFile, *SessionTrace, int64) (*SessionTrace, error)
	canAppend   func(TranscriptFile) bool
}

func (p resilienceTranscriptParser) Parse(file TranscriptFile) (*SessionTrace, error) {
	if p.parse == nil {
		return nil, errors.New("full parser not configured")
	}
	return p.parse(file)
}

func (p resilienceTranscriptParser) ParseTail(file TranscriptFile) (*SessionTrace, error) {
	return p.Parse(file)
}

func (p resilienceTranscriptParser) ParseAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if p.parseAppend == nil {
		return nil, errors.New("append parser not configured")
	}
	return p.parseAppend(file, base, offset)
}

func (p resilienceTranscriptParser) CanAppend(file TranscriptFile) bool {
	if p.canAppend != nil {
		return p.canAppend(file)
	}
	return true
}

func resilienceRegistry(parser agentTranscriptParser) *codingAgentRegistry {
	return newCodingAgentRegistry(codingAgentAdapter{
		ID: "fault",
		Capabilities: agentCapabilities{
			Transcript: parser,
		},
	})
}

func resilienceCandidate(path string) transcriptCandidate {
	return transcriptCandidate{File: TranscriptFile{Tool: "fault", Path: path}}
}

func resilienceTrace(file TranscriptFile) *SessionTrace {
	return &SessionTrace{
		Tool:       file.Tool,
		Path:       file.Path,
		SessionID:  filepath.Base(file.Path),
		EventTimes: []time.Time{time.Unix(1, 0)},
	}
}

func resilienceWorkerCount(candidateCount int) int {
	workerCount := runtime.NumCPU()
	if workerCount > 4 {
		workerCount = 4
	}
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > candidateCount {
		workerCount = candidateCount
	}
	return workerCount
}

func updateResilienceMax(max *atomic.Int32, value int32) {
	for {
		current := max.Load()
		if value <= current || max.CompareAndSwap(current, value) {
			return
		}
	}
}

// cancellationHandshakeContext exposes an unbuffered cancellation handoff so
// cancel() returns only after the producer has selected ctx.Done. This lets
// the test release parser gates after dispatch has definitely stopped, without
// relying on scheduler sleeps or an arbitrary "producer is probably blocked"
// delay.
type cancellationHandshakeContext struct {
	done     chan struct{}
	canceled atomic.Bool
}

func newCancellationHandshakeContext() *cancellationHandshakeContext {
	return &cancellationHandshakeContext{done: make(chan struct{})}
}

func (c *cancellationHandshakeContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (c *cancellationHandshakeContext) Done() <-chan struct{} { return c.done }

func (c *cancellationHandshakeContext) Err() error {
	if c.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func (c *cancellationHandshakeContext) Value(any) any { return nil }

func (c *cancellationHandshakeContext) cancel() {
	// The send completes only when the producer's cancellation-select receives
	// it. Set Err after that handoff so the producer cannot exit its pre-select
	// check without consuming the cancellation signal.
	c.done <- struct{}{}
	c.canceled.Store(true)
}

// TestParseTranscriptCandidatesRecoversEachPanic keeps the producer blocked
// behind a deterministic barrier until every worker has a panic-inducing job.
// On the baseline implementation all workers then exit and the producer
// deadlocks trying to dispatch the final healthy candidate.
func TestParseTranscriptCandidatesRecoversEachPanic(t *testing.T) {
	workerCount := resilienceWorkerCount(5)
	candidates := make([]transcriptCandidate, workerCount+1)
	for index := range candidates {
		candidates[index] = resilienceCandidate(fmt.Sprintf("panic-%d.jsonl", index))
	}

	var calls atomic.Int32
	var active atomic.Int32
	var maxActive atomic.Int32
	entered := make(chan struct{}, workerCount)
	release := make(chan struct{})
	parser := resilienceTranscriptParser{parse: func(file TranscriptFile) (*SessionTrace, error) {
		current := active.Add(1)
		updateResilienceMax(&maxActive, current)
		defer active.Add(-1)
		call := int(calls.Add(1))
		if call <= workerCount {
			entered <- struct{}{}
			<-release
			panic("synthetic transcript parser panic")
		}
		return resilienceTrace(file), nil
	}}

	resultCh := make(chan []transcriptParseResult, 1)
	go func() {
		resultCh <- parseTranscriptCandidates(context.Background(), resilienceRegistry(parser), candidates)
	}()

	for index := 0; index < workerCount; index++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("worker %d did not enter its gated parser call", index)
		}
	}
	close(release)

	var results []transcriptParseResult
	select {
	case results = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("transcript parser producer/worker pool did not complete after worker panics")
	}

	if max := maxActive.Load(); max > int32(workerCount) {
		t.Fatalf("parser concurrency exceeded worker bound: max=%d workers=%d", max, workerCount)
	}
	if len(results) != len(candidates) {
		t.Fatalf("result length=%d, want %d", len(results), len(candidates))
	}
	for index, result := range results {
		if got, want := result.Candidate.File.Path, candidates[index].File.Path; got != want {
			t.Fatalf("result[%d] candidate=%q, want %q", index, got, want)
		}
		if index < workerCount {
			if result.Err == nil || isContextError(result.Err) || !strings.Contains(strings.ToLower(result.Err.Error()), "panic") {
				t.Fatalf("result[%d] panic error=%v", index, result.Err)
			}
			if !strings.Contains(result.Err.Error(), candidates[index].File.Path) {
				t.Fatalf("result[%d] panic error=%v does not identify candidate", index, result.Err)
			}
			continue
		}
		if result.Err != nil || result.Trace == nil {
			t.Fatalf("healthy result[%d]=%+v", index, result)
		}
	}
}

func TestParseAppendTranscriptCandidatesRecoversPanicAndPreservesOrder(t *testing.T) {
	workerCount := resilienceWorkerCount(5)
	candidates := make([]transcriptAppendCandidate, workerCount+1)
	for index := range candidates {
		candidates[index] = transcriptAppendCandidate{
			Candidate: resilienceCandidate(fmt.Sprintf("append-%d.jsonl", index)),
			Base:      &SessionTrace{SessionID: fmt.Sprintf("base-%d", index), EventTimes: []time.Time{time.Unix(1, 0)}},
			Offset:    int64(index + 7),
		}
	}
	var calls atomic.Int32
	entered := make(chan struct{}, workerCount)
	release := make(chan struct{})
	parser := resilienceTranscriptParser{parseAppend: func(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
		call := int(calls.Add(1))
		if call <= workerCount {
			entered <- struct{}{}
			<-release
			panic("synthetic append parser panic")
		}
		if base == nil || offset != int64(workerCount+7) {
			return nil, fmt.Errorf("unexpected append inputs: base=%v offset=%d", base, offset)
		}
		return resilienceTrace(file), nil
	}}
	resultCh := make(chan []transcriptParseResult, 1)
	go func() {
		resultCh <- parseAppendTranscriptCandidates(context.Background(), resilienceRegistry(parser), candidates)
	}()
	for index := 0; index < workerCount; index++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("append worker %d did not enter its gated parser call", index)
		}
	}
	close(release)
	var results []transcriptParseResult
	select {
	case results = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("transcript append producer/worker pool did not complete after worker panics")
	}
	if len(results) != len(candidates) {
		t.Fatalf("result length=%d, want %d", len(results), len(candidates))
	}
	for index, result := range results {
		if got, want := result.Candidate.File.Path, candidates[index].Candidate.File.Path; got != want {
			t.Fatalf("append result[%d] candidate=%q, want %q", index, got, want)
		}
		if index < workerCount {
			if result.Err == nil || isContextError(result.Err) || !strings.Contains(strings.ToLower(result.Err.Error()), "panic") {
				t.Fatalf("append result[%d] panic error=%v", index, result.Err)
			}
			if result.Trace == nil || result.Trace.SessionID != candidates[index].Base.SessionID {
				t.Fatalf("append result[%d] lost cached prefix evidence: %+v", index, result.Trace)
			}
			if result.Trace == candidates[index].Base {
				t.Fatalf("append result[%d] exposed mutable cached prefix", index)
			}
			continue
		}
		if result.Err != nil || result.Trace == nil {
			t.Fatalf("healthy append result[%d]=%+v", index, result)
		}
	}
}

func TestParseTranscriptCandidatesDoesNotDispatchAfterCancellation(t *testing.T) {
	var calls atomic.Int32
	parser := resilienceTranscriptParser{parse: func(file TranscriptFile) (*SessionTrace, error) {
		calls.Add(1)
		return resilienceTrace(file), nil
	}}
	candidates := []transcriptCandidate{
		resilienceCandidate("cancel-0.jsonl"),
		resilienceCandidate("cancel-1.jsonl"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	results := parseTranscriptCandidates(ctx, resilienceRegistry(parser), candidates)
	if calls.Load() != 0 {
		t.Fatalf("cancelled producer dispatched %d parser calls", calls.Load())
	}
	if len(results) != len(candidates) {
		t.Fatalf("result length=%d, want %d", len(results), len(candidates))
	}
	for index, result := range results {
		if result.Candidate.File.Path != "" || result.Trace != nil || result.Err != nil {
			t.Fatalf("cancelled result[%d] should retain undispatched zero marker, got %+v", index, result)
		}
	}
}

func TestParseTranscriptCandidatesCancellationUnblocksProducer(t *testing.T) {
	workerCount := resilienceWorkerCount(5)
	candidates := make([]transcriptCandidate, workerCount+1)
	for index := range candidates {
		candidates[index] = resilienceCandidate(fmt.Sprintf("midflight-cancel-%d.jsonl", index))
	}

	var calls atomic.Int32
	entered := make(chan struct{}, workerCount)
	release := make(chan struct{})
	var released atomic.Bool
	releaseWorkers := func() {
		if released.CompareAndSwap(false, true) {
			close(release)
		}
	}
	t.Cleanup(releaseWorkers)
	parser := resilienceTranscriptParser{parse: func(file TranscriptFile) (*SessionTrace, error) {
		call := int(calls.Add(1))
		if call <= workerCount {
			entered <- struct{}{}
			<-release
			panic("synthetic cancellation parser panic")
		}
		return resilienceTrace(file), nil
	}}
	ctx := newCancellationHandshakeContext()
	resultCh := make(chan []transcriptParseResult, 1)
	go func() {
		resultCh <- parseTranscriptCandidates(ctx, resilienceRegistry(parser), candidates)
	}()

	for index := 0; index < workerCount; index++ {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("worker %d did not enter its gated parser call", index)
		}
	}

	// All workers are blocked in their first job, so the producer is blocked on
	// the extra unbuffered send. The handshake proves its cancellation case won
	// before any worker gate is released.
	cancelDone := make(chan struct{})
	go func() {
		ctx.cancel()
		close(cancelDone)
	}()
	select {
	case <-cancelDone:
	case <-time.After(5 * time.Second):
		t.Fatal("producer did not select cancellation while blocked on jobs send")
	}
	releaseWorkers()

	select {
	case results := <-resultCh:
		if len(results) != len(candidates) {
			t.Fatalf("result length=%d, want %d", len(results), len(candidates))
		}
		for index := 0; index < workerCount; index++ {
			if results[index].Err == nil || isContextError(results[index].Err) {
				t.Fatalf("released worker result[%d]=%+v, want non-context panic error", index, results[index])
			}
		}
		if results[workerCount].Candidate.File.Path != "" || results[workerCount].Err != nil || results[workerCount].Trace != nil {
			t.Fatalf("undispatched candidate should retain zero result marker, got %+v", results[workerCount])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("transcript parser did not finish after cancellation and gate release")
	}
}

func TestScanTranscriptsPersistsParserPanicAsFileError(t *testing.T) {
	tmp := t.TempDir()
	panicPath := filepath.Join(tmp, "panic.jsonl")
	healthyPath := filepath.Join(tmp, "healthy.jsonl")
	for _, path := range []string{panicPath, healthyPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write transcript %s: %v", path, err)
		}
	}
	var calls atomic.Int32
	parser := resilienceTranscriptParser{parse: func(file TranscriptFile) (*SessionTrace, error) {
		calls.Add(1)
		if file.Path == panicPath {
			panic("synthetic durable parser panic")
		}
		return resilienceTrace(file), nil
	}}
	observer := newObserverWithRegistry(Config{}, resilienceRegistry(parser))
	priority := []TranscriptFile{{Tool: "fault", Path: panicPath}, {Tool: "fault", Path: healthyPath}}
	opts := transcriptScanOptions{}

	data := observer.scanTranscriptsWithOptions(context.Background(), priority, opts)
	if data.ParsedFiles != 1 || data.Traces[healthyPath] == nil {
		t.Fatalf("healthy evidence was lost: %+v", data)
	}
	if data.Traces[panicPath] != nil {
		t.Fatalf("panic candidate should not fabricate a trace: %+v", data.Traces[panicPath])
	}
	found := false
	for _, message := range data.Errors {
		if strings.Contains(message, panicPath) && strings.Contains(strings.ToLower(message), "panic") {
			found = true
		}
	}
	if !found {
		t.Fatalf("parser panic was not disclosed in scan errors: %#v", data.Errors)
	}
	firstCalls := calls.Load()

	data = observer.scanTranscriptsWithOptions(context.Background(), priority, opts)
	if calls.Load() != firstCalls {
		t.Fatalf("unchanged panic candidate was reparsed: calls=%d first=%d", calls.Load(), firstCalls)
	}
	found = false
	for _, message := range data.Errors {
		if strings.Contains(message, panicPath) && strings.Contains(strings.ToLower(message), "panic") {
			found = true
		}
	}
	if !found || data.Traces[healthyPath] == nil {
		t.Fatalf("cached panic error or healthy evidence was lost: %+v", data)
	}
}

func TestTranscriptDataDoesNotReturnStaleCompletedFlight(t *testing.T) {
	path := filepath.Join(t.TempDir(), "waiter.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	parser := resilienceTranscriptParser{parse: func(file TranscriptFile) (*SessionTrace, error) {
		return resilienceTrace(file), nil
	}}
	observer := newObserverWithRegistry(Config{}, resilienceRegistry(parser))
	key := transcriptCacheKey(observer.adapters.roots(), []TranscriptFile{{Tool: "fault", Path: path}}, observer.cfg.IdleGap, observer.cfg.MinInterval, observer.cfg.Lookback)
	flight := &transcriptScanFlight{
		done:     make(chan struct{}),
		complete: true,
		data: &TranscriptData{
			Traces:           map[string]*SessionTrace{path: resilienceTrace(TranscriptFile{Tool: "fault", Path: path})},
			evidenceRevision: 1,
		},
	}
	close(flight.done)
	observer.mu.Lock()
	observer.inflight[key] = flight
	observer.mu.Unlock()
	observer.evidenceIndex.mu.Lock()
	observer.evidenceIndex.revision = 2
	observer.evidenceIndex.mu.Unlock()

	data, cached := observer.transcriptData(context.Background(), []TranscriptFile{{Tool: "fault", Path: path}}, time.Now())
	if cached || data == nil || data.CoverageIncomplete {
		t.Fatalf("expected waiter to rescan after stale flight, cached=%v data=%+v", cached, data)
	}
	if data.evidenceRevision <= 1 {
		t.Fatalf("expected replacement scan at a current revision, got %d", data.evidenceRevision)
	}
}

func TestTranscriptDataContainsPanickingCanAppendAndReleasesCacheLock(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}
	parser := resilienceTranscriptParser{
		parse: func(file TranscriptFile) (*SessionTrace, error) { return resilienceTrace(file), nil },
		canAppend: func(TranscriptFile) bool {
			panic("synthetic append capability panic")
		},
	}
	observer := newObserverWithRegistry(Config{}, resilienceRegistry(parser))
	observer.mu.Lock()
	observer.fileCache[path] = fileTraceCache{
		ModTime:         info.ModTime().Add(-time.Second),
		Size:            1,
		EndsWithNewline: true,
		Trace:           resilienceTrace(TranscriptFile{Tool: "fault", Path: path}),
	}
	observer.mu.Unlock()

	data, cached := observer.transcriptData(context.Background(), []TranscriptFile{{Tool: "fault", Path: path}}, time.Now())
	if cached || data == nil || !data.CoverageIncomplete {
		t.Fatalf("CanAppend panic was not converted to incomplete evidence: cached=%t data=%+v", cached, data)
	}

	// The adapter boundary ran while the cache lock was held. A contained panic
	// must release that lock so later refreshes do not deadlock on the poisoned
	// Observer instance.
	acquired := make(chan struct{})
	go func() {
		observer.mu.Lock()
		observer.mu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("Observer.mu was left locked after a panicking CanAppend capability")
	}
}

type incompleteOnceTranscriptDiscovery struct {
	delegate transcriptDiscoveryCapability
	failed   atomic.Bool
}

func (discovery *incompleteOnceTranscriptDiscovery) Discover(ctx context.Context, agentID string, roots []string, cutoff time.Time) transcriptDiscoveryResult {
	if discovery.failed.CompareAndSwap(false, true) {
		return transcriptDiscoveryResult{Errors: []string{"synthetic incomplete evidence"}}
	}
	return discovery.delegate.Discover(ctx, agentID, roots, cutoff)
}

func (discovery *incompleteOnceTranscriptDiscovery) Classify(agentID string, roots []string, path string) (TranscriptFile, bool) {
	return discovery.delegate.Classify(agentID, roots, path)
}

func TestTranscriptDataDoesNotCacheIncompleteEvidence(t *testing.T) {
	root := filepath.Join(t.TempDir(), ".codex")
	registry := defaultCodingAgentRegistry(Config{CodexRoots: []string{root}})
	registry.mu.Lock()
	adapterIndex := registry.byID["codex"]
	delegate := registry.adapters[adapterIndex].Capabilities.Discovery
	registry.adapters[adapterIndex].Capabilities.Discovery = &incompleteOnceTranscriptDiscovery{delegate: delegate}
	registry.mu.Unlock()
	observer := newObserverWithRegistry(Config{
		CodexRoots:         []string{root},
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           time.Hour,
		TranscriptCacheTTL: time.Minute,
	}, registry)

	data, cached := observer.transcriptData(context.Background(), nil, time.Now())
	if cached || data == nil || !data.CoverageIncomplete {
		t.Fatalf("incomplete evidence result = cached=%v data=%+v", cached, data)
	}
	observer.mu.Lock()
	cacheData := observer.cache.Data
	observer.mu.Unlock()
	if cacheData != nil {
		t.Fatalf("incomplete evidence populated the transcript cache: %+v", cacheData)
	}

	// The first successful reconciliation after a gap remains explicitly marked
	// as a recovery sample; only the following stable read is cacheable.
	recovered, recoveredCached := observer.transcriptData(context.Background(), nil, time.Now())
	if recoveredCached || !recovered.CoverageIncomplete {
		t.Fatalf("recovery transition result = cached=%v data=%+v", recoveredCached, recovered)
	}
	recovered, recoveredCached = observer.transcriptData(context.Background(), nil, time.Now())
	if recoveredCached || recovered.CoverageIncomplete {
		t.Fatalf("stable recovery result = cached=%v data=%+v", recoveredCached, recovered)
	}
	observer.mu.Lock()
	cacheData = observer.cache.Data
	observer.mu.Unlock()
	if cacheData == nil {
		t.Fatal("complete recovery did not populate the transcript cache")
	}
}

func TestTranscriptDataRejectsEvidenceMutationDuringParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	parser := resilienceTranscriptParser{parse: func(file TranscriptFile) (*SessionTrace, error) {
		once.Do(func() {
			close(entered)
			<-release
		})
		return resilienceTrace(file), nil
	}}
	observer := newObserverWithRegistry(Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           time.Hour,
		TranscriptCacheTTL: time.Minute,
	}, resilienceRegistry(parser))
	priority := []TranscriptFile{{Tool: "fault", Path: path}}
	type result struct {
		data   *TranscriptData
		cached bool
	}
	resultCh := make(chan result, 1)
	go func() {
		data, cached := observer.transcriptData(context.Background(), priority, time.Now())
		resultCh <- result{data: data, cached: cached}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("parser did not enter its deterministic gate")
	}
	// A watcher gap advances the evidence revision while the parser still owns
	// the candidate. The scan must be useful to the caller but cannot be cached
	// or promoted to durable history as a complete sample.
	observer.evidenceIndex.markGap("synthetic mutation during parse")
	close(release)
	var got result
	select {
	case got = <-resultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("transcript scan did not finish after the parser gate was released")
	}
	if got.cached || got.data == nil || !got.data.CoverageIncomplete {
		t.Fatalf("evidence mutation was published as complete: cached=%v data=%+v", got.cached, got.data)
	}
	if got.data.Traces[path] == nil {
		t.Fatalf("expected the useful parsed trace to remain visible: %+v", got.data)
	}
	observer.mu.Lock()
	cacheData := observer.cache.Data
	observer.mu.Unlock()
	if cacheData != nil {
		t.Fatalf("scan with an in-flight evidence mutation populated cache: %+v", cacheData)
	}
}

func TestRememberSnapshotRejectsIncompleteCoverage(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "history.jsonl")
	app := &trayApp{history: localHistoryState{path: historyPath}}
	got := app.rememberSnapshot(Snapshot{
		GeneratedAt:     time.Now().Format(time.RFC3339),
		TranscriptStats: TranscriptStats{CoverageIncomplete: true},
	})
	if !got.TranscriptStats.CoverageIncomplete {
		t.Fatalf("incomplete snapshot was changed: %+v", got.TranscriptStats)
	}
	if app.haveSnapshot {
		t.Fatal("incomplete snapshot became the cached snapshot")
	}
	if _, err := os.Stat(historyPath); !os.IsNotExist(err) {
		t.Fatalf("incomplete snapshot wrote history, stat error=%v", err)
	}
}
