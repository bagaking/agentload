import React from "react";
import { Bot, Cpu, Monitor } from "lucide-react";
import { toolDisplayName, toolIconName } from "../lib/activityModel";
import { formatCPU, formatMemory, type Translate } from "../lib/format";
import type { HostAppProcessSummary, ProcessRuntimeSummary, Snapshot } from "../types/snapshot";

export function ProcessSummaryStrip({ t, snapshot }: { t: Translate; snapshot: Snapshot }) {
  if (snapshot.process_stats?.incomplete) return null;
  const runtimeAll = [...(snapshot.runtime_process_summary ?? [])].sort((a, b) => (b.pid_count ?? 0) - (a.pid_count ?? 0));
  const hostsAll = [...(snapshot.host_app_process_summary ?? [])].sort((a, b) => (b.pid_count ?? 0) - (a.pid_count ?? 0));
  const runtime = runtimeAll.slice(0, 4);
  const hosts = hostsAll.slice(0, 3);
  if (!runtime.length && !hosts.length) return null;
  return (
    <section className="process-summary-strip" aria-label={t("processEvidenceGroups")}>
      <ProcessSummaryColumn
        t={t}
        title={t("codingAgentProcesses")}
        icon={<Bot size={13} />}
        totalCount={sumPidCount(runtimeAll)}
        totalRows={runtimeAll.length}
        rows={runtime.map((item) => runtimeRow(t, item))}
      />
      <ProcessSummaryColumn
        t={t}
        title={t("hostProcesses")}
        icon={<Monitor size={13} />}
        totalCount={sumPidCount(hostsAll)}
        totalRows={hostsAll.length}
        rows={hosts.map((item) => hostRow(t, item))}
      />
    </section>
  );
}

function ProcessSummaryColumn({
  t,
  title,
  icon,
  totalCount,
  totalRows,
  rows,
}: {
  t: Translate;
  title: string;
  icon: React.ReactNode;
  totalCount: number;
  totalRows: number;
  rows: ProcessSummaryRow[];
}) {
  const hiddenRows = Math.max(0, totalRows - rows.length);
  return (
    <article className="process-summary-column">
      <div className="process-summary-column-head">
        <span>{icon}{title}</span>
        <strong>{totalCount}</strong>
      </div>
      <div className="process-summary-rows">
        {rows.length ? rows.map((row) => <ProcessSummaryItem key={row.key} row={row} />) : null}
        {hiddenRows ? <span className="process-summary-hidden">{t("hiddenGroups").replace("{count}", String(hiddenRows))}</span> : null}
      </div>
    </article>
  );
}

type ProcessSummaryRow = {
  key: string;
  icon?: React.ReactNode;
  label: string;
  count: number;
  detail: string;
  role: string;
};

function ProcessSummaryItem({ row }: { row: ProcessSummaryRow }) {
  const label = `${row.label}: ${row.count}. ${row.detail}. ${row.role}`;
  return (
    <span className="process-summary-row" aria-label={label}>
      <i aria-hidden="true">{row.icon ?? <Cpu size={12} />}</i>
      <b>{row.label}</b>
      <strong>{row.count}</strong>
      <em>{row.detail}</em>
      <small>{row.role}</small>
    </span>
  );
}

function runtimeRow(t: Translate, item: ProcessRuntimeSummary): ProcessSummaryRow {
  const tool = item.tool || item.key || "unknown";
  const iconName = toolIconName(tool);
  return {
    key: item.key || tool,
    icon: iconName ? <img src={`/api/tool-icon/${encodeURIComponent(iconName)}`} alt="" loading="lazy" decoding="async" /> : undefined,
    label: item.display_name || toolDisplayName(tool),
    count: item.pid_count ?? 0,
    detail: `${formatCPU(item.cpu_percent)} · ${formatMemory(item.memory_bytes, t)}`,
    role: `${t("mainShort")} ${item.direct_sessions ?? 0} · ${t("subagentShort")} ${item.subagent_sessions ?? 0} · ${t("unmappedProcesses")} ${item.unmapped_processes ?? 0}`,
  };
}

function sumPidCount(items: Array<{ pid_count?: number }>): number {
  return items.reduce((total, item) => total + (item.pid_count ?? 0), 0);
}

function hostRow(t: Translate, item: HostAppProcessSummary): ProcessSummaryRow {
  return {
    key: item.key || item.name || "host",
    icon: item.pid ? <img src={`/api/host-app-icon/${encodeURIComponent(String(item.pid))}`} alt="" loading="lazy" decoding="async" /> : undefined,
    label: item.name || t("hostAppUnknown"),
    count: item.pid_count ?? 0,
    detail: `${formatCPU(item.cpu_percent)} · ${formatMemory(item.memory_bytes, t)}`,
    role: `${t("mapped")} ${item.mapped_processes ?? 0} · ${t("unmatched")} ${item.unmapped_processes ?? 0}`,
  };
}
