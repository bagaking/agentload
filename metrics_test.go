package main

import (
	"encoding/json"
	"testing"
	"time"
)

func TestBuildBurstSpansSplitsOnIdleGap(t *testing.T) {
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	traces := map[string]*SessionTrace{
		"one": {
			Tool:      "codex",
			SessionID: "s1",
			Path:      "fixtures/s1.jsonl",
			EventTimes: []time.Time{
				base,
				base.Add(10 * time.Second),
				base.Add(20 * time.Second),
				base.Add(3 * time.Minute),
				base.Add(3*time.Minute + 10*time.Second),
			},
		},
	}

	spans := buildBurstSpans(traces, 90*time.Second, 15*time.Second)
	if len(spans) != 2 {
		t.Fatalf("expected 2 spans, got %d", len(spans))
	}
	if !spans[0].Start.Equal(base) || !spans[0].End.Equal(base.Add(20*time.Second)) {
		t.Fatalf("unexpected first span: %+v", spans[0])
	}
	if !spans[1].Start.Equal(base.Add(3*time.Minute)) || !spans[1].End.Equal(base.Add(3*time.Minute+15*time.Second)) {
		t.Fatalf("unexpected second span: %+v", spans[1])
	}
}

func TestPeakConcurrency(t *testing.T) {
	base := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	intervals := []Interval{
		{Start: base, End: base.Add(5 * time.Minute)},
		{Start: base.Add(2 * time.Minute), End: base.Add(7 * time.Minute)},
		{Start: base.Add(3 * time.Minute), End: base.Add(4 * time.Minute)},
	}

	peak := peakConcurrency(intervals, base.Add(-time.Minute), base.Add(10*time.Minute))
	if peak.Value != 3 {
		t.Fatalf("expected peak 3, got %d", peak.Value)
	}
	if peak.At != base.Add(3*time.Minute).Format(time.RFC3339) {
		t.Fatalf("unexpected peak time: %s", peak.At)
	}
}

func TestConcurrencySeries(t *testing.T) {
	base := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	intervals := []Interval{
		{Start: base.Add(-time.Minute), End: base.Add(2 * time.Minute)},
		{Start: base.Add(time.Minute), End: base.Add(4 * time.Minute)},
		{Start: base.Add(4 * time.Minute), End: base.Add(5 * time.Minute)},
	}
	points := []time.Time{
		base,
		base.Add(time.Minute),
		base.Add(2 * time.Minute),
		base.Add(4 * time.Minute),
		base.Add(5 * time.Minute),
	}

	got := concurrencySeries(intervals, points)
	want := []int{1, 2, 1, 1, 0}
	if len(got) != len(want) {
		t.Fatalf("expected %d points, got %d", len(want), len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("point %d: expected %d, got %d", i, want[i], got[i])
		}
	}
}

func TestBuildTranscriptTrendWindowsLeavesEmptyCoverageUnsampled(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	trends := buildTranscriptTrendWindows(&TranscriptData{}, now, 7*24*time.Hour)
	requireExactTrendRanges(t, trends)

	for _, window := range trends.Windows {
		if window.HistoryComplete {
			t.Fatalf("expected %s window to report partial coverage without transcript evidence", window.Range)
		}
		if window.SourceFrom != "" {
			t.Fatalf("expected %s window to omit source_from without transcript evidence, got %q", window.Range, window.SourceFrom)
		}
		if window.SourceLookbackHours != 0 {
			t.Fatalf("expected %s window to omit source_lookback_hours without transcript evidence, got %d", window.Range, window.SourceLookbackHours)
		}
		if countTrendPoints(window.Points, func(point TrendPoint) bool { return point.TranscriptSampled }) != 0 {
			t.Fatalf("expected %s window to leave every bucket unsampled", window.Range)
		}
		for _, point := range window.Points {
			if point.At == "" {
				t.Fatalf("expected %s window points to retain timestamps", window.Range)
			}
		}
	}
}

func TestBuildTranscriptTrendWindowsMarshalJSONOmitsUnsampledZeroMetrics(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	trends := buildTranscriptTrendWindows(&TranscriptData{}, now, 7*24*time.Hour)
	requireExactTrendRanges(t, trends)

	var decoded struct {
		Windows []struct {
			Range  string                       `json:"range"`
			Points []map[string]json.RawMessage `json:"points"`
		} `json:"windows"`
	}
	mustMarshalAndUnmarshalJSON(t, trends, &decoded)

	if len(decoded.Windows) != len(defaultTrendSpecs) {
		t.Fatalf("expected %d marshaled windows, got %d", len(defaultTrendSpecs), len(decoded.Windows))
	}
	for _, window := range decoded.Windows {
		if len(window.Points) == 0 {
			t.Fatalf("expected %s window to retain point buckets after JSON marshal", window.Range)
		}
		for _, point := range window.Points {
			requireOnlyTimestampForUnsampledTrendPoint(t, point)
		}
	}
}

