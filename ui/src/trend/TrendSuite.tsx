import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Gauge, Info, Layers } from "lucide-react";
import {
  AreaSeries,
  CandlestickSeries,
  ColorType,
  CrosshairMode,
  LineStyle,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type MouseEventParams,
  type Time,
} from "lightweight-charts";
import { clampNumber, formatCopy, formatPct, type Translate } from "../lib/format";
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

export type TrendSnapshot = {
  trends?: TrendSet;
  realtime_trends?: TrendSet;
  project_heatmaps?: ProjectHeatmapSet;
};

type TrendLaneSummary = {
  lane: TrendLane;
  title: string;
  trendWindow?: TrendWindow;
  points: TrendPoint[];
  selected?: TrendPoint;
};

type ProjectHeatmapTile = ProjectHeatmapItem & {
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

type TrendKLineDatum = {
  at: string;
  time: Time;
  value: number;
  point: TrendPoint;
  open: number;
  high: number;
  low: number;
  close: number;
};

type TrendKLineOverlay = {
  left: number;
  top: number;
};

type TrendKLineHover = TrendKLineOverlay & {
  align: "left" | "right";
  vertical: "above" | "below";
  datum: TrendKLineDatum;
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
  const projectHeatmap = projectHeatmapWindowForRange(snapshot.project_heatmaps, effectiveRange);
  const [focusedLane, setFocusedLane] = useState<TrendLane>("history");
  const laneSummaries: TrendLaneSummary[] = [
    trendLaneSummary("history", t("historyLane"), history, trendSelection.history),
    trendLaneSummary("runtime", t("runtimeLane"), runtime, trendSelection.runtime),
  ].filter((summary) => Boolean(summary.trendWindow || summary.points.length));
  const activeSummary = laneSummaries.find((summary) => summary.lane === focusedLane && summary.selected) ?? laneSummaries.find((summary) => summary.selected);
  return (
    <section className={`trend-suite ${compact ? "compact" : "dashboard"}`}>
      <div className="trend-suite-head">
        <div className="trend-suite-copy">
          <h2>{t("trendSuite")}</h2>
          <span>{effectiveRange} · {formatTrendWindow(t, history ?? runtime)}</span>
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
                setFocusedLane={setFocusedLane}
                setTrendSelection={setTrendSelection}
              />
            ))}
          </div>
          <ProjectHeatmap t={t} window={projectHeatmap} compact={compact} />
          <TrendSelectionInspector t={t} summary={activeSummary} compact={compact} />
        </>
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </section>
  );
}

function ProjectHeatmap({ t, window, compact }: { t: Translate; window?: ProjectHeatmapWindow; compact: boolean }) {
  const [hover, setHover] = useState<ProjectHeatmapHover | null>(null);
  const rawItems = (window?.items ?? []).filter((item) => (item.session_window_count ?? 0) > 0 || (item.window_count ?? 0) > 0);
  const visibleLimit = compact ? 10 : 16;
  const items = rawItems.slice(0, visibleLimit);
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
            });
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
  const tooltipWidth = Math.min(176, Math.max(112, width - 20));
  const tooltipHeight = 58;
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
  setFocusedLane,
  setTrendSelection,
}: {
  t: Translate;
  summary: TrendLaneSummary;
  isFocused: boolean;
  setFocusedLane: (lane: TrendLane) => void;
  setTrendSelection: React.Dispatch<React.SetStateAction<Record<TrendLane, string | undefined>>>;
}) {
  const { lane, title, trendWindow, points, selected } = summary;
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
          <small>{trendWindow?.range || t("unavailable")} · {points.length} {t("samples")}</small>
        </div>
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
      {points.length ? (
        <TrendKLineChart
          t={t}
          lane={lane}
          title={title}
          points={points}
          selectedAt={selected?.at}
          onSelect={selectPoint}
        />
      ) : (
        <section className="empty-inline"><Gauge size={18} /><span>{t("noTrend")}</span></section>
      )}
    </article>
  );
}

