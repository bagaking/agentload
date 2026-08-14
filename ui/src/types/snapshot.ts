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
  throughput_trends?: TrendSet;
  project_heatmaps?: ProjectHeatmapSet;
  history?: {
    retained_sample_count?: number;
    loaded_sample_count?: number;
    last_write_error?: string;
    throughput?: {
      minute_fact_count?: number;
      legacy_fact_count?: number;
      dropped_record_count?: number;
      corrupt_record_count?: number;
      last_write_error?: string;
    };
  };
  metric_registry?: MetricRegistryEntry[];
  diagnostics?: DiagnosticSnapshot;
  runtime_telemetry?: RuntimeTelemetrySnapshot;
  transcript_stats?: TranscriptStats;
  project_focus?: ProjectSnapshot[];
  candidate_workitems?: CandidateWorkitem[];
  age_buckets?: AgeBucketSnapshot[];
  system_resources?: SystemResourceSnapshot;
  process_stats?: ProcessObservationStats;
  live_processes?: LiveProcess[];
  live_sessions?: LiveSession[];
  runtime_process_summary?: ProcessRuntimeSummary[];
  host_app_process_summary?: HostAppProcessSummary[];
  notes?: string[];
};

export type MetricRegistryEntry = {
  key?: string;
  family?: string;
  label?: string;
  unit?: string;
  source?: string;
  window?: string;
  missing_state?: string;
  description?: string;
};

export type LiveTokenRateState = "live" | "zero" | "no_data" | "stale" | "unavailable";

export type LiveTokenRateSample = {
  output_tokens_per_second?: number | null;
  state?: LiveTokenRateState;
  basis?: "output_tokens" | string;
  source?: string;
  method?: string;
  window_seconds?: number;
  sample_interval_seconds?: number;
  active_sessions?: number;
  sampled_at?: string;
  latest_signal_at?: string;
  latest_event_at?: string;
  unavailable_reason?: string;
  // Set only when the rate is a floor measured from a subset of the eligible
  // transcripts; the counts say how much of that set it saw.
  coverage?: "partial";
  // Set when the degradation cannot be expressed as a file count.
  coverage_reason?: string;
  tracked_file_count?: number;
  eligible_file_count?: number;
  projects?: LiveTokenRateProjectSample[] | null;
};

export type LiveTokenRateProjectSample = {
  project?: string;
  output_tokens_per_second?: number;
  active_sessions?: number;
};

export type RuntimeTelemetrySnapshot = {
  configured?: boolean;
  status?: string;
  event_count?: number;
  last_event_at?: string;
  adapters?: RuntimeTelemetryAdapterState[];
  detail?: string;
};

export type RuntimeTelemetryAdapterState = {
  key?: string;
  label?: string;
  status?: string;
  detail?: string;
};

export type DiagnosticSnapshot = {
  generated_at?: string;
  anomaly_signals?: DiagnosticSignal[];
  evidence_gaps?: DiagnosticSignal[];
  baselines?: DiagnosticBaseline[];
  capabilities?: DiagnosticCapability[];
  export?: DiagnosticExportSummary;
};

export type DiagnosticSignal = {
  kind?: string;
  severity?: string;
  title?: string;
  detail?: string;
  evidence?: string;
  metric_key?: string;
  source?: string;
};

export type DiagnosticBaseline = {
  key?: string;
  label?: string;
  value?: string;
  status?: string;
  detail?: string;
  metric_key?: string;
};

export type DiagnosticCapability = {
  key?: string;
  label?: string;
  status?: string;
  detail?: string;
};

export type DiagnosticExportSummary = {
  endpoint?: string;
  redaction?: string;
  omitted_fields?: string[];
};

export type CurrentMetrics = {
  active_burst_concurrency?: number;
  session_concurrency?: number;
  pid_concurrency?: number;
};

