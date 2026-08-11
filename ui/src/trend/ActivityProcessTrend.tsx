import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  AreaSeries,
  ColorType,
  CrosshairMode,
  LineSeries,
  LineStyle,
  LineType,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type LineData,
  type MouseEventParams,
  type Time,
  type WhitespaceData,
} from "lightweight-charts";
import { clampNumber, type Translate } from "../lib/format";
import type { TrendPoint } from "./types";

type CountDatum = {
  at: string;
  time: Time;
  value: number;
};

type CountSeries = {
  active: CountDatum[];
  sessions: CountDatum[];
  processes: CountDatum[];
};

type ActivityProcessHover = {
  align: "left" | "right";
  cursorAt: string;
  left: number;
  top: number;
  values: ActivityProcessValue[];
  vertical: "above" | "below";
};

type ActivityProcessValue = {
  at: string;
  key: keyof CountSeries;
  label: string;
  value: number;
};

type SelectionBead = {
  key: keyof CountSeries;
  leftPct: number;
  topPct: number;
};

type SeriesRefs = {
  active: ISeriesApi<"Area">;
  sessions: ISeriesApi<"Line">;
  processes: ISeriesApi<"Line">;
};

export function ActivityProcessTrend({
  t,
  title,
  historyPoints,
  runtimePoints,
  from,
  to,
  granularitySeconds,
  selectedHistoryAt,
  selectedRuntimeAt,
  onSelect,
}: {
  t: Translate;
  title: string;
  historyPoints: TrendPoint[];
  runtimePoints: TrendPoint[];
  from?: string;
  to?: string;
  granularitySeconds?: number;
  selectedHistoryAt?: string;
  selectedRuntimeAt?: string;
  onSelect: (selection: { historyAt?: string; runtimeAt?: string }) => void;
}) {
  const hostRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<SeriesRefs | null>(null);
  const onSelectRef = useRef(onSelect);
  const tRef = useRef(t);
  const series = useMemo(() => countSeries(historyPoints, runtimePoints), [historyPoints, runtimePoints]);
  const seriesDataRef = useRef(series);
  const domainRef = useRef({ from, to, granularitySeconds });
  const [hover, setHover] = useState<ActivityProcessHover | null>(null);
  const [selectionBeads, setSelectionBeads] = useState<SelectionBead[]>([]);
  const [layoutVersion, setLayoutVersion] = useState(0);
  const theme = useResolvedTheme();

  useEffect(() => {
    onSelectRef.current = onSelect;
  }, [onSelect]);

  useEffect(() => {
    tRef.current = t;
  }, [t]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) return;
    const styles = getComputedStyle(document.documentElement);
    const bg = cssVar(styles, "--bg");
    const faint = cssVar(styles, "--fg-faint");
    const grid = cssVar(styles, "--console-line-soft");
    const activeColor = cssVar(styles, "--ok");
    const sessionColor = cssVar(styles, "--accent-hi");
    const processColor = cssVar(styles, "--run");
    const rect = host.getBoundingClientRect();
    const chart = createChart(host, {
      autoSize: true,
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: faint,
        fontFamily: styles.getPropertyValue("--font-mono").trim() || undefined,
        fontSize: 10,
        attributionLogo: false,
      },
      grid: {
        vertLines: { color: "transparent", visible: false },
        horzLines: { color: colorWithAlpha(grid, 0.34), style: LineStyle.SparseDotted },
      },
      rightPriceScale: {
        visible: false,
        borderVisible: false,
        scaleMargins: { top: 0.12, bottom: 0.12 },
      },
      leftPriceScale: { visible: false, borderVisible: false },
      timeScale: {
        borderVisible: false,
        timeVisible: true,
        secondsVisible: false,
        fixLeftEdge: true,
        fixRightEdge: true,
        minBarSpacing: 2.8,
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: { color: sessionColor, labelVisible: false, style: LineStyle.Dashed, width: 1 },
        horzLine: { color: colorWithAlpha(faint, 0.52), labelVisible: false, style: LineStyle.Dashed, width: 1 },
      },
      localization: {
        timeFormatter: (time: Time) => formatChartTime(time),
      },
      handleScroll: false,
      handleScale: false,
    });
    const active = chart.addSeries(AreaSeries, {
      lineColor: colorWithAlpha(activeColor, 0.96),
      lineType: LineType.Curved,
      lineWidth: 2,
      topColor: colorWithAlpha(activeColor, 0.22),
      bottomColor: colorWithAlpha(bg, 0),
      priceLineVisible: false,
      lastValueVisible: false,
      crosshairMarkerVisible: true,
      crosshairMarkerRadius: 3,
    });
    const sessions = chart.addSeries(LineSeries, {
      color: colorWithAlpha(sessionColor, 0.92),
      lineStyle: LineStyle.Dashed,
      lineType: LineType.Curved,
      lineWidth: 2,
      priceLineVisible: false,
      lastValueVisible: false,
      crosshairMarkerVisible: true,
      crosshairMarkerRadius: 3,
    });
    const processes = chart.addSeries(LineSeries, {
      color: colorWithAlpha(processColor, 0.9),
      lineStyle: LineStyle.Solid,
      lineType: LineType.Curved,
      lineWidth: 2,
      priceLineVisible: false,
      lastValueVisible: false,
      crosshairMarkerVisible: true,
      crosshairMarkerRadius: 3,
    });
    const refs = { active, sessions, processes };

    const handleClick = (param: MouseEventParams<Time>) => {
      if (!param.time) return;
      const threshold = selectionThresholdSeconds(domainRef.current.granularitySeconds);
      const history = nearestDatum(seriesDataRef.current.active, param.time, threshold);
      const runtime = nearestDatum(seriesDataRef.current.processes, param.time, threshold);
      if (!history && !runtime) return;
      onSelectRef.current({ historyAt: history?.at, runtimeAt: runtime?.at });
    };
    const handleCrosshairMove = (param: MouseEventParams<Time>) => {
      const point = param.point;
      if (!param.time || !point || point.x < 0 || point.y < 0) {
        setHover(null);
        return;
      }
      const threshold = selectionThresholdSeconds(domainRef.current.granularitySeconds);
      const values = hoverValues(tRef.current, seriesDataRef.current, param.time, threshold);
      if (!values.length) {
        setHover(null);
        return;
      }
      const width = host.clientWidth || rect.width;
      const height = host.clientHeight || rect.height;
      setHover({
        align: point.x > width - 220 ? "right" : "left",
        cursorAt: formatChartTime(param.time),
        left: clampNumber(point.x, 8, Math.max(8, width - 8)),
        top: clampNumber(point.y, 10, Math.max(10, height - 10)),
        values,
        vertical: point.y < 78 ? "below" : "above",
      });
    };
    chart.subscribeClick(handleClick);
    chart.subscribeCrosshairMove(handleCrosshairMove);
    chartRef.current = chart;
    seriesRef.current = refs;
    applyCountSeries(chart, refs, seriesDataRef.current, domainRef.current);
    setLayoutVersion((value) => value + 1);
    return () => {
      chart.unsubscribeClick(handleClick);
      chart.unsubscribeCrosshairMove(handleCrosshairMove);
      chart.remove();
      chartRef.current = null;
      seriesRef.current = null;
    };
  }, [theme]);

  useEffect(() => {
    seriesDataRef.current = series;
    domainRef.current = { from, to, granularitySeconds };
    const chart = chartRef.current;
    const refs = seriesRef.current;
    if (!chart || !refs) return;
    applyCountSeries(chart, refs, series, domainRef.current);
    setHover(null);
    const frame = window.requestAnimationFrame(() => setLayoutVersion((value) => value + 1));
    return () => window.cancelAnimationFrame(frame);
  }, [series, from, to, granularitySeconds]);

  useEffect(() => {
    const chart = chartRef.current;
    const refs = seriesRef.current;
    if (!chart || !refs) {
      setSelectionBeads([]);
      return;
    }
    const history = series.active.find((datum) => datum.at === selectedHistoryAt);
    const known = series.sessions.find((datum) => datum.at === selectedHistoryAt);
    const runtime = series.processes.find((datum) => datum.at === selectedRuntimeAt);
    const width = hostRef.current?.clientWidth ?? 0;
    const height = hostRef.current?.clientHeight ?? 0;
    if (width <= 0 || height <= 0) {
      setSelectionBeads([]);
      return;
    }
    const candidates: Array<{ key: keyof CountSeries; datum?: CountDatum; series: ISeriesApi<"Area"> | ISeriesApi<"Line"> }> = [
      { key: "active", datum: history, series: refs.active },
      { key: "sessions", datum: known, series: refs.sessions },
      { key: "processes", datum: runtime, series: refs.processes },
    ];
    const beads = candidates.flatMap((candidate) => {
      if (!candidate.datum) return [];
      const left = chart.timeScale().timeToCoordinate(candidate.datum.time);
      const top = candidate.series.priceToCoordinate(candidate.datum.value);
      return left === null || top === null ? [] : [{
        key: candidate.key,
        leftPct: (left / width) * 100,
        topPct: (top / height) * 100,
      }];
    });
    setSelectionBeads(beads);
  }, [series, selectedHistoryAt, selectedRuntimeAt, layoutVersion]);

  return (
    <div className="trend-chart activity-process-chart" onMouseLeave={() => setHover(null)}>
      <div className="activity-process-host" ref={hostRef} role="img" aria-label={`${title} ${t("trend")}`} />
      <div className="activity-process-selection" aria-hidden="true">
        {selectionBeads.map((bead) => (
          <span
            className={`activity-process-bead ${bead.key}`}
            key={bead.key}
            style={{ "--bead-x": `${bead.leftPct}%`, "--bead-y": `${bead.topPct}%` } as React.CSSProperties}
          />
        ))}
      </div>
      {hover ? (
        <div
          aria-hidden="true"
          className={`activity-process-tooltip align-${hover.align} is-${hover.vertical}`}
          style={{ "--tip-x": `${hover.left}px`, "--tip-y": `${hover.top}px` } as React.CSSProperties}
        >
          <strong>{hover.cursorAt}</strong>
          <div>
            {hover.values.map((item) => (
              <span className={item.key} key={item.key}>
                <i />
                <b>{item.label}</b>
                <em>{item.value}</em>
                <small>{formatSampleTime(item.at)}</small>
              </span>
            ))}
          </div>
        </div>
      ) : null}
    </div>
  );
}

