package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBuildLiveSessionsTracksMappingEvidence(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	tracePath := "/tmp/trace-session.jsonl"
	fallbackPath := "/tmp/rollout-2026-06-28T10-00-00-fallback-session.jsonl"
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			tracePath: {
				Tool:       "codex",
				Path:       tracePath,
				SessionID:  "trace-session",
				Project:    "alpha",
				EventTimes: []time.Time{now},
				FirstEvent: now,
				LastEvent:  now,
			},
		},
	}
	processes := []LiveProcess{
		{
			PID:          101,
			Tool:         "codex",
			SessionFiles: []TranscriptFile{{Tool: "codex", Path: tracePath}},
		},
		{
			PID:          102,
			Tool:         "codex",
			SessionHints: []string{"trace-session"},
		},
		{
			PID:          103,
			Tool:         "codex",
			SessionFiles: []TranscriptFile{{Tool: "codex", Path: fallbackPath}},
		},
	}

	sessions, _ := buildLiveSessions(processes, data)
	if len(sessions) != 2 {
		t.Fatalf("expected 2 live sessions, got %d", len(sessions))
	}

	traceSession := requireLiveSession(t, sessions, "trace-session")
	if !traceSession.Mapping.TranscriptPath {
		t.Fatalf("expected trace-backed session to record transcript_path provenance")
	}
	if !traceSession.Mapping.ParsedTranscriptID {
		t.Fatalf("expected trace-backed session to record parsed transcript evidence")
	}
	if !traceSession.Mapping.CommandHint {
		t.Fatalf("expected trace-backed session to preserve command_hint provenance")
	}
	if traceSession.Mapping.FallbackSessionID {
		t.Fatalf("expected trace-backed session not to need fallback session id")
	}

	fallbackSession := requireLiveSession(t, sessions, "fallback-session")
	if !fallbackSession.Mapping.TranscriptPath {
		t.Fatalf("expected fallback session to preserve transcript_path provenance")
	}
	if fallbackSession.Mapping.ParsedTranscriptID {
		t.Fatalf("expected fallback session not to claim parsed transcript evidence")
	}
	if !fallbackSession.Mapping.FallbackSessionID {
		t.Fatalf("expected fallback session to disclose fallback session id provenance")
	}
	if fallbackSession.Trace != nil {
		t.Fatalf("expected fallback session to remain without transcript timing")
	}
}

func TestBuildLiveSessionsMergesFallbackTranscriptAndMatchingHint(t *testing.T) {
	fallbackPath := "/tmp/rollout-2026-06-28T10-00-00-fallback-session.jsonl"
	processes := []LiveProcess{
		{
			PID:          101,
			Tool:         "codex",
			SessionFiles: []TranscriptFile{{Tool: "codex", Path: fallbackPath}},
			SessionHints: []string{"fallback-session"},
		},
	}

	sessions, _ := buildLiveSessions(processes, &TranscriptData{Traces: map[string]*SessionTrace{}})
	if len(sessions) != 1 {
		t.Fatalf("expected 1 merged live session, got %d", len(sessions))
	}

	session := requireLiveSession(t, sessions, "fallback-session")
	if session.Path != fallbackPath {
		t.Fatalf("expected merged session to keep transcript path %q, got %q", fallbackPath, session.Path)
	}
	if len(session.Processes) != 1 {
		t.Fatalf("expected one process counted once, got %d", len(session.Processes))
	}
	if !session.Mapping.TranscriptPath || !session.Mapping.CommandHint || !session.Mapping.FallbackSessionID {
		t.Fatalf("expected merged session provenance to keep transcript_path, command_hint, and fallback_session_id: %#v", session.Mapping)
	}
	if session.Mapping.ParsedTranscriptID {
		t.Fatalf("expected fallback + hint session not to claim parsed transcript evidence")
	}
}

func TestBuildLiveSessionsParsedTranscriptIDWinsOverConflictingHint(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	tracePath := "/tmp/trace-session.jsonl"
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			tracePath: {
				Tool:       "codex",
				Path:       tracePath,
				SessionID:  "trace-session",
				EventTimes: []time.Time{now},
				FirstEvent: now.Add(-time.Minute),
				LastEvent:  now,
			},
		},
	}
	processes := []LiveProcess{
		{
			PID:          201,
			Tool:         "codex",
			SessionFiles: []TranscriptFile{{Tool: "codex", Path: tracePath}},
			SessionHints: []string{"conflicting-session"},
		},
	}

	sessions, notes := buildLiveSessions(processes, data)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 live session after ignoring the weaker hint, got %d", len(sessions))
	}

	session := requireLiveSession(t, sessions, "trace-session")
	if !session.Mapping.TranscriptPath || !session.Mapping.ParsedTranscriptID {
		t.Fatalf("expected parsed transcript evidence to win, got %#v", session.Mapping)
	}
	if session.Mapping.CommandHint {
		t.Fatalf("expected conflicting command hint to be ignored for mapping provenance")
	}
	if !slices.Contains(notes, `PID 201 codex ignored weaker command hint "conflicting-session" because parsed transcript session id "trace-session" won.`) {
		t.Fatalf("expected conflict note, got %#v", notes)
	}

	processSnapshots := projectLiveProcesses(processes, data)
	if len(processSnapshots) != 1 {
		t.Fatalf("expected 1 process snapshot, got %d", len(processSnapshots))
	}
	if processSnapshots[0].MappedSessions != 1 {
		t.Fatalf("expected 1 mapped session after normalization, got %d", processSnapshots[0].MappedSessions)
	}
	if !slices.Equal(processSnapshots[0].SessionIDs, []string{"trace-session"}) {
		t.Fatalf("unexpected normalized process session ids: %#v", processSnapshots[0].SessionIDs)
	}
}

func TestBuildLiveSessionsParsedTranscriptIDSuppressesConflictingHintFromSiblingFallbackFile(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	tracePath := "/tmp/trace-session.jsonl"
	fallbackPath := "/tmp/rollout-2026-06-28T10-00-00-fallback-session.jsonl"
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			tracePath: {
				Tool:       "codex",
				Path:       tracePath,
				SessionID:  "trace-session",
				EventTimes: []time.Time{now},
				FirstEvent: now.Add(-time.Minute),
				LastEvent:  now,
			},
		},
	}
	processes := []LiveProcess{
		{
			PID:  202,
			Tool: "codex",
			SessionFiles: []TranscriptFile{
				{Tool: "codex", Path: tracePath},
				{Tool: "codex", Path: fallbackPath},
			},
			SessionHints: []string{"conflicting-session"},
		},
	}

	sessions, notes := buildLiveSessions(processes, data)
	if len(sessions) != 1 {
		t.Fatalf("expected only the parsed live session to survive normalization, got %d", len(sessions))
	}

	session := requireLiveSession(t, sessions, "trace-session")
	if !session.Mapping.TranscriptPath || !session.Mapping.ParsedTranscriptID {
		t.Fatalf("expected parsed transcript evidence to win, got %#v", session.Mapping)
	}
	if session.Mapping.CommandHint {
		t.Fatalf("expected conflicting command hint to stay ignored, got %#v", session.Mapping)
	}
	if !slices.Contains(notes, `PID 202 codex ignored weaker command hint "conflicting-session" because parsed transcript session id "trace-session" won.`) {
		t.Fatalf("expected ignored weaker hint note, got %#v", notes)
	}

	processSnapshots := projectLiveProcesses(processes, data)
	if len(processSnapshots) != 1 {
		t.Fatalf("expected 1 process snapshot, got %d", len(processSnapshots))
	}
	if processSnapshots[0].MappedSessions != 1 {
		t.Fatalf("expected 1 mapped session after normalization, got %d", processSnapshots[0].MappedSessions)
	}
	if !slices.Equal(processSnapshots[0].SessionIDs, []string{"trace-session"}) {
		t.Fatalf("unexpected normalized process session ids: %#v", processSnapshots[0].SessionIDs)
	}
}

