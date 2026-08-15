package main

import (
	"agentload/internal/snapshot"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDefaultMetricRegistryContainsCoreFamilies(t *testing.T) {
	registry := defaultMetricRegistry()
	keys := map[string]snapshot.MetricRegistryEntry{}
	for _, entry := range registry {
		keys[entry.Key] = entry
	}
	for _, key := range []string{"recent_movement", "known_sessions", "process_pressure", "system_resources", "token_usage", "output_token_throughput", "diagnostic_export"} {
		entry, ok := keys[key]
		if !ok {
			t.Fatalf("missing metric registry key %q in %+v", key, registry)
		}
		if entry.Source == "" || entry.MissingState == "" || entry.Description == "" {
			t.Fatalf("registry entry %q must explain source, missing state, and description: %+v", key, entry)
		}
	}
	if keys["token_usage"].MissingState == "zero" {
		t.Fatalf("token usage must not treat missing usage as zero: %+v", keys["token_usage"])
	}
	if keys["output_token_throughput"].Window != "trailing 300 seconds of wall time" {
		t.Fatalf("output token throughput must disclose its wall-time window: %+v", keys["output_token_throughput"])
	}
}

func TestDefaultRuntimeTelemetrySnapshotIsOptional(t *testing.T) {
	telemetry := defaultRuntimeTelemetrySnapshot()
	if telemetry.Configured || telemetry.Status != "not_configured" {
		t.Fatalf("runtime telemetry must default to optional and not configured, got %+v", telemetry)
	}
	if len(telemetry.Adapters) < 2 {
		t.Fatalf("expected local adapter seams, got %+v", telemetry.Adapters)
	}
}

func TestBuildDiagnosticsSnapshotSeparatesAnomaliesAndEvidenceGaps(t *testing.T) {
	now := time.Date(2026, 7, 7, 10, 0, 0, 0, time.UTC)
	snap := snapshot.Snapshot{
		Current: snapshot.CurrentMetrics{PIDConcurrency: 4, SessionConcurrency: 2, ActiveBurstConcurrency: 0},
		Summary: snapshot.SnapshotSummary{MappedProcesses: 2, UnmappedProcesses: 2, MappingCoveragePct: 50},
		CoordinationRisk: snapshot.CoordinationRiskSnapshot{
			LowConfidenceSessionCount: 1,
			Signals: []snapshot.RiskSignalSnapshot{
				{Kind: "duplicate_overlap", Severity: "warn", Evidence: "2 sessions overlap"},
			},
		},
		TranscriptStats: snapshot.TranscriptStats{DeferredFiles: 3, Errors: []string{"bad trace"}},
		ProcessStats:    snapshot.ProcessObservationStats{Incomplete: true, LastKnown: true, Error: "signal: killed"},
		SystemResources: snapshot.SystemResourceSnapshot{Supported: true, CPUPercent: 91, MemoryUsedPct: 72, Notes: []string{"Network counters are unavailable."}},
		LiveSessions: []snapshot.LiveSessionSnapshot{
			{SessionID: "s1"},
			{SessionID: "s2", TokenUsage: &snapshot.TokenUsage{InputTokens: 10, OutputTokens: 2}, TokenUsageSource: "transcript_usage", TokenUsageConfidence: "measured"},
		},
	}

	diagnostics := buildDiagnosticsSnapshot(snap, now)

	if diagnostics.GeneratedAt == "" || diagnostics.Export.Endpoint != "/api/diagnostic-export" {
		t.Fatalf("expected generated diagnostics with export contract, got %+v", diagnostics)
	}
	if !hasDiagnosticSignal(diagnostics.AnomalySignals, "duplicate_overlap") || !hasDiagnosticSignal(diagnostics.AnomalySignals, "system_cpu_pressure") {
		t.Fatalf("expected risk and system anomaly signals, got %+v", diagnostics.AnomalySignals)
	}
	for _, kind := range []string{"process_observation_incomplete", "unmapped_processes", "low_confidence_sessions", "deferred_transcript_scan", "transcript_parse_errors", "system_resource_sampling_notes", "system_resource_rates_pending"} {
		if !hasDiagnosticSignal(diagnostics.EvidenceGaps, kind) {
			t.Fatalf("expected evidence gap %q, got %+v", kind, diagnostics.EvidenceGaps)
		}
	}
	if !hasDiagnosticCapability(diagnostics.Capabilities, "runtime_telemetry_adapter", "not_configured") {
		t.Fatalf("expected optional runtime telemetry seam, got %+v", diagnostics.Capabilities)
	}
	if !slices.Contains(diagnostics.Export.OmittedFields, "raw prompts") {
		t.Fatalf("expected export omitted fields to include raw prompts, got %+v", diagnostics.Export.OmittedFields)
	}
}

func TestDiagnosticEvolutionInsightsDescribeObservedPatterns(t *testing.T) {
	snap := snapshot.Snapshot{
		Current: snapshot.CurrentMetrics{SessionConcurrency: 3},
		Summary: snapshot.SnapshotSummary{UnmappedProcesses: 1},
		CoordinationRisk: snapshot.CoordinationRiskSnapshot{
			ActiveProjectCount:             2,
			StaleSessionCount:              1,
			DuplicateOverlapSuspicionCount: 2,
			ProjectSpreadCount:             2,
			LowConfidenceSessionCount:      1,
			ChurnSessionCount:              3,
		},
	}
	insights := buildDiagnosticEvolutionInsights(snap)
	if len(insights) != 3 {
		t.Fatalf("expected three independent RSI review prompts, got %+v", insights)
	}
	for _, insight := range insights {
		if insight.Hypothesis == "" || insight.Evidence == "" || insight.Experiment == "" || insight.Verification == "" {
			t.Fatalf("evolution insight must be an evidence-to-experiment loop: %+v", insight)
		}
		if insight.Confidence != "observed" || insight.Status != "needs_review" {
			t.Fatalf("evolution insight should remain an observed review prompt: %+v", insight)
		}
	}
}

// TestCoordinationShapeEvidenceNamesOnlyTheHalfThatFired pins the zero-as-
// evidence bug. The insight fires on overlaps OR wide project spread, but the
// evidence string used to print both counts unconditionally -- so a snapshot
// with no overlaps rendered "0 overlap suspicions across 32 projects", a zero
// offered as support for the hypothesis it actually fails to support.
func TestCoordinationShapeEvidenceNamesOnlyTheHalfThatFired(t *testing.T) {
	spreadOnly := snapshot.Snapshot{CoordinationRisk: snapshot.CoordinationRiskSnapshot{
		DuplicateOverlapSuspicionCount: 0,
		ProjectSpreadCount:             32,
	}}
	insight := requireEvolutionInsight(t, buildDiagnosticEvolutionInsights(spreadOnly), "coordination_shape")
	if strings.Contains(insight.Evidence, "0 overlap") {
		t.Errorf("spread-only evidence still cites a zero count: %q", insight.Evidence)
	}
	if insight.EvidenceKey != "coordination_shape_spread" {
		t.Errorf("spread-only insight should select the spread copy, got %q", insight.EvidenceKey)
	}

	overlapOnly := snapshot.Snapshot{CoordinationRisk: snapshot.CoordinationRiskSnapshot{
		DuplicateOverlapSuspicionCount: 2,
		ProjectSpreadCount:             1,
	}}
	insight = requireEvolutionInsight(t, buildDiagnosticEvolutionInsights(overlapOnly), "coordination_shape")
	if insight.EvidenceKey != "coordination_shape_overlap" {
		t.Errorf("overlap-only insight should select the overlap copy, got %q", insight.EvidenceKey)
	}

	both := snapshot.Snapshot{CoordinationRisk: snapshot.CoordinationRiskSnapshot{
		DuplicateOverlapSuspicionCount: 2,
		ProjectSpreadCount:             32,
	}}
	insight = requireEvolutionInsight(t, buildDiagnosticEvolutionInsights(both), "coordination_shape")
	if insight.EvidenceKey != "coordination_shape" {
		t.Errorf("both-halves insight should keep the combined copy, got %q", insight.EvidenceKey)
	}
}

// TestAnomaliesRestatingAGapAreDropped pins the deduplication. observer.go and
// diagnostics.go each derive a row from the same counter, so the panel showed
// one fact as two rows -- and with the priority table capped at six, every
// duplicate evicted a distinct finding (the deferred-scan and out-of-horizon
// counts were the ones actually pushed off the page).
func TestAnomaliesRestatingAGapAreDropped(t *testing.T) {
	snap := snapshot.Snapshot{
		Current: snapshot.CurrentMetrics{PIDConcurrency: 116},
		Summary: snapshot.SnapshotSummary{UnmappedProcesses: 16},
		CoordinationRisk: snapshot.CoordinationRiskSnapshot{
			LowConfidenceSessionCount: 11,
			Signals: []snapshot.RiskSignalSnapshot{
				{Kind: "unmatched_processes", Severity: "observed", Evidence: "16 visible live processes are not currently matched"},
				{Kind: "low_confidence_mapping", Severity: "observed", Evidence: "11 live sessions have low-confidence mapping"},
				{Kind: "project_spread", Severity: "observed", Evidence: "118 sessions span 33 projects"},
			},
		},
	}
	diagnostics := buildDiagnosticsSnapshot(snap, time.Now())
	for _, kind := range []string{"unmatched_processes", "low_confidence_mapping"} {
		if hasDiagnosticSignal(diagnostics.AnomalySignals, kind) {
			t.Errorf("anomaly %q duplicates an evidence gap and should have been dropped", kind)
		}
	}
	// The gap each duplicate restated must survive: dropping both rows would
	// hide the fact instead of deduplicating it.
	for _, kind := range []string{"unmapped_processes", "low_confidence_sessions"} {
		if !hasDiagnosticSignal(diagnostics.EvidenceGaps, kind) {
			t.Errorf("evidence gap %q went missing; dedup must keep the richer row", kind)
		}
	}
	// A risk signal with no gap counterpart is untouched.
	if !hasDiagnosticSignal(diagnostics.AnomalySignals, "project_spread") {
		t.Error("project_spread has no gap counterpart and must survive dedup")
	}
}

// TestGapWithoutItsAnomalyKeepsTheAnomaly guards the other direction: the drop
// is conditional on the gap actually being present, so a snapshot that raises
// the risk signal without the gap still reports the fact once.
func TestGapWithoutItsAnomalyKeepsTheAnomaly(t *testing.T) {
	snap := snapshot.Snapshot{CoordinationRisk: snapshot.CoordinationRiskSnapshot{
		Signals: []snapshot.RiskSignalSnapshot{
			{Kind: "unmatched_processes", Severity: "observed", Evidence: "3 unmatched"},
		},
	}}
	diagnostics := buildDiagnosticsSnapshot(snap, time.Now())
	if hasDiagnosticSignal(diagnostics.EvidenceGaps, "unmapped_processes") {
		t.Fatal("fixture should not raise the unmapped_processes gap")
	}
	if !hasDiagnosticSignal(diagnostics.AnomalySignals, "unmatched_processes") {
		t.Error("with no gap to restate, the risk signal is the only report of this fact and must survive")
	}
}

func requireEvolutionInsight(t *testing.T, insights []snapshot.DiagnosticEvolutionInsight, key string) snapshot.DiagnosticEvolutionInsight {
	t.Helper()
	for _, insight := range insights {
		if insight.Key == key {
			return insight
		}
	}
	t.Fatalf("expected an evolution insight keyed %q, got %+v", key, insights)
	return snapshot.DiagnosticEvolutionInsight{}
}

func TestSessionModelUsageKeepsUnattributedResidualExplicit(t *testing.T) {	trace := snapshot.SessionTrace{
		TokenUsage: snapshot.TokenUsage{InputTokens: 12, OutputTokens: 8, TotalTokens: 20},
		ModelUsage: snapshot.ModelTokenUsage{"model-a": {InputTokens: 7, OutputTokens: 5, TotalTokens: 12}},
	}
	trace.EnsureModelUsage()
	if got := trace.ModelUsage[snapshot.UnknownModel]; got.InputTokens != 5 || got.OutputTokens != 3 || got.TotalTokens != 8 {
		t.Fatalf("expected uncovered measured tokens in unknown model bucket, got %+v", got)
	}
	rows := trace.ModelUsage.Rows()
	if len(rows) != 2 || (rows[0].Model != snapshot.UnknownModel && rows[1].Model != snapshot.UnknownModel) {
		t.Fatalf("expected stable model rows including unknown bucket, got %+v", rows)
	}
}

func TestDiagnosticBaselinesKeepEmptyMappingAndMeasuredTokensHonest(t *testing.T) {
	snap := snapshot.Snapshot{
		Current: snapshot.CurrentMetrics{PIDConcurrency: 0, SessionConcurrency: 2},
		Summary: snapshot.SnapshotSummary{},
		LiveSessions: []snapshot.LiveSessionSnapshot{
			{SessionID: "estimated", TokenUsage: &snapshot.TokenUsage{InputTokens: 10}, TokenUsageSource: "runtime_adapter", TokenUsageConfidence: "estimated"},
			{SessionID: "measured", TokenUsage: &snapshot.TokenUsage{InputTokens: 4}, TokenUsageSource: "transcript_usage", TokenUsageConfidence: "measured"},
		},
	}
	diagnostics := buildDiagnosticsSnapshot(snap, time.Now())
	mapping := requireDiagnosticBaseline(t, diagnostics.Baselines, "mapping_coverage")
	if mapping.Status != "empty" || mapping.Value != "no visible PIDs" {
		t.Fatalf("expected empty mapping baseline for no visible pids, got %+v", mapping)
	}
	tokens := requireDiagnosticBaseline(t, diagnostics.Baselines, "token_measured_sessions")
	if tokens.Value != "1 of 2" || tokens.Status != "partial" {
		t.Fatalf("expected only measured transcript token usage to count, got %+v", tokens)
	}
}

// TestUnjudgedWalkCostNeverClaimsAPassingStatus guards the one status producer
// in diagnostics.go that compares the value against nothing.
//
// Every other one earns "ok" by clearing a bound: baselineStatus compares
// against okAt/warnAt, tokenStatus compares measured against total,
// zeroGoodStatus compares against zero. scanCostStatus has no budget to compare
// against (M02_S02 owns that), and "ok" renders mint green -- so returning it
// would paint a 30s walk and a 9ms walk the same reassuring color, which is
// exactly the verdict the function's own comment says it cannot make.
func TestUnjudgedWalkCostNeverClaimsAPassingStatus(t *testing.T) {
	for _, elapsed := range []int64{9, 1173, 30000} {
		snap := snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{
			ScanCost: snapshot.TranscriptScanCost{WalkMeasured: true, ElapsedMs: elapsed},
		}}
		cost := requireDiagnosticBaseline(t, buildDiagnosticsSnapshot(snap, time.Now()).Baselines, "evidence_walk_cost")
		if cost.Status == "ok" || cost.Status == "available" {
			t.Fatalf("a %dms walk claims passing status %q, but no documented budget says it passed", elapsed, cost.Status)
		}
		if cost.Status != "observed" {
			t.Fatalf("a measured %dms walk should report observed, got %q", elapsed, cost.Status)
		}
	}
}

