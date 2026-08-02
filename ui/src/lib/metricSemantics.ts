import type { RoleCounts } from "../types/app";
import type { CurrentMetrics, LiveSession, LiveTokenRateSample, LiveTokenRateState, Snapshot, SnapshotSummary } from "../types/snapshot";
import type { TrendLane, TrendPoint } from "../trend/types";

export type SessionRole = "main" | "subagent" | "unknown";
export type ProcessResourceTotals = { cpu: number; memory: number };

export function normalizedLiveTokenRateState(sample?: LiveTokenRateSample): LiveTokenRateState {
  const state = String(sample?.state || "no_data").trim().toLowerCase();
  if (state === "live" || state === "zero" || state === "stale" || state === "unavailable") return state;
  return "no_data";
}

export function liveTokenRateValue(sample?: LiveTokenRateSample): number | null {
  const state = normalizedLiveTokenRateState(sample);
  const value = sample?.output_tokens_per_second;
  if ((state !== "live" && state !== "zero") || typeof value !== "number" || !Number.isFinite(value) || value < 0) return null;
  return value;
}

export function projectLiveTokenRateValue(sample: LiveTokenRateSample | undefined, project?: string): number | null {
  if (liveTokenRateValue(sample) === null || !Array.isArray(sample?.projects)) return null;
  const key = String(project || "unassigned").trim() || "unassigned";
  const item = sample.projects.find((candidate) => String(candidate.project || "unassigned").trim() === key);
  if (!item) return 0;
  const value = item.output_tokens_per_second;
  return typeof value === "number" && Number.isFinite(value) && value >= 0 ? value : null;
}

export function normalizedRole(role?: string): SessionRole {
  const value = String(role || "").trim().toLowerCase();
  if (value === "main" || value === "main_agent" || value === "user") return "main";
  if (value === "sub" || value === "subagent" || value === "agent") return "subagent";
  return "unknown";
}

export function sessionHasRecentMovement(session: LiveSession): boolean {
  return Boolean(session.active_burst);
}

export function sessionNeedsHumanReview(session: LiveSession): boolean {
  // Backend needs_review is the source of truth; the heuristic below only
  // covers snapshots from older backends that predate the field.
  if (typeof session.needs_review === "boolean") return session.needs_review;
  const freshness = String(session.freshness || "").trim().toLowerCase();
  if (normalizedRole(session.session_role) !== "main") return false;
  if (sessionHasRecentMovement(session)) return false;
  return freshness === "idle" || freshness === "stale";
}

export function snapshotHumanReviewSessions(snapshot?: Snapshot | null): LiveSession[] {
  return (snapshot?.live_sessions ?? []).filter(sessionNeedsHumanReview);
}

export function sessionHumanReviewCount(sessions: LiveSession[]): number {
  return sessions.reduce((total, session) => total + (sessionNeedsHumanReview(session) ? 1 : 0), 0);
}

export function projectRecentMovementCount(project: { active_burst_count?: number }): number {
  return project.active_burst_count ?? 0;
}

export function projectHasRecentMovement(project: { active_burst_count?: number }): boolean {
  return projectRecentMovementCount(project) > 0;
}

export function sessionProcessPressure(session: LiveSession): number {
  return session.process_count ?? 0;
}

export function projectProcessPressureCount(project: { process_count?: number }): number {
  return project.process_count ?? 0;
}

export function projectKnownSessionCount(project: { session_count?: number }): number {
  return project.session_count ?? 0;
}

export function toolRecentMovementCount(tool: { active_burst_count?: number }): number {
  return tool.active_burst_count ?? 0;
}

export function toolKnownSessionCount(tool: { session_count?: number }): number {
  return tool.session_count ?? 0;
}

