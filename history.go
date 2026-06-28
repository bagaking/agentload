package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const historyRetentionWindow = 30 * 24 * time.Hour

type HistorySample struct {
	At               string                   `json:"at"`
	Current          CurrentMetrics           `json:"current"`
	Summary          SnapshotSummary          `json:"summary"`
	CoordinationRisk HistoryCoordinationRisk  `json:"coordination_risk"`
	Projects         []HistoryProjectSnapshot `json:"projects,omitempty"`
}

type HistoryProjectSnapshot struct {
	Project             string  `json:"project"`
	SessionCount        int     `json:"session_count"`
	ActiveBurstCount    int     `json:"active_burst_count"`
	MainAgentSessions   int     `json:"main_agent_sessions,omitempty"`
	SubagentSessions    int     `json:"subagent_sessions,omitempty"`
	UnknownRoleSessions int     `json:"unknown_role_sessions,omitempty"`
	ProcessCount        int     `json:"process_count"`
	AttentionSharePct   float64 `json:"attention_share_pct"`
}

type HistoryCoordinationRisk struct {
	Posture                        string  `json:"posture"`
	ActiveProjectCount             int     `json:"active_project_count"`
	RecentProjectCount             int     `json:"recent_project_count"`
	TopProject                     string  `json:"top_project"`
	TopProjectAttentionSharePct    float64 `json:"top_project_attention_share_pct"`
	CandidateWorkitemCount         int     `json:"candidate_workitem_count"`
	CandidateWorkitemCoveragePct   float64 `json:"candidate_workitem_coverage_pct"`
	StaleSessionCount              int     `json:"stale_session_count"`
	OrphanProcessCount             int     `json:"orphan_process_count"`
	ChurnSessionCount              int     `json:"churn_session_count"`
	ProjectSpreadCount             int     `json:"project_spread_count"`
	FragmentationPct               float64 `json:"fragmentation_pct"`
	LoadRatioPct                   float64 `json:"load_ratio_pct"`
	LoadPeakValue                  int     `json:"load_peak_value"`
	LoadPeakSource                 string  `json:"load_peak_source,omitempty"`
	LoadPeakAt                     string  `json:"load_peak_at,omitempty"`
	DuplicateOverlapSuspicionCount int     `json:"duplicate_overlap_suspicion_count"`
	DuplicateOverlapClusterCount   int     `json:"duplicate_overlap_cluster_count"`
}

type HistoryProjectAllocation struct {
	Project                  string  `json:"project"`
	SampleCount              int     `json:"sample_count"`
	AverageAttentionSharePct float64 `json:"average_attention_share_pct"`
	PeakAttentionSharePct    float64 `json:"peak_attention_share_pct"`
	MaxSessionCount          int     `json:"max_session_count"`
	MaxActiveBurstCount      int     `json:"max_active_burst_count"`
	MaxProcessCount          int     `json:"max_process_count"`
}

type HistoryGrowth struct {
	SampleCount             int    `json:"sample_count"`
	From                    string `json:"from,omitempty"`
	To                      string `json:"to,omitempty"`
	PIDConcurrencyStart     int    `json:"pid_concurrency_start"`
	PIDConcurrencyEnd       int    `json:"pid_concurrency_end"`
	PIDConcurrencyDelta     int    `json:"pid_concurrency_delta"`
	SessionConcurrencyStart int    `json:"session_concurrency_start"`
	SessionConcurrencyEnd   int    `json:"session_concurrency_end"`
	SessionConcurrencyDelta int    `json:"session_concurrency_delta"`
	ActiveBurstStart        int    `json:"active_burst_start"`
	ActiveBurstEnd          int    `json:"active_burst_end"`
	ActiveBurstDelta        int    `json:"active_burst_delta"`
}

type HistoryRuntimePeaks struct {
	PIDConcurrency         PeakPoint `json:"pid_concurrency"`
	SessionConcurrency     PeakPoint `json:"session_concurrency"`
	ActiveBurstConcurrency PeakPoint `json:"active_burst_concurrency"`
	MappedProcesses        PeakPoint `json:"mapped_processes"`
	UnmappedProcesses      PeakPoint `json:"unmapped_processes"`
}

