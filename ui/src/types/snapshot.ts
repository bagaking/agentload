import type { ProjectHeatmapSet, TrendSet } from "../trend/types";

export type Snapshot = {
  generated_at?: string;
  refresh_slot_id?: string;
  config?: { idle_gap_seconds?: number; process_refresh_target_seconds?: number; history_file?: string };
  current?: CurrentMetrics;
  current_by_tool?: Record<string, CurrentMetrics>;
  summary?: SnapshotSummary;
  coordination_risk?: CoordinationRisk;
  historic_peaks?: { today?: PeakWindow; seven_day?: PeakWindow };
  trends?: TrendSet;
  realtime_trends?: TrendSet;
  project_heatmaps?: ProjectHeatmapSet;
  history?: { retained_sample_count?: number; loaded_sample_count?: number; last_write_error?: string };
  transcript_stats?: TranscriptStats;
  project_focus?: ProjectSnapshot[];
  candidate_workitems?: CandidateWorkitem[];
  age_buckets?: AgeBucketSnapshot[];
  live_processes?: LiveProcess[];
  live_sessions?: LiveSession[];
  notes?: string[];
};

export type CurrentMetrics = {
  active_burst_concurrency?: number;
  session_concurrency?: number;
  pid_concurrency?: number;
};

export type SnapshotSummary = {
  active_sessions?: number;
  idle_sessions?: number;
  main_agent_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  multi_mapped_processes?: number;
  project_count?: number;
  hot_project_count?: number;
  mapping_coverage_pct?: number;
};

export type CoordinationRisk = {
  top_project?: string;
  top_project_attention_share_pct?: number;
  active_project_count?: number;
  duplicate_overlap_suspicion_count?: number;
  candidate_workitem_count?: number;
  stale_session_count?: number;
  orphan_process_count?: number;
  low_confidence_session_count?: number;
  recent_window_minutes?: number;
  load_ratio_pct?: number;
  load_peak_value?: number;
  candidate_workitem_coverage_pct?: number;
  candidate_workitem_covered_session_count?: number;
  signals?: Array<{ kind?: string; severity?: string; evidence?: string }>;
};

export type PeakWindow = {
  session_concurrency?: { value?: number; at?: string };
  active_burst_concurrency?: { value?: number; at?: string };
};

export type TranscriptStats = {
  scanned_files?: number;
  parsed_files?: number;
  deferred_files?: number;
  tail_parsed_files?: number;
  historical_scan_deferred?: boolean;
  foreground_scan_lookback_seconds?: number;
  configured_history_lookback_seconds?: number;
  cached?: boolean;
  errors?: string[];
};

export type ProjectSnapshot = {
  project?: string;
  session_count?: number;
  active_burst_count?: number;
  main_agent_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  process_count?: number;
  attention_share_pct?: number;
  attention_basis?: string;
  stale_session_count?: number;
  recent_session_count?: number;
  confidence?: string;
  confidence_breakdown?: ConfidenceCount[];
  confidence_reasons?: string[];
  project_attribution_confidence?: string;
  project_attribution_reasons?: string[];
  last_event_age_seconds?: number;
  last_event_at?: string;
  tools?: ProjectTool[];
};

export type ConfidenceCount = {
  level?: string;
  count?: number;
};

export type AgeBucketSnapshot = {
  label?: string;
  count?: number;
};

export type ProjectTool = {
  tool?: string;
  session_count?: number;
  active_burst_count?: number;
  process_count?: number;
};

export type HostApp = {
  pid?: number;
  name?: string;
  bundle_path?: string;
};

export type LiveProcess = {
  pid?: number;
  tool?: string;
  command?: string;
  mapped_sessions?: number;
  session_ids?: string[];
  host_app?: HostApp;
};

export type LiveSession = {
  tool?: string;
  session_id?: string;
  session_role?: string;
  role_confidence?: string;
  role_reasons?: string[];
  thread_source?: string;
  parent_thread_id?: string;
  agent_role?: string;
  agent_nickname?: string;
  role_hint_source?: string;
  independently_run?: boolean;
  project?: string;
  process_count?: number;
  host_apps?: HostApp[];
  active_burst?: boolean;
  freshness?: string;
  mapping_method?: string;
  missing_transcript?: boolean;
  confidence?: string;
  confidence_reasons?: string[];
  first_event_at?: string;
  last_event_age_seconds?: number;
  last_event_at?: string;
  observed_duration_seconds?: number;
  active_duration_seconds?: number;
  idle_duration_seconds?: number;
  path?: string;
  provenance?: string[];
};

export type CandidateWorkitem = {
  key?: string;
  project?: string;
  tool?: string;
  freshness_bucket?: string;
  session_count?: number;
  process_count?: number;
  confidence?: string;
  confidence_reasons?: string[];
};