func TestBuildTranscriptTrendWindowsUsesActualEvidenceStart(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	evidenceStart := now.Add(-6 * 24 * time.Hour)
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			"one": {
				Tool:      "codex",
				SessionID: "s1",
				Path:      "fixtures/s1.jsonl",
				EventTimes: []time.Time{
					evidenceStart,
					evidenceStart.Add(2 * time.Hour),
				},
				FirstEvent: evidenceStart,
				LastEvent:  evidenceStart.Add(2 * time.Hour),
			},
		},
		SessionSpans: []Interval{
			{Start: evidenceStart, End: evidenceStart.Add(30 * time.Minute)},
		},
		BurstSpans: []Interval{
			{Start: evidenceStart, End: evidenceStart.Add(15 * time.Minute)},
		},
	}

	trends := buildTranscriptTrendWindows(data, now, 7*24*time.Hour)
	requireExactTrendRanges(t, trends)

	sevenDay := requireTrendWindow(t, trends, "7D")
	if sevenDay.HistoryComplete {
		t.Fatalf("expected 7D window to stay partial when evidence starts after configured lookback start")
	}
	if sevenDay.SourceFrom != evidenceStart.Format(time.RFC3339) {
		t.Fatalf("expected 7D source_from %q, got %q", evidenceStart.Format(time.RFC3339), sevenDay.SourceFrom)
	}
	if sevenDay.SourceLookbackHours != 144 {
		t.Fatalf("expected 7D source_lookback_hours 144, got %d", sevenDay.SourceLookbackHours)
	}
	if requireTrendPoint(t, sevenDay.Points, evidenceStart.Add(-3*time.Hour)).TranscriptSampled {
		t.Fatalf("expected 7D bucket before actual evidence start to remain unsampled")
	}
	if !requireTrendPoint(t, sevenDay.Points, evidenceStart).TranscriptSampled {
		t.Fatalf("expected 7D bucket at actual evidence start to be sampled")
	}
}

func TestBuildTranscriptTrendWindowsMarksSampledMetricPresence(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	evidenceStart := now.Add(-24 * time.Hour)
	data := &TranscriptData{
		SessionSpans: []Interval{
			{Start: evidenceStart, End: evidenceStart.Add(30 * time.Minute)},
		},
		BurstSpans: []Interval{
			{Start: evidenceStart, End: evidenceStart.Add(15 * time.Minute)},
		},
	}

	trends := buildTranscriptTrendWindows(data, now, 7*24*time.Hour)
	oneDay := requireTrendWindow(t, trends, "1D")
	point := requireTrendPoint(t, oneDay.Points, evidenceStart)
	if !point.TranscriptSampled {
		t.Fatalf("expected point at evidence start to be sampled")
	}
	if !point.HasActiveBurst || !point.HasSessionConcurrency {
		t.Fatalf("expected sampled transcript point to mark metric presence: %+v", point)
	}
	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "transcript_sampled", true)
	requireJSONInt(t, decoded, "active_burst_concurrency", 1)
	requireJSONInt(t, decoded, "session_concurrency", 1)
}

func TestTrendPointMarshalJSONKeepsSampledHistoryZeroMetrics(t *testing.T) {
	point := TrendPoint{
		At:                     time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		ActiveBurstConcurrency: 0,
		HasActiveBurst:         true,
		SessionConcurrency:     0,
		HasSessionConcurrency:  true,
		TranscriptSampled:      true,
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "transcript_sampled", true)
	requireJSONInt(t, decoded, "active_burst_concurrency", 0)
	requireJSONInt(t, decoded, "session_concurrency", 0)
	requireTrendPointKeysAbsent(t, decoded,
		"pid_concurrency",
		"mapping_coverage_pct",
		"mapped_processes",
		"unmapped_processes",
		"runtime_sampled",
		"output_tokens_per_second",
		"output_token_throughput_state",
		"output_token_throughput_window_seconds",
		"output_token_active_sessions",
		"throughput_sampled",
	)
}

func TestTrendPointMarshalJSONKeepsSampledRuntimeZeroMetrics(t *testing.T) {
	point := TrendPoint{
		At:                    time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		PIDConcurrency:        0,
		HasPIDConcurrency:     true,
		MappingCoveragePct:    0,
		HasMappingCoveragePct: true,
		MappedProcesses:       0,
		HasMappedProcesses:    true,
		UnmappedProcesses:     0,
		HasUnmappedProcesses:  true,
		RuntimeSampled:        true,
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "runtime_sampled", true)
	requireJSONInt(t, decoded, "pid_concurrency", 0)
	requireJSONFloat64(t, decoded, "mapping_coverage_pct", 0)
	requireJSONInt(t, decoded, "mapped_processes", 0)
	requireJSONInt(t, decoded, "unmapped_processes", 0)
	requireTrendPointKeysAbsent(t, decoded,
		"output_tokens_per_second",
		"output_token_throughput_state",
		"output_token_throughput_window_seconds",
		"output_token_active_sessions",
		"throughput_sampled",
	)
	requireTrendPointKeysAbsent(t, decoded,
		"active_burst_concurrency",
		"session_concurrency",
		"transcript_sampled",
	)
}

