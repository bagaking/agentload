import React, { useMemo, useState } from "react";
import { AlertTriangle, Download, Radar, ShieldCheck, Sigma, Waves } from "lucide-react";
import { formatDateTime, type Translate } from "../lib/format";
import type { DiagnosticBaseline, DiagnosticCapability, DiagnosticSignal, MetricRegistryEntry, RuntimeTelemetrySnapshot, Snapshot } from "../types/snapshot";

type ExportState = "idle" | "working" | "done" | "failed";

export function DiagnosticsPanel({ t, snapshot }: { t: Translate; snapshot: Snapshot }) {
  const diagnostics = snapshot.diagnostics;
  const [exportState, setExportState] = useState<ExportState>("idle");
  const anomalies = diagnostics?.anomaly_signals ?? [];
  const gaps = diagnostics?.evidence_gaps ?? [];
  const baselines = diagnostics?.baselines ?? [];
  const capabilities = diagnostics?.capabilities ?? [];
  const generated = diagnostics?.generated_at ? formatDateTime(diagnostics.generated_at) : snapshot.generated_at ? formatDateTime(snapshot.generated_at) : t("unavailable");
  const signals = useMemo(() => [...anomalies, ...gaps], [anomalies, gaps]);
  const baselineRows = useMemo(() => diagnosticBaselineRows(t, baselines), [t, baselines]);
  const metricRegistry = useMemo(() => semanticRegistryPreview(snapshot.metric_registry ?? []), [snapshot.metric_registry]);

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
          <h2>{t("diagnosticsPage")}</h2>
        </div>
        <span>{t("liveSample")} · {generated}</span>
      </div>

      <section className="diagnostics-score-grid" aria-label={t("diagnosticBaselines")}>
        {baselineRows.length ? baselineRows.map((baseline) => (
          <article className={`diagnostic-baseline status-${baseline.status || "unknown"}`} key={baseline.key || baseline.label}>
            <span>{diagnosticBaselineLabel(t, baseline)}</span>
            <strong>{diagnosticBaselineValue(t, baseline)}</strong>
            <em>{diagnosticBaselineDetail(t, baseline)}</em>
          </article>
        )) : (
          <article className="diagnostic-baseline status-empty">
            <span>{t("diagnosticBaselines")}</span>
            <strong>{t("unavailable")}</strong>
            <em>{t("diagnosticNoSignals")}</em>
          </article>
        )}
      </section>

      <section className="diagnostic-signal-plane">
        <div className="diagnostic-section-head">
          <span><Radar size={14} />{t("diagnosticSignals")}</span>
          <em>{anomalies.length} {t("anomalies")} · {gaps.length} {t("evidenceGaps")}</em>
        </div>
        {signals.length ? (
          <div className="diagnostic-signal-list">
            {signals.map((signal, index) => <DiagnosticSignalRow key={`${signal.kind}-${index}`} t={t} signal={signal} />)}
          </div>
        ) : (
          <div className="diagnostic-empty"><ShieldCheck size={16} /><span>{t("diagnosticNoSignals")}</span></div>
        )}
      </section>

      <section className="diagnostic-semantic-plane">
        <div className="diagnostic-section-head">
          <span><Sigma size={14} />{t("metricSemantics")}</span>
          <em>{metricRegistry.length}</em>
        </div>
        <div className="diagnostic-semantic-grid">
          {metricRegistry.map((metric) => <MetricRegistryRow key={metric.key || metric.label} t={t} metric={metric} />)}
        </div>
      </section>

      <section className="diagnostics-capability-export">
        <div className="diagnostic-capabilities">
          <div className="diagnostic-section-head">
            <span><Waves size={14} />{t("diagnosticCapabilities")}</span>
            <em>{capabilities.length}</em>
          </div>
          <div className="diagnostic-capability-list">
            {capabilities.map((capability) => <DiagnosticCapabilityRow key={capability.key || capability.label} t={t} capability={capability} />)}
          </div>
          <RuntimeTelemetryRow t={t} telemetry={snapshot.runtime_telemetry} />
        </div>
        <article className="diagnostic-export-card">
          <div className="diagnostic-section-head">
            <span><Download size={14} />{t("diagnosticExport")}</span>
            <em>{t("safeExport")}</em>
          </div>
          <p>{t("diagnosticExportCopy")}</p>
          <div className="diagnostic-omissions">
            {(diagnostics?.export?.omitted_fields ?? []).map((item) => <span key={item}>{diagnosticOmittedFieldLabel(t, item)}</span>)}
          </div>
          <button type="button" className={`diagnostic-export-button is-${exportState}`} onClick={downloadExport} disabled={exportState === "working"}>
            <Download size={14} />
            <span>{exportState === "working" ? t("exportWorking") : exportState === "done" ? t("exportReady") : exportState === "failed" ? t("exportFailed") : t("downloadDiagnostics")}</span>
          </button>
        </article>
      </section>
    </section>
  );
}

