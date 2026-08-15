package main

import (
	"agentload/internal/snapshot"
	"context"
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

func TestParseCodexTraceCapturesTokenUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := strings.Join([]string{
		`{"timestamp":"2026-06-27T12:00:00Z","payload":{"id":"codex-session","cwd":"workspace/agentload","usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":3}}}`,
		`{"timestamp":"2026-06-27T12:01:00Z","payload":{"id":"codex-session","response":{"usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}}}`,
		`{"timestamp":"2026-06-27T12:02:00Z","payload":{"id":"codex-session","usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":6,"cachedContentTokenCount":4}}}`,
	}, "\n") + "\n"
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
	want := snapshot.TokenUsage{
		InputTokens:           23,
		OutputTokens:          17,
		CacheReadInputTokens:  9,
		ReasoningOutputTokens: 1,
		TotalTokens:           47,
	}
	if trace.TokenUsage != want {
		t.Fatalf("unexpected token usage: %+v, want %+v", trace.TokenUsage, want)
	}
}

func TestParseCodexTraceCapturesCumulativeTokenUsage(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	body := strings.Join([]string{
		`{"timestamp":"2026-06-27T12:00:00Z","payload":{"id":"codex-session","cwd":"workspace/agentload","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":30,"reasoning_output_tokens":5,"total_tokens":150},"last_token_usage":{"input_tokens":100,"output_tokens":30,"total_tokens":130}}}}`,
		`{"timestamp":"2026-06-27T12:01:00Z","payload":{"id":"codex-session","cwd":"workspace/agentload","info":{"total_token_usage":{"input_tokens":180,"cached_input_tokens":40,"output_tokens":70,"reasoning_output_tokens":9,"total_tokens":290},"last_token_usage":{"input_tokens":80,"output_tokens":40,"total_tokens":120}}}}`,
	}, "\n") + "\n"
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
	want := snapshot.TokenUsage{
		InputTokens:           180,
		OutputTokens:          70,
		CacheReadInputTokens:  40,
		ReasoningOutputTokens: 9,
		TotalTokens:           290,
	}
	if trace.TokenUsage != want {
		t.Fatalf("unexpected cumulative token usage: %+v, want %+v", trace.TokenUsage, want)
	}
}

