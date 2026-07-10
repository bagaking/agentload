package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func buildDiagnosticsSnapshot(snapshot Snapshot, now time.Time) DiagnosticSnapshot {
	out := DiagnosticSnapshot{
		GeneratedAt: now.Format(time.RFC3339Nano),
		Export:      diagnosticExportSummary(),
	}
	out.AnomalySignals = buildDiagnosticAnomalySignals(snapshot)
	out.EvidenceGaps = buildDiagnosticEvidenceGaps(snapshot)
	out.Baselines = buildDiagnosticBaselines(snapshot)
	out.Capabilities = buildDiagnosticCapabilities(snapshot)
	return out
}

func diagnosticExportSummary() DiagnosticExportSummary {
	return DiagnosticExportSummary{
		Endpoint:  "/api/diagnostic-export",
		Redaction: "sanitized snapshot; local roots, history paths, transcript paths, bundle paths, and command values are omitted or reduced to identity labels",
		OmittedFields: []string{
			"raw prompts",
			"absolute local paths",
			"full command arguments",
			"environment variables",
			"transcript file paths",
			"app bundle paths",
		},
	}
}

func buildDiagnosticExport(snapshot Snapshot, now time.Time) DiagnosticExportSnapshot {
	if snapshot.Diagnostics.GeneratedAt == "" {
		snapshot.Diagnostics = buildDiagnosticsSnapshot(snapshot, now)
	}
	return DiagnosticExportSnapshot{
		FormatVersion: 1,
		GeneratedAt:   now.Format(time.RFC3339Nano),
		Snapshot:      snapshot,
		OmittedFields: append([]string(nil), diagnosticExportSummary().OmittedFields...),
		Notes: []string{
			"Diagnostic export is generated from sanitized local metadata.",
			"Unavailable metrics are preserved as unavailable or absent; they are not converted to zero.",
		},
	}
}

func buildDiagnosticAnomalySignals(snapshot Snapshot) []DiagnosticSignalSnapshot {
	signals := make([]DiagnosticSignalSnapshot, 0, len(snapshot.CoordinationRisk.Signals)+4)
	for _, risk := range snapshot.CoordinationRisk.Signals {
		signals = append(signals, DiagnosticSignalSnapshot{
			Kind:      risk.Kind,
			Severity:  normalizeDiagnosticSeverity(risk.Severity),
			Title:     diagnosticTitleForRisk(risk.Kind),
			Evidence:  risk.Evidence,
			MetricKey: diagnosticMetricForRisk(risk.Kind),
			Source:    "coordination_risk",
		})
	}
	if snapshot.SystemResources.Supported {
		if snapshot.SystemResources.CPUPercent >= 85 {
			signals = append(signals, DiagnosticSignalSnapshot{
				Kind:      "system_cpu_pressure",
				Severity:  "warn",
				Title:     "High system CPU pressure",
				Detail:    "Whole-machine CPU is high. This is system context, not agent attribution by itself.",
				Evidence:  fmt.Sprintf("%.2f%% system CPU", snapshot.SystemResources.CPUPercent),
				MetricKey: "system_resources",
				Source:    "system_resources",
			})
		}
		if snapshot.SystemResources.MemoryUsedPct >= 85 {
			signals = append(signals, DiagnosticSignalSnapshot{
				Kind:      "system_memory_pressure",
				Severity:  "warn",
				Title:     "High memory pressure",
				Detail:    "Whole-machine memory usage is high. Use the System process rows to find contributing local processes.",
				Evidence:  fmt.Sprintf("%.2f%% memory used", snapshot.SystemResources.MemoryUsedPct),
				MetricKey: "system_resources",
				Source:    "system_resources",
			})
		}
		if snapshot.SystemResources.NetworkPacketIssuePct > 1 {
			signals = append(signals, DiagnosticSignalSnapshot{
				Kind:      "network_packet_issue",
				Severity:  "info",
				Title:     "Local network packet issue counter moved",
				Detail:    "This is a local interface issue rate, not an end-to-end internet packet-loss measurement.",
				Evidence:  fmt.Sprintf("%.2f%% local packet issue rate", snapshot.SystemResources.NetworkPacketIssuePct),
				MetricKey: "system_resources",
				Source:    "system_resources",
			})
		}
	}
	sortDiagnosticSignals(signals)
	return signals
}

