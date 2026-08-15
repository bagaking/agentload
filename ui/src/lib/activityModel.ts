import { formatAge, formatPct, formatTokenCount, formatTokenUsageSummary, shortID, tokenUsageHasValue, type Translate } from "./format";
import { normalizedRole, projectLiveTokenRateValue, projectRecentMovementCount, sessionHasRecentMovement, sessionNeedsHumanReview, type SessionRole } from "./metricSemantics";
import type { ToolSessionGroup, SessionWorktreeGroup } from "../types/app";
import type { LiveSession, LiveTokenRateSample, ProjectSnapshot, Snapshot } from "../types/snapshot";

export type EvidenceItem = { label: string; value: string; tone?: string };
export { normalizedRole, type SessionRole } from "./metricSemantics";

// Throughput is what the ranking is actually about, so a project producing
// tokens right now outranks one that is merely open. The rate is only a
// tiebreaker input when it is measured — when the sampler has no value the
// order falls back to activity, exactly as before.
export function orderedProjects(snapshot: Snapshot, liveTokenRate?: LiveTokenRateSample): ProjectSnapshot[] {
  return [...(snapshot.project_focus ?? [])].sort((a, b) => {
    const rateA = projectLiveTokenRateValue(liveTokenRate, a.project);
    const rateB = projectLiveTokenRateValue(liveTokenRate, b.project);
    if (rateA !== null && rateB !== null && rateA !== rateB) return rateB - rateA;
    const activeDelta = projectRecentMovementCount(b) - projectRecentMovementCount(a);
    if (activeDelta) return activeDelta;
    const attentionDelta = (b.attention_share_pct ?? 0) - (a.attention_share_pct ?? 0);
    if (attentionDelta) return attentionDelta;
    return String(a.project || "").localeCompare(String(b.project || ""));
  });
}

export function projectKey(project?: string): string {
  return String(project || "unassigned").trim() || "unassigned";
}

export function sessionsForProject(snapshot: Snapshot, project: ProjectSnapshot): LiveSession[] {
  const key = projectKey(project.project).toLowerCase();
  return [...(snapshot.live_sessions ?? [])]
    .filter((session) => projectKey(session.project).toLowerCase() === key)
    .sort(compareSessionsByFreshness);
}

// Worktrees roll up to the project body (the backend attributes them there),
// so within a project the sessions still split by worktree. The main checkout
// always sorts first; worktrees follow by name so the row order is stable
// across refreshes.
export function groupSessionsByWorktree(sessions: LiveSession[]): SessionWorktreeGroup[] {
  const groups = new Map<string, LiveSession[]>();
  sessions.forEach((session) => {
    const key = String(session.worktree || "").trim();
    const existing = groups.get(key);
    if (existing) existing.push(session);
    else groups.set(key, [session]);
  });
  return [...groups.entries()]
    .map(([worktree, members]) => ({
      worktree,
      sessions: members,
      activeCount: members.filter(sessionHasRecentMovement).length,
    }))
    .sort((a, b) => {
      if (!a.worktree !== !b.worktree) return a.worktree ? 1 : -1;
      return a.worktree.localeCompare(b.worktree);
    });
}

export function projectEvidenceItems(t: Translate, project: ProjectSnapshot, compact: boolean): EvidenceItem[] {
  const stale = project.stale_session_count ?? 0;
  const recent = project.recent_session_count ?? 0;
  const tokenItem =
    project.token_usage && tokenUsageHasValue(project.token_usage)
      ? { label: t("tokenUsage"), value: formatTokenUsageSummary(project.token_usage, t), tone: "good" }
      : null;
  const tokenSourceItem = project.token_usage_source
    ? { label: t("tokenSource"), value: tokenUsageProvenanceLabel(t, project.token_usage_source, project.token_usage_confidence), tone: project.token_usage_confidence === "measured" ? "good" : "" }
    : null;
  const modelItem = modelUsageLabel(t, project.model_usage);
  const items = [
    { label: t("attention"), value: formatPct(project.attention_share_pct), tone: (project.attention_share_pct ?? 0) > 50 ? "active" : "" },
    tokenItem,
    tokenSourceItem,
    modelItem,
    { label: t("basis"), value: project.attention_basis || t("unavailable") },
    { label: t("confidence"), value: confidenceLabel(t, project.confidence), tone: project.confidence === "high" ? "good" : "" },
    { label: t("attribution"), value: confidenceLabel(t, project.project_attribution_confidence), tone: project.project_attribution_confidence === "high" ? "good" : "" },
    { label: t("recent"), value: String(recent), tone: recent > 0 ? "active" : "" },
    { label: t("stale"), value: String(stale), tone: stale > 0 ? "warn" : "" },
    { label: t("lastEvent"), value: formatAge(project.last_event_age_seconds, t) },
  ].filter(Boolean) as EvidenceItem[];
  return compact ? items.slice(0, 4) : items;
}