func TestTrendPointMarshalJSONKeepsSampledThroughputZero(t *testing.T) {
	point := TrendPoint{
		At:                                 time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		OutputTokensPerSecond:              0,
		HasOutputTokensPerSecond:           true,
		OutputTokenThroughputState:         liveTokenRateStateZero,
		OutputTokenThroughputWindowSeconds: 300,
		OutputTokenActiveSessions:          0,
		HasOutputTokenActiveSessions:       true,
		ThroughputSampled:                  true,
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "throughput_sampled", true)
	requireJSONFloat64(t, decoded, "output_tokens_per_second", 0)
	requireJSONStringValue(t, decoded, "output_token_throughput_state", liveTokenRateStateZero)
	requireJSONInt(t, decoded, "output_token_throughput_window_seconds", 300)
	requireJSONInt(t, decoded, "output_token_active_sessions", 0)
}

func TestTrendPointMarshalJSONPreservesMissingThroughputRate(t *testing.T) {
	point := TrendPoint{
		At:                                 time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		OutputTokenThroughputState:         liveTokenRateStateStale,
		OutputTokenThroughputWindowSeconds: 300,
		ThroughputSampled:                  true,
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "throughput_sampled", true)
	requireJSONStringValue(t, decoded, "output_token_throughput_state", liveTokenRateStateStale)
	requireTrendPointKeysAbsent(t, decoded, "output_tokens_per_second", "output_token_active_sessions")
}

func TestTrendPointMarshalJSONOmitsMissingSampledHistoryMetric(t *testing.T) {
	point := TrendPoint{
		At:                     time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		ActiveBurstConcurrency: 0,
		HasActiveBurst:         true,
		SessionConcurrency:     99,
		TranscriptSampled:      true,
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "transcript_sampled", true)
	requireJSONInt(t, decoded, "active_burst_concurrency", 0)
	requireTrendPointKeysAbsent(t, decoded, "session_concurrency")
}

func TestTrendPointMarshalJSONOmitsMissingSampledRuntimeMetric(t *testing.T) {
	point := TrendPoint{
		At:                   time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		PIDConcurrency:       0,
		HasPIDConcurrency:    true,
		MappingCoveragePct:   100,
		MappedProcesses:      42,
		HasMappedProcesses:   true,
		UnmappedProcesses:    7,
		HasUnmappedProcesses: true,
		RuntimeSampled:       true,
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "runtime_sampled", true)
	requireJSONInt(t, decoded, "pid_concurrency", 0)
	requireJSONInt(t, decoded, "mapped_processes", 42)
	requireJSONInt(t, decoded, "unmapped_processes", 7)
	requireTrendPointKeysAbsent(t, decoded, "mapping_coverage_pct")
}

func TestTrendPointMarshalJSONCarriesRuntimeBreakdowns(t *testing.T) {
	point := TrendPoint{
		At:                   time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		PIDConcurrency:       5,
		HasPIDConcurrency:    true,
		MappedProcesses:      4,
		HasMappedProcesses:   true,
		UnmappedProcesses:    1,
		HasUnmappedProcesses: true,
		RuntimeSampled:       true,
		RuntimeProcesses: []ProcessRuntimeSummary{
			{Key: "codex", Tool: "codex", DisplayName: "Codex", PIDCount: 3},
			{Key: "claude", Tool: "claude", DisplayName: "Claude", PIDCount: 2},
		},
		HostAppProcesses: []HostAppProcessSummary{
			{Key: "cursor", Name: "Cursor", PIDCount: 4},
		},
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "runtime_sampled", true)
	requireJSONInt(t, decoded, "pid_concurrency", 5)
	var runtime []ProcessRuntimeSummary
	if err := json.Unmarshal(decoded["runtime_process_summary"], &runtime); err != nil {
		t.Fatalf("runtime_process_summary: %v", err)
	}
	if len(runtime) != 2 || runtime[0].Tool != "codex" || runtime[0].PIDCount != 3 {
		t.Fatalf("unexpected runtime_process_summary: %+v", runtime)
	}
	var hosts []HostAppProcessSummary
	if err := json.Unmarshal(decoded["host_app_process_summary"], &hosts); err != nil {
		t.Fatalf("host_app_process_summary: %v", err)
	}
	if len(hosts) != 1 || hosts[0].Name != "Cursor" || hosts[0].PIDCount != 4 {
		t.Fatalf("unexpected host_app_process_summary: %+v", hosts)
	}
}

