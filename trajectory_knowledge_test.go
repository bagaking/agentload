package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func knowledgeCLI(t *testing.T, instance string, args ...string) []byte {
	t.Helper()
	var out, errOut bytes.Buffer
	args = append(args, "--instance-file", instance, "--format", "json")
	if code := runTrajectoryCLI(args, &out, &errOut); code != 0 {
		t.Fatalf("CLI %v: %s", args[:1], errOut.String())
	}
	return out.Bytes()
}

func knowledgeCLIAnnotation(t *testing.T, instance string, params snapshot.TrajectoryAnnotationParams) snapshot.TrajectoryAnnotationResult {
	t.Helper()
	path := filepath.Join(t.TempDir(), "annotation.json")
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	data := knowledgeCLI(t, instance, "annotate", "--file", path)
	var result snapshot.TrajectoryAnnotationResult
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTrajectoryKnowledgeCLIAndRPC(t *testing.T) {
	app, _, instancePath := trajectoryTestApp(t)
	knowledgeCLI(t, instancePath, "access", "on")
	prepareTrajectoryAppFixture(t, app)
	var events snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(knowledgeCLI(t, instancePath, "query", "events", "--tool", "exec_command"), &events); err != nil || len(events.Events) != 1 {
		t.Fatalf("current source: %+v %v", events, err)
	}
	eventID := events.Events[0].ID
	created := knowledgeCLIAnnotation(t, instancePath, snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "candidate", SourceIDs: []string{eventID}, Text: "Local candidate from observed action"})
	if created.Record == nil || created.Record.Sources[0].EventID != eventID || created.Record.Applicability != nil {
		t.Fatalf("CLI altered source binding: %+v", created)
	}
	verification := knowledgeCLIAnnotation(t, instancePath, snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "verification", SourceIDs: []string{eventID}, TargetID: created.Record.ID, Text: "Reported scoped observation", Applicability: &snapshot.TrajectoryKnowledgeApplicability{Version: "fixture-v1"}})
	if verification.Record.Links[0].Kind != "verification_of" || verification.Record.ScopeStatus != "partial" {
		t.Fatal(verification)
	}
	instance, err := readTrajectoryInstance(instancePath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := trajectoryRPC(context.Background(), instance, "traj.get", snapshot.TrajectoryGetParams{ID: created.Record.ID})
	var got snapshot.TrajectoryGetResult
	if err != nil || json.Unmarshal(data, &got) != nil || got.Knowledge.ID != created.Record.ID || got.Knowledge.Kind != "candidate" || len(got.Events) != 0 || bytes.Contains(data, []byte("curl --head")) || bytes.Contains(data, []byte("connection failed")) {
		t.Fatalf("RPC result loses annotation/source boundaries: %s %v", data, err)
	}
	var textOut, errOut bytes.Buffer
	if code := runTrajectoryCLI([]string{"get", created.Record.ID, "--instance-file", instancePath}, &textOut, &errOut); code != 0 || !strings.Contains(textOut.String(), created.Record.Text) {
		t.Fatalf("human get omitted knowledge: %s %s", textOut.String(), errOut.String())
	}
	var q snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(knowledgeCLI(t, instancePath, "query", "knowledge", "--kind", "candidate", "--state", "active", "--tool", "exec_command"), &q); err != nil || len(q.Knowledge) != 1 || q.Knowledge[0].ID != created.Record.ID {
		t.Fatalf("CLI knowledge query: %+v %v", q, err)
	}
	withdraw := knowledgeCLIAnnotation(t, instancePath, snapshot.TrajectoryAnnotationParams{Operation: "withdraw", ID: created.Record.ID})
	if withdraw.Record.State != "withdrawn" {
		t.Fatal(withdraw)
	}
	data, err = trajectoryRPC(context.Background(), instance, "traj.get", snapshot.TrajectoryGetParams{ID: verification.Record.ID})
	if err != nil || json.Unmarshal(data, &got) != nil || got.Knowledge.Links[0].Status != "withdrawn" || got.Knowledge.State != "active" {
		t.Fatalf("linked record: %s %v", data, err)
	}
	deleted := knowledgeCLIAnnotation(t, instancePath, snapshot.TrajectoryAnnotationParams{Operation: "delete", ID: created.Record.ID})
	if deleted.DeletedID != created.Record.ID {
		t.Fatal(deleted)
	}
	if _, err := trajectoryRPC(context.Background(), instance, "traj.get", snapshot.TrajectoryGetParams{ID: created.Record.ID}); err == nil {
		t.Fatal("deleted annotation still readable")
	}
	indexPath := filepath.Join(app.trajectoryAccess.root, "trajectory.sqlite")
	indexBefore, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	notesPath := filepath.Join(app.trajectoryAccess.root, "annotations.bbolt")
	notesBefore, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeCLI(t, instancePath, "access", "off")
	indexAfter, err := os.ReadFile(indexPath)
	if err != nil || !bytes.Equal(indexBefore, indexAfter) {
		t.Fatalf("access off altered useful exceptions/checkpoints: %v", err)
	}
	notesAfter, err := os.ReadFile(notesPath)
	if err != nil || !bytes.Equal(notesBefore, notesAfter) {
		t.Fatalf("access off altered authored notes: %v", err)
	}
	if _, err := trajectoryRPC(context.Background(), instance, "traj.get", snapshot.TrajectoryGetParams{ID: verification.Record.ID}); err == nil {
		t.Fatal("content off still exposed retained annotation")
	}
	if _, err := trajectoryRPC(context.Background(), instance, "traj.get", snapshot.TrajectoryGetParams{ID: eventID, View: "raw"}); err == nil {
		t.Fatal("content off still exposed retained source")
	}
	if _, err := trajectoryRPC(context.Background(), instance, "traj.query", snapshot.TrajectorySelector{Collection: "events"}); err == nil {
		t.Fatal("content off still exposed retained query content")
	}
	knowledgeCLI(t, instancePath, "access", "on")
	data, err = trajectoryRPC(context.Background(), instance, "traj.get", snapshot.TrajectoryGetParams{ID: verification.Record.ID})
	if err != nil || json.Unmarshal(data, &got) != nil || got.Knowledge.ID != verification.Record.ID || got.Knowledge.Links[0].Status != "missing" {
		t.Fatalf("authored note not retained: %s %v", data, err)
	}
	var restored snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(knowledgeCLI(t, instancePath, "query", "events", "--tool", "exec_command"), &restored); err != nil || len(restored.Events) != 1 || restored.Events[0].ID != eventID {
		t.Fatal("re-authorized source changed canonical identity", err)
	}
}

