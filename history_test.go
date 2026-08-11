package main

import (
	"encoding/json"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentload/internal/historyfile"
)

func TestLocalHistoryStoreAppendsAndReloadsSamples(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("loadLocalHistoryState: %v", err)
	}

	sample := makeHistorySample(now.Add(-2*time.Hour),
		CurrentMetrics{PIDConcurrency: 4, SessionConcurrency: 3, ActiveBurstConcurrency: 2},
		SnapshotSummary{
			ActiveSessions:     3,
			IdleSessions:       1,
			MappedProcesses:    3,
			UnmappedProcesses:  1,
			ProjectCount:       2,
			HotProjectCount:    1,
			MappingCoveragePct: 75,
		},
		[]HistoryProjectSnapshot{
			{Project: "agentload", SessionCount: 2, ActiveBurstCount: 2, ProcessCount: 3, AttentionSharePct: 66.6},
			{Project: "infra", SessionCount: 1, ActiveBurstCount: 0, ProcessCount: 1, AttentionSharePct: 33.4},
		},
	)
	sample.CoordinationRisk = HistoryCoordinationRisk{
		Posture:                      "watch",
		TopProject:                   "agentload",
		TopProjectAttentionSharePct:  66.6,
		CandidateWorkitemCount:       2,
		CandidateWorkitemCoveragePct: 50,
		LoadPeakValue:                3,
		LoadPeakSource:               "historic",
		LoadPeakAt:                   now.Add(-3 * time.Hour).Format(time.RFC3339),
	}
	rate := 2.5
	sample.OutputTokenThroughput = &HistoryOutputTokenThroughput{
		OutputTokensPerSecond: &rate,
		State:                 liveTokenRateStateLive,
		WindowSeconds:         300,
		ActiveSessions:        2,
		Projects: []LiveTokenRateProjectSample{
			{Project: "agentload", OutputTokensPerSecond: 1.5, ActiveSessions: 1},
			{Project: liveTokenRateUnassignedProject, OutputTokensPerSecond: 1, ActiveSessions: 1},
		},
	}

	if err := state.recordSample(sample); err != nil {
		t.Fatalf("recordSample: %v", err)
	}

	reloaded, err := loadLocalHistoryState(path, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("reload history: %v", err)
	}
	if reloaded.loadedSampleCount != 1 {
		t.Fatalf("expected 1 loaded sample, got %d", reloaded.loadedSampleCount)
	}
	if len(reloaded.samples) != 1 {
		t.Fatalf("expected 1 retained sample, got %d", len(reloaded.samples))
	}
	if reloaded.samples[0].At != sample.At {
		t.Fatalf("expected retained sample at %s, got %s", sample.At, reloaded.samples[0].At)
	}
	if reloaded.samples[0].CoordinationRisk.TopProject != "agentload" {
		t.Fatalf("expected top project round-trip, got %+v", reloaded.samples[0].CoordinationRisk)
	}
	throughput := reloaded.samples[0].OutputTokenThroughput
	if throughput == nil || throughput.OutputTokensPerSecond == nil || *throughput.OutputTokensPerSecond != rate || throughput.State != liveTokenRateStateLive || throughput.WindowSeconds != 300 || throughput.ActiveSessions != 2 {
		t.Fatalf("expected output throughput round-trip, got %+v", throughput)
	}
	if len(throughput.Projects) != 2 || throughput.Projects[0].Project != "agentload" || throughput.Projects[0].OutputTokensPerSecond != 1.5 {
		t.Fatalf("expected project throughput partition round-trip, got %+v", throughput.Projects)
	}
	if got := reloaded.snapshotMetadata().StorePath; got != path {
		t.Fatalf("expected store path %q, got %q", path, got)
	}
}

func TestHistoryThroughputPersistsMeasuredEmptyProjectPartition(t *testing.T) {
	rate := 0.0
	raw, err := json.Marshal(HistorySample{
		At: "2026-06-28T12:00:00Z",
		OutputTokenThroughput: &HistoryOutputTokenThroughput{
			OutputTokensPerSecond: &rate,
			State:                 liveTokenRateStateZero,
			WindowSeconds:         300,
			Projects:              []LiveTokenRateProjectSample{},
		},
	})
	if err != nil {
		t.Fatalf("marshal history sample: %v", err)
	}
	if !strings.Contains(string(raw), `"projects":[]`) {
		t.Fatalf("measured empty project partition was not persisted: %s", raw)
	}
}

