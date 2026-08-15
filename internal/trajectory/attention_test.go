package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func attentionRecord(kind string, fields map[string]any) string {
	payload := map[string]any{"type": kind}
	for key, value := range fields {
		payload[key] = value
	}
	return codexRecord("event_msg", payload)
}

func attentionFailure(id, tool, turn string) string {
	return attentionRecord("tool_error", map[string]any{"call_id": id, "name": tool, "turn_id": turn, "message": "synthetic native failure"})
}

func attentionCall(id, tool, turn string) string {
	return codexRecord("response_item", map[string]any{"type": "function_call", "call_id": id, "name": tool, "turn_id": turn, "arguments": `{}`})
}

func attentionOnly(t *testing.T, service *Service) snapshot.TrajectoryAttention {
	t.Helper()
	q, err := service.QueryAttention(context.Background(), snapshot.TrajectorySelector{})
	if err != nil || len(q.Attention) != 1 {
		t.Fatalf("attention query: %+v %v", q, err)
	}
	if q.Rules.ErrorThreshold != 3 || q.Rules.ActionWindow != 20 || q.Rules.Version == "" || len(q.Rules.RecoveryConditions) != 3 || q.Rules.PermissionClosure == "" || q.Rules.QuestionRule == "" {
		t.Fatalf("rule contract missing: %+v", q.Rules)
	}
	return q.Attention[0]
}

func attentionIntervention(a snapshot.TrajectoryAttention, kind, request string) *snapshot.TrajectoryIntervention {
	for i := range a.Interventions {
		if a.Interventions[i].Kind == kind && (request == "" || a.Interventions[i].RequestID == request) {
			return &a.Interventions[i]
		}
	}
	return nil
}

func TestTrajectoryAttentionConcurrentProgressAndPermission(t *testing.T) {
	body := attentionRecord("task_started", map[string]any{"turn_id": "turn-a"}) +
		attentionRecord("task_started", map[string]any{"turn_id": "turn-b"}) +
		attentionRecord("permission_request", map[string]any{"approval_id": "approve-a", "turn_id": "turn-a"}) +
		attentionRecord("permission_response", map[string]any{"approval_id": "unrelated", "turn_id": "turn-b"}) +
		attentionCall("unclosed-call", "exec_command", "turn-b")
	s, _ := fixture(t, body)
	a := attentionOnly(t, s)
	permission := attentionIntervention(a, "waiting_permission", "approve-a")
	if a.Progress.Status != "observed" || len(a.Progress.Turns) != 2 || a.Progress.Turns[0].Status != "open_observed" || a.Progress.Turns[1].Status != "open_observed" || permission == nil || permission.Status != "open_observed" {
		t.Fatalf("concurrent execution and approval did not coexist: %+v", a)
	}
	if a.Liveness != "unknown" || a.LatestAction == nil || attentionIntervention(a, "stuck_candidate", "") != nil {
		t.Fatal("an unclosed call or historical progress implied live execution or stuck")
	}
	ref := permission.Evidence[0]
	read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: ref.EventID, Raw: true})
	if err != nil || len(read.Events) != 1 || read.Events[0].Source != ref.Source || ref.NativeField != "/payload/approval_id" || !strings.Contains(read.Events[0].Raw, `"approval_id":"approve-a"`) {
		t.Fatalf("permission is not navigable original evidence: %+v %+v %v", ref, read, err)
	}
}

