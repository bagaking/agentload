import React, { useMemo } from "react";
import { Activity, Bot, GitBranch, Server } from "lucide-react";
import { toolDisplayName } from "../lib/activityModel";
import { sessionHasRecentMovement, sessionProcessPressure, normalizedRole } from "../lib/metricSemantics";
import { formatTokenCount, tokenUsageHasValue, type Translate } from "../lib/format";
import type { ToolSessionGroup } from "../types/app";
import type { LiveSession } from "../types/snapshot";

export function LineageSummary({
  t,
  groups,
  sessions,
}: {
  t: Translate;
  groups: ToolSessionGroup[];
  sessions: LiveSession[];
}) {
  const summary = useMemo(() => summarizeLineage(groups, sessions), [groups, sessions]);
  if (!sessions.length) return null;
  return (
    <section className="lineage-summary" aria-label={t("sessionLineageSummary")}>
      <div className="lineage-summary-main">
        <LineageStat icon={<Activity size={12} />} label={t("activeShort")} value={`${summary.active}/${summary.total}`} detail={t("sessions")} />
        <LineageStat icon={<Bot size={12} />} label={t("directAgents")} value={roleSplitValue(summary)} detail={summary.unknown ? t("mainSubUnknownSplit") : t("mainSubSplit")} />
        <LineageStat icon={<GitBranch size={12} />} label={t("linkedBranches")} value={`${summary.linkedChildren}/${summary.unlinkedSubagents}`} detail={t("linkedUnlinkedSplit")} />
        <LineageStat icon={<Server size={12} />} label={t("processShort")} value={String(summary.processes)} detail={t("processPressure")} />
      </div>
      <div className="lineage-tool-lanes" aria-label={t("toolLanes")}>
        {summary.tools.map((tool) => (
          <span key={tool.tool} style={{ "--lane-pct": `${tool.share}%` } as React.CSSProperties}>
            <b>{toolDisplayName(tool.tool)}</b>
            <i aria-hidden="true"><em /></i>
            <strong>{tool.active}/{tool.total}</strong>
          </span>
        ))}
      </div>
      <span className="lineage-token-cue">
        <b>{t("measuredTokenSessions")}</b>
        <strong>{summary.measuredTokens ? `${summary.measuredTokens}/${summary.total}` : t("unavailable")}</strong>
        {summary.tokenTotal > 0 ? <em>{formatTokenCount(summary.tokenTotal)} {t("tokenTotal")}</em> : null}
      </span>
    </section>
  );
}

function LineageStat({ icon, label, value, detail }: { icon: React.ReactNode; label: string; value: string; detail: string }) {
  return (
    <span className="lineage-stat">
      {icon}
      <b>{label}</b>
      <strong>{value}</strong>
      <em>{detail}</em>
    </span>
  );
}

function summarizeLineage(groups: ToolSessionGroup[], sessions: LiveSession[]) {
  const roleCounts = sessions.reduce((counts, session) => {
    const role = normalizedRole(session.session_role);
    if (role === "main") counts.main++;
    else if (role === "subagent") counts.subagent++;
    else counts.unknown++;
    if (sessionHasRecentMovement(session)) counts.active++;
    counts.processes += sessionProcessPressure(session);
    const tokenUsage = session.token_usage;
    if (tokenUsage && hasMeasuredTokenUsage(session)) {
      counts.measuredTokens++;
      counts.tokenTotal += tokenUsage.total_tokens
        ?? ((tokenUsage.input_tokens ?? 0)
          + (tokenUsage.output_tokens ?? 0)
          + (tokenUsage.cache_creation_input_tokens ?? 0)
          + (tokenUsage.cache_read_input_tokens ?? 0)
          + (tokenUsage.reasoning_output_tokens ?? 0));
    }
    return counts;
  }, { active: 0, main: 0, subagent: 0, unknown: 0, processes: 0, measuredTokens: 0, tokenTotal: 0 });
  const linkedChildren = groups.reduce((total, group) => total + group.linked.reduce((sum, branch) => sum + branch.children.length, 0), 0);
  const unlinkedSubagents = groups.reduce((total, group) => total + group.unlinked.length, 0);
  const maxToolTotal = Math.max(1, ...groups.map((group) => group.sessions.length));
  return {
    ...roleCounts,
    total: sessions.length,
    linkedChildren,
    unlinkedSubagents,
    tools: groups.slice(0, 4).map((group) => ({
      tool: group.tool,
      active: group.activeCount,
      total: group.sessions.length,
      share: Math.max(4, Math.min(100, (group.sessions.length / maxToolTotal) * 100)),
    })),
  };
}

function roleSplitValue(summary: { main: number; subagent: number; unknown: number }): string {
  return summary.unknown ? `${summary.main}/${summary.subagent}/${summary.unknown}` : `${summary.main}/${summary.subagent}`;
}

function hasMeasuredTokenUsage(session: LiveSession): boolean {
  return Boolean(
    session.token_usage &&
    tokenUsageHasValue(session.token_usage) &&
    String(session.token_usage_source || "").trim().toLowerCase() === "transcript_usage" &&
    String(session.token_usage_confidence || "").trim().toLowerCase() === "measured",
  );
}
