import React, { useMemo, useState } from "react";
import {
  Activity,
  AlertTriangle,
  Box,
  ChevronRight,
  Code2,
  Cpu,
  Database,
  Download,
  EyeOff,
  FileText,
  Folder,
  Link2,
  MessageSquare,
  Radar,
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
          <em>{viewModel.anomalyCount} {t("anomalies")} · {viewModel.gapCount} {t("evidenceGaps")}</em>
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
          <div className="diagnostic-chain-stage collectors">{t("diagnosticCollectors")}</div>
          <div className="diagnostic-chain-stage semantics">{t("metricSemantics")}</div>
          <div className="diagnostic-chain-stage export">{t("diagnosticExport")}</div>
          {viewModel.chainNodes.map((node, index) => <ChainNodeItem key={node.key} node={node} index={index} total={viewModel.chainNodes.length} />)}
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
  return (
    <article className={`diagnostic-evidence-cell tone-${metric.tone}`} style={{ "--metric-pct": `${metric.percent}%` } as React.CSSProperties}>
      <span className="diagnostic-evidence-label">{evidenceMetricIcon(metric.key)}<b>{metric.label}</b></span>
      <strong>{metric.value}</strong>
      <i aria-hidden="true"><em /></i>
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
      <span className="diagnostic-source-badge">
        <b>{row.source}</b>
        <code>{row.sourceCode}</code>
      </span>
      <span className="diagnostic-priority-action">{row.action}<ChevronRight size={13} /></span>
    </article>
  );
}

function ChainNodeItem({ node, index, total }: { node: ChainNode; index: number; total: number }) {
  return (
    <article className={`diagnostic-chain-node tone-${node.tone}`} style={{ "--node-index": index, "--node-total": total } as React.CSSProperties}>
      <span>{chainNodeIcon(node.key)}</span>
      <b>{node.label}</b>
      <code>{node.code}</code>
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