type localHistoryState struct {
	path               string
	samples            []HistorySample
	loadedSampleCount  int
	droppedSampleCount int
	corruptLineCount   int
	lastWriteError     string
}

func loadLocalHistoryState(path string, now time.Time) (localHistoryState, error) {
	state := localHistoryState{
		path: resolveHistoryFile(path),
	}
	if strings.TrimSpace(state.path) == "" {
		return state, nil
	}

	file, err := os.Open(state.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	defer file.Close()

	cutoff := now.Add(-historyRetentionWindow)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var sample HistorySample
		if err := json.Unmarshal([]byte(line), &sample); err != nil {
			state.corruptLineCount++
			continue
		}
		at, ok := historySampleTime(sample)
		if !ok {
			state.corruptLineCount++
			continue
		}
		state.loadedSampleCount++
		if at.Before(cutoff) {
			state.droppedSampleCount++
			continue
		}
		state.samples, _ = appendRetainedHistorySample(state.samples, sample, cutoff)
	}
	if err := scanner.Err(); err != nil {
		return state, err
	}
	return state, nil
}

func (s *localHistoryState) recordSample(sample HistorySample) error {
	var at time.Time
	sample.At, at = normalizeHistorySampleTimestamp(sample.At, time.Now())
	s.loadedSampleCount++
	var dropped int
	s.samples, dropped = appendRetainedHistorySample(s.samples, sample, at.Add(-historyRetentionWindow))
	s.droppedSampleCount += dropped
	if err := appendHistorySampleFile(s.path, sample); err != nil {
		s.lastWriteError = err.Error()
		return err
	}
	s.lastWriteError = ""
	return nil
}

func (s localHistoryState) snapshotMetadata() SnapshotHistory {
	metadata := SnapshotHistory{
		StorePath:           s.path,
		LoadedSampleCount:   s.loadedSampleCount,
		RetainedSampleCount: len(s.samples),
		DroppedSampleCount:  s.droppedSampleCount,
		CorruptLineCount:    s.corruptLineCount,
		LastWriteError:      s.lastWriteError,
	}
	if len(s.samples) > 0 {
		metadata.FirstSampleAt = s.samples[0].At
		metadata.LastSampleAt = s.samples[len(s.samples)-1].At
	}
	return metadata
}

func (s localHistoryState) trendPoints() []TrendPoint {
	out := make([]TrendPoint, 0, len(s.samples))
	for _, sample := range s.samples {
		at, ok := historySampleTime(sample)
		if !ok {
			continue
		}
		// Realtime trend bucketing still parses RFC3339 second precision.
		out = append(out, TrendPoint{
			At:                    at.Format(time.RFC3339),
			PIDConcurrency:        sample.Current.PIDConcurrency,
			HasPIDConcurrency:     true,
			MappingCoveragePct:    sample.Summary.MappingCoveragePct,
			HasMappingCoveragePct: true,
			MappedProcesses:       sample.Summary.MappedProcesses,
			HasMappedProcesses:    true,
			UnmappedProcesses:     sample.Summary.UnmappedProcesses,
			HasUnmappedProcesses:  true,
			RuntimeSampled:        true,
		})
	}
	return out
}

