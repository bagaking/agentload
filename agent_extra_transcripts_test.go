package main

import (
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
	trace, err := parseExtraTrace("gemini", TranscriptFile{Tool: "gemini", Path: path}, 0, nil)
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
	decoder := newExtraOutputUsageDecoder()
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
	claw, err := parseExtraTrace("openclaw", TranscriptFile{Tool: "openclaw", Path: openClawPath}, 0, nil)
	if err != nil || claw == nil || claw.Project != "claw" || claw.TokenUsage.OutputTokens != 5 {
		t.Fatalf("unexpected openclaw trace: trace=%+v err=%v", claw, err)
	}

	piPath := filepath.Join(root, "pi.jsonl")
	writeJSONLine(t, piPath, map[string]interface{}{"type": "session", "id": "pi-session", "cwd": "/tmp/pi-project"})
	writeJSONLine(t, piPath, map[string]interface{}{"type": "message", "timestamp": "2026-09-19T10:00:00Z", "message": map[string]interface{}{"role": "assistant", "usage": map[string]interface{}{"input": 8, "output": 3}}})
	pi, err := parseExtraTrace("pi", TranscriptFile{Tool: "pi", Path: piPath}, 0, nil)
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
	runSQLiteTestSQL(t, hermes, `CREATE TABLE sessions (id TEXT PRIMARY KEY, model TEXT, parent_session_id TEXT, started_at REAL, ended_at REAL, input_tokens INTEGER, output_tokens INTEGER, cache_read_tokens INTEGER, cache_write_tokens INTEGER, reasoning_tokens INTEGER); INSERT INTO sessions VALUES ('h1','model',NULL,1726740000,1726740060,80,20,10,0,5);`)
	hermesTrace, err := parseHermesStateDB(hermes)
	if err != nil || hermesTrace == nil || hermesTrace.SessionID != "h1" || hermesTrace.TokenUsage.OutputTokens != 15 {
		t.Fatalf("unexpected hermes trace: trace=%+v err=%v", hermesTrace, err)
	}

	opencode := filepath.Join(dir, "opencode.db")
	runSQLiteTestSQL(t, opencode, `CREATE TABLE session (id TEXT PRIMARY KEY, directory TEXT); CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT); INSERT INTO session VALUES ('ses_1','/tmp/code'); INSERT INTO message VALUES ('msg_1','ses_1',1726740000,'{"role":"assistant","modelID":"m","time":{"created":1726740000},"path":{"cwd":"/tmp/code"},"tokens":{"input":12,"output":4}}');`)
	opencodeTrace, err := parseOpenCodeDBTrace(opencode)
	if err != nil || opencodeTrace == nil || opencodeTrace.SessionID != "ses_1" || opencodeTrace.Project != "code" || opencodeTrace.TokenUsage.OutputTokens != 4 {
		t.Fatalf("unexpected opencode trace: trace=%+v err=%v", opencodeTrace, err)
	}
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
	trace, err := parseExtraTrace("opencode", TranscriptFile{Tool: "opencode", Path: path}, 0, nil)
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