func TestTrajectoryAttentionPermissionClosureRequiresExactNativeReference(t *testing.T) {
	for _, tc := range []struct {
		name, body, id, status string
	}{
		{"out-of-order", attentionRecord("permission_response", map[string]any{"request_id": "a"}) + attentionRecord("permission_request", map[string]any{"request_id": "b"}) + attentionRecord("permission_request", map[string]any{"request_id": "a"}), "a", "closed_observed"},
		{"different-id", attentionRecord("permission_request", map[string]any{"request_id": "a"}) + attentionRecord("permission_response", map[string]any{"request_id": "b"}), "a", "open_observed"},
		{"different-id-family", attentionRecord("permission_request", map[string]any{"request_id": "a"}) + attentionRecord("permission_response", map[string]any{"approval_id": "a"}), "a", "open_observed"},
		{"duplicate-request", strings.Repeat(attentionRecord("permission_request", map[string]any{"request_id": "a"}), 2) + attentionRecord("permission_response", map[string]any{"request_id": "a"}), "a", "unknown"},
		{"duplicate-response", attentionRecord("permission_request", map[string]any{"request_id": "a"}) + strings.Repeat(attentionRecord("permission_response", map[string]any{"request_id": "a"}), 2), "a", "unknown"},
		{"missing-reference", attentionRecord("permission_request", map[string]any{"id": "own-id", "call_id": "a"}) + attentionRecord("permission_response", map[string]any{"id": "own-id", "call_id": "a"}), "", "unknown"},
		{"exact-string-id", attentionRecord("permission_request", map[string]any{"request_id": "a\x00b"}) + attentionRecord("permission_response", map[string]any{"request_id": "a\x00b"}), "a\x00b", "closed_observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := fixture(t, tc.body)
			a := attentionOnly(t, s)
			intervention := attentionIntervention(a, "waiting_permission", tc.id)
			if intervention == nil || intervention.Status != tc.status || len(intervention.Evidence) == 0 {
				t.Fatalf("native request semantics guessed: %+v", a)
			}
		})
	}
}

func TestTrajectoryAttentionLifecycleClosureDoesNotCloseOtherTurns(t *testing.T) {
	s, _ := fixture(t, attentionRecord("task_started", map[string]any{"turn_id": "a"})+
		attentionRecord("task_started", map[string]any{"turn_id": "b"})+
		attentionRecord("task_complete", map[string]any{"turn_id": "a"})+
		attentionRecord("turn_aborted", map[string]any{"turn_id": "b"})+
		attentionRecord("task_complete", map[string]any{}))
	a := attentionOnly(t, s)
	if len(a.Progress.Turns) != 3 || a.Progress.Turns[0].Status != "closed_observed" || a.Progress.Turns[1].Status != "aborted_observed" || a.Progress.Turns[2].Status != "unknown" || a.Progress.Status != "partial" || a.Liveness != "unknown" {
		t.Fatalf("missing boundary or concurrency collapsed into task state: %+v", a)
	}
}

