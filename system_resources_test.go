package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCounterDelta(t *testing.T) {
	tests := []struct {
		name     string
		previous uint64
		current  uint64
		want     uint64
	}{
		{name: "increase", previous: 10, current: 16, want: 6},
		{name: "same", previous: 10, current: 10, want: 0},
		{name: "reset", previous: 16, current: 10, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := counterDelta(tt.previous, tt.current); got != tt.want {
				t.Fatalf("counterDelta(%d, %d) = %d, want %d", tt.previous, tt.current, got, tt.want)
			}
		})
	}
}

func TestSampleSystemResourcesUsesBackgroundSamplerWhenRunning(t *testing.T) {
	stopSystemResourceSampler()
	// A long interval keeps the test deterministic: only the immediate startup
	// sample is ever stored.
	startSystemResourceSampler(time.Hour)
	t.Cleanup(stopSystemResourceSampler)

	var cached SystemResourceSnapshot
	ok := false
	for i := 0; i < 500 && !ok; i++ {
		cached, ok = latestBackgroundSystemResourceSample()
		if !ok {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatalf("expected background sampler to store a startup sample")
	}
	if got := sampleSystemResources(); got.SampledAt != cached.SampledAt {
		t.Fatalf("expected cached background sample %q, got %q", cached.SampledAt, got.SampledAt)
	}
	if got := sampleSystemResources(); got.SampledAt != cached.SampledAt {
		t.Fatalf("expected repeated polls to reuse the background sample, got %q", got.SampledAt)
	}

	stopSystemResourceSampler()
	if _, ok := latestBackgroundSystemResourceSample(); ok {
		t.Fatalf("expected stopped sampler to drop its cached sample")
	}
}

func TestSampleSystemResourcesFallsBackToDirectSampling(t *testing.T) {
	stopSystemResourceSampler()
	got := sampleSystemResources()
	if got.SampledAt == "" {
		t.Fatalf("expected direct sample to carry a timestamp")
	}
	if _, err := time.Parse(time.RFC3339Nano, got.SampledAt); err != nil {
		t.Fatalf("expected RFC3339Nano sampled_at, got %q: %v", got.SampledAt, err)
	}
}

func TestStartSystemResourceSamplerIsIdempotent(t *testing.T) {
	stopSystemResourceSampler()
	startSystemResourceSampler(time.Hour)
	t.Cleanup(stopSystemResourceSampler)
	startSystemResourceSampler(time.Hour)
	stopSystemResourceSampler()
	// A second stop must not panic on the already-stopped sampler.
	stopSystemResourceSampler()
}

func TestSystemResourceSamplerRejectsPreviousGeneration(t *testing.T) {
	stopSystemResourceSampler()
	startSystemResourceSampler(time.Hour)
	s := &backgroundSystemResourceSampler
	s.Lock()
	previousGeneration := s.generation
	s.Unlock()

	stopSystemResourceSampler()
	startSystemResourceSampler(time.Hour)
	t.Cleanup(stopSystemResourceSampler)
	s.Lock()
	currentGeneration := s.generation
	s.Unlock()
	if currentGeneration == previousGeneration {
		t.Fatal("expected sampler restart to advance generation")
	}

	storeBackgroundSystemResourceSample(previousGeneration, SystemResourceSnapshot{SampledAt: "stale-generation"})
	s.Lock()
	got := s.latest.SampledAt
	s.Unlock()
	if got == "stale-generation" {
		t.Fatal("previous sampler generation overwrote the current sample")
	}
}

// waitForSamplerRunning polls the running flag so the sampler's own goroutine
// scheduling — not a fixed sleep — decides when the assertion runs.
func waitForSamplerRunning(t *testing.T, want bool) {
	t.Helper()
	s := &backgroundSystemResourceSampler
	for i := 0; i < 500; i++ {
		s.Lock()
		got := s.running
		s.Unlock()
		if got == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for sampler running=%v", want)
}

func TestSystemResourceSamplerSurvivesPanickingStartupRead(t *testing.T) {
	stopSystemResourceSampler()
	original := sampleSystemResourcesNowFunc
	t.Cleanup(func() {
		// Stop the sampler before restoring the seam so the goroutine has
		// already exited and cleanup cannot race a live read.
		stopSystemResourceSampler()
		waitForSamplerRunning(t, false)
		sampleSystemResourcesNowFunc = original
	})

	// The startup read panics; the recovered panic must be contained to that
	// one sample so the loop stays alive and a later tick still succeeds.
	var mu sync.Mutex
	fail := true
	panicked := make(chan struct{})
	sampleSystemResourcesNowFunc = func() SystemResourceSnapshot {
		mu.Lock()
		shouldFail := fail
		mu.Unlock()
		if shouldFail {
			// Signal before panicking so the test never races ahead of the
			// fault; otherwise it could clear `fail` first and pass even when a
			// panic permanently kills the loop.
			select {
			case panicked <- struct{}{}:
			default:
			}
			panic("sampler read failed")
		}
		return SystemResourceSnapshot{SampledAt: "recovered-sample"}
	}
	startSystemResourceSampler(5 * time.Millisecond)

	select {
	case <-panicked:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the injected sampler read panic")
	}

	// The sampler is still armed: a panic must not silently retire it.
	waitForSamplerRunning(t, true)
	if _, ok := latestBackgroundSystemResourceSample(); ok {
		t.Fatal("expected no cached sample while every read panics")
	}

	mu.Lock()
	fail = false
	mu.Unlock()

	var restored SystemResourceSnapshot
	ok := false
	for i := 0; i < 500 && !ok; i++ {
		restored, ok = latestBackgroundSystemResourceSample()
		if !ok {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatal("expected sampling to resume after a recovered panic")
	}
	if restored.SampledAt != "recovered-sample" {
		t.Fatalf("expected the post-recovery sample, got %q", restored.SampledAt)
	}
}

func TestSystemResourceSamplerReleasesRunningFlagOnGoroutineExit(t *testing.T) {
	stopSystemResourceSampler()
	t.Cleanup(stopSystemResourceSampler)
	startSystemResourceSampler(time.Hour)
	waitForSamplerRunning(t, true)

	s := &backgroundSystemResourceSampler
	s.Lock()
	generation := s.generation
	stop := s.stop
	s.Unlock()

	// Close the stop channel directly so the goroutine exits on its own rather
	// than through stopSystemResourceSampler. The release defer is what keeps a
	// goroutine exit from stranding running=true, which would make every later
	// start a silent no-op while stale samples kept being served.
	close(stop)
	waitForSamplerRunning(t, false)
	if _, ok := latestBackgroundSystemResourceSample(); ok {
		t.Fatal("expected the released sampler to drop its cached sample")
	}

	// The released sampler must be restartable.
	startSystemResourceSampler(time.Hour)
	waitForSamplerRunning(t, true)
	s.Lock()
	restarted := s.generation
	s.Unlock()
	if restarted == generation {
		t.Fatal("expected the restart to advance the sampler generation")
	}
}

func TestSystemResourceSamplerKeepsSamplingAfterOneFailedRead(t *testing.T) {
	stopSystemResourceSampler()
	original := sampleSystemResourcesNowFunc
	t.Cleanup(func() {
		// Stop the sampler before restoring the seam so the goroutine has
		// already exited and cleanup cannot race a live read.
		stopSystemResourceSampler()
		waitForSamplerRunning(t, false)
		sampleSystemResourcesNowFunc = original
	})

	// The startup read panics and the ticker reads succeed, so a surviving
	// sample proves the loop kept ticking past the failed step instead of
	// dying with it.
	var mu sync.Mutex
	calls := 0
	sampleSystemResourcesNowFunc = func() SystemResourceSnapshot {
		mu.Lock()
		calls++
		attempt := calls
		mu.Unlock()
		if attempt == 1 {
			panic("first read failed")
		}
		return SystemResourceSnapshot{SampledAt: "sample-after-failure"}
	}
	startSystemResourceSampler(5 * time.Millisecond)

	var got SystemResourceSnapshot
	ok := false
	for i := 0; i < 500 && !ok; i++ {
		got, ok = latestBackgroundSystemResourceSample()
		if !ok {
			time.Sleep(10 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatal("expected the sampler to keep sampling after one read panicked")
	}
	if got.SampledAt != "sample-after-failure" {
		t.Fatalf("expected a post-failure sample, got %q", got.SampledAt)
	}
	waitForSamplerRunning(t, true)
}

func TestSystemResourceSamplerInvalidatesStaleSampleAfterReadPanic(t *testing.T) {
	stopSystemResourceSampler()
	original := sampleSystemResourcesNowFunc
	var failing atomic.Bool
	panicSeen := make(chan struct{}, 1)
	sampleSystemResourcesNowFunc = func() SystemResourceSnapshot {
		if failing.Load() {
			select {
			case panicSeen <- struct{}{}:
			default:
			}
			panic("sample failed after a published value")
		}
		return SystemResourceSnapshot{SampledAt: "initial-sample"}
	}
	t.Cleanup(func() {
		failing.Store(false)
		stopSystemResourceSampler()
		waitForSamplerRunning(t, false)
		sampleSystemResourcesNowFunc = original
	})

	startSystemResourceSampler(5 * time.Millisecond)
	var initial SystemResourceSnapshot
	for i := 0; i < 500; i++ {
		var ok bool
		initial, ok = latestBackgroundSystemResourceSample()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if initial.SampledAt != "initial-sample" {
		t.Fatalf("expected initial published sample, got %+v", initial)
	}
	failing.Store(true)
	select {
	case <-panicSeen:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the post-publication read panic")
	}
	invalidated := false
	for i := 0; i < 500; i++ {
		if _, ok := latestBackgroundSystemResourceSample(); !ok {
			invalidated = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !invalidated {
		t.Fatal("stale system sample remained published after a failed read")
	}
	if got := sampleSystemResources(); got.SampledAt != "" {
		t.Fatalf("expected unavailable sample while reads fail, got %+v", got)
	}

	failing.Store(false)
	var recovered SystemResourceSnapshot
	for i := 0; i < 500; i++ {
		var ok bool
		recovered, ok = latestBackgroundSystemResourceSample()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if recovered.SampledAt != "initial-sample" {
		t.Fatalf("expected sampling to resume with a fresh read, got %+v", recovered)
	}
}

func TestSampleSystemResourcesDoesNotDirectReadWhileBackgroundSamplerHasNoSample(t *testing.T) {
	stopSystemResourceSampler()
	original := sampleSystemResourcesNowFunc
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	finishRead := func() { releaseOnce.Do(func() { close(release) }) }
	sampleSystemResourcesNowFunc = func() SystemResourceSnapshot {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
		return SystemResourceSnapshot{SampledAt: "delayed-sample"}
	}
	t.Cleanup(func() {
		finishRead()
		stopSystemResourceSampler()
		waitForSamplerRunning(t, false)
		sampleSystemResourcesNowFunc = original
	})

	startSystemResourceSampler(time.Hour)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("background sampler did not enter its delayed read")
	}
	got := sampleSystemResources()
	if got.SampledAt != "" || len(got.Notes) == 0 {
		t.Fatalf("expected an unavailable non-blocking sample, got %+v", got)
	}
	finishRead()
	var published SystemResourceSnapshot
	for i := 0; i < 500; i++ {
		var ok bool
		published, ok = latestBackgroundSystemResourceSample()
		if ok {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if published.SampledAt != "delayed-sample" {
		t.Fatalf("expected delayed read to publish after release, got %+v", published)
	}
}

func TestSystemResourceSamplerStopResetsDeltaBaseline(t *testing.T) {
	systemResourceSampler.Lock()
	systemResourceSampler.previous = systemResourceCounters{CPUUser: 42}
	systemResourceSampler.previousAt = time.Now()
	systemResourceSampler.Unlock()
	stopSystemResourceSampler()
	systemResourceSampler.Lock()
	previous := systemResourceSampler.previous
	previousAt := systemResourceSampler.previousAt
	systemResourceSampler.Unlock()
	if previous != (systemResourceCounters{}) || !previousAt.IsZero() {
		t.Fatalf("stop left a cross-generation baseline: previous=%+v at=%v", previous, previousAt)
	}
}
