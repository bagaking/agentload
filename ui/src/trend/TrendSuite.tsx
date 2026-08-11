import React, { useCallback, useMemo, useState } from "react";
import { Gauge, Info, Layers } from "lucide-react";
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
import { ActivityProcessTrend } from "./ActivityProcessTrend";
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
  time: number;
  value: number;
  point: TrendPoint;
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
    trendLaneSummary("history", t("trendActiveSessions"), history, trendSelection.history),
    trendLaneSummary("runtime", t("trendVisiblePids"), runtime, trendSelection.runtime),
    trendLaneSummary("throughput", t("throughputLane"), throughput, trendSelection.throughput),
  ].filter((summary) => Boolean(summary.trendWindow || summary.points.length)), [t, history, runtime, throughput, trendSelection.history, trendSelection.runtime, trendSelection.throughput]);
  const historySummary = laneSummaries.find((summary) => summary.lane === "history");
  const runtimeSummary = laneSummaries.find((summary) => summary.lane === "runtime");
  const throughputSummary = laneSummaries.find((summary) => summary.lane === "throughput");
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
            {historySummary || runtimeSummary ? (
              <ActivityProcessLane
                t={t}
                history={historySummary}
                runtime={runtimeSummary}
                focusedLane={focusedLane}
                compact={compact}
                setFocusedLane={setFocusedLane}
                setTrendSelection={setTrendSelection}
              />
            ) : null}
            {throughputSummary ? (
              <ThroughputLaneView
                t={t}
                summary={throughputSummary}
                isFocused={activeSummary?.lane === "throughput"}
                setFocusedLane={setFocusedLane}
                setTrendSelection={setTrendSelection}
              />
            ) : null}
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

