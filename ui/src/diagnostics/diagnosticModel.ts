import { formatDateTime, type Translate } from "../lib/format";
import type { DiagnosticBaseline, DiagnosticCapability, DiagnosticEvolutionInsight, DiagnosticSignal, RuntimeTelemetrySnapshot, Snapshot, TranscriptScanCost } from "../types/snapshot";

// How many signals the priority table renders. The header counts every signal,
// so the view model also reports how many this cap hid.
const PRIORITY_ROW_LIMIT = 6;

export type DiagnosticTone = "ok" | "watch" | "warn" | "empty" | "muted";
export type DiagnosticState = "measured" | "partial" | "unavailable" | "out_of_scope";

export type SituationMapCard = {
  key: string;
  label: string;
  value: string;
  detail: string;
  state: DiagnosticState;
  stateLabel: string;
  tone: DiagnosticTone;
};

export type LossLedgerRow = {
  key: string;
  title: string;
  current: string;
  evidenceFamily: string;
  scope: string;
  freshness: string;
  state: DiagnosticState;
  stateLabel: string;
  source: string;
  nextStep: string;
  tone: DiagnosticTone;
};

export type EvidenceMetric = {
  key: "session_evidence" | "pid_link" | "token_visible" | "low_confidence" | "walk_cost" | "walk_scope";
  label: string;
  value: string;
  detail: string;
  // A share renders a bar; a duration or a count has no denominator, so it
  // passes null and the bar is omitted rather than drawn at a token width.
  // Rendering "512ms" with a 2%-wide bar would invent a ratio nobody measured.
  percent: number | null;
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

export type EvolutionRow = {
  key: string;
  title: string;
  baseline: string;
  hypothesis: string;
  evidence: string;
  experiment: string;
  verification: string;
  stopCondition: string;
  metric: string;
  status: string;
  tone: DiagnosticTone;
  // Which priority rows this insight was actually built from, and how many of
  // them the capped table is not showing. Without this the card's headline
  // number appeared nowhere else on the page, so the reader had no way to get
  // from "111 stale sessions" to the evidence behind it.
  relatedSignals: string[];
  relatedHiddenCount: number;
};

export type DiagnosticViewModel = {
  generated: string;
  anomalyCount: number;
  gapCount: number;
  situationMap: SituationMapCard[];
  lossLedger: LossLedgerRow[];
  evidenceMetrics: EvidenceMetric[];
  priorityRows: PriorityRow[];
  // How many signals the priority table did not render. The header counts all
  // of them, so without this a capped table reads as "these are all the
  // findings" -- the sampling gap the panel exists to expose.
  hiddenSignalCount: number;
  evolutionRows: EvolutionRow[];
  chainNodes: ChainNode[];
  omittedFields: string[];
};

export function buildDiagnosticViewModel(t: Translate, snapshot: Snapshot): DiagnosticViewModel {
  const diagnostics = snapshot.diagnostics;
  const anomalies = diagnostics?.anomaly_signals ?? [];
  const gaps = diagnostics?.evidence_gaps ?? [];
  // Warn before info, so a capped table drops the least severe rows rather
  // than whichever list happened to be concatenated first. Anomalies used to
  // come wholesale before gaps, which meant six info-level anomalies could
  // push every warn-level evidence gap off the table.
  const signals = [...anomalies, ...gaps].slice().sort(
    (a, b) => signalSeverityRank(a.severity) - signalSeverityRank(b.severity),
  );
  const shown = signals.slice(0, PRIORITY_ROW_LIMIT);
  const hidden = signals.slice(PRIORITY_ROW_LIMIT);
  return {
    generated: diagnostics?.generated_at ? formatDateTime(diagnostics.generated_at) : snapshot.generated_at ? formatDateTime(snapshot.generated_at) : t("unavailable"),
    anomalyCount: anomalies.length,
    gapCount: gaps.length,
    situationMap: buildSituationMap(t, snapshot),
    lossLedger: buildLossLedger(t, snapshot),
    evidenceMetrics: buildEvidenceMetrics(t, diagnostics?.baselines ?? [], snapshot.transcript_stats?.scan_cost),
    priorityRows: buildPriorityRows(t, shown),
    hiddenSignalCount: hidden.length,
    evolutionRows: buildEvolutionRows(t, diagnostics?.evolution ?? [], shown, hidden),
    chainNodes: buildChainNodes(t, diagnostics?.capabilities ?? [], snapshot.runtime_telemetry),
    omittedFields: diagnostics?.export?.omitted_fields ?? [],
  };
}

function buildEvolutionRows(t: Translate, insights: DiagnosticEvolutionInsight[], shown: DiagnosticSignal[], hidden: DiagnosticSignal[]): EvolutionRow[] {
  return insights.map((insight, index) => {
    const key = insight.key || `evolution-${index}`;
    // signal_kinds names the signals the insight is actually built from. The
    // join used to be metric_key equality, but a metric key is a semantic
    // family shared by several unrelated signals, so the card built from stale
    // and recent-session counts traced itself to the low-confidence and
    // workitem-coverage rows -- a provenance line pointing at numbers it never
    // read. An insight that names no source shows no trace line rather than a
    // guessed one.
    const kinds = new Set(insight.signal_kinds ?? []);
    const builtFromSignal = (signal: DiagnosticSignal) => Boolean(signal.kind) && kinds.has(signal.kind as string);
    return {
      key,
      title: evolutionText(t, key, "Title", insight.title || humanizeKey(key)),
      baseline: evolutionText(t, key, "Baseline", insight.baseline || t("unavailable"), insight),
      hypothesis: evolutionText(t, key, "Hypothesis", insight.hypothesis || t("unavailable")),
      evidence: evolutionEvidenceText(t, key, insight),
      experiment: evolutionText(t, key, "Experiment", insight.experiment || t("unavailable")),
      verification: evolutionText(t, key, "Verification", insight.verification || t("unavailable")),
      stopCondition: evolutionText(t, key, "StopCondition", insight.stop_condition || t("unavailable"), insight),
      metric: metricKeyLabel(t, insight.metric_key, humanizeKey(insight.metric_key || "evidence")),
      status: evolutionStatusLabel(t, insight.status),
      tone: evolutionTone(insight.status),
      relatedSignals: shown.filter(builtFromSignal).map((signal) => diagnosticSignalTitle(t, signal)),
      relatedHiddenCount: hidden.filter(builtFromSignal).length,
    };
  });
}

function evolutionEvidenceText(t: Translate, key: string, insight: DiagnosticEvolutionInsight): string {
  const evidenceKey = insight.evidence_key || key;
  const translated = t(`diagnosticEvolution${normalizeI18nKey(evidenceKey)}Evidence`);
  const template = translated.startsWith("diagnosticEvolution") ? insight.evidence || t("unavailable") : translated;
  return template.replace(/\{([a-z0-9_]+)\}/gi, (_, name: string) => String(insight.evidence_values?.[name] ?? `{${name}}`));
}

function evolutionText(t: Translate, key: string, suffix: string, fallback: string, insight?: DiagnosticEvolutionInsight): string {
  const translated = t(`diagnosticEvolution${normalizeI18nKey(key)}${suffix}`);
  const value = translated.startsWith("diagnosticEvolution") ? fallback : translated;
  return value.replace(/\{([a-z0-9_]+)\}/gi, (_, name: string) => String(insight?.evidence_values?.[name] ?? `{${name}}`));
}

function evolutionStatusLabel(t: Translate, status?: string): string {
  if (status === "needs_review") return t("diagnosticEvolutionReview");
  if (status === "baseline") return t("diagnosticEvolutionBaseline");
  return diagnosticStatusLabel(t, status);
}

function evolutionTone(status?: string): DiagnosticTone {
  return status === "needs_review" ? "watch" : status === "baseline" ? "ok" : toneFromStatus(status);
}

// Severity is a stable sort key, so equal-severity signals keep the backend's
// own ordering (which diagnostics.go already sorts by kind and title).
function signalSeverityRank(severity?: string): number {
  const value = String(severity || "").trim().toLowerCase();
  if (value === "warn" || value === "warning" || value === "critical" || value === "error") return 0;
  if (value === "watch") return 1;
  return 2;
}

export function diagnosticOmittedFieldLabel(t: Translate, value: string): string {
  const normalized = normalizeI18nKey(value);
  const key = `diagnosticOmitted${normalized}`;
  const translated = t(key);
  return translated !== key ? translated : humanizeKey(value);
}

// buildSituationMap answers "what does this machine currently see" in one row
// of cards. Each card reports a count and the state of the evidence behind it,
// never a bare number: a zero the observer measured and a zero that means "we
// could not look" are different facts and must not render alike.
function buildSituationMap(t: Translate, snapshot: Snapshot): SituationMapCard[] {
  const current = snapshot.current;
  const stats = snapshot.transcript_stats;
  const baselines = new Map((snapshot.diagnostics?.baselines ?? []).map((baseline) => [baseline.key, baseline]));
  const processesMeasured = snapshot.process_stats !== undefined && !snapshot.process_stats.incomplete;
  const sessionsMeasured = snapshot.live_sessions !== undefined || (stats?.parsed_files ?? 0) > 0 || (current?.session_concurrency ?? 0) > 0;
  const processState = (value: number | undefined): DiagnosticState => value === undefined ? "unavailable" : snapshot.process_stats?.incomplete ? "partial" : processesMeasured ? "measured" : "unavailable";
  const sessionState = (value: number | undefined): DiagnosticState => value === undefined ? "unavailable" : sessionsMeasured ? "measured" : "unavailable";

  const card = (
    key: string,
    label: string,
    value: string,
    detail: string,
    state: DiagnosticState,
  ): SituationMapCard => ({
    key,
    label,
    value,
    detail,
    state,
    stateLabel: diagnosticStateLabel(t, state),
    tone: toneFromState(state),
  });

  return [
    card(
      "visible_processes",
      t("diagnosticSituationVisibleProcesses"),
      current?.pid_concurrency === undefined ? t("diagnosticNoData") : String(current.pid_concurrency),
      t("diagnosticSourceLiveProcesses"),
      processState(current?.pid_concurrency),
    ),
    card(
      "known_sessions",
      t("diagnosticSituationKnownSessions"),
      current?.session_concurrency === undefined ? t("diagnosticNoData") : String(current.session_concurrency),
      t("diagnosticSourceLiveSessions"),
      sessionState(current?.session_concurrency),
    ),
    card(
      "recent_movement",
      t("diagnosticSituationRecentMovement"),
      current?.active_burst_concurrency === undefined ? t("diagnosticNoData") : String(current.active_burst_concurrency),
      t("diagnosticSourceLiveSessions"),
      sessionState(current?.active_burst_concurrency),
    ),
    card(
      "pid_mapping",
      t("diagnosticSituationPidMapping"),
      diagnosticBaselineValue(t, baselines.get("mapping_coverage")),
      t("diagnosticSourceLiveProcesses"),
      baselineState(baselines.get("mapping_coverage")),
    ),
    card(
      "token_coverage",
      t("diagnosticSituationTokenCoverage"),
      diagnosticBaselineValue(t, baselines.get("token_measured_sessions")),
      t("diagnosticSourceLiveSessions"),
      baselineState(baselines.get("token_measured_sessions")),
    ),
  ];
}

// buildLossLedger names what this snapshot could NOT see, and for each gap says
// which evidence family it belongs to, how wide the gap is, and what the next
// step would be. It is the panel's honesty surface: a row here is a known
// blind spot, not a failure.
function buildLossLedger(t: Translate, snapshot: Snapshot): LossLedgerRow[] {
  const summary = snapshot.summary;
  const stats = snapshot.transcript_stats;
  const cost = stats?.scan_cost;
  const baselines = new Map((snapshot.diagnostics?.baselines ?? []).map((baseline) => [baseline.key, baseline]));
  const current = snapshot.current;
  const generated = snapshot.diagnostics?.generated_at || snapshot.generated_at;
  const sampleFreshness = generated ? formatDateTime(generated) : t("diagnosticFreshUnavailable");
  const visible = current?.pid_concurrency;
  const known = current?.session_concurrency;
  const processState: DiagnosticState = summary === undefined || visible === undefined ? "unavailable" : snapshot.process_stats?.incomplete ? "partial" : "measured";
  const lowState: DiagnosticState = snapshot.coordination_risk === undefined || known === undefined ? "unavailable" : "measured";
  const deferredFiles = stats?.deferred_files;
  const deferredState: DiagnosticState = stats === undefined || deferredFiles === undefined || (stats.coverage_incomplete === true && deferredFiles === 0 && !stats.historical_scan_deferred) ? "unavailable" : deferredFiles > 0 || stats.historical_scan_deferred ? "partial" : "measured";
  const deferredCurrent = stats === undefined || deferredFiles === undefined || (stats.coverage_incomplete === true && deferredFiles === 0 && !stats.historical_scan_deferred) ? t("unavailable") : deferredFiles > 0 ? formatCount(deferredFiles) : stats.historical_scan_deferred ? t("diagnosticPending") : formatCount(deferredFiles);
  const walkState: DiagnosticState = cost?.walk_measured === true && cost.elapsed_ms !== undefined ? "measured" : "unavailable";
  const agedState: DiagnosticState = cost?.walk_measured !== true || cost.aged_out_files === undefined ? "unavailable" : cost.aged_out_files > 0 ? "out_of_scope" : "measured";
  const token = baselines.get("token_measured_sessions");
  return [
    ledgerRow(t, "unmapped_pid", t("diagnosticLossUnmappedPidTitle"), summary?.unmapped_processes === undefined ? t("unavailable") : formatCount(summary.unmapped_processes), metricKeyLabel(t, "process_pressure", "process_pressure"), visible === undefined ? t("unavailable") : `${formatCount(visible)} ${t("diagnosticScopeVisiblePids")}`, sampleFreshness, processState, t("diagnosticSourceLiveProcesses"), t("diagnosticNextStepInspect"), toneFromState(processState)),
    ledgerRow(t, "low_confidence_sessions", t("diagnosticLossLowConfidenceTitle"), snapshot.coordination_risk?.low_confidence_session_count === undefined ? t("unavailable") : formatCount(snapshot.coordination_risk.low_confidence_session_count), metricKeyLabel(t, "known_sessions", "known_sessions"), known === undefined ? t("unavailable") : `${formatCount(known)} ${t("diagnosticScopeKnownSessions")}`, sampleFreshness, lowState, t("diagnosticSourceLiveSessions"), t("diagnosticNextStepInspect"), toneFromState(lowState)),
    ledgerRow(t, "deferred_transcript_scan", t("diagnosticLossDeferredTitle"), deferredCurrent, metricKeyLabel(t, "recent_movement", "recent_movement"), transcriptScope(t, stats), sampleFreshness, deferredState, t("diagnosticSourceTranscriptStats"), t("diagnosticNextStepRefresh"), toneFromState(deferredState)),
    ledgerRow(t, "token_coverage", t("diagnosticLossTokenTitle"), diagnosticBaselineValue(t, token), metricKeyLabel(t, "token_usage", "token_usage"), baselineScope(t, token), sampleFreshness, baselineState(token), t("diagnosticSourceLiveSessions"), t("diagnosticNextStepInspect"), toneFromState(baselineState(token))),
    ledgerRow(t, "evidence_walk_cost", t("diagnosticLossWalkTitle"), walkCurrent(t, cost), metricKeyLabel(t, "recent_movement", "recent_movement"), walkScope(t, cost), cost?.measured_at ? formatDateTime(cost.measured_at) : sampleFreshness, walkState, t("diagnosticSourceTranscriptStats"), t("diagnosticNextStepRefresh"), toneFromState(walkState)),
    ledgerRow(t, "evidence_out_of_horizon", t("diagnosticLossOutOfScopeTitle"), agedCurrent(t, cost), metricKeyLabel(t, "recent_movement", "recent_movement"), historyScope(t, stats), cost?.measured_at ? formatDateTime(cost.measured_at) : sampleFreshness, agedState, t("diagnosticSourceTranscriptStats"), t("diagnosticNextStepHorizon"), toneFromState(agedState)),
  ];
}

function ledgerRow(t: Translate, key: string, title: string, current: string, evidenceFamily: string, scope: string, freshness: string, state: DiagnosticState, source: string, nextStep: string, tone: DiagnosticTone): LossLedgerRow {
  return { key, title, current, evidenceFamily, scope, freshness, state, stateLabel: diagnosticStateLabel(t, state), source, nextStep, tone };
}

function baselineState(baseline?: DiagnosticBaseline): DiagnosticState {
  const value = String(baseline?.value || "").trim();
  const status = String(baseline?.status || "").toLowerCase();
  if (!baseline || !value || value === "no_data" || status === "unavailable" || status === "empty") return "unavailable";
  const ratio = value.match(/([0-9]+)\s+of\s+([0-9]+)/i);
  if (status === "partial" || (ratio && ratio[1] !== ratio[2])) return "partial";
  return "measured";
}

function transcriptScope(t: Translate, stats?: Snapshot["transcript_stats"]): string {
  if (!stats) return t("unavailable");
  if (stats.coverage_incomplete === true && (stats.scanned_files ?? 0) === 0 && (stats.deferred_files ?? 0) === 0) return t("unavailable");
  const eligible = (stats.scanned_files ?? 0) + (stats.deferred_files ?? 0);
  return `${formatCount(eligible)} ${t("diagnosticScopeForegroundFiles")}`;
}

function historyScope(t: Translate, stats?: Snapshot["transcript_stats"]): string {
  if (!stats) return t("unavailable");
  const seconds = stats.configured_history_lookback_seconds;
  return seconds && seconds > 0 ? `${formatDuration(seconds)} ${t("diagnosticScopeHistoryHorizon")}` : t("diagnosticScopeHistoryHorizon");
}

function baselineScope(t: Translate, baseline?: DiagnosticBaseline): string {
  const ratio = String(baseline?.value || "").match(/([0-9]+)\s+of\s+([0-9]+)/i);
  return ratio ? `${ratio[2]} ${t("diagnosticScopeKnownSessions")}` : t("unavailable");
}

function walkCurrent(t: Translate, cost?: TranscriptScanCost): string {
  return cost?.walk_measured === true && cost.elapsed_ms !== undefined ? `${formatCount(cost.elapsed_ms)}ms` : t("unavailable");
}

function walkScope(t: Translate, cost?: TranscriptScanCost): string {
  return cost?.walk_measured === true && cost.visited_entries !== undefined ? `${formatCount(cost.visited_entries)} ${t("diagnosticScopeVisitedEntries")}` : t("unavailable");
}

function agedCurrent(t: Translate, cost?: TranscriptScanCost): string {
  return cost?.walk_measured === true && cost.aged_out_files !== undefined ? `${formatCount(cost.aged_out_files)} ${t("diagnosticScopeFiles")}` : t("unavailable");
}

function formatDuration(seconds: number): string {
  if (seconds >= 86400) return `${Math.round(seconds / 86400)}d`;
  if (seconds >= 3600) return `${Math.round(seconds / 3600)}h`;
  return `${Math.round(seconds / 60)}m`;
}

function diagnosticStateLabel(t: Translate, state: DiagnosticState): string {
  if (state === "measured") return t("diagnosticStatusMeasured");
  if (state === "partial") return t("diagnosticStatusPartial");
  if (state === "out_of_scope") return t("diagnosticStatusOutOfScope");
  return t("diagnosticStatusUnavailable");
}

// out_of_scope is muted, not warned: a file outside the configured horizon is a
// documented boundary, not a defect. Only a reading we expected and did not get
// earns the warn tone.
function toneFromState(state: DiagnosticState): DiagnosticTone {
  if (state === "measured") return "ok";
  if (state === "partial") return "watch";
  if (state === "unavailable") return "warn";
  return "muted";
}

function buildEvidenceMetrics(t: Translate, baselines: DiagnosticBaseline[], scanCost?: TranscriptScanCost): EvidenceMetric[] {
  const byKey = new Map(baselines.map((baseline) => [baseline.key, baseline]));
  const session = byKey.get("active_session_ratio");
  const mapping = byKey.get("mapping_coverage");
  const token = byKey.get("token_measured_sessions");
  const lowConfidence = byKey.get("low_confidence_sessions");
  const walk = byKey.get("evidence_walk_cost");
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
    {
      key: "low_confidence",
      label: t("diagnosticLowConfidenceSessions"),
      value: diagnosticBaselineValue(t, lowConfidence),
      detail: t("diagnosticLowConfidenceSessionsDetail"),
      // A bare count of weak sessions has no denominator on this row -- the
      // total lives in the session-evidence cell, and dividing here would
      // silently invent a second definition of "all sessions".
      percent: null,
      tone: toneFromStatus(lowConfidence?.status),
    },
    walkCostMetric(t, walk, scanCost),
    walkScopeMetric(t, scanCost),
  ];
}

