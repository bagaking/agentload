package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestThroughputHistoryPersistsZeroAndMissingMinuteFacts(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	historyPath := filepath.Join(t.TempDir(), "history.jsonl")
	store, err := loadThroughputHistoryStore(historyPath, now)
	if err != nil {
		t.Fatalf("load throughput history: %v", err)
	}
	zero := int64(0)
	if err := store.appendMinute(ThroughputMinuteFact{
		At: now.Add(-time.Minute).Format(time.RFC3339), State: liveTokenRateStateZero,
		OutputTokens: &zero, Projects: []ThroughputMinuteProjectFact{},
	}); err != nil {
		t.Fatalf("append zero minute: %v", err)
	}
	if err := store.appendMinute(ThroughputMinuteFact{
		At: now.Format(time.RFC3339), State: liveTokenRateStateUnavailable,
		UnavailableReason: liveTokenRateUnavailableWatchIncomplete,
	}); err != nil {
		t.Fatalf("append missing minute: %v", err)
	}

	reloaded, err := loadThroughputHistoryStore(historyPath, now)
	if err != nil {
		t.Fatalf("reload throughput history: %v", err)
	}
	minutes, legacy := reloaded.snapshot()
	if len(minutes) != 2 || len(legacy) != 0 {
		t.Fatalf("unexpected throughput history: minutes=%+v legacy=%+v", minutes, legacy)
	}
	if minutes[0].OutputTokens == nil || *minutes[0].OutputTokens != 0 || minutes[0].Projects == nil || len(minutes[0].Projects) != 0 {
		t.Fatalf("measured zero did not round-trip exactly: %+v", minutes[0])
	}
	if minutes[1].OutputTokens != nil || minutes[1].Projects != nil || minutes[1].UnavailableReason != liveTokenRateUnavailableWatchIncomplete {
		t.Fatalf("missing minute became measured zero: %+v", minutes[1])
	}
	metadata := reloaded.snapshotMetadata()
	if metadata == nil || metadata.StorePath != throughputHistoryPath(historyPath) || metadata.MinuteFactCount != 2 || metadata.LegacyFactCount != 0 {
		t.Fatalf("unexpected throughput history metadata: %+v", metadata)
	}
}

func TestLiveTokenRateSamplerPersistsOnlyFullyCoveredMinuteFacts(t *testing.T) {
	start := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := &throughputHistoryStore{path: filepath.Join(t.TempDir(), "throughput.jsonl")}
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{t.TempDir()}})
	sampler.bindThroughputHistory(store)
	sampler.pollMu.Lock()
	sampler.coverageStart = start.Add(10 * time.Second)
	sampler.lastMinuteEnd = start
	sampler.initialized = true
	sampler.latestSignal = start.Add(80 * time.Second)
	sampler.latestEvent = start.Add(80 * time.Second)
	sampler.sessionProjects = map[string]string{"session-a": "alpha"}
	sampler.buckets = []liveTokenRateEvent{{
		Start: start.Add(70 * time.Second), End: start.Add(80 * time.Second),
		At: start.Add(80 * time.Second), Tokens: 120, Session: "session-a",
	}}
	sampler.flushCompletedMinutesLocked(start.Add(2 * time.Minute))
	sampler.pollMu.Unlock()

	minutes, _ := store.snapshot()
	if len(minutes) != 1 {
		t.Fatalf("persisted %d minutes, want only the fully covered minute: %+v", len(minutes), minutes)
	}
	fact := minutes[0]
	if fact.At != start.Add(2*time.Minute).Format(time.RFC3339) || fact.OutputTokens == nil || *fact.OutputTokens != 120 {
		t.Fatalf("unexpected closed-minute fact: %+v", fact)
	}
	if len(fact.Projects) != 1 || fact.Projects[0].Project != "alpha" || fact.Projects[0].OutputTokens != 120 || len(fact.SessionHashes) != 1 {
		t.Fatalf("minute attribution was not exact: %+v", fact)
	}
}

func TestLegacyThroughputMigrationIsBatchedIdempotentAndClearsOldField(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	historyPath := filepath.Join(t.TempDir(), "history.jsonl")
	samples := make([]HistorySample, 0, throughputLegacyMigrationBatch+4)
	for index := 0; index < cap(samples); index++ {
		rate := float64(index) / 10
		window := 180
		if index%2 == 0 {
			window = 300
		}
		samples = append(samples, HistorySample{
			At: now.Add(-time.Duration(index) * time.Minute).Format(time.RFC3339),
			OutputTokenThroughput: &HistoryOutputTokenThroughput{
				State: liveTokenRateStateLive, WindowSeconds: window,
				OutputTokensPerSecond: &rate,
				Projects:              []LiveTokenRateProjectSample{{Project: "alpha", OutputTokensPerSecond: rate}},
			},
		})
	}
	if err := rewriteHistorySampleFile(historyPath, samples); err != nil {
		t.Fatalf("seed history: %v", err)
	}
	history, err := loadLocalHistoryState(historyPath, now)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	store, err := loadThroughputHistoryStore(historyPath, now)
	if err != nil {
		t.Fatalf("load throughput history: %v", err)
	}

	if err := migrateLegacyThroughputHistory(&history, store); err != nil {
		t.Fatalf("migrate throughput: %v", err)
	}
	if err := migrateLegacyThroughputHistory(&history, store); err != nil {
		t.Fatalf("repeat throughput migration: %v", err)
	}
	_, legacy := store.snapshot()
	if len(legacy) != len(samples) {
		t.Fatalf("legacy migration count = %d, want %d", len(legacy), len(samples))
	}
	for _, sample := range history.samples {
		if sample.OutputTokenThroughput != nil {
			t.Fatalf("old throughput field remained in memory: %+v", sample.OutputTokenThroughput)
		}
	}
	raw, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("read migrated history: %v", err)
	}
	if strings.Contains(string(raw), "output_token_throughput") {
		t.Fatalf("old throughput field remained on disk")
	}
	reloaded, err := loadThroughputHistoryStore(historyPath, now)
	if err != nil {
		t.Fatalf("reload throughput history: %v", err)
	}
	_, reloadedLegacy := reloaded.snapshot()
	if len(reloadedLegacy) != len(samples) {
		t.Fatalf("legacy dedup did not survive reload: got %d want %d", len(reloadedLegacy), len(samples))
	}
}

