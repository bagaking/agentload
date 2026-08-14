import { useEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent, type PointerEvent } from "react";
import { GitBranch } from "lucide-react";
import { formatCopy, formatTokenRate, type Translate } from "../lib/format";
import { trendOutputThroughputActiveSessions, trendOutputThroughputProjects, trendOutputThroughputValue, trendOutputThroughputWindowSeconds } from "../lib/metricSemantics";
import type { TrendPoint } from "./types";

const WIDTH = 640;
const HEIGHT = 180;
const PAD = { top: 12, right: 8, bottom: 20, left: 8 };
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
  segment: number;
};

type RiverLayer = {
  key: string;
  color: string;
  path: string;
};

type RiverModel = {
  data: RiverDatum[];
  layers: RiverLayer[];
  colors: Map<string, string>;
  maxTotal: number;
  minAt: number;
  maxAt: number;
  snapMs: number;
  windowSeconds: number | null;
  periodTotalAll: number;
};

type HoverState = { xRatio: number; index: number | null };

export function ThroughputRiver({
  t,
  title,
  points,
  from,
  to,
  selectedAt,
  onSelect,
  compact = false,
  projectWorktrees,
}: {
  t: Translate;
  title: string;
  points: TrendPoint[];
  from?: string;
  to?: string;
  selectedAt?: string;
  onSelect: (at?: string) => void;
  compact?: boolean;
  projectWorktrees?: Map<string, { worktrees?: string[]; branches?: string[] }>;
}) {
  const model = useMemo(() => buildRiverModel(points, from, to), [points, from, to]);
  const fallbackIndex = model.data.length - 1;
  const pinIndex = model.data.findIndex((datum) => datum.at === selectedAt);
  const [hover, setHover] = useState<HoverState | null>(null);
  const [expanded, setExpanded] = useState(false);
  const pendingHoverRef = useRef<HoverState | null>(null);
  const hoverFrameRef = useRef<number | null>(null);

  // Values follow the inspected instant (hover wins over pin); default = period averages.
  const hoverSampleIndex = hover?.index ?? null;
  const deckIndex = hoverSampleIndex != null ? hoverSampleIndex : pinIndex >= 0 ? pinIndex : null;
  const deckDatum = deckIndex != null ? model.data[deckIndex] : null;

  // Row membership + order derived from the WHOLE PERIOD (model.layers is period-total desc),
  // so hovering never reshuffles rows; only the numbers change.
  const rows = useMemo(() => {
    return model.layers.map((layer) => {
      const totalTokens = model.data.reduce((sum, d) => sum + (d.projects.get(layer.key) ?? 0), 0);
      const avgRate = model.data.length > 0 ? totalTokens / model.data.length : 0;
      return { key: layer.key, color: layer.color, label: riverProjectLabel(layer.key, t), totalTokens, avgRate };
    });
  }, [model, t]);
  const baseLimit = compact ? 4 : 8;
  const visibleRows = expanded ? rows : rows.slice(0, baseLimit);
  const smoothing = model.windowSeconds ? formatCopy(t("throughputSmoothingWindow"), { window: model.windowSeconds }) : "";

  const ratioOf = (index: number) => {
    const d = model.data[index];
    if (!d || model.maxAt <= model.minAt) return 0;
    return clamp01((d.timestamp - model.minAt) / (model.maxAt - model.minAt));
  };
  const resolvePointer = (event: { currentTarget: SVGSVGElement; clientX: number }): HoverState | null => {
    const rect = event.currentTarget.getBoundingClientRect();
    if (!rect.width || !model.data.length) return null;
    const viewX = ((event.clientX - rect.left) / rect.width) * WIDTH;
    const xRatio = clamp01((viewX - PAD.left) / (WIDTH - PAD.left - PAD.right));
    const targetAt = model.minAt + xRatio * (model.maxAt - model.minAt);
    const idx = nearestDatumIndex(model.data, targetAt);
    const near = model.data[idx];
    const within = near != null && Math.abs(near.timestamp - targetAt) <= model.snapMs;
    return { xRatio, index: within ? idx : null };
  };
  const commitHover = (next: HoverState | null) => {
    setHover((prev) => (sameHover(prev, next) ? prev : next));
  };
  const updateHover = (event: PointerEvent<SVGSVGElement>) => {
    pendingHoverRef.current = resolvePointer(event);
    if (hoverFrameRef.current !== null) return;
    hoverFrameRef.current = window.requestAnimationFrame(() => {
      hoverFrameRef.current = null;
      commitHover(pendingHoverRef.current);
    });
  };
  const clearHover = () => {
    if (hoverFrameRef.current !== null) window.cancelAnimationFrame(hoverFrameRef.current);
    hoverFrameRef.current = null;
    pendingHoverRef.current = null;
    commitHover(null);
  };
  const handleKey = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!model.data.length) return;
    if (event.key === "Escape") {
      clearHover();
      if (pinIndex >= 0) onSelect(undefined);
      return;
    }
    const start = hoverSampleIndex ?? (pinIndex >= 0 ? pinIndex : fallbackIndex);
    const current = Math.max(0, Math.min(fallbackIndex, start));
    if (event.key === "ArrowLeft" || event.key === "ArrowRight") {
      event.preventDefault();
      const next = Math.max(0, Math.min(fallbackIndex, current + (event.key === "ArrowLeft" ? -1 : 1)));
      commitHover({ xRatio: ratioOf(next), index: next });
    }
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      const at = model.data[current]?.at;
      onSelect(at === selectedAt ? undefined : at);
    }
  };

  useEffect(() => () => {
    if (hoverFrameRef.current !== null) window.cancelAnimationFrame(hoverFrameRef.current);
  }, []);

  const domainMs = model.maxAt - model.minAt;
  const multiDay = domainMs > 22 * 3600 * 1000 || new Date(model.minAt).getDate() !== new Date(model.maxAt).getDate();
  const lang = document.documentElement.lang || undefined;
  const timeTicks = model.data.length ? [0, 1 / 3, 2 / 3, 1] : [];
  const valueTicks = model.data.length ? [1, 0.5, 0] : [];

  const pinX = pinIndex >= 0 ? pointX(model.data[pinIndex].timestamp, model.minAt, model.maxAt) : 0;
  const pinY = pinIndex >= 0 ? valueY(model.data[pinIndex].total, model.maxTotal) : 0;
  const hoverX = hover ? (hover.index != null ? pointX(model.data[hover.index].timestamp, model.minAt, model.maxAt) : PAD.left + hover.xRatio * (WIDTH - PAD.left - PAD.right)) : 0;
  const hoverSample = hover?.index != null ? model.data[hover.index] : null;
  const hoverY = hoverSample ? valueY(hoverSample.total, model.maxTotal) : HEIGHT * 0.5;
  const tooltipTargetAt = hover ? model.minAt + hover.xRatio * domainMs : 0;

  return (
    <div className={`throughput-river-shell ${compact ? "compact" : ""}`}>
      <div
        className="trend-chart throughput-river-chart"
        role="img"
        aria-label={`${title} · ${t("throughputProjectSplit")}`}
        tabIndex={0}
        onKeyDown={handleKey}
        onPointerLeave={clearHover}
      >
        {!compact ? (
          <div className="throughput-river-legend" aria-hidden="true">
            {model.layers.slice(0, 5).map((layer) => (
              <span key={layer.key}><i style={{ backgroundColor: layer.color }} />{riverProjectLabel(layer.key, t)}</span>
            ))}
            {model.layers.length > 5 ? <span>+{model.layers.length - 5}</span> : null}
          </div>
        ) : null}
        <svg
          viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
          preserveAspectRatio="none"
          onPointerMove={updateHover}
          onClick={(event) => {
            const resolved = resolvePointer(event);
            if (!resolved || resolved.index == null) { onSelect(undefined); return; }
            const at = model.data[resolved.index].at;
            onSelect(at === selectedAt ? undefined : at);
          }}
        >
          {[0.25, 0.5, 0.75].map((ratio) => (
            <line key={ratio} className="throughput-river-grid" x1={PAD.left} x2={WIDTH - PAD.right} y1={valueY(model.maxTotal * ratio, model.maxTotal)} y2={valueY(model.maxTotal * ratio, model.maxTotal)} />
          ))}
          {model.layers.map((layer) => (
            <path key={layer.key} className="throughput-river-layer" d={layer.path} fill={layer.color} />
          ))}
          {pinIndex >= 0 ? (
            <g className="throughput-river-marker is-pinned" aria-hidden="true">
              <line x1={pinX} x2={pinX} y1={PAD.top} y2={HEIGHT - PAD.bottom} />
              <circle cx={pinX} cy={pinY} r="4" />
            </g>
          ) : null}
          {hover ? (
            <g className={`throughput-river-marker is-hover ${hover.index == null ? "is-gap" : ""}`} aria-hidden="true">
              <line x1={hoverX} x2={hoverX} y1={PAD.top} y2={HEIGHT - PAD.bottom} />
              {hoverSample ? <circle cx={hoverX} cy={hoverY} r="3.5" /> : null}
            </g>
          ) : null}
        </svg>
        {valueTicks.length ? (
          <div className="throughput-river-yaxis" aria-hidden="true">
            {valueTicks.map((ratio, index) => (
              <span key={ratio} style={{ top: `${(valueY(model.maxTotal * ratio, model.maxTotal) / HEIGHT) * 100}%` }}>
                {formatTokenRate(model.maxTotal * ratio)}{index === 0 ? ` ${t("tokenRateUnit")}` : ""}
              </span>
            ))}
          </div>
        ) : null}
        {timeTicks.length ? (
          <div className="throughput-river-xaxis" aria-hidden="true">
            {timeTicks.map((ratio) => (
              <span
                key={ratio}
                style={{ left: `${((PAD.left + ratio * (WIDTH - PAD.left - PAD.right)) / WIDTH) * 100}%`, transform: ratio === 0 ? "none" : ratio === 1 ? "translateX(-100%)" : "translateX(-50%)" }}
              >
                {formatAxisTime(model.minAt + ratio * domainMs, multiDay, lang)}
              </span>
            ))}
          </div>
        ) : null}
        {hover ? (
          <div
            className={`throughput-river-tooltip ${hoverSample ? (hoverY < HEIGHT * 0.43 ? "is-below" : "is-above") : "is-below is-gap"}`}
            style={{ "--river-x": `${(hoverX / WIDTH) * 100}%`, "--river-y": `${(hoverY / HEIGHT) * 100}%` } as CSSProperties}
            aria-hidden="true"
          >
            {hoverSample ? (
              <>
                <strong>{formatRiverTime(hoverSample.at)}</strong>
                <span><b>{t("outputThroughput")}</b><em>{formatTokenRate(hoverSample.total)} {t("tokenRateUnit")}</em></span>
                <span><b>{t("trendReadoutContributors")}</b><em>{trendOutputThroughputActiveSessions(hoverSample.point) ?? 0}</em></span>
                {model.windowSeconds ? <span><b>{t("windowSize")}</b><em>{model.windowSeconds}s</em></span> : null}
              </>
            ) : (
              <>
                <strong>{formatRiverTime(new Date(tooltipTargetAt).toISOString())}</strong>
                <span className="river-gap-line"><b>{t("throughputNoSampling")}</b></span>
                <small>{t("throughputNoSamplingDetail")}</small>
              </>
            )}
          </div>
        ) : null}
      </div>
      {compact ? (
        <div className="throughput-ranking-deck" aria-label={t("throughputProjectSplit")}>
          <div className="ranking-head">
            <span className="ranking-head-title">{t("throughputProjectSplit")}</span>
            <small>{deckDatum ? formatRiverTime(deckDatum.at) : t("throughputPeriodAvg")}</small>
          </div>
          <div className="ranking-sort-note">{t("throughputRankedByPeriodTotal")}{smoothing ? ` · ${smoothing}` : ""}</div>
          <div className="ranking-list">
            {visibleRows.map((row) => {
              const instant = deckDatum ? (deckDatum.projects.get(row.key) ?? 0) : null;
              const value = instant != null ? instant : row.avgRate;
              const active = value > 0;
              const share = deckDatum
                ? shareOf(instant ?? 0, deckDatum.total)
                : shareOf(row.totalTokens, model.periodTotalAll);
              const meta = projectWorktrees?.get(row.key.toLowerCase());
              const branchTag = meta?.branches?.[0] || meta?.worktrees?.[0];
              return (
                <div key={row.key} className={`ranking-row ${active ? "is-active" : "is-idle"}`}>
                  <div className="ranking-info">
                    <i className="ranking-dot" style={{ backgroundColor: row.color, opacity: active ? 1 : 0.35 }} />
                    <strong className="ranking-name" title={row.key}>{row.label}</strong>
                    {branchTag ? (
                      <span className="ranking-branch-tag" title={branchTag}>
                        <GitBranch size={10} aria-hidden="true" />
                        {branchTag}
                      </span>
                    ) : null}
                    <span className={`ranking-rate ${active ? "has-rate" : "zero-rate"}`}>
                      {formatTokenRate(value)} {t("tokenRateUnit")}
                    </span>
                    <span className="ranking-pct">{active ? share.text : "—"}</span>
                  </div>
                  <div className="ranking-bar-track">
                    <div className="ranking-bar-fill" style={{ width: active ? `${share.barWidth}%` : "0%", backgroundColor: row.color }} />
                  </div>
                </div>
              );
            })}
            {rows.length === 0 ? (
              <div className="ranking-empty"><span>{t("noData")}</span></div>
            ) : null}
          </div>
          {rows.length > baseLimit ? (
            <button type="button" className="ranking-expand" aria-expanded={expanded} onClick={() => setExpanded((prev) => !prev)}>
              {expanded ? t("throughputCollapseProjects") : formatCopy(t("throughputViewAllProjects"), { count: rows.length })}
            </button>
          ) : null}
        </div>
      ) : (
        <div className="throughput-river-projects" aria-label={t("throughputProjectSplit")}>
          {visibleRows.map((row) => {
            const instant = deckDatum ? (deckDatum.projects.get(row.key) ?? 0) : null;
            const value = instant != null ? instant : row.avgRate;
            return (
              <span key={row.key} title={`${row.label}: ${formatTokenRate(value)} ${t("tokenRateUnit")}`}>
                <i style={{ backgroundColor: row.color }} />
                <b>{row.label}</b>
                <em>{formatTokenRate(value)}</em>
              </span>
            );
          })}
          {rows.length > baseLimit ? (
            <button type="button" className="ranking-expand river-inline-expand" aria-expanded={expanded} onClick={() => setExpanded((prev) => !prev)}>
              {expanded ? t("throughputCollapseProjects") : formatCopy(t("throughputViewAllProjects"), { count: rows.length })}
            </button>
          ) : null}
        </div>
      )}
    </div>
  );
}

