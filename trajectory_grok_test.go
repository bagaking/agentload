package main

import (
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func trajectoryGrokFixture(t *testing.T, lines ...string) (*trajectory.Service, string) {
	t.Helper()
	path := grokSessionFixture(t, "%2FUsers%2Fdev%2Fproj%2Fagentload", "native-directory-session", strings.Join(lines, "\n")+"\n")
	source := trajectory.Source{Agent: "grok", Path: path, NativeID: grokTranscriptSessionID(path), Decoder: trajectory.GrokDecoder{}}
	service := trajectory.New(func(context.Context) trajectory.SourceSet {
		return trajectory.SourceSet{Sources: []trajectory.Source{source}, Coverage: snapshot.TrajectoryCoverage{Complete: true, Scope: "fixture", Gaps: []string{}}}
	})
	t.Cleanup(func() { _ = service.Close() })
	// Native-format assertions consume a prepared fixture. Foreground queries
	// intentionally contribute only a short, cancellable preparation quantum.
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
	return service, path
}

func TestTrajectoryGrokNativeActionsAndOutOfOrderResults(t *testing.T) {
	result := `{"timestamp":1788194984,"method":"_x.ai/session/update","params":{"sessionId":"native-directory-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-a","status":"completed","content":[{"type":"content","content":{"type":"text","text":"exact result evidence"}}]},"_meta":{"eventId":"event-result","promptId":"p1"}}}`
	call := `{"timestamp":1788194985,"method":"_x.ai/session/update","params":{"sessionId":"native-directory-session","update":{"sessionUpdate":"tool_call","toolCallId":"call-a","title":"Run a command","rawInput":{"command":"pwd"},"_meta":{"x.ai/tool":{"name":"run_terminal_command"}}},"_meta":{"eventId":"event-call","promptId":"p1","updateParams":{"status":"Pending"}}}}`
	intermediate := `{"timestamp":1788194986,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"call-a","rawInput":{"command":"pwd"},"content":[{"type":"content","content":{"type":"text","text":"running"}}]},"_meta":{"eventId":"event-update","promptId":"p1"}}}`
	missingCall := `{"timestamp":1788194987,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"tool_call","title":"Run a command","_meta":{"x.ai/tool":{"name":"run_terminal_command"}}}}}`
	missingResult := `{"timestamp":1788194988,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"tool_call_update","status":"completed","rawOutput":""}}}`
	service, path := trajectoryGrokFixture(t, result, call, intermediate, missingCall, missingResult)
	ctx := context.Background()
	query, err := service.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Agent: "grok", Tool: "run_terminal_command", Kind: "tool_call"})
	if err != nil || len(query.Events) != 2 {
		t.Fatalf("native calls query: %+v %v", query, err)
	}
	if query.Coverage.Complete || !trajectoryGrokGap(query.Coverage.Gaps, "grok_call_id_unavailable") {
		t.Fatal("missing native IDs disappeared from coverage")
	}
	e := query.Events[0]
	if e.NativeID != "event-call" || e.NativeEnvelopeID != "event-call" || e.TurnID != "p1" || e.Tool.CallID != "call-a" || e.Source.Line != 2 {
		t.Fatalf("native IDs or source line lost: %+v", e)
	}
	if e.Source.NativeType != "_x.ai/session/update:tool_call" {
		t.Fatalf("ACP source discriminator lost: %q", e.Source.NativeType)
	}
	read, err := service.Get(ctx, snapshot.TrajectoryGetParams{ID: e.ID, Raw: true})
	if err != nil || len(read.Events) != 1 || read.Events[0].Raw != call+"\n" || read.Events[0].PairID == "" {
		t.Fatalf("native request evidence or reverse-order pairing lost: %+v %v", read, err)
	}
	paired, err := service.Get(ctx, snapshot.TrajectoryGetParams{ID: read.Events[0].PairID, Raw: true})
	if err != nil || len(paired.Events) != 1 || paired.Events[0].Kind != "tool_result" || paired.Events[0].PairID != e.ID || paired.Events[0].Raw != result+"\n" {
		t.Fatalf("result evidence or symmetric pair lost: %+v %v", paired, err)
	}
	missing, err := service.Get(ctx, snapshot.TrajectoryGetParams{ID: query.Events[1].ID})
	if err != nil || missing.Events[0].PairID != "" || !trajectoryGrokGap(missing.Events[0].Omissions, "pair_not_observed") {
		t.Fatalf("title or adjacency inferred a missing call ID: %+v %v", missing, err)
	}
	sessions, err := service.Query(ctx, snapshot.TrajectorySelector{Agent: "grok"})
	if err != nil || len(sessions.Sessions) != 1 || sessions.Sessions[0].NativeID != "native-directory-session" {
		t.Fatalf("constant file stem replaced directory session identity: %+v %v", sessions, err)
	}
	if got := grokWorkdirFromTranscriptPath(path); got != "/Users/dev/proj/agentload" {
		t.Fatalf("native encoded project path lost: %q", got)
	}
}

