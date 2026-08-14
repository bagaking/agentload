package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"agentload/internal/historyfile"
)

const (
	lifecycleLogFileName       = "lifecycle.jsonl"
	lifecycleHeartbeatInterval = time.Minute
)

type lifecycleLog struct {
	path string
	mu   sync.Mutex
}

type lifecycleEvent struct {
	At                     string                    `json:"at"`
	Event                  string                    `json:"event"`
	PID                    int                       `json:"pid"`
	PPID                   int                       `json:"ppid,omitempty"`
	ListenAddr             string                    `json:"listen_addr,omitempty"`
	URL                    string                    `json:"url,omitempty"`
	RefreshIntervalSeconds int                       `json:"refresh_interval_seconds,omitempty"`
	RefreshSlotID          string                    `json:"refresh_slot_id,omitempty"`
	Signal                 string                    `json:"signal,omitempty"`
	Reason                 string                    `json:"reason,omitempty"`
	Error                  string                    `json:"error,omitempty"`
	Stack                  string                    `json:"stack,omitempty"`
	Metrics                *lifecycleSnapshotMetrics `json:"metrics,omitempty"`
	TranscriptStats        *lifecycleTranscriptStats `json:"transcript_stats,omitempty"`
	RuntimeProcesses       []lifecycleRuntimeProcess `json:"runtime_processes,omitempty"`
	HostAppProcesses       []lifecycleHostAppProcess `json:"host_app_processes,omitempty"`
	Extra                  map[string]string         `json:"extra,omitempty"`
}

type lifecycleSnapshotMetrics struct {
	PIDConcurrency         int     `json:"pid_concurrency"`
	SessionConcurrency     int     `json:"session_concurrency"`
	ActiveBurstConcurrency int     `json:"active_burst_concurrency"`
	ActiveSessions         int     `json:"active_sessions"`
	IdleSessions           int     `json:"idle_sessions"`
	MappedProcesses        int     `json:"mapped_processes"`
	UnmappedProcesses      int     `json:"unmapped_processes"`
	ProjectCount           int     `json:"project_count"`
	HotProjectCount        int     `json:"hot_project_count"`
	MappingCoveragePct     float64 `json:"mapping_coverage_pct"`
	TopProject             string  `json:"top_project,omitempty"`
}

type lifecycleTranscriptStats struct {
	ScannedFiles           int  `json:"scanned_files"`
	ParsedFiles            int  `json:"parsed_files"`
	DeferredFiles          int  `json:"deferred_files"`
	TailParsedFiles        int  `json:"tail_parsed_files,omitempty"`
	HistoricalScanDeferred bool `json:"historical_scan_deferred,omitempty"`
	Cached                 bool `json:"cached"`
	ErrorCount             int  `json:"error_count,omitempty"`
}

type lifecycleRuntimeProcess struct {
	Tool              string  `json:"tool"`
	DisplayName       string  `json:"display_name,omitempty"`
	PIDCount          int     `json:"pid_count"`
	CPUPercent        float64 `json:"cpu_percent,omitempty"`
	MemoryBytes       int64   `json:"memory_bytes,omitempty"`
	MappedProcesses   int     `json:"mapped_processes"`
	UnmappedProcesses int     `json:"unmapped_processes"`
	ActiveSessions    int     `json:"active_sessions"`
}

type lifecycleHostAppProcess struct {
	Name              string  `json:"name"`
	PID               int     `json:"pid,omitempty"`
	PIDCount          int     `json:"pid_count"`
	CPUPercent        float64 `json:"cpu_percent,omitempty"`
	MemoryBytes       int64   `json:"memory_bytes,omitempty"`
	MappedProcesses   int     `json:"mapped_processes"`
	UnmappedProcesses int     `json:"unmapped_processes"`
	ActiveSessions    int     `json:"active_sessions"`
}

func newLifecycleLog(historyPath string) *lifecycleLog {
	return &lifecycleLog{path: lifecycleLogPath(historyPath)}
}

func lifecycleLogPath(historyPath string) string {
	historyPath = resolveHistoryFile(historyPath)
	if strings.TrimSpace(historyPath) == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(historyPath), lifecycleLogFileName)
}