func TestLocalHistoryStoreRetainsDistinctSameSecondSamples(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	first := now.Add(125 * time.Millisecond)
	second := now.Add(875 * time.Millisecond)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("loadLocalHistoryState: %v", err)
	}

	if err := state.recordSample(historySampleFromSnapshot(Snapshot{
		GeneratedAt: first.Format(time.RFC3339Nano),
		Current: CurrentMetrics{
			PIDConcurrency:         2,
			SessionConcurrency:     1,
			ActiveBurstConcurrency: 1,
		},
		Summary: SnapshotSummary{
			MappedProcesses:    2,
			UnmappedProcesses:  0,
			MappingCoveragePct: 100,
		},
		CoordinationRisk: CoordinationRiskSnapshot{
			Posture: "steady",
		},
	})); err != nil {
		t.Fatalf("record first same-second sample: %v", err)
	}
	if err := state.recordSample(historySampleFromSnapshot(Snapshot{
		GeneratedAt: second.Format(time.RFC3339Nano),
		Current: CurrentMetrics{
			PIDConcurrency:         5,
			SessionConcurrency:     3,
			ActiveBurstConcurrency: 2,
		},
		Summary: SnapshotSummary{
			MappedProcesses:    4,
			UnmappedProcesses:  1,
			MappingCoveragePct: 80,
		},
		CoordinationRisk: CoordinationRiskSnapshot{
			Posture: "watch",
		},
	})); err != nil {
		t.Fatalf("record second same-second sample: %v", err)
	}

	reloaded, err := loadLocalHistoryState(path, now.Add(2*time.Second))
	if err != nil {
		t.Fatalf("reload history: %v", err)
	}

	if reloaded.loadedSampleCount != 2 {
		t.Fatalf("expected 2 loaded samples, got %d", reloaded.loadedSampleCount)
	}
	if len(reloaded.samples) != 2 {
		t.Fatalf("expected 2 retained samples, got %d", len(reloaded.samples))
	}
	if got := persistedTime(reloaded.samples[0]); !got.Equal(first) {
		t.Fatalf("expected first retained timestamp %s, got %s", first.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
	}
	if got := persistedTime(reloaded.samples[1]); !got.Equal(second) {
		t.Fatalf("expected second retained timestamp %s, got %s", second.Format(time.RFC3339Nano), got.Format(time.RFC3339Nano))
	}
	if reloaded.samples[0].At == reloaded.samples[1].At {
		t.Fatalf("expected same-second retained samples to keep distinct timestamps, got %+v", reloaded.samples)
	}

	metadata := reloaded.snapshotMetadata()
	if metadata.LoadedSampleCount != 2 || metadata.RetainedSampleCount != 2 {
		t.Fatalf("expected truthful same-second metadata, got %+v", metadata)
	}
	if metadata.FirstSampleAt != reloaded.samples[0].At || metadata.LastSampleAt != reloaded.samples[1].At {
		t.Fatalf("expected metadata bounds to follow retained samples, got %+v", metadata)
	}

	replayed := filterHistorySamplesByRange(reloaded.samples, now, now.Add(time.Second))
	if len(replayed) != 2 {
		t.Fatalf("expected replay helpers to retain both same-second samples, got %d", len(replayed))
	}
}