func TestBuildTranscriptTrendWindowsUsesEvidenceAtConfiguredSourceStart(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	evidenceStart := now.Add(-7 * 24 * time.Hour)
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			"one": {
				Tool:      "codex",
				SessionID: "s1",
				Path:      "fixtures/s1.jsonl",
				EventTimes: []time.Time{
					evidenceStart,
					evidenceStart.Add(3 * time.Hour),
					now.Add(-2 * time.Hour),
				},
				FirstEvent: evidenceStart,
				LastEvent:  now.Add(-2 * time.Hour),
			},
		},
		SessionSpans: []Interval{
			{Start: evidenceStart, End: evidenceStart.Add(20 * time.Minute)},
			{Start: now.Add(-2 * time.Hour), End: now.Add(-90 * time.Minute)},
		},
		BurstSpans: []Interval{
			{Start: evidenceStart, End: evidenceStart.Add(15 * time.Minute)},
			{Start: now.Add(-2 * time.Hour), End: now.Add(-105 * time.Minute)},
		},
	}

	trends := buildTranscriptTrendWindows(data, now, 7*24*time.Hour)
	requireExactTrendRanges(t, trends)

	sevenDay := requireTrendWindow(t, trends, "7D")
	fifteenDay := requireTrendWindow(t, trends, "15D")
	thirtyDay := requireTrendWindow(t, trends, "30D")
	sourceFrom := evidenceStart.Format(time.RFC3339)

	if sevenDay.SourceFrom != sourceFrom {
		t.Fatalf("expected 7D source_from %q, got %q", sourceFrom, sevenDay.SourceFrom)
	}
	if sevenDay.SourceLookbackHours != 168 {
		t.Fatalf("expected 7D source_lookback_hours 168, got %d", sevenDay.SourceLookbackHours)
	}
	if !sevenDay.HistoryComplete {
		t.Fatalf("expected 7D window to be complete when evidence starts at configured source start")
	}
	if countTrendPoints(sevenDay.Points, func(point TrendPoint) bool { return point.TranscriptSampled }) != len(sevenDay.Points) {
		t.Fatalf("expected every 7D bucket to be transcript-sampled")
	}

	if fifteenDay.HistoryComplete {
		t.Fatalf("expected 15D window to report partial coverage")
	}
	if thirtyDay.HistoryComplete {
		t.Fatalf("expected 30D window to report partial coverage")
	}
	if countTrendPoints(fifteenDay.Points, func(point TrendPoint) bool { return point.TranscriptSampled }) == len(fifteenDay.Points) {
		t.Fatalf("expected 15D window to keep buckets before evidence start unsampled")
	}
	if countTrendPoints(thirtyDay.Points, func(point TrendPoint) bool { return point.TranscriptSampled }) == len(thirtyDay.Points) {
		t.Fatalf("expected 30D window to keep buckets before evidence start unsampled")
	}

	beforeFifteenDayStart := requireTrendPoint(t, fifteenDay.Points, evidenceStart.Add(-6*time.Hour))
	if beforeFifteenDayStart.TranscriptSampled {
		t.Fatalf("expected 15D bucket before evidence start to remain unsampled")
	}
	atFifteenDayStart := requireTrendPoint(t, fifteenDay.Points, evidenceStart)
	if !atFifteenDayStart.TranscriptSampled {
		t.Fatalf("expected 15D bucket at evidence start to be sampled")
	}
}

func TestBuildTranscriptTrendWindowsUsesOverlappingSpanCoverageStart(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	configuredSourceFrom := now.Add(-7 * 24 * time.Hour)
	data := &TranscriptData{
		SessionSpans: []Interval{
			{
				Start: configuredSourceFrom.Add(-2 * time.Hour),
				End:   configuredSourceFrom.Add(90 * time.Minute),
			},
		},
	}

	trends := buildTranscriptTrendWindows(data, now, 7*24*time.Hour)
	sevenDay := requireTrendWindow(t, trends, "7D")
	if !sevenDay.HistoryComplete {
		t.Fatalf("expected overlapping session span to make 7D history complete")
	}
	if sevenDay.SourceFrom != configuredSourceFrom.Format(time.RFC3339) {
		t.Fatalf("expected overlapping span to clamp source_from to configured lookback start, got %q", sevenDay.SourceFrom)
	}
	if !requireTrendPoint(t, sevenDay.Points, configuredSourceFrom).TranscriptSampled {
		t.Fatalf("expected 7D bucket at configured lookback start to be sampled when span overlaps it")
	}
}

func TestBuildTranscriptTrendWindowsUsesOverlappingBurstSpanCoverageStart(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	configuredSourceFrom := now.Add(-7 * 24 * time.Hour)
	data := &TranscriptData{
		BurstSpans: []Interval{
			{
				Start: configuredSourceFrom.Add(-time.Hour),
				End:   configuredSourceFrom.Add(45 * time.Minute),
			},
		},
	}

	trends := buildTranscriptTrendWindows(data, now, 7*24*time.Hour)
	requireExactTrendRanges(t, trends)

	sevenDay := requireTrendWindow(t, trends, "7D")
	if !sevenDay.HistoryComplete {
		t.Fatalf("expected overlapping burst span to make 7D history complete")
	}
	if sevenDay.SourceFrom != configuredSourceFrom.Format(time.RFC3339) {
		t.Fatalf("expected overlapping burst span to clamp source_from to configured lookback start, got %q", sevenDay.SourceFrom)
	}
	if !requireTrendPoint(t, sevenDay.Points, configuredSourceFrom).TranscriptSampled {
		t.Fatalf("expected 7D bucket at configured lookback start to be sampled when burst span overlaps it")
	}
}