export function buildToolSessionGroups(sessions: LiveSession[]): ToolSessionGroup[] {
  const sorted = [...sessions].sort(compareSessionsByFreshness);
  const byID = new Map<string, LiveSession>();
  sorted.forEach((session) => {
    if (session.session_id) byID.set(session.session_id, session);
  });

  type MutableToolSessionGroup = {
    tool: string;
    sessions: LiveSession[];
    activeCount: number;
    reviewCount: number;
    mains: LiveSession[];
    childrenByParent: Map<string, LiveSession[]>;
    unlinked: LiveSession[];
    unknown: LiveSession[];
  };

  const groupMap = new Map<string, MutableToolSessionGroup>();
  const ensureGroup = (tool?: string): MutableToolSessionGroup => {
    const key = String(tool || "unknown").trim() || "unknown";
    const existing = groupMap.get(key);
    if (existing) return existing;
    const group: MutableToolSessionGroup = {
      tool: key,
      sessions: [],
      activeCount: 0,
      reviewCount: 0,
      mains: [],
      childrenByParent: new Map(),
      unlinked: [],
      unknown: [],
    };
    groupMap.set(key, group);
    return group;
  };

  sorted.forEach((session) => {
    const group = ensureGroup(session.tool);
    group.sessions.push(session);
    if (sessionHasRecentMovement(session)) group.activeCount++;
    if (sessionNeedsHumanReview(session)) group.reviewCount++;
    if (normalizedRole(session.session_role) === "main") group.mains.push(session);
  });

  sorted.forEach((session) => {
    const role = normalizedRole(session.session_role);
    const group = ensureGroup(session.tool);
    if (role === "subagent") {
      const parent = session.parent_thread_id ? byID.get(session.parent_thread_id) : undefined;
      if (parent && normalizedRole(parent.session_role) === "main") {
        const parentGroup = ensureGroup(parent.tool || session.tool);
        const parentKey = sessionIdentity(parent);
        parentGroup.childrenByParent.set(parentKey, [...(parentGroup.childrenByParent.get(parentKey) ?? []), session]);
      } else {
        group.unlinked.push(session);
      }
      return;
    }
    if (role === "unknown") {
      group.unknown.push(session);
    }
  });

  return Array.from(groupMap.values())
    .map((group) => ({
      tool: group.tool,
      sessions: group.sessions,
      activeCount: group.activeCount,
      reviewCount: group.reviewCount,
      linked: group.mains
        .sort(compareSessionsByFreshness)
        .map((parent) => ({
          parent,
          children: (group.childrenByParent.get(sessionIdentity(parent)) ?? []).sort(compareSessionsByFreshness),
        })),
      unlinked: group.unlinked.sort(compareSessionsByFreshness),
      unknown: group.unknown.sort(compareSessionsByFreshness),
    }))
    .sort((a, b) => {
      if (a.reviewCount !== b.reviewCount) return b.reviewCount - a.reviewCount;
      if (a.activeCount !== b.activeCount) return b.activeCount - a.activeCount;
      if (a.sessions.length !== b.sessions.length) return b.sessions.length - a.sessions.length;
      return a.tool.localeCompare(b.tool);
    });
}

export function hiddenToolSessionCount(group: ToolSessionGroup, linkedLimit: number, childLimit: number, unlinkedLimit: number): number {
  const visibleLinked = group.linked.slice(0, linkedLimit);
  const hiddenLinked = group.linked.slice(linkedLimit).reduce((total, branch) => total + 1 + branch.children.length, 0);
  const hiddenChildren = visibleLinked.reduce((total, branch) => total + Math.max(0, branch.children.length - childLimit), 0);
  const visibleUnlinkedCount = Math.min(group.unlinked.length, unlinkedLimit);
  const unknownLimit = Math.max(1, unlinkedLimit - visibleUnlinkedCount);
  const hiddenUnlinked = Math.max(0, group.unlinked.length - visibleUnlinkedCount);
  const hiddenUnknown = Math.max(0, group.unknown.length - unknownLimit);
  return hiddenLinked + hiddenChildren + hiddenUnlinked + hiddenUnknown;
}

