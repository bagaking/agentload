package main

import (
	"agentload/internal/snapshot"
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"agentload/internal/historyfile"
)

const historyRetentionWindow = 30 * 24 * time.Hour

// Compaction rewrites the JSONL store when the file holds materially more
// lines than the retained window: >25% overhead or more than this many excess lines.
const historyCompactionExcessLineLimit = 500

type HistorySample struct {
	At                    string                           `json:"at"`
	Current               snapshot.CurrentMetrics          `json:"current"`
	Summary               snapshot.SnapshotSummary         `json:"summary"`
	CoordinationRisk      HistoryCoordinationRisk          `json:"coordination_risk"`
	Projects              []HistoryProjectSnapshot         `json:"projects,omitempty"`
	RuntimeProcesses      []snapshot.ProcessRuntimeSummary `json:"runtime_process_summary,omitempty"`
	HostAppProcesses      []snapshot.HostAppProcessSummary `json:"host_app_process_summary,omitempty"`
	OutputTokenThroughput *HistoryOutputTokenThroughput    `json:"output_token_throughput,omitempty"`
}

type HistoryOutputTokenThroughput struct {
	OutputTokensPerSecond *float64                              `json:"output_tokens_per_second"`
	State                 string                                `json:"state"`
	WindowSeconds         int                                   `json:"window_seconds"`
	ActiveSessions        int                                   `json:"active_sessions"`
	Projects              []snapshot.LiveTokenRateProjectSample `json:"projects"`
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
	lock, err := historyfile.Acquire(state.path)
	if err != nil {
		return state, err
	}
	defer lock.Release()

	cutoff := now.Add(-historyRetentionWindow)

	// Cold rows live in month partitions beside the hot file. Only partitions
	// that can still hold retained rows are opened, so the read stays bounded
	// however long the archive grows.
	partitions, err := archivePartitionsSince(state.path, cutoff)
	if err != nil {
		return state, err
	}
	archived, err := readArchiveLines(partitions)
	if err != nil {
		return state, err
	}
	for _, line := range archived {
		state.consumeHistoryLine(line, cutoff)
	}

	file, err := os.Open(state.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, err
	}
	defer file.Close()

	hotLines := 0
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		hotLines++
		state.consumeHistoryLine([]byte(line), cutoff)
	}
	if err := scanner.Err(); err != nil {
		return state, err
	}
	if historyFileNeedsCompaction(hotLines, state.hotSampleCount(now)) {
		if err := compactHistorySampleFile(state.path, state.samples, now); err != nil {
			return state, err
		}
	}
	return state, nil
}

// consumeHistoryLine folds one stored line into the retained window. Archived
// and hot lines take the same path, so a row that a crash left in both places is
// merged by timestamp rather than counted twice.
func (s *localHistoryState) consumeHistoryLine(line []byte, cutoff time.Time) {
	var sample HistorySample
	if err := json.Unmarshal(line, &sample); err != nil {
		s.corruptLineCount++
		return
	}
	at, ok := historySampleTime(sample)
	if !ok {
		s.corruptLineCount++
		return
	}
	s.loadedSampleCount++
	if at.Before(cutoff) {
		s.droppedSampleCount++
		return
	}
	s.samples, _ = appendRetainedHistorySample(s.samples, sample, cutoff)
}

// hotSampleCount counts retained samples inside the hot window, which is what
// the hot file holds after compaction.
func (s *localHistoryState) hotSampleCount(now time.Time) int {
	hotCutoff := now.Add(-historyHotWindow)
	count := 0
	for _, sample := range s.samples {
		if at, ok := historySampleTime(sample); ok && !at.Before(hotCutoff) {
			count++
		}
	}
	return count
}

// compactHistorySampleFile archives samples older than the hot window and then
// rewrites the hot file with the remainder.
//
// Order matters: the archive is made durable first, so a crash between the two
// steps leaves a row in both places -- a duplicate that consumeHistoryLine
// merges -- instead of in neither.
func compactHistorySampleFile(path string, samples []HistorySample, now time.Time) error {
	hotCutoff := now.Add(-historyHotWindow)
	cold := make([]archiveRow, 0, len(samples))
	hot := make([]HistorySample, 0, len(samples))
	for _, sample := range samples {
		at, ok := historySampleTime(sample)
		if !ok {
			continue
		}
		if at.Before(hotCutoff) {
			raw, err := json.Marshal(sample)
			if err != nil {
				return err
			}
			cold = append(cold, archiveRow{At: at, Line: raw})
			continue
		}
		hot = append(hot, sample)
	}
	if err := archiveColdRows(path, cold); err != nil {
		return err
	}
	return rewriteHistorySampleFile(path, hot)
}

