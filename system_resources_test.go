package main

import (
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
