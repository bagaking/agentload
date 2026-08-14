import { freshnessLabel, normalizedRole, orderedProjects, roleLabel } from "./activityModel";
import { formatAge, formatCPU, formatCopy, formatMemory, formatPct, safeID, shortID, type Translate } from "./format";
import { currentHasAnyMetric, currentHasRecentMovement, currentKnownSessionCount, currentProcessPressureCount, currentRecentMovementCount, projectHasRecentMovement, projectKnownSessionCount, projectProcessPressureCount, projectRecentMovementCount, sessionHasRecentMovement, summaryMappedProcessCount, summaryMappingCoveragePct, summaryUnmappedProcessCount } from "./metricSemantics";
import type { RailItem, RailTab } from "../types/app";
import type { CoordinationRisk, Snapshot, TranscriptStats } from "../types/snapshot";

export function buildRailItems(t: Translate, snapshot: Snapshot | null, tab: RailTab, query: string): RailItem[] {
  if (!snapshot) return [];
  const needle = query.trim().toLowerCase();
  const filter = (item: RailItem) => !needle || `${item.title} ${item.description} ${item.command} ${item.tags.join(" ")}`.toLowerCase().includes(needle);
  let items: RailItem[];
  if (tab === "projects") {
    items = (snapshot.project_focus ?? []).map((project) => ({
      id: safeID(project.project),
      type: "project",
      kind: "scan",
      title: project.project || t("unassigned"),
      description: `${projectKnownSessionCount(project)} ${t("sessions")} · ${projectProcessPressureCount(project)} ${t("processes")}`,
      command: `attention ${formatPct(project.attention_share_pct)}`,
      status: projectHasRecentMovement(project) ? "active" : "done",
      tags: [`${t("mainShort")} ${project.main_agent_sessions ?? 0}`, `${t("subagentShort")} ${project.subagent_sessions ?? 0}`],
      value: `${projectRecentMovementCount(project)} ${t("fresh")}`,
    }));
  } else if (tab === "sessions") {
    items = (snapshot.live_sessions ?? []).map((session) => ({
      id: safeID(session.session_id),
      type: "session",
      kind: "query",
      title: session.project || shortID(session.session_id) || t("session"),
      description: `${session.tool || t("tool")} · ${roleLabel(t, normalizedRole(session.session_role))} · ${freshnessLabel(t, session.freshness)}`,
      command: session.session_id ? `session:${session.session_id}` : session.path || shortID(session.session_id) || t("session"),
      status: sessionHasRecentMovement(session) ? "active" : "done",
      tags: [session.tool || t("tool"), roleLabel(t, normalizedRole(session.session_role))],
      value: formatAge(session.last_event_age_seconds, t),
    }));
  } else {
    items = (snapshot.live_processes ?? []).map((process) => ({
      id: String(process.pid ?? ""),
      type: "process",
      kind: "verify",
      title: `${process.display_name || process.tool || t("tool")} · ${process.pid ?? t("pid")}`,
      description: `${process.mapped_sessions ?? 0} ${t("mappedSessions")} · ${formatCPU(process.cpu_percent)} ${t("cpu")} · ${formatMemory(process.memory_bytes, t)}`,
      command: process.command || t("process"),
      status: (process.mapped_sessions ?? 0) > 0 ? "done" : "failed",
      tags: [process.tool || t("tool"), process.host_app?.name || t("hostUnknown")],
      value: formatMemory(process.memory_bytes, t),
    }));
  }
  return items.filter(filter);
}

