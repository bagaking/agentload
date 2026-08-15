package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTrajectoryActorsNativeUnknownIdentityRPCAndCLIRendering(t *testing.T) {
	app, _, instance := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	args := []string{"query", "actors", "--actor-kind", "unknown", "--instance-file", instance, "--format", "json"}
	if code := runTrajectoryCLI(args, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var cli snapshot.TrajectoryActorQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &cli); err != nil || len(cli.Actors) != 4 || cli.Coverage.Complete {
		t.Fatalf("native actor query: %s %v", stdout.String(), err)
	}
	rpc, err := app.callTrajectory(context.Background(), "traj.query", json.RawMessage(`{"collection":"actors","actor_kind":"unknown"}`))
	if err != nil {
		t.Fatal(err)
	}
	canonical := rpc.(snapshot.TrajectoryActorQueryResult)
	for i, actor := range cli.Actors {
		if actor.Actor.Kind != "unknown" || actor.Actor.ID != "" || actor.EventID != canonical.Actors[i].EventID || actor.ID != canonical.Actors[i].ID || actor.Source.Digest == "" {
			t.Fatalf("actor identity differs across native/RPC/CLI: %+v", actor)
		}
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrajectoryCLI([]string{"query", "actors", "--actor-kind", "unknown", "--instance-file", instance}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), "\tunknown\t") || !strings.Contains(stdout.String(), cli.Actors[1].EventID) || strings.Contains(stdout.String(), "\thuman\t") {
		t.Fatal("human-readable rendering replaced canonical unknown identity")
	}
	humans, err := app.callTrajectory(context.Background(), "traj.query", json.RawMessage(`{"collection":"actors","actor_kind":"human"}`))
	if err != nil {
		t.Fatal(err)
	}
	noKnownHumans := humans.(snapshot.TrajectoryActorQueryResult)
	if len(noKnownHumans.Actors) != 0 || noKnownHumans.Coverage.Complete {
		t.Fatal("unknown identity reported as confirmed absence of humans")
	}
}

func TestTrajectoryRelationsNativeClaudeParentAndExactToolIDs(t *testing.T) {
	s := claudeTrajectoryService(t,
		`{"type":"user","uuid":"parent-uuid","message":{"role":"user","content":"native request"}}`,
		`{"type":"assistant","uuid":"child-uuid","parentUuid":"parent-uuid","message":{"id":"native-message","role":"assistant","content":[{"type":"text","text":"inspect in parallel"},{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"a.go"}},{"type":"tool_use","id":"call-b","name":"Read","input":{"file_path":"b.go"}}]}}`,
		`{"type":"user","uuid":"results-uuid","parentUuid":"child-uuid","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-b","content":"b"},{"type":"tool_result","tool_use_id":"call-a","content":""}]}}`,
	)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	events, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Limit: 50})
	if err != nil || len(events.Events) != 6 {
		t.Fatalf("native events: %+v %v", events, err)
	}
	parents, err := s.QueryRelations(ctx, snapshot.TrajectorySelector{RelationKind: "parent"})
	if err != nil || len(parents.Relations) != 2 {
		t.Fatalf("native parent metadata: %+v %v", parents, err)
	}
	for _, edge := range parents.Relations {
		if edge.Status != "resolved" || edge.NativeField != "/parentUuid" || edge.Kind != "parent" || edge.Source.Block != 0 {
			t.Fatalf("native parent changed to reply/delegation: %+v", edge)
		}
	}
	tools, err := s.QueryRelations(ctx, snapshot.TrajectorySelector{RelationKind: "tool_result", Kind: "tool_result"})
	if err != nil || len(tools.Relations) != 2 || tools.Relations[0].From != events.Events[4].ID || tools.Relations[1].From != events.Events[5].ID {
		t.Fatalf("native tool relation query: %+v %v", tools, err)
	}
	if tools.Relations[0].TargetIDs[0] != events.Events[3].ID || tools.Relations[1].TargetIDs[0] != events.Events[2].ID {
		t.Fatal("parallel results paired by order instead of native IDs")
	}
	read, err := s.GetRelations(ctx, snapshot.TrajectoryGetParams{ID: events.Events[4].ID, Around: 0})
	if err != nil || len(read.Relations) != 2 || len(read.Nodes) != 4 {
		t.Fatalf("native graph get: %+v %v", read, err)
	}
	for _, node := range read.Nodes {
		if node.Actor.Kind != "unknown" || node.Source.Digest == "" {
			t.Fatal("source-backed native node lost identity boundary")
		}
	}
	if _, err := s.QueryRelations(ctx, snapshot.TrajectorySelector{RelationKind: "reply"}); err != nil {
		t.Fatal(err)
	}
	replies, _ := s.QueryRelations(ctx, snapshot.TrajectorySelector{RelationKind: "reply"})
	if len(replies.Relations) != 0 {
		t.Fatal("parent metadata claimed conversational reply")
	}
}

