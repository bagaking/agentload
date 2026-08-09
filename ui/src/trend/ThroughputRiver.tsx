import { useMemo, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from "react";
import { formatTokenRate, type Translate } from "../lib/format";
import { trendOutputThroughputActiveSessions, trendOutputThroughputProjects, trendOutputThroughputValue } from "../lib/metricSemantics";
import type { TrendPoint } from "./types";

const WIDTH = 640;
const HEIGHT = 180;
const PAD = { top: 28, right: 8, bottom: 22, left: 8 };
const UNASSIGNED_PROJECT = "unassigned";
const PROJECT_COLORS = [
  "#43c99a", "#5aa7ff", "#e9b84b", "#e9786d", "#9d86ed", "#4cc5ca",
  "#d77eb4", "#91c95c", "#e39a59", "#718ed7", "#4faa76", "#be7bd0",
];

type RiverDatum = {
  at: string;
  timestamp: number;
  total: number;
  point: TrendPoint;
  projects: Map<string, number>;
};

type RiverLayer = {
  key: string;
  color: string;
  lower: number[];
  upper: number[];
};

export function ThroughputRiver({
  t,
  title,
  points,
  selectedAt,
  onSelect,
}: {
  t: Translate;
  title: string;
  points: TrendPoint[];
  selectedAt?: string;
  onSelect: (at?: string) => void;
}) {
  const model = useMemo(() => buildRiverModel(points), [points]);
  const fallbackIndex = model.data.length - 1;
  const matchedIndex = model.data.findIndex((datum) => datum.at === selectedAt);
  const selectedIndex = matchedIndex >= 0 ? matchedIndex : fallbackIndex;
  const [hoverIndex, setHoverIndex] = useState<number | null>(null);
  const activeIndex = hoverIndex ?? (selectedIndex >= 0 ? selectedIndex : fallbackIndex);
  const active = model.data[activeIndex] ?? model.data[fallbackIndex];
  const activeX = pointX(active?.timestamp ?? model.minAt, model.minAt, model.maxAt);
  const activeY = valueY(active?.total ?? 0, model.maxTotal);
  const activeProjects = active ? riverProjectRows(active, model.colors, t) : [];
  const visibleLegend = model.layers.slice(0, 5);

  const updateHover = (event: PointerEvent<SVGSVGElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    if (!rect.width || !model.data.length) return;
    const viewX = ((event.clientX - rect.left) / rect.width) * WIDTH;
    const ratio = (viewX - PAD.left) / (WIDTH - PAD.left - PAD.right);
    const targetAt = model.minAt + Math.max(0, Math.min(1, ratio)) * (model.maxAt - model.minAt);
    setHoverIndex(nearestDatumIndex(model.data, targetAt));
  };
  const handleKey = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!model.data.length) return;
    const current = hoverIndex ?? (selectedIndex >= 0 ? selectedIndex : fallbackIndex);
    if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
      event.preventDefault();
      const direction = event.key === "ArrowLeft" ? -1 : 1;
      setHoverIndex(Math.max(0, Math.min(model.data.length - 1, current + direction)));
    }
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onSelect(model.data[current]?.at);
    }
  };

  return (
    <div
      className="trend-chart throughput-river-chart"
      role="img"
      aria-label={`${title} · ${t("throughputProjectSplit")}`}
      tabIndex={0}
      onKeyDown={handleKey}
      onPointerLeave={() => setHoverIndex(null)}
    >
      <div className="throughput-river-legend" aria-hidden="true">
        {visibleLegend.map((layer) => (
          <span key={layer.key}><i style={{ backgroundColor: layer.color }} />{riverProjectLabel(layer.key, t)}</span>
        ))}
        {model.layers.length > visibleLegend.length ? <span>+{model.layers.length - visibleLegend.length}</span> : null}
      </div>
      <svg viewBox={`0 0 ${WIDTH} ${HEIGHT}`} preserveAspectRatio="none" onPointerMove={updateHover} onClick={() => onSelect(active?.at)}>
        {[0.25, 0.5, 0.75].map((ratio) => (
          <line key={ratio} className="throughput-river-grid" x1={PAD.left} x2={WIDTH - PAD.right} y1={valueY(model.maxTotal * ratio, model.maxTotal)} y2={valueY(model.maxTotal * ratio, model.maxTotal)} />
        ))}
        {model.layers.map((layer) => (
          <path key={layer.key} className="throughput-river-layer" d={layerPath(layer, model.data, model.minAt, model.maxAt, model.maxTotal)} fill={layer.color} />
        ))}
        {active ? (
          <g className="throughput-river-selection" aria-hidden="true">
            <line x1={activeX} x2={activeX} y1={PAD.top} y2={HEIGHT - PAD.bottom} />
            <circle cx={activeX} cy={activeY} r="4" />
          </g>
        ) : null}
      </svg>
      {active ? (
        <div
          className={`throughput-river-tooltip ${activeY < HEIGHT * 0.43 ? "is-below" : "is-above"}`}
          style={{ "--river-x": `${(activeX / WIDTH) * 100}%`, "--river-y": `${(activeY / HEIGHT) * 100}%` } as CSSProperties}
          aria-hidden="true"
        >
          <strong>{formatRiverTime(active.at)}</strong>
          <span><b>{t("outputThroughput")}</b><em>{formatTokenRate(active.total)} {t("tokenRateUnit")}</em></span>
          <span><b>{t("trendReadoutContributors")}</b><em>{trendOutputThroughputActiveSessions(active.point) ?? 0}</em></span>
        </div>
      ) : null}
      <div className="throughput-river-projects" aria-label={t("throughputProjectSplit")}>
        {activeProjects.map((project) => (
          <span key={project.key} title={`${project.label}: ${formatTokenRate(project.value)} ${t("tokenRateUnit")}`}>
            <i style={{ backgroundColor: project.color }} />
            <b>{project.label}</b>
            <em>{formatTokenRate(project.value)}</em>
          </span>
        ))}
      </div>
    </div>
  );
}

