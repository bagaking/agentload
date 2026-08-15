package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A fixed native-format dataset for browser and native acceptance. Never
// points at personal transcript roots and never executes recorded commands.
func trajectoryAcceptanceApp(t *testing.T, root string) (*trayApp, *httptest.Server) {
	t.Helper()
	now := time.Now().UTC()
	record := func(typ string, payload any) string {
		b, _ := json.Marshal(map[string]any{"timestamp": now.Format(time.RFC3339Nano), "type": typ, "payload": payload})
		return string(b) + "\n"
	}
	write := func(path, body string) {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := defaultConfig()
	cfg.HistoryFile = filepath.Join(root, "history.jsonl")
	cfg.CodexRoots = []string{filepath.Join(root, "codex")}
	cfg.ClaudeRoots = []string{filepath.Join(root, "claude")}
	cfg.TraeRoots = []string{filepath.Join(root, "trae")}
	cfg.GrokRoots = []string{filepath.Join(root, "grok")}
	cfg.GeminiRoots = nil
	cfg.OpenCodeRoots = nil
	cfg.HermesRoots = nil
	cfg.OpenClawRoots = nil
	cfg.PiRoots = nil
	body := record("session_meta", map[string]any{"id": "codex-fixture", "cwd": root})
	body += record("response_item", map[string]any{"type": "message", "id": "request", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "<system-reminder>fixture injection</system-reminder>Inspect Proxy with $proxy-debugger"}}})
	body += record("event_msg", map[string]any{"type": "task_started", "turn_id": "turn-fixture"})
	body += record("turn_context", map[string]any{"turn_id": "turn-fixture", "cwd": root})
	body += record("response_item", map[string]any{"type": "function_call", "id": "call-native", "call_id": "call-fixture", "name": "exec_command", "arguments": `{"cmd":"cat /fixture/skills/bagakit-researcher/SKILL.md; curl --proxy localhost:7890 --head https://example.test"}`})
	body += record("response_item", map[string]any{"type": "function_call_output", "id": "result-native", "call_id": "call-fixture", "output": "Exit code: 7\nproxy connection failed"})
	body += record("event_msg", map[string]any{"type": "permission_request", "request_id": "permission-fixture", "turn_id": "turn-fixture"})
	body += record("compacted", map[string]any{"message": "Proxy investigation checkpoint"})
	write(filepath.Join(cfg.CodexRoots[0], "sessions", now.Format("2006/01/02"), "rollout-"+now.Format("2006-01-02T15-04-05")+"-fixture.jsonl"), body)
	claude := `{"type":"user","uuid":"request-claude","sessionId":"claude-fixture","message":{"role":"user","content":"Inspect Proxy configuration"}}` + "\n"
	for _, id := range []string{"a", "b", "c"} {
		claude += `{"type":"assistant","uuid":"call-` + id + `","message":{"id":"msg-` + id + `","role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"Read","input":{"file_path":"proxy.json"}}]}}` + "\n" + `{"type":"user","uuid":"result-` + id + `","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"` + id + `","is_error":true,"content":"file missing"}]}}` + "\n"
	}
	write(filepath.Join(cfg.ClaudeRoots[0], "projects", "fixture", "claude-fixture.jsonl"), claude)
	trae := record("session_meta", map[string]any{"id": "trae-fixture", "cwd": root}) + record("response_item", map[string]any{"type": "message", "id": "request-trae", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Inspect Proxy route"}}}) + record("response_item", map[string]any{"type": "function_call", "call_id": "trae-call", "name": "exec_command", "arguments": "{}"}) + record("response_item", map[string]any{"type": "function_call_output", "call_id": "trae-call", "output": "route observed"})
	write(filepath.Join(cfg.TraeRoots[0], "sessions", now.Format("2006/01/02"), "trae-fixture.jsonl"), trae)
	grok := `{"timestamp":1788194984,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"tool_call","toolCallId":"grok-call","rawInput":{"command":"inspect proxy"},"_meta":{"x.ai/tool":{"name":"run_terminal_command"}}},"_meta":{"eventId":"grok-call-event","promptId":"grok-turn"}}}` + "\n" + `{"timestamp":1788194985,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"grok-call","status":"completed","rawOutput":"Proxy route observed"},"_meta":{"eventId":"grok-result-event","promptId":"grok-turn"}}}` + "\n"
	write(filepath.Join(cfg.GrokRoots[0], "sessions", "%2Ffixture%2Fworkspace", "grok-fixture", "updates.jsonl"), grok)
	app := newTrayApp(cfg, newObserver(cfg), log.New(io.Discard, "", 0), nil, "http://127.0.0.1:8642", nil)
	server := httptest.NewServer(app.handler())
	app.trajectoryAccess.endpoint = server.URL
	t.Cleanup(func() { server.Close(); _ = app.trajectory.Close(); app.trajectoryAccess.removeInstance() })
	if err := app.trajectoryAccess.publishInstance(); err != nil {
		t.Fatal(err)
	}
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	events, err := app.trajectory.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Agent: "codex", Tool: "exec_command"})
	// Warm the fixture for cross-entry acceptance. Cold archive work may
	// yield explicitly pending without changing the native evidence contract.
	for attempt := 0; err == nil && slices.Contains(events.Coverage.Gaps, "index_pending") && attempt < 32; attempt++ {
		events, err = app.trajectory.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Agent: "codex", Tool: "exec_command"})
	}
	if err != nil || len(events.Events) != 1 {
		t.Fatalf("acceptance native call: %v %+v", err, events)
	}
	source := events.Events[0].ID
	candidate, err := app.trajectory.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "candidate", SourceIDs: []string{source}, Text: "Proxy endpoint failure deserves checking; general solution is unverified."})
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.trajectory.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "counterexample", TargetID: candidate.Record.ID, SourceIDs: []string{source}, Text: "The same recorded command does not prove a successful fix."})
	if err != nil {
		t.Fatal(err)
	}
	return app, server
}

