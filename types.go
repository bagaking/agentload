package main

import (
	"encoding/json"
	"time"
)

type Snapshot struct {
	GeneratedAt        string                      `json:"generated_at"`
	RefreshSlotID      string                      `json:"refresh_slot_id,omitempty"`
	Config             SnapshotConfig              `json:"config"`
	Current            CurrentMetrics              `json:"current"`
	CurrentByTool      map[string]ToolMetrics      `json:"current_by_tool"`
	Summary            SnapshotSummary             `json:"summary"`
	CoordinationRisk   CoordinationRiskSnapshot    `json:"coordination_risk"`
	HistoricPeaks      HistoricPeaks               `json:"historic_peaks"`
	Trends             TrendSet                    `json:"trends"`
	RealtimeTrends     TrendSet                    `json:"realtime_trends"`
	ThroughputTrends   TrendSet                    `json:"throughput_trends"`
	ProjectHeatmaps    ProjectHeatmapSet           `json:"project_heatmaps"`
	History            SnapshotHistory             `json:"history"`
	MetricRegistry     []MetricRegistryEntry       `json:"metric_registry"`
	Diagnostics        DiagnosticSnapshot          `json:"diagnostics"`
	RuntimeTelemetry   RuntimeTelemetrySnapshot    `json:"runtime_telemetry"`
	TranscriptStats    TranscriptStats             `json:"transcript_stats"`
	ProjectFocus       []ProjectSnapshot           `json:"project_focus"`
	CandidateWorkitems []CandidateWorkitemSnapshot `json:"candidate_workitems"`
	AgeBuckets         []AgeBucketSnapshot         `json:"age_buckets"`
	SystemResources    SystemResourceSnapshot      `json:"system_resources"`
	ProcessStats       ProcessObservationStats     `json:"process_stats"`
	LiveProcesses      []LiveProcessSnapshot       `json:"live_processes"`
	LiveSessions       []LiveSessionSnapshot       `json:"live_sessions"`
	RuntimeProcesses   []ProcessRuntimeSummary     `json:"runtime_process_summary,omitempty"`
	HostAppProcesses   []HostAppProcessSummary     `json:"host_app_process_summary,omitempty"`
	Notes              []string                    `json:"notes,omitempty"`
	LiveTokenRateFiles []TranscriptFile            `json:"-"`
	LiveTokenProjects  map[string]string           `json:"-"`
}

type SnapshotConfig struct {
	IdleGapSeconds       int      `json:"idle_gap_seconds"`
	MinIntervalSeconds   int      `json:"min_interval_seconds"`
	LookbackHours        int      `json:"lookback_hours"`
	TranscriptCacheTTL   int      `json:"transcript_cache_ttl_seconds"`
	ClaudeRoots          []string `json:"claude_roots"`
	CodexRoots           []string `json:"codex_roots"`
	TraeRoots            []string `json:"trae_roots"`
	ProcessRefreshTarget int      `json:"process_refresh_target_seconds"`
	HistoryFile          string   `json:"history_file"`
}

type SnapshotHistory struct {
	StorePath           string                     `json:"store_path"`
	LoadedSampleCount   int                        `json:"loaded_sample_count"`
	RetainedSampleCount int                        `json:"retained_sample_count"`
	DroppedSampleCount  int                        `json:"dropped_sample_count"`
	CorruptLineCount    int                        `json:"corrupt_line_count"`
	FirstSampleAt       string                     `json:"first_sample_at,omitempty"`
	LastSampleAt        string                     `json:"last_sample_at,omitempty"`
	LastWriteError      string                     `json:"last_write_error,omitempty"`
	Throughput          *SnapshotThroughputHistory `json:"throughput,omitempty"`
}

type SnapshotThroughputHistory struct {
	StorePath          string `json:"store_path"`
	MinuteFactCount    int    `json:"minute_fact_count"`
	LegacyFactCount    int    `json:"legacy_fact_count"`
	DroppedRecordCount int    `json:"dropped_record_count"`
	CorruptRecordCount int    `json:"corrupt_record_count"`
	LastWriteError     string `json:"last_write_error,omitempty"`
}

type CurrentMetrics struct {
	PIDConcurrency         int `json:"pid_concurrency"`
	SessionConcurrency     int `json:"session_concurrency"`
	ActiveBurstConcurrency int `json:"active_burst_concurrency"`
}

// ProcessObservationStats distinguishes a clean process sample from a
// last-known snapshot retained while the OS process query is unavailable.
// Incomplete process evidence must never be committed to runtime history.
type ProcessObservationStats struct {
	Incomplete bool   `json:"incomplete,omitempty"`
	LastKnown  bool   `json:"last_known,omitempty"`
	Error      string `json:"error,omitempty"`
}