func TestLocalHistoryStoreIgnoresCorruptAndPartialLines(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	valid := makeHistorySample(now,
		CurrentMetrics{PIDConcurrency: 2, SessionConcurrency: 1, ActiveBurstConcurrency: 1},
		SnapshotSummary{MappedProcesses: 2, MappingCoveragePct: 100},
		nil,
	)
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatalf("marshal valid sample: %v", err)
	}
	content := string(raw) + "\n" + "{\n" + `{"at":"not-a-timestamp","current":{"pid_concurrency":1}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write history fixture: %v", err)
	}

	reloaded, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("reload history: %v", err)
	}
	if reloaded.loadedSampleCount != 1 {
		t.Fatalf("expected 1 valid sample, got %d", reloaded.loadedSampleCount)
	}
	if reloaded.corruptLineCount != 2 {
		t.Fatalf("expected 2 corrupt lines, got %d", reloaded.corruptLineCount)
	}
	if len(reloaded.samples) != 1 {
		t.Fatalf("expected 1 retained sample, got %d", len(reloaded.samples))
	}
}

func TestLoadLocalHistoryStatePreservesConfiguredPathOnReadError(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	if err := os.WriteFile(path, []byte(`{"at":"2026-06-28T12:00:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write history fixture: %v", err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod unreadable: %v", err)
	}
	defer os.Chmod(path, 0o644)

	state, err := loadLocalHistoryState(path, now)
	if err == nil {
		t.Fatalf("expected history read error")
	}
	if state.path != path {
		t.Fatalf("expected load error to preserve configured path %q, got %q", path, state.path)
	}
	if got := state.snapshotMetadata().StorePath; got != path {
		t.Fatalf("expected metadata store path %q after load error, got %q", path, got)
	}

	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("restore readable history file: %v", err)
	}
	if err := state.recordSample(makeHistorySample(now,
		CurrentMetrics{PIDConcurrency: 1, SessionConcurrency: 1, ActiveBurstConcurrency: 1},
		SnapshotSummary{MappedProcesses: 1, MappingCoveragePct: 100},
		nil,
	)); err != nil {
		t.Fatalf("recordSample after load error: %v", err)
	}
	reloaded, err := loadLocalHistoryState(path, now.Add(time.Second))
	if err != nil {
		t.Fatalf("reload history after recovering permissions: %v", err)
	}
	if reloaded.snapshotMetadata().StorePath != path {
		t.Fatalf("expected recovered history store path %q, got %+v", path, reloaded.snapshotMetadata())
	}
}

func TestLocalHistoryStoreRetentionDropsOldSamplesWithoutBackfill(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	if err := state.recordSample(makeHistorySample(now.Add(-40*24*time.Hour),
		CurrentMetrics{PIDConcurrency: 9, SessionConcurrency: 8, ActiveBurstConcurrency: 7},
		SnapshotSummary{MappedProcesses: 8, UnmappedProcesses: 1, MappingCoveragePct: 88.8},
		nil,
	)); err != nil {
		t.Fatalf("record old sample: %v", err)
	}
	if err := state.recordSample(makeHistorySample(now.Add(-2*time.Hour),
		CurrentMetrics{PIDConcurrency: 3, SessionConcurrency: 2, ActiveBurstConcurrency: 1},
		SnapshotSummary{MappedProcesses: 2, UnmappedProcesses: 1, MappingCoveragePct: 66.6},
		nil,
	)); err != nil {
		t.Fatalf("record recent sample: %v", err)
	}

	reloaded, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("reload history: %v", err)
	}
	if reloaded.loadedSampleCount != 2 {
		t.Fatalf("expected 2 valid samples, got %d", reloaded.loadedSampleCount)
	}
	if reloaded.droppedSampleCount != 1 {
		t.Fatalf("expected 1 dropped sample, got %d", reloaded.droppedSampleCount)
	}
	if len(reloaded.samples) != 1 {
		t.Fatalf("expected 1 retained sample, got %d", len(reloaded.samples))
	}

	thirtyDay := requireTrendWindow(t, buildRealtimeTrendWindows(reloaded.trendPoints(), now), "30D")
	if len(thirtyDay.Points) != 1 {
		t.Fatalf("expected only the retained runtime sample to appear, got %d points", len(thirtyDay.Points))
	}
	if thirtyDay.Points[0].At != now.Add(-2*time.Hour).Format(time.RFC3339) {
		t.Fatalf("expected retained point at %s, got %+v", now.Add(-2*time.Hour).Format(time.RFC3339), thirtyDay.Points[0])
	}
}