func TestTrajectoryRelationsCanonicalRPCAndCLIGet(t *testing.T) {
	app, _, instance := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := runTrajectoryCLI([]string{"query", "relations", "--relation-kind", "tool_result", "--instance-file", instance, "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var q snapshot.TrajectoryRelationQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &q); err != nil || len(q.Relations) != 1 || q.Relations[0].Status != "resolved" {
		t.Fatalf("CLI relation query: %s %v", stdout.String(), err)
	}
	edge := q.Relations[0]
	canonical, err := app.trajectory.QueryRelations(context.Background(), snapshot.TrajectorySelector{RelationKind: "tool_result"})
	if err != nil || canonical.Relations[0].ID != edge.ID || canonical.Relations[0].From != edge.From {
		t.Fatalf("CLI and service graph disagree: %+v %v", canonical, err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := runTrajectoryCLI([]string{"get", edge.From, "--view", "relations", "--around", "0", "--instance-file", instance, "--format", "json"}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var read snapshot.TrajectoryRelationQueryResult
	if err := json.Unmarshal(stdout.Bytes(), &read); err != nil || len(read.Relations) != 1 || read.Relations[0].ID != edge.ID || read.Relations[0].TargetIDs[0] != edge.TargetIDs[0] {
		t.Fatalf("CLI graph get lost canonical edge: %s %v", stdout.String(), err)
	}
	stdout.Reset()
	if code := runTrajectoryCLI([]string{"query", "relations", "--relation-kind", "tool_result", "--instance-file", instance}, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(stdout.String(), edge.From) || !strings.Contains(stdout.String(), "resolved") {
		t.Fatal("rendered relation lacks canonical identity and evidence status")
	}
}

func TestTrajectoryBranchesNativeDuplicateUUIDAndSidechainStayUnknown(t *testing.T) {
	s := claudeTrajectoryService(t,
		`{"type":"user","uuid":"duplicate-parent","sessionId":"shared-session","isSidechain":true,"message":{"role":"user","content":"first branch record"}}`,
		`{"type":"user","uuid":"duplicate-parent","sessionId":"shared-session","isSidechain":true,"message":{"role":"user","content":"second branch record"}}`,
		`{"type":"assistant","uuid":"resumed-child","parentUuid":"duplicate-parent","sessionId":"shared-session","isSidechain":true,"message":{"id":"resumed-message","role":"assistant","content":[{"type":"text","text":"resume branch"}]}}`,
	)
	t.Cleanup(func() { _ = s.Close() })
	q, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if err != nil || len(q.Relations) != 1 || q.Relations[0].Status != "ambiguous" || len(q.Relations[0].TargetIDs) != 0 || len(q.Relations[0].CandidateIDs) != 2 {
		t.Fatalf("native duplicate ID chose a branch: %+v %v", q, err)
	}
	for _, node := range q.Nodes {
		if node.Branch != nil || node.Actor.Kind != "unknown" {
			t.Fatal("sidechain marker invented branch/actor identity")
		}
	}
}
