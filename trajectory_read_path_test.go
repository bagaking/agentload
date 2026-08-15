package main

import (
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func trajectoryTestApp(t *testing.T) (*trayApp, *httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	now := time.Now()
	dir := filepath.Join(root, "codex", "sessions", now.Format("2006/01/02"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	record := func(typ string, payload any) string {
		b, _ := json.Marshal(map[string]any{"timestamp": now.UTC().Format(time.RFC3339Nano), "type": typ, "payload": payload})
		return string(b) + "\n"
	}
	body := record("session_meta", map[string]any{"id": "fixture", "cwd": root}) + record("response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Inspect Proxy without changing configuration"}}}) + record("response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "call-fixture", "arguments": "{\"cmd\":\"curl --head https://example.test\"}"}) + record("response_item", map[string]any{"type": "function_call_output", "call_id": "call-fixture", "output": "Exit code: 7\nconnection failed"})
	if err := os.WriteFile(filepath.Join(dir, "rollout-"+now.Format("2006-01-02T15-04-05")+"-fixture.jsonl"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	cfg.HistoryFile = filepath.Join(root, "history.jsonl")
	cfg.CodexRoots = []string{filepath.Join(root, "codex")}
	cfg.ClaudeRoots = nil
	cfg.TraeRoots = nil
	cfg.GrokRoots = nil
	cfg.GeminiRoots = nil
	cfg.OpenCodeRoots = nil
	cfg.HermesRoots = nil
	cfg.OpenClawRoots = nil
	cfg.PiRoots = nil
	app := newTrayApp(cfg, newObserver(cfg), log.New(io.Discard, "", 0), nil, "http://127.0.0.1:8642", nil)
	server := httptest.NewServer(app.handler())
	t.Cleanup(func() { server.Close(); _ = app.trajectory.Close() })
	app.trajectoryAccess.endpoint = server.URL
	if err := app.trajectoryAccess.publishInstance(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.trajectoryAccess.removeInstance)
	return app, server, filepath.Join(app.trajectoryAccess.root, "instance.json")
}

func prepareTrajectoryAppFixture(t *testing.T, app *trayApp) {
	t.Helper()
	if !app.trajectoryAccess.isEnabled() {
		t.Fatal("fixture preparation requires explicit content access")
	}
	for _, source := range app.trajectorySources(context.Background()).Sources {
		for attempt := 0; attempt < 32; attempt++ {
			more, err := app.trajectory.PrepareSource(context.Background(), source, app.trajectoryAccess.isEnabled)
			if err != nil {
				t.Fatal("prepare authorized fixture", err)
			}
			if !more {
				break
			}
			if attempt == 31 {
				t.Fatal("authorized fixture did not finish preparation")
			}
		}
	}
}

func TestTrajectoryReadPathExplicitCount(t *testing.T) {
	app, _, instancePath := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	prepareTrajectoryAppFixture(t, app)
	for _, counted := range []bool{false, true} {
		args := []string{"query", "sessions", "--text", "Proxy", "--instance-file", instancePath, "--format", "json"}
		if counted {
			args = append(args, "--count")
		}
		var stdout, stderr bytes.Buffer
		if code := runTrajectoryCLI(args, &stdout, &stderr); code != 0 {
			t.Fatal(stderr.String())
		}
		var got snapshot.TrajectoryQueryResult
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if len(got.Sessions) != 1 || len(got.Sessions[0].MatchedIDs) != 1 || (counted && (got.Sessions[0].MatchedCount == nil || *got.Sessions[0].MatchedCount != 1)) || (!counted && got.Sessions[0].MatchedCount != nil) {
			t.Fatal("CLI page lost exact hit")
		}
		if counted {
			if got.MatchedTotal == nil || *got.MatchedTotal != 1 {
				t.Fatal("CLI count lost")
			}
		} else if got.MatchedTotal != nil {
			t.Fatal("CLI fabricated unknown total")
		}
		stdout.Reset()
		stderr.Reset()
		args = []string{"query", "sessions", "--text", "Proxy", "--instance-file", instancePath}
		if counted {
			args = append(args, "--count")
		}
		if code := runTrajectoryCLI(args, &stdout, &stderr); code != 0 {
			t.Fatal(stderr.String())
		}
		if strings.Contains(stdout.String(), "matched_total: 1\n") != counted {
			t.Fatal("CLI text count visibility differs from request", stdout.String())
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	instance, err := readTrajectoryInstance(instancePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = trajectoryRPC(ctx, instance, "traj.query", snapshot.TrajectorySelector{Count: true, Text: "Proxy"}); err == nil {
		t.Fatal("cancelled count succeeded")
	}
	batch := `[ {"jsonrpc":"2.0","id":1,"method":"traj.query","params":{"count":true}}, {"jsonrpc":"2.0","id":2,"method":"traj.query","params":{"text":"Proxy"}} ]`
	data, err := trajectoryHTTP(context.Background(), instance, "/api/rpc", json.RawMessage(batch))
	if err != nil {
		t.Fatal(err)
	}
	var replies []rpcResponse
	if err = json.Unmarshal(data, &replies); err != nil || len(replies) != 2 || replies[0].Error == nil || replies[0].Error.Code != -32602 || replies[1].Error != nil {
		t.Fatal("batch count lost bounded contract", string(data), err)
	}
}

func TestTrajectoryCountRequestCancelledByShutdown(t *testing.T) {
	app, _, instancePath := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := app.trajectory.Close(); err != nil {
		t.Fatal(err)
	}
	entered, joined := make(chan struct{}), make(chan struct{})
	app.trajectory = trajectory.New(func(ctx context.Context) trajectory.SourceSet {
		close(entered)
		<-ctx.Done()
		close(joined)
		return trajectory.SourceSet{}
	})
	instance, err := readTrajectoryInstance(instancePath)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := trajectoryRPC(context.Background(), instance, "traj.query", snapshot.TrajectorySelector{Count: true})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		app.shutdownCancel()
		t.Fatal("count did not enter its owner")
	}
	app.shutdownCancel() // The first cancellation step in normal app shutdown.
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "-32008") {
			t.Fatal("shutdown count lost cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown waited for the 60-second count budget")
	}
	select {
	case <-joined:
	default:
		t.Fatal("count provider was not joined")
	}
}
func TestTrajectoryReadPathCLIAndRPC(t *testing.T) {
	app, _, instancePath := trajectoryTestApp(t)
	var stdout, stderr bytes.Buffer
	if code := runTrajectoryCLI([]string{"query", "sessions", "--instance-file", instancePath, "--format", "json"}, &stdout, &stderr); code == 0 {
		t.Fatal("disabled content was exposed")
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrajectoryCLI([]string{"access", "on", "--instance-file", instancePath}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	stdout.Reset()
	prepareTrajectoryAppFixture(t, app)
	if code := runTrajectoryCLI([]string{"query", "events", "--tool", "exec_command", "--instance-file", instancePath, "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var q snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &q); err != nil || len(q.Events) != 1 {
		t.Fatalf("CLI query: %s %v", stdout.String(), err)
	}
	id := q.Events[0].ID
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"get", id, "--raw", "--instance-file", instancePath, "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var slice snapshot.TrajectoryGetResult
	if err := json.Unmarshal(stdout.Bytes(), &slice); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range slice.Events {
		found = found || e.ID == id
	}
	if !found {
		t.Fatal("CLI lost RPC event identity")
	}
	if err := app.trajectoryAccess.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"get", id, "--instance-file", instancePath}, &stdout, &stderr); code == 0 {
		t.Fatal("revocation did not cover CLI")
	}
}
func TestTrajectoryReadPathAccessAndProtocol(t *testing.T) {
	app, server, _ := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	do := func(body, auth, origin string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", server.URL+"/api/rpc", strings.NewReader(body))
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = res.Body.Close() })
		return res
	}
	query := `{"jsonrpc":"2.0","id":7,"method":"traj.query","params":{}}`
	if response := do(query, "", ""); response.StatusCode != 403 {
		t.Fatal("public localhost exposed contents")
	}
	if response := do(query, "Bearer "+app.trajectoryAccess.token, "https://outside.example"); response.StatusCode != 403 {
		t.Fatal("foreign origin accepted")
	}
	for _, tc := range []struct {
		body string
		code int
	}{{`{`, -32700}, {`{"jsonrpc":"2.0","id":"bad","method":"missing"}`, -32601}, {`{"jsonrpc":"2.0","id":3,"method":"traj.query","params":{"made_up":1}}`, -32602}, {`{"jsonrpc":"2.0","id":true,"method":"traj.query"}`, -32600}} {
		res := do(tc.body, "Bearer "+app.trajectoryAccess.token, "")
		var rpc rpcResponse
		if json.NewDecoder(res.Body).Decode(&rpc) != nil || rpc.Error == nil || rpc.Error.Code != tc.code {
			t.Fatalf("protocol %s: %+v", tc.body, rpc)
		}
	}
	if res := do(`{"jsonrpc":"2.0","method":"traj.query","params":{}}`, "Bearer "+app.trajectoryAccess.token, ""); res.StatusCode != 204 {
		t.Fatalf("notification response %d", res.StatusCode)
	}
	res := do(query, "Bearer "+app.trajectoryAccess.token, "")
	var rpc rpcResponse
	if json.NewDecoder(res.Body).Decode(&rpc) != nil || string(rpc.ID) != "7" || rpc.Error != nil {
		t.Fatal("request ID or result envelope lost")
	}
	request, _ := http.NewRequest("POST", server.URL+"/api/trajectory/access", strings.NewReader(`{"enabled":false}`))
	request.Header.Set("X-AgentLoad-Local", "1")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 403 || !app.trajectoryAccess.isEnabled() {
		t.Fatal("uncredentialed caller changed content access")
	}
}
func TestTrajectoryReadPathUIUsesService(t *testing.T) {
	assets, _ := fs.Glob(uiAssets, "ui/dist/assets/*.js")
	joined := ""
	for _, asset := range assets {
		body, _ := uiAssets.ReadFile(asset)
		joined += string(body)
	}
	for _, term := range []string{"traj.query", "traj.get", "data-event-id", "data-session-id", "/api/trajectory/access"} {
		if !strings.Contains(joined, term) {
			t.Fatalf("UI contract missing %q; build UI before testing", term)
		}
	}
	if strings.Contains(joined, "demo-proxy-experience") {
		t.Fatal("production knowledge bundle still contains fictional results")
	}
}

// Browser acceptance may launch this fixture server. It writes a private
// receipt, contains only synthetic test logs and has a bounded lifetime.
func TestTrajectoryBrowserFixture(t *testing.T) {
	path := os.Getenv("AGENTLOAD_TRAJECTORY_TEST_INSTANCE_FILE")
	if path == "" {
		t.Skip("browser fixture not requested")
	}
	app, server, _ := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := privateJSON(path, trajectoryInstance{Endpoint: server.URL, Token: app.trajectoryAccess.token, PID: os.Getpid()}); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	deadline := time.NewTimer(8 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
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