func TestTrajectoryAttentionQuestionUsesOriginalTextAndIsOnlyAHint(t *testing.T) {
	textRecord := func(text string) string {
		return codexRecord("response_item", map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
	}
	s, _ := fixture(t, textRecord("Should I continue?"))
	a := attentionOnly(t, s)
	hint := attentionIntervention(a, "question_hint", "")
	if hint == nil || hint.Status != "hint" || attentionIntervention(a, "waiting_input", "") != nil || hint.Evidence[0].NativeField != "payload.content[0].text" {
		t.Fatalf("an ending question became an explicit input wait: %+v", a)
	}
	s, _ = fixture(t, textRecord("Should I continue?<system-reminder>Recorded statement.</system-reminder>"))
	a = attentionOnly(t, s)
	if attentionIntervention(a, "question_hint", "") != nil {
		t.Fatal("display cleaning decided a native ending-question fact")
	}
	s, _ = fixture(t, textRecord("Done.")+attentionRecord("waiting_input", map[string]any{"request_id": "input-a"})+
		attentionRecord("input_response", map[string]any{"request_id": "different"}))
	a = attentionOnly(t, s)
	if input := attentionIntervention(a, "waiting_input", "input-a"); input == nil || input.Status != "open_observed" {
		t.Fatal("explicit input request lost or closed by an unrelated response")
	}
}

func TestTrajectoryAttentionRepeatedErrorsThresholdScopeAndWindow(t *testing.T) {
	base := attentionFailure("a", "exec_command", "turn-a") + attentionFailure("b", "exec_command", "turn-a")
	for _, tc := range []struct {
		name, body string
		candidate  bool
	}{
		{"two", base, false},
		{"three", base + attentionFailure("c", "exec_command", "turn-a"), true},
		{"duplicate-call", base + attentionFailure("b", "exec_command", "turn-a"), false},
		{"new-scope", base + attentionFailure("c", "exec_command", "turn-b"), false},
		{"missing-call", base + attentionFailure("", "exec_command", "turn-a"), false},
		{"missing-turn", base + attentionFailure("c", "exec_command", ""), false},
		{"different-tool", base + attentionFailure("c", "read_file", "turn-a"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := fixture(t, tc.body)
			a := attentionOnly(t, s)
			candidate := attentionIntervention(a, "stuck_candidate", "")
			if (candidate != nil) != tc.candidate {
				t.Fatalf("incorrect repeated-error candidate: %+v", a)
			}
			if candidate != nil && (candidate.Status != "candidate" || candidate.Count != 3 || len(candidate.Evidence) != 3 || candidate.Tool != "exec_command" || a.Liveness != "unknown") {
				t.Fatalf("candidate asserted stuck or lost native evidence: %+v", a)
			}
		})
	}
	three := base + attentionFailure("c", "exec_command", "turn-a")
	for _, count := range []int{17, 18} {
		body := three
		for i := 0; i < count; i++ {
			body += attentionCall(fmt.Sprintf("other-%d", i), "other_tool", "turn-a")
		}
		s, _ := fixture(t, body)
		if candidate := attentionIntervention(attentionOnly(t, s), "stuck_candidate", ""); (candidate != nil) != (count == 17) {
			t.Fatalf("20-action window did not expire old errors at %d actions", count+3)
		}
	}
}

func attentionGrokRecord(kind, id, status, turn string) string {
	update := map[string]any{"sessionUpdate": kind, "toolCallId": id, "_meta": map[string]any{"x.ai/tool": map[string]any{"name": "run_terminal_command"}}}
	if status != "" {
		update["status"] = status
	}
	b, _ := json.Marshal(map[string]any{"timestamp": 1788194984, "method": "_x.ai/session/update", "params": map[string]any{"update": update, "_meta": map[string]any{"promptId": turn}}})
	return string(b) + "\n"
}

func attentionDecoderFixture(t *testing.T, body, agent string, decoder Decoder) *Service {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(func(context.Context) SourceSet {
		return SourceSet{Sources: []Source{{Agent: agent, Path: path, Decoder: decoder}}, Coverage: coverage("attention fixture")}
	})
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestTrajectoryAttentionNativeRecoveryAndConcurrentOutcomeOrder(t *testing.T) {
	body := ""
	// Launch success first, but observe its completion after the three native
	// failures. Recovery follows outcomes, not call launch order.
	body += attentionGrokRecord("tool_call", "recovery", "pending", "p1")
	for _, id := range []string{"a", "b", "c"} {
		body += attentionGrokRecord("tool_call", id, "pending", "p1") + attentionGrokRecord("tool_call_update", id, "failed", "p1")
	}
	s := attentionDecoderFixture(t, body, "grok", GrokDecoder{})
	if attentionIntervention(attentionOnly(t, s), "stuck_candidate", "") == nil {
		t.Fatal("native failures did not produce a candidate")
	}
	s = attentionDecoderFixture(t, body+attentionGrokRecord("tool_call_update", "recovery", "completed", "p1"), "grok", GrokDecoder{})
	a := attentionOnly(t, s)
	if attentionIntervention(a, "stuck_candidate", "") != nil || a.Progress.Status != "unknown" || a.Liveness != "unknown" {
		t.Fatalf("explicit native recovery lost, or tool completion implied task completion: %+v", a)
	}
	s, _ = fixture(t, attentionFailure("a", "exec_command", "t")+attentionFailure("b", "exec_command", "t")+attentionFailure("c", "exec_command", "t")+attentionCall("d", "exec_command", "t")+result("d"))
	if attentionIntervention(attentionOnly(t, s), "stuck_candidate", "") == nil {
		t.Fatal("output prose or Exit code: 0 fabricated explicit recovery")
	}
	s = attentionDecoderFixture(t, body+attentionGrokRecord("tool_call_update", "recovery", "Completed", "p1"), "grok", GrokDecoder{})
	if attentionIntervention(attentionOnly(t, s), "stuck_candidate", "") != nil {
		t.Fatal("recorded native status case handling differs from the decoder")
	}
}

func TestTrajectoryAttentionNativeTurnEndWithoutStartRemainsUnknown(t *testing.T) {
	s := attentionDecoderFixture(t, `{"timestamp":1788194984,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"recorded-turn","stop_reason":"end_turn"}}}`+"\n", "grok", GrokDecoder{})
	a := attentionOnly(t, s)
	if len(a.Progress.Turns) != 1 || a.Progress.Turns[0].TurnID != "recorded-turn" || a.Progress.Turns[0].Status != "unknown" || len(a.Progress.Turns[0].Starts) != 0 || len(a.Progress.Turns[0].Ends) != 1 || a.Liveness != "unknown" {
		t.Fatalf("native turn end disappeared or fabricated a complete lifecycle: %+v", a)
	}
}

func TestTrajectoryAttentionGapsDoNotFabricateOpenOrWorking(t *testing.T) {
	body := attentionRecord("task_started", map[string]any{"turn_id": "t"}) + attentionRecord("permission_request", map[string]any{"request_id": "a"}) +
		attentionFailure("a", "exec_command", "t") + attentionFailure("b", "exec_command", "t") + attentionFailure("c", "exec_command", "t") + `{"type":`
	s, _ := fixture(t, body)
	a := attentionOnly(t, s)
	permission, candidate := attentionIntervention(a, "waiting_permission", "a"), attentionIntervention(a, "stuck_candidate", "")
	if a.Coverage.Complete || a.Progress.Status != "partial" || a.Progress.Turns[0].Status != "unknown" || permission == nil || permission.Status != "unavailable" || candidate == nil || candidate.Status != "unavailable" || a.Liveness != "unknown" {
		t.Fatalf("source gap disguised as live, waiting or stuck certainty: %+v", a)
	}
	s, _ = fixture(t, call("old-unclosed")+result("missing")+request("no lifecycle metadata"))
	a = attentionOnly(t, s)
	if a.Progress.Status != "unknown" || a.Liveness != "unknown" || attentionIntervention(a, "stuck_candidate", "") != nil {
		t.Fatal("unclosed call or log silence produced working/stuck")
	}
	s, _ = fixture(t, attentionFailure("a", "exec_command", "t")+attentionFailure("b", "exec_command", "t")+attentionFailure("c", "exec_command", "t")+attentionFailure("d", "exec_command", ""))
	a = attentionOnly(t, s)
	if candidate := attentionIntervention(a, "stuck_candidate", ""); candidate == nil || candidate.Status != "unavailable" {
		t.Fatalf("newer missing native scope preserved certainty about the older error run: %+v", a)
	}
}

func TestTrajectoryAttentionSelectorsCursorAndEvidenceGet(t *testing.T) {
	first := attentionRecord("permission_request", map[string]any{"request_id": "a"}) + attentionRecord("permission_response", map[string]any{"request_id": "a"}) + attentionRecord("waiting_input", map[string]any{"request_id": "b"})
	s, paths := entityTestService(t, first, attentionRecord("permission_request", map[string]any{"request_id": "c"}))
	q, err := s.QueryAttention(context.Background(), snapshot.TrajectorySelector{Kind: "waiting_permission", State: "open_observed"})
	if err != nil || len(q.Attention) != 1 || attentionIntervention(q.Attention[0], "waiting_permission", "c") == nil {
		t.Fatalf("kind/state predicates matched separate interventions: %+v %v", q, err)
	}
	q, err = s.QueryAttention(context.Background(), snapshot.TrajectorySelector{Limit: 1})
	if err != nil || q.Next == "" || len(q.Attention) != 1 {
		t.Fatalf("bounded attention cursor missing: %+v %v", q, err)
	}
	if _, err := s.QueryAttention(context.Background(), snapshot.TrajectorySelector{Limit: 1, Cursor: q.Next, Kind: "waiting_input"}); !errors.Is(err, ErrStale) {
		t.Fatal("cursor crossed selector scope")
	}
	next, err := s.QueryAttention(context.Background(), snapshot.TrajectorySelector{Limit: 1, Cursor: q.Next})
	if err != nil || len(next.Attention) != 1 || next.Attention[0].ID == q.Attention[0].ID {
		t.Fatal("cursor lost stable session order")
	}
	focus := q.Attention[0].Interventions[0].Evidence[0].EventID
	get, err := s.GetAttention(context.Background(), snapshot.TrajectoryGetParams{ID: focus, View: "attention"})
	if err != nil || get.Attention == nil || get.Attention.ID != q.Attention[0].ID || get.FocusID != focus || get.Revision != q.Revision {
		t.Fatalf("event attention explanation differs from session evidence: %+v %v", get, err)
	}
	if _, err := s.GetAttention(context.Background(), snapshot.TrajectoryGetParams{ID: focus + "bad"}); !errors.Is(err, ErrStale) {
		t.Fatal("get attention did not verify exact source event identity")
	}
	if err := os.WriteFile(paths[0], []byte(request("replacement")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryAttention(context.Background(), snapshot.TrajectorySelector{Limit: 1, Cursor: q.Next}); !errors.Is(err, ErrStale) {
		t.Fatal("attention cursor survived source replacement")
	}
}

func TestTrajectoryAttentionRejectsUnsupportedParameters(t *testing.T) {
	s, _ := fixture(t, request("fixture"))
	for _, q := range []snapshot.TrajectorySelector{
		{Text: "fixture"}, {Skill: "a"}, {Role: "assistant"}, {ActorID: "a"}, {ActorKind: "unknown"}, {EntityKind: "path"}, {EntityID: "ent.123"}, {Predicate: "read"}, {RelationKind: "parent"}, {ContextID: "ctx.a"}, {ContextScope: "actual_input"}, {Kind: "working"}, {State: "working"}, {Collection: "events"},
	} {
		if _, err := s.QueryAttention(context.Background(), q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsupported attention predicate silently ignored: %+v %v", q, err)
		}
	}
	a := attentionOnly(t, s)
	for _, p := range []snapshot.TrajectoryGetParams{
		{Raw: true}, {RawOffset: 1}, {MemberOffset: 1}, {Around: 1}, {View: "raw"}, {MaxBytes: 1023},
	} {
		p.ID = a.ID
		if _, err := s.GetAttention(context.Background(), p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsupported attention reading accepted: %+v %v", p, err)
		}
	}
}

func TestTrajectoryAttentionProjectionAndGetAreBounded(t *testing.T) {
	body := ""
	for i := 0; i < maxAttentionRequests+8; i++ {
		body += attentionRecord("permission_request", map[string]any{"request_id": fmt.Sprintf("permission-%02d", i)})
	}
	s, _ := fixture(t, body)
	a := attentionOnly(t, s)
	if len(a.Interventions) > maxAttentionInterventions || a.Coverage.Complete || a.Coverage.Omitted == 0 {
		t.Fatal("request retention limit hidden")
	}
	get, err := s.GetAttention(context.Background(), snapshot.TrajectoryGetParams{ID: a.ID})
	encoded, _ := json.Marshal(get)
	if err != nil || len(encoded) > MaxSliceBytes || !get.Truncated || get.Coverage.Omitted == 0 {
		t.Fatalf("bounded attention explanation lost truncation: %d %+v %v", len(encoded), get, err)
	}
	if strings.Contains(string(encoded), "synthetic native failure") || strings.Contains(string(encoded), `"raw":`) || strings.Contains(string(encoded), `"arguments":`) {
		t.Fatal("attention projection contains evidence bodies")
	}
}

func TestTrajectoryAttentionBudgetPreservesMatchingIntervention(t *testing.T) {
	body := attentionRecord("waiting_input", map[string]any{"request_id": "retain-input"}) + attentionRecord("input_response", map[string]any{"request_id": "retain-input"})
	for i := 0; i < 15; i++ {
		body += attentionRecord("permission_request", map[string]any{"request_id": fmt.Sprintf("%02d-%s", i, strings.Repeat("x", 250)), "turn_id": strings.Repeat("t", 256)})
	}
	s, _ := fixture(t, body)
	selector := snapshot.TrajectorySelector{Kind: "waiting_input", State: "closed_observed"}
	result, err := s.QueryAttention(context.Background(), selector)
	if err != nil || len(result.Attention) != 1 || !attentionMatches(result.Attention[0], selector) || result.Attention[0].Coverage.Omitted == 0 || result.Coverage.Omitted == 0 {
		t.Fatalf("byte trimming removed the query's actual cause or hid omissions: %+v %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	if len(encoded) > maxAttentionQueryBytes {
		t.Fatal("filtered attention exceeded its result budget")
	}
}
