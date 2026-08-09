package main

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestCodingAgentUsageDecodersExtractVerifiedOutputShapes(t *testing.T) {
	registry := defaultCodingAgentRegistry(Config{})
	tests := []struct {
		name       string
		agent      string
		line       string
		output     int64
		cumulative bool
		identity   string
	}{
		{
			name: "codex cumulative", agent: "codex", output: 20, cumulative: true,
			line: `{"timestamp":"2026-08-02T11:59:30Z","payload":{"info":{"total_token_usage":{"input_tokens":900000,"output_tokens":20}}}}`,
		},
		{
			name: "claude growing message", agent: "claude", output: 7, identity: "session-a\x00msg-1",
			line: `{"timestamp":"2026-08-02T12:00:00Z","sessionId":"session-a","message":{"id":"msg-1","usage":{"input_tokens":1000,"output_tokens":7}}}`,
		},
		{
			name: "trae cumulative", agent: "trae", output: 31, cumulative: true,
			line: `{"timestamp":"2026-08-02T12:00:30Z","payload":{"info":{"total_token_usage":{"input_tokens":120,"output_tokens":31}}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoder, ok := registry.usageDecoder(test.agent)
			if !ok {
				t.Fatalf("%s usage decoder is unavailable", test.agent)
			}
			observation, ok := decoder.DecodeUsage([]byte(test.line))
			if !ok || observation.OutputTokens != test.output || observation.Cumulative != test.cumulative || observation.MessageIdentity != test.identity {
				t.Fatalf("decoded observation = %+v, ok=%t", observation, ok)
			}
			if observation.At.IsZero() {
				t.Fatal("verified timestamp was not decoded")
			}
		})
	}

	claude, _ := registry.usageDecoder("claude")
	inputOnly := []byte(`{"timestamp":"2026-08-02T12:00:00Z","usage":{"input_tokens":1000,"cached_input_tokens":900}}`)
	if observation, ok := claude.DecodeUsage(inputOnly); ok {
		t.Fatalf("input-only usage became output throughput: %+v", observation)
	}
	if _, ok := registry.usageDecoder("gemini"); ok {
		t.Fatal("process-only Gemini exposed output usage")
	}
}

func newTestLiveTokenRateSampler(cfg Config) *liveTokenRateSampler {
	return newLiveTokenRateSampler(cfg, defaultCodingAgentRegistry(cfg))
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

	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
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

func TestLiveTokenRateWatchDiscoversResumedOldSessionWithoutReplay(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "old-session.jsonl")
	writeCumulativeTokenFile(t, path, now.Add(-time.Hour), 100)
	if err := os.Chtimes(path, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.setWatchCoverage(true)
	sampler.poll(now)
	if got := len(sampler.files); got != 0 {
		t.Fatalf("old inactive files tracked at baseline = %d, want 0", got)
	}

	appendCumulativeTokenLine(t, path, now.Add(30*time.Second), 280)
	sampler.recordWatchBatch(liveTokenRateWatchBatch{Paths: []string{path}, Complete: true})
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.State != liveTokenRateStateZero || sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("resumed session replayed pre-baseline output: %+v", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 460)
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("resumed session delta sample = %+v, want 1 output token/second", sample)
	}
}

func TestLiveTokenRateWatchGapFailsClosedAndForcesRecovery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)

	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.setWatchCoverage(true)
	sampler.poll(now)
	sampler.recordWatchBatch(liveTokenRateWatchBatch{Complete: false})
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.State != liveTokenRateStateUnavailable || sample.OutputTokensPerSecond != nil || sample.UnavailableReason != liveTokenRateUnavailableWatchIncomplete {
		t.Fatalf("watch gap did not fail closed: %+v", sample)
	}
	if len(sampler.directories) == 0 {
		t.Fatal("watch gap did not rebuild the directory index")
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

	sampler := newTestLiveTokenRateSampler(Config{ClaudeRoots: []string{root}})
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

func TestLiveTokenRateMessageDedupeRemainsBoundedDuringIngest(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tracked := liveTokenRateTrackedFile{}
	for index := 0; index < liveTokenRateMaxMessages*4; index++ {
		identity := fmt.Sprintf("session-a\x00message-%05d", index)
		if delta := liveTokenRateMessageDelta(&tracked, identity, 1, now); delta != 1 {
			t.Fatalf("new message delta = %d, want 1", delta)
		}
		if len(tracked.MessageUsage) > liveTokenRateMaxMessages || tracked.MessageOrder.Len() > liveTokenRateMaxMessages {
			t.Fatalf("dedupe exceeded bound during ingest: map=%d order=%d", len(tracked.MessageUsage), tracked.MessageOrder.Len())
		}
	}
	identity := fmt.Sprintf("session-a\x00message-%05d", liveTokenRateMaxMessages*4-1)
	for output := int64(2); output < 1000; output++ {
		if delta := liveTokenRateMessageDelta(&tracked, identity, output, now); delta != 1 {
			t.Fatalf("growing message delta = %d, want 1", delta)
		}
		if len(tracked.MessageUsage) != liveTokenRateMaxMessages || tracked.MessageOrder.Len() != liveTokenRateMaxMessages {
			t.Fatalf("repeated update changed bounded state: map=%d order=%d", len(tracked.MessageUsage), tracked.MessageOrder.Len())
		}
	}
}

type blockingAgentUsageDecoder struct {
	delegate agentOutputUsageDecoder
	entered  chan struct{}
	release  chan struct{}
}

func (decoder blockingAgentUsageDecoder) DecodeUsage(line []byte) (liveTokenRateObservation, bool) {
	if bytes.Contains(line, []byte(`"block":true`)) {
		select {
		case decoder.entered <- struct{}{}:
		default:
		}
		<-decoder.release
	}
	return decoder.delegate.DecodeUsage(line)
}

func TestLiveTokenRateWatchBatchesMergeWhileAppendParsingIsBlocked(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "active.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	registry := newCodingAgentRegistry(codingAgentAdapter{
		ID: "codex",
		Capabilities: agentCapabilities{Usage: blockingAgentUsageDecoder{
			delegate: newCodexOutputUsageDecoder(), entered: entered, release: release,
		}},
	})
	sampler := newLiveTokenRateSampler(Config{CodexRoots: []string{root}}, registry)
	sampler.poll(now)
	appendTokenText(t, path, strings.Replace(cumulativeTokenLine(now.Add(30*time.Second), 280), `"payload"`, `"block":true,"payload"`, 1))

	pollDone := make(chan struct{})
	go func() {
		sampler.poll(now.Add(30 * time.Second))
		close(pollDone)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("append decoder did not block")
	}

	recordDone := make(chan struct{})
	go func() {
		sampler.recordWatchBatch(liveTokenRateWatchBatch{Complete: true, Paths: []string{"first.jsonl"}})
		sampler.recordWatchBatch(liveTokenRateWatchBatch{Complete: true, Paths: []string{"second.jsonl"}})
		close(recordDone)
	}()
	select {
	case <-recordDone:
	case <-time.After(time.Second):
		t.Fatal("watch intake blocked behind append parsing")
	}
	sampler.watchMu.Lock()
	pending := len(sampler.watchPending)
	sampler.watchMu.Unlock()
	if pending != 2 {
		t.Fatalf("pending watch paths = %d, want 2 merged batches", pending)
	}

	close(release)
	released = true
	select {
	case <-pollDone:
	case <-time.After(time.Second):
		t.Fatal("append poll did not resume")
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
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
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
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
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
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
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
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
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

func TestLiveTokenRateSamplerStreamsOversizedAppend(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 10)
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	fillerLine := `{"timestamp":"` + now.Add(30*time.Second).Format(time.RFC3339) + `","type":"noise"}` + "\n"
	filler := make([]byte, 0, liveTokenRateBaselineReadLimit+len(fillerLine))
	for len(filler) <= liveTokenRateBaselineReadLimit {
		filler = append(filler, fillerLine...)
	}
	filler = append(filler, []byte(cumulativeTokenLine(now.Add(30*time.Second), 190))...)
	appendTokenText(t, path, string(filler))
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("streamed append sample = %+v, want 1 output token/second", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 370)
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-2) > 0.0001 {
		t.Fatalf("second streamed delta sample = %+v, want 2 output tokens/second", sample)
	}
}

func TestLiveTokenRateSamplerWaitsForCompleteAppendedLine(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 10)
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	line := cumulativeTokenLine(now.Add(30*time.Second), 190)
	split := len(line) / 2
	appendTokenText(t, path, line[:split])
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("partial JSONL line produced throughput: %+v", sample)
	}

	appendTokenText(t, path, line[split:])
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("completed JSONL line sample = %+v, want 1 output token/second", sample)
	}
}

func TestLiveTokenRateBucketsBoundHighFrequencyUpdates(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	buckets := liveTokenRateBucketAccumulator{}
	for index := 0; index < 100_000; index++ {
		buckets.add(liveTokenRateEvent{At: now.Add(-time.Second), Tokens: 1, Session: "session-a"}, now)
	}
	events := buckets.events()
	if len(events) != 1 {
		t.Fatalf("100,000 same-second updates produced %d buckets, want 1", len(events))
	}
	tokens, sessions := liveTokenRateWindowFacts(events, now, liveTokenRateWindow, liveTokenRateFutureSkew)
	if tokens != 100_000 || sessions != 1 {
		t.Fatalf("bucketed high-frequency facts = %d tokens across %d sessions, want 100000/1", tokens, sessions)
	}
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{t.TempDir()}})
	sampler.buckets = events
	sampler.initialized = true
	sampler.latestSignal = now
	sampler.latestEvent = now
	sampler.publishLocked(now)
	sample := sampler.sample(now)
	if sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || sample.UnavailableReason != "" {
		t.Fatalf("high-frequency sample became unavailable: %+v", sample)
	}
}

func TestLiveTokenRateBucketsPreserveSparseIntervalClipping(t *testing.T) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	buckets := liveTokenRateBucketAccumulator{}
	buckets.add(newLiveTokenRateIntervalEvent(now.Add(-10*time.Minute), now, 6000, "session-a"), now)
	events := buckets.events()
	if tokens, _ := liveTokenRateWindowFacts(events, now, liveTokenRateWindow, liveTokenRateFutureSkew); tokens != 1800 {
		t.Fatalf("current bucketed interval = %d tokens, want 1800", tokens)
	}
	if tokens, _ := liveTokenRateWindowFacts(events, now.Add(time.Minute), liveTokenRateWindow, liveTokenRateFutureSkew); tokens != 1200 {
		t.Fatalf("bucketed interval one minute later = %d tokens, want 1200", tokens)
	}
	if tokens, _ := liveTokenRateWindowFacts(events, now.Add(4*time.Minute), liveTokenRateWindow, liveTokenRateFutureSkew); tokens != 0 {
		t.Fatalf("expired bucketed interval = %d tokens, want 0", tokens)
	}
}

func BenchmarkLiveTokenRateBucketsThirtyTwoMillionUpdates(b *testing.B) {
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	event := liveTokenRateEvent{At: now.Add(-time.Second), Tokens: 1, Session: "session-a"}
	buckets := liveTokenRateBucketAccumulator{}
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		buckets.add(event, now)
	}
	if len(buckets) != 1 {
		b.Fatalf("%d updates produced %d buckets, want 1", b.N, len(buckets))
	}
}

func TestLiveTokenRateSamplerLifecycleIsIdempotent(t *testing.T) {
	sampler := newTestLiveTokenRateSampler(Config{})
	sampler.start(time.Millisecond)
	sampler.start(time.Millisecond)
	sampler.stopSampler()
	sampler.stopSampler()
	if sample := sampler.sample(time.Now()); sample.State != liveTokenRateStateUnavailable || sample.UnavailableReason != liveTokenRateUnavailableNotConfigured {
		t.Fatalf("unconfigured lifecycle sample = %+v", sample)
	}
}

func TestLiveTokenRateDiscoveryPrunesTraeArtifactTrees(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	day := filepath.Join(root, "sessions", "2026", "08", "02")
	if err := os.MkdirAll(day, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(day, "rollout-session.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)

	for index := 0; index < liveTokenRateMaxDirectories+8; index++ {
		artifactDir := filepath.Join(day, fmt.Sprintf("rollout-%04d.artifacts", index), "tool-results")
		if err := os.MkdirAll(artifactDir, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	sampler := newTestLiveTokenRateSampler(Config{TraeRoots: []string{root}})
	sampler.poll(now)
	sample := sampler.sample(now)
	if sample.State != liveTokenRateStateZero || sample.OutputTokensPerSecond == nil {
		t.Fatalf("artifact tree made complete discovery unavailable: %+v", sample)
	}
	if got := len(sampler.directories); got != 4 {
		t.Fatalf("tracked directories = %d, want only sessions/year/month/day", got)
	}
	if got := len(sampler.files); got != 1 {
		t.Fatalf("tracked files = %d, want the transcript only", got)
	}
}

func TestLiveTokenRateDiscoveryReusesStableDirectoriesAndFindsNewEntries(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	writeCumulativeTokenFile(t, filepath.Join(sessions, "first.jsonl"), now, 100)

	readDir := liveTokenRateReadDir
	t.Cleanup(func() { liveTokenRateReadDir = readDir })
	readCount := 0
	liveTokenRateReadDir = func(path string) ([]os.DirEntry, error) {
		readCount++
		return readDir(path)
	}

	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.setWatchCoverage(true)
	sampler.poll(now)
	if readCount == 0 {
		t.Fatal("initial discovery did not read configured directories")
	}

	readCount = 0
	sampler.poll(now.Add(2 * time.Minute))
	if readCount != 0 {
		t.Fatalf("stable directory cache performed %d redundant reads", readCount)
	}

	writeCumulativeTokenFile(t, filepath.Join(sessions, "second.jsonl"), now.Add(150*time.Second), 200)
	if err := os.Chtimes(sessions, now.Add(150*time.Second), now.Add(150*time.Second)); err != nil {
		t.Fatal(err)
	}
	sampler.poll(now.Add(150 * time.Second))
	if got := len(sampler.files); got != 2 {
		t.Fatalf("tracked files after directory topology change = %d, want 2", got)
	}
	if readCount != 1 {
		t.Fatalf("topology change read %d directories, want one changed directory", readCount)
	}
}

func TestLiveTokenRateDynamicRootUsesPriorityFilesWithoutWalkingHistory(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "active.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)
	priority := []TranscriptFile{{Tool: "codex", Path: path}}
	for index := 0; index < liveTokenRateMaxFiles+8; index++ {
		oldPath := filepath.Join(sessions, fmt.Sprintf("old-%04d.jsonl", index))
		writeCumulativeTokenFile(t, oldPath, now.Add(-time.Hour), 100)
		if err := os.Chtimes(oldPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		priority = append(priority, TranscriptFile{Tool: "codex", Path: oldPath})
	}
	for index := 0; index < liveTokenRateMaxDirectories+8; index++ {
		if err := os.MkdirAll(filepath.Join(root, ".codexl", "history", fmt.Sprintf("lane-%04d", index)), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	readDir := liveTokenRateReadDir
	t.Cleanup(func() { liveTokenRateReadDir = readDir })
	readCount := 0
	liveTokenRateReadDir = func(path string) ([]os.DirEntry, error) {
		readCount++
		return readDir(path)
	}

	sampler := newTestLiveTokenRateSampler(Config{})
	sampler.setWatchCoverage(true)
	sampler.addSnapshotRoots(
		SnapshotConfig{CodexRoots: []string{root}},
		priority,
		map[string]string{liveTokenRateSessionKey("codex", path): "project-a"},
	)
	sampler.poll(now)
	if readCount != 0 || len(sampler.directories) != 0 {
		t.Fatalf("dynamic history was enumerated: reads=%d directories=%d", readCount, len(sampler.directories))
	}
	if got := len(sampler.files); got != 1 {
		t.Fatalf("priority files tracked = %d, want 1", got)
	}
	if sample := sampler.sample(now); sample.State != liveTokenRateStateZero || sample.OutputTokensPerSecond == nil {
		t.Fatalf("dynamic history made baseline unavailable: %+v", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(30*time.Second), 280)
	sampler.poll(now.Add(30 * time.Second))
	sample := sampler.sample(now.Add(30 * time.Second))
	if sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1) > 0.0001 {
		t.Fatalf("priority file delta = %+v, want 1 output token/second", sample)
	}
	if len(sample.Projects) != 1 || sample.Projects[0].Project != "project-a" || math.Abs(sample.Projects[0].OutputTokensPerSecond-1) > 0.0001 || sample.Projects[0].ActiveSessions != 1 {
		t.Fatalf("project throughput = %+v, want project-a at 1 output token/second", sample.Projects)
	}
}

func TestLiveTokenRateProjectsFromSessionsFailsConflictsToUnassigned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	projects := liveTokenRateProjectsFromSessions([]LiveSessionSnapshot{
		{Tool: "codex", Path: path, Project: "project-a"},
		{Tool: "codex", Path: path, Project: "project-b"},
	})
	if got := projects[liveTokenRateSessionKey("codex", path)]; got != liveTokenRateUnassignedProject {
		t.Fatalf("conflicting project attribution = %q, want %q", got, liveTokenRateUnassignedProject)
	}
}

func TestLiveTokenRatePublishedProjectsOnlyRetainEventSessions(t *testing.T) {
	now := time.Now().UTC()
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{t.TempDir()}})
	sampler.pollMu.Lock()
	sampler.buckets = []liveTokenRateEvent{{At: now, Tokens: 180, Session: "session-a"}}
	sampler.initialized = true
	sampler.latestSignal = now
	sampler.latestEvent = now
	sampler.sessionProjects = map[string]string{"session-a": "project-a"}
	for index := 0; index < 10_000; index++ {
		sampler.sessionProjects[fmt.Sprintf("inactive-%05d", index)] = "inactive"
	}
	sampler.publishLocked(now)
	sampler.pollMu.Unlock()

	sampler.publishedMu.RLock()
	projects := sampler.published.Projects
	sampler.publishedMu.RUnlock()
	if len(projects) != 1 || projects["session-a"] != "project-a" {
		t.Fatalf("published projects = %+v, want only the active-window event session", projects)
	}
	if sample := sampler.sample(now); len(sample.Projects) != 1 || sample.Projects[0].Project != "project-a" {
		t.Fatalf("sample project partition = %+v, want project-a only", sample.Projects)
	}
}

func TestLiveTokenRateConfiguredRootRemainsDiscoverableAfterSnapshotMerge(t *testing.T) {
	root := t.TempDir()
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.addSnapshotRoots(SnapshotConfig{CodexRoots: []string{root}}, nil, nil)
	for _, candidate := range sampler.roots {
		if !candidate.Discover {
			t.Fatalf("configured root lost discovery ownership: %+v", candidate)
		}
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