func historySampleFromSnapshot(snapshot Snapshot) HistorySample {
	sample := HistorySample{
		At:      strings.TrimSpace(snapshot.GeneratedAt),
		Current: snapshot.Current,
		Summary: snapshot.Summary,
		CoordinationRisk: HistoryCoordinationRisk{
			Posture:                        snapshot.CoordinationRisk.Posture,
			ActiveProjectCount:             snapshot.CoordinationRisk.ActiveProjectCount,
			RecentProjectCount:             snapshot.CoordinationRisk.RecentProjectCount,
			TopProject:                     snapshot.CoordinationRisk.TopProject,
			TopProjectAttentionSharePct:    snapshot.CoordinationRisk.TopProjectAttentionSharePct,
			CandidateWorkitemCount:         snapshot.CoordinationRisk.CandidateWorkitemCount,
			CandidateWorkitemCoveragePct:   snapshot.CoordinationRisk.CandidateWorkitemCoveragePct,
			StaleSessionCount:              snapshot.CoordinationRisk.StaleSessionCount,
			OrphanProcessCount:             snapshot.CoordinationRisk.OrphanProcessCount,
			ChurnSessionCount:              snapshot.CoordinationRisk.ChurnSessionCount,
			ProjectSpreadCount:             snapshot.CoordinationRisk.ProjectSpreadCount,
			FragmentationPct:               snapshot.CoordinationRisk.FragmentationPct,
			LoadRatioPct:                   snapshot.CoordinationRisk.LoadRatioPct,
			LoadPeakValue:                  snapshot.CoordinationRisk.LoadPeakValue,
			LoadPeakSource:                 snapshot.CoordinationRisk.LoadPeakSource,
			LoadPeakAt:                     snapshot.CoordinationRisk.LoadPeakAt,
			DuplicateOverlapSuspicionCount: snapshot.CoordinationRisk.DuplicateOverlapSuspicionCount,
			DuplicateOverlapClusterCount:   snapshot.CoordinationRisk.DuplicateOverlapClusterCount,
		},
		Projects: make([]HistoryProjectSnapshot, 0, len(snapshot.ProjectFocus)),
	}
	sample.At, _ = normalizeHistorySampleTimestamp(sample.At, time.Now())
	for _, project := range snapshot.ProjectFocus {
		sample.Projects = append(sample.Projects, HistoryProjectSnapshot{
			Project:             project.Project,
			SessionCount:        project.SessionCount,
			ActiveBurstCount:    project.ActiveBurstCount,
			MainAgentSessions:   project.MainAgentSessions,
			SubagentSessions:    project.SubagentSessions,
			UnknownRoleSessions: project.UnknownRoleSessions,
			ProcessCount:        project.ProcessCount,
			AttentionSharePct:   project.AttentionSharePct,
		})
	}
	return sample
}

func appendHistorySampleFile(path string, sample HistorySample) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("history file path is empty")
	}
	raw, err := json.Marshal(sample)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}

func appendRetainedHistorySample(samples []HistorySample, sample HistorySample, cutoff time.Time) ([]HistorySample, int) {
	out := samples[:0]
	dropped := 0
	for _, existing := range samples {
		at, ok := historySampleTime(existing)
		if !ok {
			continue
		}
		if !cutoff.IsZero() && at.Before(cutoff) {
			dropped++
			continue
		}
		out = append(out, existing)
	}
	replaced := false
	for i := range out {
		if out[i].At == sample.At {
			out[i] = sample
			replaced = true
			break
		}
	}
	if !replaced {
		out = append(out, sample)
	}
	sortHistorySamples(out)
	return out, dropped
}

func filterHistorySamplesByRange(samples []HistorySample, from, to time.Time) []HistorySample {
	if !from.IsZero() && !to.IsZero() && to.Before(from) {
		return nil
	}
	out := make([]HistorySample, 0, len(samples))
	for _, sample := range samples {
		at, ok := historySampleTime(sample)
		if !ok {
			continue
		}
		if !from.IsZero() && at.Before(from) {
			continue
		}
		if !to.IsZero() && at.After(to) {
			continue
		}
		out = append(out, sample)
	}
	sortHistorySamples(out)
	return out
}

func deriveProjectAllocation(samples []HistorySample, from, to time.Time) []HistoryProjectAllocation {
	filtered := filterHistorySamplesByRange(samples, from, to)
	type aggregate struct {
		HistoryProjectAllocation
		attentionTotal float64
	}
	aggregates := map[string]*aggregate{}
	for _, sample := range filtered {
		for _, project := range sample.Projects {
			name := strings.TrimSpace(project.Project)
			if name == "" {
				continue
			}
			entry := aggregates[name]
			if entry == nil {
				entry = &aggregate{HistoryProjectAllocation: HistoryProjectAllocation{Project: name}}
				aggregates[name] = entry
			}
			entry.SampleCount++
			entry.attentionTotal += project.AttentionSharePct
			if project.AttentionSharePct > entry.PeakAttentionSharePct {
				entry.PeakAttentionSharePct = project.AttentionSharePct
			}
			if project.SessionCount > entry.MaxSessionCount {
				entry.MaxSessionCount = project.SessionCount
			}
			if project.ActiveBurstCount > entry.MaxActiveBurstCount {
				entry.MaxActiveBurstCount = project.ActiveBurstCount
			}
			if project.ProcessCount > entry.MaxProcessCount {
				entry.MaxProcessCount = project.ProcessCount
			}
		}
	}
	out := make([]HistoryProjectAllocation, 0, len(aggregates))
	for _, entry := range aggregates {
		if entry.SampleCount > 0 {
			entry.AverageAttentionSharePct = entry.attentionTotal / float64(entry.SampleCount)
		}
		out = append(out, entry.HistoryProjectAllocation)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].AverageAttentionSharePct == out[j].AverageAttentionSharePct {
			if out[i].PeakAttentionSharePct == out[j].PeakAttentionSharePct {
				return out[i].Project < out[j].Project
			}
			return out[i].PeakAttentionSharePct > out[j].PeakAttentionSharePct
		}
		return out[i].AverageAttentionSharePct > out[j].AverageAttentionSharePct
	})
	return out
}

