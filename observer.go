package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

func (o *Observer) Snapshot(ctx context.Context) Snapshot {
	scanStart := time.Now()
	processes, processNotes := discoverLiveProcessesFunc(ctx, o.adapters)
	now := time.Now()
	extraRoots, priority := rootsFromLiveProcesses(processes, o.adapters)
	o.adapters.mergeRoots(extraRoots)
	roots := o.adapters.roots()
	data, cached := o.transcriptData(ctx, priority, scanStart)
	liveSessions, sessionNotes := buildLiveSessionsAt(processes, data, o.cfg.IdleGap, now)

	currentByTool := make(map[string]ToolMetrics)
	for _, agentID := range o.adapters.transcriptAgentIDs() {
		currentByTool[agentID] = ToolMetrics{}
	}
	current := CurrentMetrics{
		PIDConcurrency: len(processes),
	}
	for _, process := range processes {
		metrics := currentByTool[process.Tool]
		metrics.PIDConcurrency++
		currentByTool[process.Tool] = metrics
	}
	for _, session := range liveSessions {
		observation := observeLiveSession(session, o.cfg.IdleGap, now)
		facts := metricFactsForLiveSession(session, observation)
		current.SessionConcurrency++
		metrics := currentByTool[session.Tool]
		metrics.SessionConcurrency++
		if facts.RecentMovement {
			current.ActiveBurstConcurrency++
			metrics.ActiveBurstConcurrency++
		}
		currentByTool[session.Tool] = metrics
	}

	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	sevenDayStart := now.Add(-7 * 24 * time.Hour)
	historicPeaks := HistoricPeaks{
		Today: PeakWindow{
			SessionConcurrency:     peakConcurrency(data.SessionSpans, todayStart, now),
			ActiveBurstConcurrency: peakConcurrency(data.BurstSpans, todayStart, now),
		},
		SevenDay: PeakWindow{
			SessionConcurrency:     peakConcurrency(data.SessionSpans, sevenDayStart, now),
			ActiveBurstConcurrency: peakConcurrency(data.BurstSpans, sevenDayStart, now),
		},
	}
	liveSessionSnapshots := projectLiveSessions(liveSessions, o.cfg.IdleGap, now)
	liveProcessSnapshots := projectLiveProcessesWithSessions(processes, liveSessions, liveSessionSnapshots, data)
	liveSessionSnapshots = attachProcessResourcesToSessions(liveSessionSnapshots, liveProcessSnapshots)
	projectFocus := buildProjectFocus(liveSessions, o.cfg.IdleGap, now)
	candidateWorkitems := buildCandidateWorkitems(liveSessionSnapshots, sessionProcessIDsByKey(liveSessions))
	snapshot := Snapshot{
		GeneratedAt:      now.Format(time.RFC3339Nano),
		Config:           o.snapshotConfig(roots),
		Current:          current,
		CurrentByTool:    currentByTool,
		HistoricPeaks:    historicPeaks,
		MetricRegistry:   defaultMetricRegistry(),
		RuntimeTelemetry: defaultRuntimeTelemetrySnapshot(),
		Trends:           buildTranscriptTrendWindows(data, now, o.cfg.Lookback),
		TranscriptStats: TranscriptStats{
			ScannedFiles:                     data.ScannedFiles,
			ParsedFiles:                      data.ParsedFiles,
			DeferredFiles:                    data.DeferredFiles,
			TailParsedFiles:                  data.TailParsedFiles,
			HistoricalScanDeferred:           data.HistoricalScanDeferred,
			ForegroundScanLookbackSeconds:    data.ForegroundScanLookbackSeconds,
			ConfiguredHistoryLookbackSeconds: data.ConfiguredHistoryLookbackSeconds,
			Cached:                           cached,
			Errors:                           append([]string(nil), data.Errors...),
		},
		ProjectFocus:       projectFocus,
		CandidateWorkitems: candidateWorkitems,
		AgeBuckets:         buildAgeBuckets(liveSessions, o.cfg.IdleGap, now),
		SystemResources:    sampleSystemResources(),
		LiveProcesses:      liveProcessSnapshots,
		LiveSessions:       liveSessionSnapshots,
		RuntimeProcesses:   buildRuntimeProcessSummary(liveProcessSnapshots),
		HostAppProcesses:   buildHostAppProcessSummary(liveProcessSnapshots),
		LiveTokenRateFiles: append([]TranscriptFile(nil), priority...),
		LiveTokenProjects:  liveTokenRateProjectsFromSessions(liveSessionSnapshots),
	}
	snapshot.Summary = buildSnapshotSummary(snapshot.LiveProcesses, snapshot.LiveSessions, snapshot.ProjectFocus)
	snapshot.CoordinationRisk = buildCoordinationRisk(
		snapshot.LiveProcesses,
		snapshot.LiveSessions,
		snapshot.ProjectFocus,
		snapshot.CandidateWorkitems,
		snapshot.Current,
		historicPeaks,
		now,
		o.cfg.IdleGap,
	)
	snapshot.Diagnostics = buildDiagnosticsSnapshot(snapshot, now)

	snapshot.Notes = buildSnapshotNotes(snapshot, processNotes, sessionNotes)
	return snapshot
}

func buildSnapshotNotes(snapshot Snapshot, processNotes, sessionNotes []string) []string {
	notes := append([]string{}, processNotes...)
	notes = append(notes, sessionNotes...)
	if len(snapshot.TranscriptStats.Errors) > 0 {
		notes = append(notes, "Some transcript files could not be parsed; see transcript_stats.errors.")
	}
	if snapshot.TranscriptStats.DeferredFiles > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d older transcript files were deferred from the foreground snapshot; live process files and transcripts with recent local activity are still included.",
			snapshot.TranscriptStats.DeferredFiles,
		))
	}
	if snapshot.TranscriptStats.HistoricalScanDeferred {
		notes = append(notes, "Full historical transcript parsing was deferred from the foreground snapshot; live process files and foreground-window transcripts are still included.")
	}
	return uniqueSortedStrings(notes)
}

func (o *Observer) snapshotConfig(roots map[string][]string) SnapshotConfig {
	snapshotConfig := o.cfg.snapshotConfig()
	snapshotConfig.ClaudeRoots = append([]string(nil), roots["claude"]...)
	snapshotConfig.CodexRoots = append([]string(nil), roots["codex"]...)
	snapshotConfig.TraeRoots = append([]string(nil), roots["trae"]...)
	snapshotConfig.AntigravityRoots = append([]string(nil), roots["antigravity"]...)
	return snapshotConfig
}

func rootsFromLiveProcesses(processes []LiveProcess, adapters *codingAgentRegistry) (map[string][]string, []TranscriptFile) {
	rootSets := map[string]map[string]struct{}{}
	prioritySet := map[string]TranscriptFile{}
	addRoot := func(tool, root string) {
		root = strings.TrimSpace(root)
		if root == "" || !adapters.hasDiscovery(tool) {
			return
		}
		if rootSets[tool] == nil {
			rootSets[tool] = map[string]struct{}{}
		}
		rootSets[tool][root] = struct{}{}
	}
	for _, process := range processes {
		for _, file := range process.SessionFiles {
			prioritySet[file.Tool+"\x00"+file.Path] = file
			addRoot(file.Tool, adapters.rootFromTranscriptFile(file))
		}
		for _, root := range adapters.rootsFromCommand(process.Tool, process.Command) {
			addRoot(process.Tool, root)
		}
	}
	roots := map[string][]string{}
	for tool, set := range rootSets {
		for root := range set {
			roots[tool] = append(roots[tool], root)
		}
		sort.Strings(roots[tool])
	}
	priority := make([]TranscriptFile, 0, len(prioritySet))
	for _, file := range prioritySet {
		priority = append(priority, file)
	}
	sort.Slice(priority, func(i, j int) bool {
		if priority[i].Tool == priority[j].Tool {
			return priority[i].Path < priority[j].Path
		}
		return priority[i].Tool < priority[j].Tool
	})
	return roots, priority
}

type normalizedProcessSession struct {
	Key       string
	Tool      string
	SessionID string
	Path      string
	Trace     *SessionTrace
	Mapping   LiveSessionMapping
}

func buildTracesByID(data *TranscriptData) map[string]*SessionTrace {
	out := map[string]*SessionTrace{}
	if data == nil {
		return out
	}
	for _, trace := range data.Traces {
		if trace == nil || strings.TrimSpace(trace.SessionID) == "" {
			continue
		}
		key := liveSessionKeyForID(trace.Tool, trace.SessionID)
		if _, ok := out[key]; !ok {
			out[key] = trace
		}
	}
	return out
}