func TestBuildLiveSessionsPrefersCommandHintOverFilenameFallback(t *testing.T) {
	fallbackPath := "/tmp/rollout-2026-06-28T10-00-00-fallback-session.jsonl"
	processes := []LiveProcess{
		{
			PID:          202,
			Tool:         "codex",
			SessionFiles: []TranscriptFile{{Tool: "codex", Path: fallbackPath}},
			SessionHints: []string{"hint-session"},
		},
	}

	sessions, notes := buildLiveSessions(processes, &TranscriptData{Traces: map[string]*SessionTrace{}})
	if len(sessions) != 1 {
		t.Fatalf("expected 1 live session, got %d", len(sessions))
	}

	session := requireLiveSession(t, sessions, "hint-session")
	if !session.Mapping.TranscriptPath {
		t.Fatalf("expected transcript_path provenance to remain visible")
	}
	if session.Mapping.ParsedTranscriptID {
		t.Fatalf("expected command-hint mapping not to claim parsed transcript evidence")
	}
	if !session.Mapping.CommandHint {
		t.Fatalf("expected command hint to become the primary session-id evidence")
	}
	if session.Mapping.FallbackSessionID {
		t.Fatalf("expected differing filename fallback to remain secondary and not claim session-id provenance")
	}
	if session.Trace != nil {
		t.Fatalf("expected command-hint mapping without parsed trace to remain untraced")
	}
	if !slices.Contains(notes, `PID 202 codex ignored weaker filename-derived fallback session id "fallback-session" from /tmp/rollout-2026-06-28T10-00-00-fallback-session.jsonl in favor of command hint "hint-session".`) {
		t.Fatalf("expected fallback conflict note, got %#v", notes)
	}

	processSnapshots := projectLiveProcesses(processes, &TranscriptData{Traces: map[string]*SessionTrace{}})
	if len(processSnapshots) != 1 {
		t.Fatalf("expected 1 process snapshot, got %d", len(processSnapshots))
	}
	if processSnapshots[0].MappedSessions != 1 {
		t.Fatalf("expected 1 mapped session after normalization, got %d", processSnapshots[0].MappedSessions)
	}
	if !slices.Equal(processSnapshots[0].SessionIDs, []string{"hint-session"}) {
		t.Fatalf("unexpected normalized process session ids: %#v", processSnapshots[0].SessionIDs)
	}
}

func TestBuildLiveSessionsIncludesRecentTranscriptOnlySubagents(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	tracePath := "/tmp/subagent.jsonl"
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			tracePath: {
				Tool:             "trae",
				Path:             tracePath,
				SessionID:        "subagent-session",
				ThreadSource:     "subagent",
				ParentThreadID:   "main-session",
				AgentRole:        "reviewer",
				AgentNickname:    "Hilbert",
				RoleHintSource:   "transcript_metadata",
				Project:          "agentload",
				ProjectSource:    "transcript_cwd",
				EventTimes:       []time.Time{now.Add(-30 * time.Second)},
				FirstEvent:       now.Add(-2 * time.Minute),
				LastEvent:        now.Add(-30 * time.Second),
				IndependentlyRun: false,
			},
		},
	}

	sessions, notes := buildLiveSessionsAt(nil, data, 90*time.Second, now)
	if len(sessions) != 1 {
		t.Fatalf("expected 1 transcript-only session, got %d", len(sessions))
	}
	session := requireLiveSession(t, sessions, "subagent-session")
	if len(session.Processes) != 0 {
		t.Fatalf("expected no mapped processes, got %d", len(session.Processes))
	}
	if !session.Mapping.TranscriptPath || !session.Mapping.TranscriptActivity || !session.Mapping.ParsedTranscriptID {
		t.Fatalf("expected transcript-only provenance, got %#v", session.Mapping)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "recent transcript-backed sessions") {
		t.Fatalf("expected transcript-only inclusion note, got %#v", notes)
	}

	snapshots := projectLiveSessions(sessions, 90*time.Second, now)
	item := requireLiveSessionSnapshot(t, snapshots, "subagent-session")
	if !item.ActiveBurst {
		t.Fatalf("expected recent transcript-only session to count as active burst")
	}
	if item.SessionRole != "subagent" || item.RoleConfidence != "high" {
		t.Fatalf("expected high-confidence subagent role, got role=%q confidence=%q reasons=%v", item.SessionRole, item.RoleConfidence, item.RoleReasons)
	}
	if item.ThreadSource != "subagent" || item.ParentThreadID != "main-session" {
		t.Fatalf("expected relationship metadata to be exposed, got thread_source=%q parent_thread_id=%q", item.ThreadSource, item.ParentThreadID)
	}
	if item.AgentRole != "reviewer" || item.AgentNickname != "Hilbert" || item.RoleHintSource != "transcript_metadata" {
		t.Fatalf("expected agent metadata to be exposed, got role=%q nickname=%q hint=%q", item.AgentRole, item.AgentNickname, item.RoleHintSource)
	}
	if item.IndependentlyRun {
		t.Fatalf("expected subagent transcript not to be marked independently resumable")
	}
	if item.ProcessCount != 0 {
		t.Fatalf("expected process count 0 for transcript-only session, got %d", item.ProcessCount)
	}

	summary := buildSnapshotSummary(nil, snapshots, buildProjectFocus(sessions, 90*time.Second, now))
	if summary.ActiveSessions != 1 || summary.SubagentSessions != 1 || summary.MainAgentSessions != 0 {
		t.Fatalf("unexpected summary role/session counts: %#v", summary)
	}
}

func TestBuildLiveSessionsPropagatesHostApps(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	tracePath := "/tmp/host-session.jsonl"
	host := &HostApp{PID: 200, Name: "Terminal", BundlePath: "/Applications/Terminal.app"}
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			tracePath: {
				Tool:       "codex",
				Path:       tracePath,
				SessionID:  "host-session",
				Project:    "agentload",
				EventTimes: []time.Time{now.Add(-10 * time.Second)},
				FirstEvent: now.Add(-2 * time.Minute),
				LastEvent:  now.Add(-10 * time.Second),
			},
		},
	}
	processes := []LiveProcess{
		{
			PID:          501,
			Tool:         "codex",
			Command:      `codex --thread-id host-session`,
			HostApp:      host,
			SessionFiles: []TranscriptFile{{Tool: "codex", Path: tracePath}},
		},
	}

	sessions, notes := buildLiveSessionsAt(processes, data, 90*time.Second, now)
	if len(notes) != 0 {
		t.Fatalf("expected no notes, got %#v", notes)
	}
	session := requireLiveSession(t, sessions, "host-session")
	if len(session.HostApps) != 1 {
		t.Fatalf("expected one host app on live session, got %#v", session.HostApps)
	}
	if got := session.HostApps[host.PID]; got.Name != host.Name || got.BundlePath != host.BundlePath {
		t.Fatalf("unexpected session host app: %#v", got)
	}

	processSnapshots := projectLiveProcesses(processes, data)
	if len(processSnapshots) != 1 || processSnapshots[0].HostApp == nil {
		t.Fatalf("expected host app on process snapshot: %#v", processSnapshots)
	}
	if processSnapshots[0].HostApp.Name != host.Name {
		t.Fatalf("unexpected process host app: %#v", processSnapshots[0].HostApp)
	}

	sessionSnapshots := projectLiveSessions(sessions, 90*time.Second, now)
	item := requireLiveSessionSnapshot(t, sessionSnapshots, "host-session")
	if len(item.HostApps) != 1 {
		t.Fatalf("expected one host app on session snapshot, got %#v", item.HostApps)
	}
	if item.HostApps[0].PID != host.PID || item.HostApps[0].Name != host.Name {
		t.Fatalf("unexpected snapshot host app: %#v", item.HostApps[0])
	}
}