// walkCostMetric renders the last evidence walk's duration.
//
// The index reconciles roughly once per process, so in steady state this
// number was measured minutes ago and is still the best answer available --
// a walk's cost does not change until the next walk. Blanking it because it
// was not measured on this exact pass would make the cell permanently empty,
// which is the same uselessness as a permanently-zero counter. So the value is
// shown with a line saying WHEN it was measured, and only a walk that has
// never run reads as no data.
function walkCostMetric(t: Translate, baseline?: DiagnosticBaseline, scanCost?: TranscriptScanCost): EvidenceMetric {
  const measured = scanCost?.walk_measured === true;
  if (!measured) {
    return {
      key: "walk_cost",
      label: t("diagnosticWalkCost"),
      value: t("diagnosticNoData"),
      detail: t("diagnosticWalkCostUnmeasured"),
      percent: null,
      tone: "muted",
    };
  }
  const fresh = scanCost?.walk_fresh === true;
  const at = scanCost?.measured_at ? formatDateTime(scanCost.measured_at) : "";
  return {
    key: "walk_cost",
    label: t("diagnosticWalkCost"),
    // The backend already formats this and already guards the unmeasured case
    // (diagnostics.go scanCostValue); re-deriving it here would be a second
    // opinion on the same fact.
    value: diagnosticBaselineValue(t, baseline),
    detail: fresh || !at
      ? t("diagnosticWalkCostFresh")
      : t("diagnosticWalkCostStale").replace("{at}", at),
    percent: null,
    tone: toneFromStatus(baseline?.status),
  };
}

