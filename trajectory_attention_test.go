package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

func trajectoryAttentionFixture(t *testing.T) (*trayApp, trajectoryInstance, string) {
	t.Helper()
	app, server, instancePath := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.observer.evidenceIndex.stopIndex() })
	sources := app.trajectorySources(context.Background())
	if len(sources.Sources) != 1 {
		t.Fatalf("attention fixture source unavailable: %+v", sources)
	}
	record := func(kind string, fields map[string]any) string {
		payload := map[string]any{"type": kind, "message": "private-attention-content-sentinel"}
		for key, value := range fields {
			payload[key] = value
		}
		b, _ := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "event_msg", "payload": payload})
		return string(b) + "\n"
	}
	body := record("task_started", map[string]any{"turn_id": "private-attention-turn-a"}) +
		record("task_started", map[string]any{"turn_id": "private-attention-turn-b"}) +
		record("permission_request", map[string]any{"request_id": "private-attention-approval", "turn_id": "private-attention-turn-a"})
	for _, id := range []string{"a", "b", "c"} {
		body += record("tool_error", map[string]any{"call_id": "private-attention-call-" + id, "name": "exec_command", "turn_id": "private-attention-turn-b"})
	}
	f, err := os.OpenFile(sources.Sources[0].Path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(body)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatalf("attention fixture append: %v %v", writeErr, closeErr)
	}
	app.observer.evidenceIndex.recordWatchBatch(evidenceWatchBatch{Complete: true, Paths: []string{sources.Sources[0].Path}})
	return app, trajectoryInstance{Endpoint: server.URL, Token: app.trajectoryAccess.token}, instancePath
}