func TestScanCostStaysUnavailableUntilAWalkHasRun(t *testing.T) {
	// evidence_index.go zeroes its walk counters on a pass served from the
	// index. Surfacing those unguarded would report "no walk has run" as a
	// measured 0ms, so an unmeasured cost must have no number at all.
	unmeasured := snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{
		ScanCost: snapshot.TranscriptScanCost{WalkMeasured: false, AgedOutFiles: 9060},
	}}
	diagnostics := buildDiagnosticsSnapshot(unmeasured, time.Now())
	cost := requireDiagnosticBaseline(t, diagnostics.Baselines, "evidence_walk_cost")
	if cost.Value != liveTokenRateStateNoData || cost.Status != "unavailable" {
		t.Fatalf("expected an unmeasured walk to report no data, got %+v", cost)
	}
	// AgedOutFiles describes the index contents rather than the walk, so it
	// survives a non-reconciling pass and is still reportable.
	if !hasDiagnosticSignal(diagnostics.EvidenceGaps, "evidence_out_of_horizon") {
		t.Fatalf("expected out-of-horizon evidence to stay reportable, got %+v", diagnostics.EvidenceGaps)
	}

	// A warm pass carries the last real walk, so the cost stays answerable
	// between reconciles rather than blanking out for the process lifetime.
	warm := snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{
		ScanCost: snapshot.TranscriptScanCost{WalkMeasured: true, WalkFresh: false, ElapsedMs: 1173, VisitedEntries: 28145, PrunedDirectories: 934},
	}}
	diagnostics = buildDiagnosticsSnapshot(warm, time.Now())
	cost = requireDiagnosticBaseline(t, diagnostics.Baselines, "evidence_walk_cost")
	if cost.Value != "1173ms" || cost.Status != "observed" {
		t.Fatalf("expected a warm pass to keep the last measured walk, got %+v", cost)
	}

	fast := snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{
		ScanCost: snapshot.TranscriptScanCost{WalkMeasured: true, WalkFresh: true, ElapsedMs: 9},
	}}
	diagnostics = buildDiagnosticsSnapshot(fast, time.Now())
	cost = requireDiagnosticBaseline(t, diagnostics.Baselines, "evidence_walk_cost")
	if cost.Value != "9ms" || cost.Status != "observed" {
		t.Fatalf("expected a cheap measured walk to report observed, got %+v", cost)
	}
}