export function compareSessionsByFreshness(a: LiveSession, b: LiveSession): number {
  if (Number(sessionNeedsHumanReview(a)) !== Number(sessionNeedsHumanReview(b))) return Number(sessionNeedsHumanReview(b)) - Number(sessionNeedsHumanReview(a));
  if (Number(sessionHasRecentMovement(a)) !== Number(sessionHasRecentMovement(b))) return Number(sessionHasRecentMovement(b)) - Number(sessionHasRecentMovement(a));
  const ageA = typeof a.last_event_age_seconds === "number" ? a.last_event_age_seconds : Number.MAX_SAFE_INTEGER;
  const ageB = typeof b.last_event_age_seconds === "number" ? b.last_event_age_seconds : Number.MAX_SAFE_INTEGER;
  if (ageA !== ageB) return ageA - ageB;
  return sessionIdentity(a).localeCompare(sessionIdentity(b));
}

export function roleLabel(t: Translate, role: SessionRole): string {
  return role === "main" ? t("main") : role === "subagent" ? t("subagent") : t("unknown");
}

export function confidenceLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return t("unavailable");
  if (raw === "high") return t("confidenceHigh");
  if (raw === "medium") return t("confidenceMedium");
  if (raw === "low") return t("confidenceLow");
  if (raw === "unknown") return t("unknown");
  return enumDisplayValue(value);
}

export function freshnessLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return t("unavailable");
  if (raw === "active") return t("active");
  if (raw === "idle") return t("idle");
  if (raw === "stale") return t("stale");
  if (raw === "fresh") return t("fresh");
  if (raw === "unknown") return t("unknown");
  return enumDisplayValue(value);
}

export function mappingMethodLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return t("unavailable");
  if (raw === "transcript_path") return t("mappingTranscriptPath");
  if (raw === "transcript_activity") return t("mappingTranscriptActivity");
  if (raw === "command_hint") return t("mappingCommandHint");
  if (raw === "fallback_session_id") return t("mappingFallbackSession");
  if (raw === "unknown") return t("unknown");
  return enumDisplayValue(value);
}

export function threadSourceLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return t("unavailable");
  if (raw === "user") return t("threadSourceUser");
  if (raw === "subagent") return t("subagent");
  if (raw === "unknown") return t("unknown");
  return enumDisplayValue(value);
}

export function roleHintLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return "";
  if (raw === "thread_source") return t("threadSource");
  if (raw === "agent_role") return t("role");
  if (raw === "unknown") return t("unknown");
  return enumDisplayValue(value);
}

export function agentRoleLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return "";
  if (raw === "worker") return t("agentRoleWorker");
  if (raw === "explorer") return t("agentRoleExplorer");
  if (raw === "unknown") return t("unknown");
  return enumDisplayValue(value);
}

export function tokenUsageProvenanceLabel(t: Translate, source?: string, confidence?: string): string {
  const sourceLabel = tokenUsageSourceLabel(t, source);
  const confidenceLabelValue = tokenUsageConfidenceLabel(t, confidence);
  if (sourceLabel === t("unavailable")) return sourceLabel;
  return `${sourceLabel} · ${confidenceLabelValue}`;
}

export function tokenUsageSourceLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return t("unavailable");
  if (raw === "transcript_usage") return t("tokenSourceTranscriptUsage");
  return enumDisplayValue(value);
}

export function tokenUsageConfidenceLabel(t: Translate, value?: string): string {
  const raw = enumToken(value);
  if (!raw) return t("unavailable");
  if (raw === "measured") return t("tokenConfidenceMeasured");
  if (raw === "estimated") return t("tokenConfidenceEstimated");
  if (raw === "unavailable") return t("unavailable");
  return enumDisplayValue(value);
}