export function currentMeaningPoints(t: Translate, snapshot: Snapshot): string[] {
  const risk = snapshot.coordination_risk ?? {};
  const current = snapshot.current ?? {};
  const summary = snapshot.summary ?? {};
  const points: string[] = [];
  const topProjectShare = risk.top_project_attention_share_pct;
  if (risk.top_project && typeof topProjectShare === "number" && topProjectShare > 0) {
    points.push(formatCopy(t("currentMeaningTopProject"), { project: risk.top_project, pct: formatPct(topProjectShare) }));
  } else if ((risk.active_project_count ?? 0) > 1) {
    points.push(formatCopy(t("currentMeaningProjectSpread"), { count: risk.active_project_count ?? 0 }));
  }
  const unmatched = risk.orphan_process_count ?? summaryUnmappedProcessCount(summary);
  if (unmatched > 0) {
    points.push(formatCopy(t("currentMeaningOrphans"), { count: unmatched }));
  } else if ((risk.low_confidence_session_count ?? 0) > 0) {
    points.push(formatCopy(t("currentMeaningLowConfidence"), { count: risk.low_confidence_session_count ?? 0 }));
  } else if ((risk.stale_session_count ?? 0) > 0) {
    points.push(formatCopy(t("currentMeaningStale"), { count: risk.stale_session_count ?? 0 }));
  }
  const coverage = summaryMappingCoveragePct(summary);
  const hasProcessEvidence = summaryMappedProcessCount(summary) > 0 || summaryUnmappedProcessCount(summary) > 0 || currentProcessPressureCount(current) > 0;
  if (hasProcessEvidence && coverage < 100) {
    points.push(formatCopy(t("currentMeaningCoverage"), { pct: formatPct(coverage) }));
  }
  return points;
}

export function currentMeaningLead(t: Translate, snapshot: Snapshot): string {
  const current = snapshot.current ?? {};
  const hasMetric = currentHasAnyMetric(current);
  if (!hasMetric) return t("currentMeaningIdleLead");
  return formatCopy(t("currentMeaningExactLead"), {
    active: currentRecentMovementCount(current),
    sessions: currentKnownSessionCount(current),
    coverage: formatPct(summaryMappingCoveragePct(snapshot.summary)),
  });
}

export function activeWindowLabel(t: Translate, snapshot: Snapshot): string {
  const seconds = snapshot.config?.idle_gap_seconds;
  if (typeof seconds !== "number" || !Number.isFinite(seconds)) return t("activeWindowUnknown");
  return t("activeWindowDefinition").replace("{window}", formatAge(seconds, t));
}

export function dashboardProjectMeta(t: Translate, snapshot: Snapshot): string {
  const summary = snapshot.summary ?? {};
  const projectCount = summary.project_count ?? snapshot.project_focus?.length ?? 0;
  const hotCount = summary.hot_project_count ?? orderedProjects(snapshot).filter(projectHasRecentMovement).length;
  return `${projectCount} ${t("projects")} / ${hotCount} ${t("active")}`;
}

export function transcriptScanSummary(t: Translate, stats: TranscriptStats, retainedSamples?: number): string {
  const parsed = stats.parsed_files ?? 0;
  const scanned = stats.scanned_files ?? 0;
  const deferred = stats.deferred_files ?? 0;
  const tail = stats.tail_parsed_files ?? 0;
  const retained = typeof retainedSamples === "number" ? ` · ${retainedSamples} ${t("samples")}` : "";
  const boundary = stats.historical_scan_deferred
    ? t("historicalWalkDeferred")
    : deferred > 0
      ? `${deferred} ${t("deferred")}`
      : tail > 0
        ? `${tail} ${t("tail")}`
        : "";
  return `${parsed}/${scanned} ${t("localLogs")}${boundary ? ` · ${boundary}` : ""}${retained}`;
}

export function transcriptScanNote(t: Translate, stats: TranscriptStats): string {
  const window = formatAge(stats.foreground_scan_lookback_seconds, t);
  const cost = scanCostNote(t, stats);
  if (stats.historical_scan_deferred) {
    return `${t("historicalWalkDeferred")} · ${t("foregroundWindow")} ${window}${cost}`;
  }
  if ((stats.deferred_files ?? 0) > 0) {
    return `${stats.deferred_files ?? 0} ${t("deferred")} · ${t("foregroundWindow")} ${window}${cost}`;
  }
  return `${t("foregroundWindowOnly")} · ${t("foregroundWindow")} ${window}${cost}`;
}

