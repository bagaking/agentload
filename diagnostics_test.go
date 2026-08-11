package main

import (
	"slices"
	"testing"
	"time"
)

func TestDefaultMetricRegistryContainsCoreFamilies(t *testing.T) {
	registry := defaultMetricRegistry()
	keys := map[string]MetricRegistryEntry{}
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
	snapshot := Snapshot{
		Current: CurrentMetrics{PIDConcurrency: 4, SessionConcurrency: 2, ActiveBurstConcurrency: 0},
		Summary: SnapshotSummary{MappedProcesses: 2, UnmappedProcesses: 2, MappingCoveragePct: 50},
		CoordinationRisk: CoordinationRiskSnapshot{
			LowConfidenceSessionCount: 1,
			Signals: []RiskSignalSnapshot{
				{Kind: "duplicate_overlap", Severity: "warn", Evidence: "2 sessions overlap"},
			},
		},
		TranscriptStats: TranscriptStats{DeferredFiles: 3, Errors: []string{"bad trace"}},
		SystemResources: SystemResourceSnapshot{Supported: true, CPUPercent: 91, MemoryUsedPct: 72, Notes: []string{"Network counters are unavailable."}},
		LiveSessions: []LiveSessionSnapshot{
			{SessionID: "s1"},
			{SessionID: "s2", TokenUsage: &TokenUsage{InputTokens: 10, OutputTokens: 2}, TokenUsageSource: "transcript_usage", TokenUsageConfidence: "measured"},
		},
	}

	diagnostics := buildDiagnosticsSnapshot(snapshot, now)

	if diagnostics.GeneratedAt == "" || diagnostics.Export.Endpoint != "/api/diagnostic-export" {
		t.Fatalf("expected generated diagnostics with export contract, got %+v", diagnostics)
	}
	if !hasDiagnosticSignal(diagnostics.AnomalySignals, "duplicate_overlap") || !hasDiagnosticSignal(diagnostics.AnomalySignals, "system_cpu_pressure") {
		t.Fatalf("expected risk and system anomaly signals, got %+v", diagnostics.AnomalySignals)
	}
	for _, kind := range []string{"unmapped_processes", "low_confidence_sessions", "deferred_transcript_scan", "transcript_parse_errors", "system_resource_sampling_notes", "system_resource_rates_pending"} {
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

func TestDiagnosticBaselinesKeepEmptyMappingAndMeasuredTokensHonest(t *testing.T) {
	snapshot := Snapshot{
		Current: CurrentMetrics{PIDConcurrency: 0, SessionConcurrency: 2},
		Summary: SnapshotSummary{},
		LiveSessions: []LiveSessionSnapshot{
			{SessionID: "estimated", TokenUsage: &TokenUsage{InputTokens: 10}, TokenUsageSource: "runtime_adapter", TokenUsageConfidence: "estimated"},
			{SessionID: "measured", TokenUsage: &TokenUsage{InputTokens: 4}, TokenUsageSource: "transcript_usage", TokenUsageConfidence: "measured"},
		},
	}
	diagnostics := buildDiagnosticsSnapshot(snapshot, time.Now())
	mapping := requireDiagnosticBaseline(t, diagnostics.Baselines, "mapping_coverage")
	if mapping.Status != "empty" || mapping.Value != "no visible PIDs" {
		t.Fatalf("expected empty mapping baseline for no visible pids, got %+v", mapping)
	}
	tokens := requireDiagnosticBaseline(t, diagnostics.Baselines, "token_measured_sessions")
	if tokens.Value != "1 of 2" || tokens.Status != "partial" {
		t.Fatalf("expected only measured transcript token usage to count, got %+v", tokens)
	}
}

func hasDiagnosticSignal(items []DiagnosticSignalSnapshot, kind string) bool {
	for _, item := range items {
		if item.Kind == kind {
			return true
		}
	}
	return false
}

func hasDiagnosticCapability(items []DiagnosticCapabilitySnapshot, key, status string) bool {
	for _, item := range items {
		if item.Key == key && item.Status == status {
			return true
		}
	}
	return false
}

func requireDiagnosticBaseline(t *testing.T, items []DiagnosticBaselineSnapshot, key string) DiagnosticBaselineSnapshot {
	t.Helper()
	for _, item := range items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("missing diagnostic baseline %s in %+v", key, items)
	return DiagnosticBaselineSnapshot{}
}