function TrendKLineChart({
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
  const candleRef = useRef<ISeriesApi<"Candlestick"> | null>(null);
  const areaRef = useRef<ISeriesApi<"Area"> | null>(null);
  const data = useMemo(() => trendKLineData(points, lane), [points, lane]);
  const selected = selectedAt ? data.find((item) => item.at === selectedAt) ?? data[data.length - 1] : data[data.length - 1];
  const [overlay, setOverlay] = useState<TrendKLineOverlay | null>(null);
  const [hover, setHover] = useState<TrendKLineHover | null>(null);
  const [sizeKey, setSizeKey] = useState(0);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const styles = getComputedStyle(document.documentElement);
    const bg = cssVar(styles, "--bg");
    const fg = cssVar(styles, "--fg");
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
        barSpacing: data.length > 72 ? 4.2 : data.length > 36 ? 6.6 : 9.2,
        minBarSpacing: 2.8,
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: accentHi, labelVisible: false, style: LineStyle.Dashed, width: 1 },
        horzLine: { color: accent, labelVisible: false, style: LineStyle.Dashed, width: 1 },
      },
      localization: {
        timeFormatter: (time: Time) => formatKLineTime(time),
      },
      handleScroll: false,
      handleScale: false,
    });
    const area = chart.addSeries(AreaSeries, {
      lineVisible: false,
      topColor: lane === "history" ? colorMix(ok, 0.18) : colorMix(run, 0.18),
      bottomColor: colorMix(bg, 0.0),
      priceLineVisible: false,
      lastValueVisible: false,
      crosshairMarkerVisible: false,
    });
    const candles = chart.addSeries(CandlestickSeries, {
      upColor: lane === "history" ? colorMix(ok, 0.88) : colorMix(run, 0.88),
      downColor: lane === "history" ? colorMix(accent, 0.58) : colorMix(accentHi, 0.58),
      borderUpColor: lane === "history" ? colorMix(ok, 0.72) : colorMix(run, 0.72),
      borderDownColor: lane === "history" ? colorMix(accent, 0.48) : colorMix(accentHi, 0.48),
      wickUpColor: lane === "history" ? colorMix(ok, 0.68) : colorMix(run, 0.68),
      wickDownColor: lane === "history" ? colorMix(accent, 0.48) : colorMix(accentHi, 0.48),
      borderVisible: true,
      wickVisible: true,
      priceLineVisible: false,
      lastValueVisible: false,
    });
    const handleClick = (param: { time?: Time }) => {
      if (!param.time || !data.length) return;
      const clicked = nearestKLineDatum(data, param.time);
      onSelect(clicked?.at);
    };
    const handleCrosshairMove = (param: MouseEventParams<Time>) => {
      const point = param.point;
      if (!param.time || !point || point.x < 0 || point.y < 0 || !data.length) {
        setHover(null);
        return;
      }
      const hovered = nearestKLineDatum(data, param.time);
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
    candleRef.current = candles;
    areaRef.current = area;
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
      candleRef.current = null;
      areaRef.current = null;
    };
  }, [data, lane, onSelect]);

  useEffect(() => {
    const chart = chartRef.current;
    const candleSeries = candleRef.current;
    const areaSeries = areaRef.current;
    if (!chart || !candleSeries || !areaSeries) return;
    candleSeries.setData(data.map(({ time, open, high, low, close }) => ({ time, open, high, low, close })));
    areaSeries.setData(data.map(({ time, close }) => ({ time, value: close })));
    chart.timeScale().fitContent();
    setHover(null);
  }, [data]);

  useEffect(() => {
    const chart = chartRef.current;
    const candleSeries = candleRef.current;
    if (!chart || !candleSeries || !selected) {
      setOverlay(null);
      return;
    }
    const update = () => {
      const left = chart.timeScale().timeToCoordinate(selected.time);
      const top = candleSeries.priceToCoordinate(selected.close);
      setOverlay(left === null || top === null ? null : { left, top });
    };
    update();
    const frame = window.requestAnimationFrame(update);
    return () => window.cancelAnimationFrame(frame);
  }, [selected, sizeKey]);

  const hoverMetrics = hover ? trendSelectedReadoutParts(t, lane, hover.datum.point) : [];
  const hoverDelta = hover ? hover.datum.close - hover.datum.open : 0;

  return (
    <div className={`trend-chart trend-kline-chart lane-${lane}`} onMouseLeave={() => setHover(null)}>
      <div className="trend-kline-host" ref={hostRef} role="img" aria-label={`${title} ${t("trend")}`} />
      {overlay ? (
        <div className="trend-kline-selection" style={{ "--kx": `${overlay.left}px`, "--ky": `${overlay.top}px` } as React.CSSProperties} aria-hidden="true">
          <span className="trend-kline-selection-dot" />
        </div>
      ) : null}
      {hover ? (
        <div
          className={`trend-kline-tooltip align-${hover.align} is-${hover.vertical}`}
          style={{ "--tip-x": `${hover.left}px`, "--tip-y": `${hover.top}px` } as React.CSSProperties}
          aria-hidden="true"
        >
          <span className="trend-kline-tooltip-kicker">{t("trendExactBucket")}</span>
          <strong className="trend-kline-tooltip-time">{formatChartAxisLabel(hover.datum.at)}</strong>
          <div className="trend-kline-tooltip-grid">
            {hoverMetrics.map((metric) => (
              <span key={metric.label}>
                <b>{metric.label}</b>
                <em>{metric.value}</em>
              </span>
            ))}
            <span>
              <b>Δ</b>
              <em>{hoverDelta > 0 ? `+${hoverDelta}` : String(hoverDelta)}</em>
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
  return TREND_RANGES.filter((range) => Boolean(trendWindowForRange(snapshot.trends, range) || trendWindowForRange(snapshot.realtime_trends, range)));
}

function trendWindowForRange(set: TrendSet | undefined, range: TrendRange): TrendWindow | undefined {
  return set?.windows?.find((window) => window.range === range);
}

function projectHeatmapWindowForRange(set: ProjectHeatmapSet | undefined, range: TrendRange): ProjectHeatmapWindow | undefined {
  return set?.windows?.find((window) => window.range === range);
}

function trendLaneSummary(lane: TrendLane, title: string, trendWindow: TrendWindow | undefined, selectedAt?: string): TrendLaneSummary {
  const sampleKey = lane === "history" ? "transcript_sampled" : "runtime_sampled";
  const points = sampledPoints(trendWindow, sampleKey);
  return {
    lane,
    title,
    trendWindow,
    points,
    selected: selectedTrendPoint(points, selectedAt),
  };
}

function selectedTrendPoint(points: TrendPoint[], selectedAt?: string): TrendPoint | undefined {
  return points.find((point) => point.at === selectedAt) ?? points[points.length - 1];
}

function sampledPoints(window: TrendWindow | undefined, sampledKey: "transcript_sampled" | "runtime_sampled"): TrendPoint[] {
  return [...(window?.points ?? [])].filter((point) => point[sampledKey] || point.at).sort((a, b) => String(a.at).localeCompare(String(b.at)));
}

function trendKLineData(points: TrendPoint[], lane: TrendLane): TrendKLineDatum[] {
  const primaryKey: keyof TrendPoint = lane === "history" ? "active_burst_concurrency" : "pid_concurrency";
  const byTime = new Map<number, { at: string; value: number; point: TrendPoint }>();
  points.forEach((point) => {
    const at = point.at;
    const value = trendNumericValue(point, primaryKey);
    const ms = pointTimeMs(at);
    if (!at || value === null || ms === null) return;
    byTime.set(Math.floor(ms / 1000), { at, value, point });
  });
  const sorted = Array.from(byTime.entries())
    .sort((a, b) => a[0] - b[0])
    .map(([seconds, item]) => ({ ...item, time: seconds as Time }));
  return sorted.map((item, index) => {
    const previous = sorted[Math.max(0, index - 1)];
    const open = previous?.value ?? item.value;
    const close = item.value;
    return {
      at: item.at,
      time: item.time,
      value: close,
      point: item.point,
      open,
      close,
      high: Math.max(open, close),
      low: Math.min(open, close),
    };
  });
}

function nearestKLineDatum(data: TrendKLineDatum[], time: Time): TrendKLineDatum | undefined {
  const target = typeof time === "number" ? time : Date.parse(String(time)) / 1000;
  if (!Number.isFinite(target)) return data[data.length - 1];
  return data.reduce((best, item) => {
    const current = typeof item.time === "number" ? item.time : Date.parse(String(item.time)) / 1000;
    const bestTime = typeof best.time === "number" ? best.time : Date.parse(String(best.time)) / 1000;
    return Math.abs(current - target) < Math.abs(bestTime - target) ? item : best;
  }, data[0]);
}

function formatKLineTime(time: Time): string {
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

function trendDetailMetrics(t: Translate, lane: TrendLane, point: TrendPoint): Array<{ label: string; value: string }> {
  if (lane === "history") {
    return [
      { label: t("metricFresh"), value: trendMetricValue(t, point.active_burst_concurrency) },
      { label: t("metricSessions"), value: trendMetricValue(t, point.session_concurrency) },
    ];
  }
  return [
    { label: t("metricProcesses"), value: trendMetricValue(t, point.pid_concurrency) },
    { label: t("metricMatched"), value: typeof point.mapping_coverage_pct === "number" ? formatPct(point.mapping_coverage_pct) : t("unavailable") },
    { label: t("mappedProcesses"), value: trendMetricValue(t, point.mapped_processes) },
    { label: t("unmappedProcesses"), value: trendMetricValue(t, point.unmapped_processes) },
  ];
}

function trendMetricValue(t: Translate, value?: number): string {
  return typeof value === "number" && Number.isFinite(value) ? String(value) : t("unavailable");
}

function trendSelectedReadout(t: Translate, lane: TrendLane, point: TrendPoint): string {
  if (lane === "history") {
    return `${trendMetricValue(t, point.active_burst_concurrency)} / ${trendMetricValue(t, point.session_concurrency)}`;
  }
  return `${trendMetricValue(t, point.pid_concurrency)} / ${typeof point.mapping_coverage_pct === "number" ? formatPct(point.mapping_coverage_pct) : t("unavailable")}`;
}

function trendSelectedReadoutParts(t: Translate, lane: TrendLane, point: TrendPoint): Array<{ label: string; value: string; role: "primary" | "context" }> {
  if (lane === "history") {
    return [
      { label: t("trendReadoutFresh"), value: trendMetricValue(t, point.active_burst_concurrency), role: "primary" },
      { label: t("trendReadoutSessions"), value: trendMetricValue(t, point.session_concurrency), role: "context" },
    ];
  }
  return [
    { label: t("trendReadoutProcesses"), value: trendMetricValue(t, point.pid_concurrency), role: "primary" },
    { label: t("trendReadoutMatched"), value: typeof point.mapping_coverage_pct === "number" ? formatPct(point.mapping_coverage_pct) : t("unavailable"), role: "context" },
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

function trendExplanationSections(t: Translate, lane: TrendLane, point: TrendPoint): Array<{ label: string; text: string }> {
  const clicked = `${t("sampledBucket")} ${point.at ? formatDateTime(point.at) : t("unavailable")} · ${trendSelectedReadout(t, lane, point)}`;
  return [
    { label: t("trendWhatClicked"), text: clicked },
    { label: t("trendWhatMeans"), text: lane === "history" ? t("trendHistoryMeaning") : t("trendRuntimeMeaning") },
    { label: t("whyTrust"), text: lane === "history" ? t("trendHistoryTrust") : t("trendRuntimeTrust") },
    { label: t("trendHowUse"), text: lane === "history" ? t("trendHistoryUse") : t("trendRuntimeUse") },
  ];
}

function formatTrendWindow(t: Translate, window: TrendWindow | ProjectHeatmapWindow | undefined): string {
  const firstAt = window?.from;
  const lastAt = window?.to;
  if (!firstAt && !lastAt) return t("unavailable");
  const first = firstAt ? formatChartAxisLabel(firstAt) : "";
  const last = lastAt ? formatChartAxisLabel(lastAt) : "";
  return first && last ? `${first} -> ${last}` : window?.range || t("unavailable");
}

function trendNumericValue(point: TrendPoint, key: keyof TrendPoint): number | null {
  const value = point[key];
  return typeof value === "number" && Number.isFinite(value) ? value : null;
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