function countSeries(historyPoints: TrendPoint[], runtimePoints: TrendPoint[]): CountSeries {
  return {
    active: countData(historyPoints, (point) => point.active_burst_concurrency),
    sessions: countData(historyPoints, (point) => point.session_concurrency),
    processes: countData(runtimePoints, (point) => point.pid_concurrency),
  };
}

function countData(points: TrendPoint[], valueOf: (point: TrendPoint) => number | undefined): CountDatum[] {
  const byTime = new Map<number, CountDatum>();
  points.forEach((point) => {
    if (!point.at) return;
    const milliseconds = Date.parse(point.at);
    const raw = valueOf(point);
    if (!Number.isFinite(milliseconds) || typeof raw !== "number" || !Number.isFinite(raw)) return;
    const seconds = Math.floor(milliseconds / 1000);
    byTime.set(seconds, { at: point.at, time: seconds as Time, value: Math.max(0, raw) });
  });
  return [...byTime.values()].sort((a, b) => Number(a.time) - Number(b.time));
}

function applyCountSeries(
  chart: IChartApi,
  refs: SeriesRefs,
  series: CountSeries,
  domain: { from?: string; to?: string; granularitySeconds?: number },
) {
  refs.active.setData(chartData(series.active, domain));
  refs.sessions.setData(chartData(series.sessions, domain));
  refs.processes.setData(chartData(series.processes, domain));
  chart.timeScale().fitContent();
}