func TestTrajectoryKnowledgeAccessAuthenticatedWrites(t *testing.T) {
	app, server, instancePath := trajectoryTestApp(t)
	do := func(params any, auth, origin string) (int, rpcResponse) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "traj.annotate", "params": params})
		req, _ := http.NewRequest("POST", server.URL+"/api/rpc", bytes.NewReader(body))
		req.Header.Set("Authorization", auth)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var rpc rpcResponse
		if res.StatusCode == http.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&rpc); err != nil {
				t.Fatal(err)
			}
		} else {
			_, _ = io.Copy(io.Discard, res.Body)
		}
		return res.StatusCode, rpc
	}
	if status, _ := do(map[string]any{"operation": "create"}, "Bearer "+app.trajectoryAccess.token, ""); status != http.StatusForbidden {
		t.Fatal("disabled content allowed annotation")
	}
	knowledgeCLI(t, instancePath, "access", "on")
	var events snapshot.TrajectoryQueryResult
	prepareTrajectoryAppFixture(t, app)
	if err := json.Unmarshal(knowledgeCLI(t, instancePath, "query", "events", "--tool", "exec_command"), &events); err != nil {
		t.Fatal(err)
	}
	p := snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "observation", SourceIDs: []string{events.Events[0].ID}, Text: "PRIVATE_AUTHORED_INPUT"}
	for _, credentials := range [][2]string{{"", ""}, {"Bearer wrong-token", ""}, {"Bearer " + app.trajectoryAccess.token, "https://outside.example"}} {
		if status, _ := do(p, credentials[0], credentials[1]); status != http.StatusForbidden {
			t.Fatalf("unauthorized write: %v %d", credentials, status)
		}
	}
	badID := strings.Split(events.Events[0].ID, ".")
	badID[1] = strings.Repeat("0", 16)
	badSource := p
	badSource.SourceIDs = []string{strings.Join(badID, ".")}
	_, rpc := do(badSource, "Bearer "+app.trajectoryAccess.token, "")
	if rpc.Error == nil || rpc.Error.Code != -32004 {
		t.Fatalf("foreign source ID accepted: %+v", rpc)
	}
	_, rpc = do(map[string]any{"operation": "create", "kind": "observation", "source_ids": p.SourceIDs, "text": p.Text, "execute": "unrecognized"}, "Bearer "+app.trajectoryAccess.token, "")
	if rpc.Error == nil || rpc.Error.Code != -32602 {
		t.Fatalf("unknown write field accepted: %+v", rpc)
	}
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{"operation":"create","kind":"candidate","text":"CLI_PRIVATE_INPUT","execute":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if code := runTrajectoryCLI([]string{"annotate", "--file", path, "--instance-file", instancePath}, &out, &errOut); code == 0 || strings.Contains(errOut.String(), "CLI_PRIVATE_INPUT") || strings.Contains(out.String(), "CLI_PRIVATE_INPUT") {
		t.Fatalf("malformed CLI input exposed or accepted: %s %s", out.String(), errOut.String())
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 32*1024+1)), 0600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errOut.Reset()
	if code := runTrajectoryCLI([]string{"annotate", "--file", path, "--instance-file", instancePath}, &out, &errOut); code == 0 {
		t.Fatal("oversized annotation file accepted")
	}
	var q snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(knowledgeCLI(t, instancePath, "query", "knowledge"), &q); err != nil || len(q.Knowledge) != 0 {
		t.Fatalf("rejected writes persisted notes: %+v %v", q, err)
	}
}