type ToolMetrics struct {
	PIDConcurrency         int `json:"pid_concurrency"`
	SessionConcurrency     int `json:"session_concurrency"`
	ActiveBurstConcurrency int `json:"active_burst_concurrency"`
}

type SnapshotSummary struct {
	ActiveSessions       int     `json:"active_sessions"`
	IdleSessions         int     `json:"idle_sessions"`
	MainAgentSessions    int     `json:"main_agent_sessions"`
	SubagentSessions     int     `json:"subagent_sessions"`
	UnknownRoleSessions  int     `json:"unknown_role_sessions"`
	MappedProcesses      int     `json:"mapped_processes"`
	UnmappedProcesses    int     `json:"unmapped_processes"`
	MultiMappedProcesses int     `json:"multi_mapped_processes"`
	ProjectCount         int     `json:"project_count"`
	HotProjectCount      int     `json:"hot_project_count"`
	MappingCoveragePct   float64 `json:"mapping_coverage_pct"`
}

type MetricRegistryEntry struct {
	Key          string `json:"key"`
	Family       string `json:"family"`
	Label        string `json:"label"`
	Unit         string `json:"unit,omitempty"`
	Source       string `json:"source"`
	Window       string `json:"window,omitempty"`
	MissingState string `json:"missing_state"`
	Description  string `json:"description"`
}

type LiveTokenRateSample struct {
	OutputTokensPerSecond *float64                     `json:"output_tokens_per_second"`
	State                 string                       `json:"state"`
	Basis                 string                       `json:"basis"`
	Source                string                       `json:"source"`
	Method                string                       `json:"method"`
	WindowSeconds         int                          `json:"window_seconds"`
	SampleIntervalSeconds int                          `json:"sample_interval_seconds"`
	ActiveSessions        int                          `json:"active_sessions"`
	SampledAt             string                       `json:"sampled_at"`
	LatestSignalAt        string                       `json:"latest_signal_at,omitempty"`
	LatestEventAt         string                       `json:"latest_event_at,omitempty"`
	UnavailableReason     string                       `json:"unavailable_reason,omitempty"`
	Projects              []LiveTokenRateProjectSample `json:"projects"`
}

type LiveTokenRateProjectSample struct {
	Project               string  `json:"project"`
	OutputTokensPerSecond float64 `json:"output_tokens_per_second"`
	ActiveSessions        int     `json:"active_sessions"`
}

type RuntimeTelemetrySnapshot struct {
	Configured  bool                           `json:"configured"`
	Status      string                         `json:"status"`
	EventCount  int                            `json:"event_count"`
	LastEventAt string                         `json:"last_event_at,omitempty"`
	Adapters    []RuntimeTelemetryAdapterState `json:"adapters,omitempty"`
	Detail      string                         `json:"detail,omitempty"`
}

type RuntimeTelemetryAdapterState struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type DiagnosticSnapshot struct {
	GeneratedAt    string                         `json:"generated_at,omitempty"`
	AnomalySignals []DiagnosticSignalSnapshot     `json:"anomaly_signals,omitempty"`
	EvidenceGaps   []DiagnosticSignalSnapshot     `json:"evidence_gaps,omitempty"`
	Baselines      []DiagnosticBaselineSnapshot   `json:"baselines,omitempty"`
	Capabilities   []DiagnosticCapabilitySnapshot `json:"capabilities,omitempty"`
	Export         DiagnosticExportSummary        `json:"export"`
}

type DiagnosticSignalSnapshot struct {
	Kind      string `json:"kind"`
	Severity  string `json:"severity"`
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
	Evidence  string `json:"evidence,omitempty"`
	MetricKey string `json:"metric_key,omitempty"`
	Source    string `json:"source,omitempty"`
}

type DiagnosticBaselineSnapshot struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Value     string `json:"value"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"`
	MetricKey string `json:"metric_key,omitempty"`
}

type DiagnosticCapabilitySnapshot struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type DiagnosticExportSummary struct {
	Endpoint      string   `json:"endpoint,omitempty"`
	Redaction     string   `json:"redaction,omitempty"`
	OmittedFields []string `json:"omitted_fields,omitempty"`
}

type DiagnosticExportSnapshot struct {
	FormatVersion int      `json:"format_version"`
	GeneratedAt   string   `json:"generated_at"`
	Snapshot      Snapshot `json:"snapshot"`
	OmittedFields []string `json:"omitted_fields"`
	Notes         []string `json:"notes"`
}