function chartData(
  data: CountDatum[],
  domain: { from?: string; to?: string; granularitySeconds?: number },
): Array<LineData<Time> | WhitespaceData<Time>> {
  const points = new Map<number, LineData<Time> | WhitespaceData<Time>>();
  const step = domain.granularitySeconds && domain.granularitySeconds > 0 ? domain.granularitySeconds : 0;
  data.forEach((datum, index) => {
    const seconds = Number(datum.time);
    const previous = data[index - 1];
    if (previous && step > 0 && seconds - Number(previous.time) > step * 1.5) {
      points.set(Number(previous.time) + step, { time: (Number(previous.time) + step) as Time });
      points.set(seconds - step, { time: (seconds - step) as Time });
    }
    points.set(seconds, { time: datum.time, value: datum.value });
  });
  [domain.from, domain.to].forEach((value) => {
    const milliseconds = value ? Date.parse(value) : Number.NaN;
    if (!Number.isFinite(milliseconds)) return;
    const seconds = Math.floor(milliseconds / 1000);
    if (!points.has(seconds)) points.set(seconds, { time: seconds as Time });
  });
  return [...points.entries()].sort((a, b) => a[0] - b[0]).map((entry) => entry[1]);
}

function hoverValues(t: Translate, series: CountSeries, time: Time, threshold: number): ActivityProcessValue[] {
  const active = nearestDatum(series.active, time, threshold);
  const sessions = nearestDatum(series.sessions, time, threshold);
  const processes = nearestDatum(series.processes, time, threshold);
  return [
    active ? { at: active.at, key: "active" as const, label: t("trendActiveSessions"), value: active.value } : null,
    sessions ? { at: sessions.at, key: "sessions" as const, label: t("metricKnownSessions"), value: sessions.value } : null,
    processes ? { at: processes.at, key: "processes" as const, label: t("trendVisiblePids"), value: processes.value } : null,
  ].filter((value): value is ActivityProcessValue => value !== null);
}