func TestBuildRealtimeTrendWindowsBucketsLatestRuntimeSampleOnly(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	samples := []TrendPoint{
		{
			At:                    now.Add(-55 * time.Minute).Format(time.RFC3339),
			PIDConcurrency:        2,
			HasPIDConcurrency:     true,
			MappingCoveragePct:    50,
			HasMappingCoveragePct: true,
			MappedProcesses:       1,
			HasMappedProcesses:    true,
			UnmappedProcesses:     1,
			HasUnmappedProcesses:  true,
			RuntimeSampled:        true,
		},
		{
			At:                    now.Add(-35 * time.Minute).Format(time.RFC3339),
			PIDConcurrency:        4,
			HasPIDConcurrency:     true,
			MappingCoveragePct:    75,
			HasMappingCoveragePct: true,
			MappedProcesses:       3,
			HasMappedProcesses:    true,
			UnmappedProcesses:     1,
			HasUnmappedProcesses:  true,
			RuntimeSampled:        true,
			OutputTokenProjects: []LiveTokenRateProjectSample{{
				Project:               "must-not-leak",
				OutputTokensPerSecond: 2,
			}},
			ThroughputSampled: true,
		},
		{
			At:                    now.Add(-10 * time.Minute).Format(time.RFC3339),
			PIDConcurrency:        3,
			HasPIDConcurrency:     true,
			MappingCoveragePct:    66.6,
			HasMappingCoveragePct: true,
			MappedProcesses:       2,
			HasMappedProcesses:    true,
			UnmappedProcesses:     1,
			HasUnmappedProcesses:  true,
			RuntimeSampled:        true,
		},
	}

	trends := buildRealtimeTrendWindows(samples, now)
	requireExactTrendRanges(t, trends)
	oneDay := trends.Windows[0]
	if oneDay.Range != "1D" {
		t.Fatalf("expected first range 1D, got %s", oneDay.Range)
	}
	if oneDay.HistoryComplete {
		t.Fatalf("expected 1D realtime window to be partial when observer started recently")
	}
	if got := len(oneDay.Points); got != 2 {
		t.Fatalf("expected 2 sampled buckets in 1D realtime window, got %d", got)
	}
	if got := oneDay.Points[0].PIDConcurrency; got != 4 {
		t.Fatalf("expected latest runtime sample to win bucket merge, got pid=%d", got)
	}
	if oneDay.Points[0].ThroughputSampled || oneDay.Points[0].OutputTokenProjects != nil {
		t.Fatalf("runtime trend leaked throughput fields: %+v", oneDay.Points[0])
	}
	if got := oneDay.Points[1].MappedProcesses; got != 2 {
		t.Fatalf("expected latest bucket to preserve mapped process count, got %d", got)
	}
	if oneDay.SourceFrom == "" {
		t.Fatalf("expected realtime window to disclose source_from")
	}
}

func TestBuildThroughputTrendWindowsPreservesSparseTimeSeriesAcrossRanges(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	samples := make([]TrendPoint, 0, 5)
	for index := 0; index < 5; index++ {
		samples = append(samples, throughputTrendPoint(now.Add(time.Duration(index-4)*30*time.Minute), float64(index)))
	}

	trends := buildThroughputTrendWindows(samples, now)
	requireExactTrendRanges(t, trends)
	for _, window := range trends.Windows {
		if len(window.Points) != len(samples) {
			t.Fatalf("%s throughput points = %d, want %d exact time samples", window.Range, len(window.Points), len(samples))
		}
		if window.GranularitySeconds != 1800 {
			t.Fatalf("%s throughput granularity = %d, want observed 1800s", window.Range, window.GranularitySeconds)
		}
		for index, point := range window.Points {
			if !point.ThroughputSampled || !point.HasOutputTokensPerSecond || point.OutputTokensPerSecond != float64(index) {
				t.Fatalf("%s point %d did not preserve exact throughput sample: %+v", window.Range, index, point)
			}
			if point.RuntimeSampled || point.HasPIDConcurrency {
				t.Fatalf("%s throughput point leaked runtime fields: %+v", window.Range, point)
			}
		}
	}
}

func TestBuildThroughputTrendWindowsCapsDenseSeriesWithExactSamples(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	samples := make([]TrendPoint, 0, throughputTrendMaxPoints*2)
	for index := 0; index < throughputTrendMaxPoints*2; index++ {
		at := now.Add(-time.Duration(throughputTrendMaxPoints*2-1-index) * time.Minute)
		samples = append(samples, throughputTrendPoint(at, float64(index)))
	}

	oneDay := requireTrendWindow(t, buildThroughputTrendWindows(samples, now), "1D")
	if len(oneDay.Points) > throughputTrendMaxPoints {
		t.Fatalf("dense throughput series returned %d points, max %d", len(oneDay.Points), throughputTrendMaxPoints)
	}
	if len(oneDay.Points) < throughputTrendMaxPoints-2 {
		t.Fatalf("dense throughput series was over-reduced: %d points", len(oneDay.Points))
	}
	first := oneDay.Points[0]
	last := oneDay.Points[len(oneDay.Points)-1]
	if first.OutputTokensPerSecond != 0 || last.OutputTokensPerSecond != float64(len(samples)-1) {
		t.Fatalf("downsampling must retain exact edge samples, got first=%+v last=%+v", first, last)
	}
	sourceRates := make(map[string]float64, len(samples))
	for _, point := range samples {
		sourceRates[point.At] = point.OutputTokensPerSecond
	}
	for _, point := range oneDay.Points {
		if want, ok := sourceRates[point.At]; !ok || point.OutputTokensPerSecond != want {
			t.Fatalf("downsampling derived a non-source sample: %+v", point)
		}
	}
}