type CoordinationRiskSnapshot struct {
	Posture                              string                    `json:"posture"`
	ActiveProjectCount                   int                       `json:"active_project_count"`
	RecentProjectCount                   int                       `json:"recent_project_count"`
	TopProject                           string                    `json:"top_project"`
	TopProjectAttentionSharePct          float64                   `json:"top_project_attention_share_pct"`
	DuplicateOverlapSuspicionCount       int                       `json:"duplicate_overlap_suspicion_count"`
	DuplicateOverlapClusterCount         int                       `json:"duplicate_overlap_cluster_count"`
	CandidateWorkitemCount               int                       `json:"candidate_workitem_count"`
	CandidateWorkitemCoveredSessionCount int                       `json:"candidate_workitem_covered_session_count"`
	CandidateWorkitemCoveragePct         float64                   `json:"candidate_workitem_coverage_pct"`
	CandidateWorkitemConfidenceBreakdown []ConfidenceCountSnapshot `json:"candidate_workitem_confidence_breakdown"`
	StaleSessionCount                    int                       `json:"stale_session_count"`
	OrphanProcessCount                   int                       `json:"orphan_process_count"`
	ChurnSessionCount                    int                       `json:"churn_session_count"`
	ProjectSpreadCount                   int                       `json:"project_spread_count"`
	FragmentationPct                     float64                   `json:"fragmentation_pct"`
	LoadRatioPct                         float64                   `json:"load_ratio_pct"`
	LoadPeakValue                        int                       `json:"load_peak_value"`
	LoadPeakSource                       string                    `json:"load_peak_source,omitempty"`
	LoadPeakAt                           string                    `json:"load_peak_at,omitempty"`
	RecentWindowMinutes                  int                       `json:"recent_window_minutes"`
	LowConfidenceSessionCount            int                       `json:"low_confidence_session_count"`
	Signals                              []RiskSignalSnapshot      `json:"signals"`
}

type RiskSignalSnapshot struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Evidence string `json:"evidence"`
}

type ConfidenceCountSnapshot struct {
	Level string `json:"level"`
	Count int    `json:"count"`
}

type ProvenanceCountSnapshot struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

type AttributionSourceCountSnapshot struct {
	Source string `json:"source"`
	Count  int    `json:"count"`
}

type HistoricPeaks struct {
	Today    PeakWindow `json:"today"`
	SevenDay PeakWindow `json:"seven_day"`
}

type PeakWindow struct {
	SessionConcurrency     PeakPoint `json:"session_concurrency"`
	ActiveBurstConcurrency PeakPoint `json:"active_burst_concurrency"`
}

type PeakPoint struct {
	Value int    `json:"value"`
	At    string `json:"at,omitempty"`
}

type TrendSet struct {
	Windows []TrendWindow `json:"windows"`
}

type ProjectHeatmapSet struct {
	Windows []ProjectHeatmapWindow `json:"windows"`
}

type ProjectHeatmapWindow struct {
	Range              string               `json:"range"`
	From               string               `json:"from"`
	To                 string               `json:"to"`
	HistoryComplete    bool                 `json:"history_complete"`
	SampleWindowCount  int                  `json:"sample_window_count"`
	SessionWindowCount int                  `json:"session_window_count"`
	Items              []ProjectHeatmapItem `json:"items"`
}

type ProjectHeatmapItem struct {
	Project            string  `json:"project"`
	WindowCount        int     `json:"window_count"`
	SessionWindowCount int     `json:"session_window_count"`
	ProcessWindowCount int     `json:"process_window_count"`
	ActiveWindowCount  int     `json:"active_window_count"`
	MaxSessionCount    int     `json:"max_session_count"`
	MaxProcessCount    int     `json:"max_process_count"`
	AverageSessions    float64 `json:"average_sessions"`
	SharePct           float64 `json:"share_pct"`
}

type TrendWindow struct {
	Range               string                  `json:"range"`
	From                string                  `json:"from"`
	To                  string                  `json:"to"`
	GranularitySeconds  int                     `json:"granularity_seconds"`
	SourceFrom          string                  `json:"source_from,omitempty"`
	SourceLookbackHours int                     `json:"source_lookback_hours,omitempty"`
	HistoryComplete     bool                    `json:"history_complete"`
	ThroughputSeries    []ThroughputTrendSeries `json:"throughput_series,omitempty"`
	Points              []TrendPoint            `json:"points"`
}