func TestTrajectoryAcceptanceNativeMatrix(t *testing.T) {
	app, server := trajectoryAcceptanceApp(t, t.TempDir())
	ctx := context.Background()
	for _, agent := range []string{"codex", "claude", "trae", "grok"} {
		var stdout, stderr bytes.Buffer
		args := []string{"query", "events", "--agent", agent, "--format", "json", "--instance-file", filepath.Join(app.trajectoryAccess.root, "instance.json")}
		if code := runTrajectoryCLI(args, &stdout, &stderr); code != 0 {
			t.Fatal(stderr.String())
		}
		var cli snapshot.TrajectoryQueryResult
		if err := json.Unmarshal(stdout.Bytes(), &cli); err != nil || len(cli.Events) == 0 {
			t.Fatalf("%s native evidence absent", agent)
		}
		direct, err := app.trajectory.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Agent: agent})
		if err != nil || len(cli.Events) != len(direct.Events) {
			t.Fatal("transport changed semantics")
		}
		for i, e := range cli.Events {
			if e.ID != direct.Events[i].ID {
				t.Fatal("transport identity changed")
			}
			got, err := app.trajectory.Get(ctx, snapshot.TrajectoryGetParams{ID: e.ID, Raw: true})
			if err != nil || len(got.Events) != 1 || got.Events[0].Raw == "" {
				t.Fatalf("%s native raw unreadable: %v", agent, err)
			}
		}
	}
	if app.trajectoryAccess.isEnabled() == false {
		t.Fatal("fixture access")
	}
	response, err := server.Client().Get(server.URL + "/api/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "general solution is unverified") {
		t.Fatal("private annotation in public snapshot")
	}
}

func TestTrajectoryAcceptanceLiteralSkillSearch(t *testing.T) {
	app, _ := trajectoryAcceptanceApp(t, t.TempDir())
	calls, err := app.trajectory.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Agent: "codex", Tool: "exec_command"})
	if err != nil || len(calls.Events) != 1 {
		t.Fatalf("tool query: %+v %v", calls, err)
	}
	var stdout, stderr bytes.Buffer
	if code := runTrajectoryCLI([]string{"query", "sessions", "--text", "bagakit-researcher", "--agent", "codex", "--format", "json", "--instance-file", filepath.Join(app.trajectoryAccess.root, "instance.json")}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var q snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &q); err != nil || len(q.Sessions) != 1 || len(q.Sessions[0].MatchedIDs) == 0 || q.Sessions[0].MatchedIDs[0] != calls.Events[0].ID || !strings.Contains(q.Sessions[0].MatchedPreview, "bagakit-researcher") {
		t.Fatalf("ordinary Skill search differs from native tool event: %+v expected=%s err=%v", q, calls.Events[0].ID, err)
	}
}

func TestTrajectoryUIFixture(t *testing.T) {
	path := os.Getenv("AGENTLOAD_TRAJECTORY_TEST_INSTANCE_FILE")
	if path == "" {
		t.Skip("UI acceptance not requested")
	}
	root := os.Getenv("AGENTLOAD_TRAJECTORY_ACCEPTANCE_ROOT")
	if root == "" {
		root = t.TempDir()
	}
	app, server := trajectoryAcceptanceApp(t, root)
	if err := privateJSON(path, trajectoryInstance{Endpoint: server.URL, Token: app.trajectoryAccess.token, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	deadline := time.NewTimer(4 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			return
		case <-ticker.C:
			if _, err := os.Stat(path + ".stop"); err == nil {
				_ = os.Remove(path + ".stop")
				return
			}
		}
	}
}