func TestBuildThroughputTrendWindowsSummarizesFullSeriesBeforeDownsampling(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	samples := make([]TrendPoint, 0, throughputTrendMaxPoints*2)
	for index := 0; index < throughputTrendMaxPoints*2; index++ {
		at := now.Add(-time.Duration(throughputTrendMaxPoints*2-1-index) * time.Minute)
		samples = append(samples, throughputTrendPoint(at, float64(index)))
	}

	oneDay := requireTrendWindow(t, buildThroughputTrendWindows(samples, now), "1D")
	summary := oneDay.OutputTokenRateSummary
	if summary == nil {
		t.Fatal("throughput summary is missing")
	}
	if summary.Max != 479 || summary.P95 != 455 || summary.Avg != 239.5 || summary.SampleCount != 480 || summary.WindowSeconds != 300 {
		t.Fatalf("unexpected full-series summary: %+v", summary)
	}
	if summary.Current == nil || *summary.Current != 479 || summary.CurrentAt != now.Format(time.RFC3339) {
		t.Fatalf("unexpected current five-minute rate: %+v", summary)
	}
	if len(oneDay.Points) >= summary.SampleCount {
		t.Fatalf("summary used displayed points instead of %d source samples", summary.SampleCount)
	}
}

func TestBuildThroughputTrendWindowsRejectsMismatchedWindowSamples(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	old := throughputTrendPoint(now.Add(-time.Minute), 99)
	old.OutputTokenThroughputWindowSeconds = 180
	current := throughputTrendPoint(now, 5)

	oneDay := requireTrendWindow(t, buildThroughputTrendWindows([]TrendPoint{old, current}, now), "1D")
	if len(oneDay.Points) != 1 || oneDay.Points[0].At != current.At {
		t.Fatalf("mismatched rolling window leaked into trend: %+v", oneDay.Points)
	}
	if summary := oneDay.OutputTokenRateSummary; summary == nil || summary.Max != 5 || summary.SampleCount != 1 {
		t.Fatalf("mismatched rolling window leaked into summary: %+v", summary)
	}
}

func TestBuildThroughputTrendWindowsDoesNotUseOldRateAsCurrent(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	past := throughputTrendPoint(now.Add(-30*time.Minute), 4)
	unavailable := TrendPoint{
		At:                                 now.Format(time.RFC3339),
		OutputTokenThroughputState:         liveTokenRateStateUnavailable,
		OutputTokenThroughputWindowSeconds: 300,
		ThroughputSampled:                  true,
	}

	oneDay := requireTrendWindow(t, buildThroughputTrendWindows([]TrendPoint{past, unavailable}, now), "1D")
	summary := oneDay.OutputTokenRateSummary
	if summary == nil || summary.Max != 4 || summary.SampleCount != 1 {
		t.Fatalf("historical summary missing: %+v", summary)
	}
	if summary.Current != nil || summary.CurrentAt != "" {
		t.Fatalf("old numeric rate was presented as current: %+v", summary)
	}
}

func TestBuildThroughputTrendWindowsKeepsCurrentMeasuredZero(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	past := throughputTrendPoint(now.Add(-time.Minute), 4)
	current := throughputTrendPoint(now, 0)

	oneDay := requireTrendWindow(t, buildThroughputTrendWindows([]TrendPoint{past, current}, now), "1D")
	summary := oneDay.OutputTokenRateSummary
	if summary == nil || summary.Max != 4 || summary.P95 != 4 || summary.Avg != 2 || summary.SampleCount != 2 {
		t.Fatalf("measured zero missing from period summary: %+v", summary)
	}
	if summary.Current == nil || *summary.Current != 0 || summary.CurrentAt != now.Format(time.RFC3339) {
		t.Fatalf("measured zero missing from current rate: %+v", summary)
	}
}

func TestBuildThroughputTrendWindowsRejectsNumericSampleWithoutProjectPartition(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	missing := throughputTrendPoint(now, 3)
	missing.OutputTokenProjects = nil

	oneDay := requireTrendWindow(t, buildThroughputTrendWindows([]TrendPoint{missing}, now), "1D")
	if len(oneDay.Points) != 0 || oneDay.OutputTokenRateSummary != nil {
		t.Fatalf("partition-missing numeric sample leaked into throughput trend: %+v", oneDay)
	}
}

