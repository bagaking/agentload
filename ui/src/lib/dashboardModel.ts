import { freshnessLabel, normalizedRole, orderedProjects, roleLabel } from "./activityModel";
import { formatAge, formatCPU, formatCopy, formatDateTime, formatMemory, formatPct, safeID, shortID, type Translate } from "./format";
import { currentHasAnyMetric, currentHasRecentMovement, currentKnownSessionCount, currentProcessPressureCount, currentRecentMovementCount, projectHasRecentMovement, projectKnownSessionCount, projectProcessPressureCount, projectRecentMovementCount, sessionHasRecentMovement, sessionProcessPressure, summaryMappedProcessCount, summaryMappingCoveragePct, summaryUnmappedProcessCount, trendContextSessionValue, trendMappedProcessCount, trendPrimaryValue } from "./metricSemantics";
import type { LogTab, RailItem, RailTab, SelectedView, Selection } from "../types/app";
import type { CurrentMetrics, Snapshot, TranscriptStats } from "../types/snapshot";
import type { TrendPoint, TrendSet, TrendWindow } from "../trend/types";

export function resolveSelection(t: Translate, snapshot: Snapshot | null, selection: Selection, brandName: string): SelectedView {
  if (!snapshot || selection.type === "overview") {
    return {
      title: brandName,
      kind: "scan",
      status: snapshot ? "done" : "empty",
      command: "/api/snapshot",
      summary: {},
      details: [],
    };
  }
  if (selection.type === "project") {
    const project = (snapshot.project_focus ?? []).find((item) => safeID(item.project) === selection.id);
    return {
      title: project?.project || t("unassigned"),
      kind: "scan",
      status: project && projectHasRecentMovement(project) ? "running" : "done",
      command: `project:${project?.project || "unassigned"}`,
      summary: {
        sessions: project?.session_count ?? 0,
        active: project ? projectRecentMovementCount(project) : 0,
        processes: project ? projectProcessPressureCount(project) : 0,
      },
      details: [
        `attention_share_pct=${formatPct(project?.attention_share_pct)}`,
        `confidence=${project?.confidence || "unknown"}`,
        `recent_sessions=${project?.recent_session_count ?? 0}`,
        `stale_sessions=${project?.stale_session_count ?? 0}`,
      ],
    };
  }
  if (selection.type === "session") {
    const session = (snapshot.live_sessions ?? []).find((item) => safeID(item.session_id) === selection.id);
    return {
      title: session?.project || shortID(session?.session_id) || t("session"),
      kind: "query",
      status: session && sessionHasRecentMovement(session) ? "running" : "done",
      command: session?.path || `session:${session?.session_id || "unknown"}`,
      summary: {
        tool: session?.tool || "unknown",
        role: session?.session_role || "unknown",
        processes: session ? sessionProcessPressure(session) : 0,
      },
      details: [
        `session_id=${session?.session_id || "unknown"}`,
        `freshness=${session?.freshness || "unknown"}`,
        `mapping_method=${session?.mapping_method || "unknown"}`,
        `confidence=${session?.confidence || "unknown"}`,
      ],
    };
  }
  const process = (snapshot.live_processes ?? []).find((item) => String(item.pid ?? "") === selection.id);
  return {
    title: `${process?.display_name || process?.tool || t("process")} · ${process?.pid ?? t("pid")}`,
    kind: "verify",
    status: (process?.mapped_sessions ?? 0) > 0 ? "done" : "failed",
    command: process?.command || `pid:${process?.pid || "unknown"}`,
    summary: {
      pid: process?.pid ?? 0,
      tool: process?.tool || "unknown",
      mapped: process?.mapped_sessions ?? 0,
      direct: process?.main_sessions ?? 0,
      subagent: process?.subagent_sessions ?? 0,
      cpu: formatCPU(process?.cpu_percent),
      memory: formatMemory(process?.memory_bytes, t),
    },
    details: [
      `session_ids=${(process?.session_ids ?? []).join(",") || "none"}`,
      `role_mix=direct:${process?.main_sessions ?? 0},subagent:${process?.subagent_sessions ?? 0},unknown:${process?.unknown_role_sessions ?? 0}`,
      `resources=${formatCPU(process?.cpu_percent)} cpu, ${formatMemory(process?.memory_bytes, t)}`,
      `host_app=${process?.host_app?.name || "unknown"}`,
    ],
  };
}

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