function buildRiverModel(points: TrendPoint[], from?: string, to?: string): RiverModel {
  const data: RiverDatum[] = [];
  let segment = 0;
  let previousWasValid = false;
  [...points].sort((a, b) => String(a.at).localeCompare(String(b.at))).forEach((point) => {
    const at = String(point.at || "");
    const timestamp = Date.parse(at);
    const total = trendOutputThroughputValue(point);
    const partition = trendOutputThroughputProjects(point);
    if (!at || !Number.isFinite(timestamp) || total === null || partition === null) {
      previousWasValid = false;
      return;
    }
    if (!previousWasValid && data.length) segment += 1;
    const projects = new Map<string, number>();
    for (const project of partition) {
      const key = String(project.project);
      projects.set(key, (projects.get(key) ?? 0) + (project.output_tokens_per_second ?? 0));
    }
    data.push({ at, timestamp, total, point, projects, segment });
    previousWasValid = true;
  });
  const totals = new Map<string, number>();
  data.forEach((datum) => datum.projects.forEach((value, key) => totals.set(key, (totals.get(key) ?? 0) + value)));
  const keys = Array.from(totals.keys()).sort((a, b) => (totals.get(b) ?? 0) - (totals.get(a) ?? 0) || a.localeCompare(b));
  const colors = projectColors(keys);
  const sampledMinAt = data[0]?.timestamp ?? 0;
  const sampledMaxAt = data[data.length - 1]?.timestamp ?? sampledMinAt;
  const requestedMinAt = Date.parse(String(from || ""));
  const requestedMaxAt = Date.parse(String(to || ""));
  const hasRequestedDomain = Number.isFinite(requestedMinAt) && Number.isFinite(requestedMaxAt) && requestedMaxAt > requestedMinAt;
  const minAt = hasRequestedDomain ? requestedMinAt : sampledMinAt;
  const maxAt = hasRequestedDomain ? requestedMaxAt : sampledMaxAt;
  const maxTotal = Math.max(1, ...data.map((datum) => datum.total));
  const running = data.map(() => 0);
  const layers = keys.map((key): RiverLayer => {
    const lower = [...running];
    const upper = data.map((datum, index) => {
      running[index] += datum.projects.get(key) ?? 0;
      return running[index];
    });
    return { key, color: colors.get(key) ?? PROJECT_COLORS[0], path: layerPath(lower, upper, data, minAt, maxAt, maxTotal) };
  });
  // Snap bound derived from the real cadence (median contiguous inter-sample delta), not a constant;
  // beyond it a hover reads as a sampling gap. 0.75x tolerates jitter without swallowing missing samples.
  const cadenceDeltas: number[] = [];
  for (let i = 1; i < data.length; i += 1) {
    if (data[i].segment === data[i - 1].segment) cadenceDeltas.push(data[i].timestamp - data[i - 1].timestamp);
  }
  const cadence = median(cadenceDeltas);
  const snapMs = cadence > 0 ? cadence * 0.75 : (maxAt > minAt && data.length > 1 ? ((maxAt - minAt) / data.length) * 0.75 : Number.POSITIVE_INFINITY);
  const windowValues = data.map((d) => trendOutputThroughputWindowSeconds(d.point)).filter((v): v is number => v != null);
  const windowSeconds = windowValues.length ? Math.round(median(windowValues)) : null;
  const periodTotalAll = data.reduce((sum, d) => sum + d.total, 0);
  return { data, layers, colors, maxTotal, minAt, maxAt, snapMs, windowSeconds, periodTotalAll };
}

