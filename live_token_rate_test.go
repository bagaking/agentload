package main

import (
	"bytes"
	"container/list"
	"context"
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
	registry := defaultCodingAgentRegistry(cfg)
	return newLiveTokenRateSampler(registry, newTranscriptEvidenceIndex(registry))
}

func testDatedSessions(root string, now time.Time) string {
	return filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
}

func TestLiveTokenRateSamplerStartsFromBaselineAndUsesCumulativeDelta(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
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
	if sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-0.6) > 0.0001 {
		t.Fatalf("cumulative sample = %+v, want 0.6 output tokens/second", sample)
	}
	if sample.ActiveSessions != 1 {
		t.Fatalf("active sessions = %d, want 1", sample.ActiveSessions)
	}
}

func TestLiveTokenRateWatchDiscoversResumedOldSessionWithoutReplay(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "old-session.jsonl")
	writeCumulativeTokenFile(t, path, now.Add(-time.Hour), 100)
	if err := os.Chtimes(path, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)
	if got := len(sampler.files); got != 0 {
		t.Fatalf("old inactive files tracked at baseline = %d, want 0", got)
	}

	appendCumulativeTokenLine(t, path, now.Add(30*time.Second), 280)
	sampler.evidenceIndex.recordWatchBatch(evidenceWatchBatch{Paths: []string{path}, Complete: true})
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.State != liveTokenRateStateZero || sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("resumed session replayed pre-baseline output: %+v", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 460)
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-0.6) > 0.0001 {
		t.Fatalf("resumed session delta sample = %+v, want 0.6 output tokens/second", sample)
	}
}