func normalizeProcessSessionMappings(process LiveProcess, data *TranscriptData, tracesByID map[string]*SessionTrace) ([]normalizedProcessSession, []string) {
	traces := map[string]*SessionTrace{}
	if data != nil && data.Traces != nil {
		traces = data.Traces
	}
	hints := uniqueSortedStrings(process.SessionHints)
	hintSet := map[string]struct{}{}
	for _, hint := range hints {
		hintSet[hint] = struct{}{}
	}

	candidates := map[string]*normalizedProcessSession{}
	parsedKeys := map[string]struct{}{}
	parsedSessionKeys := map[string]struct{}{}
	notes := []string{}
	addCandidate := func(key, tool, sessionID, path string, trace *SessionTrace, mapping LiveSessionMapping) *normalizedProcessSession {
		if key == "" {
			switch {
			case sessionID != "":
				key = liveSessionKeyForID(tool, sessionID)
			case tool != "" && path != "":
				key = liveSessionKeyForPath(TranscriptFile{Tool: tool, Path: path})
			default:
				return nil
			}
		}
		candidate := candidates[key]
		if candidate == nil {
			candidate = &normalizedProcessSession{
				Key:       key,
				Tool:      tool,
				SessionID: sessionID,
				Path:      path,
				Trace:     trace,
				Mapping:   mapping,
			}
			if candidate.Path == "" && trace != nil && trace.Path != "" {
				candidate.Path = trace.Path
			}
			candidates[key] = candidate
			return candidate
		}
		if candidate.Tool == "" {
			candidate.Tool = tool
		}
		if candidate.SessionID == "" {
			candidate.SessionID = sessionID
		}
		if candidate.Path == "" {
			switch {
			case path != "":
				candidate.Path = path
			case trace != nil && trace.Path != "":
				candidate.Path = trace.Path
			}
		}
		if candidate.Trace == nil {
			candidate.Trace = trace
		}
		mergeLiveSessionMapping(&candidate.Mapping, mapping)
		return candidate
	}

	// Parsed transcript ids are the strongest evidence for a PID. Collect them
	// first so weaker per-file hints/fallbacks never manufacture sibling sessions.
	for _, file := range process.SessionFiles {
		trace := traces[file.Path]
		if trace == nil {
			continue
		}
		sessionID := strings.TrimSpace(trace.SessionID)
		if sessionID == "" {
			continue
		}
		parsedSessionKeys[liveSessionKeyForID(file.Tool, sessionID)] = struct{}{}
	}

	for _, file := range process.SessionFiles {
		trace := traces[file.Path]
		fallback := strings.TrimSpace(file.SessionIDHint)
		mapping := LiveSessionMapping{TranscriptPath: true}
		sessionID := ""
		key := liveSessionKeyForPath(file)
		skipCandidate := false
		switch {
		case trace != nil && strings.TrimSpace(trace.SessionID) != "":
			sessionID = strings.TrimSpace(trace.SessionID)
			key = liveSessionKeyForID(file.Tool, sessionID)
			mapping.ParsedTranscriptID = true
		case len(parsedSessionKeys) > 0:
			if fallback == "" {
				skipCandidate = true
				break
			}
			key = liveSessionKeyForID(file.Tool, fallback)
			if _, ok := parsedSessionKeys[key]; !ok {
				skipCandidate = true
				break
			}
			sessionID = fallback
			mapping.FallbackSessionID = true
		case len(hints) == 1:
			sessionID = hints[0]
			key = liveSessionKeyForID(file.Tool, sessionID)
			mapping.CommandHint = true
			if fallback != "" {
				if fallback == sessionID {
					mapping.FallbackSessionID = true
				} else {
					notes = append(notes, fmt.Sprintf("PID %d %s ignored weaker filename-derived fallback session id %q from %s in favor of command hint %q.", process.PID, process.Tool, fallback, file.Path, sessionID))
				}
			}
		case fallback != "":
			sessionID = fallback
			if _, ok := hintSet[fallback]; ok {
				key = liveSessionKeyForID(file.Tool, sessionID)
				mapping.CommandHint = true
			}
			mapping.FallbackSessionID = true
		}
		if skipCandidate {
			continue
		}
		candidate := addCandidate(key, file.Tool, sessionID, file.Path, trace, mapping)
		if candidate != nil && mapping.ParsedTranscriptID {
			parsedKeys[key] = struct{}{}
		}
	}

	switch {
	case len(process.SessionFiles) == 0:
		for _, hint := range hints {
			key := liveSessionKeyForID(process.Tool, hint)
			trace := tracesByID[key]
			candidate := addCandidate(key, process.Tool, hint, "", trace, LiveSessionMapping{CommandHint: true})
			if candidate != nil && candidate.Trace == nil {
				candidate.Trace = trace
			}
		}
	case len(parsedKeys) > 0:
		parsedIDs := normalizedProcessSessionIDs(candidates, parsedKeys)
		for _, hint := range hints {
			key := liveSessionKeyForID(process.Tool, hint)
			if candidate := candidates[key]; candidate != nil {
				candidate.Mapping.CommandHint = true
				if candidate.Trace == nil {
					candidate.Trace = tracesByID[key]
				}
				continue
			}
			if len(parsedIDs) == 1 {
				notes = append(notes, fmt.Sprintf("PID %d %s ignored weaker command hint %q because parsed transcript session id %q won.", process.PID, process.Tool, hint, parsedIDs[0]))
				continue
			}
			if len(parsedIDs) > 1 {
				notes = append(notes, fmt.Sprintf("PID %d %s ignored weaker command hint %q because parsed transcript session ids %s won.", process.PID, process.Tool, hint, quotedSessionIDs(parsedIDs)))
			}
		}
	default:
		for _, hint := range hints {
			key := liveSessionKeyForID(process.Tool, hint)
			if candidate := candidates[key]; candidate != nil {
				candidate.Mapping.CommandHint = true
				if candidate.Trace == nil {
					candidate.Trace = tracesByID[key]
				}
			}
		}
	}

	out := make([]normalizedProcessSession, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Path == "" && candidate.Trace != nil && candidate.Trace.Path != "" {
			candidate.Path = candidate.Trace.Path
		}
		out = append(out, *candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool == out[j].Tool {
			if out[i].SessionID == out[j].SessionID {
				return out[i].Path < out[j].Path
			}
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].Tool < out[j].Tool
	})
	return out, uniqueSortedStrings(notes)
}

func normalizedProcessSessionIDs(candidates map[string]*normalizedProcessSession, keys map[string]struct{}) []string {
	out := []string{}
	seen := map[string]struct{}{}
	for key := range keys {
		candidate := candidates[key]
		if candidate == nil {
			continue
		}
		sessionID := strings.TrimSpace(candidate.SessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		out = append(out, sessionID)
	}
	sort.Strings(out)
	return out
}

func quotedSessionIDs(ids []string) string {
	quoted := make([]string, 0, len(ids))
	for _, id := range ids {
		quoted = append(quoted, strconv.Quote(id))
	}
	return strings.Join(quoted, ", ")
}

func configRootFromPath(path, dirName string) string {
	path = filepath.Clean(path)
	parts := strings.Split(path, string(filepath.Separator))
	if len(parts) == 0 {
		return ""
	}
	prefix := ""
	if strings.HasPrefix(path, string(filepath.Separator)) {
		prefix = string(filepath.Separator)
	}
	for i, part := range parts {
		if part != dirName {
			continue
		}
		root := strings.Join(parts[:i+1], string(filepath.Separator))
		root = prefix + strings.TrimPrefix(root, string(filepath.Separator))
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			return filepath.Clean(root)
		}
	}
	return ""
}

func traeRootFromPath(path string) string {
	path = filepath.Clean(path)
	marker := string(filepath.Separator) + ".trae" + string(filepath.Separator) + "cli" + string(filepath.Separator)
	index := strings.Index(path, marker)
	if index < 0 {
		return ""
	}
	root := path[:index+len(marker)-1]
	if info, err := os.Stat(root); err == nil && info.IsDir() {
		return filepath.Clean(root)
	}
	return ""
}

func buildLiveSessionsAt(processes []LiveProcess, data *TranscriptData, idleGap time.Duration, now time.Time) ([]LiveSession, []string) {
	tracesByID := buildTracesByID(data)
	sessions := map[string]*LiveSession{}
	unassigned := 0
	untraced := 0
	transcriptOnly := 0
	notes := []string{}
	for _, process := range processes {
		processSessions, processNotes := normalizeProcessSessionMappings(process, data, tracesByID)
		notes = append(notes, processNotes...)
		if len(processSessions) == 0 {
			unassigned++
			continue
		}
		for _, candidate := range processSessions {
			session := sessions[candidate.Key]
			if session == nil {
				session = &LiveSession{
					Tool:      candidate.Tool,
					SessionID: candidate.SessionID,
					Path:      candidate.Path,
					Processes: map[int]struct{}{},
					HostApps:  map[int]HostApp{},
					Trace:     candidate.Trace,
					Mapping:   candidate.Mapping,
				}
				sessions[candidate.Key] = session
			} else {
				if session.Tool == "" {
					session.Tool = candidate.Tool
				}
				if session.SessionID == "" {
					session.SessionID = candidate.SessionID
				}
				if session.Path == "" {
					session.Path = candidate.Path
				}
				if session.Trace == nil {
					session.Trace = candidate.Trace
				}
				mergeLiveSessionMapping(&session.Mapping, candidate.Mapping)
			}
			if session.Path == "" && session.Trace != nil && session.Trace.Path != "" {
				session.Path = session.Trace.Path
			}
			session.Processes[process.PID] = struct{}{}
			if process.HostApp != nil && process.HostApp.PID > 0 && strings.TrimSpace(process.HostApp.Name) != "" {
				if session.HostApps == nil {
					session.HostApps = map[int]HostApp{}
				}
				session.HostApps[process.HostApp.PID] = *process.HostApp
			}
		}
	}

	recentCutoff := now.Add(-recentSessionWindow(idleGap))
	if data != nil {
		for _, trace := range data.Traces {
			if trace == nil || trace.LastEvent.IsZero() || trace.LastEvent.Before(recentCutoff) || trace.LastEvent.After(now.Add(idleGap)) {
				continue
			}
			key := liveSessionKeyForTrace(trace)
			if key == "" {
				continue
			}
			if _, exists := sessions[key]; exists {
				continue
			}
			sessionID := strings.TrimSpace(trace.SessionID)
			sessions[key] = &LiveSession{
				Tool:      trace.Tool,
				SessionID: sessionID,
				Path:      trace.Path,
				Processes: map[int]struct{}{},
				HostApps:  map[int]HostApp{},
				Trace:     trace,
				Mapping: LiveSessionMapping{
					TranscriptPath:     true,
					TranscriptActivity: true,
					ParsedTranscriptID: strings.TrimSpace(trace.SessionID) != "",
				},
			}
			transcriptOnly++
		}
	}

	out := make([]LiveSession, 0, len(sessions))
	for _, session := range sessions {
		if session.Trace == nil || session.Trace.LastEvent.IsZero() {
			untraced++
		}
		out = append(out, *session)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool == out[j].Tool {
			if out[i].SessionID == out[j].SessionID {
				return out[i].Path < out[j].Path
			}
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].Tool < out[j].Tool
	})
	if unassigned > 0 {
		notes = append(notes, itoa(unassigned)+" live processes had no session mapping and only contribute to PID concurrency.")
	}
	if untraced > 0 {
		notes = append(notes, itoa(untraced)+" live sessions lack transcript timing, so active burst concurrency is conservative.")
	}
	if transcriptOnly > 0 {
		notes = append(notes, itoa(transcriptOnly)+" recent transcript-backed sessions had no currently mapped process and were included as local activity sessions.")
	}
	return out, notes
}

func liveSessionKeyForTrace(trace *SessionTrace) string {
	if trace == nil {
		return ""
	}
	if key := liveSessionKeyForID(trace.Tool, trace.SessionID); key != "" {
		return key
	}
	if trace.Tool != "" && trace.Path != "" {
		return liveSessionKeyForPath(TranscriptFile{Tool: trace.Tool, Path: trace.Path})
	}
	return ""
}

func liveSessionKeyForID(tool, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	return tool + "\x00" + sessionID
}

func liveSessionKeyForPath(file TranscriptFile) string {
	return file.Tool + "\x00path:" + file.Path
}

func mergeLiveSessionMapping(dst *LiveSessionMapping, src LiveSessionMapping) {
	dst.TranscriptPath = dst.TranscriptPath || src.TranscriptPath
	dst.TranscriptActivity = dst.TranscriptActivity || src.TranscriptActivity
	dst.ParsedTranscriptID = dst.ParsedTranscriptID || src.ParsedTranscriptID
	dst.CommandHint = dst.CommandHint || src.CommandHint
	dst.FallbackSessionID = dst.FallbackSessionID || src.FallbackSessionID
}