func (l *lifecycleLog) record(event lifecycleEvent) error {
	if l == nil || strings.TrimSpace(l.path) == "" {
		return nil
	}
	event.Event = strings.TrimSpace(event.Event)
	if event.Event == "" {
		return nil
	}
	if strings.TrimSpace(event.At) == "" {
		event.At = time.Now().Format(time.RFC3339Nano)
	}
	if event.PID == 0 {
		event.PID = os.Getpid()
	}
	if event.PPID == 0 {
		event.PPID = os.Getppid()
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	lock, err := historyfile.Acquire(l.path)
	if err != nil {
		return err
	}
	defer lock.Release()

	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(append(raw, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func (l *lifecycleLog) recordPanic() {
	if recovered := recover(); recovered != nil {
		_ = l.record(lifecycleEvent{
			Event: "panic",
			Error: fmt.Sprint(recovered),
			Stack: string(debug.Stack()),
		})
		panic(recovered)
	}
}

func lifecycleEventFromSnapshot(event, reason string, snapshot Snapshot) lifecycleEvent {
	return lifecycleEvent{
		Event:            event,
		RefreshSlotID:    snapshot.RefreshSlotID,
		Reason:           reason,
		Metrics:          lifecycleMetricsFromSnapshot(snapshot),
		TranscriptStats:  lifecycleTranscriptStatsFromSnapshot(snapshot.TranscriptStats),
		RuntimeProcesses: lifecycleRuntimeProcesses(snapshot.RuntimeProcesses),
		HostAppProcesses: lifecycleHostAppProcesses(snapshot.HostAppProcesses),
	}
}

func lifecycleMetricsFromSnapshot(snapshot Snapshot) *lifecycleSnapshotMetrics {
	return &lifecycleSnapshotMetrics{
		PIDConcurrency:         snapshot.Current.PIDConcurrency,
		SessionConcurrency:     snapshot.Current.SessionConcurrency,
		ActiveBurstConcurrency: snapshot.Current.ActiveBurstConcurrency,
		ActiveSessions:         snapshot.Summary.ActiveSessions,
		IdleSessions:           snapshot.Summary.IdleSessions,
		MappedProcesses:        snapshot.Summary.MappedProcesses,
		UnmappedProcesses:      snapshot.Summary.UnmappedProcesses,
		ProjectCount:           snapshot.Summary.ProjectCount,
		HotProjectCount:        snapshot.Summary.HotProjectCount,
		MappingCoveragePct:     snapshot.Summary.MappingCoveragePct,
		TopProject:             snapshot.CoordinationRisk.TopProject,
	}
}

func lifecycleTranscriptStatsFromSnapshot(stats TranscriptStats) *lifecycleTranscriptStats {
	return &lifecycleTranscriptStats{
		ScannedFiles:           stats.ScannedFiles,
		ParsedFiles:            stats.ParsedFiles,
		DeferredFiles:          stats.DeferredFiles,
		TailParsedFiles:        stats.TailParsedFiles,
		HistoricalScanDeferred: stats.HistoricalScanDeferred,
		Cached:                 stats.Cached,
		ErrorCount:             len(stats.Errors),
	}
}

func lifecycleRuntimeProcesses(processes []ProcessRuntimeSummary) []lifecycleRuntimeProcess {
	out := make([]lifecycleRuntimeProcess, 0, len(processes))
	for _, process := range processes {
		out = append(out, lifecycleRuntimeProcess{
			Tool:              firstNonEmptyString(process.Tool, process.Key),
			DisplayName:       process.DisplayName,
			PIDCount:          process.PIDCount,
			CPUPercent:        process.CPUPercent,
			MemoryBytes:       process.MemoryBytes,
			MappedProcesses:   process.MappedProcesses,
			UnmappedProcesses: process.UnmappedProcesses,
			ActiveSessions:    process.ActiveSessions,
		})
	}
	return out
}

func lifecycleHostAppProcesses(processes []HostAppProcessSummary) []lifecycleHostAppProcess {
	out := make([]lifecycleHostAppProcess, 0, len(processes))
	for _, process := range processes {
		out = append(out, lifecycleHostAppProcess{
			Name:              process.Name,
			PID:               process.PID,
			PIDCount:          process.PIDCount,
			CPUPercent:        process.CPUPercent,
			MemoryBytes:       process.MemoryBytes,
			MappedProcesses:   process.MappedProcesses,
			UnmappedProcesses: process.UnmappedProcesses,
			ActiveSessions:    process.ActiveSessions,
		})
	}
	return out
}

func (l *lifecycleLog) startHeartbeat(stop <-chan struct{}, interval time.Duration) {
	if l == nil {
		return
	}
	if interval <= 0 {
		interval = lifecycleHeartbeatInterval
	}
	go func() {
		defer recoverBackgroundPanic("lifecycle heartbeat")
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_ = l.record(lifecycleEvent{Event: "heartbeat"})
			case <-stop:
				return
			}
		}
	}()
}