func TestLoadLocalHistoryStateCompactsFileWithMaterialOverhead(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	var content []byte
	for i := 0; i < 10; i++ {
		raw, err := json.Marshal(makeHistorySample(now.Add(-40*24*time.Hour).Add(time.Duration(i)*time.Minute),
			CurrentMetrics{PIDConcurrency: i},
			SnapshotSummary{MappedProcesses: i},
			nil,
		))
		if err != nil {
			t.Fatalf("marshal stale sample: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	retained := []HistorySample{
		makeHistorySample(now.Add(-2*time.Hour), CurrentMetrics{PIDConcurrency: 3}, SnapshotSummary{MappedProcesses: 2}, nil),
		makeHistorySample(now.Add(-time.Hour), CurrentMetrics{PIDConcurrency: 4}, SnapshotSummary{MappedProcesses: 3}, nil),
	}
	for _, sample := range retained {
		raw, err := json.Marshal(sample)
		if err != nil {
			t.Fatalf("marshal retained sample: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write history fixture: %v", err)
	}

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("loadLocalHistoryState: %v", err)
	}
	if len(state.samples) != 2 || state.droppedSampleCount != 10 {
		t.Fatalf("expected 2 retained and 10 dropped samples, got %d retained %d dropped", len(state.samples), state.droppedSampleCount)
	}

	rewritten, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read compacted history: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(rewritten)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected compacted file with 2 lines, got %d: %q", len(lines), string(rewritten))
	}
	for i, line := range lines {
		var sample HistorySample
		if err := json.Unmarshal([]byte(line), &sample); err != nil {
			t.Fatalf("decode compacted line %d: %v", i, err)
		}
		if sample.At != retained[i].At {
			t.Fatalf("expected compacted line %d at %s, got %s", i, retained[i].At, sample.At)
		}
	}

	reloaded, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("reload compacted history: %v", err)
	}
	if reloaded.loadedSampleCount != 2 || reloaded.droppedSampleCount != 0 || len(reloaded.samples) != 2 {
		t.Fatalf("expected clean reload after compaction, got %+v", reloaded.snapshotMetadata())
	}
}

func TestLoadLocalHistoryStateKeepsFileWithSmallOverhead(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	var content []byte
	appendSample := func(at time.Time) {
		raw, err := json.Marshal(makeHistorySample(at, CurrentMetrics{PIDConcurrency: 1}, SnapshotSummary{MappedProcesses: 1}, nil))
		if err != nil {
			t.Fatalf("marshal sample: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	appendSample(now.Add(-40 * 24 * time.Hour))
	for i := 0; i < 10; i++ {
		appendSample(now.Add(-time.Duration(i+1) * time.Hour))
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write history fixture: %v", err)
	}

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("loadLocalHistoryState: %v", err)
	}
	if len(state.samples) != 10 || state.droppedSampleCount != 1 {
		t.Fatalf("expected 10 retained and 1 dropped sample, got %d retained %d dropped", len(state.samples), state.droppedSampleCount)
	}

	kept, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read history file: %v", err)
	}
	if string(kept) != string(content) {
		t.Fatalf("expected small overhead to leave the file untouched")
	}
}

func TestHistoryFileNeedsCompactionThresholds(t *testing.T) {
	tests := []struct {
		name     string
		lines    int
		retained int
		want     bool
	}{
		{name: "clean file", lines: 10, retained: 10, want: false},
		{name: "small overhead stays", lines: 11, retained: 10, want: false},
		{name: "over quarter overhead compacts", lines: 13, retained: 10, want: true},
		{name: "excess line limit compacts", lines: 2501, retained: 2000, want: true},
		{name: "all lines stale compacts", lines: 5, retained: 0, want: true},
		{name: "empty file stays", lines: 0, retained: 0, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := historyFileNeedsCompaction(tt.lines, tt.retained); got != tt.want {
				t.Fatalf("historyFileNeedsCompaction(%d, %d) = %v, want %v", tt.lines, tt.retained, got, tt.want)
			}
		})
	}
}