export function sessionEvidenceItems(t: Translate, session: LiveSession, compact: boolean): EvidenceItem[] {
  const role = normalizedRole(session.session_role);
  const relationship = session.parent_thread_id ? shortID(session.parent_thread_id) : roleLabel(t, role);
  const items = [
    {
      label: t("confidence"),
      value: confidenceLabel(t, session.confidence || session.role_confidence),
      tone: session.confidence === "high" || session.role_confidence === "high" ? "good" : "",
    },
    { label: t("mappingMethod"), value: mappingMethodLabel(t, session.mapping_method) },
    { label: session.parent_thread_id ? t("parentThread") : t("threadSource"), value: session.thread_source ? threadSourceLabel(t, session.thread_source) : relationship },
    { label: t("roleHint"), value: roleHintLabel(t, session.role_hint_source) || agentRoleLabel(t, session.agent_role) || session.agent_nickname || t("unavailable") },
    { label: t("freshness"), value: freshnessLabel(t, session.freshness || (sessionHasRecentMovement(session) ? "active" : "idle")), tone: sessionHasRecentMovement(session) ? "active" : "" },
    modelUsageLabel(t, session.model_usage),
  ];
  if (!compact && typeof session.active_duration_seconds === "number") {
    items.push({ label: t("activeDuration"), value: formatAge(session.active_duration_seconds, t), tone: sessionHasRecentMovement(session) ? "active" : "" });
  }
  const visibleItems = items.filter(Boolean) as EvidenceItem[];
  return compact ? visibleItems.slice(0, 3) : visibleItems;
}

function modelUsageLabel(t: Translate, usage?: LiveSession["model_usage"]): EvidenceItem | null {
  if (!usage?.length) return null;
  const rows = usage
    .filter((row) => row.model && row.token_usage && tokenUsageHasValue(row.token_usage))
    .sort((a, b) => modelTokenTotal(b.token_usage) - modelTokenTotal(a.token_usage))
    .slice(0, 3)
    .map((row) => `${row.model}: ${formatTokenCount(modelTokenTotal(row.token_usage))}`);
  if (!rows.length) return null;
  const hidden = usage.length > rows.length ? ` +${usage.length - rows.length}` : "";
  return { label: t("models"), value: rows.join(" · ") + hidden, tone: "good" };
}

function modelTokenTotal(usage?: NonNullable<LiveSession["model_usage"]>[number]["token_usage"]): number {
  if (!usage) return 0;
  return usage.total_tokens ?? (usage.input_tokens ?? 0) + (usage.output_tokens ?? 0) + (usage.cache_creation_input_tokens ?? 0) + (usage.cache_read_input_tokens ?? 0) + (usage.reasoning_output_tokens ?? 0);
}

export function sessionIdentity(session: LiveSession): string {
  return session.session_id || session.path || `${session.tool || "tool"}:${session.project || "project"}`;
}

export function sessionIDsText(t: Translate, ids?: string[]): string {
  if (!ids?.length) return t("unavailable");
  const preview = ids.slice(0, 3).map((id) => shortID(id)).join(", ");
  return ids.length > 3 ? `${preview}, +${ids.length - 3}` : preview;
}

export function toolDisplayName(toolName?: string): string {
  const raw = String(toolName || "").trim();
  if (!raw) return "Unknown";
  const key = raw.toLowerCase();
  if (key === "codex" || key === "codexl") return "Codex";
  if (key === "claude") return "Claude";
  if (key === "trae" || key === "traex") return "Trae";
  if (key === "opencode" || key === "opencode-ai") return "OpenCode";
  if (key === "gemini" || key === "gemini-cli") return "Gemini";
  if (key === "grok" || key === "grok-cli") return "Grok";
  return raw;
}

export function toolIconName(toolName?: string): string {
  const key = String(toolName || "").trim().toLowerCase();
  if (key === "codex" || key === "codexl") return "codex";
  if (key === "claude") return "claude";
  if (key === "trae" || key === "traex") return "trae";
  if (key === "opencode" || key === "opencode-ai") return "opencode";
  if (key === "gemini" || key === "gemini-cli") return "gemini";
  if (key === "grok" || key === "grok-cli") return "grok";
  return "";
}

export function toolBadgeLabel(toolName?: string): string {
  const raw = String(toolName || "?").trim();
  return (raw.slice(0, 2) || "?").toUpperCase();
}

function enumToken(value?: string): string {
  return String(value || "").trim().toLowerCase();
}

function enumDisplayValue(value?: string): string {
  return String(value || "").trim().replace(/_/g, " ");
}