func projectLiveProcessesWithSessions(processes []LiveProcess, liveSessions []LiveSession, liveSessionSnapshots []LiveSessionSnapshot, data *TranscriptData) []LiveProcessSnapshot {
	tracesByID := buildTracesByID(data)
	sessionSnapshotsByID := map[string]LiveSessionSnapshot{}
	for _, session := range liveSessionSnapshots {
		if session.SessionID == "" {
			continue
		}
		sessionSnapshotsByID[liveSessionKeyForID(session.Tool, session.SessionID)] = session
	}
	processSessionIDs := map[int]map[string]struct{}{}
	for _, session := range liveSessions {
		if session.SessionID == "" {
			continue
		}
		key := liveSessionKeyForID(session.Tool, session.SessionID)
		for pid := range session.Processes {
			if processSessionIDs[pid] == nil {
				processSessionIDs[pid] = map[string]struct{}{}
			}
			processSessionIDs[pid][key] = struct{}{}
		}
	}
	out := make([]LiveProcessSnapshot, 0, len(processes))
	for _, process := range processes {
		processSessions, _ := normalizeProcessSessionMappings(process, data, tracesByID)
		snapshot := LiveProcessSnapshot{
			PID:                  process.PID,
			Tool:                 process.Tool,
			DisplayName:          processDisplayName(process),
			Command:              process.Command,
			CPUPercent:           process.CPUPercent,
			MemoryBytes:          process.MemoryBytes,
			DiskReadBytes:        process.DiskReadBytes,
			DiskWriteBytes:       process.DiskWriteBytes,
			DiskReadBytesPerSec:  process.DiskReadBytesPerSec,
			DiskWriteBytesPerSec: process.DiskWriteBytesPerSec,
			Elapsed:              process.Elapsed,
			HostApp:              cloneHostApp(process.HostApp),
		}
		sessionIDs := []string{}
		sessionPaths := []string{}
		seenIDs := map[string]struct{}{}
		seenPaths := map[string]struct{}{}
		sessionKeys := map[string]struct{}{}
		for _, candidate := range processSessions {
			if candidate.Path != "" {
				if _, ok := seenPaths[candidate.Path]; !ok {
					seenPaths[candidate.Path] = struct{}{}
					sessionPaths = append(sessionPaths, candidate.Path)
				}
			}
			if candidate.SessionID == "" {
				continue
			}
			if _, ok := seenIDs[candidate.SessionID]; ok {
				continue
			}
			seenIDs[candidate.SessionID] = struct{}{}
			sessionIDs = append(sessionIDs, candidate.SessionID)
			sessionKeys[liveSessionKeyForID(candidate.Tool, candidate.SessionID)] = struct{}{}
		}
		for key := range processSessionIDs[process.PID] {
			sessionKeys[key] = struct{}{}
			session := sessionSnapshotsByID[key]
			if session.SessionID == "" {
				continue
			}
			if _, ok := seenIDs[session.SessionID]; ok {
				continue
			}
			seenIDs[session.SessionID] = struct{}{}
			sessionIDs = append(sessionIDs, session.SessionID)
		}
		sort.Strings(sessionIDs)
		sort.Strings(sessionPaths)
		snapshot.SessionIDs = sessionIDs
		snapshot.SessionPaths = sessionPaths
		snapshot.MappedSessions = len(sessionIDs)
		snapshot.MappedSessionEvidence = buildProcessSessionEvidence(sessionKeys, sessionSnapshotsByID)
		snapshot.MatchMethods = processMatchMethods(snapshot.MappedSessionEvidence)
		for _, evidence := range snapshot.MappedSessionEvidence {
			facts := metricFactsForProcessSessionEvidence(evidence)
			if facts.RecentMovement {
				snapshot.MappedActiveSessions++
			}
			switch facts.Role {
			case "main":
				snapshot.MainSessions++
			case "subagent":
				snapshot.SubagentSessions++
			default:
				snapshot.UnknownRoleSessions++
			}
		}
		if snapshot.MappedSessions > len(snapshot.MappedSessionEvidence) {
			snapshot.UnknownRoleSessions += snapshot.MappedSessions - len(snapshot.MappedSessionEvidence)
		}
		snapshot.EvidenceSummary = processEvidenceSummary(snapshot)
		out = append(out, snapshot)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MappedSessions != out[j].MappedSessions {
			return out[i].MappedSessions > out[j].MappedSessions
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].PID < out[j].PID
	})
	return out
}