function ActivityProcessLane({
  t,
  history,
  runtime,
  focusedLane,
  compact,
  setFocusedLane,
  setTrendSelection,
}: {
  t: Translate;
  history?: TrendLaneSummary;
  runtime?: TrendLaneSummary;
  focusedLane: TrendLane;
  compact: boolean;
  setFocusedLane: (lane: TrendLane) => void;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const historySelected = history?.selected;
  const runtimeSelected = runtime?.selected;
  const historyWindow = history?.trendWindow;
  const runtimeWindow = runtime?.trendWindow;
  const range = historyWindow?.range || runtimeWindow?.range || t("unavailable");
  const sampleMeta = formatCopy(t("activityProcessSamples"), {
    sessions: history?.data.length ?? 0,
    processes: runtime?.data.length ?? 0,
  });
  const selectHistory = useCallback(() => {
    setFocusedLane("history");
    if (historySelected?.at) setTrendSelection((current) => ({ ...current, history: historySelected.at }));
  }, [historySelected?.at, setFocusedLane, setTrendSelection]);
  const selectRuntime = useCallback(() => {
    setFocusedLane("runtime");
    if (runtimeSelected?.at) setTrendSelection((current) => ({ ...current, runtime: runtimeSelected.at }));
  }, [runtimeSelected?.at, setFocusedLane, setTrendSelection]);
  const selectChartPoints = useCallback(({ historyAt, runtimeAt }: { historyAt?: string; runtimeAt?: string }) => {
    setTrendSelection((current) => ({
      ...current,
      history: historyAt || current.history,
      runtime: runtimeAt || current.runtime,
    }));
  }, [setTrendSelection]);
  return (
    <article className={`trend-lane activity-process ${focusedLane !== "throughput" ? "is-focused" : ""}`}>
      <div className="trend-lane-head activity-process-head">
        <div className="trend-lane-title">
          <span className="trend-kicker">{t("activityProcessLane")}</span>
          <small>{range} · {sampleMeta}</small>
        </div>
        <div className="activity-process-legend" role="group" aria-label={t("selectedValues")}>
          <button
            aria-pressed={focusedLane === "history"}
            className="active"
            data-focus-key={focusKey("trend-legend", "active", historySelected?.at || "")}
            disabled={!historySelected}
            title={historySelected?.at ? formatDateTime(historySelected.at) : t("unavailable")}
            type="button"
            onClick={selectHistory}
          >
            <i aria-hidden="true" /><span>{t("trendActiveSessions")}</span><strong>{trendMetricValue(t, historySelected?.value)}</strong>
          </button>
          <button
            aria-pressed={focusedLane === "history"}
            className="sessions"
            data-focus-key={focusKey("trend-legend", "sessions", historySelected?.at || "")}
            disabled={!historySelected}
            title={historySelected?.at ? formatDateTime(historySelected.at) : t("unavailable")}
            type="button"
            onClick={selectHistory}
          >
            <i aria-hidden="true" /><span>{t("metricKnownSessions")}</span><strong>{trendMetricValue(t, historySelected ? trendContextSessionValue(historySelected.point) ?? undefined : undefined)}</strong>
          </button>
          <button
            aria-pressed={focusedLane === "runtime"}
            className="processes"
            data-focus-key={focusKey("trend-legend", "processes", runtimeSelected?.at || "")}
            disabled={!runtimeSelected}
            title={runtimeSelected?.at ? formatDateTime(runtimeSelected.at) : t("unavailable")}
            type="button"
            onClick={selectRuntime}
          >
            <i aria-hidden="true" /><span>{t("trendVisiblePids")}</span><strong>{trendMetricValue(t, runtimeSelected?.value)}</strong>
          </button>
        </div>
      </div>
      {compact && focusedLane === "runtime" ? <TrendRuntimeDrilldown t={t} summary={runtime} /> : null}
      {(history?.data.length ?? 0) + (runtime?.data.length ?? 0) > 0 ? (
        <ActivityProcessTrend
          t={t}
          title={t("activityProcessLane")}
          historyPoints={history?.points ?? []}
          runtimePoints={runtime?.points ?? []}
          from={historyWindow?.from || runtimeWindow?.from}
          to={historyWindow?.to || runtimeWindow?.to}
          granularitySeconds={historyWindow?.granularity_seconds || runtimeWindow?.granularity_seconds}
          selectedHistoryAt={historySelected?.at}
          selectedRuntimeAt={runtimeSelected?.at}
          onSelect={selectChartPoints}
        />
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </article>
  );
}

function ThroughputLaneView({
  t,
  summary,
  isFocused,
  setFocusedLane,
  setTrendSelection,
}: {
  t: Translate;
  summary: TrendLaneSummary;
  isFocused: boolean;
  setFocusedLane: (lane: TrendLane) => void;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const { title, trendWindow, points, selected } = summary;
  const periodSummary = trendWindow?.output_token_rate_summary;
  const periodMetrics = [
    { label: "MAX", value: formatThroughputPeriodRate(periodSummary?.max) },
    { label: "P95", value: formatThroughputPeriodRate(periodSummary?.p95) },
    { label: "AVG", value: formatThroughputPeriodRate(periodSummary?.avg) },
    { label: "CUR(5m)", value: formatThroughputPeriodRate(periodSummary?.current) },
  ];
  const selectPoint = useCallback((at?: string) => {
    setFocusedLane("throughput");
    if (at) setTrendSelection((current) => ({ ...current, throughput: at }));
  }, [setFocusedLane, setTrendSelection]);
  return (
    <article className={`trend-lane throughput ${isFocused ? "is-focused" : ""}`} onPointerEnter={() => setFocusedLane("throughput")}>
      <div className="trend-lane-head">
        <div className="trend-lane-title">
          <span className="trend-kicker">{title}</span>
          <small>{trendWindow?.range || t("unavailable")} · {periodSummary?.sample_count ?? 0} {t("samples")} · 5m</small>
        </div>
        <dl className="throughput-period-summary" aria-label={t("selectedValues")}>
          {periodMetrics.map((metric) => (
            <div key={metric.label} title={`${metric.label}: ${metric.value} ${t("tokenRateUnit")}`}>
              <dt>{metric.label}</dt>
              <dd>{metric.value}</dd>
            </div>
          ))}
        </dl>
      </div>
      {summary.data.length ? (
        <ThroughputRiver
          key={trendWindow?.range}
          t={t}
          title={title}
          points={points}
          from={trendWindow?.from}
          to={trendWindow?.to}
          selectedAt={selected?.at}
          onSelect={selectPoint}
        />
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </article>
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
    selected: selectedTrendDatum(data, selectedAt, lane),
  };
}

function selectedTrendDatum(data: TrendSignalDatum[], selectedAt: string | undefined, lane: TrendLane): TrendSignalDatum | undefined {
  const selected = data.find((datum) => datum.at === selectedAt);
  if (selected) return selected;
  if (lane === "throughput") return data[data.length - 1];
  return [...data].reverse().find((datum) => datum.value > 0) ?? data[data.length - 1];
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
    .map(([seconds, item]) => ({ ...item, time: seconds }));
  return sorted;
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

function formatThroughputPeriodRate(value?: number): string {
  return typeof value === "number" && Number.isFinite(value) ? formatTokenRate(value) : "n/a";
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