func TestLegacyThroughputMigrationRetriesAfterHistoryRewriteFailure(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	badHistoryPath := filepath.Join(root, "history-dir")
	if err := os.Mkdir(badHistoryPath, 0o755); err != nil {
		t.Fatal(err)
	}
	rate := 2.5
	history := localHistoryState{
		path: badHistoryPath,
		samples: []HistorySample{{
			At: now.Format(time.RFC3339),
			OutputTokenThroughput: &HistoryOutputTokenThroughput{
				State: liveTokenRateStateLive, WindowSeconds: 300,
				OutputTokensPerSecond: &rate,
				Projects:              []LiveTokenRateProjectSample{{Project: "alpha", OutputTokensPerSecond: rate}},
			},
		}},
	}
	store := &throughputHistoryStore{path: filepath.Join(root, "throughput.jsonl")}
	if err := migrateLegacyThroughputHistory(&history, store); err == nil {
		t.Fatal("expected main-history rewrite failure")
	}
	if history.samples[0].OutputTokenThroughput == nil {
		t.Fatal("failed migration cleared the in-memory source")
	}

	history.path = filepath.Join(root, "history.jsonl")
	if err := migrateLegacyThroughputHistory(&history, store); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	_, legacy := store.snapshot()
	if len(legacy) != 1 || history.samples[0].OutputTokenThroughput != nil {
		t.Fatalf("retry was not idempotent: history=%+v legacy=%+v", history.samples, legacy)
	}
}

func TestLegacyThroughputHistoryPreservesDistinctSubsecondTimestamps(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	store := &throughputHistoryStore{path: filepath.Join(t.TempDir(), "throughput.jsonl")}
	rate := 1.0
	facts := []LegacyThroughputFact{
		{At: now.Add(100 * time.Millisecond).Format(time.RFC3339Nano), State: liveTokenRateStateLive, WindowSeconds: 300, OutputTokensPerSecond: &rate, Projects: []LiveTokenRateProjectSample{}},
		{At: now.Add(900 * time.Millisecond).Format(time.RFC3339Nano), State: liveTokenRateStateLive, WindowSeconds: 300, OutputTokensPerSecond: &rate, Projects: []LiveTokenRateProjectSample{}},
	}
	if err := store.appendLegacyBatch(facts); err != nil {
		t.Fatalf("append legacy facts: %v", err)
	}
	_, legacy := store.snapshot()
	if len(legacy) != 2 || legacy[0].At == legacy[1].At {
		t.Fatalf("subsecond legacy facts were collapsed: %+v", legacy)
	}
}

func TestThroughputHistoryLoadCompactsExpiredFacts(t *testing.T) {
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	historyPath := filepath.Join(t.TempDir(), "history.jsonl")
	path := throughputHistoryPath(historyPath)
	records := make([]throughputHistoryRecord, 0, 4)
	for index := 0; index < 3; index++ {
		minute := throughputTestMinute(now.Add(-historyRetentionWindow-time.Duration(index+1)*time.Minute), 60)
		records = append(records, throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindMinute, Minute: &minute})
	}
	current := throughputTestMinute(now, 120)
	records = append(records, throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindMinute, Minute: &current})
	if err := appendThroughputHistoryRecords(path, records); err != nil {
		t.Fatalf("seed throughput history: %v", err)
	}

	store, err := loadThroughputHistoryStore(historyPath, now)
	if err != nil {
		t.Fatalf("load throughput history: %v", err)
	}
	minutes, _ := store.snapshot()
	if len(minutes) != 1 || minutes[0].At != current.At || store.droppedRecordCount != 3 {
		t.Fatalf("expired throughput facts were not pruned: minutes=%+v dropped=%d", minutes, store.droppedRecordCount)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compacted throughput history: %v", err)
	}
	if lines := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; lines != 1 {
		t.Fatalf("compacted throughput history has %d lines, want 1", lines)
	}
}

func throughputTestMinute(at time.Time, tokens int64) ThroughputMinuteFact {
	return ThroughputMinuteFact{
		At: at.Format(time.RFC3339), State: liveTokenRateStateLive, OutputTokens: &tokens,
		SessionHashes: []string{"session"},
		Projects:      []ThroughputMinuteProjectFact{{Project: "alpha", OutputTokens: tokens, SessionHashes: []string{"session"}}},
	}
}