func TestBuildThroughputTrendWindowsPreservesProjectPartitions(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	point := throughputTrendPoint(now, 3)
	point.OutputTokenProjects = []LiveTokenRateProjectSample{
		{Project: "alpha", OutputTokensPerSecond: 2, ActiveSessions: 2},
		{Project: liveTokenRateUnassignedProject, OutputTokensPerSecond: 1, ActiveSessions: 1},
	}

	for _, window := range buildThroughputTrendWindows([]TrendPoint{point}, now).Windows {
		if len(window.Points) != 1 || len(window.Points[0].OutputTokenProjects) != 2 {
			t.Fatalf("%s project partition was not preserved: %+v", window.Range, window.Points)
		}
		total := 0.0
		for _, project := range window.Points[0].OutputTokenProjects {
			total += project.OutputTokensPerSecond
		}
		if total != window.Points[0].OutputTokensPerSecond {
			t.Fatalf("%s project rates sum to %v, aggregate %v", window.Range, total, window.Points[0].OutputTokensPerSecond)
		}
		decoded := marshalTrendPointJSON(t, window.Points[0])
		if _, ok := decoded["output_token_projects"]; !ok {
			t.Fatalf("%s JSON omitted project throughput partition", window.Range)
		}
	}
}

func TestTrendPointJSONDoesNotInventMissingProjectPartition(t *testing.T) {
	point := throughputTrendPoint(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC), 3)
	point.OutputTokenProjects = nil
	decoded := marshalTrendPointJSON(t, point)
	if _, ok := decoded["output_token_projects"]; ok {
		t.Fatal("missing stored project partition must remain absent")
	}

	point.OutputTokensPerSecond = 0
	point.OutputTokenProjects = []LiveTokenRateProjectSample{}
	decoded = marshalTrendPointJSON(t, point)
	var projects []LiveTokenRateProjectSample
	if err := json.Unmarshal(decoded["output_token_projects"], &projects); err != nil || projects == nil || len(projects) != 0 {
		t.Fatalf("measured zero partition = %#v, want empty array", decoded["output_token_projects"])
	}
}

func TestMergeRuntimeTrendsMarksSampledMetricPresence(t *testing.T) {
	generatedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	app := &trayApp{}
	snapshot := Snapshot{
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Current: CurrentMetrics{
			PIDConcurrency: 0,
		},
		Summary: SnapshotSummary{
			MappingCoveragePct: 0,
			MappedProcesses:    0,
			UnmappedProcesses:  0,
		},
	}

	snapshot = app.rememberSnapshot(snapshot)
	oneDay := requireTrendWindow(t, snapshot.RealtimeTrends, "1D")
	point := requireTrendPoint(t, oneDay.Points, generatedAt)
	if !point.RuntimeSampled {
		t.Fatalf("expected generated runtime sample to be sampled")
	}
	if !point.HasPIDConcurrency || !point.HasMappingCoveragePct || !point.HasMappedProcesses || !point.HasUnmappedProcesses {
		t.Fatalf("expected runtime sample to mark metric presence: %+v", point)
	}
	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "runtime_sampled", true)
	requireJSONInt(t, decoded, "pid_concurrency", 0)
	requireJSONFloat64(t, decoded, "mapping_coverage_pct", 0)
	requireJSONInt(t, decoded, "mapped_processes", 0)
	requireJSONInt(t, decoded, "unmapped_processes", 0)
	requireTrendPointKeysAbsent(t, decoded, "throughput_sampled", "output_tokens_per_second")
}

func TestMergeRuntimeTrendsCarriesLiveOutputThroughput(t *testing.T) {
	generatedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sampler := newTestLiveTokenRateSampler(Config{CodexRoots: []string{t.TempDir()}})
	sampler.published = liveTokenRatePublished{
		Configured:   true,
		Initialized:  true,
		LatestSignal: generatedAt,
		LatestEvent:  generatedAt,
		Buckets:      []liveTokenRateEvent{{At: generatedAt, Tokens: 360, Session: "session-a"}},
		Projects:     map[string]string{"session-a": "alpha"},
	}
	app := &trayApp{liveTokenRate: sampler}
	snapshot := app.rememberSnapshot(Snapshot{GeneratedAt: generatedAt.Format(time.RFC3339)})

	point := requireTrendPoint(t, requireTrendWindow(t, snapshot.ThroughputTrends, "1D").Points, generatedAt)
	if !point.ThroughputSampled || !point.HasOutputTokensPerSecond || point.OutputTokensPerSecond != 1.2 || point.OutputTokenThroughputState != liveTokenRateStateLive || point.OutputTokenThroughputWindowSeconds != 300 {
		t.Fatalf("expected live output throughput in trend point, got %+v", point)
	}
	if len(point.OutputTokenProjects) != 1 || point.OutputTokenProjects[0].Project != "alpha" || point.OutputTokenProjects[0].OutputTokensPerSecond != 1.2 {
		t.Fatalf("expected exact project throughput partition, got %+v", point.OutputTokenProjects)
	}
	decoded := marshalTrendPointJSON(t, point)
	requireJSONFloat64(t, decoded, "output_tokens_per_second", 1.2)
	requireJSONStringValue(t, decoded, "output_token_throughput_state", liveTokenRateStateLive)
	requireJSONInt(t, decoded, "output_token_throughput_window_seconds", 300)
}