function shareOf(value: number, total: number): { text: string; barWidth: number } {
  if (total <= 0 || value <= 0) return { text: "—", barWidth: 0 };
  const pct = (value / total) * 100;
  const text = pct < 1 ? "<1%" : `${pct.toFixed(pct < 10 ? 1 : 0)}%`;
  return { text, barWidth: Math.max(2, Math.min(100, pct)) };
}

function median(values: number[]): number {
  if (!values.length) return 0;
  const sorted = [...values].sort((a, b) => a - b);
  const mid = Math.floor(sorted.length / 2);
  return sorted.length % 2 ? sorted[mid] : (sorted[mid - 1] + sorted[mid]) / 2;
}

function clamp01(value: number): number {
  return Math.max(0, Math.min(1, value));
}

function sameHover(a: HoverState | null, b: HoverState | null): boolean {
  if (a === b) return true;
  if (!a || !b) return false;
  return a.index === b.index && Math.abs(a.xRatio - b.xRatio) < 0.002;
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

function riverProjectLabel(key: string, t: Translate): string {
  if (key === UNASSIGNED_PROJECT) return t("unassigned");
  return key;
}

function layerPath(lower: number[], upper: number[], data: RiverDatum[], minAt: number, maxAt: number, maxTotal: number): string {
  if (!data.length) return "";
  const paths: string[] = [];
  let start = 0;
  for (let index = 1; index <= data.length; index += 1) {
    if (index < data.length && data[index].segment === data[start].segment) continue;
    paths.push(layerSegmentPath(lower.slice(start, index), upper.slice(start, index), data.slice(start, index), minAt, maxAt, maxTotal));
    start = index;
  }
  return paths.join(" ");
}

function layerSegmentPath(lower: number[], upper: number[], data: RiverDatum[], minAt: number, maxAt: number, maxTotal: number): string {
  if (data.length === 1) {
    const x = pointX(data[0].timestamp, minAt, maxAt);
    return `M${(x - 2).toFixed(2)},${valueY(lower[0], maxTotal).toFixed(2)} L${(x - 2).toFixed(2)},${valueY(upper[0], maxTotal).toFixed(2)} L${(x + 2).toFixed(2)},${valueY(upper[0], maxTotal).toFixed(2)} L${(x + 2).toFixed(2)},${valueY(lower[0], maxTotal).toFixed(2)} Z`;
  }
  const upperPoints = upper.map((value, index) => ({ x: pointX(data[index].timestamp, minAt, maxAt), y: valueY(value, maxTotal) }));
  const lowerPoints = lower.map((value, index) => ({ x: pointX(data[index].timestamp, minAt, maxAt), y: valueY(value, maxTotal) })).reverse();
  return `${smoothLine(upperPoints, "M")} ${smoothLine(lowerPoints, "L")} Z`;
}

function smoothLine(points: Array<{ x: number; y: number }>, start: "M" | "L"): string {
  if (!points.length) return "";
  let path = `${start}${points[0].x.toFixed(2)},${points[0].y.toFixed(2)}`;
  for (let index = 1; index < points.length; index += 1) {
    const previous = points[index - 1];
    const current = points[index];
    const midX = (previous.x + current.x) / 2;
    path += ` C${midX.toFixed(2)},${previous.y.toFixed(2)} ${midX.toFixed(2)},${current.y.toFixed(2)} ${current.x.toFixed(2)},${current.y.toFixed(2)}`;
  }
  return path;
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

function formatAxisTime(timestamp: number, multiDay: boolean, lang?: string): string {
  if (!Number.isFinite(timestamp)) return "";
  return new Intl.DateTimeFormat(lang, multiDay ? { month: "numeric", day: "numeric" } : { hour: "2-digit", minute: "2-digit" }).format(new Date(timestamp));
}