// TestWalkCostIsReportedWithoutJudgingIt pins the deletion of the 750ms
// threshold. The walk cost is a reading, and nothing in the repo documents a
// refresh budget that would say which duration is too long -- so no duration
// may raise a signal or downgrade the baseline's status. Restoring a threshold
// here would be an undocumented user-visible judgement, which the neutral
// observation rules forbid.
func TestWalkCostIsReportedWithoutJudgingIt(t *testing.T) {
	for _, elapsed := range []int64{0, 9, 750, 1173, 60_000} {
		snap := snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{
			ScanCost: snapshot.TranscriptScanCost{WalkMeasured: true, ElapsedMs: elapsed},
		}}
		diagnostics := buildDiagnosticsSnapshot(snap, time.Now())
		cost := requireDiagnosticBaseline(t, diagnostics.Baselines, "evidence_walk_cost")
		if cost.Status != "observed" {
			t.Errorf("a %dms walk reported status %q; a measured cost is known, not good or bad", elapsed, cost.Status)
		}
		for _, signal := range diagnostics.EvidenceGaps {
			if strings.Contains(signal.Kind, "scan_expensive") || strings.Contains(signal.Kind, "walk_cost") {
				t.Errorf("a %dms walk raised %q; walk cost has no documented budget to breach", elapsed, signal.Kind)
			}
		}
	}
}

func hasDiagnosticSignal(items []snapshot.DiagnosticSignalSnapshot, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func hasDiagnosticCapability(items []snapshot.DiagnosticCapabilitySnapshot, key, status string) bool {
	for _, item := range items {
		if item.Key == key && item.Status == status {
			return true
		}
	}
	return false
}

func requireDiagnosticBaseline(t *testing.T, items []snapshot.DiagnosticBaselineSnapshot, key string) snapshot.DiagnosticBaselineSnapshot {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("missing diagnostic baseline %s in %+v", key, items)
	return snapshot.DiagnosticBaselineSnapshot{}
}
