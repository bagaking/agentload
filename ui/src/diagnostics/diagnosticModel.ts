import { formatDateTime, type Translate } from "../lib/format";
import type { DiagnosticBaseline, DiagnosticCapability, DiagnosticSignal, RuntimeTelemetrySnapshot, Snapshot } from "../types/snapshot";

export type DiagnosticTone = "ok" | "watch" | "warn" | "empty" | "muted";

export type EvidenceMetric = {
  key: "session_evidence" | "pid_link" | "token_visible";
  label: string;
  value: string;
  detail: string;
  percent: number;
  tone: DiagnosticTone;
};

export type PriorityRow = {
  key: string;
  tone: DiagnosticTone;
  title: string;
  detail: string;
  evidence: string;
  evidenceLabel: string;
  source: string;
  sourceCode: string;
  action: string;
};

export type ChainNode = {
  key: "passive_process_observer" | "transcript_parser" | "system_resources" | "runtime_telemetry_adapter" | "diagnostic_export";
  label: string;
  code: string;
  status: string;
  tone: DiagnosticTone;
};

export type DiagnosticViewModel = {
  generated: string;
  anomalyCount: number;
  gapCount: number;
  evidenceMetrics: EvidenceMetric[];
  priorityRows: PriorityRow[];
  chainNodes: ChainNode[];
  omittedFields: string[];
};

export function buildDiagnosticViewModel(t: Translate, snapshot: Snapshot): DiagnosticViewModel {
  const diagnostics = snapshot.diagnostics;
  const anomalies = diagnostics?.anomaly_signals ?? [];
  const gaps = diagnostics?.evidence_gaps ?? [];
  const signals = [...anomalies, ...gaps];
  const baselineRows = diagnosticBaselineRows(t, diagnostics?.baselines ?? []);
  return {
    generated: diagnostics?.generated_at ? formatDateTime(diagnostics.generated_at) : snapshot.generated_at ? formatDateTime(snapshot.generated_at) : t("unavailable"),
    anomalyCount: anomalies.length,
    gapCount: gaps.length,
    evidenceMetrics: buildEvidenceMetrics(t, baselineRows),
    priorityRows: buildPriorityRows(t, signals),
    chainNodes: buildChainNodes(t, diagnostics?.capabilities ?? [], snapshot.runtime_telemetry),
    omittedFields: diagnostics?.export?.omitted_fields ?? [],
  };
}

export function diagnosticOmittedFieldLabel(t: Translate, value: string): string {
  const normalized = normalizeI18nKey(value);
  const key = `diagnosticOmitted${normalized}`;
  const translated = t(key);
  return translated !== key ? translated : humanizeKey(value);
}

function buildEvidenceMetrics(t: Translate, baselines: DiagnosticBaseline[]): EvidenceMetric[] {
  const byKey = new Map(baselines.map((baseline) => [baseline.key, baseline]));
  const session = byKey.get("active_session_ratio");
  const mapping = byKey.get("mapping_coverage");
  const token = byKey.get("token_measured_sessions");
  return [
    {
      key: "session_evidence",
      label: t("diagnosticSessionEvidence"),
      value: diagnosticBaselineValue(t, session),
      detail: coverageDetail(t, session, "diagnosticCovered"),
      percent: baselinePercent(session),
      tone: toneFromStatus(session?.status),
    },
    {
      key: "pid_link",
      label: t("diagnosticPidLink"),
      value: diagnosticBaselineValue(t, mapping),
      detail: t("diagnosticPidLinkDetail"),
      percent: baselinePercent(mapping),
      tone: toneFromStatus(mapping?.status),
    },
    {
      key: "token_visible",
      label: t("diagnosticTokenVisible"),
      value: diagnosticBaselineValue(t, token),
      detail: t("diagnosticTokenVisibleDetail"),
      percent: baselinePercent(token),
      tone: toneFromStatus(token?.status),
    },
  ];
}

