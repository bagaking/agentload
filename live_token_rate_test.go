package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"container/list"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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
		{
			// Per-turn, NOT cumulative: real sessions show the output series
			// falling between turns. Flipping this to cumulative would make the
			// sampler read each turn as a fresh total and inflate the rate.
			name: "grok per turn", agent: "grok", output: 8007, identity: "01a058b6\x00p1",
			line: `{"timestamp":1788194990,"method":"_x.ai/session/update","params":{"sessionId":"01a058b6","update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{"inputTokens":442440,"outputTokens":8007,"totalTokens":450447,"costUsdTicks":946549800}}}}`,
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
	if decoder, ok := registry.usageDecoder("gemini"); !ok {
		t.Fatal("Gemini output usage decoder is unavailable")
	} else if observation, ok := decoder.DecodeUsage([]byte(`{"id":"gemini-1","timestamp":"2026-08-02T12:00:00Z","role":"assistant","usage":{"output":4}}`)); !ok || observation.OutputTokens != 4 {
		t.Fatalf("Gemini output usage was not decoded: %+v ok=%t", observation, ok)
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

	// With real tokens measured, the same gap is a floor rather than an unknown:
	// missing file events can only mean there was MORE throughput, never less,
	// so blanking a positive reading would hide throughput that did happen.
	appendCumulativeTokenLine(t, path, now.Add(60*time.Second), 400)
	sampler.poll(now.Add(60 * time.Second))
	sampler.evidenceIndex.recordWatchBatch(evidenceWatchBatch{Complete: false})
	sampler.poll(now.Add(90 * time.Second))
	sample := sampler.sample(now.Add(90 * time.Second))
	if sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond <= 0 {
		t.Fatalf("watch gap blanked a positive measurement: %+v", sample)
	}
	if sample.Coverage != liveTokenRateCoveragePartial || sample.CoverageReason != liveTokenRateUnavailableWatchIncomplete {
		t.Fatalf("degraded floor did not declare its coverage: %+v", sample)
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

// assertLiveTokenRateMessageStateConsistent locks the invariant the ingest fast
// path depends on: the map and order list name the same identities one-for-one,
// and every usage points at its own element in that list.
func assertLiveTokenRateMessageStateConsistent(t *testing.T, tracked *liveTokenRateTrackedFile) {
	t.Helper()
	if len(tracked.MessageUsage) == 0 {
		if tracked.MessageOrder != nil && tracked.MessageOrder.Len() != 0 {
			t.Fatalf("empty usage map kept %d order elements", tracked.MessageOrder.Len())
		}
		return
	}
	if tracked.MessageOrder == nil {
		t.Fatalf("usage map holds %d entries with no order list", len(tracked.MessageUsage))
	}
	if tracked.MessageOrder.Len() != len(tracked.MessageUsage) {
		t.Fatalf("order length %d does not match usage map size %d", tracked.MessageOrder.Len(), len(tracked.MessageUsage))
	}
	seen := map[string]struct{}{}
	for element := tracked.MessageOrder.Front(); element != nil; element = element.Next() {
		identity, ok := element.Value.(string)
		if !ok {
			t.Fatalf("order element holds a non-string value %v", element.Value)
		}
		if _, duplicate := seen[identity]; duplicate {
			t.Fatalf("order list repeats identity %q", identity)
		}
		seen[identity] = struct{}{}
		usage := tracked.MessageUsage[identity]
		if usage == nil {
			t.Fatalf("order list names identity %q that the usage map does not hold", identity)
		}
		if usage.order != element {
			t.Fatalf("usage %q points at a different element than the order list holds", identity)
		}
	}
	for identity, usage := range tracked.MessageUsage {
		if usage == nil {
			t.Fatalf("usage map holds a nil entry for %q", identity)
		}
		if usage.order == nil {
			t.Fatalf("usage %q has no order element", identity)
		}
		if _, ok := seen[identity]; !ok {
			t.Fatalf("usage %q is missing from the order list", identity)
		}
	}
}

func liveTokenRateMessageOrderIdentities(tracked *liveTokenRateTrackedFile) []string {
	if tracked.MessageOrder == nil {
		return nil
	}
	identities := make([]string, 0, tracked.MessageOrder.Len())
	for element := tracked.MessageOrder.Front(); element != nil; element = element.Next() {
		identity, _ := element.Value.(string)
		identities = append(identities, identity)
	}
	return identities
}

func TestLiveTokenRateMessageDedupeEvictsLeastRecentlySeen(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tracked := liveTokenRateTrackedFile{}
	identity := func(index int) string { return fmt.Sprintf("session-a\x00message-%05d", index) }
	for index := 0; index < liveTokenRateMaxMessages; index++ {
		liveTokenRateRememberMessage(&tracked, identity(index), 1, now)
	}

	// Re-touching the oldest identity must move it behind its successor, so the
	// next insert evicts that successor instead.
	liveTokenRateRememberMessage(&tracked, identity(0), 2, now)
	liveTokenRateRememberMessage(&tracked, "session-a\x00fresh", 5, now)

	if len(tracked.MessageUsage) != liveTokenRateMaxMessages {
		t.Fatalf("usage map size = %d, want %d", len(tracked.MessageUsage), liveTokenRateMaxMessages)
	}
	if tracked.MessageUsage[identity(0)] == nil {
		t.Fatalf("re-touched identity %q was evicted despite being most recent", identity(0))
	}
	if tracked.MessageUsage[identity(1)] != nil {
		t.Fatalf("least recently seen identity %q survived eviction", identity(1))
	}
	if tracked.MessageUsage["session-a\x00fresh"] == nil {
		t.Fatal("newly remembered identity was not retained")
	}
	if back := tracked.MessageOrder.Back(); back == nil || back.Value != "session-a\x00fresh" {
		t.Fatalf("most recent identity is not at the back of the order: %v", back)
	}
	assertLiveTokenRateMessageStateConsistent(t, &tracked)
}

func TestLiveTokenRateMessageDedupePrunesExpiredButKeepsFresh(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	expired := now.Add(-liveTokenRateMessageRetention - time.Minute)
	tracked := liveTokenRateTrackedFile{}
	liveTokenRateRememberMessage(&tracked, "session-a\x00expired-first", 3, expired)
	liveTokenRateRememberMessage(&tracked, "session-a\x00expired-second", 4, expired)
	liveTokenRateRememberMessage(&tracked, "session-a\x00fresh", 6, now)

	// A new message prunes by retention before inserting, so both expired
	// identities go and the fresh one stays deduped against its own history.
	if delta := liveTokenRateMessageDelta(&tracked, "session-a\x00incoming", 9, now); delta != 9 {
		t.Fatalf("incoming message delta = %d, want 9", delta)
	}
	if tracked.MessageUsage["session-a\x00expired-first"] != nil || tracked.MessageUsage["session-a\x00expired-second"] != nil {
		t.Fatalf("expired identities survived retention pruning: %+v", liveTokenRateMessageOrderIdentities(&tracked))
	}
	if tracked.MessageUsage["session-a\x00fresh"] == nil {
		t.Fatal("fresh identity was pruned with the expired ones")
	}
	if delta := liveTokenRateMessageDelta(&tracked, "session-a\x00fresh", 6, now); delta != 0 {
		t.Fatalf("retained identity lost its dedupe history: delta = %d, want 0", delta)
	}
	assertLiveTokenRateMessageStateConsistent(t, &tracked)
}

func TestLiveTokenRateMessageStateStaysConsistentAcrossSaturatedIngest(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tracked := liveTokenRateTrackedFile{}
	// Drive past the bound so inserts, LRU re-touches, evictions, and retention
	// pruning all interleave, then prove the pointers still agree.
	for index := 0; index < liveTokenRateMaxMessages*3; index++ {
		identity := fmt.Sprintf("session-a\x00message-%05d", index)
		at := now.Add(time.Duration(index) * time.Millisecond)
		if delta := liveTokenRateMessageDelta(&tracked, identity, 1, at); delta != 1 {
			t.Fatalf("new message delta = %d, want 1", delta)
		}
		if index%7 == 0 {
			if delta := liveTokenRateMessageDelta(&tracked, identity, 3, at); delta != 2 {
				t.Fatalf("re-touched message delta = %d, want 2", delta)
			}
		}
		if len(tracked.MessageUsage) > liveTokenRateMaxMessages || tracked.MessageOrder.Len() > liveTokenRateMaxMessages {
			t.Fatalf("dedupe exceeded bound: map=%d order=%d", len(tracked.MessageUsage), tracked.MessageOrder.Len())
		}
	}
	assertLiveTokenRateMessageStateConsistent(t, &tracked)

	liveTokenRatePruneMessages(&tracked, now.Add(liveTokenRateMessageRetention*2))
	if len(tracked.MessageUsage) != 0 {
		t.Fatalf("retention prune left %d entries", len(tracked.MessageUsage))
	}
	// A fully pruned file must still accept new messages.
	if delta := liveTokenRateMessageDelta(&tracked, "session-a\x00after-prune", 4, now); delta != 4 {
		t.Fatalf("post-prune delta = %d, want 4", delta)
	}
	assertLiveTokenRateMessageStateConsistent(t, &tracked)
}

func TestCloneLiveTokenRateMessagesIsolatesSourceState(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	source := liveTokenRateTrackedFile{}
	for index := 0; index < 3; index++ {
		liveTokenRateRememberMessage(&source, fmt.Sprintf("session-a\x00message-%d", index), int64(index+1), now)
	}
	before := liveTokenRateMessageOrderIdentities(&source)

	cloned := liveTokenRateTrackedFile{}
	cloned.MessageUsage, cloned.MessageOrder = cloneLiveTokenRateMessages(source.MessageUsage, source.MessageOrder)
	if len(cloned.MessageUsage) != len(source.MessageUsage) {
		t.Fatalf("clone size = %d, want %d", len(cloned.MessageUsage), len(source.MessageUsage))
	}
	for identity, usage := range cloned.MessageUsage {
		if usage == source.MessageUsage[identity] {
			t.Fatalf("clone shares the usage pointer for %q", identity)
		}
	}
	assertLiveTokenRateMessageStateConsistent(t, &cloned)

	// Mutating the clone must leave the source byte-for-byte intact.
	liveTokenRateRememberMessage(&cloned, "session-a\x00message-0", 99, now.Add(time.Hour))
	liveTokenRateForgetOldestMessage(&cloned)
	if got := liveTokenRateMessageOrderIdentities(&source); !slices.Equal(got, before) {
		t.Fatalf("source order changed with the clone: %v, want %v", got, before)
	}
	for index := 0; index < 3; index++ {
		identity := fmt.Sprintf("session-a\x00message-%d", index)
		usage := source.MessageUsage[identity]
		if usage == nil || usage.Output != int64(index+1) || !usage.LastSeen.Equal(now) {
			t.Fatalf("source usage %q was mutated: %+v", identity, usage)
		}
	}
	assertLiveTokenRateMessageStateConsistent(t, &source)
}

func TestCloneLiveTokenRateMessagesConvergesFromRepeatedOrderIdentity(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// A repeated identity in the source order would otherwise clone into an
	// order list longer than its map, leaving an element nothing can evict.
	source := liveTokenRateTrackedFile{
		MessageUsage: map[string]*liveTokenRateMessageUsage{
			"session-a\x00duplicated": {Output: 5, LastSeen: now},
			"session-a\x00missing":    {Output: 7, LastSeen: now},
		},
		MessageOrder: list.New(),
	}
	source.MessageOrder.PushBack("session-a\x00duplicated")
	source.MessageOrder.PushBack("session-a\x00duplicated")

	cloned := liveTokenRateTrackedFile{}
	cloned.MessageUsage, cloned.MessageOrder = cloneLiveTokenRateMessages(source.MessageUsage, source.MessageOrder)
	assertLiveTokenRateMessageStateConsistent(t, &cloned)
	if len(cloned.MessageUsage) != 2 {
		t.Fatalf("clone size = %d, want 2", len(cloned.MessageUsage))
	}
	if delta := liveTokenRateMessageDelta(&cloned, "session-a\x00duplicated", 8, now); delta != 3 {
		t.Fatalf("cloned dedupe delta = %d, want 3", delta)
	}
	assertLiveTokenRateMessageStateConsistent(t, &cloned)
}

func TestLiveTokenRateForgetOldestMessageAlwaysShrinksUsage(t *testing.T) {
	now := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	// An order list that names nothing the map holds must not stall the callers'
	// eviction loops, which bound the map rather than the list.
	tracked := liveTokenRateTrackedFile{
		MessageUsage: map[string]*liveTokenRateMessageUsage{
			"session-a\x00unreachable": {Output: 4, LastSeen: now},
		},
		MessageOrder: list.New(),
	}
	for len(tracked.MessageUsage) > 0 {
		before := len(tracked.MessageUsage)
		liveTokenRateForgetOldestMessage(&tracked)
		if len(tracked.MessageUsage) >= before {
			t.Fatalf("eviction made no progress at map size %d", before)
		}
	}

	// The same guarantee must hold when the list names a stale identity.
	tracked = liveTokenRateTrackedFile{
		MessageUsage: map[string]*liveTokenRateMessageUsage{
			"session-a\x00live": {Output: 4, LastSeen: now},
		},
		MessageOrder: list.New(),
	}
	tracked.MessageOrder.PushBack("session-a\x00stale")
	for attempts := 0; len(tracked.MessageUsage) > 0; attempts++ {
		if attempts > 4 {
			t.Fatalf("eviction did not drain a stale order list: map=%d", len(tracked.MessageUsage))
		}
		liveTokenRateForgetOldestMessage(&tracked)
	}
}

func TestLiveTokenRateReadAppendKeepsOriginalStateOnScannerError(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	projects := filepath.Join(root, "projects", "project-a")
	if err := os.MkdirAll(projects, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projects, "session.jsonl")
	claudeLine := func(at time.Time, id string, output int64) string {
		return `{"timestamp":"` + at.Format(time.RFC3339) + `","sessionId":"session-a","message":{"id":"` + id + `","usage":{"output_tokens":` + strconv.FormatInt(output, 10) + `}}}` + "\n"
	}
	if err := os.WriteFile(path, []byte(claudeLine(now, "msg-1", 10)), 0o600); err != nil {
		t.Fatal(err)
	}
	registry := defaultCodingAgentRegistry(Config{ClaudeRoots: []string{root}})
	sampler := newLiveTokenRateSampler(registry, newTranscriptEvidenceIndex(registry))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	original := sampler.rebaselineFile(path, "claude", info, now)
	if original.MessageUsage["session-a\x00msg-1"] == nil {
		t.Fatalf("baseline did not remember the message: %+v", original.MessageUsage)
	}
	snap := *original.MessageUsage["session-a\x00msg-1"]
	orderBefore := liveTokenRateMessageOrderIdentities(&original)

	// A single line past the scanner's token limit makes scanner.Err() report
	// bufio.ErrTooLong after the loop, which must roll the whole append back.
	oversized := append(bytes.Repeat([]byte("x"), liveTokenRateMaxJSONLineBytes+1), '\n')
	appendTokenText(t, path, string(oversized))
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder, ok := registry.usageDecoder("claude")
	if !ok {
		t.Fatal("claude usage decoder is unavailable")
	}
	updated, buckets, latestSignal, latestEvent := liveTokenRateReadAppend(path, original, info, now.Add(30*time.Second), decoder)
	if len(buckets) != 0 || !latestSignal.IsZero() || !latestEvent.IsZero() {
		t.Fatalf("scanner error still reported progress: buckets=%d signal=%v event=%v", len(buckets), latestSignal, latestEvent)
	}
	if updated.Offset != original.Offset {
		t.Fatalf("rolled-back offset = %d, want %d", updated.Offset, original.Offset)
	}
	usage := original.MessageUsage["session-a\x00msg-1"]
	if usage == nil || usage.Output != snap.Output || !usage.LastSeen.Equal(snap.LastSeen) {
		t.Fatalf("original usage was mutated through the clone: %+v, want %+v", usage, &snap)
	}
	if got := liveTokenRateMessageOrderIdentities(&original); !slices.Equal(got, orderBefore) {
		t.Fatalf("original order changed: %v, want %v", got, orderBefore)
	}
	assertLiveTokenRateMessageStateConsistent(t, &original)
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

func TestLiveTokenRateSamplerContinuesAfterPanickingPoll(t *testing.T) {
	sampler := newTestLiveTokenRateSampler(Config{})
	var calls atomic.Int32
	recovered := make(chan struct{})
	sampler.startLoop(5*time.Millisecond, func(time.Time) {
		if calls.Add(1) == 1 {
			panic("synthetic token poll failure")
		}
		select {
		case <-recovered:
		default:
			close(recovered)
		}
	})
	t.Cleanup(sampler.stopSampler)

	select {
	case <-recovered:
	case <-time.After(5 * time.Second):
		t.Fatal("token sampler stopped after a panicking poll")
	}
	if calls.Load() < 2 {
		t.Fatalf("poll calls = %d, want at least 2", calls.Load())
	}
	sampler.lifecycleMu.Lock()
	running := sampler.running
	sampler.lifecycleMu.Unlock()
	if !running {
		t.Fatal("token sampler was not still running after the recovered poll")
	}
}

func TestLiveTokenRateProjectsFromSessionsFailsConflictsToUnassigned(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	projects := liveTokenRateProjectsFromSessions([]snapshot.LiveSessionSnapshot{
		{Tool: "codex", Path: path, Project: "project-a"},
		{Tool: "codex", Path: path, Project: "project-b"},
	})
	if got := projects[liveTokenRateSessionKey("codex", path)]; got != liveTokenRateUnassignedProject {
		t.Fatalf("conflicting project attribution = %q, want %q", got, liveTokenRateUnassignedProject)
	}
}

func TestLiveTokenRateProjectsForBucketsRecoverUnmappedSessions(t *testing.T) {
	claude := filepath.Join(t.TempDir(), ".claude", "projects", "-Users-me-proj-flowlens--worktrees-wt-f-001", "abc.jsonl")
	// The real codex layout: a date tree whose leaf ("14") is a plausible-looking
	// directory name that names no project at all.
	plain := filepath.Join(t.TempDir(), ".codex", "sessions", "2026", "09", "14", "rollout-abc.jsonl")
	repoDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoDir, "checkout", ".git"), 0o755); err != nil {
		t.Fatalf("seed repo: %v", err)
	}
	inRepo := filepath.Join(repoDir, "checkout", "notes.jsonl")
	claudeKey := liveTokenRateSessionKey("claude", claude)
	plainKey := liveTokenRateSessionKey("codex", plain)
	repoKey := liveTokenRateSessionKey("codex", inRepo)

	// No snapshot entry for any session: the process behind the transcript
	// was never mapped, but the throughput it produced is still real.
	projects := liveTokenRateProjectsForBuckets([]liveTokenRateEvent{
		{Session: claudeKey}, {Session: plainKey}, {Session: repoKey},
	}, map[string]string{})

	if got := projects[claudeKey]; got != "flowlens" {
		t.Fatalf("claude transcript project = %q, want flowlens", got)
	}
	// A codex rollout lives in a transcript store that names no project, so it
	// stays honestly unassigned rather than inventing one from the date path.
	if got := projects[plainKey]; got != liveTokenRateUnassignedProject {
		t.Fatalf("projectless transcript = %q, want %q", got, liveTokenRateUnassignedProject)
	}
	// A transcript that really does sit inside a checkout still recovers its name.
	if got := projects[repoKey]; got != "checkout" {
		t.Fatalf("in-repo transcript project = %q, want checkout", got)
	}
}

func TestLiveTokenRateMinuteFactsAttributeLikeTheLiveSample(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Minute)
	root := t.TempDir()
	sessions := filepath.Join(root, ".claude", "projects", "-Users-me-proj-flowlens")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessions, "abc.jsonl")
	key := liveTokenRateSessionKey("claude", path)

	sampler := newTestLiveTokenRateSampler(Config{ClaudeRoots: []string{filepath.Join(root, ".claude")}})
	sampler.pollMu.Lock()
	sampler.buckets = []liveTokenRateEvent{{At: now.Add(-30 * time.Second), Tokens: 600, Session: key}}
	sampler.initialized = true
	sampler.latestSignal = now.Add(-30 * time.Second)
	sampler.latestEvent = now.Add(-30 * time.Second)
	// No live process mapped this session, exactly like a transcript whose agent
	// the process scan could not match.
	sampler.sessionProjects = map[string]string{}
	fact := sampler.minuteFactLocked(now.Add(-time.Minute), now)
	sampler.pollMu.Unlock()

	// The persisted minute must bucket the tokens the same way the live readout
	// does. Passing the raw session map here sent every unmapped session to
	// "unassigned" in history only, so the trend chart read ~80% unassigned while
	// the live value showed none — the same tokens, two different answers.
	if len(fact.Projects) != 1 || fact.Projects[0].Project != "flowlens" {
		t.Fatalf("persisted minute did not recover the project: %+v", fact.Projects)
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

func TestLiveTokenRateOverCapReportsFloorRatherThanCompleteReading(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := t.TempDir()
	sessions := testDatedSessions(root, now)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	// More simultaneously-hot transcripts than the sampler can track. The cap
	// keeps the hottest subset; what must never happen is the subset shipping as
	// a complete reading.
	total := liveTokenRateMaxFiles + 8
	paths := make([]string, 0, total)
	for index := 0; index < total; index++ {
		path := filepath.Join(sessions, "session-"+strconv.Itoa(index)+".jsonl")
		writeCumulativeTokenFile(t, path, now, 100)
		paths = append(paths, path)
	}

	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{root}})
	sampler.poll(now)
	for _, path := range paths {
		appendCumulativeTokenLine(t, path, now.Add(30*time.Second), 400)
	}
	sampler.poll(now.Add(30 * time.Second))

	sample := sampler.sample(now.Add(30 * time.Second))
	if sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond <= 0 {
		t.Fatalf("over-cap sampling must still produce a rate: %+v", sample)
	}
	if sample.Coverage != liveTokenRateCoveragePartial {
		t.Fatalf("subset reading shipped as complete: coverage=%q tracked=%d eligible=%d", sample.Coverage, sample.TrackedFileCount, sample.EligibleFileCount)
	}
	if sample.TrackedFileCount > liveTokenRateMaxFiles || sample.EligibleFileCount <= sample.TrackedFileCount {
		t.Fatalf("coverage counts do not describe the cap: tracked=%d eligible=%d", sample.TrackedFileCount, sample.EligibleFileCount)
	}
}