func TestTrajectoryAttentionRPCAndCLIUseSameEvidence(t *testing.T) {
	app, instance, instancePath := trajectoryAttentionFixture(t)
	ctx := context.Background()
	selector := snapshot.TrajectorySelector{Collection: "attention", Kind: "waiting_permission", State: "open_observed"}
	domain, err := app.trajectory.QueryAttention(ctx, selector)
	if err != nil || len(domain.Attention) != 1 {
		t.Fatalf("attention domain: %+v %v", domain, err)
	}
	body, err := trajectoryRPC(ctx, instance, "traj.query", selector)
	var rpc snapshot.TrajectoryAttentionQueryResult
	if err != nil || json.Unmarshal(body, &rpc) != nil || len(rpc.Attention) != 1 || rpc.Revision != domain.Revision {
		t.Fatalf("attention RPC: %s %v", body, err)
	}
	actual, _ := json.Marshal(rpc)
	want, _ := json.Marshal(domain)
	if !bytes.Equal(actual, want) {
		t.Fatalf("RPC attention differs from shared evidence domain: %s / %s", actual, want)
	}
	var stdout, stderr bytes.Buffer
	if code := runTrajectoryCLI([]string{"query", "attention", "--kind", "waiting_permission", "--state", "open_observed", "--format", "json", "--instance-file", instancePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("attention CLI: %d %s", code, stderr.String())
	}
	var cli snapshot.TrajectoryAttentionQueryResult
	if json.Unmarshal(stdout.Bytes(), &cli) != nil || len(cli.Attention) != 1 {
		t.Fatalf("attention CLI result: %s", stdout.String())
	}
	actual, _ = json.Marshal(cli)
	if !bytes.Equal(actual, want) {
		t.Fatal("CLI attention did not use the RPC domain projection")
	}
	focus := domain.Attention[0].Interventions[0].Evidence[0]
	body, err = trajectoryRPC(ctx, instance, "traj.get", map[string]any{"id": focus.EventID, "view": "attention"})
	var explained snapshot.TrajectoryAttentionGetResult
	if err != nil || json.Unmarshal(body, &explained) != nil || explained.Attention == nil || explained.FocusID != focus.EventID || explained.Attention.ID != domain.Attention[0].ID || explained.Attention.Liveness != "unknown" {
		t.Fatalf("focused attention RPC: %s %v", body, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrajectoryCLI([]string{"get", focus.EventID, "--view", "attention", "--format", "json", "--instance-file", instancePath}, &stdout, &stderr); code != 0 {
		t.Fatalf("focused attention CLI default-around handling: %d %s", code, stderr.String())
	}
	var cliGet snapshot.TrajectoryAttentionGetResult
	if json.Unmarshal(stdout.Bytes(), &cliGet) != nil || cliGet.FocusID != explained.FocusID || cliGet.Attention == nil || cliGet.Attention.SessionID != explained.Attention.SessionID {
		t.Fatal("CLI focused attention used a different evidence identity")
	}
	body, err = trajectoryRPC(ctx, instance, "traj.get", snapshot.TrajectoryGetParams{ID: focus.EventID, Raw: true})
	var raw snapshot.TrajectoryGetResult
	if err != nil || json.Unmarshal(body, &raw) != nil || len(raw.Events) != 1 || raw.Events[0].Source != focus.Source || !strings.Contains(raw.Events[0].Raw, "private-attention-approval") {
		t.Fatalf("attention reference did not navigate original evidence: %s %v", body, err)
	}
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"get", focus.EventID, "--raw", "--around", "0", "--format", "json", "--instance-file", instancePath}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var cliRaw snapshot.TrajectoryGetResult
	if json.Unmarshal(stdout.Bytes(), &cliRaw) != nil || len(cliRaw.Events) != 1 || cliRaw.Events[0].ID != focus.EventID || cliRaw.Events[0].Source != focus.Source {
		t.Fatal("CLI --around 0 omitted the explicit zero or changed source identity")
	}
	cliAttention, _ := json.Marshal(cliGet)
	for _, value := range []string{string(actual), string(want), string(cliAttention)} {
		if strings.Contains(value, "private-attention-content-sentinel") || strings.Contains(value, `"arguments":`) || strings.Contains(value, `"raw":`) {
			t.Fatal("attention projection included transcript bodies")
		}
	}
}

func TestTrajectoryAttentionAccessAndUnsupportedPredicates(t *testing.T) {
	app, instance, _ := trajectoryAttentionFixture(t)
	for _, selector := range []snapshot.TrajectorySelector{
		{Collection: "attention", Text: "private"}, {Collection: "attention", EntityKind: "path"}, {Collection: "attention", ContextID: "ctx.unknown"}, {Collection: "attention", ActorKind: "unknown"},
	} {
		if _, err := trajectoryRPC(context.Background(), instance, "traj.query", selector); err == nil {
			t.Fatalf("RPC ignored unsupported attention predicate: %+v", selector)
		}
	}
	q, err := app.trajectory.QueryAttention(context.Background(), snapshot.TrajectorySelector{})
	if err != nil || len(q.Attention) != 1 {
		t.Fatal(err)
	}
	if _, err := trajectoryRPC(context.Background(), instance, "traj.get", map[string]any{"id": q.Attention[0].ID, "view": "attention", "around": 1}); err == nil {
		t.Fatal("attention silently ignored an explicit nonzero around")
	}
	request, _ := http.NewRequest(http.MethodPost, instance.Endpoint+"/api/rpc", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"traj.query","params":{"collection":"attention"}}`))
	res, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatal("unauthenticated caller accessed attention evidence")
	}
	if err := app.trajectoryAccess.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	if _, err := trajectoryRPC(context.Background(), instance, "traj.query", snapshot.TrajectorySelector{Collection: "attention"}); err == nil {
		t.Fatal("revoked access still exposed cached attention")
	}
}

func TestDiagnosticTrajectoryAttentionIsPrivateAndResourceAttentionUnchanged(t *testing.T) {
	app, instance, _ := trajectoryAttentionFixture(t)
	// Seed an existing resource observation so the real public handlers can be
	// compared before and after the authenticated trajectory explanation.
	public := snapshot.Snapshot{LiveSessions: []snapshot.LiveSessionSnapshot{{SessionID: "resource-session", AttentionState: "needs_review", AttentionReason: "existing resource observation"}}}
	public.Diagnostics = buildDiagnosticsSnapshot(public, time.Now())
	app.lastMu.Lock()
	app.lastSnapshot, app.haveSnapshot = public, true
	app.lastMu.Unlock()
	readPublic := func(path string) []byte {
		t.Helper()
		res, err := http.Get(instance.Endpoint + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != http.StatusOK || !json.Valid(body) {
			t.Fatalf("public diagnostic response %s: %d %s %v", path, res.StatusCode, body, err)
		}
		return body
	}
	before := readPublic("/api/snapshot")
	var resourceBefore snapshot.Snapshot
	if json.Unmarshal(before, &resourceBefore) != nil || len(resourceBefore.LiveSessions) != 1 {
		t.Fatal("public resource fixture unavailable")
	}
	domain, err := app.trajectory.QueryAttention(context.Background(), snapshot.TrajectorySelector{Collection: "attention", Kind: "stuck_candidate", State: "candidate"})
	if err != nil || len(domain.Attention) != 1 {
		t.Fatalf("private diagnostic candidate unavailable: %+v %v", domain, err)
	}
	body, err := trajectoryRPC(context.Background(), instance, "traj.query", snapshot.TrajectorySelector{Collection: "attention", Kind: "stuck_candidate", State: "candidate"})
	var rpc snapshot.TrajectoryAttentionQueryResult
	if err != nil || json.Unmarshal(body, &rpc) != nil || len(rpc.Attention) != 1 {
		t.Fatalf("authenticated diagnostic explanation: %s %v", body, err)
	}
	got, _ := json.Marshal(rpc.Attention[0])
	want, _ := json.Marshal(domain.Attention[0])
	if !bytes.Equal(got, want) {
		t.Fatal("authenticated diagnostic projection changed trajectory reasons or evidence")
	}
	after := readPublic("/api/snapshot")
	exported := readPublic("/api/diagnostic-export")
	var resourceAfter snapshot.Snapshot
	if json.Unmarshal(after, &resourceAfter) != nil || len(resourceAfter.LiveSessions) != 1 || resourceAfter.LiveSessions[0].AttentionState != resourceBefore.LiveSessions[0].AttentionState || resourceAfter.LiveSessions[0].AttentionReason != resourceBefore.LiveSessions[0].AttentionReason {
		t.Fatal("private trajectory attention migrated existing resource attention")
	}
	for _, publicBody := range [][]byte{after, exported} {
		for _, privateValue := range []string{"private-attention-", domain.Attention[0].ID, "native-attention-v1", `"interventions":`, `"latest_action":`} {
			if bytes.Contains(publicBody, []byte(privateValue)) {
				t.Fatalf("public snapshot/export exposed private trajectory evidence %q", privateValue)
			}
		}
	}
}