// walkScopeMetric reports how much ground the walk covered. Visited and pruned
// come from the same measurement as the duration, so they share its guard: no
// walk means no scope, not a scope of zero.
function walkScopeMetric(t: Translate, scanCost?: TranscriptScanCost): EvidenceMetric {
  if (scanCost?.walk_measured !== true) {
    return {
      key: "walk_scope",
      label: t("diagnosticWalkScope"),
      value: t("diagnosticNoData"),
      detail: t("diagnosticWalkScopeUnmeasured"),
      percent: null,
      tone: "muted",
    };
  }
  const visited = scanCost.visited_entries ?? 0;
  const pruned = scanCost.pruned_directories ?? 0;
  return {
    key: "walk_scope",
    label: t("diagnosticWalkScope"),
    value: formatCount(visited),
    detail: t("diagnosticWalkScopeDetail")
      .replace("{visited}", formatCount(visited))
      .replace("{pruned}", formatCount(pruned)),
    percent: null,
    tone: "ok",
  };
}

function formatCount(value: number): string {
  return Number.isFinite(value) ? value.toLocaleString() : "0";
}

function buildPriorityRows(t: Translate, signals: DiagnosticSignal[]): PriorityRow[] {
  return signals.slice(0, PRIORITY_ROW_LIMIT).map((signal, index) => {
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

function diagnosticBaselineValue(t: Translate, baseline?: DiagnosticBaseline): string {
  const value = String(baseline?.value || "").trim();
  if (!value) return t("unavailable");
  // The backend spells "we never measured this" as the metric-semantics token
  // no_data. Falling through would print that identifier raw in all three
  // locales, which reads like a bug rather than like an honest blank.
  if (value === "no_data") return t("diagnosticNoData");
  if (value === "no visible PIDs") return t("noVisiblePids");
  const ratio = value.match(/^([0-9]+)\s+of\s+([0-9]+)$/i);
  if (ratio) return `${ratio[1]} / ${ratio[2]}`;
  return value;
}

function coverageDetail(t: Translate, baseline: DiagnosticBaseline | undefined, key: string): string {
  const pct = baselinePercent(baseline);
  // Only an unparseable value is unavailable. A measured 0% is a measurement,
  // and reporting it as "n/a" hides a real reading behind an honest-unknown
  // word -- the mirror image of reporting an unknown as zero.
  if (pct === null) return t("unavailable");
  return `${pct.toFixed(pct >= 10 ? 1 : 2)}% ${t(key)}`;
}

// null, not 0, when the value carries no share. A baseline that is missing or
// reads "no_data" has no ratio at all, and returning 0 let the CSS floor
// (max(2%, ...)) paint a thin bar next to "n/a" -- a measurement rendered for
// a measurement that does not exist.
function baselinePercent(baseline?: DiagnosticBaseline): number | null {
  const value = String(baseline?.value || "").trim();
  const percent = value.match(/([0-9]+(?:\.[0-9]+)?)%/);
  if (percent) return clampPercent(Number(percent[1]));
  const ratio = value.match(/([0-9]+)\s+of\s+([0-9]+)/i);
  if (ratio) return clampPercent((Number(ratio[1]) / Math.max(1, Number(ratio[2]))) * 100);
  return null;
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
  if (key === "coordination_risk") return "risk";
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
  // "observed" means measured-but-unjudged: the backend has a number and no
  // documented budget to compare it against. Listed rather than left to the
  // fallback so that renaming it shows up here instead of silently going muted.
  if (value === "observed") return "muted";
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
