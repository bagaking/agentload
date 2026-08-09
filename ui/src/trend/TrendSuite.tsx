import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Gauge, Info, Layers } from "lucide-react";
import {
  AreaSeries,
  ColorType,
  CrosshairMode,
  LineStyle,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type MouseEventParams,
  type Time,
} from "lightweight-charts";
import { clampNumber, formatAge as formatRelativeAge, formatCopy, formatPct, formatTokenRate, type Translate } from "../lib/format";
import { trendContextSessionValue, trendMappedProcessCount, trendMappingCoverageValue, trendOutputThroughputActiveSessions, trendOutputThroughputProjects, trendOutputThroughputState, trendOutputThroughputValue, trendOutputThroughputWindowSeconds, trendPrimaryValue, trendUnmappedProcessCount } from "../lib/metricSemantics";
import {
  TREND_RANGES,
  type ProjectHeatmapItem,
  type ProjectHeatmapSet,
  type ProjectHeatmapWindow,
  type TrendLane,
  type TrendPoint,
  type TrendRange,
  type TrendSet,
  type TrendWindow,
} from "./types";
import { toolDisplayName, toolIconName } from "../lib/activityModel";
import { ThroughputRiver } from "./ThroughputRiver";

export type TrendSnapshot = {
  trends?: TrendSet;
  realtime_trends?: TrendSet;
  throughput_trends?: TrendSet;
  project_heatmaps?: ProjectHeatmapSet;
  project_focus?: ProjectHeatmapActivity[];
};

type ProjectHeatmapActivity = {
  project?: string;
  last_event_age_seconds?: number;
  last_event_at?: string;
};

type TrendLaneSummary = {
  lane: TrendLane;
  title: string;
  trendWindow?: TrendWindow;
  points: TrendPoint[];
  data: TrendSignalDatum[];
  selected?: TrendSignalDatum;
};

type ProjectHeatmapTile = ProjectHeatmapItem & {
  last_event_age_seconds?: number;
  last_event_at?: string;
  x: number;
  y: number;
  width: number;
  height: number;
  value: number;
  tone: number;
};

type ProjectHeatmapLabelMode = "full" | "value" | "mini" | "none";

type ProjectHeatmapHover = {
  align: "left" | "right";
  vertical: "above" | "below";
  item: ProjectHeatmapTile;
  left: number;
  top: number;
};

type TrendSignalDatum = {
  at: string;
  time: Time;
  value: number;
  point: TrendPoint;
  previous: number;
};

type TrendSignalOverlay = {
  left: number;
  top: number;
};

type TrendSignalHover = TrendSignalOverlay & {
  align: "left" | "right";
  vertical: "above" | "below";
  datum: TrendSignalDatum;
};