func historyFileNeedsCompaction(fileLineCount, retainedCount int) bool {
	excess := fileLineCount - retainedCount
	if excess <= 0 {
		return false
	}
	return excess > historyCompactionExcessLineLimit || excess*4 > retainedCount
}

func rewriteHistorySampleFile(path string, samples []HistorySample) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("history file path is empty")
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".compact-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	cleanup := func(err error) error {
		tmp.Close()
		os.Remove(tmpPath)
		return err
	}
	for _, sample := range samples {
		raw, err := json.Marshal(sample)
		if err != nil {
			return cleanup(err)
		}
		if _, err := tmp.Write(append(raw, '\n')); err != nil {
			return cleanup(err)
		}
	}
	if err := tmp.Chmod(0o644); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return err
	}
	return syncDir(dir)
}

func (s *localHistoryState) recordSample(sample HistorySample) error {
	var at time.Time
	sample.At, at = normalizeHistorySampleTimestamp(sample.At, time.Now())
	err := appendHistorySampleFile(s.path, sample)
	s.recordSampleInMemory(sample, at, err)
	return err
}

// recordSampleInMemory merges an already-persisted (or failed-to-persist) sample
// so callers can run the file append outside the snapshot lock.
func (s *localHistoryState) recordSampleInMemory(sample HistorySample, at time.Time, writeErr error) {
	s.loadedSampleCount++
	var dropped int
	s.samples, dropped = appendRetainedHistorySample(s.samples, sample, at.Add(-historyRetentionWindow))
	s.droppedSampleCount += dropped
	if writeErr != nil {
		s.lastWriteError = writeErr.Error()
		return
	}
	s.lastWriteError = ""
}