type ThroughputTrendSeries struct {
	Key                string                  `json:"key"`
	Kind               string                  `json:"kind"`
	WindowSeconds      int                     `json:"window_seconds"`
	GranularitySeconds int                     `json:"granularity_seconds"`
	SourceFrom         string                  `json:"source_from,omitempty"`
	HistoryComplete    bool                    `json:"history_complete"`
	Summary            *ThroughputTrendSummary `json:"summary,omitempty"`
	Points             []TrendPoint            `json:"points"`
}

type ThroughputTrendSummary struct {
	Max           float64  `json:"max"`
	P95           float64  `json:"p95"`
	Avg           float64  `json:"avg"`
	Current       *float64 `json:"current,omitempty"`
	CurrentAt     string   `json:"current_at,omitempty"`
	WindowSeconds int      `json:"window_seconds"`
	SampleCount   int      `json:"sample_count"`
}

type TrendPoint struct {
	At                                 string                       `json:"at"`
	ActiveBurstConcurrency             int                          `json:"active_burst_concurrency"`
	HasActiveBurst                     bool                         `json:"-"`
	SessionConcurrency                 int                          `json:"session_concurrency"`
	HasSessionConcurrency              bool                         `json:"-"`
	TranscriptSampled                  bool                         `json:"transcript_sampled"`
	PIDConcurrency                     int                          `json:"pid_concurrency"`
	HasPIDConcurrency                  bool                         `json:"-"`
	MappingCoveragePct                 float64                      `json:"mapping_coverage_pct"`
	HasMappingCoveragePct              bool                         `json:"-"`
	MappedProcesses                    int                          `json:"mapped_processes"`
	HasMappedProcesses                 bool                         `json:"-"`
	UnmappedProcesses                  int                          `json:"unmapped_processes"`
	HasUnmappedProcesses               bool                         `json:"-"`
	RuntimeSampled                     bool                         `json:"runtime_sampled"`
	RuntimeProcesses                   []ProcessRuntimeSummary      `json:"runtime_process_summary,omitempty"`
	HostAppProcesses                   []HostAppProcessSummary      `json:"host_app_process_summary,omitempty"`
	OutputTokensPerSecond              float64                      `json:"output_tokens_per_second"`
	HasOutputTokensPerSecond           bool                         `json:"-"`
	OutputTokenThroughputState         string                       `json:"output_token_throughput_state"`
	OutputTokenThroughputWindowSeconds int                          `json:"output_token_throughput_window_seconds"`
	OutputTokenActiveSessions          int                          `json:"output_token_active_sessions"`
	HasOutputTokenActiveSessions       bool                         `json:"-"`
	OutputTokenProjects                []LiveTokenRateProjectSample `json:"output_token_projects,omitempty"`
	ThroughputSampled                  bool                         `json:"throughput_sampled"`
}

type trendPointJSON struct {
	At                                 string                        `json:"at"`
	ActiveBurstConcurrency             *int                          `json:"active_burst_concurrency,omitempty"`
	SessionConcurrency                 *int                          `json:"session_concurrency,omitempty"`
	TranscriptSampled                  *bool                         `json:"transcript_sampled,omitempty"`
	PIDConcurrency                     *int                          `json:"pid_concurrency,omitempty"`
	MappingCoveragePct                 *float64                      `json:"mapping_coverage_pct,omitempty"`
	MappedProcesses                    *int                          `json:"mapped_processes,omitempty"`
	UnmappedProcesses                  *int                          `json:"unmapped_processes,omitempty"`
	RuntimeProcesses                   []ProcessRuntimeSummary       `json:"runtime_process_summary,omitempty"`
	HostAppProcesses                   []HostAppProcessSummary       `json:"host_app_process_summary,omitempty"`
	RuntimeSampled                     *bool                         `json:"runtime_sampled,omitempty"`
	OutputTokensPerSecond              *float64                      `json:"output_tokens_per_second,omitempty"`
	OutputTokenThroughputState         string                        `json:"output_token_throughput_state,omitempty"`
	OutputTokenThroughputWindowSeconds int                           `json:"output_token_throughput_window_seconds,omitempty"`
	OutputTokenActiveSessions          *int                          `json:"output_token_active_sessions,omitempty"`
	OutputTokenProjects                *[]LiveTokenRateProjectSample `json:"output_token_projects,omitempty"`
	ThroughputSampled                  *bool                         `json:"throughput_sampled,omitempty"`
}

