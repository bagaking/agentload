export const TREND_RANGES = ["1D", "3D", "7D", "15D", "30D"] as const;

export type TrendRange = (typeof TREND_RANGES)[number];
export type TrendLane = "history" | "runtime";

export type TrendSet = { windows?: TrendWindow[] };

export type TrendWindow = {
  range?: string;
  from?: string;
  to?: string;
  granularity_seconds?: number;
  source_from?: string;
  source_lookback_hours?: number;
  points?: TrendPoint[];
  history_complete?: boolean;
};

export type TrendPoint = {
  at?: string;
  active_burst_concurrency?: number;
  session_concurrency?: number;
  pid_concurrency?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  mapping_coverage_pct?: number;
  transcript_sampled?: boolean;
  runtime_sampled?: boolean;
};

export type ProjectHeatmapSet = { windows?: ProjectHeatmapWindow[] };

export type ProjectHeatmapWindow = {
  range?: string;
  from?: string;
  to?: string;
  history_complete?: boolean;
  sample_window_count?: number;
  session_window_count?: number;
  items?: ProjectHeatmapItem[];
};

export type ProjectHeatmapItem = {
  project?: string;
  window_count?: number;
  session_window_count?: number;
  process_window_count?: number;
  active_window_count?: number;
  max_session_count?: number;
  max_process_count?: number;
  average_sessions?: number;
  share_pct?: number;
};