func buildProcessSessionEvidence(sessionKeys map[string]struct{}, sessionsByID map[string]LiveSessionSnapshot) []ProcessSessionEvidence {
	out := []ProcessSessionEvidence{}
	for key := range sessionKeys {
		session, ok := sessionsByID[key]
		if !ok || session.SessionID == "" {
			continue
		}
		facts := metricFactsForSessionSnapshot(session)
		out = append(out, ProcessSessionEvidence{
			Tool:                session.Tool,
			SessionID:           session.SessionID,
			Project:             session.Project,
			Role:                facts.Role,
			ActiveBurst:         facts.RecentMovement,
			Freshness:           session.Freshness,
			MappingMethod:       session.MappingMethod,
			Confidence:          session.Confidence,
			RoleConfidence:      session.RoleConfidence,
			LastEventAgeSeconds: session.LastEventAgeSeconds,
			Provenance:          append([]string(nil), session.Provenance...),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ActiveBurst != out[j].ActiveBurst {
			return out[i].ActiveBurst
		}
		if roleRank(out[i].Role) != roleRank(out[j].Role) {
			return roleRank(out[i].Role) < roleRank(out[j].Role)
		}
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

func attachProcessResourcesToSessions(sessions []LiveSessionSnapshot, processes []LiveProcessSnapshot) []LiveSessionSnapshot {
	if len(sessions) == 0 || len(processes) == 0 {
		return sessions
	}
	out := append([]LiveSessionSnapshot(nil), sessions...)
	index := map[string]int{}
	for i, session := range out {
		if session.SessionID == "" {
			continue
		}
		index[liveSessionKeyForID(session.Tool, session.SessionID)] = i
	}
	for _, process := range processes {
		attached := map[int]struct{}{}
		for _, evidence := range process.MappedSessionEvidence {
			if evidence.SessionID == "" {
				continue
			}
			key := liveSessionKeyForID(evidence.Tool, evidence.SessionID)
			i, ok := index[key]
			if !ok {
				key = liveSessionKeyForID(process.Tool, evidence.SessionID)
				i, ok = index[key]
			}
			if !ok {
				continue
			}
			attached[i] = struct{}{}
		}
		// Per-session attribution keeps the full process CPU/memory on every
		// attached session ("resources of processes attached to this session").
		// SharedProcessCount discloses when that attribution overlaps other
		// sessions so aggregates and UIs can avoid double counting.
		for i := range attached {
			out[i].ProcessCPUPercent += process.CPUPercent
			out[i].ProcessMemoryBytes += process.MemoryBytes
			if len(attached) > 1 {
				out[i].SharedProcessCount++
			}
		}
	}
	return out
}

func normalizedRole(role string) string {
	switch strings.TrimSpace(role) {
	case "main", "subagent":
		return role
	default:
		return "unknown"
	}
}

func processMatchMethods(evidence []ProcessSessionEvidence) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, item := range evidence {
		method := strings.TrimSpace(item.MappingMethod)
		if method == "" {
			continue
		}
		if _, ok := seen[method]; ok {
			continue
		}
		seen[method] = struct{}{}
		out = append(out, method)
	}
	sort.Strings(out)
	return out
}

func processEvidenceSummary(process LiveProcessSnapshot) string {
	if process.MappedSessions == 0 {
		return "unmapped process"
	}
	return fmt.Sprintf("direct %d / subagent %d / unknown %d / active %d", process.MainSessions, process.SubagentSessions, process.UnknownRoleSessions, process.MappedActiveSessions)
}

func processDisplayName(process LiveProcess) string {
	if identity := strings.TrimSpace(process.DisplayName); identity != "" {
		return identity
	}
	fields := strings.Fields(strings.TrimSpace(process.Command))
	if len(fields) == 0 {
		return process.Tool
	}
	first := cleanCommandBase(fields[0])
	if first == "" {
		return process.Tool
	}
	if isGenericRuntimeExecutable(first) {
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-") {
				continue
			}
			base := cleanCommandBase(field)
			if base != "" && !isGenericRuntimeExecutable(base) {
				return base
			}
		}
	}
	return first
}

func cleanCommandBase(value string) string {
	token := strings.Trim(value, "\"'")
	if token == "" {
		return ""
	}
	base := filepath.Base(token)
	base = strings.TrimSuffix(base, ".js")
	base = strings.TrimSuffix(base, ".mjs")
	base = strings.TrimSuffix(base, ".cjs")
	base = strings.TrimSuffix(base, ".ts")
	return strings.TrimSpace(base)
}

func isGenericRuntimeExecutable(name string) bool {
	switch strings.ToLower(name) {
	case "node", "bun", "deno", "python", "python3", "ruby", "go", "bash", "zsh", "sh", "electron":
		return true
	default:
		return false
	}
}

func roleRank(role string) int {
	switch normalizedRole(role) {
	case "main":
		return 0
	case "subagent":
		return 1
	default:
		return 2
	}
}

func buildRuntimeProcessSummary(processes []LiveProcessSnapshot) []ProcessRuntimeSummary {
	type accumulator struct {
		item            ProcessRuntimeSummary
		directSessions  map[string]struct{}
		subSessions     map[string]struct{}
		unknownSessions map[string]struct{}
		activeSessions  map[string]struct{}
	}
	items := map[string]*accumulator{}
	addEvidence := func(acc *accumulator, process LiveProcessSnapshot) {
		for _, evidence := range process.MappedSessionEvidence {
			facts := metricFactsForProcessSessionEvidence(evidence)
			if !facts.KnownSession {
				continue
			}
			if facts.RecentMovement {
				acc.activeSessions[evidence.SessionID] = struct{}{}
			}
			switch facts.Role {
			case "main":
				acc.directSessions[evidence.SessionID] = struct{}{}
			case "subagent":
				acc.subSessions[evidence.SessionID] = struct{}{}
			default:
				acc.unknownSessions[evidence.SessionID] = struct{}{}
			}
		}
		for _, sessionID := range process.SessionIDs {
			if sessionID == "" {
				continue
			}
			known := false
			for _, evidence := range process.MappedSessionEvidence {
				if evidence.SessionID == sessionID {
					known = true
					break
				}
			}
			if !known {
				acc.unknownSessions[sessionID] = struct{}{}
			}
		}
	}
	for _, process := range processes {
		key := strings.TrimSpace(process.Tool)
		if key == "" {
			key = "unknown"
		}
		acc := items[key]
		if acc == nil {
			acc = &accumulator{
				item: ProcessRuntimeSummary{
					Key:         key,
					Tool:        key,
					DisplayName: key,
				},
				directSessions:  map[string]struct{}{},
				subSessions:     map[string]struct{}{},
				unknownSessions: map[string]struct{}{},
				activeSessions:  map[string]struct{}{},
			}
			items[key] = acc
		}
		acc.item.PIDCount++
		acc.item.CPUPercent += process.CPUPercent
		acc.item.MemoryBytes += process.MemoryBytes
		if process.MappedSessions > 0 {
			acc.item.MappedProcesses++
		} else {
			acc.item.UnmappedProcesses++
		}
		addEvidence(acc, process)
	}
	out := make([]ProcessRuntimeSummary, 0, len(items))
	for _, acc := range items {
		acc.item.DirectSessions = len(acc.directSessions)
		acc.item.SubagentSessions = len(acc.subSessions)
		acc.item.UnknownRoleSessions = len(acc.unknownSessions)
		acc.item.ActiveSessions = len(acc.activeSessions)
		out = append(out, acc.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PIDCount != out[j].PIDCount {
			return out[i].PIDCount > out[j].PIDCount
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func buildHostAppProcessSummary(processes []LiveProcessSnapshot) []HostAppProcessSummary {
	type accumulator struct {
		item            HostAppProcessSummary
		directSessions  map[string]struct{}
		subSessions     map[string]struct{}
		unknownSessions map[string]struct{}
		activeSessions  map[string]struct{}
	}
	items := map[string]*accumulator{}
	addEvidence := func(acc *accumulator, process LiveProcessSnapshot) {
		for _, evidence := range process.MappedSessionEvidence {
			facts := metricFactsForProcessSessionEvidence(evidence)
			if !facts.KnownSession {
				continue
			}
			if facts.RecentMovement {
				acc.activeSessions[evidence.SessionID] = struct{}{}
			}
			switch facts.Role {
			case "main":
				acc.directSessions[evidence.SessionID] = struct{}{}
			case "subagent":
				acc.subSessions[evidence.SessionID] = struct{}{}
			default:
				acc.unknownSessions[evidence.SessionID] = struct{}{}
			}
		}
		for _, sessionID := range process.SessionIDs {
			if sessionID == "" {
				continue
			}
			known := false
			for _, evidence := range process.MappedSessionEvidence {
				if evidence.SessionID == sessionID {
					known = true
					break
				}
			}
			if !known {
				acc.unknownSessions[sessionID] = struct{}{}
			}
		}
	}
	for _, process := range processes {
		if process.HostApp == nil || strings.TrimSpace(process.HostApp.Name) == "" {
			continue
		}
		key := process.HostApp.Name
		if process.HostApp.PID > 0 {
			key = fmt.Sprintf("%s:%d", process.HostApp.Name, process.HostApp.PID)
		}
		acc := items[key]
		if acc == nil {
			acc = &accumulator{
				item: HostAppProcessSummary{
					Key:  key,
					Name: process.HostApp.Name,
					PID:  process.HostApp.PID,
				},
				directSessions:  map[string]struct{}{},
				subSessions:     map[string]struct{}{},
				unknownSessions: map[string]struct{}{},
				activeSessions:  map[string]struct{}{},
			}
			items[key] = acc
		}
		acc.item.PIDCount++
		acc.item.CPUPercent += process.CPUPercent
		acc.item.MemoryBytes += process.MemoryBytes
		if process.MappedSessions > 0 {
			acc.item.MappedProcesses++
		} else {
			acc.item.UnmappedProcesses++
		}
		addEvidence(acc, process)
	}
	out := make([]HostAppProcessSummary, 0, len(items))
	for _, acc := range items {
		acc.item.DirectSessions = len(acc.directSessions)
		acc.item.SubagentSessions = len(acc.subSessions)
		acc.item.UnknownRoleSessions = len(acc.unknownSessions)
		acc.item.ActiveSessions = len(acc.activeSessions)
		out = append(out, acc.item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PIDCount != out[j].PIDCount {
			return out[i].PIDCount > out[j].PIDCount
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func projectLiveSessions(sessions []LiveSession, idleGap time.Duration, now time.Time) []LiveSessionSnapshot {
	out := make([]LiveSessionSnapshot, 0, len(sessions))
	for _, session := range sessions {
		observation := observeLiveSession(session, idleGap, now)
		facts := metricFactsForLiveSession(session, observation)
		projectAttribution := observeProjectAttribution(session)
		role := observeSessionRole(session)
		item := LiveSessionSnapshot{
			Tool:                         session.Tool,
			SessionID:                    session.SessionID,
			SessionRole:                  facts.Role,
			RoleConfidence:               role.Confidence,
			RoleReasons:                  role.Reasons,
			Project:                      projectAttribution.Project,
			Path:                         session.Path,
			ProcessCount:                 facts.ProcessPressure,
			ActiveBurst:                  facts.RecentMovement,
			Freshness:                    observation.Freshness,
			NeedsReview:                  sessionNeedsReviewObservation(facts.Role, observation),
			MappingMethod:                observation.MappingMethod,
			MissingTranscript:            observation.MissingTranscript,
			Confidence:                   observation.Confidence,
			ConfidenceReasons:            observation.ConfidenceReasons,
			ProjectAttributionSource:     projectAttribution.Source,
			ProjectAttributionConfidence: projectAttribution.Confidence,
			ProjectAttributionReasons:    projectAttribution.Reasons,
			Provenance:                   observation.Provenance,
		}
		item.HostApps = sortedHostApps(session.HostApps)
		if session.Trace != nil {
			item.ThreadSource = strings.TrimSpace(session.Trace.ThreadSource)
			item.ParentThreadID = strings.TrimSpace(session.Trace.ParentThreadID)
			item.AgentRole = strings.TrimSpace(session.Trace.AgentRole)
			item.AgentNickname = strings.TrimSpace(session.Trace.AgentNickname)
			item.RoleHintSource = strings.TrimSpace(session.Trace.RoleHintSource)
			item.IndependentlyRun = session.Trace.IndependentlyRun
			if duration, ok := buildSessionDurationMetrics(session.Trace, idleGap, 0); ok {
				item.FirstEventAt = duration.FirstEventAt.Format(time.RFC3339)
				item.ObservedDurationSeconds = duration.ObservedDurationSeconds
				item.ActiveDurationSeconds = duration.ActiveDurationSeconds
				item.IdleDurationSeconds = duration.IdleDurationSeconds
			}
			if !session.Trace.TokenUsage.Empty() {
				tokenUsage := session.Trace.TokenUsage
				item.TokenUsage = &tokenUsage
				item.TokenUsageSource = "transcript_usage"
				item.TokenUsageConfidence = "measured"
			}
		}
		if observation.LastEventAt != "" {
			item.LastEventAt = observation.LastEventAt
			item.LastEventAgeSeconds = observation.LastEventAgeSeconds
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if freshnessRank(out[i].Freshness) != freshnessRank(out[j].Freshness) {
			return freshnessRank(out[i].Freshness) < freshnessRank(out[j].Freshness)
		}
		if out[i].ProcessCount != out[j].ProcessCount {
			return out[i].ProcessCount > out[j].ProcessCount
		}
		ageI := sessionSortAge(out[i])
		ageJ := sessionSortAge(out[j])
		if ageI != ageJ {
			return ageI < ageJ
		}
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

func cloneHostApp(app *HostApp) *HostApp {
	if app == nil {
		return nil
	}
	out := *app
	return &out
}

func sortedHostApps(apps map[int]HostApp) []HostApp {
	out := make([]HostApp, 0, len(apps))
	for _, app := range apps {
		if app.PID <= 0 || strings.TrimSpace(app.Name) == "" {
			continue
		}
		app.Name = strings.TrimSpace(app.Name)
		app.BundlePath = strings.TrimSpace(app.BundlePath)
		out = append(out, app)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].PID < out[j].PID
		}
		return out[i].Name < out[j].Name
	})
	return out
}

type sessionRoleObservation struct {
	Role       string
	Confidence string
	Reasons    []string
}

type liveSessionObservation struct {
	Freshness           string
	MappingMethod       string
	MissingTranscript   bool
	Confidence          string
	ConfidenceReasons   []string
	Provenance          []string
	LastEventAt         string
	LastEventAgeSeconds int
	ActiveBurst         bool
	Stale               bool
	Recent              bool
}

type projectAttributionObservation struct {
	Project    string
	Source     string
	Confidence string
	Reasons    []string
}

func observeSessionRole(session LiveSession) sessionRoleObservation {
	trace := session.Trace
	role := sessionRoleObservation{
		Role:       "unknown",
		Confidence: "low",
	}
	if trace == nil {
		role.Reasons = append(role.Reasons, "no transcript role metadata")
		return role
	}
	switch strings.TrimSpace(trace.ThreadSource) {
	case "subagent":
		role.Role = "subagent"
		role.Confidence = "high"
		role.Reasons = append(role.Reasons, "thread_source=subagent")
	case "user":
		role.Role = "main"
		role.Confidence = "high"
		role.Reasons = append(role.Reasons, "thread_source=user")
	}
	if strings.TrimSpace(trace.ParentThreadID) != "" {
		role.Role = "subagent"
		role.Confidence = "high"
		role.Reasons = append(role.Reasons, "parent_thread_id present")
	}
	if strings.TrimSpace(trace.RoleHintSource) == "codexl_lane_path" {
		role.Role = "subagent"
		role.Confidence = maxConfidence(role.Confidence, "medium")
		role.Reasons = append(role.Reasons, "codexL lane transcript path")
	}
	if role.Role == "unknown" && trace.IndependentlyRun {
		role.Role = "main"
		role.Confidence = "medium"
		role.Reasons = append(role.Reasons, "independently resumable session log")
	}
	if role.Role == "unknown" && strings.TrimSpace(session.SessionID) != "" && len(session.Processes) > 0 {
		role.Confidence = "low"
		role.Reasons = append(role.Reasons, "process and session id present but role metadata absent")
	}
	if len(role.Reasons) == 0 {
		role.Reasons = append(role.Reasons, "role metadata unavailable")
	}
	role.Reasons = uniqueSortedStrings(role.Reasons)
	return role
}

func maxConfidence(a, b string) string {
	if confidenceRank(b) > confidenceRank(a) {
		return b
	}
	if strings.TrimSpace(a) == "" {
		return b
	}
	return a
}

func observeLiveSession(session LiveSession, idleGap time.Duration, now time.Time) liveSessionObservation {
	effectiveIdleGap := idleGap
	if effectiveIdleGap <= 0 {
		effectiveIdleGap = 90 * time.Second
	}
	observation := liveSessionObservation{
		Freshness:         "unknown",
		MappingMethod:     sessionMappingMethod(session.Mapping),
		MissingTranscript: !sessionHasTranscriptTiming(session),
		Confidence:        "low",
		Provenance:        sessionProvenance(session.Mapping),
	}
	if observation.MappingMethod == "" {
		observation.MappingMethod = "fallback_session_id"
	}
	if len(observation.Provenance) == 0 {
		observation.Provenance = []string{"fallback_session_id"}
	}

	if !observation.MissingTranscript {
		age := now.Sub(session.Trace.LastEvent)
		if age < 0 {
			age = 0
		}
		observation.LastEventAt = session.Trace.LastEvent.Format(time.RFC3339)
		observation.LastEventAgeSeconds = int(age.Seconds())
		switch {
		case age <= effectiveIdleGap:
			observation.Freshness = "active"
			observation.ActiveBurst = true
		case age <= staleSessionThreshold(effectiveIdleGap):
			observation.Freshness = "idle"
		default:
			observation.Freshness = "stale"
			observation.Stale = true
		}
	}
	if session.Trace != nil && !session.Trace.FirstEvent.IsZero() {
		windowStart := now.Add(-recentSessionWindow(effectiveIdleGap))
		observation.Recent = !session.Trace.FirstEvent.Before(windowStart) && !session.Trace.FirstEvent.After(now)
	}

	switch {
	case session.Mapping.ParsedTranscriptID:
		observation.Confidence = "high"
	case session.Mapping.CommandHint:
		observation.Confidence = "medium"
	case session.Mapping.FallbackSessionID:
		observation.Confidence = "low"
	default:
		observation.Confidence = "low"
	}
	if session.Mapping.FallbackSessionID {
		if !session.Mapping.ParsedTranscriptID && !session.Mapping.CommandHint {
			observation.ConfidenceReasons = append(observation.ConfidenceReasons, "session id falls back to filename-derived evidence")
		} else {
			observation.ConfidenceReasons = append(observation.ConfidenceReasons, "filename-derived fallback evidence also matched this session")
		}
	}
	if session.Mapping.CommandHint && !session.Mapping.ParsedTranscriptID {
		observation.ConfidenceReasons = append(observation.ConfidenceReasons, "mapped from command hint instead of a parsed transcript session id")
	}
	if observation.MissingTranscript {
		observation.Confidence = lowerConfidence(observation.Confidence)
		observation.ConfidenceReasons = append(observation.ConfidenceReasons, "transcript timing missing")
	}
	return observation
}

func observeProjectAttribution(session LiveSession) projectAttributionObservation {
	if project, source, reason := traceProjectAttribution(session.Trace); project != "" {
		return projectAttributionObservation{
			Project:    project,
			Source:     source,
			Confidence: projectAttributionConfidence(source),
			Reasons:    []string{reason},
		}
	}
	reasons := []string{}
	if project, reason := configRootProjectAttribution(session.Path); project != "" {
		return projectAttributionObservation{
			Project:    project,
			Source:     "config_root_parent",
			Confidence: projectAttributionConfidence("config_root_parent"),
			Reasons:    []string{reason},
		}
	} else if reason != "" {
		reasons = append(reasons, reason)
	}
	if session.Trace == nil || trustedTraceProjectName(session.Trace.Project) == "" {
		reasons = append(reasons, "no parsed transcript cwd/project evidence")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "no trusted local project evidence")
	}
	return projectAttributionObservation{
		Source:     "unassigned",
		Confidence: projectAttributionConfidence("unassigned"),
		Reasons:    uniqueSortedStrings(reasons),
	}
}

func sessionSortAge(item LiveSessionSnapshot) int {
	if item.LastEventAt == "" {
		return int(^uint(0) >> 1)
	}
	return item.LastEventAgeSeconds
}

func freshnessRank(freshness string) int {
	switch freshness {
	case "active":
		return 0
	case "idle":
		return 1
	case "stale":
		return 2
	default:
		return 3
	}
}

func sessionHasTranscriptTiming(session LiveSession) bool {
	return session.Trace != nil && !session.Trace.LastEvent.IsZero()
}

func sessionProvenance(mapping LiveSessionMapping) []string {
	out := []string{}
	if mapping.TranscriptPath {
		out = append(out, "transcript_path")
	}
	if mapping.TranscriptActivity {
		out = append(out, "transcript_activity")
	}
	if mapping.CommandHint {
		out = append(out, "command_hint")
	}
	if mapping.FallbackSessionID {
		out = append(out, "fallback_session_id")
	}
	return out
}

func sessionMappingMethod(mapping LiveSessionMapping) string {
	switch {
	case mapping.ParsedTranscriptID:
		return "transcript_path"
	case mapping.CommandHint:
		return "command_hint"
	case mapping.FallbackSessionID:
		return "fallback_session_id"
	case mapping.TranscriptPath:
		return "transcript_path"
	default:
		return ""
	}
}

func staleSessionThreshold(idleGap time.Duration) time.Duration {
	if idleGap <= 0 {
		idleGap = 90 * time.Second
	}
	threshold := idleGap * 3
	if threshold < 5*time.Minute {
		threshold = 5 * time.Minute
	}
	return threshold
}

func recentSessionWindow(idleGap time.Duration) time.Duration {
	if idleGap <= 0 {
		idleGap = 90 * time.Second
	}
	window := idleGap * 10
	if window < 15*time.Minute {
		window = 15 * time.Minute
	}
	if window > time.Hour {
		window = time.Hour
	}
	return window
}

func lowerConfidence(level string) string {
	switch level {
	case "high":
		return "medium"
	case "medium":
		return "low"
	default:
		return "low"
	}
}

func buildSnapshotSummary(processes []LiveProcessSnapshot, sessions []LiveSessionSnapshot, projects []ProjectSnapshot) SnapshotSummary {
	summary := SnapshotSummary{
		ProjectCount: len(projects),
	}
	for _, process := range processes {
		if process.MappedSessions > 0 {
			summary.MappedProcesses++
		} else {
			summary.UnmappedProcesses++
		}
		if process.MappedSessions > 1 {
			summary.MultiMappedProcesses++
		}
	}
	for _, session := range sessions {
		facts := metricFactsForSessionSnapshot(session)
		if facts.RecentMovement {
			summary.ActiveSessions++
		} else {
			summary.IdleSessions++
		}
		switch facts.Role {
		case "main":
			summary.MainAgentSessions++
		case "subagent":
			summary.SubagentSessions++
		default:
			summary.UnknownRoleSessions++
		}
	}
	for _, project := range projects {
		if project.ActiveBurstCount > 0 {
			summary.HotProjectCount++
		}
	}
	totalProcesses := len(processes)
	if totalProcesses > 0 {
		summary.MappingCoveragePct = float64(summary.MappedProcesses*1000) / float64(totalProcesses) / 10
	}
	return summary
}

func buildAgeBuckets(sessions []LiveSession, idleGap time.Duration, now time.Time) []AgeBucketSnapshot {
	idleSeconds := int(idleGap / time.Second)
	if idleSeconds < 30 {
		idleSeconds = 30
	}
	buckets := []AgeBucketSnapshot{
		{Label: "0-30s"},
		{Label: "31-" + itoa(idleSeconds) + "s"},
		{Label: itoa(idleSeconds+1) + "-300s"},
		{Label: "5m+"},
		{Label: "No trace"},
	}
	for _, session := range sessions {
		if session.Trace == nil || session.Trace.LastEvent.IsZero() {
			buckets[4].Count++
			continue
		}
		age := now.Sub(session.Trace.LastEvent)
		if age < 0 {
			age = 0
		}
		switch {
		case age <= 30*time.Second:
			buckets[0].Count++
		case age <= idleGap:
			buckets[1].Count++
		case age <= 5*time.Minute:
			buckets[2].Count++
		default:
			buckets[3].Count++
		}
	}
	return buckets
}

func buildProjectFocus(sessions []LiveSession, idleGap time.Duration, now time.Time) []ProjectSnapshot {
	type toolAggregate struct {
		sessionCount     int
		activeBurstCount int
		processes        map[int]struct{}
		tokenUsage       TokenUsage
	}
	type aggregate struct {
		project                            string
		sessionCount                       int
		activeBurstCount                   int
		mainAgentSessions                  int
		subagentSessions                   int
		unknownRoleSessions                int
		staleSessionCount                  int
		recentSessionCount                 int
		processes                          map[int]struct{}
		lastEvent                          time.Time
		tools                              map[string]*toolAggregate
		tokenUsage                         TokenUsage
		confidenceCounts                   map[string]int
		provenanceCounts                   map[string]int
		projectAttributionConfidenceCounts map[string]int
		projectAttributionSourceCounts     map[string]int
		missingTranscriptCount             int
	}

	projects := map[string]*aggregate{}
	pidProjects := map[int]map[string]struct{}{}
	for _, session := range sessions {
		observation := observeLiveSession(session, idleGap, now)
		facts := metricFactsForLiveSession(session, observation)
		projectAttribution := observeProjectAttribution(session)
		projectName := projectAttribution.Project
		if projectName == "" {
			projectName = "unassigned"
		}
		item := projects[projectName]
		if item == nil {
			item = &aggregate{
				project:                            projectName,
				processes:                          map[int]struct{}{},
				tools:                              map[string]*toolAggregate{},
				confidenceCounts:                   map[string]int{},
				provenanceCounts:                   map[string]int{},
				projectAttributionConfidenceCounts: map[string]int{},
				projectAttributionSourceCounts:     map[string]int{},
			}
			projects[projectName] = item
		}
		item.sessionCount++
		switch facts.Role {
		case "main":
			item.mainAgentSessions++
		case "subagent":
			item.subagentSessions++
		default:
			item.unknownRoleSessions++
		}
		item.confidenceCounts[observation.Confidence]++
		item.projectAttributionConfidenceCounts[projectAttribution.Confidence]++
		item.projectAttributionSourceCounts[projectAttribution.Source]++
		if observation.MissingTranscript {
			item.missingTranscriptCount++
		}
		if facts.StaleSession {
			item.staleSessionCount++
		}
		if facts.RecentSession {
			item.recentSessionCount++
		}
		for _, source := range observation.Provenance {
			item.provenanceCounts[source]++
		}
		toolAgg := item.tools[session.Tool]
		if toolAgg == nil {
			toolAgg = &toolAggregate{processes: map[int]struct{}{}}
			item.tools[session.Tool] = toolAgg
		}
		toolAgg.sessionCount++
		if session.Trace != nil && !session.Trace.TokenUsage.Empty() {
			item.tokenUsage.Add(session.Trace.TokenUsage)
			toolAgg.tokenUsage.Add(session.Trace.TokenUsage)
		}
		if facts.RecentMovement {
			item.activeBurstCount++
			toolAgg.activeBurstCount++
		}
		if session.Trace != nil && session.Trace.LastEvent.After(item.lastEvent) {
			item.lastEvent = session.Trace.LastEvent
		}
		for _, pid := range facts.ProcessIDs {
			item.processes[pid] = struct{}{}
			toolAgg.processes[pid] = struct{}{}
			if pidProjects[pid] == nil {
				pidProjects[pid] = map[string]struct{}{}
			}
			pidProjects[pid][projectName] = struct{}{}
		}
	}

	attentionBasis := "process_count"
	attentionDenominator := len(pidProjects)
	for _, mappedProjects := range pidProjects {
		if len(mappedProjects) > 1 {
			attentionBasis = "session_count"
			break
		}
	}
	if attentionBasis == "session_count" || attentionDenominator == 0 {
		attentionBasis = "session_count"
		attentionDenominator = 0
		for _, item := range projects {
			attentionDenominator += item.sessionCount
		}
	}

	out := make([]ProjectSnapshot, 0, len(projects))
	for _, item := range projects {
		project := ProjectSnapshot{
			Project:                         item.project,
			SessionCount:                    item.sessionCount,
			ActiveBurstCount:                item.activeBurstCount,
			MainAgentSessions:               item.mainAgentSessions,
			SubagentSessions:                item.subagentSessions,
			UnknownRoleSessions:             item.unknownRoleSessions,
			ProcessCount:                    len(item.processes),
			AttentionBasis:                  attentionBasis,
			StaleSessionCount:               item.staleSessionCount,
			RecentSessionCount:              item.recentSessionCount,
			Confidence:                      summarizeConfidence(item.confidenceCounts, item.sessionCount),
			ConfidenceBreakdown:             buildConfidenceBreakdown(item.confidenceCounts),
			ConfidenceReasons:               projectConfidenceReasons(item.confidenceCounts, item.missingTranscriptCount),
			ProjectAttributionConfidence:    summarizeConfidence(item.projectAttributionConfidenceCounts, item.sessionCount),
			ProjectAttributionReasons:       buildProjectAttributionReasons(item.projectAttributionSourceCounts),
			ProjectAttributionSourceSummary: buildProjectAttributionSourceSummary(item.projectAttributionSourceCounts),
			ProvenanceSummary:               buildProvenanceSummary(item.provenanceCounts),
		}
		attentionNumerator := len(item.processes)
		if attentionBasis == "session_count" {
			attentionNumerator = item.sessionCount
		}
		project.AttentionSharePct = ratioPct(attentionNumerator, attentionDenominator)
		if !item.lastEvent.IsZero() {
			age := now.Sub(item.lastEvent)
			if age < 0 {
				age = 0
			}
			project.LastEventAt = item.lastEvent.Format(time.RFC3339)
			project.LastEventAgeSeconds = int(age.Seconds())
		}
		if !item.tokenUsage.Empty() {
			tokenUsage := item.tokenUsage
			project.TokenUsage = &tokenUsage
			project.TokenUsageSource = "transcript_usage"
			project.TokenUsageConfidence = "measured"
		}
		for tool, toolAgg := range item.tools {
			toolSnapshot := ProjectToolSnapshot{
				Tool:             tool,
				SessionCount:     toolAgg.sessionCount,
				ActiveBurstCount: toolAgg.activeBurstCount,
				ProcessCount:     len(toolAgg.processes),
			}
			if !toolAgg.tokenUsage.Empty() {
				tokenUsage := toolAgg.tokenUsage
				toolSnapshot.TokenUsage = &tokenUsage
				toolSnapshot.TokenUsageSource = "transcript_usage"
				toolSnapshot.TokenUsageConfidence = "measured"
			}
			project.Tools = append(project.Tools, toolSnapshot)
		}
		sort.Slice(project.Tools, func(i, j int) bool {
			if project.Tools[i].ActiveBurstCount != project.Tools[j].ActiveBurstCount {
				return project.Tools[i].ActiveBurstCount > project.Tools[j].ActiveBurstCount
			}
			if project.Tools[i].SessionCount != project.Tools[j].SessionCount {
				return project.Tools[i].SessionCount > project.Tools[j].SessionCount
			}
			return project.Tools[i].Tool < project.Tools[j].Tool
		})
		out = append(out, project)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ActiveBurstCount != out[j].ActiveBurstCount {
			return out[i].ActiveBurstCount > out[j].ActiveBurstCount
		}
		if out[i].SessionCount != out[j].SessionCount {
			return out[i].SessionCount > out[j].SessionCount
		}
		if out[i].ProcessCount != out[j].ProcessCount {
			return out[i].ProcessCount > out[j].ProcessCount
		}
		ageI := out[i].LastEventAgeSeconds
		ageJ := out[j].LastEventAgeSeconds
		if out[i].LastEventAt == "" {
			ageI = int(^uint(0) >> 1)
		}
		if out[j].LastEventAt == "" {
			ageJ = int(^uint(0) >> 1)
		}
		if ageI != ageJ {
			return ageI < ageJ
		}
		return out[i].Project < out[j].Project
	})
	return out
}

// sessionProcessIDsByKey exposes per-session PID evidence so aggregates over
// session snapshots can dedupe shared processes instead of re-counting them.
func sessionProcessIDsByKey(sessions []LiveSession) map[string][]int {
	out := map[string][]int{}
	for _, session := range sessions {
		key := liveSessionKeyForID(session.Tool, session.SessionID)
		if key == "" {
			continue
		}
		ids := out[key]
		for pid := range session.Processes {
			ids = append(ids, pid)
		}
		out[key] = ids
	}
	return out
}

func buildCandidateWorkitems(sessions []LiveSessionSnapshot, sessionProcessIDs map[string][]int) []CandidateWorkitemSnapshot {
	type aggregate struct {
		key                                string
		project                            string
		tool                               string
		freshnessBucket                    string
		sessionCount                       int
		processCount                       int
		processPIDs                        map[int]struct{}
		sessionIDs                         []string
		seenSessionIDs                     map[string]struct{}
		provenanceCounts                   map[string]int
		confidenceCounts                   map[string]int
		projectAttributionConfidenceCounts map[string]int
		projectAttributionSourceCounts     map[string]int
		minConfidence                      string
		lowConfidenceCount                 int
		missingTranscriptCount             int
	}

	groups := map[string]*aggregate{}
	for _, session := range sessions {
		projectName := strings.TrimSpace(session.Project)
		if projectName == "" {
			projectName = "unassigned"
		}
		freshnessBucket := strings.TrimSpace(session.Freshness)
		if freshnessBucket == "" {
			freshnessBucket = "unknown"
		}
		key := fmt.Sprintf("project=%s|tool=%s|freshness=%s", projectName, session.Tool, freshnessBucket)
		item := groups[key]
		if item == nil {
			item = &aggregate{
				key:                                key,
				project:                            projectName,
				tool:                               session.Tool,
				freshnessBucket:                    freshnessBucket,
				processPIDs:                        map[int]struct{}{},
				seenSessionIDs:                     map[string]struct{}{},
				provenanceCounts:                   map[string]int{},
				confidenceCounts:                   map[string]int{},
				projectAttributionConfidenceCounts: map[string]int{},
				projectAttributionSourceCounts:     map[string]int{},
			}
			groups[key] = item
		}
		item.sessionCount++
		// Dedupe by PID when the session carries PID evidence: one process
		// mapped to several sessions in this bucket must count once. Sessions
		// without PID evidence fall back to their disclosed process count.
		if pids, ok := sessionProcessIDs[liveSessionKeyForID(session.Tool, session.SessionID)]; ok {
			for _, pid := range pids {
				item.processPIDs[pid] = struct{}{}
			}
		} else {
			item.processCount += session.ProcessCount
		}
		item.confidenceCounts[session.Confidence]++
		projectAttributionConfidence := strings.TrimSpace(session.ProjectAttributionConfidence)
		if projectAttributionConfidence == "" {
			projectAttributionConfidence = projectAttributionConfidenceForProject(session.Project)
		}
		item.projectAttributionConfidenceCounts[projectAttributionConfidence]++
		projectAttributionSource := strings.TrimSpace(session.ProjectAttributionSource)
		if projectAttributionSource == "" {
			projectAttributionSource = projectAttributionSourceForProject(session.Project)
		}
		item.projectAttributionSourceCounts[projectAttributionSource]++
		if session.Confidence == "low" {
			item.lowConfidenceCount++
		}
		if item.minConfidence == "" || confidenceRank(session.Confidence) < confidenceRank(item.minConfidence) {
			item.minConfidence = session.Confidence
		}
		if session.MissingTranscript {
			item.missingTranscriptCount++
		}
		if session.SessionID != "" {
			if _, ok := item.seenSessionIDs[session.SessionID]; !ok {
				item.seenSessionIDs[session.SessionID] = struct{}{}
				item.sessionIDs = append(item.sessionIDs, session.SessionID)
			}
		}
		for _, source := range session.Provenance {
			item.provenanceCounts[source]++
		}
	}

	out := make([]CandidateWorkitemSnapshot, 0, len(groups))
	for _, item := range groups {
		confidence := item.minConfidence
		if confidence == "" {
			confidence = summarizeConfidence(item.confidenceCounts, item.sessionCount)
		}
		if item.sessionCount > 1 && confidence == "high" {
			confidence = "medium"
		}
		sort.Strings(item.sessionIDs)
		reasons := []string{"grouped only by project + tool + freshness bucket"}
		if item.sessionCount > 1 {
			reasons = append(reasons, fmt.Sprintf("%d sessions share this conservative bucket", item.sessionCount))
		}
		if item.lowConfidenceCount > 0 {
			reasons = append(reasons, fmt.Sprintf("%d sessions are low-confidence", item.lowConfidenceCount))
		}
		if item.missingTranscriptCount > 0 {
			reasons = append(reasons, fmt.Sprintf("%d sessions are missing transcript timing", item.missingTranscriptCount))
		}
		out = append(out, CandidateWorkitemSnapshot{
			Key:                             item.key,
			Project:                         item.project,
			Tool:                            item.tool,
			FreshnessBucket:                 item.freshnessBucket,
			SessionCount:                    item.sessionCount,
			ProcessCount:                    item.processCount + len(item.processPIDs),
			SessionIDs:                      item.sessionIDs,
			Canonical:                       false,
			InferenceMode:                   "project_tool_freshness",
			FallbackView:                    "project_anchored",
			Confidence:                      confidence,
			ConfidenceReasons:               reasons,
			ProjectAttributionConfidence:    summarizeConfidence(item.projectAttributionConfidenceCounts, item.sessionCount),
			ProjectAttributionReasons:       buildProjectAttributionReasons(item.projectAttributionSourceCounts),
			ProjectAttributionSourceSummary: buildProjectAttributionSourceSummary(item.projectAttributionSourceCounts),
			ProvenanceSummary:               buildProvenanceSummary(item.provenanceCounts),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if freshnessRank(out[i].FreshnessBucket) != freshnessRank(out[j].FreshnessBucket) {
			return freshnessRank(out[i].FreshnessBucket) < freshnessRank(out[j].FreshnessBucket)
		}
		if out[i].SessionCount != out[j].SessionCount {
			return out[i].SessionCount > out[j].SessionCount
		}
		if out[i].ProcessCount != out[j].ProcessCount {
			return out[i].ProcessCount > out[j].ProcessCount
		}
		if out[i].Project != out[j].Project {
			return out[i].Project < out[j].Project
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Key < out[j].Key
	})
	return out
}

type duplicateOverlapCluster struct {
	Project      string
	Tool         string
	Freshness    string
	SessionCount int
}

func buildCoordinationRisk(processes []LiveProcessSnapshot, sessions []LiveSessionSnapshot, projects []ProjectSnapshot, candidateWorkitems []CandidateWorkitemSnapshot, current CurrentMetrics, historicPeaks HistoricPeaks, now time.Time, idleGap time.Duration) CoordinationRiskSnapshot {
	risk := CoordinationRiskSnapshot{
		Posture:                              "observed",
		RecentWindowMinutes:                  int(recentSessionWindow(idleGap) / time.Minute),
		CandidateWorkitemConfidenceBreakdown: []ConfidenceCountSnapshot{},
		Signals:                              []RiskSignalSnapshot{},
	}
	projectAnchoredProjects := projectAnchoredProjectSnapshots(projects)
	projectAnchoredSessions := projectAnchoredLiveSessions(sessions)
	risk.ActiveProjectCount, risk.RecentProjectCount, risk.TopProject, risk.TopProjectAttentionSharePct = summarizeProjectLoad(projectAnchoredProjects)
	risk.CandidateWorkitemCount = len(candidateWorkitems)
	risk.CandidateWorkitemCoveredSessionCount, risk.CandidateWorkitemCoveragePct, risk.CandidateWorkitemConfidenceBreakdown = summarizeCandidateWorkitems(candidateWorkitems, len(sessions))
	duplicateOverlapSuspicionCount, duplicateOverlapClusterCount, duplicateClusters := summarizeDuplicateOverlapSuspicion(sessions)
	risk.DuplicateOverlapSuspicionCount = duplicateOverlapSuspicionCount
	risk.DuplicateOverlapClusterCount = duplicateOverlapClusterCount
	lowConfidenceMappingCount := 0
	missingTranscriptCount := 0
	for _, session := range sessions {
		if session.Freshness == "stale" {
			risk.StaleSessionCount++
		}
		lowConfidence := session.Confidence == "low"
		missingTranscript := session.MissingTranscript
		if lowConfidence {
			lowConfidenceMappingCount++
		}
		if missingTranscript {
			missingTranscriptCount++
		}
		if lowConfidence || missingTranscript {
			risk.LowConfidenceSessionCount++
		}
	}
	for _, process := range processes {
		if process.MappedSessions == 0 {
			risk.OrphanProcessCount++
		}
	}
	for _, project := range projects {
		risk.ChurnSessionCount += project.RecentSessionCount
	}
	risk.ProjectSpreadCount = len(projectAnchoredProjects)
	if risk.ProjectSpreadCount > 1 && len(projectAnchoredSessions) > 1 {
		risk.FragmentationPct = ratioPct(risk.ProjectSpreadCount, len(projectAnchoredSessions))
	}
	risk.LoadPeakValue, risk.LoadPeakSource, risk.LoadPeakAt = observedLoadPeak(current.SessionConcurrency, historicPeaks, now)
	risk.LoadRatioPct = ratioPct(current.SessionConcurrency, risk.LoadPeakValue)

	competingProjects := competingProjectCount(risk.ActiveProjectCount, risk.RecentProjectCount, len(projectAnchoredProjects))
	if risk.TopProject != "" {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "top_project_share",
			Severity: "observed",
			Evidence: fmt.Sprintf("Project %s has %.1f%% of the observed project slice; other live or recent projects: %d.", risk.TopProject, risk.TopProjectAttentionSharePct, competingProjects),
		})
	}
	if risk.StaleSessionCount > 0 {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "sessions_without_recent_event",
			Severity: "observed",
			Evidence: fmt.Sprintf("%d live sessions have last transcript event older than %s.", risk.StaleSessionCount, formatDurationLabel(staleSessionThreshold(idleGap))),
		})
	}
	if risk.OrphanProcessCount > 0 {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "unmatched_processes",
			Severity: "observed",
			Evidence: fmt.Sprintf("%d visible live processes are not currently matched to local session evidence.", risk.OrphanProcessCount),
		})
	}
	if risk.ChurnSessionCount > 0 {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "recent_sessions",
			Severity: "observed",
			Evidence: fmt.Sprintf("%d live sessions started within the last %dm.", risk.ChurnSessionCount, risk.RecentWindowMinutes),
		})
	}
	if risk.ProjectSpreadCount > 1 && len(projectAnchoredSessions) > 1 {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "project_spread",
			Severity: "observed",
			Evidence: fmt.Sprintf("%d project-anchored live sessions span %d projects (%.1f%% spread).", len(projectAnchoredSessions), risk.ProjectSpreadCount, risk.FragmentationPct),
		})
	}
	if risk.LoadPeakValue > 0 && current.SessionConcurrency > 0 {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "observed_peak_ratio",
			Severity: "observed",
			Evidence: fmt.Sprintf("Current live sessions are %.1f%% of the observed peak (%d/%d from %s).", risk.LoadRatioPct, current.SessionConcurrency, risk.LoadPeakValue, loadPeakSourceLabel(risk.LoadPeakSource)),
		})
	}
	if risk.DuplicateOverlapSuspicionCount > 0 || risk.DuplicateOverlapClusterCount > 0 {
		evidence := fmt.Sprintf("%d extra active/idle sessions cluster in %d conservative project/tool buckets", risk.DuplicateOverlapSuspicionCount, risk.DuplicateOverlapClusterCount)
		if details := formatDuplicateOverlapClusters(duplicateClusters); details != "" {
			evidence = fmt.Sprintf("%s (%s)", evidence, details)
		}
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "duplicate_overlap_candidates",
			Severity: "observed",
			Evidence: evidence + ". This is overlap suspicion only, not semantic identity.",
		})
	}
	if risk.LowConfidenceSessionCount > 0 {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "low_confidence_mapping",
			Severity: "observed",
			Evidence: fmt.Sprintf("%d live sessions have low-confidence mapping or missing transcript timing (%d low-confidence, %d missing transcript timing; overlaps deduplicated).", risk.LowConfidenceSessionCount, lowConfidenceMappingCount, missingTranscriptCount),
		})
	}
	if len(sessions) > 0 {
		risk.Signals = append(risk.Signals, RiskSignalSnapshot{
			Kind:     "candidate_workitem_coverage",
			Severity: "observed",
			Evidence: fmt.Sprintf("Candidate workitems anchor %.1f%% of live sessions to a project (%d/%d across %d buckets); confidence mix: %s.", risk.CandidateWorkitemCoveragePct, risk.CandidateWorkitemCoveredSessionCount, len(sessions), risk.CandidateWorkitemCount, formatConfidenceBreakdown(risk.CandidateWorkitemConfidenceBreakdown)),
		})
	}
	risk.Posture = coordinationPosture(risk.Signals)
	return risk
}