export function projectRoleCounts(project: { main_agent_sessions?: number; subagent_sessions?: number; unknown_role_sessions?: number; session_count?: number; active_burst_count?: number }, sessions: LiveSession[]): RoleCounts {
  const counts: RoleCounts = {
    main: project.main_agent_sessions ?? 0,
    sub: project.subagent_sessions ?? 0,
    unknown: project.unknown_role_sessions ?? 0,
    total: project.session_count ?? 0,
    activeMain: 0,
    activeSub: 0,
    activeUnknown: 0,
    activeTotal: project.active_burst_count ?? 0,
  };
  if (!sessions.length) return counts;

  sessions.forEach((session) => {
    const role = normalizedRole(session.session_role);
    if (sessionHasRecentMovement(session)) {
      if (role === "main") counts.activeMain++;
      else if (role === "subagent") counts.activeSub++;
      else counts.activeUnknown++;
    }
  });

  const activeTotal = projectRecentMovementCount(project);
  const activeKnown = counts.activeMain + counts.activeSub + counts.activeUnknown;
  if (activeKnown < activeTotal) {
    counts.activeUnknown += activeTotal - activeKnown;
  } else if (activeKnown > activeTotal) {
    let overflow = activeKnown - activeTotal;
    const trimUnknown = Math.min(counts.activeUnknown, overflow);
    counts.activeUnknown -= trimUnknown;
    overflow -= trimUnknown;
    const trimSub = Math.min(counts.activeSub, overflow);
    counts.activeSub -= trimSub;
    overflow -= trimSub;
    counts.activeMain = Math.max(0, counts.activeMain - overflow);
  }
  counts.activeTotal = activeTotal;
  return counts;
}

export function currentRecentMovementCount(current?: CurrentMetrics): number {
  return current?.active_burst_concurrency ?? 0;
}

export function currentKnownSessionCount(current?: CurrentMetrics): number {
  return current?.session_concurrency ?? 0;
}

export function currentProcessPressureCount(current?: CurrentMetrics): number {
  return current?.pid_concurrency ?? 0;
}

export function currentHasRecentMovement(current?: CurrentMetrics): boolean {
  return currentRecentMovementCount(current) > 0;
}

export function currentHasAnyMetric(current?: CurrentMetrics): boolean {
  return typeof current?.active_burst_concurrency === "number" || typeof current?.session_concurrency === "number" || typeof current?.pid_concurrency === "number";
}

export function summaryMappedProcessCount(summary?: SnapshotSummary): number {
  return summary?.mapped_processes ?? 0;
}

export function summaryUnmappedProcessCount(summary?: SnapshotSummary): number {
  return summary?.unmapped_processes ?? 0;
}

export function summaryMappingCoveragePct(summary?: SnapshotSummary): number {
  return summary?.mapping_coverage_pct ?? 0;
}

export function projectProcessResources(snapshot: Snapshot, sessions: LiveSession[]): ProcessResourceTotals {
  const sessionKeys = new Set(sessions.map((session) => `${session.tool || ""}\x00${session.session_id || ""}`));
  const sessionIDs = new Set(sessions.map((session) => session.session_id || "").filter(Boolean));
  let cpu = 0;
  let memory = 0;
  for (const process of snapshot.live_processes ?? []) {
    const matched = (process.mapped_session_evidence ?? []).some((evidence) => sessionKeys.has(`${evidence.tool || process.tool || ""}\x00${evidence.session_id || ""}`))
      || (process.session_ids ?? []).some((id) => sessionIDs.has(id));
    if (!matched) continue;
    cpu += process.cpu_percent ?? 0;
    memory += process.memory_bytes ?? 0;
  }
  return { cpu, memory };
}

export function trendPrimaryValue(lane: TrendLane, point: TrendPoint): number | null {
  const value = lane === "history" ? point.active_burst_concurrency : point.pid_concurrency;
  return typeof value === "number" && Number.isFinite(value) ? value : null;
}

export function trendContextSessionValue(point: TrendPoint): number | null {
  return typeof point.session_concurrency === "number" && Number.isFinite(point.session_concurrency) ? point.session_concurrency : null;
}

export function trendMappingCoverageValue(point: TrendPoint): number | null {
  return typeof point.mapping_coverage_pct === "number" && Number.isFinite(point.mapping_coverage_pct) ? point.mapping_coverage_pct : null;
}

export function trendMappedProcessCount(point: TrendPoint): number | null {
  return typeof point.mapped_processes === "number" && Number.isFinite(point.mapped_processes) ? point.mapped_processes : null;
}

export function trendUnmappedProcessCount(point: TrendPoint): number | null {
  return typeof point.unmapped_processes === "number" && Number.isFinite(point.unmapped_processes) ? point.unmapped_processes : null;
}
