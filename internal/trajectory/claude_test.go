package trajectory

import (
	"agentload/internal/snapshot"
	"reflect"
	"strings"
	"testing"
)

func decodeClaudeFixture(t *testing.T, raw string) []snapshot.TrajectoryEvent {
	t.Helper()
	events, err := (ClaudeDecoder{}).Decode([]byte(raw), DecodeContext{SessionID: "local-source-session"})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestTrajectoryClaudeDecoderContentOrderAndNativeIDs(t *testing.T) {
	events := decodeClaudeFixture(t, `{"type":"assistant","uuid":"record-1","parentUuid":"record-parent","sessionId":"parent-session","isSidechain":true,"timestamp":"2026-10-01T02:03:04.123Z","message":{"id":"msg-1","role":"assistant","content":[{"type":"thinking","thinking":"compare options"},{"type":"text","text":"<system-reminder>display-only</system-reminder>Check files"},{"type":"tool_use","id":"call-a","name":"Read","input":{"file_path":"a.go"}},{"type":"image","source":{"type":"base64","data":"synthetic"}},{"type":"tool_use","id":"call-b","name":"Read","input":{"file_path":"b.go"}}]}}`)
	wantKinds := []string{"reasoning", "text", "tool_call", "unknown", "tool_call"}
	if len(events) != len(wantKinds) {
		t.Fatalf("content blocks lost: %+v", events)
	}
	for i, e := range events {
		if e.Kind != wantKinds[i] || e.Role != "assistant" || e.ProtocolRole != "assistant" {
			t.Fatalf("block %d: %+v", i, e)
		}
		if e.NativeID != "msg-1" || e.NativeEnvelopeID != "record-1" || e.SessionID != "local-source-session" {
			t.Fatalf("native/source identity changed: %+v", e)
		}
		if e.Actor.Kind != "unknown" || e.Actor.ID != "" || e.PairID != "" || e.TurnID != "" {
			t.Fatalf("actor or relationship inferred: %+v", e)
		}
		if e.Timestamp == nil || e.Timestamp.Format("2006-01-02T15:04:05.000Z07:00") != "2026-10-01T02:03:04.123Z" {
			t.Fatalf("timestamp lost: %+v", e)
		}
	}
	if events[1].Text != "Check files" || events[2].Tool.CallID != "call-a" || events[4].Tool.CallID != "call-b" {
		t.Fatalf("content or concurrent call identity changed: %+v", events)
	}
	if string(events[2].Tool.Arguments) != `{"file_path":"a.go"}` {
		t.Fatal("tool arguments changed")
	}
	if !reflect.DeepEqual(events[3].Omissions, []string{"unsupported_claude_content_block:image"}) {
		t.Fatal("unsupported block lacks coverage gap")
	}
}

func TestTrajectoryClaudeDecoderEmptyAndStructuredToolResults(t *testing.T) {
	events := decodeClaudeFixture(t, `{"type":"user","uuid":"results-1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"call-b","is_error":true,"content":[{"type":"text","text":"first"},{"type":"image","source":{}},{"type":"text","text":"second"}]},{"type":"tool_result","tool_use_id":"call-a","is_error":false,"content":""},{"type":"tool_result","tool_use_id":"unpaired","content":[]},{"type":"tool_result","content":null}]}}`)
	if len(events) != 4 {
		t.Fatalf("empty result dropped: %+v", events)
	}
	for _, e := range events {
		if e.Kind != "tool_result" || e.Role != "tool" || e.ProtocolRole != "user" || e.Actor.Kind != "unknown" || e.NativeID != "results-1" {
			t.Fatalf("transport user converted to actor: %+v", e)
		}
	}
	if events[0].Text != "first\nsecond" || events[0].Outcome != "error" || events[0].Tool.CallID != "call-b" {
		t.Fatalf("structured result lost: %+v", events[0])
	}
	if !reflect.DeepEqual(events[0].Omissions, []string{"unsupported_claude_tool_result_block:image"}) {
		t.Fatal("nested unsupported content lacks gap")
	}
	if events[1].Text != "" || events[1].Outcome != "not_error" || len(events[1].Omissions) != 0 || events[2].Outcome != "" {
		t.Fatal("empty result or absent error status changed")
	}
	if !reflect.DeepEqual(events[3].Omissions, []string{"no_visible_tool_result_content", "tool_call_id_missing"}) {
		t.Fatalf("missing result fields hidden: %+v", events[3])
	}
}

func TestTrajectoryClaudeDecoderSummaryAndExplicitEvents(t *testing.T) {
	tests := []struct {
		raw, kind, text string
	}{
		{`{"type":"summary","summary":"session title","leafUuid":"leaf"}`, "summary", "session title"},
		{`{"type":"user","uuid":"summary-record","isCompactSummary":true,"message":{"role":"user","content":"prior work summary"}}`, "summary", "prior work summary"},
		{`{"type":"system","subtype":"compact_boundary","content":"Context compacted","compactMetadata":{"preTokens":12,"postTokens":8}}`, "compaction", "Context compacted"},
		{`{"type":"system","subtype":"turn_duration","durationMs":123}`, "turn_duration", ""},
		{`{"type":"system","subtype":"stop_hook_summary","preventedContinuation":true}`, "stop_hook_summary", ""},
		{`{"type":"system","subtype":"local_command","content":"command output"}`, "local_command", "command output"},
		{`{"type":"system","subtype":"informational","content":"notice"}`, "informational", "notice"},
	}
	for _, tc := range tests {
		events := decodeClaudeFixture(t, tc.raw)
		if len(events) != 1 || events[0].Kind != tc.kind || events[0].Text != tc.text || events[0].Actor.Kind != "unknown" {
			t.Fatalf("%s: %+v", tc.raw, events)
		}
	}
	for _, raw := range []string{
		`{"type":"permission-mode","permissionMode":"bypassPermissions"}`,
		`{"type":"system","subtype":"permission_request","toolUseID":"call-a"}`,
		`{"type":"progress","data":{"type":"agent_progress","agentId":"agent-a"}}`,
	} {
		events := decodeClaudeFixture(t, raw)
		if len(events) != 1 || events[0].Kind != "unknown" || len(events[0].Omissions) == 0 {
			t.Fatalf("unsupported record claimed capability: %+v", events)
		}
	}
}

func TestTrajectoryClaudeDecoderUsageKeepsMessageScopeAndRecordedCounters(t *testing.T) {
	events := decodeClaudeFixture(t, `{"type":"assistant","uuid":"usage-record","message":{"id":"msg-usage","role":"assistant","model":"model-a","content":[{"type":"text","text":"done"},{"type":"text","text":"more"}],"usage":{"input_tokens":10,"output_tokens":0,"cache_creation":{"ephemeral_5m_input_tokens":3},"service_tier":"standard","unknown_counter":null}}}`)
	if len(events) != 3 || events[0].Usage != nil || events[1].Usage != nil || events[2].Kind != "usage" {
		t.Fatalf("usage duplicated into content: %+v", events)
	}
	usage := events[2].Usage
	if usage == nil || usage.Scope != "message" || usage.ScopeID != "msg-usage" || usage.Aggregation != "last_nonempty" {
		t.Fatalf("usage scope changed: %+v", usage)
	}
	want := map[string]int64{"input_tokens": 10, "output_tokens": 0, "cache_creation.ephemeral_5m_input_tokens": 3}
	if !reflect.DeepEqual(usage.Counters, want) {
		t.Fatalf("recorded fields replaced or synthesized: %+v", usage.Counters)
	}
	missingID := decodeClaudeFixture(t, `{"type":"assistant","message":{"role":"assistant","content":"partial","usage":{"output_tokens":2}}}`)
	if missingID[1].Usage.ScopeID != "" || !strings.Contains(strings.Join(missingID[1].Omissions, ","), "usage_message_id_missing") {
		t.Fatal("missing message identity guessed")
	}
}

func TestTrajectoryClaudeDecoderMalformedAndUnsupportedContent(t *testing.T) {
	if _, err := (ClaudeDecoder{}).Decode([]byte(`{"type":`), DecodeContext{}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	for _, content := range []string{`null`, `123`, `{}`, `[]`} {
		events := decodeClaudeFixture(t, `{"type":"user","message":{"role":"user","content":`+content+`}}`)
		if len(events) != 1 || !reflect.DeepEqual(events[0].Omissions, []string{"no_visible_content"}) {
			t.Fatalf("unsupported content hidden: %+v", events)
		}
	}
	redacted := decodeClaudeFixture(t, `{"type":"assistant","message":{"role":"assistant","content":[{"type":"redacted_thinking","data":"opaque"}]}}`)
	if len(redacted) != 1 || redacted[0].Kind != "reasoning" || redacted[0].Text != "" || !reflect.DeepEqual(redacted[0].Omissions, []string{"reasoning_redacted"}) {
		t.Fatal("opaque reasoning treated as visible text")
	}
}