// The index reconciles about once per process, so this reports the last walk
// that really ran. Nothing is printed until one has: absent means not measured,
// never a walk that cost 0ms.
function scanCostNote(t: Translate, stats: TranscriptStats): string {
  const cost = stats.scan_cost;
  if (!cost?.walk_measured) return "";
  return ` · ${t("evidenceWalk")} ${cost.elapsed_ms ?? 0}ms / ${cost.pruned_directories ?? 0} ${t("pruned")}`;
}

export function deferredScanValue(t: Translate, stats?: TranscriptStats): string {
  return stats?.historical_scan_deferred ? t("historicalWalkShort") : String(stats?.deferred_files ?? 0);
}

export function mappingHealthText(t: Translate, snapshot: Snapshot): string {
  const summary = snapshot.summary ?? {};
  const current = snapshot.current ?? {};
  return formatCopy(t("processDiagnosticFormula"), {
    pids: currentProcessPressureCount(current),
    mapped: summaryMappedProcessCount(summary),
    unmatched: summaryUnmappedProcessCount(summary),
  });
}

export function primaryEvidenceNote(t: Translate, snapshot: Snapshot): string {
  const risk = snapshot.coordination_risk ?? {};
  const summary = snapshot.summary ?? {};
  const firstSignal = risk.signals?.find((signal) => signal.evidence);
  if (firstSignal) return localizedSignalNote(t, risk, firstSignal) ?? normalizeEvidenceNote(t, firstSignal.evidence ?? "");
  const firstNote = [...(snapshot.notes ?? []), ...(snapshot.transcript_stats?.errors ?? [])].find((note) => note);
  if (firstNote) return normalizeEvidenceNote(t, firstNote);
  const unmapped = risk.orphan_process_count ?? summaryUnmappedProcessCount(summary);
  if (unmapped > 0) return formatCopy(t("unmatchedSignalWarning"), { count: unmapped });
  const lowConfidence = risk.low_confidence_session_count ?? 0;
  if (lowConfidence > 0) return `${lowConfidence} ${t("lowConfidenceEvidence")}`;
  const mapped = summaryMappedProcessCount(summary);
  const pids = currentProcessPressureCount(snapshot.current);
  if (mapped || pids) return `${mapped}/${pids} ${t("processEvidenceMapped")}`;
  return t("noSignals");
}

export function statusTone(snapshot: Snapshot): "active" | "idle" | "warn" {
  if (currentHasRecentMovement(snapshot.current)) return "active";
  if ((snapshot.transcript_stats?.errors?.length ?? 0) > 0) return "warn";
  return "idle";
}

export function metricState(snapshot: Snapshot, t: Translate): string {
  if (currentHasRecentMovement(snapshot.current)) return t("active");
  return t("idle");
}

export function coordinationPostureLabel(snapshot: Snapshot, t: Translate): string {
  const risk = snapshot.coordination_risk ?? {};
  const signals = risk.signals?.length ?? 0;
  if (signals > 0) return t("signalsCount").replace("{count}", String(signals));
  if ((risk.low_confidence_session_count ?? 0) > 0) return t("lowConfidenceCount").replace("{count}", String(risk.low_confidence_session_count));
  return t("fresh");
}

// Signals carry a structured kind while the matching count lives on the risk
// payload, so known kinds localize without parsing the English evidence text.
function localizedSignalNote(t: Translate, risk: CoordinationRisk, signal: { kind?: string; evidence?: string }): string | null {
  if (signal.kind === "unmatched_processes" && typeof risk.orphan_process_count === "number") {
    return formatCopy(t("unmatchedSignalWarning"), { count: risk.orphan_process_count });
  }
  return null;
}

function normalizeEvidenceNote(t: Translate, note: string): string {
  const text = String(note || "").trim();
  const unmatched = text.match(/^(\d+)\s+(?:visible\s+)?live processes (?:are not currently matched to local session evidence|have no mapped session)\.?$/i);
  if (unmatched) return formatCopy(t("unmatchedSignalWarning"), { count: unmatched[1] });
  return text;
}