func deriveSessionRuntimeGrowth(samples []HistorySample, from, to time.Time) HistoryGrowth {
	filtered := filterHistorySamplesByRange(samples, from, to)
	if len(filtered) == 0 {
		return HistoryGrowth{}
	}
	first := filtered[0]
	last := filtered[len(filtered)-1]
	return HistoryGrowth{
		SampleCount:             len(filtered),
		From:                    first.At,
		To:                      last.At,
		PIDConcurrencyStart:     first.Current.PIDConcurrency,
		PIDConcurrencyEnd:       last.Current.PIDConcurrency,
		PIDConcurrencyDelta:     last.Current.PIDConcurrency - first.Current.PIDConcurrency,
		SessionConcurrencyStart: first.Current.SessionConcurrency,
		SessionConcurrencyEnd:   last.Current.SessionConcurrency,
		SessionConcurrencyDelta: last.Current.SessionConcurrency - first.Current.SessionConcurrency,
		ActiveBurstStart:        first.Current.ActiveBurstConcurrency,
		ActiveBurstEnd:          last.Current.ActiveBurstConcurrency,
		ActiveBurstDelta:        last.Current.ActiveBurstConcurrency - first.Current.ActiveBurstConcurrency,
	}
}

func reconstructRuntimePeaks(samples []HistorySample, from, to time.Time) HistoryRuntimePeaks {
	filtered := filterHistorySamplesByRange(samples, from, to)
	peaks := HistoryRuntimePeaks{}
	for _, sample := range filtered {
		updatePeakPoint(&peaks.PIDConcurrency, sample.Current.PIDConcurrency, sample.At)
		updatePeakPoint(&peaks.SessionConcurrency, sample.Current.SessionConcurrency, sample.At)
		updatePeakPoint(&peaks.ActiveBurstConcurrency, sample.Current.ActiveBurstConcurrency, sample.At)
		updatePeakPoint(&peaks.MappedProcesses, sample.Summary.MappedProcesses, sample.At)
		updatePeakPoint(&peaks.UnmappedProcesses, sample.Summary.UnmappedProcesses, sample.At)
	}
	return peaks
}

func updatePeakPoint(peak *PeakPoint, value int, at string) {
	if peak.At == "" || value > peak.Value {
		peak.Value = value
		peak.At = at
	}
}

func normalizeHistorySampleTimestamp(raw string, fallback time.Time) (string, time.Time) {
	if ts, ok := parseObservedTime(raw); ok {
		return ts.Format(time.RFC3339Nano), ts
	}
	if fallback.IsZero() {
		fallback = time.Now()
	}
	return fallback.Format(time.RFC3339Nano), fallback
}

func historySampleTime(sample HistorySample) (time.Time, bool) {
	return parseObservedTime(sample.At)
}

func parseObservedTime(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts, true
		}
	}
	return time.Time{}, false
}

func sortHistorySamples(samples []HistorySample) {
	sort.Slice(samples, func(i, j int) bool {
		ti, okI := historySampleTime(samples[i])
		tj, okJ := historySampleTime(samples[j])
		switch {
		case okI && okJ:
			return ti.Before(tj)
		case okI:
			return true
		case okJ:
			return false
		default:
			return samples[i].At < samples[j].At
		}
	})
}
