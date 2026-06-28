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
	History            SnapshotHistory             `json:"history"`
	TranscriptStats    TranscriptStats             `json:"transcript_stats"`
	ProjectFocus       []ProjectSnapshot           `json:"project_focus"`
	CandidateWorkitems []CandidateWorkitemSnapshot `json:"candidate_workitems"`
	AgeBuckets         []AgeBucketSnapshot         `json:"age_buckets"`
	LiveProcesses      []LiveProcessSnapshot       `json:"live_processes"`
	LiveSessions       []LiveSessionSnapshot       `json:"live_sessions"`
	Notes              []string                    `json:"notes,omitempty"`
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
	StorePath           string `json:"store_path"`
	LoadedSampleCount   int    `json:"loaded_sample_count"`
	RetainedSampleCount int    `json:"retained_sample_count"`
	DroppedSampleCount  int    `json:"dropped_sample_count"`
	CorruptLineCount    int    `json:"corrupt_line_count"`
	FirstSampleAt       string `json:"first_sample_at,omitempty"`
	LastSampleAt        string `json:"last_sample_at,omitempty"`
	LastWriteError      string `json:"last_write_error,omitempty"`
}

type CurrentMetrics struct {
	PIDConcurrency         int `json:"pid_concurrency"`
	SessionConcurrency     int `json:"session_concurrency"`
	ActiveBurstConcurrency int `json:"active_burst_concurrency"`
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

type TrendWindow struct {
	Range               string       `json:"range"`
	From                string       `json:"from"`
	To                  string       `json:"to"`
	GranularitySeconds  int          `json:"granularity_seconds"`
	SourceFrom          string       `json:"source_from,omitempty"`
	SourceLookbackHours int          `json:"source_lookback_hours,omitempty"`
	HistoryComplete     bool         `json:"history_complete"`
	Points              []TrendPoint `json:"points"`
}

type TrendPoint struct {
	At                     string  `json:"at"`
	ActiveBurstConcurrency int     `json:"active_burst_concurrency"`
	HasActiveBurst         bool    `json:"-"`
	SessionConcurrency     int     `json:"session_concurrency"`
	HasSessionConcurrency  bool    `json:"-"`
	TranscriptSampled      bool    `json:"transcript_sampled"`
	PIDConcurrency         int     `json:"pid_concurrency"`
	HasPIDConcurrency      bool    `json:"-"`
	MappingCoveragePct     float64 `json:"mapping_coverage_pct"`
	HasMappingCoveragePct  bool    `json:"-"`
	MappedProcesses        int     `json:"mapped_processes"`
	HasMappedProcesses     bool    `json:"-"`
	UnmappedProcesses      int     `json:"unmapped_processes"`
	HasUnmappedProcesses   bool    `json:"-"`
	RuntimeSampled         bool    `json:"runtime_sampled"`
}

type trendPointJSON struct {
	At                     string   `json:"at"`
	ActiveBurstConcurrency *int     `json:"active_burst_concurrency,omitempty"`
	SessionConcurrency     *int     `json:"session_concurrency,omitempty"`
	TranscriptSampled      *bool    `json:"transcript_sampled,omitempty"`
	PIDConcurrency         *int     `json:"pid_concurrency,omitempty"`
	MappingCoveragePct     *float64 `json:"mapping_coverage_pct,omitempty"`
	MappedProcesses        *int     `json:"mapped_processes,omitempty"`
	UnmappedProcesses      *int     `json:"unmapped_processes,omitempty"`
	RuntimeSampled         *bool    `json:"runtime_sampled,omitempty"`
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
		payload.RuntimeSampled = jsonValue(true)
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
	Tools                           []ProjectToolSnapshot            `json:"tools,omitempty"`
}

type ProjectToolSnapshot struct {
	Tool             string `json:"tool"`
	SessionCount     int    `json:"session_count"`
	ActiveBurstCount int    `json:"active_burst_count"`
	ProcessCount     int    `json:"process_count"`
}

type AgeBucketSnapshot struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type LiveProcessSnapshot struct {
	PID            int      `json:"pid"`
	Tool           string   `json:"tool"`
	Command        string   `json:"command"`
	HostApp        *HostApp `json:"host_app,omitempty"`
	SessionIDs     []string `json:"session_ids,omitempty"`
	SessionPaths   []string `json:"session_paths,omitempty"`
	MappedSessions int      `json:"mapped_sessions"`
}

type HostApp struct {
	PID        int    `json:"pid"`
	Name       string `json:"name"`
	BundlePath string `json:"bundle_path,omitempty"`
}

type LiveSessionSnapshot struct {
	Tool                         string    `json:"tool"`
	SessionID                    string    `json:"session_id"`
	SessionRole                  string    `json:"session_role"`
	RoleConfidence               string    `json:"role_confidence"`
	RoleReasons                  []string  `json:"role_reasons,omitempty"`
	ThreadSource                 string    `json:"thread_source,omitempty"`
	ParentThreadID               string    `json:"parent_thread_id,omitempty"`
	AgentRole                    string    `json:"agent_role,omitempty"`
	AgentNickname                string    `json:"agent_nickname,omitempty"`
	RoleHintSource               string    `json:"role_hint_source,omitempty"`
	IndependentlyRun             bool      `json:"independently_run,omitempty"`
	Project                      string    `json:"project"`
	Path                         string    `json:"path"`
	ProcessCount                 int       `json:"process_count"`
	HostApps                     []HostApp `json:"host_apps,omitempty"`
	LastEventAt                  string    `json:"last_event_at,omitempty"`
	LastEventAgeSeconds          int       `json:"last_event_age_seconds,omitempty"`
	ActiveBurst                  bool      `json:"active_burst"`
	Freshness                    string    `json:"freshness"`
	MappingMethod                string    `json:"mapping_method"`
	MissingTranscript            bool      `json:"missing_transcript"`
	Confidence                   string    `json:"confidence"`
	ConfidenceReasons            []string  `json:"confidence_reasons,omitempty"`
	ProjectAttributionSource     string    `json:"project_attribution_source"`
	ProjectAttributionConfidence string    `json:"project_attribution_confidence"`
	ProjectAttributionReasons    []string  `json:"project_attribution_reasons,omitempty"`
	Provenance                   []string  `json:"provenance"`
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
	Tool string
	Path string
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
	ForegroundScanLookbackSeconds    int
	ConfiguredHistoryLookbackSeconds int
	Errors                           []string
}

type transcriptCacheState struct {
	Key       string
	ExpiresAt time.Time
	Data      *TranscriptData
}

type LiveProcess struct {
	PID          int
	PPID         int
	Tool         string
	Command      string
	HostApp      *HostApp
	SessionFiles []TranscriptFile
	SessionHints []string
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
