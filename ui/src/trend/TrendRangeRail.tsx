import { TREND_RANGES, type ProjectHeatmapSet, type TrendRange, type TrendSet, type TrendWindow } from "./types";

export type TrendRangeSnapshot = {
  trends?: TrendSet;
  realtime_trends?: TrendSet;
  throughput_trends?: TrendSet;
  project_heatmaps?: ProjectHeatmapSet;
};

export function trendWindowForRange(set: TrendSet | undefined, range: TrendRange): TrendWindow | undefined {
  return set?.windows?.find((window) => window.range === range);
}

export function activeTrendRanges(snapshot: TrendRangeSnapshot): TrendRange[] {
  return TREND_RANGES.filter((range) => Boolean(trendWindowForRange(snapshot.trends, range) || trendWindowForRange(snapshot.realtime_trends, range) || trendWindowForRange(snapshot.throughput_trends, range)));
}

// The history span belongs to whichever trend tab is open, not to one lane, so
// it lives here rather than inside the lazily loaded chart suite — the footer
// needs it before that chunk has arrived.
export function TrendRangeRail({
  label,
  range,
  setRange,
  activeRanges,
  focusKey,
}: {
  label: string;
  range: TrendRange;
  setRange: (value: TrendRange) => void;
  activeRanges: readonly TrendRange[];
  focusKey?: (kind: string, id: string) => string;
}) {
  return (
    <div className="trend-range-switch" role="group" aria-label={label}>
      {TREND_RANGES.map((item) => (
        <button
          key={item}
          type="button"
          disabled={!activeRanges.includes(item)}
          aria-pressed={item === range}
          data-focus-key={focusKey?.("trend-range", item)}
          onClick={() => setRange(item)}
        >
          {item}
        </button>
      ))}
    </div>
  );
}
