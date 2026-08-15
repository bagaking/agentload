package main

import (
	"agentload/internal/snapshot"
	"fmt"
	"sort"
	"strings"
	"time"
)

func buildDiagnosticsSnapshot(snap snapshot.Snapshot, now time.Time) snapshot.DiagnosticSnapshot {
	out := snapshot.DiagnosticSnapshot{
		GeneratedAt: now.Format(time.RFC3339Nano),
		Export:      diagnosticExportSummary(),
	}
	out.Evolution = buildDiagnosticEvolutionInsights(snap)
	out.AnomalySignals = buildDiagnosticAnomalySignals(snap)
	out.EvidenceGaps = buildDiagnosticEvidenceGaps(snap)
	out.Baselines = buildDiagnosticBaselines(snap)
	out.Capabilities = buildDiagnosticCapabilities(snap)
	return out
}

// buildDiagnosticEvolutionInsights is the RSI surface: it converts observed
// session and coordination patterns into hypotheses and small experiments. It
// never scores agent quality because this observer has no task outcome or code
// review ground truth.
func buildDiagnosticEvolutionInsights(snap snapshot.Snapshot) []snapshot.DiagnosticEvolutionInsight {
	insights := make([]snapshot.DiagnosticEvolutionInsight, 0, 3)
	risk := snap.CoordinationRisk
	if risk.StaleSessionCount > 0 || risk.ChurnSessionCount > 0 {
		insights = append(insights, snapshot.DiagnosticEvolutionInsight{
			Key:            "session_hygiene",
			Title:          "Review session hygiene",
			Hypothesis:     "Long-lived or rapidly accumulating sessions may be carrying stale context into later work.",
			Evidence:       fmt.Sprintf("%d stale sessions; %d sessions started in the recent window", risk.StaleSessionCount, risk.ChurnSessionCount),
			EvidenceKey:    "session_hygiene",
			EvidenceValues: map[string]int{"count_a": risk.StaleSessionCount, "count_b": risk.ChurnSessionCount},
			Experiment:     "Review one stale session and one recent session; close or summarize the stale context before the next task.",
			Verification:   "On the next refresh, confirm stale-session count and recent-session movement separately.",
			MetricKey:      "known_sessions",
			Confidence:     "observed",
			Status:         "needs_review",
		})
	}
	if risk.DuplicateOverlapSuspicionCount > 0 || risk.ProjectSpreadCount > 1 {
		insights = append(insights, snapshot.DiagnosticEvolutionInsight{
			Key:            "coordination_shape",
			Title:          "Test coordination shape",
			Hypothesis:     "Overlapping sessions or wide project spread may be adding coordination cost.",
			Evidence:       fmt.Sprintf("%d overlap suspicions across %d projects", risk.DuplicateOverlapSuspicionCount, risk.ProjectSpreadCount),
			EvidenceKey:    "coordination_shape",
			EvidenceValues: map[string]int{"count_a": risk.DuplicateOverlapSuspicionCount, "count_b": risk.ProjectSpreadCount},
			Experiment:     "Run the next comparable task with one explicit owner and one bounded subagent branch.",
			Verification:   "Compare the next run's session count, overlap evidence, and review outcome; Agent Load does not infer outcome quality.",
			MetricKey:      "role_matrix",
			Confidence:     "observed",
			Status:         "needs_review",
		})
	}
	if risk.LowConfidenceSessionCount > 0 || snap.Summary.UnmappedProcesses > 0 {
		insights = append(insights, snapshot.DiagnosticEvolutionInsight{
			Key:            "evidence_boundary",
			Title:          "Repair the evidence boundary first",
			Hypothesis:     "Weak attribution can make an agent change look better or worse than the evidence supports.",
			Evidence:       fmt.Sprintf("%d low-confidence sessions; %d unmapped processes", risk.LowConfidenceSessionCount, snap.Summary.UnmappedProcesses),
			EvidenceKey:    "evidence_boundary",
			EvidenceValues: map[string]int{"count_a": risk.LowConfidenceSessionCount, "count_b": snap.Summary.UnmappedProcesses},
			Experiment:     "Use the session and process inspectors to resolve one unmapped or low-confidence item before changing prompts or delegation rules.",
			Verification:   "Confirm the next snapshot has stronger mapping evidence; do not treat missing evidence as a zero outcome.",
			MetricKey:      "process_pressure",
			Confidence:     "observed",
			Status:         "needs_review",
		})
	}
	if len(insights) == 0 {
		insights = append(insights, snapshot.DiagnosticEvolutionInsight{
			Key:            "baseline_review",
			Title:          "Capture a baseline before changing the agent",
			Hypothesis:     "Current evidence does not identify a specific coordination or attribution problem.",
			Evidence:       fmt.Sprintf("%d live sessions; %d projects; no current RSI trigger", snap.Current.SessionConcurrency, risk.ActiveProjectCount),
			EvidenceKey:    "baseline_review",
			EvidenceValues: map[string]int{"count_a": snap.Current.SessionConcurrency, "count_b": risk.ActiveProjectCount},
			Experiment:     "Choose one repeatable task and record its session shape, token usage, and review result before changing the agent setup.",
			Verification:   "Compare the next run with this baseline using the same task and review criteria.",
			MetricKey:      "known_sessions",
			Confidence:     "observed",
			Status:         "baseline",
		})
	}
	return insights
}