func buildDiagnosticEvidenceGaps(snapshot Snapshot) []DiagnosticSignalSnapshot {
	gaps := []DiagnosticSignalSnapshot{}
	if snapshot.Summary.UnmappedProcesses > 0 {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "unmapped_processes",
			Severity:  "warn",
			Title:     "Visible processes without session mapping",
			Detail:    "These PIDs count as runtime pressure but not confirmed sessions.",
			Evidence:  fmt.Sprintf("%d unmapped of %d visible PIDs", snapshot.Summary.UnmappedProcesses, snapshot.Current.PIDConcurrency),
			MetricKey: "process_pressure",
			Source:    "live_processes",
		})
	}
	if snapshot.CoordinationRisk.LowConfidenceSessionCount > 0 {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "low_confidence_sessions",
			Severity:  "warn",
			Title:     "Low-confidence session evidence",
			Detail:    "Some sessions are present but have weak mapping, timing, or attribution evidence.",
			Evidence:  fmt.Sprintf("%d low-confidence or missing-transcript sessions", snapshot.CoordinationRisk.LowConfidenceSessionCount),
			MetricKey: "known_sessions",
			Source:    "live_sessions",
		})
	}
	if snapshot.TranscriptStats.DeferredFiles > 0 || snapshot.TranscriptStats.HistoricalScanDeferred {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "deferred_transcript_scan",
			Severity:  "info",
			Title:     "Transcript scan deferred",
			Detail:    "Some historical files were deferred, so long-range history can lag behind current evidence.",
			Evidence:  fmt.Sprintf("%d deferred files", snapshot.TranscriptStats.DeferredFiles),
			MetricKey: "recent_movement",
			Source:    "transcript_stats",
		})
	}
	if len(snapshot.TranscriptStats.Errors) > 0 {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "transcript_parse_errors",
			Severity:  "warn",
			Title:     "Transcript parse errors",
			Detail:    "Some local evidence files could not be parsed.",
			Evidence:  fmt.Sprintf("%d parse errors", len(snapshot.TranscriptStats.Errors)),
			MetricKey: "known_sessions",
			Source:    "transcript_stats",
		})
	}
	if !snapshot.SystemResources.Supported {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "system_resources_unavailable",
			Severity:  "info",
			Title:     "System resource sampling unavailable",
			Detail:    "Whole-machine CPU, memory, disk, or network counters are unavailable on this run.",
			MetricKey: "system_resources",
			Source:    "system_resources",
		})
	}
	if snapshot.SystemResources.Supported && len(snapshot.SystemResources.Notes) > 0 {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "system_resource_sampling_notes",
			Severity:  "info",
			Title:     "System resource sampling has caveats",
			Detail:    "Some whole-machine counters are unavailable or pending. Treat absent rates as unavailable, not zero.",
			Evidence:  fmt.Sprintf("%d sampler notes", len(snapshot.SystemResources.Notes)),
			MetricKey: "system_resources",
			Source:    "system_resources",
		})
	}
	if snapshot.SystemResources.Supported && snapshot.SystemResources.SampleIntervalSeconds == 0 {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "system_resource_rates_pending",
			Severity:  "info",
			Title:     "System resource rates need another sample",
			Detail:    "CPU and network rates require two samples. The first sample is not a measured zero.",
			MetricKey: "system_resources",
			Source:    "system_resources",
		})
	}
	if countTokenMeasuredSessions(snapshot.LiveSessions) == 0 && len(snapshot.LiveSessions) > 0 {
		gaps = append(gaps, DiagnosticSignalSnapshot{
			Kind:      "token_usage_unavailable",
			Severity:  "info",
			Title:     "Token usage unavailable",
			Detail:    "No live session currently exposes parsed token usage. This should stay unavailable, not zero.",
			Evidence:  fmt.Sprintf("%d live sessions without measured token usage", len(snapshot.LiveSessions)),
			MetricKey: "token_usage",
			Source:    "live_sessions",
		})
	}
	sortDiagnosticSignals(gaps)
	return gaps
}

func buildDiagnosticBaselines(snapshot Snapshot) []DiagnosticBaselineSnapshot {
	mappingValue, mappingStatus := mappingCoverageBaseline(snapshot)
	measuredTokens := countTokenMeasuredSessions(snapshot.LiveSessions)
	return []DiagnosticBaselineSnapshot{
		{
			Key:       "mapping_coverage",
			Label:     "PID match coverage",
			Value:     mappingValue,
			Status:    mappingStatus,
			Detail:    "Share of visible AI PIDs mapped back to session evidence.",
			MetricKey: "process_pressure",
		},
		{
			Key:       "active_session_ratio",
			Label:     "Recent movement share",
			Value:     fmt.Sprintf("%d of %d", snapshot.Current.ActiveBurstConcurrency, snapshot.Current.SessionConcurrency),
			Status:    recentMovementStatus(snapshot.Current.ActiveBurstConcurrency, snapshot.Current.SessionConcurrency),
			Detail:    "Recent local-log movement compared with known live sessions.",
			MetricKey: "recent_movement",
		},
		{
			Key:       "low_confidence_sessions",
			Label:     "Low-confidence sessions",
			Value:     fmt.Sprintf("%d", snapshot.CoordinationRisk.LowConfidenceSessionCount),
			Status:    zeroGoodStatus(snapshot.CoordinationRisk.LowConfidenceSessionCount),
			Detail:    "Sessions with weak mapping or missing transcript timing.",
			MetricKey: "known_sessions",
		},
		{
			Key:       "token_measured_sessions",
			Label:     "Measured token sessions",
			Value:     fmt.Sprintf("%d of %d", measuredTokens, len(snapshot.LiveSessions)),
			Status:    tokenStatus(measuredTokens, len(snapshot.LiveSessions)),
			Detail:    "Token usage is counted only when parsed usage fields exist and source/confidence marks it measured.",
			MetricKey: "token_usage",
		},
	}
}

