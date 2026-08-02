package main

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestLiveTokenRateObservationParsesOutputOnly(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	codex := []byte(`{"timestamp":"2026-08-02T11:59:30Z","payload":{"info":{"total_token_usage":{"input_tokens":900000,"cached_input_tokens":800000,"output_tokens":20,"reasoning_output_tokens":50000}}}}`)
	observation, ok := liveTokenRateObservationFromJSONLine(codex, now)
	if !ok || !observation.Cumulative || observation.OutputTokens != 20 {
		t.Fatalf("Codex cumulative observation = %+v, ok=%t", observation, ok)
	}
	if !observation.At.Equal(time.Date(2026, 8, 2, 11, 59, 30, 0, time.UTC)) {
		t.Fatalf("Codex timestamp = %s", observation.At)
	}

	claude := []byte(`{"timestamp":"2026-08-02T12:00:00Z","sessionId":"session-a","message":{"id":"msg-1","usage":{"input_tokens":1000,"output_tokens":7}}}`)
	observation, ok = liveTokenRateObservationFromJSONLine(claude, now)
	if !ok || observation.Cumulative || observation.OutputTokens != 7 || observation.MessageIdentity != "session-a\x00msg-1" {
		t.Fatalf("Claude message observation = %+v, ok=%t", observation, ok)
	}

	inputOnly := []byte(`{"timestamp":"2026-08-02T12:00:00Z","usage":{"input_tokens":1000,"cached_input_tokens":900}}`)
	if observation, ok = liveTokenRateObservationFromJSONLine(inputOnly, now); ok {
		t.Fatalf("input-only usage became output throughput: %+v", observation)
	}
}

func TestLiveTokenRateSamplerStartsFromBaselineAndUsesCumulativeDelta(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)

	sampler := newLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)
	initial := sampler.sample(now)
	if initial.State != liveTokenRateStateZero || initial.OutputTokensPerSecond == nil || *initial.OutputTokensPerSecond != 0 {
		t.Fatalf("initial sample replayed baseline: %+v", initial)
	}

	appendCumulativeTokenLine(t, path, now.Add(30*time.Second), 280)
	sampler.poll(now.Add(30 * time.Second))
	sample := sampler.sample(now.Add(30 * time.Second))
	if sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("cumulative sample = %+v, want 1 output token/second", sample)
	}
	if sample.ActiveSessions != 1 {
		t.Fatalf("active sessions = %d, want 1", sample.ActiveSessions)
	}
}

func TestLiveTokenRateSamplerDedupesGrowingClaudeMessage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	projects := filepath.Join(root, "projects", "project-a")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projects, "session.jsonl")
	line := func(at time.Time, output int64) string {
		return `{"timestamp":"` + at.Format(time.RFC3339) + `","sessionId":"session-a","message":{"id":"msg-1","usage":{"output_tokens":` + strconv.FormatInt(output, 10) + `}}}` + "\n"
	}
	if err := os.WriteFile(path, []byte(line(now, 100)), 0o600); err != nil {
		t.Fatal(err)
	}

	sampler := newLiveTokenRateSampler(Config{ClaudeRoots: []string{root}})
	sampler.poll(now)
	appendTokenText(t, path, line(now.Add(30*time.Second), 100))
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("repeated message produced throughput: %+v", sample)
	}

	appendTokenText(t, path, line(now.Add(60*time.Second), 140))
	sampler.poll(now.Add(60 * time.Second))
	sample := sampler.sample(now.Add(60 * time.Second))
	want := 40.0 / liveTokenRateWindow.Seconds()
	if sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-want) > 0.0001 {
		t.Fatalf("growing message rate = %+v, want %f", sample, want)
	}
}

func TestLiveTokenRateSamplerRebaselinesAfterObservationGap(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 10)
	sampler := newLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	appendCumulativeTokenLine(t, path, now.Add(181*time.Second), 1000)
	sampler.poll(now.Add(181 * time.Second))
	if sample := sampler.sample(now.Add(181 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("observation gap replayed output history: %+v", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(211*time.Second), 1180)
	sampler.poll(now.Add(211 * time.Second))
	if sample := sampler.sample(now.Add(211 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("post-gap delta sample = %+v, want 1 output token/second", sample)
	}
}

func TestLiveTokenRateSamplerDoesNotReplayCounterWithRegressedTimestamp(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)
	sampler := newLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	appendCumulativeTokenLine(t, path, now.Add(-time.Minute), 10000)
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("regressed timestamp replayed cumulative delta: %+v", sample)
	}
}

func TestLiveTokenRateSamplerRebaselinesRewrittenFile(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)
	sampler := newLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	writeCumulativeTokenFile(t, path, now.Add(30*time.Second), 10000)
	if err := os.Chtimes(path, now.Add(30*time.Second), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("rewritten file replayed replacement total: %+v", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 10180)
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("post-rewrite delta sample = %+v, want 1 output token/second", sample)
	}
}

func TestLiveTokenRateSamplerRebaselinesTruncatedCounter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 900000)
	sampler := newLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	writeCumulativeTokenFile(t, path, now.Add(30*time.Second), 10)
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("truncated counter produced throughput: %+v", sample)
	}
	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 190)
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("post-truncation delta sample = %+v, want 1 output token/second", sample)
	}
}

func TestLiveTokenRateSamplerRebaselinesOversizedAppend(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 10)
	sampler := newLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	fillerLine := `{"timestamp":"` + now.Add(30*time.Second).Format(time.RFC3339) + `","usage":{"output_tokens":999}}` + "\n"
	filler := make([]byte, 0, liveTokenRateMaxAppendRead+len(fillerLine))
	for len(filler) <= liveTokenRateMaxAppendRead {
		filler = append(filler, fillerLine...)
	}
	filler = append(filler, []byte(cumulativeTokenLine(now.Add(30*time.Second), 9000000))...)
	appendTokenText(t, path, string(filler))
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("oversized append replayed skipped output: %+v", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 9000180)
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("post-gap delta sample = %+v, want 1 output token/second", sample)
	}
}

func TestLiveTokenRateSamplerLifecycleIsIdempotent(t *testing.T) {
	sampler := newLiveTokenRateSampler(Config{})
	sampler.start(time.Millisecond)
	sampler.start(time.Millisecond)
	sampler.stopSampler()
	sampler.stopSampler()
	if sample := sampler.sample(time.Now()); sample.State != liveTokenRateStateUnavailable {
		t.Fatalf("unconfigured lifecycle sample = %+v", sample)
	}
}

func writeCumulativeTokenFile(t *testing.T, path string, at time.Time, output int64) {
	t.Helper()
	line := cumulativeTokenLine(at, output)
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendCumulativeTokenLine(t *testing.T, path string, at time.Time, output int64) {
	t.Helper()
	appendTokenText(t, path, cumulativeTokenLine(at, output))
}

func cumulativeTokenLine(at time.Time, output int64) string {
	return `{"timestamp":"` + at.Format(time.RFC3339) + `","payload":{"info":{"total_token_usage":{"input_tokens":100,"output_tokens":` + strconv.FormatInt(output, 10) + `}}}}` + "\n"
}

func appendTokenText(t *testing.T, path, text string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(text); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}