func diagnosticExportSummary() snapshot.DiagnosticExportSummary {
	return snapshot.DiagnosticExportSummary{
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

func buildDiagnosticExport(snap snapshot.Snapshot, now time.Time) snapshot.DiagnosticExportSnapshot {
	if snap.Diagnostics.GeneratedAt == "" {
		snap.Diagnostics = buildDiagnosticsSnapshot(snap, now)
	}
	return snapshot.DiagnosticExportSnapshot{
		FormatVersion: 1,
		GeneratedAt:   now.Format(time.RFC3339Nano),
		Snapshot:      snap,
		OmittedFields: append([]string(nil), diagnosticExportSummary().OmittedFields...),
		Notes: []string{
			"Diagnostic export is generated from sanitized local metadata.",
			"Unavailable metrics are preserved as unavailable or absent; they are not converted to zero.",
		},
	}
}

func buildDiagnosticAnomalySignals(snap snapshot.Snapshot) []snapshot.DiagnosticSignalSnapshot {
	signals := make([]snapshot.DiagnosticSignalSnapshot, 0, len(snap.CoordinationRisk.Signals)+4)
	for _, risk := range snap.CoordinationRisk.Signals {
		signals = append(signals, snapshot.DiagnosticSignalSnapshot{
			Kind:      risk.Kind,
			Severity:  normalizeDiagnosticSeverity(risk.Severity),
			Title:     diagnosticTitleForRisk(risk.Kind),
			Evidence:  risk.Evidence,
			MetricKey: diagnosticMetricForRisk(risk.Kind),
			Source:    "coordination_risk",
		})
	}
	if snap.SystemResources.Supported {
		if snap.SystemResources.CPUPercent >= 85 {
			signals = append(signals, snapshot.DiagnosticSignalSnapshot{
				Kind:      "system_cpu_pressure",
				Severity:  "warn",
				Title:     "High system CPU pressure",
				Detail:    "Whole-machine CPU is high. This is system context, not agent attribution by itself.",
				Evidence:  fmt.Sprintf("%.2f%% system CPU", snap.SystemResources.CPUPercent),
				MetricKey: "system_resources",
				Source:    "system_resources",
			})
		}
		if snap.SystemResources.MemoryUsedPct >= 85 {
			signals = append(signals, snapshot.DiagnosticSignalSnapshot{
				Kind:      "system_memory_pressure",
				Severity:  "warn",
				Title:     "High memory pressure",
				Detail:    "Whole-machine memory usage is high. Use the System process rows to find contributing local processes.",
				Evidence:  fmt.Sprintf("%.2f%% memory used", snap.SystemResources.MemoryUsedPct),
				MetricKey: "system_resources",
				Source:    "system_resources",
			})
		}
		if snap.SystemResources.NetworkPacketIssuePct > 1 {
			signals = append(signals, snapshot.DiagnosticSignalSnapshot{
				Kind:      "network_packet_issue",
				Severity:  "info",
				Title:     "Local network packet issue counter moved",
				Detail:    "This is a local interface issue rate, not an end-to-end internet packet-loss measurement.",
				Evidence:  fmt.Sprintf("%.2f%% local packet issue rate", snap.SystemResources.NetworkPacketIssuePct),
				MetricKey: "system_resources",
				Source:    "system_resources",
			})
		}
	}
	sortDiagnosticSignals(signals)
	return signals
}

func buildDiagnosticEvidenceGaps(snap snapshot.Snapshot) []snapshot.DiagnosticSignalSnapshot {
	gaps := []snapshot.DiagnosticSignalSnapshot{}
	if snap.ProcessStats.Incomplete {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "process_observation_incomplete",
			Severity:  "warn",
			Title:     "Process observation incomplete",
			Detail:    "The operating-system process query failed; process rows are last known evidence when available.",
			Evidence:  "current process sample unavailable",
			MetricKey: "process_pressure",
			Source:    "process_observer",
		})
	}
	if snap.Summary.UnmappedProcesses > 0 {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "unmapped_processes",
			Severity:  "warn",
			Title:     "Visible processes without session mapping",
			Detail:    "These PIDs count as runtime pressure but not confirmed sessions.",
			Evidence:  fmt.Sprintf("%d unmapped of %d visible PIDs", snap.Summary.UnmappedProcesses, snap.Current.PIDConcurrency),
			MetricKey: "process_pressure",
			Source:    "live_processes",
		})
	}
	if snap.CoordinationRisk.LowConfidenceSessionCount > 0 {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "low_confidence_sessions",
			Severity:  "warn",
			Title:     "Low-confidence session evidence",
			Detail:    "Some sessions are present but have weak mapping, timing, or attribution evidence.",
			Evidence:  fmt.Sprintf("%d low-confidence or missing-transcript sessions", snap.CoordinationRisk.LowConfidenceSessionCount),
			MetricKey: "known_sessions",
			Source:    "live_sessions",
		})
	}
	if snap.TranscriptStats.DeferredFiles > 0 || snap.TranscriptStats.HistoricalScanDeferred {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "deferred_transcript_scan",
			Severity:  "info",
			Title:     "Transcript scan deferred",
			Detail:    "Some historical files were deferred, so long-range history can lag behind current evidence.",
			Evidence:  fmt.Sprintf("%d deferred files", snap.TranscriptStats.DeferredFiles),
			MetricKey: "recent_movement",
			Source:    "transcript_stats",
		})
	}
	if snap.TranscriptStats.ScanCost.AgedOutFiles > 0 {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:     "evidence_out_of_horizon",
			Severity: "info",
			Title:    "Evidence excluded by the history horizon",
			// Deferred files are in scope and unscanned; these are out of scope
			// entirely. Without the split, the deferred count above reads as a
			// much larger gap than it is.
			Detail:    "These transcripts exist on disk but fall outside the configured history horizon, so they are out of scope rather than a coverage gap.",
			Evidence:  fmt.Sprintf("%d files older than the history horizon", snap.TranscriptStats.ScanCost.AgedOutFiles),
			MetricKey: "recent_movement",
			Source:    "transcript_stats",
		})
	}
	// A walk is measured once per reconcile and reported until the next one, so
	// its cost is a reading rather than an event. It rides the evidence_walk_cost
	// baseline; there is no signal for it, because no documented refresh budget
	// exists to say which duration is too long.
	if len(snap.TranscriptStats.Errors) > 0 {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "transcript_parse_errors",
			Severity:  "warn",
			Title:     "Transcript parse errors",
			Detail:    "Some local evidence files could not be parsed.",
			Evidence:  fmt.Sprintf("%d parse errors", len(snap.TranscriptStats.Errors)),
			MetricKey: "known_sessions",
			Source:    "transcript_stats",
		})
	}
	if !snap.SystemResources.Supported {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "system_resources_unavailable",
			Severity:  "info",
			Title:     "System resource sampling unavailable",
			Detail:    "Whole-machine CPU, memory, disk, or network counters are unavailable on this run.",
			MetricKey: "system_resources",
			Source:    "system_resources",
		})
	}
	if snap.SystemResources.Supported && len(snap.SystemResources.Notes) > 0 {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "system_resource_sampling_notes",
			Severity:  "info",
			Title:     "System resource sampling has caveats",
			Detail:    "Some whole-machine counters are unavailable or pending. Treat absent rates as unavailable, not zero.",
			Evidence:  fmt.Sprintf("%d sampler notes", len(snap.SystemResources.Notes)),
			MetricKey: "system_resources",
			Source:    "system_resources",
		})
	}
	if snap.SystemResources.Supported && snap.SystemResources.SampleIntervalSeconds == 0 {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "system_resource_rates_pending",
			Severity:  "info",
			Title:     "System resource rates need another sample",
			Detail:    "CPU and network rates require two samples. The first sample is not a measured zero.",
			MetricKey: "system_resources",
			Source:    "system_resources",
		})
	}
	if countTokenMeasuredSessions(snap.LiveSessions) == 0 && len(snap.LiveSessions) > 0 {
		gaps = append(gaps, snapshot.DiagnosticSignalSnapshot{
			Kind:      "token_usage_unavailable",
			Severity:  "info",
			Title:     "Token usage unavailable",
			Detail:    "No live session currently exposes parsed token usage. This should stay unavailable, not zero.",
			Evidence:  fmt.Sprintf("%d live sessions without measured token usage", len(snap.LiveSessions)),
			MetricKey: "token_usage",
			Source:    "live_sessions",
		})
	}
	sortDiagnosticSignals(gaps)
	return gaps
}