func buildDiagnosticCapabilities(snapshot Snapshot) []DiagnosticCapabilitySnapshot {
	processStatus := "available"
	if len(snapshot.LiveProcesses) == 0 {
		processStatus = "empty"
	}
	transcriptStatus := "available"
	if snapshot.TranscriptStats.ScannedFiles == 0 {
		transcriptStatus = "empty"
	}
	systemStatus := "available"
	if !snapshot.SystemResources.Supported {
		systemStatus = "unavailable"
	}
	runtimeStatus := snapshot.RuntimeTelemetry.Status
	if runtimeStatus == "" {
		runtimeStatus = "not_configured"
	}
	runtimeDetail := snapshot.RuntimeTelemetry.Detail
	if runtimeDetail == "" {
		runtimeDetail = "Adapter seam reserved for future local OpenTelemetry or JSONL runtime events."
	}
	return []DiagnosticCapabilitySnapshot{
		{Key: "passive_process_observer", Label: "Passive process observer", Status: processStatus, Detail: "Local visible AI process rows and resource counters."},
		{Key: "transcript_parser", Label: "Transcript parser", Status: transcriptStatus, Detail: "Local transcript/activity evidence for sessions, roles, timing, and token usage when present."},
		{Key: "system_resources", Label: "System resource sampler", Status: systemStatus, Detail: "Whole-machine CPU, memory, disk, network, and public thermal-pressure state when available."},
		{Key: "runtime_telemetry_adapter", Label: "Runtime telemetry adapter", Status: runtimeStatus, Detail: runtimeDetail},
		{Key: "diagnostic_export", Label: "Safe diagnostic export", Status: "available", Detail: "Sanitized local evidence bundle available from /api/diagnostic-export."},
	}
}

func countTokenMeasuredSessions(sessions []LiveSessionSnapshot) int {
	count := 0
	for _, session := range sessions {
		if hasMeasuredTokenUsage(session) {
			count++
		}
	}
	return count
}

func hasMeasuredTokenUsage(session LiveSessionSnapshot) bool {
	return session.TokenUsage != nil &&
		!session.TokenUsage.Empty() &&
		strings.EqualFold(strings.TrimSpace(session.TokenUsageSource), "transcript_usage") &&
		strings.EqualFold(strings.TrimSpace(session.TokenUsageConfidence), "measured")
}

func mappingCoverageBaseline(snapshot Snapshot) (string, string) {
	visible := snapshot.Summary.MappedProcesses + snapshot.Summary.UnmappedProcesses
	if visible == 0 && snapshot.Current.PIDConcurrency == 0 {
		return "no visible PIDs", "empty"
	}
	return fmt.Sprintf("%.2f%%", snapshot.Summary.MappingCoveragePct), baselineStatus(snapshot.Summary.MappingCoveragePct, 80, 60)
}

func baselineStatus(value, okAt, warnAt float64) string {
	switch {
	case value >= okAt:
		return "ok"
	case value >= warnAt:
		return "watch"
	default:
		return "warn"
	}
}

func recentMovementStatus(active, total int) string {
	if total == 0 {
		return "empty"
	}
	if active == 0 {
		return "idle"
	}
	return "ok"
}

func zeroGoodStatus(value int) string {
	if value == 0 {
		return "ok"
	}
	return "watch"
}

func tokenStatus(measured, total int) string {
	if total == 0 {
		return "empty"
	}
	if measured == 0 {
		return "unavailable"
	}
	if measured == total {
		return "ok"
	}
	return "partial"
}

func normalizeDiagnosticSeverity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical", "error", "warn", "warning":
		return "warn"
	case "ok", "observed", "info":
		return "info"
	default:
		return "info"
	}
}

func diagnosticTitleForRisk(kind string) string {
	switch strings.TrimSpace(kind) {
	case "unmatched_processes":
		return "Unmatched process pressure"
	case "low_confidence_mapping":
		return "Low-confidence mapping"
	case "duplicate_overlap":
		return "Possible duplicate or overlap"
	case "project_spread":
		return "Wide project spread"
	default:
		return strings.ReplaceAll(strings.TrimSpace(kind), "_", " ")
	}
}

func diagnosticMetricForRisk(kind string) string {
	switch strings.TrimSpace(kind) {
	case "unmatched_processes":
		return "process_pressure"
	case "low_confidence_mapping":
		return "known_sessions"
	case "duplicate_overlap", "project_spread":
		return "role_matrix"
	default:
		return ""
	}
}

func sortDiagnosticSignals(items []DiagnosticSignalSnapshot) {
	sort.SliceStable(items, func(i, j int) bool {
		if diagnosticSeverityRank(items[i].Severity) != diagnosticSeverityRank(items[j].Severity) {
			return diagnosticSeverityRank(items[i].Severity) < diagnosticSeverityRank(items[j].Severity)
		}
		if items[i].Kind != items[j].Kind {
			return items[i].Kind < items[j].Kind
		}
		return items[i].Title < items[j].Title
	})
}

func diagnosticSeverityRank(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "warn":
		return 0
	case "watch":
		return 1
	case "info":
		return 2
	default:
		return 3
	}
}