export function TrendSuite({
  t,
  snapshot,
  compact = false,
  range,
  setRange,
  trendSelection,
  setTrendSelection,
}: {
  t: Translate;
  snapshot: TrendSnapshot;
  compact?: boolean;
  range: TrendRange;
  setRange: (value: TrendRange) => void;
  trendSelection: Record<TrendLane, string | undefined>;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const activeRanges = activeTrendRanges(snapshot);
  const effectiveRange = activeRanges.includes(range) ? range : activeRanges[0] ?? range;
  const history = trendWindowForRange(snapshot.trends, effectiveRange);
  const runtime = trendWindowForRange(snapshot.realtime_trends, effectiveRange);
  const throughput = trendWindowForRange(snapshot.throughput_trends, effectiveRange);
  const projectHeatmap = projectHeatmapWindowForRange(snapshot.project_heatmaps, effectiveRange);
  const projectActivity = useMemo(() => projectHeatmapActivityByProject(snapshot.project_focus), [snapshot.project_focus]);
  const [focusedLane, setFocusedLane] = useState<TrendLane>("history");
  // Stable summary (and points) identities keep the chart data effect from
  // re-firing on focus-only re-renders.
  const laneSummaries: TrendLaneSummary[] = useMemo(() => [
    trendLaneSummary("history", t("historyLane"), history, trendSelection.history),
    trendLaneSummary("runtime", t("runtimeLane"), runtime, trendSelection.runtime),
    trendLaneSummary("throughput", t("throughputLane"), throughput, trendSelection.throughput),
  ].filter((summary) => Boolean(summary.trendWindow || summary.points.length)), [t, history, runtime, throughput, trendSelection.history, trendSelection.runtime, trendSelection.throughput]);
  const activeSummary = laneSummaries.find((summary) => summary.lane === focusedLane && summary.selected) ?? laneSummaries.find((summary) => summary.selected);
  return (
    <section className={`trend-suite ${compact ? "compact" : "dashboard"}`}>
      <div className="trend-suite-head">
        <div className="trend-suite-copy">
          <h2>{t("trendSuite")}</h2>
          <span>{effectiveRange} · {formatTrendWindow(t, history ?? runtime ?? throughput)}</span>
        </div>
        <div className="trend-range-switch" role="group" aria-label={t("trend")}>
          {TREND_RANGES.map((item) => (
            <button key={item} type="button" disabled={!activeRanges.includes(item)} aria-pressed={item === effectiveRange} data-focus-key={focusKey("trend-range", item)} onClick={() => setRange(item)}>
              {item}
            </button>
          ))}
        </div>
      </div>
      {laneSummaries.length ? (
        <>
          <div className="trend-duo">
            {laneSummaries.map((summary) => (
              <TrendLaneView
                key={summary.lane}
                t={t}
                summary={summary}
                isFocused={activeSummary?.lane === summary.lane}
                compact={compact}
                setFocusedLane={setFocusedLane}
                setTrendSelection={setTrendSelection}
              />
            ))}
          </div>
          <ProjectHeatmap t={t} window={projectHeatmap} projectActivity={projectActivity} compact={compact} />
          {compact ? null : <TrendSelectionInspector t={t} summary={activeSummary} compact={compact} />}
        </>
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </section>
  );
}

function ProjectHeatmap({
  t,
  window,
  projectActivity,
  compact,
}: {
  t: Translate;
  window?: ProjectHeatmapWindow;
  projectActivity: Map<string, ProjectHeatmapActivity>;
  compact: boolean;
}) {
  const [hover, setHover] = useState<ProjectHeatmapHover | null>(null);
  const rawItems = (window?.items ?? []).filter((item) => (item.session_window_count ?? 0) > 0 || (item.window_count ?? 0) > 0);
  const visibleLimit = compact ? 10 : 16;
  const items = rawItems.slice(0, visibleLimit).map((item) => ({
    ...item,
    ...projectActivity.get(projectHeatmapProjectKey(item.project)),
  }));
  const hiddenCount = Math.max(0, rawItems.length - items.length);
  const tiles = projectHeatmapTiles(items);
  const totalSessions = window?.session_window_count ?? items.reduce((sum, item) => sum + (item.session_window_count ?? 0), 0);
  const sampleWindows = window?.sample_window_count ?? 0;
  const meta = formatCopy(t("projectHeatmapMeta"), {
    sessions: totalSessions,
    windows: sampleWindows,
  });
  return (
    <section className={`project-heatmap ${compact ? "compact" : ""}`} aria-label={t("projectHeatmap")}>
      <div className="project-heatmap-head">
        <span className="project-heatmap-title"><Layers size={13} aria-hidden="true" />{t("projectHeatmap")}</span>
        <em>{meta}{hiddenCount ? ` · +${hiddenCount}` : ""}</em>
      </div>
      {tiles.length ? (
        <div className="project-heatmap-map" role="list" onPointerLeave={() => setHover(null)}>
          {tiles.map((tile, index) => {
            const title = formatCopy(t("projectHeatmapItemTitle"), {
              project: tile.project || t("unassigned"),
              sessions: tile.session_window_count ?? 0,
              windows: tile.window_count ?? 0,
              share: formatPct(tile.share_pct),
            }) + ` ${t("projectHeatmapLastActive")}: ${formatHeatmapLastActive(t, tile)}.`;
            const area = tile.width * tile.height;
            const labelMode = projectHeatmapLabelMode(tile, index, compact);
            const heat = clampNumber((tile.share_pct ?? 0) / 100, 0.18, 1);
            const densityClass = tile.width < 10 || tile.height < 10 || area < 150
              ? "is-dot"
              : tile.width < 22 || tile.height < 26 || area < 780
                ? "is-flat"
                : area < 1500
                  ? "is-small"
                  : "";
            const style = {
              "--x": `${tile.x}%`,
              "--y": `${tile.y}%`,
              "--w": `${tile.width}%`,
              "--h": `${tile.height}%`,
              "--heat": heat.toFixed(3),
              "--heat-mix": `${Math.round(20 + heat * 18)}%`,
              "--heat-opacity": (0.14 + heat * 0.22).toFixed(3),
            } as React.CSSProperties;
            return (
              <div
                aria-label={title}
                className={`project-heatmap-tile tone-${tile.tone} ${densityClass} label-${labelMode}`}
                key={`${tile.project}-${tile.x}-${tile.y}`}
                onPointerEnter={(event) => setHover(projectHeatmapHoverFromPointer(event, tile, title))}
                onPointerMove={(event) => setHover(projectHeatmapHoverFromPointer(event, tile, title))}
                role="listitem"
                style={style}
              >
                <span className="project-heatmap-label">{tile.project || t("unassigned")}</span>
                <b>{labelMode === "mini" ? formatHeatmapInlineValue(tile.session_window_count ?? 0) : tile.session_window_count ?? 0}</b>
                <small>{tile.window_count ?? 0} {t("heatmapWindows")}</small>
              </div>
            );
          })}
          {hover ? (
            <div
              aria-hidden="true"
              className={`project-heatmap-tooltip align-${hover.align} is-${hover.vertical}`}
              style={{ "--tip-x": `${hover.left}px`, "--tip-y": `${hover.top}px` } as React.CSSProperties}
            >
              <strong>{hover.item.project || t("unassigned")}</strong>
              <div>
                <span><b>{hover.item.session_window_count ?? 0}</b><em>{t("heatmapSessionWindows")}</em></span>
                <span><b>{hover.item.window_count ?? 0}</b><em>{t("heatmapWindows")}</em></span>
                <span><b>{formatPct(hover.item.share_pct)}</b><em>{t("heatmapShare")}</em></span>
                <span><b>{formatHeatmapLastActive(t, hover.item)}</b><em>{t("projectHeatmapLastActive")}</em></span>
              </div>
            </div>
          ) : null}
        </div>
      ) : (
        <div className="project-heatmap-empty">
          <span>{t("projectHeatmapEmpty")}</span>
          <small>{formatTrendWindow(t, window)}</small>
        </div>
      )}
    </section>
  );
}

function projectHeatmapActivityByProject(items?: ProjectHeatmapActivity[]): Map<string, ProjectHeatmapActivity> {
  const result = new Map<string, ProjectHeatmapActivity>();
  (items ?? []).forEach((item) => {
    result.set(projectHeatmapProjectKey(item.project), item);
  });
  return result;
}

function projectHeatmapProjectKey(project?: string): string {
  const text = String(project || "unassigned").trim().toLowerCase();
  return text || "unassigned";
}

function formatHeatmapLastActive(t: Translate, item: ProjectHeatmapTile): string {
  if (typeof item.last_event_age_seconds === "number") return formatRelativeAge(item.last_event_age_seconds, t);
  return t("unavailable");
}

function projectHeatmapLabelMode(tile: ProjectHeatmapTile, index: number, compact: boolean): ProjectHeatmapLabelMode {
  const area = tile.width * tile.height;
  if (!compact) {
    if (tile.width < 10 || tile.height < 10 || area < 150) return "none";
    if (tile.width < 18 || tile.height < 18 || area < 520) return "mini";
    if (tile.width < 24 || tile.height < 30 || area < 1200) return "value";
    return "full";
  }
  if (tile.width < 10 || tile.height < 12 || area < 260) return "none";
  if (index > 5 && (tile.width < 20 || tile.height < 24 || area < 900)) return "none";
  if (tile.width < 16 || tile.height < 18 || area < 640) return "mini";
  if (tile.width < 26 || tile.height < 34 || area < 1800) return "value";
  return "full";
}

function projectHeatmapHoverFromPointer(
  event: React.PointerEvent<HTMLDivElement>,
  item: ProjectHeatmapTile,
  _label: string,
): ProjectHeatmapHover {
  const map = event.currentTarget.closest(".project-heatmap-map");
  const rect = map?.getBoundingClientRect();
  const width = rect?.width ?? event.currentTarget.clientWidth;
  const height = rect?.height ?? event.currentTarget.clientHeight;
  const left = rect ? event.clientX - rect.left : event.nativeEvent.offsetX;
  const top = rect ? event.clientY - rect.top : event.nativeEvent.offsetY;
  const tooltipWidth = Math.min(204, Math.max(132, width - 20));
  const tooltipHeight = 66;
  const align: ProjectHeatmapHover["align"] = left > width / 2 ? "right" : "left";
  const tooltipLeft = align === "right" ? left - tooltipWidth - 8 : left + 8;
  const tooltipTop = top - tooltipHeight / 2;
  return {
    align,
    vertical: top > height / 2 ? "above" : "below",
    item,
    left: clampNumber(tooltipLeft, 8, Math.max(8, width - tooltipWidth - 8)),
    top: clampNumber(tooltipTop, 8, Math.max(8, height - tooltipHeight - 8)),
  };
}

function formatHeatmapInlineValue(value: number): string {
  if (!Number.isFinite(value)) return "0";
  if (value >= 10000) return `${Math.round(value / 1000)}k`;
  if (value >= 1000) return `${(Math.round(value / 100) / 10).toFixed(1)}k`;
  return String(value);
}

function TrendLaneView({
  t,
  summary,
  isFocused,
  compact,
  setFocusedLane,
  setTrendSelection,
}: {
  t: Translate;
  summary: TrendLaneSummary;
  isFocused: boolean;
  compact: boolean;
  setFocusedLane: (lane: TrendLane) => void;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const { lane, title, trendWindow, points, data, selected } = summary;
  const selectedReadoutParts = selected ? trendSelectedReadoutParts(t, lane, selected) : [];
  const selectPoint = useCallback((at?: string) => {
    setFocusedLane(lane);
    if (at) setTrendSelection((current) => ({ ...current, [lane]: at }));
  }, [lane, setFocusedLane, setTrendSelection]);
  return (
    <article className={`trend-lane ${lane} ${isFocused ? "is-focused" : ""}`} onPointerEnter={() => setFocusedLane(lane)}>
      <div className="trend-lane-head">
        <div className="trend-lane-title">
          <span className="trend-kicker">{title}</span>
          <small>{trendWindow?.range || t("unavailable")} · {data.length} {t("samples")}{lane === "throughput" && selected ? ` · ${formatTrendThroughputWindow(t, selected.point)}` : ""}</small>
        </div>
        {compact && lane === "runtime" && isFocused ? <TrendRuntimeDrilldown t={t} summary={summary} /> : null}
        <button
          aria-pressed={isFocused}
          className="trend-lane-readout"
          data-focus-key={focusKey("trend-lane-readout", lane, selected?.at || "")}
          disabled={!selected}
          title={selected?.at ? formatDateTime(selected.at) : t("unavailable")}
          type="button"
          onClick={() => selectPoint(selected?.at)}
        >
          <span className="trend-readout-time">{selected?.at ? formatChartAxisLabel(selected.at) : t("unavailable")}</span>
          <strong className="trend-readout-values">
            {selectedReadoutParts.length ? selectedReadoutParts.map((part) => (
              <React.Fragment key={part.label}>
                <span className={`trend-readout-value ${part.role}`}><em>{part.label}</em><b>{part.value}</b></span>
              </React.Fragment>
            )) : t("unavailable")}
          </strong>
        </button>
      </div>
      {summary.data.length ? (lane === "throughput" ? (
        <ThroughputRiver t={t} title={title} points={points} selectedAt={selected?.at} onSelect={selectPoint} />
      ) : compact && lane === "runtime" ? (
        <TrendRuntimeCurve t={t} summary={summary} selectedAt={selected?.at} onSelect={selectPoint} />
      ) : (
        <TrendSignalChart
          t={t}
          lane={lane}
          title={title}
          points={points}
          selectedAt={selected?.at}
          onSelect={selectPoint}
        />
      )) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </article>
  );
}

function TrendRuntimeCurve({ t, summary, selectedAt, onSelect }: { t: Translate; summary: TrendLaneSummary; selectedAt?: string; onSelect: (at?: string) => void }) {
  const data = summary.data;
  const width = 320;
  const height = 88;
  const pad = 10;
  const maxValue = Math.max(1, ...data.map((item) => item.value));
  const selected = selectedAt ? data.find((item) => item.at === selectedAt) ?? data[data.length - 1] : data[data.length - 1];
  const pointAt = (index: number, item: TrendSignalDatum) => {
    const x = data.length <= 1 ? width / 2 : pad + (index / (data.length - 1)) * (width - pad * 2);
    const y = height - pad - (item.value / maxValue) * (height - pad * 2);
    return { x, y };
  };
  const line = data.map((item, index) => {
    const point = pointAt(index, item);
    return `${point.x.toFixed(2)},${point.y.toFixed(2)}`;
  }).join(" ");
  const area = data.length ? `${pad},${height - pad} ${line} ${width - pad},${height - pad}` : "";
  const selectedIndex = Math.max(0, data.findIndex((item) => item.at === selected?.at));
  const selectedPoint = selected ? pointAt(selectedIndex, selected) : null;
  return (
    <div className="trend-runtime-curve" role="img" aria-label={`${summary.title} ${t("trend")}`}>
      <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none">
        <polygon className="runtime-curve-area" points={area} />
        <polyline className="runtime-curve-line" points={line} />
        {data.map((item, index) => {
          const point = pointAt(index, item);
          const isSelected = item.at === selected?.at;
          return (
            <circle
              className={isSelected ? "is-selected" : ""}
              key={item.at || index}
              cx={point.x}
              cy={point.y}
              r={isSelected ? 3.4 : 2.2}
              onClick={() => onSelect(item.at)}
            />
          );
        })}
        {selectedPoint ? (
          <>
            <line className="runtime-curve-crosshair" x1={selectedPoint.x} x2={selectedPoint.x} y1={pad} y2={height - pad} />
            <line className="runtime-curve-crosshair" x1={pad} x2={width - pad} y1={selectedPoint.y} y2={selectedPoint.y} />
          </>
        ) : null}
      </svg>
    </div>
  );
}

function TrendRuntimeDrilldown({ t, summary }: { t: Translate; summary?: TrendLaneSummary }) {
  const datum = summary?.selected;
  if (!summary || summary.lane !== "runtime" || !datum) return null;
  const point = datum.point;
  const total = Math.max(0, datum.value);
  const { mode, parts } = runtimeDrilldownParts(t, point, total);
  return (
    <aside className="trend-runtime-drilldown" aria-label={t("processPressure")}>
      <div className="trend-runtime-drilldown-head">
        <span>{mode}</span>
      </div>
      <div className="trend-runtime-drilldown-meter" aria-hidden="true">
        {parts.map((part) => (
          <i className={part.tone} key={part.key} style={{ width: `${clampNumber(part.pct, 0, 100)}%` }} />
        ))}
      </div>
      <div className="trend-runtime-drilldown-parts">
        {parts.map((part) => (
          <span className={part.tone} key={part.key}>
            <TrendDrilldownMark part={part} />
            <b>{part.label}</b>
            <strong>{trendMetricValue(t, part.value)}</strong>
          </span>
        ))}
      </div>
    </aside>
  );
}

type TrendDrilldownPart = {
  key: string;
  tone: string;
  label: string;
  value: number;
  pct: number;
  tool?: string;
};

function runtimeDrilldownParts(t: Translate, point: TrendPoint, total: number): { mode: string; parts: TrendDrilldownPart[] } {
  const scale = Math.max(total, 1);
  const toolItems = [...(point.runtime_process_summary ?? [])]
    .filter((item) => (item.pid_count ?? 0) > 0)
    .sort((a, b) => (b.pid_count ?? 0) - (a.pid_count ?? 0));
  if (toolItems.length) {
    return {
      mode: t("codingAgents"),
      parts: compactTrendParts(toolItems.map((item, index) => ({
        key: item.key || item.tool || `tool-${index}`,
        tone: `tool-${index % 4}`,
        label: toolDisplayName(item.display_name || item.tool || item.key),
        value: item.pid_count ?? 0,
        pct: ((item.pid_count ?? 0) / scale) * 100,
        tool: item.tool || item.key,
      })), scale),
    };
  }
  const hostItems = [...(point.host_app_process_summary ?? [])]
    .filter((item) => (item.pid_count ?? 0) > 0)
    .sort((a, b) => (b.pid_count ?? 0) - (a.pid_count ?? 0));
  if (hostItems.length) {
    return {
      mode: t("hostProcesses"),
      parts: compactTrendParts(hostItems.map((item, index) => ({
        key: item.key || item.name || `host-${index}`,
        tone: `host-${index % 4}`,
        label: item.name || t("hostUnknown"),
        value: item.pid_count ?? 0,
        pct: ((item.pid_count ?? 0) / scale) * 100,
      })), scale),
    };
  }
  const mapped = trendMappedProcessCount(point);
  const unmatched = trendUnmappedProcessCount(point);
  const explained = Math.max(0, (mapped ?? 0) + (unmatched ?? 0));
  const unknown = Math.max(0, total - explained);
  return {
    mode: t("processComposition"),
    parts: [
      { key: "mapped", tone: "mapped", label: t("mapped"), value: mapped ?? 0, pct: mapped !== null ? (mapped / scale) * 100 : 0 },
      { key: "unmatched", tone: "unmatched", label: t("unmatched"), value: unmatched ?? 0, pct: unmatched !== null ? (unmatched / scale) * 100 : 0 },
      ...(unknown > 0 ? [{ key: "unknown", tone: "unknown", label: t("unknown"), value: unknown, pct: (unknown / scale) * 100 }] : []),
    ],
  };
}

function compactTrendParts(parts: TrendDrilldownPart[], scale: number): TrendDrilldownPart[] {
  if (parts.length <= 3) return parts;
  const visible = parts.slice(0, 2);
  const rest = parts.slice(2);
  const restValue = rest.reduce((sum, item) => sum + item.value, 0);
  return [
    ...visible,
    {
      key: "other",
      tone: "other",
      label: `+${rest.length}`,
      value: restValue,
      pct: (restValue / Math.max(scale, 1)) * 100,
    },
  ];
}

function TrendDrilldownMark({ part }: { part: TrendDrilldownPart }) {
  const icon = toolIconName(part.tool);
  if (icon) {
    return <img alt="" aria-hidden="true" src={`/api/tool-icon/${icon}`} />;
  }
  return <i aria-hidden="true">{part.label.slice(0, 1).toUpperCase()}</i>;
}

function TrendSignalChart({
  t,
  lane,
  title,
  points,
  selectedAt,
  onSelect,
}: {
  t: Translate;
  lane: TrendLane;
  title: string;
  points: TrendPoint[];
  selectedAt?: string;
  onSelect: (at?: string) => void;
}) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const areaRef = useRef<ISeriesApi<"Area"> | null>(null);
  const data = useMemo(() => trendSignalData(points, lane), [points, lane]);
  // The chart instance outlives snapshot polls, so click/crosshair handlers and
  // the data pushes read the latest values through refs instead of effect deps.
  const dataRef = useRef(data);
  const onSelectRef = useRef(onSelect);
  const selected = selectedAt ? data.find((item) => item.at === selectedAt) ?? data[data.length - 1] : data[data.length - 1];
  const [overlay, setOverlay] = useState<TrendSignalOverlay | null>(null);
  const [hover, setHover] = useState<TrendSignalHover | null>(null);
  const [sizeKey, setSizeKey] = useState(0);
  const theme = useResolvedTheme();

  useEffect(() => {
    onSelectRef.current = onSelect;
  }, [onSelect]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const styles = getComputedStyle(document.documentElement);
    const bg = cssVar(styles, "--bg");
    const faint = cssVar(styles, "--fg-faint");
    const line = cssVar(styles, "--console-line-soft");
    const ok = cssVar(styles, "--ok");
    const run = cssVar(styles, "--run");
    const accent = cssVar(styles, "--accent");
    const accentHi = cssVar(styles, "--accent-hi");
    const rect = host.getBoundingClientRect();
    const chart = createChart(host, {
      width: Math.max(1, Math.floor(rect.width)),
      height: Math.max(1, Math.floor(rect.height)),
      autoSize: false,
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: faint,
        fontFamily: styles.getPropertyValue("--font-mono").trim() || undefined,
        fontSize: 10,
        attributionLogo: false,
      },
      grid: {
        vertLines: { color: "transparent", visible: false },
        horzLines: { color: colorMix(line, 0.34), style: LineStyle.SparseDotted },
      },
      rightPriceScale: { visible: false, borderVisible: false },
      leftPriceScale: { visible: false, borderVisible: false },
      timeScale: {
        borderVisible: false,
        timeVisible: true,
        secondsVisible: false,
        fixLeftEdge: true,
        fixRightEdge: true,
        barSpacing: trendBarSpacing(dataRef.current.length),
        minBarSpacing: 2.8,
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: accentHi, labelVisible: false, style: LineStyle.Dashed, width: 1 },
        horzLine: { color: accent, labelVisible: false, style: LineStyle.Dashed, width: 1 },
      },
      localization: {
        timeFormatter: (time: Time) => formatSignalTime(time),
      },
      handleScroll: false,
      handleScale: false,
    });
    const primaryColor = lane === "history" ? ok : lane === "runtime" ? run : accent;
    const area = chart.addSeries(AreaSeries, {
      lineVisible: true,
      lineColor: colorMix(primaryColor, 0.92),
      lineWidth: 2,
      topColor: colorMix(primaryColor, 0.28),
      bottomColor: colorMix(bg, 0.0),
      priceLineVisible: false,
      lastValueVisible: false,
      crosshairMarkerVisible: true,
      crosshairMarkerRadius: 3,
    });
    const handleClick = (param: { time?: Time }) => {
      if (!param.time || !dataRef.current.length) return;
      const clicked = nearestSignalDatum(dataRef.current, param.time);
      onSelectRef.current(clicked?.at);
    };
    const handleCrosshairMove = (param: MouseEventParams<Time>) => {
      const point = param.point;
      if (!param.time || !point || point.x < 0 || point.y < 0 || !dataRef.current.length) {
        setHover(null);
        return;
      }
      const hovered = nearestSignalDatum(dataRef.current, param.time);
      if (!hovered) {
        setHover(null);
        return;
      }
      const width = host.clientWidth || rect.width;
      const height = host.clientHeight || rect.height;
      setHover({
        datum: hovered,
        left: clampNumber(point.x, 8, Math.max(8, width - 8)),
        top: clampNumber(point.y, 10, Math.max(10, height - 10)),
        align: point.x > width - 156 ? "right" : "left",
        vertical: point.y < 72 ? "below" : "above",
      });
    };
    chart.subscribeClick(handleClick);
    chart.subscribeCrosshairMove(handleCrosshairMove);
    chartRef.current = chart;
    areaRef.current = area;
    if (dataRef.current.length) {
      applyTrendSignalData(chart, area, dataRef.current);
    }
    // Re-sync the selection overlay with the freshly created chart.
    setSizeKey((value) => value + 1);
    const resizeObserver = new ResizeObserver((entries) => {
      const entry = entries[0];
      if (!entry) return;
      const width = Math.max(1, Math.floor(entry.contentRect.width));
      const height = Math.max(1, Math.floor(entry.contentRect.height));
      chart.resize(width, height);
      setSizeKey((value) => value + 1);
    });
    resizeObserver.observe(host);
    return () => {
      resizeObserver.disconnect();
      chart.unsubscribeClick(handleClick);
      chart.unsubscribeCrosshairMove(handleCrosshairMove);
      chart.remove();
      chartRef.current = null;
      areaRef.current = null;
    };
  }, [lane, theme]);

  useEffect(() => {
    dataRef.current = data;
    const chart = chartRef.current;
    const areaSeries = areaRef.current;
    if (!chart || !areaSeries) return;
    applyTrendSignalData(chart, areaSeries, data);
    setHover(null);
  }, [data]);

  useEffect(() => {
    const chart = chartRef.current;
    const areaSeries = areaRef.current;
    if (!chart || !areaSeries || !selected) {
      setOverlay(null);
      return;
    }
    const update = () => {
      const left = chart.timeScale().timeToCoordinate(selected.time);
      const top = areaSeries.priceToCoordinate(selected.value);
      setOverlay(left === null || top === null ? null : { left, top });
    };
    update();
    const frame = window.requestAnimationFrame(update);
    return () => window.cancelAnimationFrame(frame);
  }, [selected, sizeKey]);

  const hoverMetrics = hover ? trendSelectedReadoutParts(t, lane, hover.datum) : [];
  const hoverDelta = hover ? hover.datum.value - hover.datum.previous : 0;

  return (
    <div className={`trend-chart trend-signal-chart lane-${lane}`} onMouseLeave={() => setHover(null)}>
      <div className="trend-signal-host" ref={hostRef} role="img" aria-label={`${title} ${t("trend")}`} />
      {overlay ? (
        <div className="trend-signal-selection" style={{ "--kx": `${overlay.left}px`, "--ky": `${overlay.top}px` } as React.CSSProperties} aria-hidden="true">
          <span className="trend-signal-selection-dot" />
        </div>
      ) : null}
      {hover ? (
        <div
          className={`trend-signal-tooltip align-${hover.align} is-${hover.vertical}`}
          style={{ "--tip-x": `${hover.left}px`, "--tip-y": `${hover.top}px` } as React.CSSProperties}
          aria-hidden="true"
        >
          <span className="trend-signal-tooltip-kicker">{t("trendExactBucket")}</span>
          <strong className="trend-signal-tooltip-time">{formatChartAxisLabel(hover.datum.at)}</strong>
          <div className="trend-signal-tooltip-grid">
            {hoverMetrics.map((metric) => (
              <span key={metric.label}>
                <b>{metric.label}</b>
                <em>{metric.value}</em>
              </span>
            ))}
            <span>
              <b>Δ</b>
              <em>{trendDeltaValue(lane, hoverDelta)}</em>
            </span>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function TrendSelectionInspector({ t, summary, compact }: { t: Translate; summary?: TrendLaneSummary; compact: boolean }) {
  const detailsId = React.useId();
  const [expanded, setExpanded] = useState(false);
  const point = summary?.selected;
  if (!summary || !point) return null;
  const contextMetrics = trendContextMetrics(t, summary.trendWindow);
  const metrics = trendDetailMetrics(t, summary.lane, point);
  const sections = trendExplanationSections(t, summary.lane, point);
  const primaryMetric = metrics[0];
  const secondaryMetric = metrics[1];
  const mappedMetric = summary.lane === "runtime" ? metrics[2] : undefined;
  const unmappedMetric = summary.lane === "runtime" ? metrics[3] : undefined;
  return (
    <aside className={`trend-inspector ${summary.lane} ${compact ? "compact" : ""} ${expanded ? "expanded" : ""}`}>
      <div className="trend-inspector-lead">
        <div className="trend-inspector-anchor">
          <span>{t("trendSelectedBucket")} · {summary.title}</span>
          <strong>{point.at ? formatDateTime(point.at) : t("unavailable")}</strong>
        </div>
        <div className="trend-inspector-readouts" aria-label={t("selectedValues")}>
          {summary.lane === "runtime" && primaryMetric && mappedMetric && unmappedMetric ? (
            <div className="trend-inspector-equation">
              <span className="trend-inspector-unit primary">
                <b>{primaryMetric.label}</b>
                <strong>{primaryMetric.value}</strong>
              </span>
              <i>=</i>
              <span className="trend-inspector-unit mapped">
                <b>{mappedMetric.label}</b>
                <strong>{mappedMetric.value}</strong>
              </span>
              <i>+</i>
              <span className="trend-inspector-unit unmatched">
                <b>{unmappedMetric.label}</b>
                <strong>{unmappedMetric.value}</strong>
              </span>
              {secondaryMetric ? (
                <span className="trend-inspector-ratio">
                  <b>{secondaryMetric.label}</b>
                  <strong>{secondaryMetric.value}</strong>
                </span>
              ) : null}
            </div>
          ) : (
            <div className="trend-inspector-equation history">
              {metrics.slice(0, 2).map((metric, index) => (
                <span className={`trend-inspector-unit ${index === 0 ? "primary" : "context"}`} key={metric.label}>
                  <b>{metric.label}</b>
                  <strong>{metric.value}</strong>
                </span>
              ))}
            </div>
          )}
        </div>
        <button
          aria-controls={detailsId}
          aria-expanded={expanded}
          aria-label={expanded ? t("collapseDetails") : t("expandDetails")}
          className="disclosure-icon-btn trend-detail-toggle"
          data-focus-key={focusKey("trend-detail", summary.lane, point.at || "", compact ? "compact" : "full")}
          title={expanded ? t("collapseDetails") : t("expandDetails")}
          type="button"
          onClick={() => setExpanded((current) => !current)}
        >
          <Info size={13} aria-hidden="true" />
          <span>{expanded ? t("collapseDetails") : t("expandDetails")}</span>
        </button>
      </div>
      <div className="trend-detail-details trend-inspector-details" id={detailsId} hidden={!expanded}>
        <div className="trend-detail-grid trend-context-grid">
          {contextMetrics.map((metric) => (
            <span className="trend-detail-metric context" key={metric.label}>
              <b>{metric.label}</b>
              <strong>{metric.value}</strong>
            </span>
          ))}
        </div>
        <div className="trend-detail-sections">
          {sections.map((section) => (
            <section key={section.label}>
              <span>{section.label}</span>
              <p>{section.text}</p>
            </section>
          ))}
        </div>
      </div>
    </aside>
  );
}

function activeTrendRanges(snapshot: TrendSnapshot): TrendRange[] {
  return TREND_RANGES.filter((range) => Boolean(trendWindowForRange(snapshot.trends, range) || trendWindowForRange(snapshot.realtime_trends, range) || trendWindowForRange(snapshot.throughput_trends, range)));
}

function trendWindowForRange(set: TrendSet | undefined, range: TrendRange): TrendWindow | undefined {
  return set?.windows?.find((window) => window.range === range);
}

function projectHeatmapWindowForRange(set: ProjectHeatmapSet | undefined, range: TrendRange): ProjectHeatmapWindow | undefined {
  return set?.windows?.find((window) => window.range === range);
}

function trendLaneSummary(lane: TrendLane, title: string, trendWindow: TrendWindow | undefined, selectedAt?: string): TrendLaneSummary {
  const sampleKey = lane === "history" ? "transcript_sampled" : lane === "runtime" ? "runtime_sampled" : "throughput_sampled";
  const sampled = sampledPoints(trendWindow, sampleKey);
  const points = lane === "throughput" ? sampled.filter((point) => trendOutputThroughputProjects(point) !== null) : sampled;
  const data = trendSignalData(points, lane);
  return {
    lane,
    title,
    trendWindow,
    points,
    data,
    selected: selectedTrendDatum(data, selectedAt),
  };
}

function selectedTrendDatum(data: TrendSignalDatum[], selectedAt?: string): TrendSignalDatum | undefined {
  return data.find((datum) => datum.at === selectedAt) ?? [...data].reverse().find((datum) => datum.value > 0) ?? data[data.length - 1];
}

function sampledPoints(window: TrendWindow | undefined, sampledKey: "transcript_sampled" | "runtime_sampled" | "throughput_sampled"): TrendPoint[] {
  return [...(window?.points ?? [])]
    .filter((point) => sampledKey === "throughput_sampled" ? point.throughput_sampled : point[sampledKey] || point.at)
    .sort((a, b) => String(a.at).localeCompare(String(b.at)));
}

function trendSignalData(points: TrendPoint[], lane: TrendLane): TrendSignalDatum[] {
  const byTime = new Map<number, { at: string; value: number; point: TrendPoint }>();
  points.forEach((point) => {
    const at = point.at;
    const value = trendPrimaryValue(lane, point);
    const ms = pointTimeMs(at);
    if (!at || value === null || ms === null) return;
    byTime.set(Math.floor(ms / 1000), { at, value, point });
  });
  const sorted = Array.from(byTime.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([seconds, item]) => ({ ...item, time: seconds as Time }));
  return sorted.map((item, index) => {
    const previous = sorted[Math.max(0, index - 1)];
    return {
      at: item.at,
      time: item.time,
      value: item.value,
      point: item.point,
      previous: previous?.value ?? item.value,
    };
  });
}

function applyTrendSignalData(chart: IChartApi, areaSeries: ISeriesApi<"Area">, data: TrendSignalDatum[]) {
  areaSeries.setData(data.map(({ time, value }) => ({ time, value })));
  chart.timeScale().applyOptions({ barSpacing: trendBarSpacing(data.length) });
  chart.timeScale().fitContent();
}

function trendBarSpacing(count: number): number {
  return count > 72 ? 4.2 : count > 36 ? 6.6 : 9.2;
}

// Chart colors are resolved from computed styles at creation time, so the
// create effect must re-run when the document theme flips.
function useResolvedTheme(): string {
  const [theme, setTheme] = useState(() => document.documentElement.dataset.theme || "dark");
  useEffect(() => {
    const observer = new MutationObserver(() => {
      setTheme(document.documentElement.dataset.theme || "dark");
    });
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
    return () => observer.disconnect();
  }, []);
  return theme;
}

function nearestSignalDatum(data: TrendSignalDatum[], time: Time): TrendSignalDatum | undefined {
  const target = typeof time === "number" ? time : Date.parse(String(time)) / 1000;
  if (!Number.isFinite(target)) return data[data.length - 1];
  return data.reduce((best, item) => {
    const current = typeof item.time === "number" ? item.time : Date.parse(String(item.time)) / 1000;
    const bestTime = typeof best.time === "number" ? best.time : Date.parse(String(best.time)) / 1000;
    return Math.abs(current - target) < Math.abs(bestTime - target) ? item : best;
  }, data[0]);
}

function formatSignalTime(time: Time): string {
  const seconds = typeof time === "number" ? time : Date.parse(String(time)) / 1000;
  if (!Number.isFinite(seconds)) return String(time);
  return formatChartAxisLabel(new Date(seconds * 1000).toISOString());
}

function projectHeatmapTiles(items: ProjectHeatmapItem[]): ProjectHeatmapTile[] {
  const entries = items
    .map((item, index) => ({
      ...item,
      value: Math.max(0, item.session_window_count ?? 0, item.window_count ?? 0),
      tone: index % 6,
    }))
    .filter((item) => item.value > 0)
    .sort((a, b) => {
      if (b.value === a.value) return String(a.project || "").localeCompare(String(b.project || ""));
      return b.value - a.value;
    });
  if (!entries.length) return [];
  const layout = (nodes: typeof entries, x: number, y: number, width: number, height: number): ProjectHeatmapTile[] => {
    if (nodes.length === 1) return [{ ...nodes[0], x, y, width, height }];
    const total = nodes.reduce((sum, item) => sum + item.value, 0);
    if (total <= 0) return [];
    let splitIndex = 1;
    let running = 0;
    let bestDiff = Number.POSITIVE_INFINITY;
    for (let index = 0; index < nodes.length - 1; index++) {
      running += nodes[index].value;
      const diff = Math.abs(total / 2 - running);
      if (diff < bestDiff) {
        bestDiff = diff;
        splitIndex = index + 1;
      }
    }
    const first = nodes.slice(0, splitIndex);
    const second = nodes.slice(splitIndex);
    const firstTotal = first.reduce((sum, item) => sum + item.value, 0);
    const ratio = clampNumber(firstTotal / total, 0.08, 0.92);
    if (width >= height) {
      const firstWidth = width * ratio;
      return [
        ...layout(first, x, y, firstWidth, height),
        ...layout(second, x + firstWidth, y, width - firstWidth, height),
      ];
    }
    const firstHeight = height * ratio;
    return [
      ...layout(first, x, y, width, firstHeight),
      ...layout(second, x, y + firstHeight, width, height - firstHeight),
    ];
  };
  return layout(entries, 0, 0, 100, 100);
}

function trendDetailMetrics(t: Translate, lane: TrendLane, datum: TrendSignalDatum): Array<{ label: string; value: string }> {
  const point = datum.point;
  if (lane === "history") {
    return [
      { label: t("metricFresh"), value: trendMetricValue(t, datum.value) },
      { label: t("metricSessions"), value: trendMetricValue(t, trendContextSessionValue(point) ?? undefined) },
    ];
  }
  if (lane === "throughput") {
    return [
      { label: t("outputThroughput"), value: formatTrendTokenRate(t, trendOutputThroughputValue(point)) },
      { label: t("trendThroughputState"), value: formatThroughputState(t, point) },
      { label: t("trendThroughputWindow"), value: formatTrendThroughputWindow(t, point) },
      { label: t("trendReadoutContributors"), value: trendMetricValue(t, trendOutputThroughputActiveSessions(point) ?? undefined) },
    ];
  }
  return [
    { label: t("metricProcesses"), value: trendMetricValue(t, datum.value) },
    { label: t("metricMatched"), value: trendMappingCoverageValue(point) !== null ? formatPct(trendMappingCoverageValue(point) ?? undefined) : t("unavailable") },
    { label: t("mappedProcesses"), value: trendMetricValue(t, trendMappedProcessCount(point) ?? undefined) },
    { label: t("unmappedProcesses"), value: trendMetricValue(t, trendUnmappedProcessCount(point) ?? undefined) },
  ];
}

function trendMetricValue(t: Translate, value?: number): string {
  return typeof value === "number" && Number.isFinite(value) ? String(value) : t("unavailable");
}

function trendSelectedReadout(t: Translate, lane: TrendLane, datum: TrendSignalDatum): string {
  const point = datum.point;
  if (lane === "history") {
    return `${trendMetricValue(t, datum.value)} / ${trendMetricValue(t, trendContextSessionValue(point) ?? undefined)}`;
  }
  if (lane === "throughput") {
    return `${formatTrendTokenRate(t, trendOutputThroughputValue(point))} / ${trendMetricValue(t, trendOutputThroughputActiveSessions(point) ?? undefined)} ${t("trendReadoutContributors")}`;
  }
  return `${trendMetricValue(t, datum.value)} / ${trendMappingCoverageValue(point) !== null ? formatPct(trendMappingCoverageValue(point) ?? undefined) : t("unavailable")}`;
}

function trendSelectedReadoutParts(t: Translate, lane: TrendLane, datum: TrendSignalDatum): Array<{ label: string; value: string; role: "primary" | "context" }> {
  const point = datum.point;
  if (lane === "history") {
    return [
      { label: t("trendReadoutFresh"), value: trendMetricValue(t, datum.value), role: "primary" },
      { label: t("trendReadoutSessions"), value: trendMetricValue(t, trendContextSessionValue(point) ?? undefined), role: "context" },
    ];
  }
  if (lane === "throughput") {
    return [
      { label: t("tokenRateUnit"), value: formatTokenRate(trendOutputThroughputValue(point)), role: "primary" },
      { label: t("trendReadoutContributors"), value: trendMetricValue(t, trendOutputThroughputActiveSessions(point) ?? undefined), role: "context" },
    ];
  }
  return [
    { label: t("trendReadoutProcesses"), value: trendMetricValue(t, datum.value), role: "primary" },
    { label: t("trendReadoutMatched"), value: trendMappingCoverageValue(point) !== null ? formatPct(trendMappingCoverageValue(point) ?? undefined) : t("unavailable"), role: "context" },
  ];
}

function trendContextMetrics(t: Translate, window?: TrendWindow): Array<{ label: string; value: string }> {
  const granularity = typeof window?.granularity_seconds === "number" && window.granularity_seconds > 0 ? formatAge(window.granularity_seconds, t) : t("unavailable");
  const sourceLookback = typeof window?.source_lookback_hours === "number" && Number.isFinite(window.source_lookback_hours) ? `${window.source_lookback_hours}h` : "";
  const source = window?.source_from ? `${formatDateTime(window.source_from)}${sourceLookback ? ` · ${sourceLookback}` : ""}` : sourceLookback || t("unavailable");
  const completeness = window?.history_complete === true ? t("complete") : window?.history_complete === false ? t("partial") : t("unavailable");
  return [
    { label: t("trendRange"), value: window?.range || t("unavailable") },
    { label: t("trendBucket"), value: granularity },
    { label: t("trendWindow"), value: formatTrendWindow(t, window) },
    { label: t("trendSourceWindow"), value: source },
    { label: t("trendCompleteness"), value: completeness },
  ];
}

function trendExplanationSections(t: Translate, lane: TrendLane, datum: TrendSignalDatum): Array<{ label: string; text: string }> {
  const clicked = `${t("sampledBucket")} ${datum.at ? formatDateTime(datum.at) : t("unavailable")} · ${trendSelectedReadout(t, lane, datum)}`;
  const meaning = lane === "history" ? t("trendHistoryMeaning") : lane === "runtime" ? t("trendRuntimeMeaning") : t("trendThroughputMeaning");
  const trust = lane === "history" ? t("trendHistoryTrust") : lane === "runtime" ? t("trendRuntimeTrust") : t("trendThroughputTrust");
  const use = lane === "history" ? t("trendHistoryUse") : lane === "runtime" ? t("trendRuntimeUse") : t("trendThroughputUse");
  return [
    { label: t("trendWhatClicked"), text: clicked },
    { label: t("trendWhatMeans"), text: meaning },
    { label: t("whyTrust"), text: trust },
    { label: t("trendHowUse"), text: use },
  ];
}

function formatTrendTokenRate(t: Translate, value: number | null): string {
  return value === null ? t("unavailable") : `${formatTokenRate(value)} ${t("tokenRateUnit")}`;
}

function formatThroughputState(t: Translate, point: TrendPoint): string {
  const state = trendOutputThroughputState(point);
  if (state === "live") return t("trendThroughputStateLive");
  if (state === "zero") return t("trendThroughputStateZero");
  if (state === "stale") return t("trendThroughputStateStale");
  if (state === "unavailable") return t("trendThroughputStateUnavailable");
  return t("trendThroughputStateNoData");
}

function formatTrendThroughputWindow(t: Translate, point: TrendPoint): string {
  const seconds = trendOutputThroughputWindowSeconds(point);
  return seconds === null ? t("unavailable") : formatRelativeAge(seconds, t);
}

function trendDeltaValue(lane: TrendLane, value: number): string {
  const sign = value > 0 ? "+" : value < 0 ? "-" : "";
  const magnitude = lane === "throughput" ? formatTokenRate(Math.abs(value)) : String(Math.abs(value));
  return `${sign}${magnitude}`;
}

function formatTrendWindow(t: Translate, window: TrendWindow | ProjectHeatmapWindow | undefined): string {
  const firstAt = window?.from;
  const lastAt = window?.to;
  if (!firstAt && !lastAt) return t("unavailable");
  const first = firstAt ? formatChartAxisLabel(firstAt) : "";
  const last = lastAt ? formatChartAxisLabel(lastAt) : "";
  return first && last ? `${first} -> ${last}` : window?.range || t("unavailable");
}

function pointTimeMs(value?: string): number | null {
  if (!value) return null;
  const time = new Date(value).getTime();
  return Number.isFinite(time) ? time : null;
}

function cssVar(styles: CSSStyleDeclaration, name: string): string {
  return styles.getPropertyValue(name).trim();
}

function colorMix(color: string, alpha: number): string {
  const text = color.trim();
  const clamped = clampNumber(alpha, 0, 1);
  if (text.startsWith("#")) {
    const hex = text.slice(1);
    const normalized = hex.length === 3 ? hex.split("").map((char) => char + char).join("") : hex;
    const value = Number.parseInt(normalized, 16);
    if (Number.isFinite(value)) {
      const r = (value >> 16) & 255;
      const g = (value >> 8) & 255;
      const b = value & 255;
      return `rgba(${r}, ${g}, ${b}, ${clamped})`;
    }
  }
  const rgbMatch = text.match(/^rgba?\(([^)]+)\)$/);
  if (rgbMatch) {
    const parts = rgbMatch[1].split(",").map((part) => part.trim()).slice(0, 3);
    if (parts.length === 3) return `rgba(${parts.join(", ")}, ${clamped})`;
  }
  return text;
}

function formatDateTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return `${date.toLocaleDateString(undefined, { month: "numeric", day: "numeric" })} ${date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false })}`;
}

function formatChartAxisLabel(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return `${date.toLocaleDateString(undefined, { month: "numeric", day: "numeric" })} ${date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })}`;
}

function formatAge(seconds?: number, t?: Translate): string {
  if (typeof seconds !== "number" || !Number.isFinite(seconds)) return t ? t("unavailable") : "";
  if (seconds < 60) return `${Math.max(0, Math.round(seconds))}s`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m`;
  return `${Math.round(seconds / 3600)}h`;
}

function focusKey(...parts: Array<string | number | boolean | null | undefined>): string {
  return parts.filter((part) => part !== null && part !== undefined && part !== "").map((part) => String(part).replace(/\s+/g, "_")).join(":");
}