func TestLiveTokenRateWatchGapFailsClosedAndForcesRecovery(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 100)

	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)
	sampler.evidenceIndex.recordWatchBatch(evidenceWatchBatch{Complete: false})
	sampler.poll(now.Add(30 * time.Second))
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.State != liveTokenRateStateUnavailable || sample.OutputTokensPerSecond != nil || sample.UnavailableReason != liveTokenRateUnavailableWatchIncomplete {
		t.Fatalf("watch gap did not fail closed: %+v", sample)
	}
	if recovered := sampler.evidenceIndex.snapshot(context.Background(), now.Add(-liveTokenRateRecentFileAge), nil); !recovered.Complete {
		t.Fatalf("watch gap did not restore the evidence index: %+v", recovered)
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

func TestLiveTokenRateMessageDedupeSurvivesFullyPrunedState(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tracked := liveTokenRateTrackedFile{}
	liveTokenRateRememberMessage(&tracked, "session-a\x00old-message", 10, now.Add(-liveTokenRateMessageRetention-time.Second))

	if delta := liveTokenRateMessageDelta(&tracked, "session-a\x00new-message", 7, now); delta != 7 {
		t.Fatalf("new message delta after full prune = %d, want 7", delta)
	}
	if len(tracked.MessageUsage) != 1 || tracked.MessageOrder == nil || tracked.MessageOrder.Len() != 1 {
		t.Fatalf("unexpected dedupe state after full prune: map=%d order=%v", len(tracked.MessageUsage), tracked.MessageOrder)
	}
	if tracked.MessageUsage["session-a\x00new-message"] == nil {
		t.Fatalf("new message was not retained after full prune: %+v", tracked.MessageUsage)
	}
}

func TestLiveTokenRateMessageDedupeRepairsMissingOrderElement(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tracked := liveTokenRateTrackedFile{
		MessageUsage: map[string]*liveTokenRateMessageUsage{
			"session-a\x00message": {Output: 5, LastSeen: now},
		},
	}

	if delta := liveTokenRateMessageDelta(&tracked, "session-a\x00message", 8, now); delta != 3 {
		t.Fatalf("existing message delta with missing order element = %d, want 3", delta)
	}
	usage := tracked.MessageUsage["session-a\x00message"]
	if tracked.MessageOrder == nil || tracked.MessageOrder.Len() != 1 || usage == nil || usage.order == nil {
		t.Fatalf("missing order element was not repaired: map=%+v order=%v", tracked.MessageUsage, tracked.MessageOrder)
	}
}

func TestLiveTokenRateMessageDedupeRepairsFullMapWithMissingOrder(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tracked := liveTokenRateTrackedFile{MessageUsage: map[string]*liveTokenRateMessageUsage{}}
	for index := 0; index < liveTokenRateMaxMessages; index++ {
		identity := fmt.Sprintf("session-a\x00message-%05d", index)
		tracked.MessageUsage[identity] = &liveTokenRateMessageUsage{Output: 1, LastSeen: now}
	}

	if delta := liveTokenRateMessageDelta(&tracked, "session-a\x00new-message", 3, now); delta != 3 {
		t.Fatalf("new message delta with full map and missing order = %d, want 3", delta)
	}
	if len(tracked.MessageUsage) > liveTokenRateMaxMessages || tracked.MessageOrder == nil || tracked.MessageOrder.Len() > liveTokenRateMaxMessages {
		t.Fatalf("dedupe state exceeded bound after order repair: map=%d order=%v", len(tracked.MessageUsage), tracked.MessageOrder)
	}
	if tracked.MessageUsage["session-a\x00new-message"] == nil {
		t.Fatalf("new message was not retained after order repair")
	}
}

func TestLiveTokenRateMessageDedupeRebuildsInconsistentOrder(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tracked := liveTokenRateTrackedFile{
		MessageUsage: map[string]*liveTokenRateMessageUsage{
			"session-a\x00old-message": {Output: 5, LastSeen: now.Add(-time.Minute)},
			"session-a\x00new-message": {Output: 8, LastSeen: now},
		},
		MessageOrder: list.New(),
	}
	tracked.MessageOrder.PushBack("stale-message")
	tracked.MessageOrder.PushBack("session-a\x00new-message")

	if delta := liveTokenRateMessageDelta(&tracked, "session-a\x00new-message", 11, now); delta != 3 {
		t.Fatalf("new message delta with inconsistent order = %d, want 3", delta)
	}
	if tracked.MessageOrder == nil || tracked.MessageOrder.Len() != 2 {
		t.Fatalf("unexpected rebuilt order length: %v", tracked.MessageOrder)
	}
	if front := tracked.MessageOrder.Front(); front == nil || front.Value != "session-a\x00old-message" {
		t.Fatalf("expected oldest message first after rebuild, got %v", front)
	}
	if back := tracked.MessageOrder.Back(); back == nil || back.Value != "session-a\x00new-message" {
		t.Fatalf("expected newest message last after rebuild, got %v", back)
	}
	if tracked.MessageUsage["session-a\x00old-message"].order == nil || tracked.MessageUsage["session-a\x00new-message"].order == nil {
		t.Fatalf("rebuilt order did not restore usage pointers: %+v", tracked.MessageUsage)
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

func TestLiveTokenRateWatchUpdatesIndexWhileAppendParsingIsBlocked(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
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
	cfg := Config{CodexRoots: []string{root}}
	registry := defaultCodingAgentRegistry(cfg)
	registry.mu.Lock()
	codexIndex := registry.byID["codex"]
	registry.adapters[codexIndex].Capabilities.Usage = blockingAgentUsageDecoder{
		delegate: newCodexOutputUsageDecoder(), entered: entered, release: release,
	}
	registry.mu.Unlock()
	evidenceIndex := newTranscriptEvidenceIndex(registry)
	sampler := newLiveTokenRateSampler(registry, evidenceIndex)
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
	firstPath := filepath.Join(sessions, "first.jsonl")
	secondPath := filepath.Join(sessions, "second.jsonl")
	writeCumulativeTokenFile(t, firstPath, now.Add(30*time.Second), 1)
	writeCumulativeTokenFile(t, secondPath, now.Add(30*time.Second), 1)

	recordDone := make(chan struct{})
	go func() {
		evidenceIndex.recordWatchBatch(evidenceWatchBatch{Complete: true, Paths: []string{firstPath}})
		evidenceIndex.recordWatchBatch(evidenceWatchBatch{Complete: true, Paths: []string{secondPath}})
		close(recordDone)
	}()
	select {
	case <-recordDone:
	case <-time.After(time.Second):
		t.Fatal("watch intake blocked behind append parsing")
	}
	evidenceIndex.mu.Lock()
	_, firstIndexed := evidenceIndex.files[canonicalEvidencePath(firstPath)]
	_, secondIndexed := evidenceIndex.files[canonicalEvidencePath(secondPath)]
	pending := len(evidenceIndex.mutations)
	evidenceIndex.mu.Unlock()
	if !firstIndexed || !secondIndexed || pending != 0 {
		t.Fatalf("watch updates: first=%t second=%t reconcile_mutations=%d", firstIndexed, secondIndexed, pending)
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
	sessions := testDatedSessions(root, now)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "session.jsonl")
	writeCumulativeTokenFile(t, path, now, 10)
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)

	appendCumulativeTokenLine(t, path, now.Add(301*time.Second), 1000)
	sampler.poll(now.Add(301 * time.Second))
	if sample := sampler.sample(now.Add(301 * time.Second)); sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 0 {
		t.Fatalf("observation gap replayed output history: %+v", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(331*time.Second), 1180)
	sampler.poll(now.Add(331 * time.Second))
	if sample := sampler.sample(now.Add(331 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-0.6) > 0.0001 {
		t.Fatalf("post-gap delta sample = %+v, want 0.6 output tokens/second", sample)
	}
}

func TestLiveTokenRateSamplerDoesNotReplayCounterWithRegressedTimestamp(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
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
	sessions := testDatedSessions(root, now)
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
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-0.6) > 0.0001 {
		t.Fatalf("post-rewrite delta sample = %+v, want 0.6 output tokens/second", sample)
	}
}

func TestLiveTokenRateSamplerRebaselinesTruncatedCounter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
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
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-0.6) > 0.0001 {
		t.Fatalf("post-truncation delta sample = %+v, want 0.6 output tokens/second", sample)
	}
}

func TestLiveTokenRateSamplerStreamsOversizedAppend(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
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
	if sample := sampler.sample(now.Add(30 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-0.6) > 0.0001 {
		t.Fatalf("streamed append sample = %+v, want 0.6 output tokens/second", sample)
	}

	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 370)
	sampler.poll(now.Add(60 * time.Second))
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-1.2) > 0.0001 {
		t.Fatalf("second streamed delta sample = %+v, want 1.2 output tokens/second", sample)
	}
}

func TestLiveTokenRateSamplerWaitsForCompleteAppendedLine(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
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
	if sample := sampler.sample(now.Add(60 * time.Second)); sample.OutputTokensPerSecond == nil || math.Abs(*sample.OutputTokensPerSecond-0.6) > 0.0001 {
		t.Fatalf("completed JSONL line sample = %+v, want 0.6 output tokens/second", sample)
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
	if tokens, _ := liveTokenRateWindowFacts(events, now, liveTokenRateWindow, liveTokenRateFutureSkew); tokens != 3000 {
		t.Fatalf("current bucketed interval = %d tokens, want 3000", tokens)
	}
	if tokens, _ := liveTokenRateWindowFacts(events, now.Add(time.Minute), liveTokenRateWindow, liveTokenRateFutureSkew); tokens != 2400 {
		t.Fatalf("bucketed interval one minute later = %d tokens, want 2400", tokens)
	}
	if tokens, _ := liveTokenRateWindowFacts(events, now.Add(6*time.Minute), liveTokenRateWindow, liveTokenRateFutureSkew); tokens != 0 {
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
