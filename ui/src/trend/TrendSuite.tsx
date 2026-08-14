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
  type ThroughputTrendSeries,
  type TrendWindow,
} from "./types";
import { toolDisplayName } from "../lib/activityModel";
import { ActivityProcessTrend } from "./ActivityProcessTrend";
import { ThroughputRiver } from "./ThroughputRiver";
import { TrendRangeRail, activeTrendRanges, trendWindowForRange } from "./TrendRangeRail";

export type TrendSnapshot = {
  trends?: TrendSet;
  realtime_trends?: TrendSet;
  throughput_trends?: TrendSet;
  project_heatmaps?: ProjectHeatmapSet;
  project_focus?: ProjectHeatmapActivity[];
  current?: TrendCurrentMetrics;
  historic_peaks?: { today?: { active_burst_concurrency?: TrendPeakPoint } };
};

// The live counts the activity hero reads. These are a different evidence
// family from the trend windows -- measured at the snapshot instant rather than
// swept from spans -- which is exactly why the hero can answer "right now"
// while the chart's right edge honestly stops at the last measurable bucket.
type TrendCurrentMetrics = {
  pid_concurrency?: number;
  session_concurrency?: number;
  active_burst_concurrency?: number;
};

type TrendPeakPoint = { value?: number; at?: string };