func (p TrendPoint) MarshalJSON() ([]byte, error) {
	payload := trendPointJSON{
		At: p.At,
	}
	if p.TranscriptSampled {
		if p.HasActiveBurst {
			payload.ActiveBurstConcurrency = jsonValue(p.ActiveBurstConcurrency)
		}
		if p.HasSessionConcurrency {
			payload.SessionConcurrency = jsonValue(p.SessionConcurrency)
		}
		payload.TranscriptSampled = jsonValue(true)
	}
	if p.RuntimeSampled {
		if p.HasPIDConcurrency {
			payload.PIDConcurrency = jsonValue(p.PIDConcurrency)
		}
		if p.HasMappingCoveragePct {
			payload.MappingCoveragePct = jsonValue(p.MappingCoveragePct)
		}
		if p.HasMappedProcesses {
			payload.MappedProcesses = jsonValue(p.MappedProcesses)
		}
		if p.HasUnmappedProcesses {
			payload.UnmappedProcesses = jsonValue(p.UnmappedProcesses)
		}
		payload.RuntimeProcesses = p.RuntimeProcesses
		payload.HostAppProcesses = p.HostAppProcesses
		payload.RuntimeSampled = jsonValue(true)
	}
	if p.ThroughputSampled {
		if p.HasOutputTokensPerSecond {
			payload.OutputTokensPerSecond = jsonValue(p.OutputTokensPerSecond)
		}
		if p.HasOutputTokenActiveSessions {
			payload.OutputTokenActiveSessions = jsonValue(p.OutputTokenActiveSessions)
		}
		if p.HasOutputTokensPerSecond && p.OutputTokenProjects != nil {
			projects := cloneLiveTokenRateProjectSamples(p.OutputTokenProjects)
			payload.OutputTokenProjects = &projects
		}
		payload.OutputTokenThroughputState = p.OutputTokenThroughputState
		payload.OutputTokenThroughputWindowSeconds = p.OutputTokenThroughputWindowSeconds
		payload.ThroughputSampled = jsonValue(true)
	}
	return json.Marshal(payload)
}

func jsonValue[T any](v T) *T {
	return &v
}

type TranscriptStats struct {
	ScannedFiles                     int      `json:"scanned_files"`
	ParsedFiles                      int      `json:"parsed_files"`
	DeferredFiles                    int      `json:"deferred_files"`
	TailParsedFiles                  int      `json:"tail_parsed_files,omitempty"`
	HistoricalScanDeferred           bool     `json:"historical_scan_deferred,omitempty"`
	CoverageIncomplete               bool     `json:"coverage_incomplete,omitempty"`
	ForegroundScanLookbackSeconds    int      `json:"foreground_scan_lookback_seconds,omitempty"`
	ConfiguredHistoryLookbackSeconds int      `json:"configured_history_lookback_seconds,omitempty"`
	Cached                           bool     `json:"cached"`
	Errors                           []string `json:"errors,omitempty"`
}

type ProjectSnapshot struct {
	Project                         string                           `json:"project"`
	SessionCount                    int                              `json:"session_count"`
	ActiveBurstCount                int                              `json:"active_burst_count"`
	MainAgentSessions               int                              `json:"main_agent_sessions"`
	SubagentSessions                int                              `json:"subagent_sessions"`
	UnknownRoleSessions             int                              `json:"unknown_role_sessions"`
	ProcessCount                    int                              `json:"process_count"`
	AttentionSharePct               float64                          `json:"attention_share_pct"`
	AttentionBasis                  string                           `json:"attention_basis"`
	StaleSessionCount               int                              `json:"stale_session_count"`
	RecentSessionCount              int                              `json:"recent_session_count"`
	Confidence                      string                           `json:"confidence"`
	ConfidenceBreakdown             []ConfidenceCountSnapshot        `json:"confidence_breakdown"`
	ConfidenceReasons               []string                         `json:"confidence_reasons,omitempty"`
	ProjectAttributionConfidence    string                           `json:"project_attribution_confidence"`
	ProjectAttributionReasons       []string                         `json:"project_attribution_reasons,omitempty"`
	ProjectAttributionSourceSummary []AttributionSourceCountSnapshot `json:"project_attribution_source_summary"`
	ProvenanceSummary               []ProvenanceCountSnapshot        `json:"provenance_summary"`
	LastEventAt                     string                           `json:"last_event_at,omitempty"`
	LastEventAgeSeconds             int                              `json:"last_event_age_seconds,omitempty"`
	TokenUsage                      *TokenUsage                      `json:"token_usage,omitempty"`
	TokenUsageSource                string                           `json:"token_usage_source,omitempty"`
	TokenUsageConfidence            string                           `json:"token_usage_confidence,omitempty"`
	Tools                           []ProjectToolSnapshot            `json:"tools,omitempty"`
}

