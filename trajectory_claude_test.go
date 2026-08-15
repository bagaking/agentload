package main

import (
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func claudeTrajectoryService(t *testing.T, records ...string) *trajectory.Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude-session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(records, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	source := trajectory.Source{Agent: "claude", Path: path, NativeID: "claude-session", Decoder: trajectory.ClaudeDecoder{}}
	service := trajectory.New(func(context.Context) trajectory.SourceSet {
		return trajectory.SourceSet{
			Sources:  []trajectory.Source{source},
			Coverage: snapshot.TrajectoryCoverage{Complete: true, Scope: "claude fixture", Gaps: []string{}},
		}
	})
	t.Cleanup(func() { _ = service.Close() })
	// These assertions concern decoded native evidence. Complete the same
	// background preparation used by the app rather than assuming that the
	// cancellable foreground quantum is a fixture readiness barrier.
	for attempt := 0; attempt < 32; attempt++ {
		more, err := service.PrepareSource(context.Background(), source, func() bool { return true })
		if err != nil {
			t.Fatal("prepare native fixture", err)
		}
		if !more {
			break
		}
		if attempt == 31 {
			t.Fatal("native fixture preparation did not finish")
		}
	}
	return service
}

func TestTrajectoryClaudeServiceNativeBlocksConcurrentCallsAndEmptyResult(t *testing.T) {
	request := `{"type":"user","uuid":"request-1","sessionId":"native-session","timestamp":"2026-10-01T01:00:00Z","message":{"role":"user","content":"<system-reminder>injected text</system-reminder>Inspect files"}}`
	calls := `{"type":"assistant","uuid":"calls-1","parentUuid":"request-1","timestamp":"2026-10-01T01:00:01Z","message":{"id":"msg-calls","role":"assistant","content":[{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"a.go"}},{"type":"text","text":"also inspect b"},{"type":"tool_use","id":"call-b","name":"Read","input":{"file_path":"b.go"}}]}}`
	results := `{"type":"user","uuid":"results-1","timestamp":"2026-10-01T01:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-b","is_error":true,"content":"missing file"},{"type":"tool_result","tool_use_id":"call-a","is_error":false,"content":""}]}}`
	s := claudeTrajectoryService(t, request, calls, results)
	ctx := context.Background()
	q, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Agent: "claude", Limit: 50})
	if err != nil || len(q.Events) != 6 || !q.Coverage.Complete {
		t.Fatalf("query: %+v %v", q, err)
	}
	wantKinds := []string{"text", "tool_call", "text", "tool_call", "tool_result", "tool_result"}
	wantLines := []int{1, 2, 2, 2, 3, 3}
	wantBlocks := []int{0, 0, 1, 2, 0, 1}
	seen := map[string]bool{}
	for i, e := range q.Events {
		if e.Kind != wantKinds[i] || e.Source.Line != wantLines[i] || e.Source.Block != wantBlocks[i] || e.Source.Digest == "" {
			t.Fatalf("record/block order lost at %d: %+v", i, e)
		}
		if e.ID == "" || seen[e.ID] || e.Actor.Kind != "unknown" {
			t.Fatalf("event identity or actor changed: %+v", e)
		}
		seen[e.ID] = true
	}
	for i := 1; i <= 3; i++ {
		if q.Events[i].NativeID != "msg-calls" || q.Events[i].NativeEnvelopeID != "calls-1" || q.Events[i].Role != "assistant" {
			t.Fatalf("assistant message identity changed: %+v", q.Events[i])
		}
	}
	tools, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Tool: "Read"})
	if err != nil || len(tools.Events) != 2 || tools.Events[0].ID != q.Events[1].ID || tools.Events[1].ID != q.Events[3].ID {
		t.Fatalf("tool query differs: %+v %v", tools, err)
	}
	for _, pair := range [][2]int{{1, 5}, {3, 4}, {5, 1}, {4, 3}} {
		read, err := s.Get(ctx, snapshot.TrajectoryGetParams{ID: q.Events[pair[0]].ID, Around: 0, Raw: true})
		if err != nil || len(read.Events) != 1 {
			t.Fatalf("get: %+v %v", read, err)
		}
		e := read.Events[0]
		if e.PairID != q.Events[pair[1]].ID || e.ID != q.Events[pair[0]].ID || strings.Contains(strings.Join(e.Omissions, "|"), "pair_not_observed") {
			t.Fatalf("native ID pairing lost: %+v", e)
		}
		if e.Source.Line == 3 && (e.Role != "tool" || e.ProtocolRole != "user" || e.NativeEnvelopeID != "results-1") {
			t.Fatalf("tool result transport role lost: %+v", e)
		}
		if e.Raw != calls+"\n" && e.Raw != results+"\n" {
			t.Fatal("raw evidence differs from its source line")
		}
	}
	empty, err := s.Get(ctx, snapshot.TrajectoryGetParams{ID: q.Events[5].ID, Around: 0})
	if err != nil || empty.Events[0].Text != "" || empty.Events[0].Outcome != "not_error" {
		t.Fatalf("empty reply not addressable: %+v %v", empty, err)
	}
	requestRead, err := s.Get(ctx, snapshot.TrajectoryGetParams{ID: q.Events[0].ID, Around: 0, Raw: true})
	if err != nil || requestRead.Events[0].Raw != request+"\n" || requestRead.Events[0].Text != "Inspect files" {
		t.Fatal("presentation cleaning changed source evidence")
	}
	sessions, err := s.Query(ctx, snapshot.TrajectorySelector{})
	if err != nil || len(sessions.Sessions) != 1 || sessions.Sessions[0].Title != "Inspect files" {
		t.Fatalf("request session query: %+v %v", sessions, err)
	}
}

func TestTrajectoryClaudeServiceUnknownAndUnpairedEvidence(t *testing.T) {
	s := claudeTrajectoryService(t,
		`{"type":"user","uuid":"agent-request","isSidechain":true,"sessionId":"parent-session","parentUuid":"unobserved","message":{"role":"user","content":"agent transport input"}}`,
		`{"type":"user","uuid":"orphan-result","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"missing-call","content":""}]}}`,
		`{"type":"permission-mode","permissionMode":"bypassPermissions"}`,
		`{"type":"system","subtype":"unknown_wait_marker","content":"waiting"}`,
	)
	q, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Limit: 50})
	if err != nil || len(q.Events) != 4 || q.Coverage.Complete {
		t.Fatalf("unknown coverage hidden: %+v %v", q, err)
	}
	for _, e := range q.Events {
		if e.Actor.Kind != "unknown" || e.Actor.ID != "" || e.SessionID == "parent-session" || e.TurnID != "" {
			t.Fatalf("actor, child identity, or turn inferred: %+v", e)
		}
	}
	if q.Events[2].Kind != "unknown" || q.Events[3].Kind != "unknown" {
		t.Fatal("unsupported approval or waiting event inferred")
	}
	read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: q.Events[1].ID, Around: 0})
	if err != nil || len(read.Events) != 1 || read.Events[0].PairID != "" || !strings.Contains(strings.Join(read.Events[0].Omissions, ","), "pair_not_observed") {
		t.Fatalf("orphan result paired without ID evidence: %+v %v", read, err)
	}
}
