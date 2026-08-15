package main

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestExtraAgentProcessIdentities(t *testing.T) {
	registry := defaultCodingAgentRegistry(Config{})
	cases := []struct {
		command string
		want    string
	}{
		{"gemini --prompt hello", "gemini"},
		{"opencode run", "opencode"},
		{"python -m hermes_cli.main", "hermes"},
		{"openclaw gateway", "openclaw"},
		{"penclaw run", "openclaw"},
		{"pi --resume abc", "pi"},
	}
	for _, tc := range cases {
		got, _ := registry.detectProcess(tc.command)
		if got != tc.want {
			t.Errorf("detectProcess(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestGeminiTranscriptParserCapturesEventsAndUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	body := "{" + `"timestamp":"2026-09-19T10:00:00Z","role":"user"` + "}\n" +
		"{" + `"timestamp":"2026-09-19T10:00:01Z","role":"assistant","cwd":"/tmp/project","tokens":{"input":100,"cached":20,"output":30,"thoughts":4}` + "}\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	trace, err := parseOneExtraSession(t, "gemini", path)
	if err != nil {
		t.Fatal(err)
	}
	if trace == nil || len(trace.EventTimes) != 2 || trace.Project != "project" {
		t.Fatalf("unexpected trace: %+v", trace)
	}
	if trace.TokenUsage.InputTokens != 80 || trace.TokenUsage.CacheReadInputTokens != 20 || trace.TokenUsage.OutputTokens != 26 || trace.TokenUsage.ReasoningOutputTokens != 4 {
		t.Fatalf("unexpected usage: %+v", trace.TokenUsage)
	}
}

func TestExtraOutputUsageDecoderReadsPerMessageOutput(t *testing.T) {
	decoder := extraOutputUsageDecoder{kind: "gemini"}
	observation, ok := decoder.DecodeUsage([]byte(`{"id":"m1","timestamp":"2026-09-19T10:00:00Z","message":{"role":"assistant","usage":{"output":7}}}`))
	if !ok || observation.OutputTokens != 7 || observation.MessageIdentity != "m1" {
		t.Fatalf("unexpected live usage observation: %+v ok=%t", observation, ok)
	}
}

func TestOpenClawAndPiTranscriptParsersCaptureSessionMetadata(t *testing.T) {
	root := t.TempDir()
	openClawPath := filepath.Join(root, "openclaw.jsonl")
	openClawLine := map[string]interface{}{
		"type": "message", "id": "m1",
		"message": map[string]interface{}{"role": "assistant", "timestamp": "2026-09-19T10:00:00Z", "cwd": "/tmp/claw", "usage": map[string]interface{}{"input": 10, "output": 5}},
	}
	writeJSONLine(t, openClawPath, openClawLine)
	claw, err := parseOneExtraSession(t, "openclaw", openClawPath)
	if err != nil || claw == nil || claw.Project != "claw" || claw.TokenUsage.OutputTokens != 5 {
		t.Fatalf("unexpected openclaw trace: trace=%+v err=%v", claw, err)
	}

	piPath := filepath.Join(root, "pi.jsonl")
	writeJSONLine(t, piPath, map[string]interface{}{"type": "session", "id": "pi-session", "cwd": "/tmp/pi-project"})
	writeJSONLine(t, piPath, map[string]interface{}{"type": "message", "timestamp": "2026-09-19T10:00:00Z", "message": map[string]interface{}{"role": "assistant", "usage": map[string]interface{}{"input": 8, "output": 3}}})
	pi, err := parseOneExtraSession(t, "pi", piPath)
	if err != nil || pi == nil || pi.SessionID != "pi-session" || pi.Project != "pi-project" || pi.TokenUsage.TotalTokens != 11 {
		t.Fatalf("unexpected pi trace: trace=%+v err=%v", pi, err)
	}
}

func TestHermesAndOpenCodeSQLiteParsers(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is required for the macOS local adapters")
	}
	dir := t.TempDir()
	hermes := filepath.Join(dir, "state.db")
	runSQLiteTestSQL(t, hermes, `CREATE TABLE sessions (id TEXT PRIMARY KEY, model TEXT, parent_session_id TEXT, started_at REAL, ended_at REAL, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER, reasoning_tokens INTEGER); INSERT INTO sessions VALUES ('h1','model',NULL,1726740000,1726740060,80,20,10,0,5); INSERT INTO sessions VALUES ('h2','model',NULL,1726740100,1726740160,40,12,0,0,2);`)
	// One database holds many sessions, so the parser is asserted through
	// ParseSessions -- the production entry point. A helper that returned only
	// the first trace would stay green while every later session vanished.
	hermesTraces := parseExtraSessions(t, "hermes", hermes)
	if len(hermesTraces) != 2 {
		t.Fatalf("expected both hermes sessions, got %d", len(hermesTraces))
	}
	hermesTrace := traceBySessionID(t, hermesTraces, "h1")
	if hermesTrace.TokenUsage.OutputTokens != 15 {
		t.Fatalf("unexpected hermes usage: %+v", hermesTrace.TokenUsage)
	}
	if got := hermesTrace.ModelUsage["model"].OutputTokens; got != 15 {
		t.Fatalf("expected hermes model usage to retain model identity, got %d", got)
	}

	opencode := filepath.Join(dir, "opencode.db")
	runSQLiteTestSQL(t, opencode, `CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT); CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT); INSERT INTO session VALUES ('ses_1','/tmp/code'); INSERT INTO session VALUES ('ses_2','/tmp/other'); INSERT INTO message VALUES ('msg_1','ses_1',1726740000,'{"role":"assistant","modelID":"m","time":{"created":1726740000},"path":{"cwd":"/tmp/code"},"tokens":{"input":12,"output":4}}'); INSERT INTO message VALUES ('msg_2','ses_2',1726740100,'{"role":"assistant","modelID":"m","time":{"created":1726740100},"path":{"cwd":"/tmp/other"},"tokens":{"input":6,"output":2}}');`)
	opencodeTraces := parseExtraSessions(t, "opencode", opencode)
	if len(opencodeTraces) != 2 {
		t.Fatalf("expected both opencode sessions, got %d", len(opencodeTraces))
	}
	opencodeTrace := traceBySessionID(t, opencodeTraces, "ses_1")
	if opencodeTrace.Project != "code" || opencodeTrace.TokenUsage.OutputTokens != 4 {
		t.Fatalf("unexpected opencode trace: %+v", opencodeTrace)
	}
	if got := opencodeTrace.ModelUsage["m"].OutputTokens; got != 4 {
		t.Fatalf("expected opencode model usage, got %d", got)
	}
}

// parseExtraSessions runs the registry's own parser, so a test exercises the
// same path production does.
func parseExtraSessions(t *testing.T, kind, path string) []*snapshot.SessionTrace {
	t.Helper()
	traces, err := extraTranscriptParser{kind: kind}.ParseSessions(
		context.Background(), snapshot.TranscriptFile{Tool: kind, Path: path})
	if err != nil {
		t.Fatalf("parse %s sessions: %v", kind, err)
	}
	return traces
}

func parseOneExtraSession(t *testing.T, kind, path string) (*snapshot.SessionTrace, error) {
	t.Helper()
	traces, err := extraTranscriptParser{kind: kind}.ParseSessions(
		context.Background(), snapshot.TranscriptFile{Tool: kind, Path: path})
	if err != nil || len(traces) == 0 {
		return nil, err
	}
	if len(traces) != 1 {
		t.Fatalf("expected one %s session, got %d", kind, len(traces))
	}
	return traces[0], err
}

func traceBySessionID(t *testing.T, traces []*snapshot.SessionTrace, id string) *snapshot.SessionTrace {
	t.Helper()
	for _, trace := range traces {
		if trace != nil && trace.SessionID == id {
			return trace
		}
	}
	t.Fatalf("session %s missing from %d parsed traces", id, len(traces))
	return nil
}

func TestExtraTranscriptDiscoveryUsesBoundedLayouts(t *testing.T) {
	cases := []struct {
		kind string
		rel  string
		bad  string
	}{
		{"gemini", "tmp/project/chats/session-1.json", "tmp/project/cache/session-1.json"},
		{"opencode", "opencode.db", "storage/other/message.json"},
		{"hermes", "state.db", "logs/state.db"},
		{"openclaw", "agents/main/sessions/s1.jsonl", "agents/main/sessions/s1.trajectory.jsonl"},
		{"pi", "agent/session-artifacts/s1/child.jsonl", "agent/cache/child.jsonl"},
	}
	for _, tc := range cases {
		root := t.TempDir()
		good := filepath.Join(root, filepath.FromSlash(tc.rel))
		bad := filepath.Join(root, filepath.FromSlash(tc.bad))
		if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(good, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bad, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		found := extraTranscriptDiscovery{kind: tc.kind}.Discover(context.Background(), tc.kind, []string{root}, time.Time{})
		if len(found.Files) != 1 || found.Files[0].File.Path != good {
			t.Fatalf("%s discovery = %+v, want %s only", tc.kind, found.Files, good)
		}
	}
}

func TestOpenCodeLegacyMessageJSONParser(t *testing.T) {
	path := filepath.Join(t.TempDir(), "message.json")
	data := `{"id":"m1","role":"assistant","time":{"created":1726740000},"path":{"cwd":"/tmp/legacy-code"},"tokens":{"input":12,"output":4}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	trace, err := parseOneExtraSession(t, "opencode", path)
	if err != nil || trace == nil || trace.Project != "legacy-code" || trace.TokenUsage.OutputTokens != 4 {
		t.Fatalf("unexpected legacy OpenCode trace: trace=%+v err=%v", trace, err)
	}
}

func writeJSONLine(t *testing.T, path string, value interface{}) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		t.Fatal(err)
	}
}

func runSQLiteTestSQL(t *testing.T, path, sql string) {
	t.Helper()
	if err := exec.CommandContext(context.Background(), "sqlite3", path, sql).Run(); err != nil {
		t.Fatal(err)
	}
}

// A SQLite database used to bypass the size+mtime cache entirely, because a
// database mtime does not move when a write is committed to the WAL. The
// bypass made every scan re-parse the whole file: measured 410ms per scan,
// forever, against ~9ms once cached.
//
// agentEvidenceStat closes that gap by reporting database+WAL metadata, so the
// bypass is gone and a database caches like any other evidence file. This test
// pins both halves: an untouched database must be served from cache, and a
// WAL-only write must still invalidate it.
func TestAgentDatabaseCachesUntilTheWALMoves(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Skip("sqlite3 is required for the macOS local adapters")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")
	runSQLiteTestSQL(t, path, `PRAGMA journal_mode=WAL; CREATE TABLE sessions (id TEXT PRIMARY KEY, model TEXT, parent_session_id TEXT, started_at REAL, ended_at REAL, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER, reasoning_tokens INTEGER); INSERT INTO sessions VALUES ('h1','model',NULL,1726740000,1726740060,80,20,10,0,5);`)

	file := snapshot.TranscriptFile{Tool: "hermes", Path: path}
	before, err := agentEvidenceStat(file)
	if err != nil {
		t.Fatalf("stat database: %v", err)
	}

	// An unchanged database must now look unchanged to the cache decision.
	unchanged, err := agentEvidenceStat(file)
	if err != nil {
		t.Fatalf("restat database: %v", err)
	}
	if unchanged.Size() != before.Size() || !unchanged.ModTime().Equal(before.ModTime()) {
		t.Fatal("an untouched database reported new metadata, so it would re-parse on every scan")
	}

	// A committed write must move the reported metadata even when it lands in
	// the WAL. Without this the cache would serve a stale trace forever.
	runSQLiteTestSQL(t, path, `INSERT INTO sessions VALUES ('h2','model',NULL,1726740100,1726740160,40,12,0,0,2);`)
	after, err := agentEvidenceStat(file)
	if err != nil {
		t.Fatalf("stat after write: %v", err)
	}
	if after.Size() == before.Size() && after.ModTime().Equal(before.ModTime()) {
		t.Fatal("a committed database write did not change the reported metadata, so the cache would go stale")
	}

	traces := parseExtraSessions(t, "hermes", path)
	if len(traces) != 2 {
		t.Fatalf("expected the written session to be visible, got %d traces", len(traces))
	}
}

// Antigravity is a second gemini evidence root. Its records carry created_at
// but the corpus has no token field of any kind (measured on 44783 real
// records), so it must contribute session spans and never a token number.
func TestAntigravityTranscriptsAreTimelineEvidenceOnly(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "antigravity-cli", "brain", "s1", ".system_generated", "logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"USER_INPUT","created_at":"2026-08-23T20:55:52Z","source":"USER_EXPLICIT","content":"hi"}` + "\n" +
		`{"type":"PLANNER_RESPONSE","created_at":"2026-08-23T20:56:10Z","source":"MODEL","content":"ok"}` + "\n" +
		`{"type":"RUN_COMMAND","created_at":"2026-08-23T20:56:20Z","source":"MODEL","content":"ls"}` + "\n"
	path := filepath.Join(dir, "transcript.jsonl")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Byte-identical sibling: admitting it too would count every session twice.
	if err := os.WriteFile(filepath.Join(dir, "transcript_full.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	found := extraTranscriptDiscovery{kind: "gemini"}.Discover(
		context.Background(), "gemini", []string{root}, time.Time{})
	if len(found.Files) != 1 || found.Files[0].File.Path != path {
		t.Fatalf("discovery = %+v, want transcript.jsonl only", found.Files)
	}

	trace, err := parseOneExtraSession(t, "gemini", path)
	if err != nil || trace == nil {
		t.Fatalf("parse antigravity transcript: trace=%+v err=%v", trace, err)
	}
	// The two conversational turns are timeline evidence; RUN_COMMAND is not a
	// turn and must not inflate the span.
	if len(trace.EventTimes) != 2 {
		t.Fatalf("expected the two conversational turns, got %d events", len(trace.EventTimes))
	}
	if trace.TokenUsage.TotalTokens != 0 || trace.TokenUsage.OutputTokens != 0 || trace.TokenUsage.InputTokens != 0 {
		t.Fatalf("antigravity has no token fields, so reporting any is fabrication: %+v", trace.TokenUsage)
	}
}