type ProjectToolSnapshot struct {
	Tool                 string      `json:"tool"`
	SessionCount         int         `json:"session_count"`
	ActiveBurstCount     int         `json:"active_burst_count"`
	ProcessCount         int         `json:"process_count"`
	TokenUsage           *TokenUsage `json:"token_usage,omitempty"`
	TokenUsageSource     string      `json:"token_usage_source,omitempty"`
	TokenUsageConfidence string      `json:"token_usage_confidence,omitempty"`
}

type AgeBucketSnapshot struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type SystemResourceSnapshot struct {
	SampledAt                   string   `json:"sampled_at,omitempty"`
	Supported                   bool     `json:"supported"`
	ThermalState                string   `json:"thermal_state,omitempty"`
	ThermalStateSupported       bool     `json:"thermal_state_supported"`
	CPUPercent                  float64  `json:"cpu_percent"`
	LoadAverage1                float64  `json:"load_average_1"`
	LoadAverage5                float64  `json:"load_average_5"`
	LoadAverage15               float64  `json:"load_average_15"`
	UptimeSeconds               int64    `json:"uptime_seconds"`
	MemoryTotalBytes            uint64   `json:"memory_total_bytes"`
	MemoryUsedBytes             uint64   `json:"memory_used_bytes"`
	MemoryFreeBytes             uint64   `json:"memory_free_bytes"`
	MemoryUsedPct               float64  `json:"memory_used_pct"`
	DiskTotalBytes              uint64   `json:"disk_total_bytes"`
	DiskUsedBytes               uint64   `json:"disk_used_bytes"`
	DiskFreeBytes               uint64   `json:"disk_free_bytes"`
	DiskUsedPct                 float64  `json:"disk_used_pct"`
	NetworkRxBytes              uint64   `json:"network_rx_bytes"`
	NetworkTxBytes              uint64   `json:"network_tx_bytes"`
	NetworkRxPackets            uint64   `json:"network_rx_packets"`
	NetworkTxPackets            uint64   `json:"network_tx_packets"`
	NetworkRxErrors             uint64   `json:"network_rx_errors"`
	NetworkTxErrors             uint64   `json:"network_tx_errors"`
	NetworkRxDrops              uint64   `json:"network_rx_drops"`
	NetworkRxBytesPerSec        float64  `json:"network_rx_bytes_per_sec"`
	NetworkTxBytesPerSec        float64  `json:"network_tx_bytes_per_sec"`
	NetworkRxPacketsPerSec      float64  `json:"network_rx_packets_per_sec"`
	NetworkTxPacketsPerSec      float64  `json:"network_tx_packets_per_sec"`
	NetworkErrorPacketsPerSec   float64  `json:"network_error_packets_per_sec"`
	NetworkDroppedPacketsPerSec float64  `json:"network_dropped_packets_per_sec"`
	NetworkPacketIssuePct       float64  `json:"network_packet_issue_pct"`
	NetworkInterfaceCount       int      `json:"network_interface_count"`
	SampleIntervalSeconds       float64  `json:"sample_interval_seconds"`
	Notes                       []string `json:"notes,omitempty"`
}

type LiveProcessSnapshot struct {
	PID                   int                      `json:"pid"`
	Tool                  string                   `json:"tool"`
	DisplayName           string                   `json:"display_name,omitempty"`
	Command               string                   `json:"command"`
	CPUPercent            float64                  `json:"cpu_percent,omitempty"`
	MemoryBytes           int64                    `json:"memory_bytes,omitempty"`
	DiskReadBytes         uint64                   `json:"disk_read_bytes,omitempty"`
	DiskWriteBytes        uint64                   `json:"disk_write_bytes,omitempty"`
	DiskReadBytesPerSec   float64                  `json:"disk_read_bytes_per_sec,omitempty"`
	DiskWriteBytesPerSec  float64                  `json:"disk_write_bytes_per_sec,omitempty"`
	Elapsed               string                   `json:"elapsed,omitempty"`
	HostApp               *HostApp                 `json:"host_app,omitempty"`
	SessionIDs            []string                 `json:"session_ids,omitempty"`
	SessionPaths          []string                 `json:"session_paths,omitempty"`
	MappedSessions        int                      `json:"mapped_sessions"`
	MappedActiveSessions  int                      `json:"mapped_active_sessions"`
	MainSessions          int                      `json:"main_sessions"`
	SubagentSessions      int                      `json:"subagent_sessions"`
	UnknownRoleSessions   int                      `json:"unknown_role_sessions"`
	MatchMethods          []string                 `json:"match_methods,omitempty"`
	EvidenceSummary       string                   `json:"evidence_summary,omitempty"`
	MappedSessionEvidence []ProcessSessionEvidence `json:"mapped_session_evidence,omitempty"`
}