type ProjectHeatmapActivity = {
  project?: string;
  worktrees?: { name?: string }[];
  branches?: string[];
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
  throughputSeries?: ThroughputTrendSeries;
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
  liveReadout,
  compact = false,
  lane = "throughput",
  range,
  setRange,
  trendSelection,
  setTrendSelection,
}: {
  t: Translate;
  snapshot: TrendSnapshot;
  liveReadout?: React.ReactNode;
  compact?: boolean;
  lane?: "throughput" | "activity";
  range: TrendRange;
  setRange: (value: TrendRange) => void;
  trendSelection: Record<TrendLane, string | undefined>;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const currentLane = lane || "throughput";
  const [throughputSeriesKey, setThroughputSeriesKey] = useState("minute:300");
  const activeRanges = activeTrendRanges(snapshot);
  const effectiveRange = activeRanges.includes(range) ? range : activeRanges[0] ?? range;
  const history = trendWindowForRange(snapshot.trends, effectiveRange);
  const runtime = trendWindowForRange(snapshot.realtime_trends, effectiveRange);
  const throughput = trendWindowForRange(snapshot.throughput_trends, effectiveRange);
  const throughputSeries = selectedThroughputSeries(throughput, throughputSeriesKey);
  const throughputView = useMemo(() => throughputSeriesWindow(throughput, throughputSeries), [throughput, throughputSeries]);
  const projectHeatmap = projectHeatmapWindowForRange(snapshot.project_heatmaps, effectiveRange);
  const projectActivity = useMemo(() => projectHeatmapActivityByProject(snapshot.project_focus), [snapshot.project_focus]);
  const projectWorktrees = useMemo(() => {
    const map = new Map<string, { worktrees?: string[]; branches?: string[] }>();
    (snapshot.project_focus ?? []).forEach((p) => {
      if (p.project) {
        map.set(p.project.toLowerCase(), {
          worktrees: (p.worktrees ?? []).map((w) => w.name).filter((name): name is string => !!name),
          branches: p.branches,
        });
      }
    });
    return map;
  }, [snapshot.project_focus]);
  const [focusedLane, setFocusedLane] = useState<TrendLane>("throughput");
  // Stable summary (and points) identities keep the chart data effect from
  // re-firing on focus-only re-renders.
  const allLaneSummaries: TrendLaneSummary[] = useMemo(() => [
    trendLaneSummary("history", t("trendActiveSessions"), history, trendSelection.history),
    trendLaneSummary("runtime", t("trendVisiblePids"), runtime, trendSelection.runtime),
    trendLaneSummary("throughput", t("throughputLane"), throughputView, trendSelection.throughput, throughputSeries),
  ], [t, history, runtime, throughputView, throughputSeries, trendSelection.history, trendSelection.runtime, trendSelection.throughput]);
  const laneSummaries = allLaneSummaries.filter((summary) => Boolean(summary.trendWindow || summary.points.length));
  const historySummary = laneSummaries.find((summary) => summary.lane === "history");
  const runtimeSummary = laneSummaries.find((summary) => summary.lane === "runtime");
  // The compact throughput lane keeps its summary even with no trend window yet:
  // the live rate beside the chart is a real measurement available immediately,
  // and the lane renders its own empty state for the chart alone. Dropping the
  // whole lane would hide a number we actually have during warm-up.
  const throughputSummary = (compact && currentLane === "throughput" ? allLaneSummaries : laneSummaries).find((summary) => summary.lane === "throughput");
  const activeSummary = laneSummaries.find((summary) => summary.lane === focusedLane && summary.selected) ?? laneSummaries.find((summary) => summary.selected);
  // In compact mode only one lane is on screen, so the empty state has to follow
  // that lane. Gating it on "any lane has data" rendered an empty container —
  // a blank panel with no explanation — whenever the open tab was the one still
  // warming up.
  const laneHasData = compact
    ? Boolean(currentLane === "throughput" ? throughputSummary : historySummary || runtimeSummary)
    : laneSummaries.length > 0;
  return (
    <section className={`trend-suite ${compact ? "compact" : "dashboard"}`}>
      {compact ? null : (
        <div className="trend-suite-head">
          <div className="trend-suite-copy">
            <h2 tabIndex={0} title={`${t("trendSuite")} · ${formatTrendWindow(t, throughput ?? history ?? runtime)}`}>
              {t("trend")}
            </h2>
          </div>
          <TrendRangeRail label={t("trend")} range={effectiveRange} setRange={setRange} activeRanges={activeRanges} focusKey={focusKey} />
        </div>
      )}
      {laneHasData ? (
        <>
          {compact ? (
            <div className="trend-compact-view">
              {currentLane === "throughput" && throughputSummary ? (
                <ThroughputLaneView
                  t={t}
                  summary={throughputSummary}
                  seriesOptions={throughput?.throughput_series ?? []}
                  selectedSeriesKey={throughputSeries?.key ?? "minute:300"}
                  isFocused={true}
                  setFocusedLane={setFocusedLane}
                  setSeriesKey={setThroughputSeriesKey}
                  setTrendSelection={setTrendSelection}
                  liveReadout={liveReadout}
                  compact={compact}
                  projectWorktrees={projectWorktrees}
                  range={effectiveRange}
                  setRange={setRange}
                  activeRanges={activeRanges}
                />
              ) : null}
              {currentLane === "activity" && (historySummary || runtimeSummary) ? (
                <>
                  <ActivityProcessLane
                    t={t}
                    history={historySummary}
                    runtime={runtimeSummary}
                    focusedLane={focusedLane}
                    compact={compact}
                    live={snapshot.current}
                    peakToday={snapshot.historic_peaks?.today?.active_burst_concurrency}
                    setTrendSelection={setTrendSelection}
                  />
                  <ProjectHeatmap t={t} window={projectHeatmap} projectActivity={projectActivity} compact={compact} />
                </>
              ) : null}
            </div>
          ) : (
            <>
              <div className="trend-duo">
                {throughputSummary ? (
                  <ThroughputLaneView
                    t={t}
                    summary={throughputSummary}
                    seriesOptions={throughput?.throughput_series ?? []}
                    selectedSeriesKey={throughputSeries?.key ?? "minute:300"}
                    isFocused={activeSummary?.lane === "throughput"}
                    setFocusedLane={setFocusedLane}
                    setSeriesKey={setThroughputSeriesKey}
                    setTrendSelection={setTrendSelection}
                    liveReadout={liveReadout}
                    compact={compact}
                    projectWorktrees={projectWorktrees}
                  />
                ) : null}
                {historySummary || runtimeSummary ? (
                  <ActivityProcessLane
                    t={t}
                    history={historySummary}
                    runtime={runtimeSummary}
                    focusedLane={focusedLane}
                    compact={compact}
                    live={snapshot.current}
                    peakToday={snapshot.historic_peaks?.today?.active_burst_concurrency}
                    setTrendSelection={setTrendSelection}
                  />
                ) : null}
              </div>
              <ProjectHeatmap t={t} window={projectHeatmap} projectActivity={projectActivity} compact={compact} />
              <TrendSelectionInspector t={t} summary={activeSummary} compact={compact} />
            </>
          )}
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
  live,
  peakToday,
  setTrendSelection,
}: {
  t: Translate;
  history?: TrendLaneSummary;
  runtime?: TrendLaneSummary;
  focusedLane: TrendLane;
  compact: boolean;
  live?: TrendCurrentMetrics;
  peakToday?: TrendPeakPoint;
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
  const selectChartPoints = useCallback(({ historyAt, runtimeAt }: { historyAt?: string; runtimeAt?: string }) => {
    setTrendSelection((current) => ({
      ...current,
      history: historyAt || current.history,
      runtime: runtimeAt || current.runtime,
    }));
  }, [setTrendSelection]);
  return (
    <article className={`trend-lane activity-process ${focusedLane !== "throughput" ? "is-focused" : ""}`}>
      <ActivityLiveHero t={t} live={live} peakToday={peakToday} runtime={runtime} compact={compact} meta={compact ? undefined : `${range} · ${sampleMeta}`} />
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

// The hero answers "how loaded am I right now" from snapshot.current, which is
// measured at the observation instant. That is a different evidence family from
// the chart below it -- the chart sweeps spans onto a grid and honestly stops
// one bucket short of now -- so the two are labelled separately and never
// reconciled into one number.
function ActivityLiveHero({
  t,
  live,
  peakToday,
  runtime,
  compact,
  meta,
}: {
  t: Translate;
  live?: TrendCurrentMetrics;
  peakToday?: TrendPeakPoint;
  runtime?: TrendLaneSummary;
  compact: boolean;
  meta?: string;
}) {
  const activeNow = typeof live?.active_burst_concurrency === "number" && Number.isFinite(live.active_burst_concurrency)
    ? live.active_burst_concurrency
    : undefined;
  const knownNow = typeof live?.session_concurrency === "number" && Number.isFinite(live.session_concurrency)
    ? live.session_concurrency
    : undefined;
  const pidsNow = typeof live?.pid_concurrency === "number" && Number.isFinite(live.pid_concurrency)
    ? live.pid_concurrency
    : undefined;
  const peak = typeof peakToday?.value === "number" && Number.isFinite(peakToday.value) ? peakToday.value : undefined;
  // The newest runtime sample already carries a per-tool split. Showing it here
  // keeps it visible instead of hiding it behind a legend click.
  const latestRuntime = runtime?.points[runtime.points.length - 1];
  const tools = [...(latestRuntime?.runtime_process_summary ?? [])]
    .filter((item) => (item.pid_count ?? 0) > 0)
    .sort((a, b) => (b.pid_count ?? 0) - (a.pid_count ?? 0))
    .slice(0, compact ? 4 : 6);
  return (
    <header className="activity-hero">
      <div className="activity-hero-head">
        <div className="activity-hero-figure">
          <strong>{trendMetricValue(t, activeNow)}</strong>
          <span>{t("trendActiveSessions")}</span>
        </div>
        <dl className="activity-hero-rail" aria-label={t("selectedValues")}>
          <div>
            <dt>{t("metricKnownSessions")}</dt>
            <dd>{trendMetricValue(t, knownNow)}</dd>
          </div>
          <div>
            <dt>{t("trendVisiblePids")}</dt>
            <dd>{trendMetricValue(t, pidsNow)}</dd>
          </div>
          <div>
            <dt>{t("activityPeakToday")}</dt>
            <dd title={peakToday?.at ? formatDateTime(peakToday.at) : undefined}>{trendMetricValue(t, peak)}</dd>
          </div>
        </dl>
      </div>
      {tools.length ? (
        <div className="activity-hero-tools" aria-label={t("codingAgents")}>
          {tools.map((item, index) => (
            <span className={`tool-${index % 4}`} key={item.key || item.tool || `tool-${index}`}>
              <b>{toolDisplayName(item.display_name || item.tool || item.key)}</b>
              <strong>{trendMetricValue(t, item.pid_count)}</strong>
            </span>
          ))}
        </div>
      ) : null}
      {meta ? <p className="activity-hero-meta">{meta}</p> : null}
    </header>
  );
}

function ThroughputLaneView({
  t,
  summary,
  seriesOptions,
  selectedSeriesKey,
  isFocused,
  setFocusedLane,
  setSeriesKey,
  setTrendSelection,
  liveReadout,
  compact = false,
  projectWorktrees,
  range,
  setRange,
  activeRanges = [],
}: {
  t: Translate;
  summary: TrendLaneSummary;
  seriesOptions: ThroughputTrendSeries[];
  selectedSeriesKey: string;
  isFocused: boolean;
  setFocusedLane: (lane: TrendLane) => void;
  setSeriesKey: (key: string) => void;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
  liveReadout?: React.ReactNode;
  compact?: boolean;
  projectWorktrees?: Map<string, { worktrees?: string[]; branches?: string[] }>;
  range?: TrendRange;
  setRange?: (value: TrendRange) => void;
  activeRanges?: readonly TrendRange[];
}) {
  const { title, trendWindow, points, selected } = summary;
  const series = summary.throughputSeries;
  const periodSummary = series?.summary;
  const windowLabel = formatThroughputWindowLabel(series?.window_seconds);
  const legacySeries = series?.kind === "legacy_rolling_rate";
  const periodMetrics = [
    { label: "MAX", value: formatThroughputPeriodRate(periodSummary?.max) },
    { label: "P95", value: formatThroughputPeriodRate(periodSummary?.p95) },
    { label: "AVG", value: formatThroughputPeriodRate(periodSummary?.avg) },
    ...(!legacySeries && !compact ? [{ label: `CUR(${windowLabel})`, value: formatThroughputPeriodRate(periodSummary?.current) }] : []),
  ];
  const selectPoint = useCallback((at?: string) => {
    setFocusedLane("throughput");
    if (at) setTrendSelection((current) => ({ ...current, throughput: at }));
  }, [setFocusedLane, setTrendSelection]);

  const windowSwitch = (
    <div className="throughput-window-switch" role="group" aria-label={t("throughputRateWindow")}>
      {seriesOptions.map((option) => {
        const key = String(option.key || "");
        const legacy = option.kind === "legacy_rolling_rate";
        const label = formatThroughputWindowLabel(option.window_seconds);
        return (
          <button
            key={key}
            type="button"
            className={legacy ? "legacy" : ""}
            aria-pressed={key === selectedSeriesKey}
            title={legacy ? `${t("throughputLegacyShort")} · ${label}` : `${t("throughputRateWindow")} · ${label}`}
            onClick={() => setSeriesKey(key)}
          >
            {legacy ? `${t("throughputLegacyShort")} ${label}` : label}
          </button>
        );
      })}
    </div>
  );

  const periodSummaryDl = (
    <dl className={`throughput-period-summary ${legacySeries ? "legacy" : ""}`} aria-label={t("selectedValues")}>
      {periodMetrics.map((metric) => (
        <div key={metric.label} title={`${metric.label}: ${metric.value} ${t("tokenRateUnit")}`}>
          <dt>{metric.label}</dt>
          <dd>{metric.value}</dd>
        </div>
      ))}
    </dl>
  );

  return (
    <article className={`trend-lane throughput ${isFocused ? "is-focused" : ""}`} onPointerEnter={() => setFocusedLane("throughput")}>
      {compact ? (
        <>
          <div className="throughput-kpi-bar">
            <div className="throughput-title-group">
              <h2 className="throughput-heading">{title}</h2>
              {periodSummaryDl}
            </div>
            <div className="throughput-current-readout">
              <span className="throughput-basis-label">{t("throughputLiveBasis")}</span>
              {liveReadout}
            </div>
          </div>
          <div className="throughput-chart-control-row">
            <div className="chart-control-cluster window-cluster">
              <span className="control-label">{t("throughputHistoryWindow")}</span>
              {windowSwitch}
            </div>
          </div>
        </>
      ) : (
        <div className="trend-lane-head">
          <div className="trend-lane-title">
            <span className="trend-kicker" tabIndex={0} title={`${trendWindow?.range || t("unavailable")} · ${periodSummary?.sample_count ?? 0} ${t("samples")} · ${windowLabel} · ${t("tokenRateUnit")}`}>{title}{legacySeries ? ` · ${t("throughputLegacyShort")}` : ""}</span>
          </div>
          <div className="throughput-controls-left">
            {windowSwitch}
            {periodSummaryDl}
          </div>
          <div className="throughput-current-readout">{liveReadout}</div>
        </div>
      )}
      {summary.data.length ? (
        <ThroughputRiver
          key={`${trendWindow?.range}:${selectedSeriesKey}`}
          t={t}
          title={title}
          points={points}
          from={trendWindow?.from}
          to={trendWindow?.to}
          selectedAt={selected?.at}
          onSelect={selectPoint}
          compact={compact}
          projectWorktrees={projectWorktrees}
        />
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </article>
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

function projectHeatmapWindowForRange(set: ProjectHeatmapSet | undefined, range: TrendRange): ProjectHeatmapWindow | undefined {
  return set?.windows?.find((window) => window.range === range);
}

function trendLaneSummary(lane: TrendLane, title: string, trendWindow: TrendWindow | undefined, selectedAt?: string, throughputSeries?: ThroughputTrendSeries): TrendLaneSummary {
  const sampleKey = lane === "history" ? "transcript_sampled" : lane === "runtime" ? "runtime_sampled" : "throughput_sampled";
  const points = sampledPoints(trendWindow, sampleKey);
  const signalPoints = lane === "throughput" ? points.filter((point) => trendOutputThroughputProjects(point) !== null) : points;
  const data = trendSignalData(signalPoints, lane);
  return {
    lane,
    title,
    trendWindow,
    points,
    data,
    selected: selectedTrendDatum(data, selectedAt),
    throughputSeries,
  };
}

function selectedThroughputSeries(window: TrendWindow | undefined, key: string): ThroughputTrendSeries | undefined {
  const series = window?.throughput_series ?? [];
  return series.find((item) => item.key === key)
    ?? series.find((item) => item.key === "minute:300")
    ?? series.find((item) => item.kind === "minute_rollup");
}

function throughputSeriesWindow(window: TrendWindow | undefined, series: ThroughputTrendSeries | undefined): TrendWindow | undefined {
  if (!window || !series) return undefined;
  return {
    ...window,
    granularity_seconds: series.granularity_seconds,
    source_from: series.source_from,
    source_lookback_hours: undefined,
    history_complete: series.history_complete,
    points: series.points,
  };
}

function selectedTrendDatum(data: TrendSignalDatum[], selectedAt: string | undefined): TrendSignalDatum | undefined {
  const selected = data.find((datum) => datum.at === selectedAt);
  if (selected) return selected;
  // Every lane defaults to the last sampled datum -- the same one the chart
  // draws at its right edge. The history lane used to scan backwards for the
  // last value > 0, which existed only to skip the fabricated zero the backend
  // reported at the observation instant; with that instant now left unsampled,
  // the scan would just hide genuinely idle buckets and make the readout
  // disagree with the chart.
  return data[data.length - 1];
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
    byTime.set(ms, { at, value, point });
  });
  const sorted = Array.from(byTime.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([milliseconds, item]) => ({ ...item, time: milliseconds / 1000 }));
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

function formatThroughputWindowLabel(seconds?: number): string {
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds <= 0) return "n/a";
  if (seconds % 60 === 0) return `${Math.round(seconds / 60)}m`;
  return `${Math.round(seconds)}s`;
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