function buildPriorityRows(t: Translate, signals: DiagnosticSignal[]): PriorityRow[] {
  return signals.slice(0, 6).map((signal, index) => {
    const metric = signal.metric_key || signal.source || signal.kind || "diagnostic";
    return {
      key: `${signal.kind || metric}-${index}`,
      tone: signal.severity === "warn" ? "warn" : "watch",
      title: diagnosticSignalTitle(t, signal),
      detail: diagnosticSignalDetail(t, signal),
      evidence: signalEvidenceValue(t, signal),
      evidenceLabel: signalEvidenceLabel(t, signal),
      source: signalSourceLabel(t, signal, metric),
      sourceCode: signalSourceCode(metric),
      action: diagnosticNextAction(t, signal),
    };
  });
}

function buildChainNodes(t: Translate, capabilities: DiagnosticCapability[], telemetry?: RuntimeTelemetrySnapshot): ChainNode[] {
  const byKey = new Map(capabilities.map((capability) => [capability.key, capability]));
  const runtime = byKey.get("runtime_telemetry_adapter");
  const runtimeStatus = runtime?.status || telemetry?.status || "not_configured";
  return [
    chainNode(t, byKey.get("passive_process_observer"), "passive_process_observer", "proc_observe"),
    chainNode(t, byKey.get("transcript_parser"), "transcript_parser", "log_parse"),
    chainNode(t, byKey.get("system_resources"), "system_resources", "sys_metrics"),
    chainNode(t, { key: "runtime_telemetry_adapter", status: runtimeStatus }, "runtime_telemetry_adapter", "rt_telemetry"),
    chainNode(t, byKey.get("diagnostic_export"), "diagnostic_export", "diag_export"),
  ];
}