function DiagnosticSignalRow({ t, signal }: { t: Translate; signal: DiagnosticSignal }) {
  const severity = signal.severity || "info";
  return (
    <article className={`diagnostic-signal severity-${severity}`}>
      <AlertTriangle size={13} aria-hidden="true" />
      <span>
        <strong>{diagnosticSignalTitle(t, signal)}</strong>
        <em>{diagnosticSignalDetail(t, signal)}</em>
      </span>
      <b>{signal.metric_key ? metricKeyLabel(t, signal.metric_key) : diagnosticSourceLabel(t, signal.source)}</b>
    </article>
  );
}

function DiagnosticCapabilityRow({ t, capability }: { t: Translate; capability: DiagnosticCapability }) {
  return (
    <article className={`diagnostic-capability status-${capability.status || "unknown"}`}>
      <span>{diagnosticCapabilityLabel(t, capability)}</span>
      <strong>{diagnosticStatusLabel(t, capability.status)}</strong>
      <em>{diagnosticCapabilityDetail(t, capability)}</em>
    </article>
  );
}

function MetricRegistryRow({ t, metric }: { t: Translate; metric: MetricRegistryEntry }) {
  return (
    <article className="diagnostic-semantic-row">
      <span>
        <b>{metricKeyLabel(t, metric.key, metric.label)}</b>
        <em>{metricFamilyLabel(t, metric.family)} · {metricUnitLabel(t, metric)}</em>
      </span>
      <strong>{metricSourceLabel(t, metric)}</strong>
      <small>{metricMissingStateLabel(t, metric)}</small>
    </article>
  );
}

function RuntimeTelemetryRow({ t, telemetry }: { t: Translate; telemetry?: RuntimeTelemetrySnapshot }) {
  if (!telemetry) return null;
  return (
    <article className={`diagnostic-runtime-telemetry status-${telemetry.status || "unknown"}`}>
      <div className="diagnostic-section-head">
        <span><Waves size={14} />{t("runtimeTelemetry")}</span>
        <em>{diagnosticStatusLabel(t, telemetry.status)}</em>
      </div>
      <p>{runtimeTelemetryDetail(t, telemetry)}</p>
      <div className="diagnostic-adapter-row">
        {(telemetry.adapters ?? []).map((adapter) => (
          <span key={adapter.key || adapter.label}>
            <b>{runtimeTelemetryAdapterLabel(t, adapter.key, adapter.label)}</b>
            <strong>{diagnosticStatusLabel(t, adapter.status)}</strong>
          </span>
        ))}
      </div>
    </article>
  );
}

function semanticRegistryPreview(items: MetricRegistryEntry[]): MetricRegistryEntry[] {
  const preferred = ["recent_movement", "known_sessions", "process_pressure", "process_resources", "system_resources", "role_matrix", "token_usage", "runtime_telemetry"];
  const byKey = new Map(items.map((item) => [item.key, item]));
  return preferred.map((key) => byKey.get(key)).filter(Boolean) as MetricRegistryEntry[];
}

function diagnosticBaselineRows(t: Translate, baselines: DiagnosticBaseline[]): DiagnosticBaseline[] {
  return [
    ...baselines,
    {
      key: "prediction_safe_status",
      label: t("predictionSafeStatus"),
      value: t("predictionUnavailable"),
      status: "unavailable",
      detail: t("predictionUnavailableDetail"),
      metric_key: "diagnostic_export",
    },
  ];
}

function diagnosticBaselineLabel(t: Translate, baseline: DiagnosticBaseline): string {
  if (baseline.key === "mapping_coverage") return t("baselineMappingCoverage");
  if (baseline.key === "active_session_ratio") return t("baselineRecentMovementShare");
  if (baseline.key === "low_confidence_sessions") return t("baselineLowConfidenceSessions");
  if (baseline.key === "token_measured_sessions") return t("baselineTokenMeasuredSessions");
  if (baseline.key === "prediction_safe_status") return t("predictionSafeStatus");
  return baseline.key ? humanizeKey(baseline.key) : baseline.label || t("unknown");
}

function diagnosticBaselineValue(t: Translate, baseline: DiagnosticBaseline): string {
  const value = String(baseline.value || "").trim();
  if (!value) return t("unavailable");
  if (value === "no visible PIDs") return t("noVisiblePids");
  return value;
}

function diagnosticBaselineDetail(t: Translate, baseline: DiagnosticBaseline): string {
  if (baseline.key === "mapping_coverage") return t("baselineMappingCoverageDetail");
  if (baseline.key === "active_session_ratio") return t("baselineRecentMovementShareDetail");
  if (baseline.key === "low_confidence_sessions") return t("baselineLowConfidenceSessionsDetail");
  if (baseline.key === "token_measured_sessions") return t("baselineTokenMeasuredSessionsDetail");
  if (baseline.key === "prediction_safe_status") return t("predictionUnavailableDetail");
  return baseline.metric_key ? metricKeyLabel(t, baseline.metric_key) : t("diagnosticEvidence");
}

