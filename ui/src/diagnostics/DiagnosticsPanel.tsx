import React, { useMemo, useState } from "react";
import {
  Activity,
  AlertTriangle,
  Box,
  ChevronRight,
  Clock,
  Code2,
  Cpu,
  Database,
  Download,
  EyeOff,
  FileText,
  Folder,
  HelpCircle,
  Link2,
  MessageSquare,
  Radar,
  Search,
  ShieldCheck,
  Target,
  Terminal,
} from "lucide-react";
import { type Translate } from "../lib/format";
import type { Snapshot } from "../types/snapshot";
import { buildDiagnosticViewModel, diagnosticOmittedFieldLabel, type ChainNode, type DiagnosticTone, type EvidenceMetric, type PriorityRow } from "./diagnosticModel";

type ExportState = "idle" | "working" | "done" | "failed";

export function DiagnosticsPanel({ t, snapshot }: { t: Translate; snapshot: Snapshot }) {
  const [exportState, setExportState] = useState<ExportState>("idle");
  const viewModel = useMemo(() => buildDiagnosticViewModel(t, snapshot), [t, snapshot]);

  const downloadExport = async () => {
    setExportState("working");
    try {
      const response = await fetch("/api/diagnostic-export", { cache: "no-store" });
      if (!response.ok) throw new Error(`HTTP ${response.status}`);
      const blob = await response.blob();
      const href = URL.createObjectURL(blob);
      const anchor = document.createElement("a");
      anchor.href = href;
      anchor.download = "agentload-diagnostics.json";
      document.body.appendChild(anchor);
      anchor.click();
      anchor.remove();
      URL.revokeObjectURL(href);
      setExportState("done");
      window.setTimeout(() => setExportState("idle"), 1800);
    } catch {
      setExportState("failed");
    }
  };

  return (
    <section className="popover-panel diagnostics-shell">
      <div className="diagnostics-headline">
        <div>
          <span className="note-kicker">{t("diagnostics")}</span>
          <h2>{t("diagnosticFactCheck")}</h2>
        </div>
        <span className="diagnostic-head-meta">
          <em>{t("liveSample")} · {viewModel.generated}</em>
          <b><Radar size={11} />{t("diagnosticNoForecastBadge")}</b>
        </span>
      </div>

      <section className="diagnostic-evidence-strip" aria-label={t("diagnosticEvidenceHealth")}>
        {viewModel.evidenceMetrics.map((metric) => <EvidenceMetricCell key={metric.key} metric={metric} />)}
      </section>

      <section className="diagnostic-priority-plane" aria-label={t("diagnosticPriority")}>
        <div className="diagnostic-plane-head">
          <span><Target size={15} />{t("diagnosticPriority")}</span>
          <em>
            {viewModel.anomalyCount} {t("anomalies")} · {viewModel.gapCount} {t("evidenceGaps")}
            {viewModel.hiddenSignalCount > 0 ? ` · ${t("diagnosticSignalsHidden").replace("{count}", String(viewModel.hiddenSignalCount))}` : ""}
          </em>
        </div>
        {viewModel.priorityRows.length ? (
          <div className="diagnostic-priority-table">
            <div className="diagnostic-priority-header" aria-hidden="true">
              <span>{t("diagnosticIssue")}</span>
              <span>{t("evidence")}</span>
              <span>{t("source")}</span>
              <span>{t("diagnosticNextCheck")}</span>
            </div>
            {viewModel.priorityRows.map((row) => <PrioritySignalRow key={row.key} row={row} />)}
          </div>
        ) : (
          <div className="diagnostic-empty"><ShieldCheck size={16} /><span>{t("diagnosticNoSignals")}</span></div>
        )}
      </section>

      <section className="diagnostic-chain-plane" aria-label={t("diagnosticEvidenceChain")}>
        <div className="diagnostic-plane-head">
          <span><Link2 size={15} />{t("diagnosticEvidenceChain")}</span>
          <StatusLegend t={t} />
        </div>
        <div className="diagnostic-chain-map">
          {diagnosticChainGroups(t, viewModel.chainNodes).map((group) => (
            <section key={group.key} className={`diagnostic-chain-group ${group.key}`}>
              <span className="diagnostic-chain-stage">{group.label}</span>
              <div className="diagnostic-chain-list">
                {group.nodes.map((node) => <ChainNodeItem key={node.key} node={node} />)}
              </div>
            </section>
          ))}
        </div>
      </section>

      <section className="diagnostic-export-plane" aria-label={t("diagnosticExportBoundary")}>
        <div className="diagnostic-export-title">
          <span><ShieldCheck size={15} />{t("diagnosticExportBoundary")}</span>
          <em>{t("diagnosticExportBoundaryCopy")}</em>
        </div>
        <div className="diagnostic-omissions">
          {viewModel.omittedFields.map((item) => <span key={item}>{omittedFieldIcon(item)}{diagnosticOmittedFieldLabel(t, item)}</span>)}
        </div>
        <button type="button" className={`diagnostic-export-button is-${exportState}`} onClick={downloadExport} disabled={exportState === "working"}>
          <Download size={14} />
          <span>{exportState === "working" ? t("exportWorking") : exportState === "done" ? t("exportReady") : exportState === "failed" ? t("exportFailed") : t("downloadDiagnostics")}</span>
        </button>
      </section>
    </section>
  );
}