func summarizeProjectLoad(projects []ProjectSnapshot) (activeProjectCount, recentProjectCount int, topProject string, topProjectAttentionSharePct float64) {
	totalAttentionWeight := 0.0
	topIndex := -1
	for i, project := range projects {
		if !hasAssignedProject(project.Project) {
			continue
		}
		if project.ActiveBurstCount > 0 {
			activeProjectCount++
		}
		if project.RecentSessionCount > 0 {
			recentProjectCount++
		}
		totalAttentionWeight += projectAttentionWeight(project)
		if topIndex == -1 || projectBeatsForAttentionTop(project, projects[topIndex]) {
			topIndex = i
		}
	}
	if topIndex >= 0 {
		topProject = projects[topIndex].Project
		topAttentionWeight := projectAttentionWeight(projects[topIndex])
		if totalAttentionWeight > 0 {
			topProjectAttentionSharePct = ratioPctFloat(topAttentionWeight, totalAttentionWeight)
		} else {
			topProjectAttentionSharePct = projects[topIndex].AttentionSharePct
		}
	}
	return activeProjectCount, recentProjectCount, topProject, topProjectAttentionSharePct
}

func projectBeatsForAttentionTop(candidate, current ProjectSnapshot) bool {
	if projectAttentionWeight(candidate) != projectAttentionWeight(current) {
		return projectAttentionWeight(candidate) > projectAttentionWeight(current)
	}
	if candidate.ActiveBurstCount != current.ActiveBurstCount {
		return candidate.ActiveBurstCount > current.ActiveBurstCount
	}
	if candidate.SessionCount != current.SessionCount {
		return candidate.SessionCount > current.SessionCount
	}
	if candidate.ProcessCount != current.ProcessCount {
		return candidate.ProcessCount > current.ProcessCount
	}
	return candidate.Project < current.Project
}