export function dashboardProjectLead(t: Translate, snapshot: Snapshot): string {
  const top = orderedProjects(snapshot)[0];
  if (!top?.project) return t("unavailable");
  const attention = typeof top.attention_share_pct === "number" ? `${formatPct(top.attention_share_pct)} · ` : "";
  return `${attention}${top.project}`;
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
  if (stats.historical_scan_deferred) {
    return `${t("historicalWalkDeferred")} · ${t("foregroundWindow")} ${window}`;
  }
  if ((stats.deferred_files ?? 0) > 0) {
    return `${stats.deferred_files ?? 0} ${t("deferred")} · ${t("foregroundWindow")} ${window}`;
  }
  return `${t("foregroundWindowOnly")} · ${t("foregroundWindow")} ${window}`;
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
  const firstSignal = risk.signals?.find((signal) => signal.evidence)?.evidence;
  if (firstSignal) return normalizeEvidenceNote(t, firstSignal);
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

export function renderLogText(t: Translate, snapshot: Snapshot, selected: SelectedView, tab: LogTab): string {
  if (tab === "summary") {
    return JSON.stringify(
      {
        selected: selected.title,
        status: selected.status,
        metrics: selected.summary,
        current: snapshot.current,
        summary: snapshot.summary,
      },
      null,
      2,
    );
  }
  if (tab === "evidence") {
    const notes = [...(snapshot.notes ?? []), ...(snapshot.transcript_stats?.errors ?? []), ...selected.details];
    if (!notes.length) return `${t("evidence")}: ${t("none")}`;
    return notes.map((line, index) => `${String(index + 1).padStart(2, "0")}  ${line}`).join("\n");
  }
  const history = bestWindow(snapshot.trends);
  const runtime = bestWindow(snapshot.realtime_trends);
  const points = mergeTrendPoints(history?.points ?? [], runtime?.points ?? []).slice(-18);
  return points
    .map((point) => {
      const at = point.at ? formatDateTime(point.at) : t("unavailable");
      return `${at}  fresh=${trendPrimaryValue("history", point) ?? "-"} sessions=${trendContextSessionValue(point) ?? "-"} pids=${trendPrimaryValue("runtime", point) ?? "-"} mapped=${trendMappedProcessCount(point) ?? "-"}`;
    })
    .join("\n") || `${t("trend")}: ${t("noTrend")}`;
}

export function currentPeerScale(current: CurrentMetrics): number {
  return Math.max(1, currentRecentMovementCount(current), currentKnownSessionCount(current), currentProcessPressureCount(current));
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

function normalizeEvidenceNote(t: Translate, note: string): string {
  const text = String(note || "").trim();
  const unmatched = text.match(/^(\d+)\s+(?:visible\s+)?live processes (?:are not currently matched to local session evidence|have no mapped session)\.?$/i);
  if (unmatched) return formatCopy(t("unmatchedSignalWarning"), { count: unmatched[1] });
  return text;
}

function bestWindow(set?: TrendSet): TrendWindow | undefined {
  return set?.windows?.find((window) => window.range === "1D") ?? set?.windows?.[0];
}

function mergeTrendPoints(history: TrendPoint[], runtime: TrendPoint[]): TrendPoint[] {
  const byAt = new Map<string, TrendPoint>();
  [...history, ...runtime].forEach((point) => {
    const key = point.at || `${byAt.size}`;
    byAt.set(key, { ...(byAt.get(key) ?? {}), ...point, at: key });
  });
  return Array.from(byAt.values()).sort((a, b) => String(a.at).localeCompare(String(b.at)));
}
