package main

import (
	"math"
	"testing"
	"time"
)

func TestLiveTokenRateIntervalEventClipsToTrailingWindow(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 10, 0, 0, time.UTC)
	event := newLiveTokenRateIntervalEvent(now.Add(-10*time.Minute), now, 6000, "session-a")
	if got := liveTokenRateEventTokensInWindow(event, now, 180*time.Second, 5*time.Second); got != 1800 {
		t.Fatalf("tokens in current window = %d, want 1800", got)
	}
	if got := liveTokenRateEventTokensInWindow(event, now.Add(time.Minute), 180*time.Second, 5*time.Second); got != 1200 {
		t.Fatalf("tokens one minute later = %d, want 1200", got)
	}
	if got := liveTokenRateEventTokensInWindow(event, now.Add(4*time.Minute), 180*time.Second, 5*time.Second); got != 0 {
		t.Fatalf("expired interval tokens = %d, want 0", got)
	}
}

func TestLiveTokenRateSampleKeepsMissingStatesDistinctFromZero(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	base := liveTokenRateFacts{
		Window: 180 * time.Second, SampleInterval: 30 * time.Second, StaleAfter: 5 * time.Minute, SampledAt: now,
	}

	unavailable := liveTokenRateSampleFromFacts(base)
	if unavailable.State != liveTokenRateStateUnavailable || unavailable.OutputTokensPerSecond != nil || unavailable.UnavailableReason != liveTokenRateUnavailableNotConfigured {
		t.Fatalf("unavailable sample = %+v", unavailable)
	}

	base.Configured = true
	base.Limited = true
	base.UnavailableReason = liveTokenRateUnavailableFileCapacity
	limited := liveTokenRateSampleFromFacts(base)
	if limited.State != liveTokenRateStateUnavailable || limited.OutputTokensPerSecond != nil || limited.UnavailableReason != liveTokenRateUnavailableFileCapacity {
		t.Fatalf("limited sample = %+v", limited)
	}

	base.Limited = false
	base.UnavailableReason = ""
	noData := liveTokenRateSampleFromFacts(base)
	if noData.State != liveTokenRateStateNoData || noData.OutputTokensPerSecond != nil {
		t.Fatalf("no-data sample = %+v", noData)
	}

	base.Initialized = true
	base.LatestSignal = now.Add(-time.Minute)
	zero := liveTokenRateSampleFromFacts(base)
	if zero.State != liveTokenRateStateZero || zero.OutputTokensPerSecond == nil || *zero.OutputTokensPerSecond != 0 {
		t.Fatalf("zero sample = %+v", zero)
	}

	base.LatestSignal = now.Add(-6 * time.Minute)
	stale := liveTokenRateSampleFromFacts(base)
	if stale.State != liveTokenRateStateStale || stale.OutputTokensPerSecond != nil {
		t.Fatalf("stale sample = %+v", stale)
	}
}

func TestLiveTokenRateSampleUsesOutputTokensOverWallTime(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	sample := liveTokenRateSampleFromFacts(liveTokenRateFacts{
		Configured: true, Initialized: true, TokensInWindow: 360, ActiveSessions: 2,
		LatestSignal: now, LatestEvent: now, Window: 180 * time.Second,
		SampleInterval: 30 * time.Second, StaleAfter: 5 * time.Minute, SampledAt: now,
	})
	if sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-2) > 0.0001 {
		t.Fatalf("live sample = %+v, want 2 output tokens/second", sample)
	}
	if sample.Basis != liveTokenRateBasis || sample.Method != liveTokenRateMethod || sample.ActiveSessions != 2 {
		t.Fatalf("live sample metadata = %+v", sample)
	}
}

func TestLiveTokenRateWindowBreakdownPreservesAggregateAndUnassignedTruth(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	events := []liveTokenRateEvent{
		{At: now, Tokens: 180, Session: "session-a"},
		{At: now, Tokens: 360, Session: "session-b"},
		{At: now, Tokens: 90, Session: "session-c"},
	}
	tokens, sessions, projects := liveTokenRateWindowBreakdown(events, map[string]string{
		"session-a": "project-a",
		"session-b": "project-a",
	}, now, 180*time.Second, 5*time.Second)
	if tokens != 630 || sessions != 3 {
		t.Fatalf("aggregate breakdown = %d tokens across %d sessions, want 630/3", tokens, sessions)
	}
	if got := projects["project-a"]; got.TokensInWindow != 540 || len(got.Sessions) != 2 {
		t.Fatalf("project-a breakdown = %+v, want 540 tokens across 2 sessions", got)
	}
	if got := projects[liveTokenRateUnassignedProject]; got.TokensInWindow != 90 || len(got.Sessions) != 1 {
		t.Fatalf("unassigned breakdown = %+v, want 90 tokens across 1 session", got)
	}
}