type HostApp struct {
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	BundlePath string `json:"bundle_path,omitempty"`
}

type ProcessSessionEvidence struct {
	Tool                string   `json:"tool,omitempty"`
	SessionID           string   `json:"session_id"`
	Project             string   `json:"project,omitempty"`
	Role                string   `json:"role"`
	ActiveBurst         bool     `json:"active_burst"`
	Freshness           string   `json:"freshness,omitempty"`
	MappingMethod       string   `json:"mapping_method,omitempty"`
	Confidence          string   `json:"confidence,omitempty"`
	RoleConfidence      string   `json:"role_confidence,omitempty"`
	LastEventAgeSeconds int      `json:"last_event_age_seconds,omitempty"`
	Provenance          []string `json:"provenance,omitempty"`
}

type ProcessDiagnosticSnapshot struct {
	PID          int      `json:"pid"`
	Command      string   `json:"command,omitempty"`
	SessionIDs   []string `json:"session_ids,omitempty"`
	SessionPaths []string `json:"session_paths,omitempty"`
	HostApp      *HostApp `json:"host_app,omitempty"`
}

type ProcessRuntimeSummary struct {
	Key                 string  `json:"key"`
	Tool                string  `json:"tool"`
	DisplayName         string  `json:"display_name"`
	PIDCount            int     `json:"pid_count"`
	CPUPercent          float64 `json:"cpu_percent,omitempty"`
	MemoryBytes         int64   `json:"memory_bytes,omitempty"`
	MappedProcesses     int     `json:"mapped_processes"`
	UnmappedProcesses   int     `json:"unmapped_processes"`
	DirectSessions      int     `json:"direct_sessions"`
	SubagentSessions    int     `json:"subagent_sessions"`
	UnknownRoleSessions int     `json:"unknown_role_sessions"`
	ActiveSessions      int     `json:"active_sessions"`
}

type HostAppProcessSummary struct {
	Key                 string  `json:"key"`
	Name                string  `json:"name"`
	PID                 int     `json:"pid,omitempty"`
	PIDCount            int     `json:"pid_count"`
	CPUPercent          float64 `json:"cpu_percent,omitempty"`
	MemoryBytes         int64   `json:"memory_bytes,omitempty"`
	MappedProcesses     int     `json:"mapped_processes"`
	UnmappedProcesses   int     `json:"unmapped_processes"`
	DirectSessions      int     `json:"direct_sessions"`
	SubagentSessions    int     `json:"subagent_sessions"`
	UnknownRoleSessions int     `json:"unknown_role_sessions"`
	ActiveSessions      int     `json:"active_sessions"`
}

type LiveSessionSnapshot struct {
	Tool                         string      `json:"tool"`
	SessionID                    string      `json:"session_id"`
	SessionRole                  string      `json:"session_role"`
	RoleConfidence               string      `json:"role_confidence"`
	RoleReasons                  []string    `json:"role_reasons,omitempty"`
	ThreadSource                 string      `json:"thread_source,omitempty"`
	ParentThreadID               string      `json:"parent_thread_id,omitempty"`
	AgentRole                    string      `json:"agent_role,omitempty"`
	AgentNickname                string      `json:"agent_nickname,omitempty"`
	RoleHintSource               string      `json:"role_hint_source,omitempty"`
	IndependentlyRun             bool        `json:"independently_run,omitempty"`
	Project                      string      `json:"project"`
	Path                         string      `json:"path"`
	ProcessCount                 int         `json:"process_count"`
	SharedProcessCount           int         `json:"shared_process_count"`
	ProcessCPUPercent            float64     `json:"process_cpu_percent,omitempty"`
	ProcessMemoryBytes           int64       `json:"process_memory_bytes,omitempty"`
	HostApps                     []HostApp   `json:"host_apps,omitempty"`
	FirstEventAt                 string      `json:"first_event_at,omitempty"`
	LastEventAt                  string      `json:"last_event_at,omitempty"`
	LastEventAgeSeconds          int         `json:"last_event_age_seconds,omitempty"`
	ObservedDurationSeconds      int         `json:"observed_duration_seconds,omitempty"`
	ActiveDurationSeconds        int         `json:"active_duration_seconds,omitempty"`
	IdleDurationSeconds          int         `json:"idle_duration_seconds,omitempty"`
	TokenUsage                   *TokenUsage `json:"token_usage,omitempty"`
	TokenUsageSource             string      `json:"token_usage_source,omitempty"`
	TokenUsageConfidence         string      `json:"token_usage_confidence,omitempty"`
	ActiveBurst                  bool        `json:"active_burst"`
	Freshness                    string      `json:"freshness"`
	NeedsReview                  bool        `json:"needs_review"`
	MappingMethod                string      `json:"mapping_method"`
	MissingTranscript            bool        `json:"missing_transcript"`
	Confidence                   string      `json:"confidence"`
	ConfidenceReasons            []string    `json:"confidence_reasons,omitempty"`
	ProjectAttributionSource     string      `json:"project_attribution_source"`
	ProjectAttributionConfidence string      `json:"project_attribution_confidence"`
	ProjectAttributionReasons    []string    `json:"project_attribution_reasons,omitempty"`
	Provenance                   []string    `json:"provenance"`
}

