import { Activity, ChevronDown, Cpu, HardDrive, MemoryStick, Network, Server, Thermometer } from "lucide-react";
import { useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { clampPct, formatAge, formatBytesPerSecond, formatCompactCPU, formatLoadAverage, formatMemory, formatPacketRate, formatPct, formatPrecisePct, type Translate } from "../lib/format";
import type { SystemResourceSnapshot } from "../types/snapshot";
import type { SystemResourceHistoryPoint } from "./useLiveSystemResources";

type SystemMetricKey = "cpu" | "memory" | "disk" | "network" | "thermal" | "agent";
type TrendKind = "cpu" | "memory" | "disk" | "network" | "thermal" | "none";
type SystemMetricTone = "pressure" | "network" | "thermal" | "agent";

type ResourceFact = { label: string; value: string };
type SystemMetric = {
  key: SystemMetricKey;
  label: string;
  icon: ReactNode;
  value: string;
  detail: string;
  score: number;
  available: boolean;
  tone: SystemMetricTone;
  facts: ResourceFact[];
  source: string;
  scope: string;
  trend: TrendKind;
};

type Props = {
  t: Translate;
  resources?: SystemResourceSnapshot;
  history: SystemResourceHistoryPoint[];
  processCount: number;
  processCPU: number;
  processMemory: number;
  mappedProcesses: number;
  unmappedProcesses: number;
  processEvidenceComplete: boolean;
};

export function SystemResourceDeck({
  t,
  resources,
  history,
  processCount,
  processCPU,
  processMemory,
  mappedProcesses,
  unmappedProcesses,
  processEvidenceComplete,
}: Props) {
  const [selectedMetric, setSelectedMetric] = useState<SystemMetricKey>("network");
  const [detailExpanded, setDetailExpanded] = useState(false);
  const metrics = useMemo(() => buildMetrics({
    t,
    resources,
    processCount,
    processCPU,
    processMemory,
    mappedProcesses,
    unmappedProcesses,
    processEvidenceComplete,
  }), [mappedProcesses, processCPU, processCount, processEvidenceComplete, processMemory, resources, t, unmappedProcesses]);
  const selected = metrics.find((metric) => metric.key === selectedMetric) ?? metrics[0];
  const headlineMetrics = metrics.filter((metric) => metric.key === "cpu" || metric.key === "memory" || metric.key === "disk");
  const diskMetric = metrics.find((metric) => metric.key === "disk");
  const supportMetrics = metrics.filter((metric) => metric.key === "network" || metric.key === "thermal" || metric.key === "agent");
  const selectMetric = (key: SystemMetricKey) => {
    setSelectedMetric(key);
    setDetailExpanded(false);
  };

  return (
    <section className="system-resource-deck" aria-label={t("systemResources")}>
      <div className="system-resource-grid" role="toolbar" aria-label={t("systemResourceSelector")}>
        {headlineMetrics.map((metric) => (
          <SystemMetricHeadline
            key={metric.key}
            metric={metric}
            selected={metric.key === selectedMetric}
            onSelect={() => selectMetric(metric.key)}
          />
        ))}
      </div>
      <div className="system-resource-support-list" role="toolbar" aria-label={t("systemResourceSelector")}>
        {diskMetric ? <SystemMetricRow
          metric={diskMetric}
          selected={diskMetric.key === selectedMetric}
          onSelect={() => selectMetric(diskMetric.key)}
          value={`${t("available")} ${formatMemory(resources?.disk_free_bytes, t)} / ${formatMemory(resources?.disk_total_bytes, t)}`}
          detail={formatPrecisePct(resources?.disk_used_pct)}
        /> : null}
        {supportMetrics.map((metric) => (
          <SystemMetricRow
            key={metric.key}
            metric={metric}
            selected={metric.key === selectedMetric}
            onSelect={() => selectMetric(metric.key)}
          />
        ))}
      </div>
      {selected ? <SystemResourceInspector t={t} metric={selected} history={history} expanded={detailExpanded} onToggle={() => setDetailExpanded((value) => !value)} /> : null}
      {resources?.supported === false ? <p className="system-resource-note">{(resources.notes ?? []).join(" · ") || t("unsupported")}</p> : null}
    </section>
  );
}

function buildMetrics({
  t,
  resources,
  processCount,
  processCPU,
  processMemory,
  mappedProcesses,
  unmappedProcesses,
  processEvidenceComplete,
}: Omit<Props, "history">): SystemMetric[] {
  const cpu = resources?.cpu_percent ?? 0;
  const memoryPct = resources?.memory_used_pct ?? 0;
  const diskPct = resources?.disk_used_pct ?? 0;
  const rxRate = resources?.network_rx_bytes_per_sec ?? 0;
  const txRate = resources?.network_tx_bytes_per_sec ?? 0;
  const packetRate = (resources?.network_rx_packets_per_sec ?? 0) + (resources?.network_tx_packets_per_sec ?? 0);
  const packetIssueRate = (resources?.network_error_packets_per_sec ?? 0) + (resources?.network_dropped_packets_per_sec ?? 0);
  const networkIntensity = clampPct(((rxRate + txRate) / 2_000_000) * 100, 4);
  const thermal = thermalReading(t, resources?.thermal_state, resources?.thermal_state_supported);
  const hasRateSample = typeof resources?.sample_interval_seconds === "number" && resources.sample_interval_seconds > 0;
  const hasMemory = (resources?.memory_total_bytes ?? 0) > 0;
  const hasDisk = (resources?.disk_total_bytes ?? 0) > 0;
  const processAvailable = processEvidenceComplete;

  return [
    {
      key: "cpu",
      label: t("systemCpu"),
      icon: <Cpu size={14} />,
      value: hasRateSample ? formatCompactCPU(cpu) : t("unavailable"),
      detail: `${t("loadAvg")} ${formatLoadAverage(resources?.load_average_1)} / ${formatLoadAverage(resources?.load_average_5)}`,
      score: cpu,
      available: hasRateSample,
      tone: "pressure",
      facts: [
        { label: t("usage"), value: hasRateSample ? formatCompactCPU(cpu) : t("unavailable") },
        { label: t("loadAvg"), value: `${formatLoadAverage(resources?.load_average_1)} / ${formatLoadAverage(resources?.load_average_5)} / ${formatLoadAverage(resources?.load_average_15)}` },
        { label: t("uptime"), value: formatAge(resources?.uptime_seconds, t) },
        { label: t("sampleInterval"), value: formatSampleInterval(resources?.sample_interval_seconds, t) },
      ],
      source: t("systemSourceHostStats"),
      scope: t("resourceScopeWholeMachine"),
      trend: "cpu",
    },
    {
      key: "memory",
      label: t("systemMemory"),
      icon: <MemoryStick size={14} />,
      value: hasMemory ? formatPct(memoryPct) : t("unavailable"),
      detail: `${formatMemory(resources?.memory_used_bytes, t)} / ${formatMemory(resources?.memory_total_bytes, t)}`,
      score: memoryPct,
      available: hasMemory,
      tone: "pressure",
      facts: [
        { label: t("used"), value: formatMemory(resources?.memory_used_bytes, t) },
        { label: t("available"), value: formatMemory(resources?.memory_free_bytes, t) },
        { label: t("total"), value: formatMemory(resources?.memory_total_bytes, t) },
        { label: t("usage"), value: formatPrecisePct(memoryPct) },
      ],
      source: t("systemSourceMemory"),
      scope: t("resourceScopeWholeMachine"),
      trend: "memory",
    },
    {
      key: "disk",
      label: t("systemDisk"),
      icon: <HardDrive size={14} />,
      value: hasDisk ? formatPct(diskPct) : t("unavailable"),
      detail: `${t("available")} ${formatMemory(resources?.disk_free_bytes, t)}`,
      score: diskPct,
      available: hasDisk,
      tone: "pressure",
      facts: [
        { label: t("used"), value: formatMemory(resources?.disk_used_bytes, t) },
        { label: t("available"), value: formatMemory(resources?.disk_free_bytes, t) },
        { label: t("total"), value: formatMemory(resources?.disk_total_bytes, t) },
        { label: t("usage"), value: formatPrecisePct(diskPct) },
      ],
      source: t("systemSourceDisk"),
      scope: t("resourceScopeRootVolume"),
      trend: "disk",
    },
    {
      key: "network",
      label: t("networkFlow"),
      icon: <Network size={14} />,
      value: hasRateSample ? `${formatBytesPerSecond(rxRate, t)} / ${formatBytesPerSecond(txRate, t)}` : t("unavailable"),
      detail: `${t("inbound")} / ${t("outbound")}`,
      score: networkIntensity,
      available: hasRateSample,
      tone: "network",
      facts: [
        { label: t("inbound"), value: hasRateSample ? formatBytesPerSecond(rxRate, t) : t("unavailable") },
        { label: t("outbound"), value: hasRateSample ? formatBytesPerSecond(txRate, t) : t("unavailable") },
        { label: t("packetRate"), value: hasRateSample ? formatPacketRate(packetRate, t) : t("unavailable") },
        { label: t("packetIssue"), value: hasRateSample ? formatPrecisePct(resources?.network_packet_issue_pct) : t("unavailable") },
        { label: t("packetIssueRate"), value: hasRateSample ? formatPacketRate(packetIssueRate, t) : t("unavailable") },
        { label: t("interfaces"), value: String(resources?.network_interface_count ?? 0) },
      ],
      source: t("systemSourceNetwork"),
      scope: t("resourceScopeNetworkInterfaces"),
      trend: "network",
    },
    {
      key: "thermal",
      label: t("thermalState"),
      icon: <Thermometer size={14} />,
      value: thermal.label,
      detail: thermal.detail,
      score: thermal.score,
      available: thermal.available,
      tone: "thermal",
      facts: [
        { label: t("thermalState"), value: thermal.label },
        { label: t("temperature"), value: t("sensorUnavailable") },
        { label: t("fanSpeed"), value: t("sensorUnavailable") },
        { label: t("sampleInterval"), value: formatSampleInterval(resources?.sample_interval_seconds, t) },
      ],
      source: t("systemSourceThermal"),
      scope: t("resourceScopeThermal"),
      trend: "thermal",
    },
    {
      key: "agent",
      label: t("agentProcessLoad"),
      icon: <Server size={14} />,
      value: processAvailable ? String(processCount) : t("unavailable"),
      detail: processAvailable ? `${formatCompactCPU(processCPU)} · ${formatMemory(processMemory, t)}` : t("processEvidenceIncomplete"),
      score: processAvailable ? Math.min(100, processCPU) : 0,
      available: processAvailable,
      tone: "agent",
      facts: [
        { label: t("processes"), value: processAvailable ? String(processCount) : t("unavailable") },
        { label: t("mapped"), value: processAvailable ? String(mappedProcesses) : t("unavailable") },
        { label: t("unmapped"), value: processAvailable ? String(unmappedProcesses) : t("unavailable") },
        { label: t("processCPU"), value: processAvailable ? formatCompactCPU(processCPU) : t("unavailable") },
        { label: t("processMemory"), value: processAvailable ? formatMemory(processMemory, t) : t("unavailable") },
      ],
      source: t("systemSourceObservedProcesses"),
      scope: t("resourceScopeVisibleProcesses"),
      trend: "none",
    },
  ];
}

function SystemMetricHeadline({ metric, selected, onSelect }: { metric: SystemMetric; selected: boolean; onSelect: () => void }) {
  return (
    <button
      type="button"
      className={`system-metric-headline ${metric.key}${selected ? " is-selected" : ""}${metric.available ? "" : " is-unavailable"}`}
      style={systemMetricStyle(metric.score, metric.tone)}
      aria-pressed={selected}
      onClick={onSelect}
    >
      <strong>{metric.value}</strong>
      <span>{metric.label}</span>
      <em>{metric.detail}</em>
    </button>
  );
}

function SystemMetricRow({
  metric,
  selected,
  onSelect,
  value = metric.value,
  detail = metric.detail,
}: {
  metric: SystemMetric;
  selected: boolean;
  onSelect: () => void;
  value?: string;
  detail?: string;
}) {
  return (
    <button
      type="button"
      className={`system-metric-row ${metric.key}${selected ? " is-selected" : ""}${metric.available ? "" : " is-unavailable"}`}
      style={systemMetricStyle(metric.score, metric.tone)}
      aria-pressed={selected}
      onClick={onSelect}
    >
      <span className="system-metric-row-label">{metric.icon}<b>{metric.label}</b></span>
      <strong>{value}</strong>
      <em>{detail}</em>
    </button>
  );
}

function SystemResourceInspector({ t, metric, history, expanded, onToggle }: { t: Translate; metric: SystemMetric; history: SystemResourceHistoryPoint[]; expanded: boolean; onToggle: () => void }) {
  return (
    <section className={`system-resource-inspector${expanded ? " is-expanded" : ""}`} style={systemMetricStyle(metric.score, metric.tone)} aria-live="polite">
      <header className="system-resource-inspector-head">
        <span className="system-resource-inspector-title">
          {metric.icon}
          <span><b>{t("metricDetails")}</b><strong>{metric.label}</strong></span>
        </span>
        <button type="button" className="system-resource-inspector-toggle" aria-label={t(expanded ? "collapseDetails" : "expandDetails")} aria-expanded={expanded} onClick={onToggle}>
          <ChevronDown size={15} />
        </button>
      </header>
      <SystemMetricTrend t={t} kind={metric.trend} history={history} available={metric.available} />
      {expanded ? <>
        <dl className="system-resource-facts">
          {metric.facts.map((fact) => (
            <div key={fact.label}>
              <dt>{fact.label}</dt>
              <dd>{fact.value}</dd>
            </div>
          ))}
        </dl>
        <footer className="system-resource-inspector-foot">
          <span><b>{t("source")}</b>{metric.source}</span>
          <span><b>{t("scope")}</b>{metric.scope}</span>
        </footer>
      </> : null}
    </section>
  );
}

function SystemMetricTrend({ t, kind, history, available }: { t: Translate; kind: TrendKind; history: SystemResourceHistoryPoint[]; available: boolean }) {
  const primaryValues = history.map((sample) => valueForTrend(kind, sample));
  const hasTrend = available && kind !== "none" && primaryValues.length >= 2;
  if (!hasTrend) {
    return <p className="system-resource-trend-empty"><Activity size={13} />{t(kind === "none" ? "processLedgerBelow" : available ? "trendPending" : "unavailable")}</p>;
  }
  const networkOutbound = kind === "network" ? history.map((sample) => sample.network_tx_bytes_per_sec ?? 0) : [];
  const max = Math.max(1, ...primaryValues, ...networkOutbound);
  return (
    <figure className="system-resource-trend" aria-label={`${t("resourceTrend")} · ${t("observedSinceOpen")}`}>
      <figcaption><span>{t("resourceTrend")}</span><em>{t("observedSinceOpen")} · {history.length} {t("samples")}</em></figcaption>
      <svg viewBox="0 0 240 38" preserveAspectRatio="none" role="img" aria-label={t("resourceTrend")}>
        <path className="system-resource-trend-area" d={areaPath(primaryValues, max)} />
        <path className="system-resource-trend-line" d={linePath(primaryValues, max)} />
        {kind === "network" ? <path className="system-resource-trend-line is-outbound" d={linePath(networkOutbound, max)} /> : null}
      </svg>
    </figure>
  );
}

function valueForTrend(kind: TrendKind, sample: SystemResourceHistoryPoint): number {
  if (kind === "cpu") return sample.cpu_percent ?? 0;
  if (kind === "memory") return sample.memory_used_pct ?? 0;
  if (kind === "disk") return sample.disk_used_pct ?? 0;
  if (kind === "network") return sample.network_rx_bytes_per_sec ?? 0;
  if (kind === "thermal") return thermalScore(sample.thermal_state);
  return 0;
}

function linePath(values: number[], max: number): string {
  if (!values.length) return "";
  return values.map((value, index) => {
    const x = values.length === 1 ? 0 : (index / (values.length - 1)) * 240;
    const y = 35 - clampPct((value / max) * 100, 0) * .31;
    return `${index === 0 ? "M" : "L"}${x.toFixed(2)} ${y.toFixed(2)}`;
  }).join(" ");
}

function areaPath(values: number[], max: number): string {
  const line = linePath(values, max);
  if (!line) return "";
  return `${line} L240 38 L0 38 Z`;
}

function thermalReading(t: Translate, value: SystemResourceSnapshot["thermal_state"], supported?: boolean) {
  if (!supported) return { label: t("sensorUnavailable"), detail: t("thermalUnavailableDetail"), score: 0, available: false };
  const label = thermalLabel(t, value);
  return { label, detail: t("thermalStateDetail"), score: thermalScore(value), available: true };
}

function thermalLabel(t: Translate, value: SystemResourceSnapshot["thermal_state"]): string {
  if (value === "fair") return t("thermalFair");
  if (value === "serious") return t("thermalSerious");
  if (value === "critical") return t("thermalCritical");
  return t("thermalNominal");
}

function thermalScore(value: SystemResourceSnapshot["thermal_state"]): number {
  if (value === "fair") return 45;
  if (value === "serious") return 75;
  if (value === "critical") return 100;
  return 15;
}

function formatSampleInterval(value: number | undefined, t: Translate): string {
  if (typeof value !== "number" || value <= 0) return t("unavailable");
  return formatAge(value, t);
}

// Bands resolve to per-theme tokens declared in styles/system-resource-inspector.css.
type SystemMetricBand = "network" | "agent" | "cool" | "critical" | "high" | "elevated" | "calm";

function systemMetricStyle(value: number, tone: SystemMetricTone): CSSProperties {
  const pct = clampPct(value, 0);
  const band: SystemMetricBand = tone === "network"
    ? "network"
    : tone === "agent"
      ? "agent"
      : tone === "thermal" && pct < 55
        ? "cool"
        : pct >= 90
          ? "critical"
          : pct >= 75
            ? "high"
            : pct >= 55
              ? "elevated"
              : "calm";
  return {
    "--system-pct": `${pct}%`,
    "--metric-color": `var(--sysband-${band}-color)`,
    "--metric-strong": `var(--sysband-${band}-strong)`,
    "--metric-soft": `var(--sysband-${band}-soft)`,
    "--metric-line": `var(--sysband-${band}-line)`,
    "--metric-track": `color-mix(in srgb, var(--sysband-${band}-color) 11%, transparent)`,
  } as CSSProperties;
}