function buildRiverModel(points: TrendPoint[]): { data: RiverDatum[]; layers: RiverLayer[]; colors: Map<string, string>; maxTotal: number; minAt: number; maxAt: number } {
  const data = points.flatMap((point): RiverDatum[] => {
    const at = String(point.at || "");
    const timestamp = Date.parse(at);
    const total = trendOutputThroughputValue(point);
    const partition = trendOutputThroughputProjects(point);
    if (!at || !Number.isFinite(timestamp) || total === null || partition === null) return [];
    const projects = new Map<string, number>();
    for (const project of partition) {
      const key = String(project.project);
      projects.set(key, (projects.get(key) ?? 0) + (project.output_tokens_per_second ?? 0));
    }
    return [{ at, timestamp, total, point, projects }];
  }).sort((a, b) => a.timestamp - b.timestamp);
  const totals = new Map<string, number>();
  data.forEach((datum) => datum.projects.forEach((value, key) => totals.set(key, (totals.get(key) ?? 0) + value)));
  const keys = Array.from(totals.keys()).sort((a, b) => (totals.get(b) ?? 0) - (totals.get(a) ?? 0) || a.localeCompare(b));
  const colors = projectColors(keys);
  const running = data.map(() => 0);
  const layers = keys.map((key): RiverLayer => {
    const lower = [...running];
    const upper = data.map((datum, index) => {
      running[index] += datum.projects.get(key) ?? 0;
      return running[index];
    });
    return { key, color: colors.get(key) ?? PROJECT_COLORS[0], lower, upper };
  });
  const minAt = data[0]?.timestamp ?? 0;
  const maxAt = data[data.length - 1]?.timestamp ?? minAt;
  return { data, layers, colors, maxTotal: Math.max(1, ...data.map((datum) => datum.total)), minAt, maxAt };
}

function projectColors(keys: string[]): Map<string, string> {
  const out = new Map<string, string>();
  const used = new Set<number>();
  keys.forEach((key) => {
    if (key === UNASSIGNED_PROJECT) {
      out.set(key, "#b99355");
      return;
    }
    let index = hashString(key) % PROJECT_COLORS.length;
    while (used.has(index) && used.size < PROJECT_COLORS.length) index = (index + 1) % PROJECT_COLORS.length;
    used.add(index);
    out.set(key, PROJECT_COLORS[index]);
  });
  return out;
}

function riverProjectRows(datum: RiverDatum, colors: Map<string, string>, t: Translate) {
  return Array.from(datum.projects.entries())
    .filter(([, value]) => value > 0)
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([key, value]) => ({ key, value, color: colors.get(key) ?? PROJECT_COLORS[0], label: riverProjectLabel(key, t) }));
}

function riverProjectLabel(key: string, t: Translate): string {
  if (key === UNASSIGNED_PROJECT) return t("unassigned");
  return key;
}

function layerPath(layer: RiverLayer, data: RiverDatum[], minAt: number, maxAt: number, maxTotal: number): string {
  if (!data.length) return "";
  if (data.length === 1) {
    const x = pointX(data[0].timestamp, minAt, maxAt);
    return `M${(x - 2).toFixed(2)},${valueY(layer.lower[0], maxTotal).toFixed(2)} L${(x - 2).toFixed(2)},${valueY(layer.upper[0], maxTotal).toFixed(2)} L${(x + 2).toFixed(2)},${valueY(layer.upper[0], maxTotal).toFixed(2)} L${(x + 2).toFixed(2)},${valueY(layer.lower[0], maxTotal).toFixed(2)} Z`;
  }
  const upper = layer.upper.map((value, index) => `${index ? "L" : "M"}${pointX(data[index].timestamp, minAt, maxAt).toFixed(2)},${valueY(value, maxTotal).toFixed(2)}`).join(" ");
  const lower = layer.lower.map((value, index) => ({ value, index })).reverse()
    .map(({ value, index }) => `L${pointX(data[index].timestamp, minAt, maxAt).toFixed(2)},${valueY(value, maxTotal).toFixed(2)}`).join(" ");
  return `${upper} ${lower} Z`;
}

function pointX(timestamp: number, minAt: number, maxAt: number): number {
  return maxAt <= minAt ? WIDTH / 2 : PAD.left + ((timestamp - minAt) / (maxAt - minAt)) * (WIDTH - PAD.left - PAD.right);
}

function nearestDatumIndex(data: RiverDatum[], targetAt: number): number {
  let bestIndex = 0;
  let bestDistance = Number.POSITIVE_INFINITY;
  data.forEach((datum, index) => {
    const distance = Math.abs(datum.timestamp - targetAt);
    if (distance < bestDistance) {
      bestDistance = distance;
      bestIndex = index;
    }
  });
  return bestIndex;
}

function valueY(value: number, maxTotal: number): number {
  return HEIGHT - PAD.bottom - (value / Math.max(1, maxTotal)) * (HEIGHT - PAD.top - PAD.bottom);
}

function hashString(value: string): number {
  let hash = 2166136261;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 16777619);
  }
  return hash >>> 0;
}

function formatRiverTime(value: string): string {
  const date = new Date(value);
  if (!Number.isFinite(date.getTime())) return value;
  return new Intl.DateTimeFormat(document.documentElement.lang || undefined, {
    month: "numeric",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  }).format(date);
}