func TestTrajectoryKnowledgeAccessRevocationRace(t *testing.T) {
	app, _, instancePath := trajectoryTestApp(t)
	knowledgeCLI(t, instancePath, "access", "on")
	prepareTrajectoryAppFixture(t, app)
	instance, err := readTrajectoryInstance(instancePath)
	if err != nil {
		t.Fatal(err)
	}
	var events snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(knowledgeCLI(t, instancePath, "query", "events", "--tool", "exec_command"), &events); err != nil {
		t.Fatal(err)
	}
	p := snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "observation", SourceIDs: []string{events.Events[0].ID}, Text: "Concurrent explicit annotation"}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, _ = trajectoryRPC(context.Background(), instance, "traj.annotate", p)
		}()
	}
	close(start)
	knowledgeCLI(t, instancePath, "access", "off")
	wg.Wait()
	if _, err := trajectoryRPC(context.Background(), instance, "traj.annotate", p); err == nil {
		t.Fatal("completed revocation allowed later write")
	}
	if _, err := trajectoryRPC(context.Background(), instance, "traj.query", snapshot.TrajectorySelector{Collection: "knowledge"}); err == nil {
		t.Fatal("completed revocation allowed later read")
	}
	knowledgeCLI(t, instancePath, "access", "on")
	var q snapshot.TrajectoryQueryResult
	if err := json.Unmarshal(knowledgeCLI(t, instancePath, "query", "knowledge"), &q); err != nil || len(q.Knowledge) > 4 {
		t.Fatalf("revocation race corrupted authored storage: %+v %v", q, err)
	}
}