func (s localHistoryState) snapshotMetadata() snapshot.SnapshotHistory {
	metadata := snapshot.SnapshotHistory{
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

func (s localHistoryState) trendPoints() []snapshot.TrendPoint {
	out := make([]snapshot.TrendPoint, 0, len(s.samples))
	for _, sample := range s.samples {
		at, ok := historySampleTime(sample)
		if !ok {
			continue
		}
		// Realtime trend bucketing still parses RFC3339 second precision.
		out = append(out, snapshot.TrendPoint{
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
			RuntimeProcesses:      append([]snapshot.ProcessRuntimeSummary(nil), sample.RuntimeProcesses...),
			HostAppProcesses:      append([]snapshot.HostAppProcessSummary(nil), sample.HostAppProcesses...),
		})
	}
	return out
}

func historySampleFromSnapshot(snap snapshot.Snapshot) HistorySample {
	sample := HistorySample{
		At:      strings.TrimSpace(snap.GeneratedAt),
		Current: snap.Current,
		Summary: snap.Summary,
		CoordinationRisk: HistoryCoordinationRisk{
			Posture:                        snap.CoordinationRisk.Posture,
			ActiveProjectCount:             snap.CoordinationRisk.ActiveProjectCount,
			RecentProjectCount:             snap.CoordinationRisk.RecentProjectCount,
			TopProject:                     snap.CoordinationRisk.TopProject,
			TopProjectAttentionSharePct:    snap.CoordinationRisk.TopProjectAttentionSharePct,
			CandidateWorkitemCount:         snap.CoordinationRisk.CandidateWorkitemCount,
			CandidateWorkitemCoveragePct:   snap.CoordinationRisk.CandidateWorkitemCoveragePct,
			StaleSessionCount:              snap.CoordinationRisk.StaleSessionCount,
			OrphanProcessCount:             snap.CoordinationRisk.OrphanProcessCount,
			ChurnSessionCount:              snap.CoordinationRisk.ChurnSessionCount,
			ProjectSpreadCount:             snap.CoordinationRisk.ProjectSpreadCount,
			FragmentationPct:               snap.CoordinationRisk.FragmentationPct,
			LoadRatioPct:                   snap.CoordinationRisk.LoadRatioPct,
			LoadPeakValue:                  snap.CoordinationRisk.LoadPeakValue,
			LoadPeakSource:                 snap.CoordinationRisk.LoadPeakSource,
			LoadPeakAt:                     snap.CoordinationRisk.LoadPeakAt,
			DuplicateOverlapSuspicionCount: snap.CoordinationRisk.DuplicateOverlapSuspicionCount,
			DuplicateOverlapClusterCount:   snap.CoordinationRisk.DuplicateOverlapClusterCount,
		},
		Projects:         make([]HistoryProjectSnapshot, 0, len(snap.ProjectFocus)),
		RuntimeProcesses: append([]snapshot.ProcessRuntimeSummary(nil), snap.RuntimeProcesses...),
		HostAppProcesses: append([]snapshot.HostAppProcessSummary(nil), snap.HostAppProcesses...),
	}
	sample.At, _ = normalizeHistorySampleTimestamp(sample.At, time.Now())
	for _, project := range snap.ProjectFocus {
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
	lock, err := historyfile.Acquire(path)
	if err != nil {
		return err
	}
	defer lock.Release()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(append(raw, '\n')); err != nil {
		return err
	}
	return file.Sync()
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

func buildProjectHeatmapWindows(samples []HistorySample, now time.Time) snapshot.ProjectHeatmapSet {
	if now.IsZero() {
		now = time.Now()
	}
	out := snapshot.ProjectHeatmapSet{Windows: make([]snapshot.ProjectHeatmapWindow, 0, len(defaultTrendSpecs))}
	for _, spec := range defaultTrendSpecs {
		from := now.Add(-spec.span)
		filtered := filterHistorySamplesByRange(samples, from, now)
		items, sessionWindowCount := deriveProjectHeatmapItems(filtered)
		out.Windows = append(out.Windows, snapshot.ProjectHeatmapWindow{
			Range:              spec.label,
			From:               from.Format(time.RFC3339),
			To:                 now.Format(time.RFC3339),
			HistoryComplete:    historySamplesCoverRange(samples, from),
			SampleWindowCount:  len(filtered),
			SessionWindowCount: sessionWindowCount,
			Items:              items,
		})
	}
	return out
}

func deriveProjectHeatmapItems(samples []HistorySample) ([]snapshot.ProjectHeatmapItem, int) {
	type aggregate struct {
		snapshot.ProjectHeatmapItem
	}
	aggregates := map[string]*aggregate{}
	totalSessionWindows := 0
	totalProjectWindows := 0
	for _, sample := range samples {
		perWindow := map[string]HistoryProjectSnapshot{}
		for _, project := range sample.Projects {
			name := strings.TrimSpace(project.Project)
			if name == "" {
				continue
			}
			current := perWindow[name]
			current.Project = name
			current.SessionCount += project.SessionCount
			current.ActiveBurstCount += project.ActiveBurstCount
			current.ProcessCount += project.ProcessCount
			perWindow[name] = current
		}
		for name, project := range perWindow {
			entry := aggregates[name]
			if entry == nil {
				entry = &aggregate{ProjectHeatmapItem: snapshot.ProjectHeatmapItem{Project: name}}
				aggregates[name] = entry
			}
			entry.WindowCount++
			entry.SessionWindowCount += project.SessionCount
			entry.ProcessWindowCount += project.ProcessCount
			entry.ActiveWindowCount += project.ActiveBurstCount
			if project.SessionCount > entry.MaxSessionCount {
				entry.MaxSessionCount = project.SessionCount
			}
			if project.ProcessCount > entry.MaxProcessCount {
				entry.MaxProcessCount = project.ProcessCount
			}
			totalSessionWindows += project.SessionCount
			totalProjectWindows++
		}
	}
	out := make([]snapshot.ProjectHeatmapItem, 0, len(aggregates))
	for _, entry := range aggregates {
		if entry.WindowCount > 0 {
			entry.AverageSessions = float64(entry.SessionWindowCount) / float64(entry.WindowCount)
		}
		if totalSessionWindows > 0 {
			entry.SharePct = float64(entry.SessionWindowCount) / float64(totalSessionWindows) * 100
		} else if totalProjectWindows > 0 {
			entry.SharePct = float64(entry.WindowCount) / float64(totalProjectWindows) * 100
		}
		out = append(out, entry.ProjectHeatmapItem)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SessionWindowCount == out[j].SessionWindowCount {
			if out[i].WindowCount == out[j].WindowCount {
				if out[i].ProcessWindowCount == out[j].ProcessWindowCount {
					return out[i].Project < out[j].Project
				}
				return out[i].ProcessWindowCount > out[j].ProcessWindowCount
			}
			return out[i].WindowCount > out[j].WindowCount
		}
		return out[i].SessionWindowCount > out[j].SessionWindowCount
	})
	return out, totalSessionWindows
}

func historySamplesCoverRange(samples []HistorySample, from time.Time) bool {
	if from.IsZero() {
		return false
	}
	earliest := time.Time{}
	for _, sample := range samples {
		at, ok := historySampleTime(sample)
		if !ok {
			continue
		}
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	return !earliest.IsZero() && !earliest.After(from)
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
