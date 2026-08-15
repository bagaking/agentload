package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendNativeContextRecords(t *testing.T, app *trayApp, records ...string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(app.cfg.CodexRoots[0], "sessions", "*", "*", "*", "*.jsonl"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("native fixture path: %v %v", paths, err)
	}
	f, err := os.OpenFile(paths[0], os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(records, "\n") + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestTrajectoryContextNativeCodexUnknownInputCanonicalRPCAndCLI(t *testing.T) {
	app, _, instance := trajectoryTestApp(t)
	appendNativeContextRecords(t, app,
		`{"type":"turn_context","payload":{"turn_id":"native-turn","cwd":"/recorded/workspace","model":"recorded-model"}}`,
		`{"type":"compacted","payload":{"message":"Recorded compacted summary","replacement_history":[{"type":"message","role":"user","content":[{"type":"input_text","text":"unverified replacement visibility"}]}]}}`,
	)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	query := []string{"query", "contexts", "--context-scope", "actual_input", "--instance-file", instance, "--format", "json"}
	if code := runTrajectoryCLI(query, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var cli snapshot.TrajectoryContextQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &cli); err != nil || len(cli.Contexts) != 1 || cli.Contexts[0].Scope != "actual_input" || cli.Contexts[0].Membership != "unknown" || cli.Coverage.Complete {
		t.Fatalf("native CLI input context: %s %v", stdout.String(), err)
	}
	ctx := context.Background()
	canonical, err := app.trajectory.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input"})
	if err != nil || canonical.Contexts[0].ID != cli.Contexts[0].ID || canonical.Contexts[0].AnchorID != cli.Contexts[0].AnchorID || cli.Contexts[0].Source.Line != 5 || cli.Contexts[0].Source.Digest == "" {
		t.Fatalf("RPC and canonical context disagree: %+v %v", canonical, err)
	}
	events, err := app.trajectory.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Kind: "context"})
	if err != nil || len(events.Events) != 1 || events.Events[0].ID != cli.Contexts[0].AnchorID || events.Events[0].TurnID != "native-turn" {
		t.Fatal("native turn descriptor lost canonical source anchor")
	}
	stdout.Reset()
	stderr.Reset()
	get := []string{"get", cli.Contexts[0].ID, "--view", "context", "--around", "0", "--instance-file", instance, "--format", "json"}
	if code := runTrajectoryCLI(get, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var manifest snapshot.TrajectoryContextGetResult
	if err := json.Unmarshal(stdout.Bytes(), &manifest); err != nil || manifest.Context == nil || manifest.Context.ID != cli.Contexts[0].ID || len(manifest.Members) != 0 || manifest.Context.Membership != "unknown" || manifest.Coverage.Complete {
		t.Fatalf("native context get fabricated model inputs: %s %v", stdout.String(), err)
	}
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"get", cli.Contexts[0].ID, "--view", "context", "--around", "0", "--instance-file", instance}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), cli.Contexts[0].ID) || !strings.Contains(stdout.String(), "actual_input\tunknown") || !strings.Contains(stdout.String(), "model_input_membership_unknown") {
		t.Fatalf("human manifest hides native scope uncertainty: %s", stdout.String())
	}
	params, _ := json.Marshal(snapshot.TrajectorySelector{Collection: "events", ContextID: cli.Contexts[0].ID})
	rpc, err := app.callTrajectory(ctx, "traj.query", params)
	if err != nil || len(rpc.(snapshot.TrajectoryQueryResult).Events) != 0 || rpc.(snapshot.TrajectoryQueryResult).Coverage.Complete {
		t.Fatalf("native archived history became later model input: %+v %v", rpc, err)
	}
	archive, err := app.trajectory.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "archive"})
	if err != nil || len(archive.Contexts) != 2 || archive.Contexts[1].Transformation == nil || archive.Contexts[1].Transformation.Kind != "summary" || archive.Contexts[1].Transformation.OriginalsStatus != "unknown" || archive.Contexts[1].BeforeID != archive.Contexts[0].ID {
		t.Fatalf("native compacted record overclaimed transformation: %+v %v", archive, err)
	}
	workspace, err := app.trajectory.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "workspace", Kind: "context"})
	if err != nil || len(workspace.Contexts) != 1 || workspace.Contexts[0].Workspace.Path != "/recorded/workspace" || workspace.Contexts[0].ID == cli.Contexts[0].ID {
		t.Fatalf("workspace and actual input context mixed: %+v %v", workspace, err)
	}
}

func TestTrajectoryContextNativeArchiveCLIManifestContinuationAndScopeValidation(t *testing.T) {
	app, _, instance := trajectoryTestApp(t)
	records := []string{}
	for i := 0; i < 24; i++ {
		records = append(records, fmt.Sprintf(`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"archive native request %d"}]}}`, i))
	}
	appendNativeContextRecords(t, app, records...)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runTrajectoryCLI([]string{"query", "contexts", "--context-scope", "archive", "--instance-file", instance, "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var query snapshot.TrajectoryContextQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &query); err != nil || len(query.Contexts) != 1 || query.Contexts[0].KnownMembers != 28 {
		t.Fatalf("native archive query: %s %v", stdout.String(), err)
	}
	id := query.Contexts[0].ID
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"get", id, "--view", "context", "--around", "0", "--instance-file", instance, "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var first snapshot.TrajectoryContextGetResult
	if err := json.Unmarshal(stdout.Bytes(), &first); err != nil || len(first.Members) == 0 || first.NextOffset == nil || !first.Truncated || len(stdout.Bytes()) > 5121 {
		t.Fatalf("native archive bound: %s %v", stdout.String(), err)
	}
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"get", id, "--view", "context", "--around", "0", "--member-offset", fmt.Sprint(*first.NextOffset), "--instance-file", instance, "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var second snapshot.TrajectoryContextGetResult
	if err := json.Unmarshal(stdout.Bytes(), &second); err != nil || len(second.Members) == 0 || second.MemberOffset != *first.NextOffset || second.Members[0].Source.Line != first.Members[len(first.Members)-1].Source.Line+1 {
		t.Fatalf("native manifest continuation disagrees with source: %s %v", stdout.String(), err)
	}
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"get", id, "--view", "context", "--around", "0", "--instance-file", instance}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), first.Members[0].EventIDs[0]) || !strings.Contains(stdout.String(), "archive_record") || !strings.Contains(stdout.String(), "next member offset:") {
		t.Fatal("rendered native manifest lost identity/scope/continuation")
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrajectoryCLI([]string{"query", "events", "--context", id, "--instance-file", instance}, &stdout, &stderr); code == 0 {
		t.Fatal("archive incorrectly accepted as model-input event selection")
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrajectoryCLI([]string{"watch", "events", "--context", id, "--once", "--timeout-ms", "0", "--instance-file", instance}, &stdout, &stderr); code == 0 {
		t.Fatal("watch silently ignored unsupported ContextID")
	}
}