func TestParseCodexLaneTraceCapturesTokenUsage(t *testing.T) {
	root := t.TempDir()
	eventsPath := filepath.Join(root, "agentload", ".codex", ".codexl", "asagent", "lane-1", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(eventsPath), 0o755); err != nil {
		t.Fatalf("mkdir events dir: %v", err)
	}
	body := strings.Join([]string{
		`{"thread_id":"lane-1"}`,
		`{"payload":{"info":{"total_token_usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(eventsPath, []byte(body), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}

	trace, err := parseCodexLaneTrace(eventsPath)
	if err != nil {
		t.Fatalf("parseCodexLaneTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.TokenUsage != (snapshot.TokenUsage{InputTokens: 10, OutputTokens: 4, TotalTokens: 14}) {
		t.Fatalf("unexpected lane token usage: %+v", trace.TokenUsage)
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

	trace, err := newCodexTranscriptParser().ParseTail(snapshot.TranscriptFile{Tool: "codex", Path: path})
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

	trace, err := newCodexTranscriptParser().ParseTail(snapshot.TranscriptFile{Tool: "codex", Path: path})
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

func TestParseTranscriptFileTailKeepsMetadataAfterOversizedPreambleLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	preamble := `{"timestamp":"2026-06-28T09:59:00Z","payload":{"id":"preamble","text":"` + strings.Repeat("x", 2*1024*1024) + `"}}` + "\n"
	metadata := `{"timestamp":"2026-06-28T10:00:00Z","type":"session_meta","payload":{"id":"codex-session","cwd":"workspace/agentload","thread_source":"subagent","parent_thread_id":"parent-session","agent_nickname":"Review lane","agent_role":"worker"}}` + "\n"
	padding := `{"timestamp":"2026-06-28T10:01:00Z","payload":{"id":"padding","text":"` + strings.Repeat("y", 700*1024) + `"}}` + "\n"
	tail := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"codex-session"}}` + "\n"
	if err := os.WriteFile(path, []byte(preamble+metadata+padding+tail), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	trace, err := newCodexTranscriptParser().ParseTail(snapshot.TranscriptFile{Tool: "codex", Path: path})
	if err != nil {
		t.Fatalf("parseTranscriptFileTail: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.SessionID != "codex-session" || trace.Project != "agentload" {
		t.Fatalf("expected metadata after oversized preamble to survive tail parse, session=%q project=%q", trace.SessionID, trace.Project)
	}
	if trace.ThreadSource != "subagent" || trace.ParentThreadID != "parent-session" {
		t.Fatalf("expected role metadata after oversized preamble, source=%q parent=%q", trace.ThreadSource, trace.ParentThreadID)
	}
	if trace.AgentNickname != "Review lane" || trace.AgentRole != "worker" {
		t.Fatalf("expected agent metadata after oversized preamble, nickname=%q role=%q", trace.AgentNickname, trace.AgentRole)
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

func TestTranscriptDataCancelledWaiterReturnsPromptly(t *testing.T) {
	observer := newObserver(Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           24 * time.Hour,
		TranscriptCacheTTL: time.Minute,
	})
	key := transcriptCacheKey(observer.adapters.roots(), nil, observer.cfg.IdleGap, observer.cfg.MinInterval, observer.cfg.Lookback)
	// Simulate a scan wedged on a hung volume: the flight never completes.
	observer.inflight[key] = &transcriptScanFlight{done: make(chan struct{})}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	data, cached := observer.transcriptData(ctx, nil, time.Now())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("cancelled waiter should return promptly, took %s", elapsed)
	}
	if cached {
		t.Fatalf("no cache existed, cached should be false")
	}
	if data == nil {
		t.Fatalf("expected non-nil transcript data")
	}
	found := false
	for _, message := range data.Errors {
		if strings.Contains(message, "transcript scan wait cancelled") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected cancellation disclosure in errors, got %#v", data.Errors)
	}
}

func TestTranscriptDataDoesNotCacheCancelledScan(t *testing.T) {
	tmp := t.TempDir()
	codexRoot := filepath.Join(tmp, ".codex")
	sessionsDir := filepath.Join(codexRoot, "sessions", "2026", "06", "28")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions: %v", err)
	}
	path := filepath.Join(sessionsDir, "rollout-2026-06-28T11-30-00-abc.jsonl")
	if err := os.WriteFile(path, []byte(`{"timestamp":"2026-06-28T11:30:00Z","session_id":"abc"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	observer := newObserver(Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           24 * time.Hour,
		TranscriptCacheTTL: time.Minute,
		CodexRoots:         []string{codexRoot},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	data, cached := observer.transcriptData(ctx, nil, time.Now())
	if cached {
		t.Fatalf("cancelled scan should not report cached data")
	}
	found := false
	for _, message := range data.Errors {
		if strings.Contains(message, "transcript scan aborted early") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected aborted-scan disclosure in errors, got %#v", data.Errors)
	}
	observer.mu.Lock()
	cachedData := observer.cache.Data
	observer.mu.Unlock()
	if cachedData != nil {
		t.Fatalf("partial cancelled scan must not populate the transcript cache")
	}
}

func TestTranscriptDataHealthyWaiterRetriesIncompleteFlight(t *testing.T) {
	observer := newObserver(Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           24 * time.Hour,
		TranscriptCacheTTL: time.Minute,
	})
	key := transcriptCacheKey(observer.adapters.roots(), nil, observer.cfg.IdleGap, observer.cfg.MinInterval, observer.cfg.Lookback)
	flight := &transcriptScanFlight{
		done: make(chan struct{}),
		data: &snapshot.TranscriptData{
			Traces: map[string]*snapshot.SessionTrace{},
			Errors: []string{"partial owner result"},
		},
	}
	observer.inflight[key] = flight

	result := make(chan *snapshot.TranscriptData, 1)
	go func() {
		data, _ := observer.transcriptData(context.Background(), nil, time.Now())
		result <- data
	}()
	time.Sleep(20 * time.Millisecond)
	observer.mu.Lock()
	delete(observer.inflight, key)
	close(flight.done)
	observer.mu.Unlock()

	select {
	case data := <-result:
		for _, message := range data.Errors {
			if strings.Contains(message, "partial owner result") {
				t.Fatalf("healthy waiter consumed incomplete flight: %+v", data.Errors)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("healthy waiter did not retry incomplete flight")
	}
}

func TestTranscriptDataCacheInvalidatesAfterEvidenceMutation(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	root := filepath.Join(t.TempDir(), ".codex")
	day := filepath.Join(root, "sessions", now.Format("2006"), now.Format("01"), now.Format("02"))
	firstPath := filepath.Join(day, "first.jsonl")
	writeDiscoveryFixture(t, firstPath, now)
	observer := newObserver(Config{
		CodexRoots:         []string{root},
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           time.Hour,
		TranscriptCacheTTL: time.Minute,
	})

	first, firstCached := observer.transcriptData(context.Background(), nil, now)
	if firstCached || first.CoverageIncomplete || first.ScannedFiles != 1 {
		t.Fatalf("initial transcript data = cached=%v data=%+v", firstCached, first)
	}
	second, secondCached := observer.transcriptData(context.Background(), nil, now)
	if !secondCached || second.CoverageIncomplete {
		t.Fatalf("expected a complete cache hit, got cached=%v data=%+v", secondCached, second)
	}

	secondPath := filepath.Join(day, "second.jsonl")
	writeDiscoveryFixture(t, secondPath, now.Add(time.Minute))
	observer.evidenceIndex.recordWatchBatch(evidenceWatchBatch{Complete: true, Paths: []string{secondPath}})
	third, thirdCached := observer.transcriptData(context.Background(), nil, now)
	if thirdCached || third.CoverageIncomplete || third.ScannedFiles != 2 {
		t.Fatalf("evidence mutation reused stale cache: cached=%v data=%+v", thirdCached, third)
	}
}

func TestCollectTranscriptCandidatesSurfacesWalkErrors(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission errors are not observable as root")
	}
	tmp := t.TempDir()
	projectsDir := filepath.Join(tmp, ".claude", "projects")
	lockedDir := filepath.Join(projectsDir, "locked")
	if err := os.MkdirAll(lockedDir, 0o755); err != nil {
		t.Fatalf("mkdir locked dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(lockedDir, "hidden.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write hidden transcript: %v", err)
	}
	if err := os.Chmod(lockedDir, 0o000); err != nil {
		t.Fatalf("chmod locked dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })

	registry := defaultCodingAgentRegistry(Config{ClaudeRoots: []string{filepath.Join(tmp, ".claude")}})
	_, walkErrors := collectTranscriptCandidates(context.Background(), newTranscriptEvidenceIndex(registry), registry, nil, time.Time{}, time.Time{})
	found := false
	for _, message := range walkErrors {
		if strings.Contains(message, lockedDir) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected walk error mentioning %q, got %#v", lockedDir, walkErrors)
	}
}

func TestScanTranscriptsSurfacesWalkErrorsInData(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission errors are not observable as root")
	}
	tmp := t.TempDir()
	projectsDir := filepath.Join(tmp, ".claude", "projects")
	lockedDir := filepath.Join(projectsDir, "locked")
	if err := os.MkdirAll(lockedDir, 0o755); err != nil {
		t.Fatalf("mkdir locked dir: %v", err)
	}
	if err := os.Chmod(lockedDir, 0o000); err != nil {
		t.Fatalf("chmod locked dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o755) })

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
		ClaudeRoots: []string{filepath.Join(tmp, ".claude")},
	})
	data := observer.scanTranscripts(nil, time.Time{}, 90*time.Second, 15*time.Second)
	found := false
	for _, message := range data.Errors {
		if strings.Contains(message, lockedDir) {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected scan errors mentioning %q, got %#v", lockedDir, data.Errors)
	}
}

func TestPruneFileCacheDropsMissingPaths(t *testing.T) {
	tmp := t.TempDir()
	candidatePath := filepath.Join(tmp, "candidate.jsonl")
	uncandidatedPath := filepath.Join(tmp, "still-on-disk.jsonl")
	missingPath := filepath.Join(tmp, "deleted.jsonl")
	for _, path := range []string{candidatePath, uncandidatedPath} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}

	observer := newObserver(Config{})
	observer.fileCache[candidatePath] = fileTraceCache{}
	observer.fileCache[uncandidatedPath] = fileTraceCache{}
	observer.fileCache[missingPath] = fileTraceCache{}

	observer.pruneFileCache([]transcriptCandidate{{File: snapshot.TranscriptFile{Tool: "claude", Path: candidatePath}}})

	observer.mu.Lock()
	defer observer.mu.Unlock()
	if _, ok := observer.fileCache[candidatePath]; !ok {
		t.Fatalf("candidate path must stay cached")
	}
	if _, ok := observer.fileCache[uncandidatedPath]; !ok {
		t.Fatalf("non-candidate path still on disk must stay cached")
	}
	if _, ok := observer.fileCache[missingPath]; ok {
		t.Fatalf("path deleted from disk must be pruned from the cache")
	}
}

func TestParseCodexLaneTraceSurfacesSidecarErrors(t *testing.T) {
	root := t.TempDir()
	laneDir := filepath.Join(root, "agentload", ".codex", ".codexl", "asagent", "lane-1")
	if err := os.MkdirAll(laneDir, 0o755); err != nil {
		t.Fatalf("mkdir lane dir: %v", err)
	}
	eventsPath := filepath.Join(laneDir, "events.jsonl")
	if err := os.WriteFile(eventsPath, []byte("{\"thread_id\":\"lane-1\"}\n"), 0o644); err != nil {
		t.Fatalf("write events: %v", err)
	}
	if err := os.WriteFile(filepath.Join(laneDir, "request.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("write request.json: %v", err)
	}

	trace, err := parseCodexLaneTrace(eventsPath)
	if err == nil || !strings.Contains(err.Error(), "request.json") {
		t.Fatalf("expected sidecar error mentioning request.json, got %v", err)
	}
	if trace == nil {
		t.Fatalf("sidecar failure should degrade the trace, not void it")
	}
	if trace.SessionID != "lane-1" {
		t.Fatalf("expected degraded trace to keep session id, got %q", trace.SessionID)
	}
}

// Claude stamps the parent's sessionId on every sidechain line. Adopting it
// made each subagent transcript key to the parent, so ten concurrent subagents
// reported one session; the fix keeps the file's own identity and records the
// borrowed id as the parent link instead.
func TestParseClaudeTraceKeepsSubagentIdentityWhenSidechainBorrowsParentSessionID(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, ".claude", "projects", "-Users-dev-proj-agentmux", "parent-session", "subagents")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir subagent dir: %v", err)
	}
	path := filepath.Join(projectDir, "agent-a6732a14c39c662.jsonl")
	line := `{"isSidechain":true,"type":"user","timestamp":"2026-09-14T04:36:29.829Z",` +
		`"cwd":"/Users/dev/proj/agentmux","sessionId":"parent-session"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write subagent transcript: %v", err)
	}

	trace, err := parseClaudeTrace(path)
	if err != nil {
		t.Fatalf("parse subagent transcript: %v", err)
	}
	if trace == nil {
		t.Fatalf("subagent transcript with events must produce a trace")
	}
	if trace.SessionID != "agent-a6732a14c39c662" {
		t.Fatalf("subagent must keep its own identity, got %q", trace.SessionID)
	}
	if trace.ParentThreadID != "parent-session" {
		t.Fatalf("borrowed sessionId must become the parent link, got %q", trace.ParentThreadID)
	}
	if observed := observeSessionRole(snapshot.LiveSession{Trace: trace}); observed.Role != "subagent" {
		t.Fatalf("sidechain transcript must observe as a subagent, got %q", observed.Role)
	}
}

// The same parser reads main transcripts, which carry no isSidechain marker.
// A guard that over-triggered would strip every real session id.
func TestParseClaudeTraceStillAdoptsSessionIDOnMainTranscripts(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, ".claude", "projects", "-Users-dev-proj-agentmux")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	path := filepath.Join(projectDir, "9e0b9996.jsonl")
	line := `{"type":"user","timestamp":"2026-09-14T04:36:29.829Z",` +
		`"cwd":"/Users/dev/proj/agentmux","sessionId":"9e0b9996-real-id"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write main transcript: %v", err)
	}

	trace, err := parseClaudeTrace(path)
	if err != nil {
		t.Fatalf("parse main transcript: %v", err)
	}
	if trace == nil || trace.SessionID != "9e0b9996-real-id" {
		t.Fatalf("main transcript must adopt its own sessionId, got %+v", trace)
	}
	if trace.ParentThreadID != "" {
		t.Fatalf("main transcript must not gain a parent link, got %q", trace.ParentThreadID)
	}
}

// grokSessionFixture lays out the real on-disk shape: the working directory is
// percent-encoded as the grandparent directory and the session id is the parent.
func grokSessionFixture(t *testing.T, encodedCwd, sessionID, body string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".grok", "sessions", encodedCwd, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir grok session: %v", err)
	}
	path := filepath.Join(dir, "updates.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestParseGrokTraceReadsEpochSecondsAndPathEncodedProject(t *testing.T) {
	body := `{"timestamp":1788194984,"method":"_x.ai/session/update","params":{"sessionId":"01a058b6-f632-7102-abc9-6767c9ed332d","update":{"sessionUpdate":"agent_message_chunk"}}}` + "\n" +
		`{"timestamp":1788194990,"method":"_x.ai/session/update","params":{"sessionId":"01a058b6-f632-7102-abc9-6767c9ed332d","update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{"outputTokens":8007}}}}` + "\n"
	path := grokSessionFixture(t, "%2FUsers%2Fdev%2Fproj%2Fagentload", "01a058b6-f632-7102-abc9-6767c9ed332d", body)

	trace, err := parseGrokTrace(path)
	if err != nil {
		t.Fatalf("parseGrokTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.SessionID != "01a058b6-f632-7102-abc9-6767c9ed332d" {
		t.Fatalf("expected session id from params, got %q", trace.SessionID)
	}
	if trace.Project != "agentload" {
		t.Fatalf("expected project decoded from the path-encoded cwd, got %q", trace.Project)
	}
	if len(trace.EventTimes) != 2 {
		t.Fatalf("expected both epoch-second lines to count as events, got %d", len(trace.EventTimes))
	}
	if got := trace.FirstEvent.UTC().Format(time.RFC3339); got != "2026-08-31T16:49:44Z" {
		t.Fatalf("expected epoch seconds decoded to UTC, got %s", got)
	}
}

// A line without a usable epoch timestamp must not become an event; the
// pipeline drops traces with no EventTimes, so a bogus zero time would invent
// activity at 1970 instead.
func TestParseGrokTraceSkipsLinesWithoutEpochTimestamp(t *testing.T) {
	body := `{"method":"_x.ai/session/update","params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk"}}}` + "\n" +
		`{"timestamp":0,"params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk"}}}` + "\n"
	path := grokSessionFixture(t, "%2FUsers%2Fdev%2Fproj%2Fagentload", "s1", body)

	trace, err := parseGrokTrace(path)
	if err != nil {
		t.Fatalf("parseGrokTrace: %v", err)
	}
	if trace != nil {
		t.Fatalf("expected no trace from timestamp-less lines, got %#v", trace)
	}
}

// An un-decodable directory name must leave the project unassigned rather than
// guessing one out of the raw encoded string (OPINIONS D-008).
func TestParseGrokTraceLeavesProjectUnassignedWhenPathIsNotEncodedCwd(t *testing.T) {
	body := `{"timestamp":1788194984,"params":{"sessionId":"s1","update":{"sessionUpdate":"agent_message_chunk"}}}` + "\n"
	path := grokSessionFixture(t, "not-an-encoded-path", "s1", body)

	trace, err := parseGrokTrace(path)
	if err != nil {
		t.Fatalf("parseGrokTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.Project != "" {
		t.Fatalf("expected unassigned project for an undecodable directory, got %q", trace.Project)
	}
}
