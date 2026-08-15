package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func decodeGrokTest(t *testing.T, raw string) []snapshot.TrajectoryEvent {
	t.Helper()
	events, err := (GrokDecoder{}).Decode([]byte(raw), DecodeContext{SessionID: "local-session"})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestGrokDecoderNativeUpdateShape(t *testing.T) {
	raw := `{"timestamp":1788194984,"method":"_x.ai/session/update","params":{"sessionId":"native-session","update":{"sessionUpdate":"tool_call","toolCallId":"call-a","title":"Friendly title is not the tool name","rawInput":{"command":"pwd"},"_meta":{"x.ai/tool":{"name":"run_terminal_command"}}},"_meta":{"eventId":"event-a","promptId":"prompt-a","updateParams":{"status":"Pending"}}}}`
	events := decodeGrokTest(t, raw)
	if len(events) != 1 {
		t.Fatalf("unexpected events: %+v", events)
	}
	e := events[0]
	if e.Kind != "tool_call" || e.Role != "assistant" || e.Actor.Kind != "unknown" || e.ProtocolRole != "" {
		t.Fatalf("protocol discriminator invented an actor or role: %+v", e)
	}
	if e.NativeID != "event-a" || e.NativeEnvelopeID != "event-a" || e.TurnID != "prompt-a" || e.SessionID != "local-session" {
		t.Fatalf("native IDs or source identity lost: %+v", e)
	}
	if e.Tool == nil || e.Tool.CallID != "call-a" || e.Tool.Name != "run_terminal_command" || string(e.Tool.Arguments) != `{"command":"pwd"}` || e.Outcome != "Pending" {
		t.Fatalf("native action lost: %+v", e)
	}
	if e.Timestamp == nil || e.Timestamp.Unix() != 1788194984 {
		t.Fatalf("epoch seconds not preserved: %+v", e)
	}
}

func TestGrokDecoderIntermediateAndEmptyResults(t *testing.T) {
	for _, tc := range []struct {
		update, wantKind, wantRole, wantText string
	}{
		{`{"sessionUpdate":"tool_call_update","toolCallId":"a","rawInput":{"command":"pwd"},"content":[{"type":"content","content":{"type":"text","text":"still running"}}]}`, "tool_update", "assistant", "still running"},
		{`{"sessionUpdate":"tool_call_update","toolCallId":"a","status":"completed","content":[],"rawOutput":""}`, "tool_result", "tool", ""},
		{`{"sessionUpdate":"tool_call_update","toolCallId":"a","status":"failed","rawOutput":{"type":"Error","message":"failure"}}`, "tool_result", "tool", `{"type":"Error","message":"failure"}`},
	} {
		e := decodeGrokTest(t, `{"timestamp":1788194984,"params":{"update":`+tc.update+`}}`)[0]
		if e.Kind != tc.wantKind || e.Role != tc.wantRole || e.Text != tc.wantText || e.Tool.CallID != "a" {
			t.Fatalf("wrong action state: %+v", e)
		}
		if e.PairID != "" {
			t.Fatal("decoder inferred a pair without source-wide ID evidence")
		}
	}
}

func TestGrokDecoderContentAndUnknownCoverage(t *testing.T) {
	for _, tc := range []struct{ typ, role, kind string }{
		{"user_message_chunk", "user", "text"},
		{"agent_message_chunk", "assistant", "text"},
		{"agent_thought_chunk", "assistant", "reasoning"},
	} {
		e := decodeGrokTest(t, fmt.Sprintf(`{"timestamp":1788194984,"params":{"update":{"sessionUpdate":%q,"content":{"type":"text","text":"visible evidence"}},"_meta":{"eventId":"chunk-1","promptId":"p1"}}}`, tc.typ))[0]
		if e.Role != tc.role || e.Kind != tc.kind || e.Text != "visible evidence" || e.Actor.Kind != "unknown" {
			t.Fatalf("unsupported content mapping: %+v", e)
		}
	}
	unknown := decodeGrokTest(t, `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"future_vendor_update","title":"Do not assume this is a tool"}}}`)[0]
	if unknown.Kind != "unknown" || !grokTestOmission(unknown, "unsupported_grok_update:future_vendor_update") || unknown.Tool != nil {
		t.Fatalf("new native event was guessed: %+v", unknown)
	}
	missing := decodeGrokTest(t, `{"params":{"update":{"sessionUpdate":"tool_call","title":"run_terminal_command"}}}`)[0]
	for _, omission := range []string{"grok_call_id_unavailable", "grok_tool_name_unavailable", "grok_timestamp_unavailable"} {
		if !grokTestOmission(missing, omission) {
			t.Fatalf("missing %s: %+v", omission, missing)
		}
	}
	if missing.NativeID != "" || missing.TurnID != "" || missing.Tool.CallID != "" || missing.Tool.Name != "" {
		t.Fatal("missing identity inferred from a title or source session")
	}
	invalid := decodeGrokTest(t, `{"timestamp":9223372036854775807,"params":{"update":{"sessionUpdate":"tool_call","toolCallId":{"id":"not-a-string"}},"_meta":{"eventId":{},"promptId":1}}}`)[0]
	if invalid.Tool.CallID != "" || invalid.NativeID != "" || invalid.TurnID != "" || invalid.Timestamp != nil {
		t.Fatalf("invalid native field shape became usable identity or timestamp: %+v", invalid)
	}
}

func TestGrokDecoderUsagePreservesMissingZeroAndScope(t *testing.T) {
	events := decodeGrokTest(t, `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{"outputTokens":0,"inputTokens":100,"modelUsage":{"grok-build":{"inputTokens":100,"outputTokens":0}}}}}}`)
	if len(events) != 2 || events[1].Kind != "usage" || events[1].Usage == nil {
		t.Fatalf("recorded usage missing: %+v", events)
	}
	u := events[1].Usage
	if u.Scope != "turn" || u.ScopeID != "p1" || u.Aggregation != "last_nonempty" || u.Counters["input_tokens"] != 100 || u.Models["grok-build"]["input_tokens"] != 100 {
		t.Fatalf("usage scope or model attribution changed: %+v", u)
	}
	if value, ok := u.Counters["output_tokens"]; !ok || value != 0 {
		t.Fatal("recorded zero changed into missing usage")
	}
	if _, ok := u.Counters["total_tokens"]; ok {
		t.Fatal("a missing total was inferred from other counters")
	}
	for _, usage := range []string{"", `,"usage":null`, `,"usage":{}`} {
		events := decodeGrokTest(t, `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"p1"`+usage+`}}}`)
		if len(events) != 1 || events[0].Usage != nil {
			t.Fatalf("missing usage became a zero observation: %+v", events)
		}
	}
	missingID := decodeGrokTest(t, `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"turn_completed","usage":{"outputTokens":10}}}}`)[1]
	if missingID.Usage.ScopeID != "" || !grokTestOmission(missingID, "grok_usage_scope_id_unavailable") {
		t.Fatalf("missing prompt ID was replaced with session or adjacency: %+v", missingID)
	}
}

func TestGrokDecoderUsageBoundsAndInvalidCounters(t *testing.T) {
	models := map[string]any{}
	for i := 0; i < 20; i++ {
		models[fmt.Sprintf("model-%02d", i)] = map[string]any{"outputTokens": i}
	}
	models[strings.Repeat("z", 81)] = map[string]any{"outputTokens": 99}
	raw, _ := json.Marshal(map[string]any{"timestamp": 1788194984, "params": map[string]any{"update": map[string]any{"sessionUpdate": "turn_completed", "prompt_id": "p1", "usage": map[string]any{"inputTokens": nil, "outputTokens": -1, "totalTokens": 3.5, strings.Repeat("a", 5000): 10, "modelUsage": models}}}})
	events, err := (GrokDecoder{}).Decode(raw, DecodeContext{})
	if err != nil || len(events) != 2 {
		t.Fatalf("decode: %+v %v", events, err)
	}
	e := events[1]
	if e.Usage == nil || len(e.Usage.Models) != 16 || len(e.Usage.Counters) != 0 {
		t.Fatalf("invalid counters or oversized model projection: %+v", e)
	}
	if !grokTestOmission(e, "grok_model_usage_limit") || !grokTestOmission(e, "unsupported_grok_usage_counter") || len(e.Omissions) > 16 {
		t.Fatalf("omission coverage missing or unbounded: %+v", e.Omissions)
	}
	for _, omission := range e.Omissions {
		if len(omission) > 160 {
			t.Fatal("untrusted field name escaped into unbounded coverage")
		}
	}
	longName, _ := json.Marshal(map[string]any{"timestamp": 1788194984, "params": map[string]any{"update": map[string]any{"sessionUpdate": "turn_completed", "prompt_id": "p1", "usage": map[string]any{"outputTokens": 1, "modelUsage": map[string]any{strings.Repeat("a", 81): map[string]any{"outputTokens": 1}}}}}})
	bounded, err := (GrokDecoder{}).Decode(longName, DecodeContext{})
	if err != nil || len(bounded[1].Usage.Models) != 0 || !grokTestOmission(bounded[1], "grok_model_name_unavailable") {
		t.Fatalf("oversized model name escaped projection without a gap: %+v %v", bounded, err)
	}
}

func grokTestOmission(e snapshot.TrajectoryEvent, want string) bool {
	for _, omission := range e.Omissions {
		if omission == want {
			return true
		}
	}
	return false
}
