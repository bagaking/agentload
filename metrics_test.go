package main

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"testing"
	"time"
)

func TestBuildBurstSpansSplitsOnIdleGap(t *testing.T) {
	base := time.Date(2026, 6, 27, 10, 0, 0, 0, time.UTC)
	traces := map[string]*snapshot.SessionTrace{
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
	intervals := []snapshot.Interval{
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
	intervals := []snapshot.Interval{
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
	trends := buildTranscriptTrendWindows(&snapshot.TranscriptData{}, now, 7*24*time.Hour)
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
		if countTrendPoints(window.Points, func(point snapshot.TrendPoint) bool { return point.TranscriptSampled }) != 0 {
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
	trends := buildTranscriptTrendWindows(&snapshot.TranscriptData{}, now, 7*24*time.Hour)
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
	data := &snapshot.TranscriptData{
		Traces: map[string]*snapshot.SessionTrace{
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
		SessionSpans: []snapshot.Interval{
			{Start: evidenceStart, End: evidenceStart.Add(30 * time.Minute)},
		},
		BurstSpans: []snapshot.Interval{
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

func TestBuildTranscriptTrendWindowsLeavesObservationInstantUnsampled(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	// Nineteen sessions that are all still running. On disk a live session's
	// span ends at its last transcript event, so every end is strictly before
	// now -- which is exactly why the instant `now` cannot be measured.
	spans := make([]snapshot.Interval, 0, 19)
	for i := 0; i < 19; i++ {
		spans = append(spans, snapshot.Interval{
			Start: now.Add(-3 * time.Hour),
			End:   now.Add(-time.Duration(5+i*25) * time.Second),
		})
	}
	data := &snapshot.TranscriptData{SessionSpans: spans, BurstSpans: spans}

	for _, window := range buildTranscriptTrendWindows(data, now, 7*24*time.Hour).Windows {
		points := window.Points
		last := points[len(points)-1]
		if last.At != now.Format(time.RFC3339) {
			t.Fatalf("%s: expected the final point to sit at the observation instant, got %q", window.Range, last.At)
		}
		// Claiming a number here would report "nothing was running" for a
		// machine with nineteen live sessions. Report nothing instead.
		if last.TranscriptSampled || last.HasActiveBurst || last.HasSessionConcurrency {
			t.Fatalf("%s: expected the observation instant to stay unsampled, got %+v", window.Range, last)
		}
		if last.SessionConcurrency != 0 || last.ActiveBurstConcurrency != 0 {
			t.Fatalf("%s: expected no concurrency values at the observation instant, got %+v", window.Range, last)
		}
	}

	// The bucket before it still has to carry the real count, otherwise the
	// chart would end on a gap with nothing behind it.
	oneDay := requireTrendWindow(t, buildTranscriptTrendWindows(data, now, 7*24*time.Hour), "1D")
	previous := oneDay.Points[len(oneDay.Points)-2]
	if !previous.TranscriptSampled {
		t.Fatalf("expected the bucket before the observation instant to be sampled, got %+v", previous)
	}
	if previous.SessionConcurrency != 19 {
		t.Fatalf("expected the bucket before the observation instant to report 19 open sessions, got %d", previous.SessionConcurrency)
	}
}

func TestBuildTranscriptTrendWindowsMarksSampledMetricPresence(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	evidenceStart := now.Add(-24 * time.Hour)
	data := &snapshot.TranscriptData{
		SessionSpans: []snapshot.Interval{
			{Start: evidenceStart, End: evidenceStart.Add(30 * time.Minute)},
		},
		BurstSpans: []snapshot.Interval{
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
	point := snapshot.TrendPoint{
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
	point := snapshot.TrendPoint{
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
	point := snapshot.TrendPoint{
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
	point := snapshot.TrendPoint{
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
	point := snapshot.TrendPoint{
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
	point := snapshot.TrendPoint{
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
	point := snapshot.TrendPoint{
		At:                   time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		PIDConcurrency:       5,
		HasPIDConcurrency:    true,
		MappedProcesses:      4,
		HasMappedProcesses:   true,
		UnmappedProcesses:    1,
		HasUnmappedProcesses: true,
		RuntimeSampled:       true,
		RuntimeProcesses: []snapshot.ProcessRuntimeSummary{
			{Key: "codex", Tool: "codex", DisplayName: "Codex", PIDCount: 3},
			{Key: "claude", Tool: "claude", DisplayName: "Claude", PIDCount: 2},
		},
		HostAppProcesses: []snapshot.HostAppProcessSummary{
			{Key: "cursor", Name: "Cursor", PIDCount: 4},
		},
	}

	decoded := marshalTrendPointJSON(t, point)
	requireJSONBool(t, decoded, "runtime_sampled", true)
	requireJSONInt(t, decoded, "pid_concurrency", 5)
	var runtime []snapshot.ProcessRuntimeSummary
	if err := json.Unmarshal(decoded["runtime_process_summary"], &runtime); err != nil {
		t.Fatalf("runtime_process_summary: %v", err)
	}
	if len(runtime) != 2 || runtime[0].Tool != "codex" || runtime[0].PIDCount != 3 {
		t.Fatalf("unexpected runtime_process_summary: %+v", runtime)
	}
	var hosts []snapshot.HostAppProcessSummary
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
	data := &snapshot.TranscriptData{
		Traces: map[string]*snapshot.SessionTrace{
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
		SessionSpans: []snapshot.Interval{
			{Start: evidenceStart, End: evidenceStart.Add(20 * time.Minute)},
			{Start: now.Add(-2 * time.Hour), End: now.Add(-90 * time.Minute)},
		},
		BurstSpans: []snapshot.Interval{
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
	// Every bucket is covered by evidence except the terminal one: that point
	// sits at the observation instant, which no span can overlap, so
	// buildTranscriptTrendWindows leaves it unsampled rather than reporting 0.
	// See TestBuildTranscriptTrendWindowsLeavesObservationInstantUnsampled.
	if countTrendPoints(sevenDay.Points, func(point snapshot.TrendPoint) bool { return point.TranscriptSampled }) != len(sevenDay.Points)-1 {
		t.Fatalf("expected every 7D bucket before the observation instant to be transcript-sampled")
	}

	if fifteenDay.HistoryComplete {
		t.Fatalf("expected 15D window to report partial coverage")
	}
	if thirtyDay.HistoryComplete {
		t.Fatalf("expected 30D window to report partial coverage")
	}
	if countTrendPoints(fifteenDay.Points, func(point snapshot.TrendPoint) bool { return point.TranscriptSampled }) == len(fifteenDay.Points) {
		t.Fatalf("expected 15D window to keep buckets before evidence start unsampled")
	}
	if countTrendPoints(thirtyDay.Points, func(point snapshot.TrendPoint) bool { return point.TranscriptSampled }) == len(thirtyDay.Points) {
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
	data := &snapshot.TranscriptData{
		SessionSpans: []snapshot.Interval{
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
	data := &snapshot.TranscriptData{
		BurstSpans: []snapshot.Interval{
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
	samples := []snapshot.TrendPoint{
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
			OutputTokenProjects: []snapshot.LiveTokenRateProjectSample{{
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

func TestBuildThroughputTrendWindowsDerivesSelectableWindowsFromMinuteFacts(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	minutes := make([]ThroughputMinuteFact, 0, 20)
	for index := 1; index <= 20; index++ {
		minutes = append(minutes, throughputMinuteFact(now.Add(-time.Duration(20-index)*time.Minute), int64(index*60), "alpha"))
	}

	oneDay := requireTrendWindow(t, buildThroughputTrendWindows(minutes, nil, now), "1D")
	oneMinute := requireThroughputSeries(t, oneDay, "minute:60")
	fiveMinutes := requireThroughputSeries(t, oneDay, "minute:300")
	fifteenMinutes := requireThroughputSeries(t, oneDay, "minute:900")
	if len(oneMinute.Points) != 20 || len(fiveMinutes.Points) != 16 || len(fifteenMinutes.Points) != 6 {
		t.Fatalf("unexpected rollup point counts: 1m=%d 5m=%d 15m=%d", len(oneMinute.Points), len(fiveMinutes.Points), len(fifteenMinutes.Points))
	}
	if got := oneMinute.Points[len(oneMinute.Points)-1].OutputTokensPerSecond; got != 20 {
		t.Fatalf("latest 1m rate = %v, want 20", got)
	}
	if got := fiveMinutes.Points[len(fiveMinutes.Points)-1].OutputTokensPerSecond; got != 18 {
		t.Fatalf("latest 5m rate = %v, want 18", got)
	}
	if got := fifteenMinutes.Points[len(fifteenMinutes.Points)-1].OutputTokensPerSecond; got != 13 {
		t.Fatalf("latest 15m rate = %v, want 13", got)
	}
	if fiveMinutes.Summary == nil || fiveMinutes.Summary.Current == nil || *fiveMinutes.Summary.Current != 18 || fiveMinutes.Summary.WindowSeconds != 300 {
		t.Fatalf("unexpected 5m summary: %+v", fiveMinutes.Summary)
	}
	if oneMinute.Summary == nil || oneMinute.Summary.WindowSeconds != 60 || fifteenMinutes.Summary == nil || fifteenMinutes.Summary.WindowSeconds != 900 {
		t.Fatalf("summary windows do not match selected series: 1m=%+v 15m=%+v", oneMinute.Summary, fifteenMinutes.Summary)
	}
}

func TestBuildThroughputTrendWindowsKeepsMinuteCoverageGaps(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	minutes := make([]ThroughputMinuteFact, 0, 6)
	for index := 0; index < 6; index++ {
		minute := throughputMinuteFact(now.Add(-time.Duration(5-index)*time.Minute), 60, "alpha")
		if index == 2 {
			minute.OutputTokens = nil
			minute.State = liveTokenRateStateUnavailable
			minute.Projects = nil
		}
		minutes = append(minutes, minute)
	}

	fiveMinutes := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows(minutes, nil, now), "1D"), "minute:300")
	if len(fiveMinutes.Points) != 2 || fiveMinutes.Summary != nil {
		t.Fatalf("coverage gap became numeric throughput: %+v", fiveMinutes)
	}
	for _, point := range fiveMinutes.Points {
		if point.HasOutputTokensPerSecond || point.OutputTokenThroughputState != liveTokenRateStateUnavailable {
			t.Fatalf("coverage gap was hidden: %+v", point)
		}
	}
}

func TestBuildThroughputTrendWindowsMarksAbsentMinuteCoverage(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	minutes := []ThroughputMinuteFact{
		throughputMinuteFact(now.Add(-3*time.Minute), 60, "alpha"),
		throughputMinuteFact(now.Add(-2*time.Minute), 60, "alpha"),
		throughputMinuteFact(now, 60, "alpha"),
	}

	oneMinute := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows(minutes, nil, now), "1D"), "minute:60")
	if len(oneMinute.Points) != 4 {
		t.Fatalf("absent minute was not represented as a coverage gap: %+v", oneMinute.Points)
	}
	gap := oneMinute.Points[2]
	if gap.At != now.Add(-time.Minute).Format(time.RFC3339) || gap.HasOutputTokensPerSecond || gap.OutputTokenThroughputState != liveTokenRateStateNoData {
		t.Fatalf("unexpected coverage-gap marker: %+v", gap)
	}
}

func TestBuildThroughputTrendWindowsCarriesPartialCoverageIntoRollup(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	// A single floor minute inside the 5-minute window makes the rolled-up rate
	// a floor: the sum can only be missing tokens, never carrying extra, so the
	// marker has to survive persistence and aggregation rather than being
	// laundered into an exact-looking number.
	minutes := make([]ThroughputMinuteFact, 0, 5)
	for index := 4; index >= 0; index-- {
		minute := throughputMinuteFact(now.Add(-time.Duration(index)*time.Minute), 60, "alpha")
		if index == 2 {
			minute.Coverage = liveTokenRateCoveragePartial
		}
		minutes = append(minutes, minute)
	}

	fiveMinutes := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows(minutes, nil, now), "1D"), "minute:300")
	point := fiveMinutes.Points[len(fiveMinutes.Points)-1]
	if point.OutputTokenThroughputCoverage != liveTokenRateCoveragePartial {
		t.Fatalf("rolled-up window dropped the floor marker: %+v", point)
	}
	if !point.HasOutputTokensPerSecond || point.OutputTokensPerSecond <= 0 {
		t.Fatalf("floor marker must qualify a real rate, not replace it: %+v", point)
	}

	// Windows built only from complete minutes stay unqualified.
	clean := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows([]ThroughputMinuteFact{
		throughputMinuteFact(now.Add(-time.Minute), 60, "alpha"),
		throughputMinuteFact(now, 60, "alpha"),
	}, nil, now), "1D"), "minute:60")
	if got := clean.Points[len(clean.Points)-1].OutputTokenThroughputCoverage; got != "" {
		t.Fatalf("complete window was labelled %q", got)
	}
}

func TestBuildThroughputTrendWindowsSummarizesBeforeDisplayReduction(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	minutes := make([]ThroughputMinuteFact, 0, throughputTrendMaxPoints*2)
	for index := 0; index < throughputTrendMaxPoints*2; index++ {
		at := now.Add(-time.Duration(throughputTrendMaxPoints*2-1-index) * time.Minute)
		minutes = append(minutes, throughputMinuteFact(at, int64(index*60), "alpha"))
	}

	oneMinute := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows(minutes, nil, now), "1D"), "minute:60")
	if oneMinute.Summary == nil || oneMinute.Summary.Max != 479 || oneMinute.Summary.P95 != 455 || oneMinute.Summary.Avg != 239.5 || oneMinute.Summary.SampleCount != 480 {
		t.Fatalf("unexpected full-series summary: %+v", oneMinute.Summary)
	}
	if len(oneMinute.Points) >= oneMinute.Summary.SampleCount || len(oneMinute.Points) > throughputTrendMaxPoints {
		t.Fatalf("summary used reduced series: points=%d summary=%+v", len(oneMinute.Points), oneMinute.Summary)
	}
}

func TestBuildThroughputTrendWindowsPreservesGapDuringDisplayReduction(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	minutes := make([]ThroughputMinuteFact, 0, throughputTrendMaxPoints*2)
	for index := 0; index < throughputTrendMaxPoints*2; index++ {
		at := now.Add(-time.Duration(throughputTrendMaxPoints*2-1-index) * time.Minute)
		minute := throughputMinuteFact(at, 60, "alpha")
		if index == throughputTrendMaxPoints {
			minute.OutputTokens = nil
			minute.State = liveTokenRateStateUnavailable
			minute.Projects = nil
		}
		minutes = append(minutes, minute)
	}

	oneMinute := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows(minutes, nil, now), "1D"), "minute:60")
	for _, point := range oneMinute.Points {
		if !point.HasOutputTokensPerSecond && point.OutputTokenThroughputState == liveTokenRateStateUnavailable {
			return
		}
	}
	t.Fatalf("display reduction hid the unavailable gap: %+v", oneMinute.Points)
}

func TestBuildThroughputTrendWindowsKeepsCurrentMeasuredZero(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	oneMinute := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows([]ThroughputMinuteFact{throughputMinuteFact(now, 0, "")}, nil, now), "1D"), "minute:60")
	if oneMinute.Summary == nil || oneMinute.Summary.Current == nil || *oneMinute.Summary.Current != 0 || oneMinute.Summary.SampleCount != 1 {
		t.Fatalf("measured zero missing from current summary: %+v", oneMinute.Summary)
	}
}

func TestBuildThroughputTrendWindowsSeparatesLegacySeries(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	rate180 := 3.0
	rate300 := 2.0
	legacy := []LegacyThroughputFact{
		{At: now.Add(-time.Minute).Format(time.RFC3339), State: liveTokenRateStateLive, WindowSeconds: 180, OutputTokensPerSecond: &rate180, Projects: []snapshot.LiveTokenRateProjectSample{{Project: "alpha", OutputTokensPerSecond: rate180}}},
		{At: now.Format(time.RFC3339), State: liveTokenRateStateLive, WindowSeconds: 300, OutputTokensPerSecond: &rate300, Projects: []snapshot.LiveTokenRateProjectSample{{Project: "alpha", OutputTokensPerSecond: rate300}}},
	}
	oneDay := requireTrendWindow(t, buildThroughputTrendWindows(nil, legacy, now), "1D")
	legacy180 := requireThroughputSeries(t, oneDay, "legacy:180")
	legacy300 := requireThroughputSeries(t, oneDay, "legacy:300")
	if legacy180.Kind != throughputSeriesKindLegacy || legacy300.Kind != throughputSeriesKindLegacy || len(legacy180.Points) != 1 || len(legacy300.Points) != 1 {
		t.Fatalf("legacy windows were mixed or lost: 180=%+v 300=%+v", legacy180, legacy300)
	}
	if legacy180.Summary == nil || legacy180.Summary.Current != nil || legacy300.Summary == nil || legacy300.Summary.Current != nil {
		t.Fatalf("legacy rate was presented as current: 180=%+v 300=%+v", legacy180.Summary, legacy300.Summary)
	}
}

func TestBuildThroughputTrendWindowsKeepsDistinctLegacySubsecondPoints(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 1, 0, time.UTC)
	rate := 3.0
	legacy := []LegacyThroughputFact{
		{At: now.Add(-900 * time.Millisecond).Format(time.RFC3339Nano), State: liveTokenRateStateLive, WindowSeconds: 300, OutputTokensPerSecond: &rate, Projects: []snapshot.LiveTokenRateProjectSample{}},
		{At: now.Add(-100 * time.Millisecond).Format(time.RFC3339Nano), State: liveTokenRateStateLive, WindowSeconds: 300, OutputTokensPerSecond: &rate, Projects: []snapshot.LiveTokenRateProjectSample{}},
	}
	series := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows(nil, legacy, now), "1D"), "legacy:300")
	if len(series.Points) != 2 || series.Points[0].At == series.Points[1].At {
		t.Fatalf("distinct legacy observations collapsed: %+v", series.Points)
	}
}

func TestBuildThroughputTrendWindowsPreservesMinuteProjectPartitions(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	tokens := int64(180)
	minute := ThroughputMinuteFact{
		At:            now.Format(time.RFC3339),
		State:         liveTokenRateStateLive,
		OutputTokens:  &tokens,
		SessionHashes: []string{"a", "b"},
		Projects: []ThroughputMinuteProjectFact{
			{Project: "alpha", OutputTokens: 120, SessionHashes: []string{"a"}},
			{Project: liveTokenRateUnassignedProject, OutputTokens: 60, SessionHashes: []string{"b"}},
		},
	}
	series := requireThroughputSeries(t, requireTrendWindow(t, buildThroughputTrendWindows([]ThroughputMinuteFact{minute}, nil, now), "1D"), "minute:60")
	point := series.Points[0]
	if point.OutputTokensPerSecond != 3 || point.OutputTokenActiveSessions != 2 || len(point.OutputTokenProjects) != 2 {
		t.Fatalf("minute project partition was not preserved: %+v", point)
	}
	total := 0.0
	for _, project := range point.OutputTokenProjects {
		total += project.OutputTokensPerSecond
	}
	if total != point.OutputTokensPerSecond {
		t.Fatalf("project rates sum to %v, aggregate %v", total, point.OutputTokensPerSecond)
	}
}

func TestTrendPointJSONDoesNotInventMissingProjectPartition(t *testing.T) {
	point := snapshot.TrendPoint{
		At:                                 time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Format(time.RFC3339),
		OutputTokensPerSecond:              3,
		HasOutputTokensPerSecond:           true,
		OutputTokenThroughputState:         liveTokenRateStateLive,
		OutputTokenThroughputWindowSeconds: 300,
		ThroughputSampled:                  true,
	}
	decoded := marshalTrendPointJSON(t, point)
	if _, ok := decoded["output_token_projects"]; ok {
		t.Fatal("missing stored project partition must remain absent")
	}

	point.OutputTokensPerSecond = 0
	point.OutputTokenProjects = []snapshot.LiveTokenRateProjectSample{}
	decoded = marshalTrendPointJSON(t, point)
	var projects []snapshot.LiveTokenRateProjectSample
	if err := json.Unmarshal(decoded["output_token_projects"], &projects); err != nil || projects == nil || len(projects) != 0 {
		t.Fatalf("measured zero partition = %#v, want empty array", decoded["output_token_projects"])
	}
}

func TestMergeRuntimeTrendsMarksSampledMetricPresence(t *testing.T) {
	generatedAt := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	app := &trayApp{}
	snap := snapshot.Snapshot{
		GeneratedAt: generatedAt.Format(time.RFC3339),
		Current: snapshot.CurrentMetrics{
			PIDConcurrency: 0,
		},
		Summary: snapshot.SnapshotSummary{
			MappingCoveragePct: 0,
			MappedProcesses:    0,
			UnmappedProcesses:  0,
		},
	}

	snap = app.rememberSnapshot(snap)
	oneDay := requireTrendWindow(t, snap.RealtimeTrends, "1D")
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

func throughputMinuteFact(at time.Time, tokens int64, project string) ThroughputMinuteFact {
	fact := ThroughputMinuteFact{
		At:           at.Format(time.RFC3339),
		State:        liveTokenRateStateZero,
		OutputTokens: &tokens,
		Projects:     []ThroughputMinuteProjectFact{},
	}
	if tokens <= 0 {
		return fact
	}
	fact.State = liveTokenRateStateLive
	fact.SessionHashes = []string{"session"}
	if project == "" {
		project = liveTokenRateUnassignedProject
	}
	fact.Projects = []ThroughputMinuteProjectFact{{
		Project:       project,
		OutputTokens:  tokens,
		SessionHashes: []string{"session"},
	}}
	return fact
}

func countTrendPoints(points []snapshot.TrendPoint, keep func(snapshot.TrendPoint) bool) int {
	total := 0
	for _, point := range points {
		if keep(point) {
			total++
		}
	}
	return total
}

func requireTrendWindow(t *testing.T, trends snapshot.TrendSet, label string) *snapshot.TrendWindow {
	t.Helper()
	for i := range trends.Windows {
		if trends.Windows[i].Range == label {
			return &trends.Windows[i]
		}
	}
	t.Fatalf("missing %s trend window", label)
	return nil
}

func requireThroughputSeries(t *testing.T, window *snapshot.TrendWindow, key string) *snapshot.ThroughputTrendSeries {
	t.Helper()
	if window == nil {
		t.Fatalf("missing trend window for throughput series %s", key)
	}
	for i := range window.ThroughputSeries {
		if window.ThroughputSeries[i].Key == key {
			return &window.ThroughputSeries[i]
		}
	}
	t.Fatalf("missing throughput series %s", key)
	return nil
}

func requireTrendPoint(t *testing.T, points []snapshot.TrendPoint, at time.Time) snapshot.TrendPoint {
	t.Helper()
	want := at.Format(time.RFC3339)
	for _, point := range points {
		if point.At == want {
			return point
		}
	}
	t.Fatalf("missing trend point at %s", want)
	return snapshot.TrendPoint{}
}

func requireExactTrendRanges(t *testing.T, trends snapshot.TrendSet) {
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

func marshalTrendPointJSON(t *testing.T, point snapshot.TrendPoint) map[string]json.RawMessage {
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