func buildDiagnosticBaselines(snap snapshot.Snapshot) []snapshot.DiagnosticBaselineSnapshot {
	mappingValue, mappingStatus := mappingCoverageBaseline(snap)
	measuredTokens := countTokenMeasuredSessions(snap.LiveSessions)
	return []snapshot.DiagnosticBaselineSnapshot{
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
			Value:     fmt.Sprintf("%d of %d", snap.Current.ActiveBurstConcurrency, snap.Current.SessionConcurrency),
			Status:    recentMovementStatus(snap.Current.ActiveBurstConcurrency, snap.Current.SessionConcurrency),
			Detail:    "Recent local-log movement compared with known live sessions.",
			MetricKey: "recent_movement",
		},
		{
			Key:       "low_confidence_sessions",
			Label:     "Low-confidence sessions",
			Value:     fmt.Sprintf("%d", snap.CoordinationRisk.LowConfidenceSessionCount),
			Status:    zeroGoodStatus(snap.CoordinationRisk.LowConfidenceSessionCount),
			Detail:    "Sessions with weak mapping or missing transcript timing.",
			MetricKey: "known_sessions",
		},
		{
			Key:       "token_measured_sessions",
			Label:     "Measured token sessions",
			Value:     fmt.Sprintf("%d of %d", measuredTokens, len(snap.LiveSessions)),
			Status:    tokenStatus(measuredTokens, len(snap.LiveSessions)),
			Detail:    "Token usage is counted only when parsed usage fields exist and source/confidence marks it measured.",
			MetricKey: "token_usage",
		},
		{
			Key:       "evidence_walk_cost",
			Label:     "Evidence walk cost",
			Value:     scanCostValue(snap.TranscriptStats.ScanCost),
			Status:    scanCostStatus(snap.TranscriptStats.ScanCost),
			Detail:    "Duration of the last full evidence walk. The index reconciles about once per process, so this is the last measured walk, and stays no data until one has run.",
			MetricKey: "recent_movement",
		},
	}
}