function nearestDatum(data: CountDatum[], time: Time, threshold: number): CountDatum | undefined {
  if (!data.length) return undefined;
  const target = timeSeconds(time);
  if (!Number.isFinite(target)) return undefined;
  const nearest = data.reduce((best, datum) => (
    Math.abs(Number(datum.time) - target) < Math.abs(Number(best.time) - target) ? datum : best
  ), data[0]);
  return Math.abs(Number(nearest.time) - target) <= threshold ? nearest : undefined;
}

function selectionThresholdSeconds(granularitySeconds?: number): number {
  return Math.max(60, (granularitySeconds ?? 0) * 0.75);
}

function timeSeconds(time: Time): number {
  if (typeof time === "number") return time;
  const parsed = Date.parse(String(time));
  return Number.isFinite(parsed) ? parsed / 1000 : Number.NaN;
}

function formatChartTime(time: Time): string {
  const seconds = timeSeconds(time);
  if (!Number.isFinite(seconds)) return String(time);
  return formatSampleTime(new Date(seconds * 1000).toISOString());
}

function formatSampleTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return `${date.toLocaleDateString(undefined, { month: "numeric", day: "numeric" })} ${date.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", hour12: false })}`;
}

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

function cssVar(styles: CSSStyleDeclaration, name: string): string {
  return styles.getPropertyValue(name).trim() || "#7b8794";
}

function colorWithAlpha(color: string, alpha: number): string {
  const text = color.trim();
  const clamped = clampNumber(alpha, 0, 1);
  if (text.startsWith("#")) {
    const hex = text.slice(1);
    const normalized = hex.length === 3 ? hex.split("").map((char) => char + char).join("") : hex;
    const value = Number.parseInt(normalized, 16);
    if (Number.isFinite(value)) {
      return `rgba(${(value >> 16) & 255}, ${(value >> 8) & 255}, ${value & 255}, ${clamped})`;
    }
  }
  const rgbMatch = text.match(/^rgba?\(([^)]+)\)$/);
  if (rgbMatch) {
    const parts = rgbMatch[1].split(",").map((part) => part.trim()).slice(0, 3);
    if (parts.length === 3) return `rgba(${parts.join(", ")}, ${clamped})`;
  }
  return text;
}