func summarizeDuplicateOverlapSuspicion(sessions []LiveSessionSnapshot) (int, int, []duplicateOverlapCluster) {
	type clusterKey struct {
		project   string
		tool      string
		freshness string
	}

	groups := map[clusterKey]int{}
	for _, session := range sessions {
		project := strings.TrimSpace(session.Project)
		tool := strings.TrimSpace(session.Tool)
		if !hasAssignedProject(project) || tool == "" {
			continue
		}
		if session.Freshness != "active" && session.Freshness != "idle" {
			continue
		}
		key := clusterKey{
			project:   project,
			tool:      tool,
			freshness: session.Freshness,
		}
		groups[key]++
	}

	suspicionCount := 0
	clusters := []duplicateOverlapCluster{}
	for key, count := range groups {
		if count <= 1 {
			continue
		}
		suspicionCount += count - 1
		clusters = append(clusters, duplicateOverlapCluster{
			Project:      key.project,
			Tool:         key.tool,
			Freshness:    key.freshness,
			SessionCount: count,
		})
	}
	sort.Slice(clusters, func(i, j int) bool {
		if clusters[i].SessionCount != clusters[j].SessionCount {
			return clusters[i].SessionCount > clusters[j].SessionCount
		}
		if freshnessRank(clusters[i].Freshness) != freshnessRank(clusters[j].Freshness) {
			return freshnessRank(clusters[i].Freshness) < freshnessRank(clusters[j].Freshness)
		}
		if clusters[i].Project != clusters[j].Project {
			return clusters[i].Project < clusters[j].Project
		}
		return clusters[i].Tool < clusters[j].Tool
	})
	return suspicionCount, len(clusters), clusters
}