func TestBuildLiveSessionsExcludesOldTranscriptOnlySessions(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	data := &TranscriptData{
		Traces: map[string]*SessionTrace{
			"/tmp/old.jsonl": {
				Tool:       "trae",
				Path:       "/tmp/old.jsonl",
				SessionID:  "old-session",
				EventTimes: []time.Time{now.Add(-2 * time.Hour)},
				FirstEvent: now.Add(-2 * time.Hour),
				LastEvent:  now.Add(-2 * time.Hour),
			},
		},
	}

	sessions, _ := buildLiveSessionsAt(nil, data, 90*time.Second, now)
	if len(sessions) != 0 {
		t.Fatalf("expected old transcript-only sessions to stay out of current sessions, got %d", len(sessions))
	}
}

func TestObserveProjectAttributionUsesTrustedEvidenceOnly(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	projectCodexRoot := filepath.Join(root, "agentload", ".codex")
	projectLaneRoot := filepath.Join(projectCodexRoot, ".codexl", "asagent", "lane")
	homeCodexRoot := filepath.Join(root, "Users", "alice", ".codex")
	claudeProjectsRoot := filepath.Join(root, "Users", "alice", ".claude", "projects", "encoded-project")
	for _, dir := range []string{
		filepath.Join(projectCodexRoot, "sessions"),
		projectLaneRoot,
		filepath.Join(homeCodexRoot, "sessions"),
		claudeProjectsRoot,
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	cases := []struct {
		name           string
		session        LiveSession
		wantProject    string
		wantSource     string
		wantConfidence string
		wantReasons    []string
	}{
		{
			name: "trace project wins",
			session: LiveSession{
				Tool:      "codex",
				SessionID: "trace-project",
				Path:      "/tmp/rollout-2026-06-28T10-00-00-trace-project.jsonl",
				Processes: map[int]struct{}{1: {}},
				Trace: &SessionTrace{
					Project:       "alpha",
					ProjectSource: "transcript_cwd",
					FirstEvent:    now.Add(-2 * time.Minute),
					LastEvent:     now.Add(-time.Minute),
				},
			},
			wantProject:    "alpha",
			wantSource:     "transcript_cwd",
			wantConfidence: "high",
			wantReasons:    []string{"parsed transcript cwd resolved to the project directory"},
		},
		{
			name: "project root from codex config",
			session: LiveSession{
				Tool:      "codex",
				SessionID: "root-project",
				Path:      filepath.Join(projectCodexRoot, "sessions", "root-project.jsonl"),
				Processes: map[int]struct{}{2: {}},
			},
			wantProject:    "agentload",
			wantSource:     "config_root_parent",
			wantConfidence: "low",
			wantReasons:    []string{"fell back to the parent of the local .codex config root"},
		},
		{
			name: "project root from codex lane storage",
			session: LiveSession{
				Tool:      "codex",
				SessionID: "lane-project",
				Path:      filepath.Join(projectLaneRoot, "events.jsonl"),
				Processes: map[int]struct{}{3: {}},
			},
			wantProject:    "agentload",
			wantSource:     "config_root_parent",
			wantConfidence: "low",
			wantReasons:    []string{"fell back to the parent of the local .codex config root"},
		},
		{
			name: "home codex root stays unassigned",
			session: LiveSession{
				Tool:      "codex",
				SessionID: "home-project",
				Path:      filepath.Join(homeCodexRoot, "sessions", "home-project.jsonl"),
				Processes: map[int]struct{}{4: {}},
			},
			wantProject:    "",
			wantSource:     "unassigned",
			wantConfidence: "low",
			wantReasons:    []string{"home/global directory"},
		},
		{
			name: "claude projects anchor stays unassigned",
			session: LiveSession{
				Tool:      "claude",
				SessionID: "claude-project",
				Path:      filepath.Join(claudeProjectsRoot, "events.jsonl"),
				Processes: map[int]struct{}{5: {}},
			},
			wantProject:    "",
			wantSource:     "unassigned",
			wantConfidence: "low",
			wantReasons:    []string{"home/global directory"},
		},
		{
			name: "tmp parent stays unassigned",
			session: LiveSession{
				Tool:      "codex",
				SessionID: "tmp-project",
				Path:      "/tmp/rollout-2026-06-28T10-00-00-tmp-project.jsonl",
				Processes: map[int]struct{}{6: {}},
			},
			wantProject:    "",
			wantSource:     "unassigned",
			wantConfidence: "low",
			wantReasons:    []string{"generic temporary path does not identify a project"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := observeProjectAttribution(tc.session)
			if got.Project != tc.wantProject {
				t.Fatalf("expected project %q, got %q", tc.wantProject, got.Project)
			}
			if got.Source != tc.wantSource {
				t.Fatalf("expected source %q, got %q", tc.wantSource, got.Source)
			}
			if got.Confidence != tc.wantConfidence {
				t.Fatalf("expected confidence %q, got %q", tc.wantConfidence, got.Confidence)
			}
			for _, reason := range tc.wantReasons {
				if !slices.ContainsFunc(got.Reasons, func(item string) bool { return strings.Contains(item, reason) }) {
					t.Fatalf("expected reason containing %q, got %#v", reason, got.Reasons)
				}
			}
			if displayProjectName(tc.session) != tc.wantProject {
				t.Fatalf("displayProjectName() = %q, want %q", displayProjectName(tc.session), tc.wantProject)
			}
		})
	}
}

func TestProjectLiveSessionsExposeFreshnessConfidenceAndProvenance(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	root := t.TempDir()
	projectCodexRoot := filepath.Join(root, "agentload", ".codex")
	if err := os.MkdirAll(filepath.Join(projectCodexRoot, "sessions"), 0o755); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	sessions := []LiveSession{
		{
			Tool:      "codex",
			SessionID: "trace-session",
			Path:      "/tmp/trace-session.jsonl",
			Processes: map[int]struct{}{1: {}},
			Trace: &SessionTrace{
				Project:       "alpha",
				ProjectSource: "transcript_cwd",
				FirstEvent:    now.Add(-10 * time.Minute),
				LastEvent:     now.Add(-30 * time.Second),
			},
			Mapping: LiveSessionMapping{TranscriptPath: true, ParsedTranscriptID: true},
		},
		{
			Tool:      "claude",
			SessionID: "hint-session",
			Path:      "/tmp/hint-session.jsonl",
			Processes: map[int]struct{}{2: {}},
			Trace: &SessionTrace{
				Project:       "beta",
				ProjectSource: "transcript_project",
				FirstEvent:    now.Add(-5 * time.Minute),
				LastEvent:     now.Add(-2 * time.Minute),
			},
			Mapping: LiveSessionMapping{CommandHint: true},
		},
		{
			Tool:      "codex",
			SessionID: "root-session",
			Path:      filepath.Join(projectCodexRoot, "sessions", "root-session.jsonl"),
			Processes: map[int]struct{}{3: {}},
			Mapping:   LiveSessionMapping{TranscriptPath: true, FallbackSessionID: true},
		},
		{
			Tool:      "codex",
			SessionID: "fallback-session",
			Path:      "/tmp/fallback-session.jsonl",
			Processes: map[int]struct{}{4: {}},
			Mapping:   LiveSessionMapping{TranscriptPath: true, FallbackSessionID: true},
		},
		{
			Tool:      "codex",
			SessionID: "stale-session",
			Path:      "/tmp/stale-session.jsonl",
			Processes: map[int]struct{}{5: {}},
			Trace: &SessionTrace{
				Project:       "gamma",
				ProjectSource: "transcript_cwd",
				FirstEvent:    now.Add(-20 * time.Minute),
				LastEvent:     now.Add(-6 * time.Minute),
			},
			Mapping: LiveSessionMapping{TranscriptPath: true, ParsedTranscriptID: true},
		},
	}

	snapshot := projectLiveSessions(sessions, 90*time.Second, now)

	traceSession := requireLiveSessionSnapshot(t, snapshot, "trace-session")
	if traceSession.Freshness != "active" {
		t.Fatalf("expected active freshness, got %s", traceSession.Freshness)
	}
	if traceSession.Confidence != "high" {
		t.Fatalf("expected high confidence, got %s", traceSession.Confidence)
	}
	if traceSession.MappingMethod != "transcript_path" {
		t.Fatalf("expected transcript_path mapping method, got %s", traceSession.MappingMethod)
	}
	if traceSession.MissingTranscript {
		t.Fatalf("expected trace-backed session to keep transcript timing")
	}
	if !traceSession.ActiveBurst {
		t.Fatalf("expected active session to contribute to active burst")
	}
	if !slices.Equal(traceSession.Provenance, []string{"transcript_path"}) {
		t.Fatalf("unexpected provenance: %#v", traceSession.Provenance)
	}
	if traceSession.ProjectAttributionSource != "transcript_cwd" {
		t.Fatalf("expected transcript_cwd attribution, got %s", traceSession.ProjectAttributionSource)
	}
	if traceSession.ProjectAttributionConfidence != "high" {
		t.Fatalf("expected high project attribution confidence, got %s", traceSession.ProjectAttributionConfidence)
	}

	hintSession := requireLiveSessionSnapshot(t, snapshot, "hint-session")
	if hintSession.Freshness != "idle" {
		t.Fatalf("expected idle freshness, got %s", hintSession.Freshness)
	}
	if hintSession.Confidence != "medium" {
		t.Fatalf("expected medium confidence for command-hint mapping, got %s", hintSession.Confidence)
	}
	if hintSession.MappingMethod != "command_hint" {
		t.Fatalf("expected command_hint mapping method, got %s", hintSession.MappingMethod)
	}
	if !slices.Contains(hintSession.ConfidenceReasons, "mapped from command hint instead of a parsed transcript session id") {
		t.Fatalf("expected command-hint reason to stay explicit, got %#v", hintSession.ConfidenceReasons)
	}
	if !slices.Equal(hintSession.Provenance, []string{"command_hint"}) {
		t.Fatalf("unexpected command-hint provenance: %#v", hintSession.Provenance)
	}
	if hintSession.ProjectAttributionSource != "transcript_project" {
		t.Fatalf("expected transcript_project attribution, got %s", hintSession.ProjectAttributionSource)
	}

	rootSession := requireLiveSessionSnapshot(t, snapshot, "root-session")
	if rootSession.Project != "agentload" {
		t.Fatalf("expected config-root fallback project agentload, got %q", rootSession.Project)
	}
	if rootSession.ProjectAttributionSource != "config_root_parent" {
		t.Fatalf("expected config_root_parent attribution, got %s", rootSession.ProjectAttributionSource)
	}
	if rootSession.ProjectAttributionConfidence != "low" {
		t.Fatalf("expected low config-root attribution confidence, got %s", rootSession.ProjectAttributionConfidence)
	}
	if !slices.Contains(rootSession.ProjectAttributionReasons, "fell back to the parent of the local .codex config root") {
		t.Fatalf("expected config-root fallback reason, got %#v", rootSession.ProjectAttributionReasons)
	}

	fallbackSession := requireLiveSessionSnapshot(t, snapshot, "fallback-session")
	if fallbackSession.Freshness != "unknown" {
		t.Fatalf("expected unknown freshness without transcript timing, got %s", fallbackSession.Freshness)
	}
	if fallbackSession.Confidence != "low" {
		t.Fatalf("expected low confidence for fallback session, got %s", fallbackSession.Confidence)
	}
	if fallbackSession.MappingMethod != "fallback_session_id" {
		t.Fatalf("expected fallback_session_id mapping method when no parsed transcript id exists, got %s", fallbackSession.MappingMethod)
	}
	if !fallbackSession.MissingTranscript {
		t.Fatalf("expected fallback session to disclose missing transcript timing")
	}
	if fallbackSession.Project != "" {
		t.Fatalf("expected generic tmp path to stay unassigned, got %q", fallbackSession.Project)
	}
	if fallbackSession.ProjectAttributionSource != "unassigned" {
		t.Fatalf("expected unassigned project attribution, got %s", fallbackSession.ProjectAttributionSource)
	}
	if !slices.Contains(fallbackSession.ProjectAttributionReasons, "generic temporary path does not identify a project") {
		t.Fatalf("expected generic temporary path reason, got %#v", fallbackSession.ProjectAttributionReasons)
	}
	if !slices.Contains(fallbackSession.ConfidenceReasons, "session id falls back to filename-derived evidence") {
		t.Fatalf("expected fallback reason to stay explicit, got %#v", fallbackSession.ConfidenceReasons)
	}
	if !slices.Equal(fallbackSession.Provenance, []string{"transcript_path", "fallback_session_id"}) {
		t.Fatalf("unexpected fallback provenance: %#v", fallbackSession.Provenance)
	}

	staleSession := requireLiveSessionSnapshot(t, snapshot, "stale-session")
	if staleSession.Freshness != "stale" {
		t.Fatalf("expected stale freshness, got %s", staleSession.Freshness)
	}
}

func TestBuildProjectFocusAddsAllocationRiskAndConfidenceSummary(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sessions := []LiveSession{
		{
			Tool:      "codex",
			SessionID: "alpha-active",
			Processes: map[int]struct{}{1: {}, 2: {}},
			Trace: &SessionTrace{
				Project:       "alpha",
				ProjectSource: "transcript_cwd",
				ThreadSource:  "user",
				FirstEvent:    now.Add(-5 * time.Minute),
				LastEvent:     now.Add(-30 * time.Second),
			},
			Mapping: LiveSessionMapping{TranscriptPath: true, ParsedTranscriptID: true},
		},
		{
			Tool:      "codex",
			SessionID: "alpha-stale",
			Processes: map[int]struct{}{3: {}},
			Trace: &SessionTrace{
				Project:       "alpha",
				ProjectSource: "transcript_cwd",
				ThreadSource:  "subagent",
				FirstEvent:    now.Add(-2 * time.Hour),
				LastEvent:     now.Add(-10 * time.Minute),
			},
			Mapping: LiveSessionMapping{TranscriptPath: true, ParsedTranscriptID: true},
		},
		{
			Tool:      "claude",
			SessionID: "beta-active",
			Processes: map[int]struct{}{4: {}},
			Trace: &SessionTrace{
				Project:       "beta",
				ProjectSource: "transcript_cwd",
				ThreadSource:  "user",
				FirstEvent:    now.Add(-4 * time.Minute),
				LastEvent:     now.Add(-1 * time.Minute),
			},
			Mapping: LiveSessionMapping{CommandHint: true},
		},
		{
			Tool:      "codex",
			SessionID: "beta-unknown",
			Processes: map[int]struct{}{5: {}},
			Trace: &SessionTrace{
				Project:       "beta",
				ProjectSource: "config_root_parent",
			},
			Mapping: LiveSessionMapping{TranscriptPath: true, FallbackSessionID: true},
		},
	}

	projects := buildProjectFocus(sessions, 90*time.Second, now)
	if len(projects) != 2 {
		t.Fatalf("expected 2 project snapshots, got %d", len(projects))
	}

	alpha := requireProjectSnapshot(t, projects, "alpha")
	if alpha.AttentionBasis != "process_count" {
		t.Fatalf("expected process_count attention basis, got %s", alpha.AttentionBasis)
	}
	if alpha.AttentionSharePct != 60 {
		t.Fatalf("expected alpha attention share 60.0, got %.1f", alpha.AttentionSharePct)
	}
	if alpha.StaleSessionCount != 1 {
		t.Fatalf("expected 1 stale alpha session, got %d", alpha.StaleSessionCount)
	}
	if alpha.RecentSessionCount != 1 {
		t.Fatalf("expected 1 recent alpha session, got %d", alpha.RecentSessionCount)
	}
	if alpha.MainAgentSessions != 1 || alpha.SubagentSessions != 1 || alpha.UnknownRoleSessions != 0 {
		t.Fatalf("unexpected alpha role split: main=%d subagent=%d unknown=%d", alpha.MainAgentSessions, alpha.SubagentSessions, alpha.UnknownRoleSessions)
	}
	if alpha.Confidence != "high" {
		t.Fatalf("expected alpha confidence high, got %s", alpha.Confidence)
	}
	if alpha.ProjectAttributionConfidence != "high" {
		t.Fatalf("expected alpha project attribution confidence high, got %s", alpha.ProjectAttributionConfidence)
	}
	if !slices.Equal(alpha.ConfidenceBreakdown, []ConfidenceCountSnapshot{{Level: "high", Count: 2}}) {
		t.Fatalf("unexpected alpha confidence breakdown: %#v", alpha.ConfidenceBreakdown)
	}
	if !slices.Equal(alpha.ProjectAttributionSourceSummary, []AttributionSourceCountSnapshot{{Source: "transcript_cwd", Count: 2}}) {
		t.Fatalf("unexpected alpha project attribution summary: %#v", alpha.ProjectAttributionSourceSummary)
	}
	if !slices.Equal(alpha.ProvenanceSummary, []ProvenanceCountSnapshot{{Source: "transcript_path", Count: 2}}) {
		t.Fatalf("unexpected alpha provenance summary: %#v", alpha.ProvenanceSummary)
	}

	beta := requireProjectSnapshot(t, projects, "beta")
	if beta.AttentionSharePct != 40 {
		t.Fatalf("expected beta attention share 40.0, got %.1f", beta.AttentionSharePct)
	}
	if beta.MainAgentSessions != 1 || beta.SubagentSessions != 0 || beta.UnknownRoleSessions != 1 {
		t.Fatalf("unexpected beta role split: main=%d subagent=%d unknown=%d", beta.MainAgentSessions, beta.SubagentSessions, beta.UnknownRoleSessions)
	}
	if beta.Confidence != "medium" {
		t.Fatalf("expected beta confidence medium, got %s", beta.Confidence)
	}
	if beta.ProjectAttributionConfidence != "medium" {
		t.Fatalf("expected beta project attribution confidence medium, got %s", beta.ProjectAttributionConfidence)
	}
	if !slices.Contains(beta.ConfidenceReasons, "1 session mappings are low-confidence") {
		t.Fatalf("expected beta reasons to disclose low-confidence session, got %#v", beta.ConfidenceReasons)
	}
	if !slices.Contains(beta.ConfidenceReasons, "1 sessions are missing transcript timing") {
		t.Fatalf("expected beta reasons to disclose missing transcript timing, got %#v", beta.ConfidenceReasons)
	}
	if !slices.Contains(beta.ProjectAttributionReasons, "1 sessions rely on config-root parent fallback") {
		t.Fatalf("expected beta attribution reasons to disclose config-root fallback, got %#v", beta.ProjectAttributionReasons)
	}
	if !slices.Contains(beta.ProjectAttributionReasons, "project attribution mixes multiple evidence strengths") {
		t.Fatalf("expected beta attribution reasons to disclose mixed strength, got %#v", beta.ProjectAttributionReasons)
	}
	expectedAttribution := []AttributionSourceCountSnapshot{
		{Source: "transcript_cwd", Count: 1},
		{Source: "config_root_parent", Count: 1},
	}
	if !slices.Equal(beta.ProjectAttributionSourceSummary, expectedAttribution) {
		t.Fatalf("unexpected beta attribution summary: %#v", beta.ProjectAttributionSourceSummary)
	}
	expectedProvenance := []ProvenanceCountSnapshot{
		{Source: "transcript_path", Count: 1},
		{Source: "command_hint", Count: 1},
		{Source: "fallback_session_id", Count: 1},
	}
	if !slices.Equal(beta.ProvenanceSummary, expectedProvenance) {
		t.Fatalf("unexpected beta provenance summary: %#v", beta.ProvenanceSummary)
	}
}

func TestBuildProjectFocusKeepsTranscriptProjectAndCWDHighConfidence(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sessions := []LiveSession{
		{
			Tool:      "codex",
			SessionID: "alpha-explicit",
			Processes: map[int]struct{}{1: {}},
			Trace: &SessionTrace{
				Project:       "alpha",
				ProjectSource: "transcript_project",
				FirstEvent:    now.Add(-3 * time.Minute),
				LastEvent:     now.Add(-45 * time.Second),
			},
			Mapping: LiveSessionMapping{TranscriptPath: true, ParsedTranscriptID: true},
		},
		{
			Tool:      "codex",
			SessionID: "alpha-cwd",
			Processes: map[int]struct{}{2: {}},
			Trace: &SessionTrace{
				Project:       "alpha",
				ProjectSource: "transcript_cwd",
				FirstEvent:    now.Add(-6 * time.Minute),
				LastEvent:     now.Add(-2 * time.Minute),
			},
			Mapping: LiveSessionMapping{TranscriptPath: true, ParsedTranscriptID: true},
		},
	}

	projects := buildProjectFocus(sessions, 90*time.Second, now)
	if len(projects) != 1 {
		t.Fatalf("expected 1 project snapshot, got %d", len(projects))
	}

	alpha := requireProjectSnapshot(t, projects, "alpha")
	if alpha.ProjectAttributionConfidence != "high" {
		t.Fatalf("expected high project attribution confidence, got %s", alpha.ProjectAttributionConfidence)
	}
	if slices.Contains(alpha.ProjectAttributionReasons, "project attribution mixes multiple evidence strengths") {
		t.Fatalf("expected high-confidence transcript_project + transcript_cwd mix to avoid mixed-strength reason, got %#v", alpha.ProjectAttributionReasons)
	}
	if !slices.Equal(alpha.ProjectAttributionSourceSummary, []AttributionSourceCountSnapshot{
		{Source: "transcript_project", Count: 1},
		{Source: "transcript_cwd", Count: 1},
	}) {
		t.Fatalf("unexpected project attribution summary: %#v", alpha.ProjectAttributionSourceSummary)
	}
}

func TestBuildCandidateWorkitemsAndCoordinationRisk(t *testing.T) {
	sessions := []LiveSessionSnapshot{
		{
			Tool:                         "codex",
			SessionID:                    "alpha-a",
			Project:                      "alpha",
			ProcessCount:                 2,
			Freshness:                    "active",
			Confidence:                   "high",
			ProjectAttributionSource:     "transcript_cwd",
			ProjectAttributionConfidence: "high",
			Provenance:                   []string{"transcript_path"},
		},
		{
			Tool:                         "codex",
			SessionID:                    "alpha-b",
			Project:                      "alpha",
			ProcessCount:                 1,
			Freshness:                    "active",
			Confidence:                   "medium",
			ProjectAttributionSource:     "config_root_parent",
			ProjectAttributionConfidence: "low",
			MissingTranscript:            true,
			Provenance:                   []string{"command_hint"},
		},
		{
			Tool:                         "claude",
			SessionID:                    "beta-a",
			Project:                      "beta",
			ProcessCount:                 1,
			Freshness:                    "stale",
			Confidence:                   "high",
			ProjectAttributionSource:     "transcript_cwd",
			ProjectAttributionConfidence: "high",
			Provenance:                   []string{"transcript_path"},
		},
	}

	workitems := buildCandidateWorkitems(sessions)
	if len(workitems) != 2 {
		t.Fatalf("expected 2 candidate workitems, got %d", len(workitems))
	}

	alphaWorkitem := requireCandidateWorkitem(t, workitems, "alpha", "codex", "active")
	if alphaWorkitem.SessionCount != 2 || alphaWorkitem.ProcessCount != 3 {
		t.Fatalf("unexpected alpha workitem sizing: %#v", alphaWorkitem)
	}
	if alphaWorkitem.Canonical {
		t.Fatalf("candidate workitem must stay non-canonical")
	}
	if alphaWorkitem.InferenceMode != "project_tool_freshness" {
		t.Fatalf("unexpected inference mode: %s", alphaWorkitem.InferenceMode)
	}
	if alphaWorkitem.FallbackView != "project_anchored" {
		t.Fatalf("unexpected fallback view: %s", alphaWorkitem.FallbackView)
	}
	if alphaWorkitem.Confidence != "medium" {
		t.Fatalf("expected conservative medium confidence, got %s", alphaWorkitem.Confidence)
	}
	if alphaWorkitem.ProjectAttributionConfidence != "medium" {
		t.Fatalf("expected project attribution confidence medium, got %s", alphaWorkitem.ProjectAttributionConfidence)
	}
	if !slices.Equal(alphaWorkitem.SessionIDs, []string{"alpha-a", "alpha-b"}) {
		t.Fatalf("unexpected workitem session ids: %#v", alphaWorkitem.SessionIDs)
	}
	if !slices.Contains(alphaWorkitem.ConfidenceReasons, "grouped only by project + tool + freshness bucket") {
		t.Fatalf("expected conservative grouping reason, got %#v", alphaWorkitem.ConfidenceReasons)
	}
	if !slices.Equal(alphaWorkitem.ProvenanceSummary, []ProvenanceCountSnapshot{
		{Source: "transcript_path", Count: 1},
		{Source: "command_hint", Count: 1},
	}) {
		t.Fatalf("unexpected workitem provenance summary: %#v", alphaWorkitem.ProvenanceSummary)
	}
	if !slices.Contains(alphaWorkitem.ProjectAttributionReasons, "1 sessions rely on config-root parent fallback") {
		t.Fatalf("expected project attribution fallback reason, got %#v", alphaWorkitem.ProjectAttributionReasons)
	}
	if !slices.Contains(alphaWorkitem.ProjectAttributionReasons, "project attribution mixes multiple evidence strengths") {
		t.Fatalf("expected project attribution mix reason, got %#v", alphaWorkitem.ProjectAttributionReasons)
	}
	if !slices.Equal(alphaWorkitem.ProjectAttributionSourceSummary, []AttributionSourceCountSnapshot{
		{Source: "transcript_cwd", Count: 1},
		{Source: "config_root_parent", Count: 1},
	}) {
		t.Fatalf("unexpected workitem attribution summary: %#v", alphaWorkitem.ProjectAttributionSourceSummary)
	}

	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	processes := []LiveProcessSnapshot{
		{PID: 1, MappedSessions: 1},
		{PID: 2, MappedSessions: 0},
		{PID: 3, MappedSessions: 1},
	}
	riskSessions := []LiveSessionSnapshot{
		{SessionID: "s1", Project: "alpha", Tool: "codex", Freshness: "stale", Confidence: "low"},
		{SessionID: "s2", Project: "alpha", Tool: "codex", Freshness: "active", Confidence: "high"},
		{SessionID: "s3", Project: "beta", Tool: "claude", Freshness: "active", Confidence: "medium"},
		{SessionID: "s4", Project: "gamma", Tool: "codex", Freshness: "idle", Confidence: "low"},
	}
	projects := []ProjectSnapshot{
		{Project: "alpha", SessionCount: 2, ActiveBurstCount: 1, RecentSessionCount: 2, AttentionSharePct: 34, ProcessCount: 2},
		{Project: "beta", SessionCount: 1, ActiveBurstCount: 1, RecentSessionCount: 1, AttentionSharePct: 33, ProcessCount: 1},
		{Project: "gamma", SessionCount: 1, ActiveBurstCount: 0, RecentSessionCount: 0, AttentionSharePct: 33, ProcessCount: 1},
	}
	candidateWorkitems := []CandidateWorkitemSnapshot{
		{Project: "alpha", Tool: "codex", FreshnessBucket: "active", SessionCount: 1, Confidence: "high"},
		{Project: "alpha", Tool: "codex", FreshnessBucket: "stale", SessionCount: 1, Confidence: "medium"},
		{Project: "beta", Tool: "claude", FreshnessBucket: "active", SessionCount: 1, Confidence: "high"},
		{Project: "gamma", Tool: "codex", FreshnessBucket: "idle", SessionCount: 1, Confidence: "high"},
	}
	risk := buildCoordinationRisk(
		processes,
		riskSessions,
		projects,
		candidateWorkitems,
		CurrentMetrics{SessionConcurrency: 4},
		HistoricPeaks{
			SevenDay: PeakWindow{
				SessionConcurrency: PeakPoint{
					Value: 5,
					At:    now.Add(-time.Hour).Format(time.RFC3339),
				},
			},
		},
		now,
		90*time.Second,
	)

	if risk.StaleSessionCount != 1 {
		t.Fatalf("expected 1 stale session, got %d", risk.StaleSessionCount)
	}
	if risk.OrphanProcessCount != 1 {
		t.Fatalf("expected 1 orphan process, got %d", risk.OrphanProcessCount)
	}
	if risk.ChurnSessionCount != 3 {
		t.Fatalf("expected churn count 3, got %d", risk.ChurnSessionCount)
	}
	if risk.ProjectSpreadCount != 3 {
		t.Fatalf("expected project spread 3, got %d", risk.ProjectSpreadCount)
	}
	if risk.FragmentationPct != 75 {
		t.Fatalf("expected fragmentation 75.0, got %.1f", risk.FragmentationPct)
	}
	if risk.LoadRatioPct != 80 {
		t.Fatalf("expected load ratio 80.0, got %.1f", risk.LoadRatioPct)
	}
	if risk.LoadPeakValue != 5 || risk.LoadPeakSource != "seven_day_transcript_peak" {
		t.Fatalf("unexpected load peak: %#v", risk)
	}
	if risk.LowConfidenceSessionCount != 2 {
		t.Fatalf("expected 2 low-confidence sessions, got %d", risk.LowConfidenceSessionCount)
	}
	if risk.Posture != "observed" {
		t.Fatalf("expected neutral observed posture, got %s", risk.Posture)
	}

	gotSignalKinds := make([]string, 0, len(risk.Signals))
	for _, signal := range risk.Signals {
		gotSignalKinds = append(gotSignalKinds, signal.Kind)
	}
	wantSignalKinds := []string{
		"top_project_share",
		"sessions_without_recent_event",
		"unmatched_processes",
		"recent_sessions",
		"project_spread",
		"observed_peak_ratio",
		"low_confidence_mapping",
		"candidate_workitem_coverage",
	}
	if !slices.Equal(gotSignalKinds, wantSignalKinds) {
		t.Fatalf("unexpected risk signal order: want %v, got %v", wantSignalKinds, gotSignalKinds)
	}
	for _, signal := range risk.Signals {
		if signal.Severity != "observed" {
			t.Fatalf("expected neutral observed signal severity, got %#v", signal)
		}
	}
}

func TestBuildCoordinationRiskAddsAllocationSkewSignal(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sessions := []LiveSessionSnapshot{
		{SessionID: "alpha-codex", Project: "alpha", Tool: "codex", Freshness: "active", Confidence: "high"},
		{SessionID: "alpha-claude", Project: "alpha", Tool: "claude", Freshness: "active", Confidence: "high"},
		{SessionID: "beta-codex", Project: "beta", Tool: "codex", Freshness: "idle", Confidence: "high"},
	}
	risk := buildCoordinationRisk(
		[]LiveProcessSnapshot{{PID: 1, MappedSessions: 1}},
		sessions,
		[]ProjectSnapshot{
			{Project: "alpha", SessionCount: 2, ActiveBurstCount: 2, RecentSessionCount: 2, AttentionSharePct: 80, ProcessCount: 4},
			{Project: "beta", SessionCount: 1, ActiveBurstCount: 1, RecentSessionCount: 1, AttentionSharePct: 20, ProcessCount: 1},
		},
		[]CandidateWorkitemSnapshot{
			{Project: "alpha", Tool: "codex", FreshnessBucket: "active", SessionCount: 1, Confidence: "high"},
			{Project: "alpha", Tool: "claude", FreshnessBucket: "active", SessionCount: 1, Confidence: "high"},
			{Project: "beta", Tool: "codex", FreshnessBucket: "idle", SessionCount: 1, Confidence: "high"},
		},
		CurrentMetrics{SessionConcurrency: 3},
		HistoricPeaks{
			SevenDay: PeakWindow{
				SessionConcurrency: PeakPoint{
					Value: 10,
					At:    now.Add(-time.Hour).Format(time.RFC3339),
				},
			},
		},
		now,
		90*time.Second,
	)

	if risk.ActiveProjectCount != 2 {
		t.Fatalf("expected 2 active projects, got %d", risk.ActiveProjectCount)
	}
	if risk.RecentProjectCount != 2 {
		t.Fatalf("expected 2 recent projects, got %d", risk.RecentProjectCount)
	}
	if risk.TopProject != "alpha" {
		t.Fatalf("expected alpha as top project, got %s", risk.TopProject)
	}
	if risk.TopProjectAttentionSharePct != 80 {
		t.Fatalf("expected 80.0 top attention share, got %.1f", risk.TopProjectAttentionSharePct)
	}

	signal := requireRiskSignal(t, risk.Signals, "top_project_share")
	if signal.Severity != "observed" {
		t.Fatalf("expected neutral observed severity, got %s", signal.Severity)
	}
	if !strings.Contains(signal.Evidence, "Project alpha has 80.0% of the observed project slice") {
		t.Fatalf("expected share evidence to name the top project, got %q", signal.Evidence)
	}
}

func TestBuildCoordinationRiskCountsMissingTranscriptSessionsInLowConfidenceSignal(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sessions := []LiveSessionSnapshot{
		{SessionID: "alpha-medium-missing", Project: "alpha", Tool: "codex", Freshness: "unknown", Confidence: "medium", MissingTranscript: true},
		{SessionID: "beta-low-missing", Project: "beta", Tool: "claude", Freshness: "unknown", Confidence: "low", MissingTranscript: true},
		{SessionID: "alpha-high", Project: "alpha", Tool: "codex", Freshness: "active", Confidence: "high"},
	}
	risk := buildCoordinationRisk(
		nil,
		sessions,
		[]ProjectSnapshot{
			{Project: "alpha", SessionCount: 2, ActiveBurstCount: 1, RecentSessionCount: 1, AttentionSharePct: 67, ProcessCount: 2},
			{Project: "beta", SessionCount: 1, ActiveBurstCount: 0, RecentSessionCount: 0, AttentionSharePct: 33, ProcessCount: 1},
		},
		buildCandidateWorkitems(sessions),
		CurrentMetrics{SessionConcurrency: 3},
		HistoricPeaks{
			SevenDay: PeakWindow{
				SessionConcurrency: PeakPoint{
					Value: 5,
					At:    now.Add(-time.Hour).Format(time.RFC3339),
				},
			},
		},
		now,
		90*time.Second,
	)

	if risk.LowConfidenceSessionCount != 2 {
		t.Fatalf("expected low-confidence/missing-transcript union count 2, got %d", risk.LowConfidenceSessionCount)
	}

	signal := requireRiskSignal(t, risk.Signals, "low_confidence_mapping")
	if !strings.Contains(signal.Evidence, "2 live sessions have low-confidence mapping or missing transcript timing") {
		t.Fatalf("expected low-confidence evidence summary, got %q", signal.Evidence)
	}
	if !strings.Contains(signal.Evidence, "1 low-confidence") {
		t.Fatalf("expected low-confidence evidence to count only the low-confidence session once, got %q", signal.Evidence)
	}
	if !strings.Contains(signal.Evidence, "2 missing transcript timing") {
		t.Fatalf("expected low-confidence evidence to include the medium-confidence missing-transcript session, got %q", signal.Evidence)
	}
	if !strings.Contains(signal.Evidence, "overlaps deduplicated") {
		t.Fatalf("expected low-confidence evidence to disclose deduplicated overlap, got %q", signal.Evidence)
	}
}

func TestBuildCoordinationRiskIgnoresUnassignedBucketsForProjectSpread(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sessions := []LiveSessionSnapshot{
		{SessionID: "alpha", Project: "alpha", Tool: "codex", Freshness: "active", Confidence: "high"},
		{SessionID: "tmp", Project: "unassigned", Tool: "claude", Freshness: "active", Confidence: "low"},
	}
	risk := buildCoordinationRisk(
		nil,
		sessions,
		[]ProjectSnapshot{
			{Project: "alpha", SessionCount: 1, ActiveBurstCount: 1, RecentSessionCount: 1, AttentionSharePct: 30, ProcessCount: 1},
			{Project: "unassigned", SessionCount: 1, ActiveBurstCount: 1, RecentSessionCount: 1, AttentionSharePct: 70, ProcessCount: 1},
		},
		buildCandidateWorkitems(sessions),
		CurrentMetrics{SessionConcurrency: 2},
		HistoricPeaks{
			SevenDay: PeakWindow{
				SessionConcurrency: PeakPoint{
					Value: 4,
					At:    now.Add(-time.Hour).Format(time.RFC3339),
				},
			},
		},
		now,
		90*time.Second,
	)

	if risk.ActiveProjectCount != 1 {
		t.Fatalf("expected 1 active real project, got %d", risk.ActiveProjectCount)
	}
	if risk.RecentProjectCount != 1 {
		t.Fatalf("expected 1 recent real project, got %d", risk.RecentProjectCount)
	}
	if risk.TopProject != "alpha" {
		t.Fatalf("expected alpha as the top real project, got %s", risk.TopProject)
	}
	if risk.TopProjectAttentionSharePct != 100 {
		t.Fatalf("expected alpha to own 100.0%% of real-project attention, got %.1f", risk.TopProjectAttentionSharePct)
	}
	if risk.ProjectSpreadCount != 1 {
		t.Fatalf("expected project spread 1 after ignoring unassigned, got %d", risk.ProjectSpreadCount)
	}
	if risk.FragmentationPct != 0 {
		t.Fatalf("expected fragmentation 0.0 with only one real project, got %.1f", risk.FragmentationPct)
	}
	requireNoRiskSignal(t, risk.Signals, "project_spread")
	requireRiskSignal(t, risk.Signals, "top_project_share")
}

func TestBuildCoordinationRiskCountsDuplicateOverlapConservatively(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	sessions := []LiveSessionSnapshot{
		{SessionID: "alpha-a", Project: "alpha", Tool: "codex", Freshness: "active", Confidence: "high"},
		{SessionID: "alpha-b", Project: "alpha", Tool: "codex", Freshness: "active", Confidence: "high"},
		{SessionID: "alpha-c", Project: "alpha", Tool: "claude", Freshness: "active", Confidence: "high"},
		{SessionID: "alpha-d", Project: "alpha", Tool: "codex", Freshness: "stale", Confidence: "high"},
		{SessionID: "alpha-e", Project: "alpha", Tool: "codex", Freshness: "stale", Confidence: "high"},
		{SessionID: "beta-a", Project: "beta", Tool: "codex", Freshness: "idle", Confidence: "high"},
		{SessionID: "beta-b", Project: "beta", Tool: "codex", Freshness: "idle", Confidence: "high"},
	}
	risk := buildCoordinationRisk(
		nil,
		sessions,
		[]ProjectSnapshot{
			{Project: "alpha", SessionCount: 5, ActiveBurstCount: 3, AttentionSharePct: 57},
			{Project: "beta", SessionCount: 2, ActiveBurstCount: 0, AttentionSharePct: 43},
		},
		buildCandidateWorkitems(sessions),
		CurrentMetrics{SessionConcurrency: 7},
		HistoricPeaks{
			SevenDay: PeakWindow{
				SessionConcurrency: PeakPoint{
					Value: 10,
					At:    now.Add(-time.Hour).Format(time.RFC3339),
				},
			},
		},
		now,
		90*time.Second,
	)

	if risk.DuplicateOverlapSuspicionCount != 2 {
		t.Fatalf("expected 2 duplicate/overlap suspicions, got %d", risk.DuplicateOverlapSuspicionCount)
	}
	if risk.DuplicateOverlapClusterCount != 2 {
		t.Fatalf("expected 2 duplicate/overlap clusters, got %d", risk.DuplicateOverlapClusterCount)
	}

	signal := requireRiskSignal(t, risk.Signals, "duplicate_overlap_candidates")
	if signal.Severity != "observed" {
		t.Fatalf("expected neutral observed severity, got %s", signal.Severity)
	}
	if !strings.Contains(signal.Evidence, "alpha/codex active x2") {
		t.Fatalf("expected active same-project/tool cluster in evidence, got %q", signal.Evidence)
	}
	if !strings.Contains(signal.Evidence, "beta/codex idle x2") {
		t.Fatalf("expected idle same-project/tool cluster in evidence, got %q", signal.Evidence)
	}
	if strings.Contains(signal.Evidence, "stale") {
		t.Fatalf("stale sessions must not count toward duplicate/overlap evidence, got %q", signal.Evidence)
	}
	if !strings.Contains(signal.Evidence, "not semantic identity") {
		t.Fatalf("expected conservative overlap wording, got %q", signal.Evidence)
	}
}

func TestBuildCoordinationRiskSummarizesCandidateCoverageAndConfidence(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	risk := buildCoordinationRisk(
		nil,
		[]LiveSessionSnapshot{
			{SessionID: "alpha", Project: "alpha", Tool: "codex", Freshness: "active", Confidence: "high"},
			{SessionID: "beta", Project: "beta", Tool: "claude", Freshness: "active", Confidence: "high"},
			{SessionID: "gamma", Project: "beta", Tool: "codex", Freshness: "idle", Confidence: "medium"},
			{SessionID: "tmp", Tool: "codex", Freshness: "unknown", Confidence: "low"},
		},
		nil,
		[]CandidateWorkitemSnapshot{
			{Project: "alpha", Tool: "codex", FreshnessBucket: "active", SessionCount: 1, Confidence: "high"},
			{Project: "beta", Tool: "claude", FreshnessBucket: "active", SessionCount: 2, Confidence: "medium"},
			{Project: "unassigned", Tool: "codex", FreshnessBucket: "unknown", SessionCount: 1, Confidence: "low"},
		},
		CurrentMetrics{SessionConcurrency: 4},
		HistoricPeaks{
			SevenDay: PeakWindow{
				SessionConcurrency: PeakPoint{
					Value: 10,
					At:    now.Add(-time.Hour).Format(time.RFC3339),
				},
			},
		},
		now,
		90*time.Second,
	)

	if risk.CandidateWorkitemCount != 3 {
		t.Fatalf("expected 3 candidate workitems, got %d", risk.CandidateWorkitemCount)
	}
	if risk.CandidateWorkitemCoveredSessionCount != 3 {
		t.Fatalf("expected 3 project-anchored candidate sessions, got %d", risk.CandidateWorkitemCoveredSessionCount)
	}
	if risk.CandidateWorkitemCoveragePct != 75 {
		t.Fatalf("expected 75.0 candidate coverage, got %.1f", risk.CandidateWorkitemCoveragePct)
	}
	if !slices.Equal(risk.CandidateWorkitemConfidenceBreakdown, []ConfidenceCountSnapshot{
		{Level: "high", Count: 1},
		{Level: "medium", Count: 1},
		{Level: "low", Count: 1},
	}) {
		t.Fatalf("unexpected candidate confidence breakdown: %#v", risk.CandidateWorkitemConfidenceBreakdown)
	}

	signal := requireRiskSignal(t, risk.Signals, "candidate_workitem_coverage")
	if signal.Severity != "observed" {
		t.Fatalf("expected neutral observed severity, got %s", signal.Severity)
	}
	if !strings.Contains(signal.Evidence, "75.0% of live sessions") {
		t.Fatalf("expected candidate coverage in evidence, got %q", signal.Evidence)
	}
	if !strings.Contains(signal.Evidence, "1 high, 1 medium, 1 low") {
		t.Fatalf("expected candidate confidence mix in evidence, got %q", signal.Evidence)
	}
}

func requireLiveSession(t *testing.T, sessions []LiveSession, sessionID string) LiveSession {
	t.Helper()
	for _, session := range sessions {
		if session.SessionID == sessionID {
			return session
		}
	}
	t.Fatalf("missing live session %s", sessionID)
	return LiveSession{}
}

func requireLiveSessionSnapshot(t *testing.T, sessions []LiveSessionSnapshot, sessionID string) LiveSessionSnapshot {
	t.Helper()
	for _, session := range sessions {
		if session.SessionID == sessionID {
			return session
		}
	}
	t.Fatalf("missing live session snapshot %s", sessionID)
	return LiveSessionSnapshot{}
}

func requireProjectSnapshot(t *testing.T, projects []ProjectSnapshot, projectName string) ProjectSnapshot {
	t.Helper()
	for _, project := range projects {
		if project.Project == projectName {
			return project
		}
	}
	t.Fatalf("missing project snapshot %s", projectName)
	return ProjectSnapshot{}
}

func requireCandidateWorkitem(t *testing.T, items []CandidateWorkitemSnapshot, project, tool, freshness string) CandidateWorkitemSnapshot {
	t.Helper()
	for _, item := range items {
		if item.Project == project && item.Tool == tool && item.FreshnessBucket == freshness {
			return item
		}
	}
	t.Fatalf("missing candidate workitem for %s/%s/%s", project, tool, freshness)
	return CandidateWorkitemSnapshot{}
}

func requireRiskSignal(t *testing.T, signals []RiskSignalSnapshot, kind string) RiskSignalSnapshot {
	t.Helper()
	for _, signal := range signals {
		if signal.Kind == kind {
			return signal
		}
	}
	t.Fatalf("missing risk signal %s", kind)
	return RiskSignalSnapshot{}
}

func requireNoRiskSignal(t *testing.T, signals []RiskSignalSnapshot, kind string) {
	t.Helper()
	for _, signal := range signals {
		if signal.Kind == kind {
			t.Fatalf("unexpected risk signal %s: %#v", kind, signal)
		}
	}
}