func TestHistoryOperationsShareCrossProcessLock(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	lock, err := historyfile.Acquire(path)
	if err != nil {
		t.Fatalf("acquire history lock: %v", err)
	}

	appendDone := make(chan error, 1)
	go func() {
		appendDone <- appendHistorySampleFile(path, makeHistorySample(now, CurrentMetrics{PIDConcurrency: 1}, SnapshotSummary{}, nil))
	}()
	select {
	case err := <-appendDone:
		lock.Release()
		t.Fatalf("append bypassed held history lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	lock.Release()
	select {
	case err := <-appendDone:
		if err != nil {
			t.Fatalf("append after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("append did not resume after history lock release")
	}

	lock, err = historyfile.Acquire(path)
	if err != nil {
		t.Fatalf("reacquire history lock: %v", err)
	}
	loadDone := make(chan error, 1)
	go func() {
		_, err := loadLocalHistoryState(path, now)
		loadDone <- err
	}()
	select {
	case err := <-loadDone:
		lock.Release()
		t.Fatalf("load bypassed held history lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	lock.Release()
	select {
	case err := <-loadDone:
		if err != nil {
			t.Fatalf("load after lock release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("load did not resume after history lock release")
	}
}

func TestMergeRuntimeTrendsUsesLoadedHistoryAndCurrentSample(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("load history: %v", err)
	}
	persisted := makeHistorySample(now.Add(-6*time.Hour),
		CurrentMetrics{PIDConcurrency: 2, SessionConcurrency: 1, ActiveBurstConcurrency: 1},
		SnapshotSummary{MappedProcesses: 1, UnmappedProcesses: 1, MappingCoveragePct: 50},
		[]HistoryProjectSnapshot{{Project: "alpha", SessionCount: 1, ActiveBurstCount: 1, ProcessCount: 2, AttentionSharePct: 100}},
	)
	if err := state.recordSample(persisted); err != nil {
		t.Fatalf("record persisted sample: %v", err)
	}

	reloaded, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("reload history: %v", err)
	}
	app := &trayApp{
		logger:  log.New(io.Discard, "", 0),
		history: reloaded,
	}
	snapshot := Snapshot{
		GeneratedAt: now.Format(time.RFC3339),
		Current: CurrentMetrics{
			PIDConcurrency:         5,
			SessionConcurrency:     4,
			ActiveBurstConcurrency: 3,
		},
		Summary: SnapshotSummary{
			MappedProcesses:    4,
			UnmappedProcesses:  1,
			MappingCoveragePct: 80,
		},
		CoordinationRisk: CoordinationRiskSnapshot{
			Posture:            "watch",
			ActiveProjectCount: 1,
			TopProject:         "alpha",
		},
		ProjectFocus: []ProjectSnapshot{
			{Project: "alpha", SessionCount: 4, ActiveBurstCount: 3, ProcessCount: 5, AttentionSharePct: 100},
		},
		RuntimeProcesses: []ProcessRuntimeSummary{
			{Key: "codex", Tool: "codex", DisplayName: "Codex", PIDCount: 3},
		},
		HostAppProcesses: []HostAppProcessSummary{
			{Key: "cursor", Name: "Cursor", PIDCount: 5},
		},
	}

	snapshot = app.rememberSnapshot(snapshot)
	oneDay := requireTrendWindow(t, snapshot.RealtimeTrends, "1D")
	if len(oneDay.Points) != 2 {
		t.Fatalf("expected loaded sample plus current sample, got %d points", len(oneDay.Points))
	}
	requireTrendPoint(t, oneDay.Points, persistedTime(persisted))
	currentPoint := requireTrendPoint(t, oneDay.Points, now)
	if currentPoint.PIDConcurrency != 5 {
		t.Fatalf("expected current point pid 5, got %+v", currentPoint)
	}
	if len(currentPoint.RuntimeProcesses) != 1 || currentPoint.RuntimeProcesses[0].Tool != "codex" || currentPoint.RuntimeProcesses[0].PIDCount != 3 {
		t.Fatalf("expected current point runtime process breakdown, got %+v", currentPoint.RuntimeProcesses)
	}
	if len(currentPoint.HostAppProcesses) != 1 || currentPoint.HostAppProcesses[0].Name != "Cursor" || currentPoint.HostAppProcesses[0].PIDCount != 5 {
		t.Fatalf("expected current point host process breakdown, got %+v", currentPoint.HostAppProcesses)
	}
	throughputPoint := requireTrendPoint(t, requireTrendWindow(t, snapshot.ThroughputTrends, "1D").Points, now)
	if !throughputPoint.ThroughputSampled || throughputPoint.OutputTokenThroughputState != liveTokenRateStateUnavailable || throughputPoint.HasOutputTokensPerSecond {
		t.Fatalf("expected unavailable throughput evidence without a numeric rate, got %+v", throughputPoint)
	}
	if snapshot.History.LoadedSampleCount != 2 {
		t.Fatalf("expected loaded sample count 2 after current append, got %+v", snapshot.History)
	}
	if snapshot.History.RetainedSampleCount != 2 {
		t.Fatalf("expected retained sample count 2, got %+v", snapshot.History)
	}
	if snapshot.History.StorePath != path {
		t.Fatalf("expected history store path %q, got %q", path, snapshot.History.StorePath)
	}
}

func TestRefreshSlotIDDedupesSameTimeSlice(t *testing.T) {
	app := &trayApp{
		cfg:       Config{RefreshInterval: 30 * time.Second},
		refreshCh: make(chan struct{}, 1),
	}
	at := time.Date(2026, 6, 28, 12, 34, 41, 0, time.UTC)
	slot := app.refreshSlotID(at)
	if slot != "30s:2026-06-28T12:34:30Z" {
		t.Fatalf("unexpected slot id %q", slot)
	}

	first := app.requestRefreshForSlot(slot)
	second := app.requestRefreshForSlot(slot)
	if first != slot || second != slot {
		t.Fatalf("expected repeated requests to return same slot, got %q and %q", first, second)
	}
	if queued := len(app.refreshCh); queued != 1 {
		t.Fatalf("expected one queued refresh for duplicate slot, got %d", queued)
	}
	<-app.refreshCh
	claimed := app.claimRefreshSlot()
	if claimed != slot {
		t.Fatalf("expected claimed slot %q, got %q", slot, claimed)
	}
	if app.requestRefreshForSlot(slot) != slot {
		t.Fatalf("expected active duplicate request to return same slot")
	}
	if queued := len(app.refreshCh); queued != 0 {
		t.Fatalf("expected active duplicate not to queue, got %d", queued)
	}
	app.finishRefreshSlot(slot)
	if app.requestRefreshForSlot(slot) != slot {
		t.Fatalf("expected completed duplicate request to return same slot")
	}
	if queued := len(app.refreshCh); queued != 0 {
		t.Fatalf("expected completed duplicate not to queue, got %d", queued)
	}

	next := app.refreshSlotID(at.Add(31 * time.Second))
	if next == slot {
		t.Fatalf("expected next time slice to produce a new slot")
	}
	app.requestRefreshForSlot(next)
	if queued := len(app.refreshCh); queued != 1 {
		t.Fatalf("expected new slot to queue one refresh, got %d", queued)
	}
}

func TestRefreshSlotIDCanUseRequestedInterval(t *testing.T) {
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	at := time.Date(2026, 6, 28, 12, 34, 41, 0, time.UTC)
	if got := app.refreshSlotID(at); got != "300s:2026-06-28T12:30:00Z" {
		t.Fatalf("unexpected default slot id %q", got)
	}
	if got := app.refreshSlotIDForInterval(at, 30*time.Second); got != "30s:2026-06-28T12:34:30Z" {
		t.Fatalf("unexpected requested slot id %q", got)
	}
}

func TestReplayHelpersUseObservedSamplesOnly(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	samples := []HistorySample{
		makeHistorySample(now.Add(-48*time.Hour),
			CurrentMetrics{PIDConcurrency: 20, SessionConcurrency: 10, ActiveBurstConcurrency: 9},
			SnapshotSummary{MappedProcesses: 18, UnmappedProcesses: 2, MappingCoveragePct: 90},
			[]HistoryProjectSnapshot{{Project: "outside", SessionCount: 10, ActiveBurstCount: 9, ProcessCount: 20, AttentionSharePct: 100}},
		),
		makeHistorySample(now.Add(-5*time.Hour),
			CurrentMetrics{PIDConcurrency: 4, SessionConcurrency: 2, ActiveBurstConcurrency: 1},
			SnapshotSummary{MappedProcesses: 3, UnmappedProcesses: 1, MappingCoveragePct: 75},
			[]HistoryProjectSnapshot{
				{Project: "alpha", SessionCount: 2, ActiveBurstCount: 1, ProcessCount: 2, AttentionSharePct: 70},
				{Project: "beta", SessionCount: 1, ActiveBurstCount: 1, ProcessCount: 1, AttentionSharePct: 30},
			},
		),
		makeHistorySample(now.Add(-3*time.Hour),
			CurrentMetrics{PIDConcurrency: 6, SessionConcurrency: 4, ActiveBurstConcurrency: 3},
			SnapshotSummary{MappedProcesses: 5, UnmappedProcesses: 1, MappingCoveragePct: 83.3},
			[]HistoryProjectSnapshot{
				{Project: "alpha", SessionCount: 2, ActiveBurstCount: 1, ProcessCount: 2, AttentionSharePct: 40},
				{Project: "beta", SessionCount: 4, ActiveBurstCount: 3, ProcessCount: 4, AttentionSharePct: 60},
			},
		),
		makeHistorySample(now.Add(-time.Hour),
			CurrentMetrics{PIDConcurrency: 5, SessionConcurrency: 5, ActiveBurstConcurrency: 2},
			SnapshotSummary{MappedProcesses: 4, UnmappedProcesses: 1, MappingCoveragePct: 80},
			[]HistoryProjectSnapshot{
				{Project: "beta", SessionCount: 5, ActiveBurstCount: 2, ProcessCount: 4, AttentionSharePct: 100},
			},
		),
	}

	heatmaps := buildProjectHeatmapWindows(samples, now)
	oneDayHeatmap := requireProjectHeatmapWindow(t, heatmaps, "1D")
	if oneDayHeatmap.SampleWindowCount != 3 || oneDayHeatmap.SessionWindowCount != 14 {
		t.Fatalf("expected one-day heatmap to aggregate 3 windows and 14 session-windows, got %+v", oneDayHeatmap)
	}
	betaHeat := requireProjectHeatmapItem(t, oneDayHeatmap.Items, "beta")
	if betaHeat.WindowCount != 3 || betaHeat.SessionWindowCount != 10 || betaHeat.ProcessWindowCount != 9 {
		t.Fatalf("expected beta heatmap to count sampled windows and cumulative sessions/processes, got %+v", betaHeat)
	}
	if math.Abs(betaHeat.SharePct-71.4285714) > 0.01 {
		t.Fatalf("expected beta heatmap share about 71.43, got %+v", betaHeat)
	}
	alphaHeat := requireProjectHeatmapItem(t, oneDayHeatmap.Items, "alpha")
	if alphaHeat.WindowCount != 2 || alphaHeat.SessionWindowCount != 4 || alphaHeat.MaxSessionCount != 2 {
		t.Fatalf("expected alpha heatmap to exclude old sample and retain max session count, got %+v", alphaHeat)
	}
	if oneDayHeatmap.Items[0].Project != "beta" {
		t.Fatalf("expected heatmap items sorted by session-window investment, got %+v", oneDayHeatmap.Items)
	}
}

func makeHistorySample(at time.Time, current CurrentMetrics, summary SnapshotSummary, projects []HistoryProjectSnapshot) HistorySample {
	return HistorySample{
		At:      at.Format(time.RFC3339),
		Current: current,
		Summary: summary,
		CoordinationRisk: HistoryCoordinationRisk{
			Posture:                      "steady",
			ActiveProjectCount:           len(projects),
			RecentProjectCount:           len(projects),
			CandidateWorkitemCount:       len(projects),
			CandidateWorkitemCoveragePct: 100,
		},
		Projects: projects,
	}
}

func persistedTime(sample HistorySample) time.Time {
	t, ok := historySampleTime(sample)
	if !ok {
		return time.Time{}
	}
	return t
}

func requireProjectHeatmapWindow(t *testing.T, heatmaps ProjectHeatmapSet, label string) ProjectHeatmapWindow {
	t.Helper()
	for _, window := range heatmaps.Windows {
		if window.Range == label {
			return window
		}
	}
	t.Fatalf("missing project heatmap window %s", label)
	return ProjectHeatmapWindow{}
}

func requireProjectHeatmapItem(t *testing.T, items []ProjectHeatmapItem, project string) ProjectHeatmapItem {
	t.Helper()
	for _, item := range items {
		if item.Project == project {
			return item
		}
	}
	t.Fatalf("missing project heatmap item %s", project)
	return ProjectHeatmapItem{}
}