export type ProcessObservationStats = {
  incomplete?: boolean;
  last_known?: boolean;
  error?: string;
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

// One checkout of a project. `name: ""`/absent is the main checkout.
export type ProjectWorktreeSnapshot = {
  name?: string;
  branch?: string;
  session_count?: number;
  active_burst_count?: number;
  process_count?: number;
  last_event_at?: string;
};

export type ProjectSnapshot = {
  project?: string;
  worktrees?: ProjectWorktreeSnapshot[];
  branches?: string[];
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
  token_usage?: TokenUsage;
  token_usage_source?: string;
  token_usage_confidence?: string;
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

export type SystemResourceSnapshot = {
  sampled_at?: string;
  supported?: boolean;
  thermal_state?: "nominal" | "fair" | "serious" | "critical";
  thermal_state_supported?: boolean;
  cpu_percent?: number;
  load_average_1?: number;
  load_average_5?: number;
  load_average_15?: number;
  uptime_seconds?: number;
  memory_total_bytes?: number;
  memory_used_bytes?: number;
  memory_free_bytes?: number;
  memory_used_pct?: number;
  disk_total_bytes?: number;
  disk_used_bytes?: number;
  disk_free_bytes?: number;
  disk_used_pct?: number;
  network_rx_bytes?: number;
  network_tx_bytes?: number;
  network_rx_packets?: number;
  network_tx_packets?: number;
  network_rx_errors?: number;
  network_tx_errors?: number;
  network_rx_drops?: number;
  network_rx_bytes_per_sec?: number;
  network_tx_bytes_per_sec?: number;
  network_rx_packets_per_sec?: number;
  network_tx_packets_per_sec?: number;
  network_error_packets_per_sec?: number;
  network_dropped_packets_per_sec?: number;
  network_packet_issue_pct?: number;
  network_interface_count?: number;
  sample_interval_seconds?: number;
  notes?: string[];
};

export type ProjectTool = {
  tool?: string;
  session_count?: number;
  active_burst_count?: number;
  process_count?: number;
  token_usage?: TokenUsage;
  token_usage_source?: string;
  token_usage_confidence?: string;
};

export type HostApp = {
  pid?: number;
  name?: string;
  bundle_path?: string;
};

export type TokenUsage = {
  input_tokens?: number;
  output_tokens?: number;
  cache_creation_input_tokens?: number;
  cache_read_input_tokens?: number;
  reasoning_output_tokens?: number;
  total_tokens?: number;
};

export type LiveProcess = {
  pid?: number;
  tool?: string;
  display_name?: string;
  command?: string;
  cpu_percent?: number;
  memory_bytes?: number;
  disk_read_bytes?: number;
  disk_write_bytes?: number;
  disk_read_bytes_per_sec?: number;
  disk_write_bytes_per_sec?: number;
  elapsed?: string;
  mapped_sessions?: number;
  mapped_active_sessions?: number;
  main_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  match_methods?: string[];
  evidence_summary?: string;
  mapped_session_evidence?: ProcessSessionEvidence[];
  session_ids?: string[];
  host_app?: HostApp;
};

export type ProcessDiagnostic = {
  pid?: number;
  command?: string;
  session_ids?: string[];
  session_paths?: string[];
  host_app?: HostApp;
};

export type ProcessSessionEvidence = {
  tool?: string;
  session_id?: string;
  project?: string;
  role?: string;
  active_burst?: boolean;
  freshness?: string;
  mapping_method?: string;
  confidence?: string;
  role_confidence?: string;
  last_event_age_seconds?: number;
  provenance?: string[];
};

export type ProcessRuntimeSummary = {
  key?: string;
  tool?: string;
  display_name?: string;
  pid_count?: number;
  cpu_percent?: number;
  memory_bytes?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  direct_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  active_sessions?: number;
};

export type HostAppProcessSummary = {
  key?: string;
  name?: string;
  pid?: number;
  pid_count?: number;
  cpu_percent?: number;
  memory_bytes?: number;
  mapped_processes?: number;
  unmapped_processes?: number;
  direct_sessions?: number;
  subagent_sessions?: number;
  unknown_role_sessions?: number;
  active_sessions?: number;
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
  worktree?: string;
  branch?: string;
  process_count?: number;
  process_cpu_percent?: number;
  process_memory_bytes?: number;
  host_apps?: HostApp[];
  active_burst?: boolean;
  freshness?: string;
  needs_review?: boolean;
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
  token_usage?: TokenUsage;
  token_usage_source?: string;
  token_usage_confidence?: string;
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
