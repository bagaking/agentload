package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONStringField(t *testing.T) {
	line := []byte(`{"type":"assistant","timestamp":"2026-06-27T12:00:00Z","message":{"cwd":"workspace/project"},"cwd":"workspace/root","sessionId":"abc-123"}`)

	if got := jsonStringField(line, "timestamp"); got != "2026-06-27T12:00:00Z" {
		t.Fatalf("unexpected timestamp: %q", got)
	}
	if got := jsonStringField(line, "sessionId"); got != "abc-123" {
		t.Fatalf("unexpected session id: %q", got)
	}
	if got := jsonNestedStringField(line, "message", "cwd"); got != "workspace/project" {
		t.Fatalf("unexpected nested cwd: %q", got)
	}
}

func TestJSONStringFieldEscaped(t *testing.T) {
	line := []byte(`{"cwd":"workspace/project \"quoted\"","timestamp":"2026-06-27T12:00:00Z"}`)

	if got := jsonStringField(line, "cwd"); got != `workspace/project "quoted"` {
		t.Fatalf("unexpected escaped string: %q", got)
	}
}

func TestParseCodexTraceCapturesProjectSourceFromCWD(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"timestamp\":\"2026-06-27T12:00:00Z\",\"payload\":{\"id\":\"codex-session\",\"cwd\":\"workspace/agentload\"}}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	trace, err := parseCodexTrace(path)
	if err != nil {
		t.Fatalf("parseCodexTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.Project != "agentload" {
		t.Fatalf("expected cwd-derived project agentload, got %q", trace.Project)
	}
	if trace.ProjectSource != "transcript_cwd" {
		t.Fatalf("expected transcript_cwd source, got %q", trace.ProjectSource)
	}
}

func TestParseCodexTraceCapturesExplicitProjectSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte("{\"timestamp\":\"2026-06-27T12:00:00Z\",\"payload\":{\"id\":\"codex-session\",\"project\":\"agentload-explicit\"}}\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	trace, err := parseCodexTrace(path)
	if err != nil {
		t.Fatalf("parseCodexTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.Project != "agentload-explicit" {
		t.Fatalf("expected explicit project agentload-explicit, got %q", trace.Project)
	}
	if trace.ProjectSource != "transcript_project" {
		t.Fatalf("expected transcript_project source, got %q", trace.ProjectSource)
	}
}

func TestParseCodexLaneTraceFallsBackToConfigRootParent(t *testing.T) {
	root := t.TempDir()
	eventsPath := filepath.Join(root, "agentload", ".codex", ".codexl", "asagent", "lane-1", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		t.Fatalf("mkdir events dir: %v", err)
	}
	if err := os.WriteFile(eventsPath, []byte("{\"thread_id\":\"lane-1\"}\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	trace, err := parseCodexLaneTrace(eventsPath)
	if err != nil {
		t.Fatalf("parseCodexLaneTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.Project != "agentload" {
		t.Fatalf("expected config-root fallback project agentload, got %q", trace.Project)
	}
	if trace.ProjectSource != "config_root_parent" {
		t.Fatalf("expected config_root_parent source, got %q", trace.ProjectSource)
	}
}

func TestParseTraeTraceCapturesSessionRoleAndProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := `{"timestamp":"2026-06-27T12:00:00Z","type":"session_meta","payload":{"id":"trae-session","cwd":"workspace/agentload","thread_source":"subagent","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent-session","agent_nickname":"Review lane","agent_role":"worker"}}},"agent_nickname":"Review lane","agent_role":"worker"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	trace, err := parseTraeTrace(path)
	if err != nil {
		t.Fatalf("parseTraeTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.SessionID != "trae-session" {
		t.Fatalf("expected parsed session id, got %q", trace.SessionID)
	}
	if trace.Project != "agentload" {
		t.Fatalf("expected cwd-derived project agentload, got %q", trace.Project)
	}
	if trace.ThreadSource != "subagent" {
		t.Fatalf("expected thread_source=subagent, got %q", trace.ThreadSource)
	}
	if trace.ParentThreadID != "parent-session" {
		t.Fatalf("expected parent thread id, got %q", trace.ParentThreadID)
	}
	if trace.AgentNickname != "Review lane" || trace.AgentRole != "worker" {
		t.Fatalf("expected agent metadata, got nickname=%q role=%q", trace.AgentNickname, trace.AgentRole)
	}
	if trace.IndependentlyRun {
		t.Fatalf("expected subagent trace not to be marked independently run")
	}
}

func TestParseCodexTraceCapturesUserThreadSource(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := `{"timestamp":"2026-06-27T12:00:00Z","type":"session_meta","payload":{"id":"codex-session","cwd":"workspace/agentload","thread_source":"user"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	trace, err := parseCodexTrace(path)
	if err != nil {
		t.Fatalf("parseCodexTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.ThreadSource != "user" {
		t.Fatalf("expected thread_source=user, got %q", trace.ThreadSource)
	}
	if !trace.IndependentlyRun {
		t.Fatalf("expected user codex trace to remain independently run")
	}
}

func TestParseTranscriptFileTailKeepsHeadMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	head := `{"timestamp":"2026-06-28T10:00:00Z","type":"session_meta","payload":{"id":"codex-session","cwd":"workspace/agentload","thread_source":"subagent","parent_thread_id":"parent-session","agent_nickname":"Review lane","agent_role":"worker"}}` + "\n"
	padding := `{"timestamp":"2026-06-28T10:01:00Z","payload":{"id":"padding","text":"` + strings.Repeat("x", 700*1024) + `"}}` + "\n"
	tail := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"codex-session"}}` + "\n"
	if err := os.WriteFile(path, []byte(head+padding+tail), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	trace, err := parseTranscriptFileTail(TranscriptFile{Tool: "codex", Path: path})
	if err != nil {
		t.Fatalf("parseTranscriptFileTail: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.SessionID != "codex-session" || trace.Project != "agentload" {
		t.Fatalf("expected head metadata to survive tail parse, session=%q project=%q", trace.SessionID, trace.Project)
	}
	if trace.ThreadSource != "subagent" || trace.ParentThreadID != "parent-session" {
		t.Fatalf("expected role metadata from head, source=%q parent=%q", trace.ThreadSource, trace.ParentThreadID)
	}
	if trace.AgentNickname != "Review lane" || trace.AgentRole != "worker" {
		t.Fatalf("expected agent metadata from head, nickname=%q role=%q", trace.AgentNickname, trace.AgentRole)
	}
	if trace.LastEvent.IsZero() || !trace.LastEvent.Equal(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected tail event to be included, got last=%s", trace.LastEvent)
	}
}

func TestParseTranscriptFileTailKeepsMetadataAfterLargePreamble(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	preamble := `{"timestamp":"2026-06-28T09:59:00Z","payload":{"id":"preamble","text":"` + strings.Repeat("x", 300*1024) + `"}}` + "\n"
	metadata := `{"timestamp":"2026-06-28T10:00:00Z","type":"session_meta","payload":{"id":"codex-session","cwd":"workspace/agentload","thread_source":"subagent","parent_thread_id":"parent-session","agent_nickname":"Review lane","agent_role":"worker"}}` + "\n"
	padding := `{"timestamp":"2026-06-28T10:01:00Z","payload":{"id":"padding","text":"` + strings.Repeat("y", 700*1024) + `"}}` + "\n"
	tail := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"codex-session"}}` + "\n"
	if err := os.WriteFile(path, []byte(preamble+metadata+padding+tail), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	trace, err := parseTranscriptFileTail(TranscriptFile{Tool: "codex", Path: path})
	if err != nil {
		t.Fatalf("parseTranscriptFileTail: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.SessionID != "codex-session" || trace.Project != "agentload" {
		t.Fatalf("expected metadata after preamble to survive tail parse, session=%q project=%q", trace.SessionID, trace.Project)
	}
	if trace.ThreadSource != "subagent" || trace.ParentThreadID != "parent-session" {
		t.Fatalf("expected role metadata after preamble, source=%q parent=%q", trace.ThreadSource, trace.ParentThreadID)
	}
	if trace.AgentNickname != "Review lane" || trace.AgentRole != "worker" {
		t.Fatalf("expected agent metadata after preamble, nickname=%q role=%q", trace.AgentNickname, trace.AgentRole)
	}
	if trace.LastEvent.IsZero() || !trace.LastEvent.Equal(time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("expected tail event to be included, got last=%s", trace.LastEvent)
	}
}

func TestFileMayContainEventsAfterCutoffUsesTailTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old-tail.jsonl")
	body := `{"timestamp":"2026-06-20T12:00:00Z","payload":{"id":"old-session"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	futureMTime := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, futureMTime, futureMTime); err != nil {
		t.Fatalf("set transcript mtime: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}

	cutoff := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	if fileMayContainEventsAfterCutoff(path, info, cutoff) {
		t.Fatalf("expected old tail timestamp to allow skipping mtime-new transcript")
	}
}

func TestFileMayContainEventsAfterCutoffKeepsUnknownTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "unknown-tail.jsonl")
	body := `{"payload":{"id":"no-timestamp"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}

	cutoff := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	if !fileMayContainEventsAfterCutoff(path, info, cutoff) {
		t.Fatalf("expected unknown tail timestamp to stay eligible")
	}
}

func TestFileMayContainEventsAfterCutoffKeepsRecentTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "recent-tail.jsonl")
	body := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"recent-session"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}

	cutoff := time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC)
	if !fileMayContainEventsAfterCutoff(path, info, cutoff) {
		t.Fatalf("expected recent tail timestamp to stay eligible")
	}
}