type CandidateWorkitemSnapshot struct {
	Key                             string                           `json:"key"`
	Project                         string                           `json:"project"`
	Tool                            string                           `json:"tool"`
	FreshnessBucket                 string                           `json:"freshness_bucket"`
	SessionCount                    int                              `json:"session_count"`
	ProcessCount                    int                              `json:"process_count"`
	SessionIDs                      []string                         `json:"session_ids"`
	Canonical                       bool                             `json:"canonical"`
	InferenceMode                   string                           `json:"inference_mode"`
	FallbackView                    string                           `json:"fallback_view"`
	Confidence                      string                           `json:"confidence"`
	ConfidenceReasons               []string                         `json:"confidence_reasons,omitempty"`
	ProjectAttributionConfidence    string                           `json:"project_attribution_confidence"`
	ProjectAttributionReasons       []string                         `json:"project_attribution_reasons,omitempty"`
	ProjectAttributionSourceSummary []AttributionSourceCountSnapshot `json:"project_attribution_source_summary"`
	ProvenanceSummary               []ProvenanceCountSnapshot        `json:"provenance_summary"`
}

type TranscriptFile struct {
	Tool          string
	Path          string
	SessionIDHint string
}

type SessionTrace struct {
	Tool             string
	Path             string
	SessionID        string
	Project          string
	ProjectSource    string
	ThreadSource     string
	ParentThreadID   string
	AgentRole        string
	AgentNickname    string
	RoleHintSource   string
	IndependentlyRun bool
	EventTimes       []time.Time
	FirstEvent       time.Time
	LastEvent        time.Time
	TokenUsage       TokenUsage
}

type TokenUsage struct {
	InputTokens              int `json:"input_tokens,omitempty"`
	OutputTokens             int `json:"output_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	ReasoningOutputTokens    int `json:"reasoning_output_tokens,omitempty"`
	TotalTokens              int `json:"total_tokens,omitempty"`
}

type Interval struct {
	Tool      string
	SessionID string
	Path      string
	Project   string
	Start     time.Time
	End       time.Time
}

type TranscriptData struct {
	Traces                           map[string]*SessionTrace
	SessionSpans                     []Interval
	BurstSpans                       []Interval
	ScannedFiles                     int
	ParsedFiles                      int
	DeferredFiles                    int
	TailParsedFiles                  int
	HistoricalScanDeferred           bool
	CoverageIncomplete               bool
	ForegroundScanLookbackSeconds    int
	ConfiguredHistoryLookbackSeconds int
	Errors                           []string
	// evidenceRevision is internal provenance for cache publication. It is not
	// serialized; public callers receive CoverageIncomplete when the revision
	// changed during collection or parsing.
	evidenceRevision uint64
}

type transcriptCacheState struct {
	Key              string
	ExpiresAt        time.Time
	EvidenceRevision uint64
	Data             *TranscriptData
}

type LiveProcess struct {
	PID                  int
	PPID                 int
	Tool                 string
	DisplayName          string
	Command              string
	CPUPercent           float64
	MemoryBytes          int64
	DiskReadBytes        uint64
	DiskWriteBytes       uint64
	DiskReadBytesPerSec  float64
	DiskWriteBytesPerSec float64
	Elapsed              string
	HostApp              *HostApp
	SessionFiles         []TranscriptFile
	SessionHints         []string
}

type LiveSession struct {
	Tool      string
	SessionID string
	Path      string
	Processes map[int]struct{}
	HostApps  map[int]HostApp
	Trace     *SessionTrace
	Mapping   LiveSessionMapping
}

type LiveSessionMapping struct {
	TranscriptPath     bool
	TranscriptActivity bool
	ParsedTranscriptID bool
	CommandHint        bool
	FallbackSessionID  bool
}