func formatDuplicateOverlapClusters(clusters []duplicateOverlapCluster) string {
	if len(clusters) == 0 {
		return ""
	}
	parts := make([]string, 0, len(clusters))
	for i, cluster := range clusters {
		if i >= 3 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s/%s %s x%d", cluster.Project, cluster.Tool, cluster.Freshness, cluster.SessionCount))
	}
	if len(clusters) > 3 {
		parts = append(parts, fmt.Sprintf("+%d more", len(clusters)-3))
	}
	return strings.Join(parts, "; ")
}

func summarizeCandidateWorkitems(items []CandidateWorkitemSnapshot, sessionCount int) (coveredSessionCount int, coveragePct float64, confidenceBreakdown []ConfidenceCountSnapshot) {
	confidenceCounts := map[string]int{}
	for _, item := range items {
		confidenceCounts[item.Confidence]++
		if candidateWorkitemAnchorsProject(item) {
			coveredSessionCount += item.SessionCount
		}
	}
	return coveredSessionCount, ratioPct(coveredSessionCount, sessionCount), buildConfidenceBreakdown(confidenceCounts)
}

func candidateWorkitemAnchorsProject(item CandidateWorkitemSnapshot) bool {
	return hasAssignedProject(item.Project)
}

func hasAssignedProject(project string) bool {
	switch strings.ToLower(strings.TrimSpace(project)) {
	case "", "unassigned":
		return false
	default:
		return true
	}
}

func projectAnchoredProjectSnapshots(projects []ProjectSnapshot) []ProjectSnapshot {
	out := make([]ProjectSnapshot, 0, len(projects))
	for _, project := range projects {
		if hasAssignedProject(project.Project) {
			out = append(out, project)
		}
	}
	return out
}

func projectAnchoredLiveSessions(sessions []LiveSessionSnapshot) []LiveSessionSnapshot {
	out := make([]LiveSessionSnapshot, 0, len(sessions))
	for _, session := range sessions {
		if hasAssignedProject(session.Project) {
			out = append(out, session)
		}
	}
	return out
}