func TestTrajectoryGrokUsageKeepsPerTurnReplacementSemantics(t *testing.T) {
	line := func(prompt string, output int, model bool) string {
		usage := map[string]any{"inputTokens": 100, "outputTokens": output}
		if model {
			usage["modelUsage"] = map[string]any{"grok-build": map[string]any{"inputTokens": 100, "outputTokens": output}}
		}
		raw, _ := json.Marshal(map[string]any{"timestamp": 1788194984, "method": "_x.ai/session/update", "params": map[string]any{"sessionId": "native-directory-session", "update": map[string]any{"sessionUpdate": "turn_completed", "prompt_id": prompt, "usage": usage}}})
		return string(raw)
	}
	service, _ := trajectoryGrokFixture(t,
		line("p1", 300, true), line("p1", 300, true), line("p1", 350, true),
		`{"timestamp":1788194985,"params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{}}}}`,
		line("p2", 100, true),
		`{"timestamp":1788194986,"params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"p2"}}}`,
	)
	query, err := service.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Agent: "grok", Kind: "usage"})
	if err != nil || len(query.Events) != 4 {
		t.Fatalf("missing observations were counted or recorded updates were lost: %+v %v", query, err)
	}
	// Query is an evidence stream: duplicate and revised source observations
	// retain their locators. Their native scope and replacement contract, not
	// addition of observations or model breakdowns, define the turn totals.
	turns := map[string]int64{}
	for _, event := range query.Events {
		u := event.Usage
		if u == nil || u.Scope != "turn" || u.ScopeID == "" || u.ScopeID != event.TurnID || u.Aggregation != "last_nonempty" {
			t.Fatalf("usage no longer states its measured scope: %+v", event)
		}
		if _, ok := u.Counters["total_tokens"]; ok {
			t.Fatal("missing total synthesized from input/output")
		}
		if u.Models["grok-build"]["output_tokens"] != u.Counters["output_tokens"] {
			t.Fatal("model breakdown changed the outer counters")
		}
		turns[event.SessionID+"\x00"+u.ScopeID] = u.Counters["output_tokens"]
	}
	var total int64
	for _, count := range turns {
		total += count
	}
	if len(turns) != 2 || total != 450 {
		t.Fatalf("duplicate, empty, revised or per-model usage was accumulated: turns=%+v total=%d", turns, total)
	}
}

func TestTrajectoryGrokUnknownFormatsAndUnassignedProject(t *testing.T) {
	service, _ := trajectoryGrokFixture(t, `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"future_vendor_update"}}}`)
	query, err := service.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events"})
	if err != nil || len(query.Events) != 1 || query.Events[0].Kind != "unknown" || query.Coverage.Complete || !trajectoryGrokGap(query.Coverage.Gaps, "unsupported_grok_update:future_vendor_update") {
		t.Fatalf("unsupported native format was hidden: %+v %v", query, err)
	}
	for _, directory := range []string{"not-an-encoded-path", "%GGbroken", "relative%2Fpath"} {
		path := grokSessionFixture(t, directory, "native-id", `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"agent_message_chunk"}}}`+"\n")
		if got := grokWorkdirFromTranscriptPath(path); got != "" {
			t.Fatalf("undecodable directory %q invented project %q", directory, got)
		}
	}
}

func trajectoryGrokGap(gaps []string, want string) bool {
	for _, gap := range gaps {
		if gap == want {
			return true
		}
	}
	return false
}
