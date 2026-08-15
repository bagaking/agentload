package main

import (
	"agentload/internal/trajectory"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

func TestThroughputRecoveryMetadataDoesNotRetainSourcePrefix(t *testing.T) {
	const secret = "private-prompt-sentinel-should-never-be-persisted"
	dir := t.TempDir()
	history := filepath.Join(dir, "history.jsonl")
	cacheDir := history + ".throughput-index"
	if err := os.Mkdir(cacheDir, 0700); err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(cacheDir, "usage.bbolt")
	legacy, err := bolt.Open(cachePath, 0600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = legacy.Update(func(tx *bolt.Tx) error {
		b, e := tx.CreateBucket([]byte("usage-sources-v1"))
		if e != nil {
			return e
		}
		return b.Put([]byte("old-prefix"), []byte(secret))
	})
	legacy.Close()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	store, err := loadThroughputHistoryStore(history, now)
	if err != nil {
		t.Fatal(err)
	}
	r, err := openThroughputRecovery(history, store, defaultCodingAgentRegistry(defaultConfig()))
	if err != nil {
		t.Fatal(err)
	}
	defer r.db.Close()
	prompt, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": secret}}}})
	body := string(prompt) + "\n" + replayUsageRecord(now.Add(-2*time.Minute), 100) + replayUsageRecord(now.Add(-time.Minute), 160)
	path := filepath.Join(dir, "session.jsonl")
	if err = os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	prepareReplayFixture(t, r, trajectory.Source{Agent: "codex", Path: path}, now)
	if total := replayTotal(t, r, now); total != 60 {
		t.Fatal("rebuild lost native usage", total)
	}
	data, err := os.ReadFile(cachePath)
	if err != nil || bytes.Contains(data, []byte(secret)) {
		t.Fatal("usage-only checkpoint persisted prompt bytes", err)
	}
}

func replayUsageRecord(at time.Time, total int64) string {
	b, _ := json.Marshal(map[string]any{"timestamp": at.UTC().Format(time.RFC3339Nano), "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": map[string]int64{"output_tokens": total}}}})
	return string(b) + "\n"
}
func throughputReplayFixture(t *testing.T, agent, body string) (*throughputRecovery, trajectory.Source, time.Time) {
	t.Helper()
	dir := t.TempDir()
	history := filepath.Join(dir, "history.jsonl")
	now := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	store, _ := loadThroughputHistoryStore(history, now)
	r, err := openThroughputRecovery(history, store, defaultCodingAgentRegistry(cfg))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.db.Close() })
	return r, trajectory.Source{Agent: agent, Path: path}, now
}
func prepareReplayFixture(t *testing.T, r *throughputRecovery, src trajectory.Source, now time.Time) {
	t.Helper()
	for i := 0; i < 100; i++ {
		more, err := r.prepareSource(context.Background(), src, now)
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return
		}
	}
	t.Fatal("replay did not converge")
}
func replayTotal(t *testing.T, r *throughputRecovery, now time.Time) int64 {
	t.Helper()
	if err := r.publish(now); err != nil {
		t.Fatal(err)
	}
	facts, _ := r.store.snapshot()
	var total int64
	for _, fact := range facts {
		if fact.OutputTokens != nil {
			total += *fact.OutputTokens
			if fact.Origin != "session_replay" || fact.Coverage != liveTokenRateCoveragePartial {
				t.Fatal("replay lost provenance/floor", fact)
			}
		}
	}
	return total
}
func TestThroughputRecoveryOfflineCountersNoStartupSpike(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 10, 0, 0, time.UTC)
	body := replayUsageRecord(at, 1000) + replayUsageRecord(at.Add(2*time.Minute), 1120) + replayUsageRecord(at.Add(2*time.Minute), 1120) + replayUsageRecord(at.Add(3*time.Minute), 5) + replayUsageRecord(at.Add(4*time.Minute), 65)
	r, src, now := throughputReplayFixture(t, "codex", body)
	prepareReplayFixture(t, r, src, now)
	if total := replayTotal(t, r, now); total != 180 {
		t.Fatal("baseline/reset/duplicate counted", total)
	}
	facts, _ := r.store.snapshot()
	if len(facts) != 3 {
		t.Fatal(facts)
	}
	for _, fact := range facts {
		if *fact.OutputTokens != 60 || fact.At >= now.Add(-10*time.Minute).Format(time.RFC3339) {
			t.Fatal("timestamp moved to startup", fact)
		}
	}
	records := r.store.loadedRecordCount
	prepareReplayFixture(t, r, src, now)
	replayTotal(t, r, now)
	if r.store.loadedRecordCount != records {
		t.Fatal("idempotent replay wrote duplicates")
	}
}
func TestThroughputRecoveryMessageDedupAndRestart(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 10, 12, 0, time.UTC)
	record := func(id string, n int64) string {
		b, _ := json.Marshal(map[string]any{"timestamp": at.Format(time.RFC3339), "type": "assistant", "sessionId": "s", "message": map[string]any{"id": id, "usage": map[string]int64{"output_tokens": n}}})
		return string(b) + "\n"
	}
	body := record("m", 20)
	for i := 0; i < 300; i++ {
		body += record("m", 20)
	}
	body += record("m", 10) + record("m", 25)
	r, src, now := throughputReplayFixture(t, "claude", body)
	more, err := r.prepareSource(context.Background(), src, now)
	if err != nil || !more {
		t.Fatal(more, err)
	}
	indexPath := r.db.Path()
	r.db.Close()
	reopened, err := openThroughputRecovery(filepath.Join(filepath.Dir(filepath.Dir(indexPath)), "history.jsonl"), r.store, r.adapters)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.db.Close()
	prepareReplayFixture(t, reopened, src, now)
	if total := replayTotal(t, reopened, now); total != 25 {
		t.Fatal("restart lost message maximum", total)
	}
}
func TestThroughputRecoveryMissingTimeAndPartialRecord(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 10, 0, 0, time.UTC)
	body := replayUsageRecord(at, 10)
	tail := replayUsageRecord(at.Add(time.Minute), 70)
	r, src, now := throughputReplayFixture(t, "codex", body+tail[:len(tail)-1])
	prepareReplayFixture(t, r, src, now)
	if total := replayTotal(t, r, now); total != 0 {
		t.Fatal("half record counted", total)
	}
	f, _ := os.OpenFile(src.Path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString("\n" + `{"type":"assistant","message":{"id":"missing","usage":{"output_tokens":999}}}` + "\n")
	f.Close()
	prepareReplayFixture(t, r, src, now)
	if total := replayTotal(t, r, now); total != 60 {
		t.Fatal(total)
	}
	facts, _ := r.store.snapshot()
	if len(facts) != 1 || facts[0].At != at.Add(time.Minute).Format(time.RFC3339) {
		t.Fatal("missing time became now", facts)
	}
}
func TestThroughputRecoveryDoesNotAddOnlineOverlap(t *testing.T) {
	at := time.Date(2026, 10, 1, 12, 10, 0, 0, time.UTC)
	r, src, now := throughputReplayFixture(t, "codex", replayUsageRecord(at, 10)+replayUsageRecord(at.Add(2*time.Minute), 130))
	online := int64(9)
	onlineFact := ThroughputMinuteFact{At: at.Add(time.Minute).Format(time.RFC3339), State: liveTokenRateStateLive, OutputTokens: &online, Projects: []ThroughputMinuteProjectFact{{Project: liveTokenRateUnassignedProject, OutputTokens: online}}}
	if err := r.store.appendMinute(onlineFact, now); err != nil {
		t.Fatal(err)
	}
	if err := r.store.appendMinute(ThroughputMinuteFact{At: at.Add(2 * time.Minute).Format(time.RFC3339), State: liveTokenRateStateNoData}, now); err != nil {
		t.Fatal(err)
	}
	prepareReplayFixture(t, r, src, now)
	if err := r.publish(now); err != nil {
		t.Fatal(err)
	}
	facts, _ := r.store.snapshot()
	if len(facts) != 2 || *facts[0].OutputTokens != 9 || facts[0].Origin != "" || *facts[1].OutputTokens != 60 || facts[1].Origin != "session_replay" {
		t.Fatal("overlap or missing-slot repair failed", facts)
	}
	reloaded, err := loadThroughputHistoryStore(filepath.Join(filepath.Dir(src.Path), "history.jsonl"), now)
	if err != nil {
		t.Fatal(err)
	}
	reloadedFacts, _ := reloaded.snapshot()
	if !reflect.DeepEqual(facts, reloadedFacts) {
		t.Fatal("restart history changed", facts, reloadedFacts)
	}
}
func TestOutputUsageReplayEquivalence(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	start := now.Add(-3 * time.Minute).Add(350 * time.Millisecond)
	end := now.Add(-time.Minute).Add(-220 * time.Millisecond)
	event := outputUsageEvent(liveTokenRateObservation{At: end, OutputTokens: 197, Cumulative: true}, 20, start, true, "s")
	buckets := liveTokenRateBucketAccumulator{}
	buckets.add(event, now)
	liveMinutes := map[int64]int64{}
	for _, bucket := range buckets.events() {
		liveMinutes[bucket.Start.Truncate(time.Minute).Unix()] += bucket.Tokens
	}
	replayMinutes := map[int64]int64{}
	partitionOutputUsage(event, now.Add(-liveTokenRateWindow), now.Add(liveTokenRateFutureSkew), time.Minute, func(at time.Time, tokens int64) { replayMinutes[at.Unix()] += tokens })
	if !reflect.DeepEqual(liveMinutes, replayMinutes) {
		t.Fatal("second and minute partitions differ", liveMinutes, replayMinutes)
	}
	// Clipping at an arbitrary boundary retains endpoint differences too.
	cutoff := start.Add(23*time.Second + 710*time.Millisecond)
	seconds, minutes := int64(0), int64(0)
	partitionOutputUsage(event, cutoff, end, time.Second, func(_ time.Time, n int64) { seconds += n })
	partitionOutputUsage(event, cutoff, end, time.Minute, func(_ time.Time, n int64) { minutes += n })
	if seconds != minutes {
		t.Fatal("clipped partition changed tokens", seconds, minutes)
	}
}