func throughputTrendPoint(at time.Time, rate float64) TrendPoint {
	point := TrendPoint{
		At:                                 at.Format(time.RFC3339),
		OutputTokensPerSecond:              rate,
		HasOutputTokensPerSecond:           true,
		OutputTokenThroughputState:         liveTokenRateStateLive,
		OutputTokenThroughputWindowSeconds: 300,
		ThroughputSampled:                  true,
	}
	if rate > 0 {
		point.OutputTokenProjects = []LiveTokenRateProjectSample{{
			Project:               liveTokenRateUnassignedProject,
			OutputTokensPerSecond: rate,
		}}
	} else {
		point.OutputTokenProjects = []LiveTokenRateProjectSample{}
	}
	return point
}

func countTrendPoints(points []TrendPoint, keep func(TrendPoint) bool) int {
	total := 0
	for _, point := range points {
		if keep(point) {
			total++
		}
	}
	return total
}

func requireTrendWindow(t *testing.T, trends TrendSet, label string) *TrendWindow {
	t.Helper()
	for i := range trends.Windows {
		if trends.Windows[i].Range == label {
			return &trends.Windows[i]
		}
	}
	t.Fatalf("missing %s trend window", label)
	return nil
}

func requireTrendPoint(t *testing.T, points []TrendPoint, at time.Time) TrendPoint {
	t.Helper()
	want := at.Format(time.RFC3339)
	for _, point := range points {
		if point.At == want {
			return point
		}
	}
	t.Fatalf("missing trend point at %s", want)
	return TrendPoint{}
}

func requireExactTrendRanges(t *testing.T, trends TrendSet) {
	t.Helper()
	want := []string{"1D", "3D", "7D", "15D", "30D"}
	if len(trends.Windows) != len(want) {
		t.Fatalf("expected %d trend windows, got %d", len(want), len(trends.Windows))
	}
	for i, label := range want {
		if trends.Windows[i].Range != label {
			t.Fatalf("trend window %d: expected %s, got %s", i, label, trends.Windows[i].Range)
		}
	}
}

func marshalTrendPointJSON(t *testing.T, point TrendPoint) map[string]json.RawMessage {
	t.Helper()
	decoded := map[string]json.RawMessage{}
	mustMarshalAndUnmarshalJSON(t, point, &decoded)
	return decoded
}

func mustMarshalAndUnmarshalJSON(t *testing.T, input any, out any) {
	t.Helper()
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
}

func requireOnlyTimestampForUnsampledTrendPoint(t *testing.T, point map[string]json.RawMessage) {
	t.Helper()
	requireJSONString(t, point, "at")
	requireTrendPointKeysAbsent(t, point,
		"active_burst_concurrency",
		"session_concurrency",
		"transcript_sampled",
		"pid_concurrency",
		"mapping_coverage_pct",
		"mapped_processes",
		"unmapped_processes",
		"runtime_sampled",
		"output_tokens_per_second",
		"output_token_throughput_state",
		"output_token_throughput_window_seconds",
		"output_token_active_sessions",
		"throughput_sampled",
	)
}

func requireTrendPointKeysAbsent(t *testing.T, point map[string]json.RawMessage, keys ...string) {
	t.Helper()
	for _, key := range keys {
		if _, ok := point[key]; ok {
			t.Fatalf("expected key %q to be omitted, got %s", key, string(point[key]))
		}
	}
}

func requireJSONString(t *testing.T, point map[string]json.RawMessage, key string) string {
	t.Helper()
	raw, ok := point[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("key %q: expected string, got %s: %v", key, string(raw), err)
	}
	return value
}

func requireJSONStringValue(t *testing.T, point map[string]json.RawMessage, key, want string) {
	t.Helper()
	if got := requireJSONString(t, point, key); got != want {
		t.Fatalf("%s = %q, want %q", key, got, want)
	}
}

func requireJSONBool(t *testing.T, point map[string]json.RawMessage, key string, want bool) {
	t.Helper()
	raw, ok := point[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("key %q: expected bool, got %s: %v", key, string(raw), err)
	}
	if value != want {
		t.Fatalf("key %q: expected %t, got %t", key, want, value)
	}
}

func requireJSONInt(t *testing.T, point map[string]json.RawMessage, key string, want int) {
	t.Helper()
	raw, ok := point[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("key %q: expected int, got %s: %v", key, string(raw), err)
	}
	if value != want {
		t.Fatalf("key %q: expected %d, got %d", key, want, value)
	}
}

func requireJSONFloat64(t *testing.T, point map[string]json.RawMessage, key string, want float64) {
	t.Helper()
	raw, ok := point[key]
	if !ok {
		t.Fatalf("missing key %q", key)
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("key %q: expected float64, got %s: %v", key, string(raw), err)
	}
	if value != want {
		t.Fatalf("key %q: expected %v, got %v", key, want, value)
	}
}