// scanCostValue keeps the zeroing trap in evidence_index.go from surfacing as a
// measurement: an unmeasured walk has no duration, not a duration of zero.
func scanCostValue(cost snapshot.TranscriptScanCost) string {
	if !cost.WalkMeasured {
		return liveTokenRateStateNoData
	}
	return fmt.Sprintf("%dms", cost.ElapsedMs)
}

// scanCostStatus reports whether the walk cost is known, not whether it is
// acceptable. Judging it would need a documented refresh budget (M02_S02), and
// picking a number here would be an undocumented user-visible threshold.
func scanCostStatus(cost snapshot.TranscriptScanCost) string {
	if !cost.WalkMeasured {
		return "unavailable"
	}
	return "ok"
}

func buildDiagnosticCapabilities(snap snapshot.Snapshot) []snapshot.DiagnosticCapabilitySnapshot {
	processStatus := "available"
	if len(snap.LiveProcesses) == 0 {
		processStatus = "empty"
	}
	transcriptStatus := "available"
	if snap.TranscriptStats.ScannedFiles == 0 {
		transcriptStatus = "empty"
	}
	systemStatus := "available"
	if !snap.SystemResources.Supported {
		systemStatus = "unavailable"
	}
	runtimeStatus := snap.RuntimeTelemetry.Status
	if runtimeStatus == "" {
		runtimeStatus = "not_configured"
	}
	runtimeDetail := snap.RuntimeTelemetry.Detail
	if runtimeDetail == "" {
		runtimeDetail = "Adapter seam reserved for future local OpenTelemetry or JSONL runtime events."
	}
	return []snapshot.DiagnosticCapabilitySnapshot{
		{Key: "passive_process_observer", Label: "Passive process observer", Status: processStatus, Detail: "Local visible AI process rows and resource counters."},
		{Key: "transcript_parser", Label: "Transcript parser", Status: transcriptStatus, Detail: "Local transcript/activity evidence for sessions, roles, timing, and token usage when present."},
		{Key: "system_resources", Label: "System resource sampler", Status: systemStatus, Detail: "Whole-machine CPU, memory, disk, network, and public thermal-pressure state when available."},
		{Key: "runtime_telemetry_adapter", Label: "Runtime telemetry adapter", Status: runtimeStatus, Detail: runtimeDetail},
		{Key: "diagnostic_export", Label: "Safe diagnostic export", Status: "available", Detail: "Sanitized local evidence bundle available from /api/diagnostic-export."},
	}
}

func countTokenMeasuredSessions(sessions []snapshot.LiveSessionSnapshot) int {
	count := 0
	for _, session := range sessions {
		if hasMeasuredTokenUsage(session) {
			count++
		}
	}
	return count
}

func hasMeasuredTokenUsage(session snapshot.LiveSessionSnapshot) bool {
	return session.TokenUsage != nil &&
		!session.TokenUsage.Empty() &&
		strings.EqualFold(strings.TrimSpace(session.TokenUsageSource), "transcript_usage") &&
		strings.EqualFold(strings.TrimSpace(session.TokenUsageConfidence), "measured")
}

func mappingCoverageBaseline(snap snapshot.Snapshot) (string, string) {
	visible := snap.Summary.MappedProcesses + snap.Summary.UnmappedProcesses
	if visible == 0 && snap.Current.PIDConcurrency == 0 {
		return "no visible PIDs", "empty"
	}
	return fmt.Sprintf("%.2f%%", snap.Summary.MappingCoveragePct), baselineStatus(snap.Summary.MappingCoveragePct, 80, 60)
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
		return baselineStatusIdle
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

func sortDiagnosticSignals(items []snapshot.DiagnosticSignalSnapshot) {
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