function metricKeyLabel(t: Translate, key?: string, fallback?: string): string {
  if (key === "recent_movement") return t("metricFresh");
  if (key === "known_sessions") return t("metricKnownSessions");
  if (key === "process_pressure") return t("processPressure");
  if (key === "process_resources") return t("processResources");
  if (key === "system_resources") return t("systemResources");
  if (key === "role_matrix") return t("roleMatrix");
  if (key === "token_usage") return t("tokenUsage");
  if (key === "runtime_telemetry") return t("runtimeTelemetry");
  if (key === "diagnostic_export") return t("diagnosticExport");
  return fallback || key || t("unknown");
}

function diagnosticCapabilityLabel(t: Translate, capability: DiagnosticCapability): string {
  if (capability.key === "passive_process_observer") return t("passiveProcessObserver");
  if (capability.key === "transcript_parser") return t("transcriptParser");
  if (capability.key === "system_resources") return t("systemResources");
  if (capability.key === "runtime_telemetry_adapter") return t("runtimeTelemetry");
  if (capability.key === "diagnostic_export") return t("diagnosticExport");
  return capability.label || capability.key || t("unknown");
}

function diagnosticSignalTitle(t: Translate, signal: DiagnosticSignal): string {
  const title = translateDiagnosticKey(t, "diagnosticSignal", signal.kind, "Title");
  if (title) return title;
  return signal.kind ? humanizeKey(signal.kind) : t("unknown");
}

function diagnosticSignalDetail(t: Translate, signal: DiagnosticSignal): string {
  const detail = translateDiagnosticKey(t, "diagnosticSignal", signal.kind, "Detail") || t("diagnosticEvidenceDetail");
  const evidence = String(signal.evidence || "").trim();
  return evidence ? `${detail} · ${evidence}` : detail;
}

function diagnosticCapabilityDetail(t: Translate, capability: DiagnosticCapability): string {
  return translateDiagnosticKey(t, "diagnosticCapability", capability.key, "Detail") || t("diagnosticEvidenceDetail");
}

function diagnosticSourceLabel(t: Translate, source?: string): string {
  return translateDiagnosticKey(t, "diagnosticSource", source, "Label") || (source ? humanizeKey(source) : "");
}

function diagnosticOmittedFieldLabel(t: Translate, value: string): string {
  const normalized = normalizeI18nKey(value);
  const key = `diagnosticOmitted${normalized}`;
  const translated = t(key);
  return translated !== key ? translated : humanizeKey(value);
}

function metricFamilyLabel(t: Translate, family?: string): string {
  return translateDiagnosticKey(t, "metricFamily", family, "Label") || (family ? humanizeKey(family) : t("unknown"));
}

function metricUnitLabel(t: Translate, metric: MetricRegistryEntry): string {
  return translateDiagnosticKey(t, "metricUnit", metric.key, "Label") || metric.unit || t("unavailable");
}

function metricSourceLabel(t: Translate, metric: MetricRegistryEntry): string {
  return translateDiagnosticKey(t, "metricSource", metric.key, "Label") || t("localSource");
}

function metricMissingStateLabel(t: Translate, metric: MetricRegistryEntry): string {
  return translateDiagnosticKey(t, "metricMissing", metric.key, "Label") || t("unavailable");
}

function runtimeTelemetryDetail(t: Translate, telemetry: RuntimeTelemetrySnapshot): string {
  if (telemetry.configured) return t("runtimeTelemetryConfiguredCopy");
  return t("runtimeTelemetryCopy");
}

function runtimeTelemetryAdapterLabel(t: Translate, key?: string, fallback?: string): string {
  return translateDiagnosticKey(t, "runtimeTelemetryAdapter", key, "Label") || fallback || key || t("unknown");
}

function translateDiagnosticKey(t: Translate, prefix: string, value?: string, suffix = ""): string {
  const normalized = normalizeI18nKey(value);
  if (!normalized) return "";
  const key = `${prefix}${normalized}${suffix}`;
  const translated = t(key);
  return translated !== key ? translated : "";
}

function normalizeI18nKey(value?: string): string {
  const text = String(value || "").trim();
  if (!text) return "";
  return text
    .split(/[^a-zA-Z0-9]+/)
    .filter(Boolean)
    .map((part) => part.slice(0, 1).toUpperCase() + part.slice(1))
    .join("");
}

function humanizeKey(value: string): string {
  return String(value || "").trim().replace(/[_-]+/g, " ").replace(/\s+/g, " ");
}

function diagnosticStatusLabel(t: Translate, status?: string): string {
  const value = String(status || "").trim().toLowerCase();
  if (value === "available") return t("available");
  if (value === "not_configured") return t("notConfigured");
  if (value === "unavailable") return t("unavailable");
  if (value === "partial") return t("partial");
  if (value === "empty") return t("emptyStatus");
  if (value === "ok") return t("okStatus");
  if (value === "watch") return t("watchStatus");
  if (value === "warn") return t("warnStatus");
  if (value === "idle") return t("idle");
  return status || t("unknown");
}