func projectAttentionWeight(project ProjectSnapshot) float64 {
	switch project.AttentionBasis {
	case "process_count":
		if project.ProcessCount > 0 {
			return float64(project.ProcessCount)
		}
	case "session_count":
		if project.SessionCount > 0 {
			return float64(project.SessionCount)
		}
	}
	if project.AttentionSharePct > 0 {
		return project.AttentionSharePct
	}
	if project.SessionCount > 0 {
		return float64(project.SessionCount)
	}
	if project.ProcessCount > 0 {
		return float64(project.ProcessCount)
	}
	return 0
}

func formatConfidenceBreakdown(breakdown []ConfidenceCountSnapshot) string {
	if len(breakdown) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(breakdown))
	for _, item := range breakdown {
		parts = append(parts, fmt.Sprintf("%d %s", item.Count, item.Level))
	}
	return strings.Join(parts, ", ")
}

func competingProjectCount(activeProjectCount, recentProjectCount, projectCount int) int {
	switch {
	case activeProjectCount > 1:
		return activeProjectCount - 1
	case recentProjectCount > 1:
		return recentProjectCount - 1
	case projectCount > 1:
		return projectCount - 1
	default:
		return 0
	}
}

func summarizeConfidence(counts map[string]int, total int) string {
	if total <= 0 {
		return ""
	}
	if counts["high"] == total {
		return "high"
	}
	if counts["low"] == total {
		return "low"
	}
	return "medium"
}

func buildConfidenceBreakdown(counts map[string]int) []ConfidenceCountSnapshot {
	order := []string{"high", "medium", "low"}
	out := make([]ConfidenceCountSnapshot, 0, len(order))
	for _, level := range order {
		if count := counts[level]; count > 0 {
			out = append(out, ConfidenceCountSnapshot{
				Level: level,
				Count: count,
			})
		}
	}
	return out
}

func projectConfidenceReasons(counts map[string]int, missingTranscriptCount int) []string {
	reasons := []string{}
	if counts["low"] > 0 {
		reasons = append(reasons, fmt.Sprintf("%d session mappings are low-confidence", counts["low"]))
	}
	if missingTranscriptCount > 0 {
		reasons = append(reasons, fmt.Sprintf("%d sessions are missing transcript timing", missingTranscriptCount))
	}
	if counts["high"] > 0 && (counts["medium"] > 0 || counts["low"] > 0) {
		reasons = append(reasons, "project mixes direct transcript-path evidence with weaker mappings")
	}
	return reasons
}

func buildProvenanceSummary(counts map[string]int) []ProvenanceCountSnapshot {
	order := []string{"transcript_path", "transcript_activity", "command_hint", "fallback_session_id"}
	out := make([]ProvenanceCountSnapshot, 0, len(order))
	for _, source := range order {
		if count := counts[source]; count > 0 {
			out = append(out, ProvenanceCountSnapshot{
				Source: source,
				Count:  count,
			})
		}
	}
	return out
}

func ratioPct(numerator, denominator int) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return float64(numerator*1000) / float64(denominator) / 10
}

func ratioPctFloat(numerator, denominator float64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	return numerator * 100 / denominator
}

func confidenceRank(level string) int {
	switch level {
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	default:
		return 0
	}
}

func observedLoadPeak(currentSessions int, historicPeaks HistoricPeaks, now time.Time) (int, string, string) {
	bestValue := 0
	bestSource := ""
	bestAt := ""
	if currentSessions > 0 {
		bestValue = currentSessions
		bestSource = "current_snapshot"
		bestAt = now.Format(time.RFC3339)
	}
	if historicPeaks.Today.SessionConcurrency.Value > bestValue {
		bestValue = historicPeaks.Today.SessionConcurrency.Value
		bestSource = "today_transcript_peak"
		bestAt = historicPeaks.Today.SessionConcurrency.At
	}
	if historicPeaks.SevenDay.SessionConcurrency.Value > bestValue {
		bestValue = historicPeaks.SevenDay.SessionConcurrency.Value
		bestSource = "seven_day_transcript_peak"
		bestAt = historicPeaks.SevenDay.SessionConcurrency.At
	}
	return bestValue, bestSource, bestAt
}

func loadPeakSourceLabel(source string) string {
	switch source {
	case "today_transcript_peak":
		return "today transcript peak"
	case "seven_day_transcript_peak":
		return "7D transcript peak"
	case "current_snapshot":
		return "current snapshot"
	default:
		return "observed peak"
	}
}

func formatDurationLabel(duration time.Duration) string {
	if duration <= 0 {
		return "0s"
	}
	if duration%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(duration/time.Hour))
	}
	if duration%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(duration/time.Minute))
	}
	return fmt.Sprintf("%ds", int(duration/time.Second))
}

func coordinationPosture(signals []RiskSignalSnapshot) string {
	return "observed"
}

func traceProjectAttribution(trace *SessionTrace) (project, source, reason string) {
	if trace == nil {
		return "", "", ""
	}
	project = trustedTraceProjectName(trace.Project)
	if project == "" {
		return "", "", ""
	}
	source = strings.TrimSpace(trace.ProjectSource)
	if source == "" {
		source = "transcript_project"
	}
	switch source {
	case "transcript_project":
		return project, source, "parsed transcript provided an explicit project name"
	case "transcript_cwd":
		return project, source, "parsed transcript cwd resolved to the project directory"
	case "transcript_path":
		return project, source, "project derived from the transcript storage path"
	case "config_root_parent":
		return project, source, "fell back to the parent of the local config root"
	default:
		return project, "transcript_project", "parsed transcript provided project evidence"
	}
}

func configRootProjectAttribution(path string) (project, reason string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." || path == string(filepath.Separator) {
		return "", ""
	}
	for _, marker := range []string{".codex", ".claude", ".trae/cli"} {
		var root string
		if marker == ".trae/cli" {
			root = traeRootFromPath(path)
		} else {
			root = configRootFromPath(path, marker)
		}
		if root == "" {
			continue
		}
		parent := filepath.Dir(root)
		if name := trustedPathProjectName(parent); name != "" {
			return name, fmt.Sprintf("fell back to the parent of the local %s config root", marker)
		}
		return "", configRootUnassignedReason(marker, parent)
	}
	if isGenericTemporaryPath(path) {
		return "", "generic temporary path does not identify a project"
	}
	return "", ""
}

func trustedTraceProjectName(name string) string {
	name = strings.TrimSpace(name)
	switch strings.ToLower(name) {
	case "", "unknown", "unassigned":
		return ""
	default:
		return name
	}
}

func trustedPathProjectName(path string) string {
	name, _ := pathProjectName(path)
	return name
}

func pathProjectName(path string) (string, string) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." || path == string(filepath.Separator) {
		return "", "path is empty or root"
	}
	path = normalizeProjectAttributionPath(path)
	if parent := strings.ToLower(filepath.Base(filepath.Dir(path))); parent == "users" || parent == "home" || parent == "profiles" {
		return "", "path is anchored under a home/global directory"
	}
	name := strings.TrimSpace(filepath.Base(path))
	if name == "" || name == "." || name == string(filepath.Separator) || strings.HasPrefix(name, ".") {
		return "", "path does not end in a stable project directory"
	}
	switch strings.ToLower(name) {
	case "sessions", "archived_sessions", "projects", "tmp", "unknown", "unassigned":
		return "", fmt.Sprintf("path ends in generic directory %q", name)
	default:
		return name, ""
	}
}

func normalizeProjectAttributionPath(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i, part := range parts {
		if part != ".benchmark" || i == 0 {
			continue
		}
		prefix := strings.Join(parts[:i], string(filepath.Separator))
		if filepath.IsAbs(path) {
			return string(filepath.Separator) + strings.TrimPrefix(prefix, string(filepath.Separator))
		}
		return prefix
	}
	return path
}

func configRootUnassignedReason(marker, parent string) string {
	_, reason := pathProjectName(parent)
	switch {
	case strings.Contains(reason, "home/global directory"):
		return fmt.Sprintf("local %s config root is anchored under a home/global directory, so project stays unassigned", marker)
	case reason != "":
		return fmt.Sprintf("parent of the local %s config root is generic, so project stays unassigned", marker)
	default:
		return fmt.Sprintf("local %s config root parent was not trusted, so project stays unassigned", marker)
	}
}

func isGenericTemporaryPath(path string) bool {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "" || path == "." || path == string(filepath.Separator) {
		return false
	}
	for _, prefix := range []string{"/tmp", "/private/tmp", "/var/tmp", "/var/folders", "/private/var/folders"} {
		if path == prefix || strings.HasPrefix(path, prefix+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func projectAttributionSourceRank(source string) int {
	switch source {
	case "transcript_project":
		return 5
	case "transcript_cwd":
		return 4
	case "transcript_path":
		return 3
	case "config_root_parent":
		return 2
	case "unassigned":
		return 1
	default:
		return 0
	}
}

func projectAttributionConfidence(source string) string {
	switch source {
	case "transcript_project", "transcript_cwd":
		return "high"
	case "transcript_path":
		return "medium"
	case "config_root_parent", "unassigned":
		return "low"
	default:
		return "low"
	}
}

func projectAttributionConfidenceForProject(project string) string {
	return projectAttributionConfidence(projectAttributionSourceForProject(project))
}

func projectAttributionSourceForProject(project string) string {
	switch strings.ToLower(strings.TrimSpace(project)) {
	case "", "unassigned":
		return "unassigned"
	default:
		return "transcript_project"
	}
}

func buildProjectAttributionSourceSummary(counts map[string]int) []AttributionSourceCountSnapshot {
	order := []string{"transcript_project", "transcript_cwd", "transcript_path", "config_root_parent", "unassigned"}
	out := make([]AttributionSourceCountSnapshot, 0, len(order))
	for _, source := range order {
		if count := counts[source]; count > 0 {
			out = append(out, AttributionSourceCountSnapshot{
				Source: source,
				Count:  count,
			})
		}
	}
	return out
}

func projectAttributionStrengthCount(counts map[string]int) int {
	strengths := map[string]struct{}{}
	for _, source := range []string{"transcript_project", "transcript_cwd", "transcript_path", "config_root_parent", "unassigned"} {
		if counts[source] <= 0 {
			continue
		}
		strengths[projectAttributionConfidence(source)] = struct{}{}
	}
	return len(strengths)
}

func buildProjectAttributionReasons(counts map[string]int) []string {
	reasons := []string{}
	if counts["config_root_parent"] > 0 {
		reasons = append(reasons, fmt.Sprintf("%d sessions rely on config-root parent fallback", counts["config_root_parent"]))
	}
	if counts["transcript_path"] > 0 {
		reasons = append(reasons, fmt.Sprintf("%d sessions derive project from transcript storage paths", counts["transcript_path"]))
	}
	if counts["unassigned"] > 0 {
		reasons = append(reasons, fmt.Sprintf("%d sessions stayed unassigned", counts["unassigned"]))
	}
	if projectAttributionStrengthCount(counts) > 1 {
		reasons = append(reasons, "project attribution mixes multiple evidence strengths")
	}
	return uniqueSortedStrings(reasons)
}

func mergeStringSets(base, extra []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, value := range append(append([]string{}, base...), extra...) {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func uniqueSortedStrings(items []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
