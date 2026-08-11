export const TREND_RANGES = ["1D", "3D", "7D", "15D", "30D"] as const;

export type TrendRange = (typeof TREND_RANGES)[number];
export type TrendLane = "history" | "runtime" | "throughput";

export type TrendSet = { windows?: TrendWindow[] };

export type TrendWindow = {
  range?: string;
  from?: string;
  to?: string;
  granularity_seconds?: number;
  source_from?: string;
  source_lookback_hours?: number;
  output_token_rate_summary?: ThroughputTrendSummary;
  points?: TrendPoint[];
  history_complete?: boolean;
};

export type ThroughputTrendSummary = {
  max?: number;
  p95?: number;
  avg?: number;
  current?: number;
  current_at?: string;
  window_seconds?: number;
  sample_count?: number;
};

export type TrendPoint = {
  at?: string;
  active_burst_concurrency?: number;
  session_concurrency?: number;
  pid_concurrency?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  mapping_coverage_pct?: number;
  runtime_process_summary?: TrendProcessRuntimeSummary[];
  host_app_process_summary?: TrendHostAppProcessSummary[];
  transcript_sampled?: boolean;
  runtime_sampled?: boolean;
  output_tokens_per_second?: number;
  output_token_throughput_state?: "live" | "zero" | "no_data" | "stale" | "unavailable" | string;
  output_token_throughput_window_seconds?: number;
  output_token_active_sessions?: number;
  output_token_projects?: TrendThroughputProjectSample[];
  throughput_sampled?: boolean;
};

export type TrendThroughputProjectSample = {
  project?: string;
  output_tokens_per_second?: number;
  active_sessions?: number;
};

export type TrendProcessRuntimeSummary = {
  key?: string;
  tool?: string;
  display_name?: string;
  pid_count?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  direct_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  active_sessions?: number;
};

export type TrendHostAppProcessSummary = {
  key?: string;
  name?: string;
  pid?: number;
  pid_count?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  direct_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  active_sessions?: number;
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