function EvidenceMetricCell({ metric }: { metric: EvidenceMetric }) {
  // A null percent means the value has no denominator (a duration, a count).
  // Drawing the bar anyway would show a ratio nobody measured, so the bar is
  // omitted entirely rather than pinned at the CSS minimum width.
  const hasShare = metric.percent !== null;
  return (
    <article
      className={`diagnostic-evidence-cell tone-${metric.tone}${hasShare ? "" : " is-unscaled"}`}
      style={hasShare ? ({ "--metric-pct": `${metric.percent}%` } as React.CSSProperties) : undefined}
    >
      <span className="diagnostic-evidence-label">{evidenceMetricIcon(metric.key)}<b>{metric.label}</b></span>
      <strong>{metric.value}</strong>
      {hasShare ? <i aria-hidden="true"><em /></i> : <i aria-hidden="true" className="is-empty" />}
      <small>{metric.detail}</small>
    </article>
  );
}

function PrioritySignalRow({ row }: { row: PriorityRow }) {
  return (
    <article className={`diagnostic-priority-row tone-${row.tone}`}>
      <span className="diagnostic-priority-issue">
        <SeverityIcon tone={row.tone} />
        <span>
          <b>{row.title}</b>
          <em>{row.detail}</em>
        </span>
      </span>
      <span className="diagnostic-priority-evidence">
        <strong>{row.evidence}</strong>
        <em>{row.evidenceLabel}</em>
      </span>
      <span className="diagnostic-source-badge" title={row.sourceCode ? `${row.source} · ${row.sourceCode}` : row.source} aria-label={row.source}>
        <b>{row.source}</b>
      </span>
      <span className="diagnostic-priority-action">{row.action}<ChevronRight size={13} /></span>
    </article>
  );
}

type EvidenceChainGroup = {
  key: "collectors" | "semantics" | "export";
  label: string;
  nodes: ChainNode[];
};

function diagnosticChainGroups(t: Translate, nodes: ChainNode[]): EvidenceChainGroup[] {
  const byKey = new Map(nodes.map((node) => [node.key, node]));
  const pick = (keys: ChainNode["key"][]) => keys.map((key) => byKey.get(key)).filter((node): node is ChainNode => Boolean(node));
  return [
    { key: "collectors", label: t("diagnosticCollectors"), nodes: pick(["passive_process_observer", "transcript_parser", "system_resources"]) },
    { key: "semantics", label: t("metricSemantics"), nodes: pick(["runtime_telemetry_adapter"]) },
    { key: "export", label: t("diagnosticExport"), nodes: pick(["diagnostic_export"]) },
  ];
}

function ChainNodeItem({ node }: { node: ChainNode }) {
  return (
    <article className={`diagnostic-chain-node tone-${node.tone}`} title={node.code ? `${node.label} · ${node.code}` : node.label}>
      <span className="diagnostic-chain-node-icon">{chainNodeIcon(node.key)}</span>
      <span className="diagnostic-chain-node-main">
        <b>{node.label}</b>
      </span>
      <em>{node.status}</em>
    </article>
  );
}

function SeverityIcon({ tone }: { tone: DiagnosticTone }) {
  if (tone === "warn" || tone === "watch") return <AlertTriangle size={17} />;
  return <Radar size={17} />;
}

function StatusLegend({ t }: { t: Translate }) {
  return (
    <span className="diagnostic-status-legend">
      <i className="tone-ok" />{t("available")}
      <i className="tone-empty" />{t("emptyStatus")}
      <i className="tone-muted" />{t("notConfigured")}
    </span>
  );
}

function evidenceMetricIcon(key: EvidenceMetric["key"]) {
  if (key === "session_evidence") return <Database size={14} />;
  if (key === "pid_link") return <Link2 size={14} />;
  if (key === "low_confidence") return <HelpCircle size={14} />;
  if (key === "walk_cost") return <Clock size={14} />;
  if (key === "walk_scope") return <Search size={14} />;
  return <EyeOff size={14} />;
}

function chainNodeIcon(key: ChainNode["key"]) {
  if (key === "passive_process_observer") return <Activity size={19} />;
  if (key === "transcript_parser") return <FileText size={19} />;
  if (key === "system_resources") return <Cpu size={19} />;
  if (key === "runtime_telemetry_adapter") return <Radar size={19} />;
  return <Download size={19} />;
}

function omittedFieldIcon(value: string) {
  const text = value.toLowerCase();
  if (text.includes("prompt")) return <MessageSquare size={12} />;
  if (text.includes("path")) return <Folder size={12} />;
  if (text.includes("command")) return <Terminal size={12} />;
  if (text.includes("env")) return <Code2 size={12} />;
  if (text.includes("bundle")) return <Box size={12} />;
  return <ShieldCheck size={12} />;
}