function chainNode(t: Translate, capability: Pick<DiagnosticCapability, "key" | "status"> | undefined, key: ChainNode["key"], code: string): ChainNode {
  const status = capability?.status || "unavailable";
  return {
    key,
    label: diagnosticCapabilityLabel(t, { key }),
    code,
    status: diagnosticStatusLabel(t, status),
    tone: toneFromStatus(status),
  };
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

function diagnosticBaselineValue(t: Translate, baseline?: DiagnosticBaseline): string {
  const value = String(baseline?.value || "").trim();
  if (!value) return t("unavailable");
  if (value === "no visible PIDs") return t("noVisiblePids");
  const ratio = value.match(/^([0-9]+)\s+of\s+([0-9]+)$/i);
  if (ratio) return `${ratio[1]} / ${ratio[2]}`;
  return value;
}

function coverageDetail(t: Translate, baseline: DiagnosticBaseline | undefined, key: string): string {
  const pct = baselinePercent(baseline);
  if (!Number.isFinite(pct) || pct <= 0) return t("unavailable");
  return `${pct.toFixed(pct >= 10 ? 1 : 2)}% ${t(key)}`;
}

function baselinePercent(baseline?: DiagnosticBaseline): number {
  const value = String(baseline?.value || "").trim();
  const percent = value.match(/([0-9]+(?:\.[0-9]+)?)%/);
  if (percent) return clampPercent(Number(percent[1]));
  const ratio = value.match(/([0-9]+)\s+of\s+([0-9]+)/i);
  if (ratio) return clampPercent((Number(ratio[1]) / Math.max(1, Number(ratio[2]))) * 100);
  return 0;
}

function signalEvidenceValue(t: Translate, signal: DiagnosticSignal): string {
  const evidence = String(signal.evidence || "").trim();
  if (!evidence) return t("unavailable");
  const percent = evidence.match(/([0-9]+(?:\.[0-9]+)?)%/);
  if (percent) return `${percent[1]}%`;
  const ratio = evidence.match(/([0-9]+)\s*(?:\/|of)\s*([0-9]+)/i);
  if (ratio) return `${ratio[1]}/${ratio[2]}`;
  const count = evidence.match(/^([0-9]+)/);
  if (count) return count[1];
  return evidence.length > 18 ? `${evidence.slice(0, 18)}...` : evidence;
}

function signalEvidenceLabel(t: Translate, signal: DiagnosticSignal): string {
  if (signal.metric_key) return metricKeyLabel(t, signal.metric_key);
  if (signal.source) return diagnosticSourceLabel(t, signal.source);
  return t("diagnosticEvidence");
}

function signalSourceLabel(t: Translate, signal: DiagnosticSignal, fallback: string): string {
  if (signal.source) return diagnosticSourceLabel(t, signal.source);
  if (signal.metric_key) return metricKeyLabel(t, signal.metric_key);
  return metricKeyLabel(t, fallback, humanizeKey(fallback));
}

function signalSourceCode(value: string): string {
  const key = String(value || "").trim();
  if (key === "process_pressure" || key === "live_processes") return "proc_link";
  if (key === "token_usage") return "usage_parse";
  if (key === "role_matrix") return "role_map";
  if (key === "known_sessions" || key === "live_sessions") return "session_ev";
  if (key === "recent_movement" || key === "transcript_stats") return "log_scan";
  if (key === "system_resources") return "sys_metrics";
  if (key === "coordination_risk") return "risk_mix";
  return key.replace(/[^a-zA-Z0-9_]+/g, "_").slice(0, 12) || "diag";
}

function diagnosticNextAction(t: Translate, signal: DiagnosticSignal): string {
  const kind = String(signal.kind || "").trim();
  if (kind.includes("unmapped") || kind.includes("unmatched")) return t("diagnosticActionCheckSessionLink");
  if (kind.includes("token")) return t("diagnosticActionCheckTokenParser");
  if (kind.includes("candidate") || kind.includes("workitem")) return t("diagnosticActionCheckWorkitems");
  if (kind.includes("system") || kind.includes("network")) return t("diagnosticActionCheckSystemSampler");
  if (kind.includes("transcript") || kind.includes("deferred")) return t("diagnosticActionCheckTranscriptScan");
  if (kind.includes("duplicate") || kind.includes("overlap")) return t("diagnosticActionCheckOverlap");
  return t("diagnosticActionInspectEvidence");
}

function diagnosticSignalTitle(t: Translate, signal: DiagnosticSignal): string {
  const title = translateDiagnosticKey(t, "diagnosticSignal", signal.kind, "Title");
  if (title) return title;
  return signal.kind ? humanizeKey(signal.kind) : t("unknown");
}

function diagnosticSignalDetail(t: Translate, signal: DiagnosticSignal): string {
  return translateDiagnosticKey(t, "diagnosticSignal", signal.kind, "Detail") || t("diagnosticEvidenceDetail");
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

function diagnosticCapabilityLabel(t: Translate, capability: Pick<DiagnosticCapability, "key" | "label">): string {
  if (capability.key === "passive_process_observer") return t("passiveProcessObserver");
  if (capability.key === "transcript_parser") return t("transcriptParser");
  if (capability.key === "system_resources") return t("systemResources");
  if (capability.key === "runtime_telemetry_adapter") return t("runtimeTelemetry");
  if (capability.key === "diagnostic_export") return t("diagnosticExport");
  return capability.label || capability.key || t("unknown");
}

function diagnosticSourceLabel(t: Translate, source?: string): string {
  return translateDiagnosticKey(t, "diagnosticSource", source, "Label") || (source ? humanizeKey(source) : "");
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

function toneFromStatus(status?: string): DiagnosticTone {
  const value = String(status || "").trim().toLowerCase();
  if (value === "ok" || value === "available") return "ok";
  if (value === "watch" || value === "partial" || value === "idle") return "watch";
  if (value === "warn" || value === "unavailable") return "warn";
  if (value === "empty") return "empty";
  return "muted";
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

function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0;
  return Math.max(0, Math.min(100, value));
}
