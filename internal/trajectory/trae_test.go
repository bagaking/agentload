package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"strings"
	"testing"
)

func traeDecodeFixture(t *testing.T, typ string, payload any) []snapshot.TrajectoryEvent {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"timestamp": "2026-10-01T00:00:00.123Z", "type": typ, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	events, err := (TraeDecoder{}).Decode(raw, DecodeContext{SessionID: "local-session"})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func TestTrajectoryTraeDecoderHistoryAppend(t *testing.T) {
	events := traeDecodeFixture(t, "history_mutation", map[string]any{
		"version": 1, "operation": "append", "commit_id": "commit-a", "turn_id": "envelope-turn",
		"items": []any{
			map[string]any{"type": "message", "id": "request-a", "role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "first"}, map[string]any{"type": "input_text", "text": "second"},
			}, "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": "item-turn"}},
			map[string]any{"type": "custom_tool_call", "id": "native-action", "name": "apply_patch", "call_id": "call-a", "input": "*** Begin Patch\n*** End Patch"},
			map[string]any{"type": "custom_tool_call_output", "id": "native-receipt", "call_id": "call-a", "output": []any{
				map[string]any{"type": "input_text", "text": "applied"}, map[string]any{"type": "input_text", "text": "checked"},
			}},
		},
	})
	if len(events) != 4 || events[0].Text != "first" || events[1].Text != "second" {
		t.Fatalf("content block order lost: %+v", events)
	}
	for _, e := range events {
		if e.NativeEnvelopeID != "commit-a" || e.Actor.Kind != "unknown" || e.Actor.ID != "" || e.Timestamp == nil || e.SessionID != "local-session" {
			t.Fatalf("envelope evidence or unknown actor changed: %+v", e)
		}
	}
	if events[0].NativeID != "request-a" || events[1].NativeID != "request-a" || events[0].TurnID != "item-turn" || events[0].ProtocolRole != "user" {
		t.Fatalf("request identity lost: %+v", events[:2])
	}
	call, receipt := events[2], events[3]
	var input string
	if json.Unmarshal(call.Tool.Arguments, &input) != nil || input != "*** Begin Patch\n*** End Patch" || call.NativeID != "native-action" || call.Tool.CallID != "call-a" {
		t.Fatalf("custom input or IDs lost: %+v", call)
	}
	if receipt.Kind != "tool_result" || receipt.Text != "applied\nchecked" || receipt.NativeID != "native-receipt" || receipt.Tool.CallID != "call-a" || receipt.TurnID != "envelope-turn" {
		t.Fatalf("receipt blocks became multiple completions: %+v", receipt)
	}
	if call.PairID != "" || receipt.PairID != "" || call.ProtocolRole != "" || receipt.ProtocolRole != "" {
		t.Fatal("decoder invented a relation or protocol role")
	}
}

func TestTrajectoryTraeDecoderNativeShapeDifferences(t *testing.T) {
	for _, emptyOutput := range []any{"", []any{}} {
		events := traeDecodeFixture(t, "response_item", map[string]any{"type": "function_call_output", "id": "receipt", "call_id": "call", "output": emptyOutput})
		if len(events) != 1 || events[0].Text != "" || events[0].Kind != "tool_result" || events[0].NativeID != "receipt" || len(events[0].Omissions) != 0 {
			t.Fatalf("empty native receipt lost: %+v", events)
		}
	}
	call := traeDecodeFixture(t, "response_item", map[string]any{"type": "function_call", "id": "native", "name": "exec_command", "call_id": "call", "arguments": `{"cmd":"pwd"}`})[0]
	if string(call.Tool.Arguments) != `{"cmd":"pwd"}` {
		t.Fatalf("JSON arguments remained encoded: %s", call.Tool.Arguments)
	}
	reasoning := traeDecodeFixture(t, "response_item", map[string]any{"type": "reasoning", "id": "reasoning-a", "summary": []any{}, "content": []any{map[string]any{"type": "reasoning_text", "text": "recorded reasoning"}}})[0]
	if reasoning.Kind != "reasoning" || reasoning.Text != "recorded reasoning" || reasoning.NativeID != "reasoning-a" || reasoning.ProtocolRole != "" {
		t.Fatalf("Trae content was replaced by empty summary: %+v", reasoning)
	}
	developer := traeDecodeFixture(t, "response_item", map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "constraint"}}})[0]
	if developer.Role != "system" || developer.ProtocolRole != "developer" || developer.Actor.Kind != "unknown" {
		t.Fatalf("protocol role was mistaken for actor identity: %+v", developer)
	}
}

func TestTrajectoryTraeDecoderUnknownAndFormatChanges(t *testing.T) {
	for _, tc := range []struct {
		typ     string
		payload map[string]any
		gap     string
	}{
		{"history_mutation", map[string]any{"version": 1, "operation": "replace", "commit_id": "replace-a", "items": []any{map[string]any{"type": "message", "role": "user"}}}, "unsupported_history_operation:replace"},
		{"history_mutation", map[string]any{"version": 2, "operation": "append"}, "unsupported_history_version:2"},
		{"history_mutation", map[string]any{"version": 1, "operation": "append", "items": "changed format"}, "history_items_unavailable"},
		{"inter_agent_communication", map[string]any{"id": "native-communication", "author": "agent-a", "recipient": "agent-b"}, "unsupported_record:inter_agent_communication"},
		{"event_msg", map[string]any{"type": "item_completed", "item": map[string]any{"type": "CommandExecution", "id": "call-a"}}, "unsupported_event_msg:item_completed"},
		{"response_item", map[string]any{"type": "trae_extra_info"}, "unsupported_response_item:trae_extra_info"},
	} {
		events := traeDecodeFixture(t, tc.typ, tc.payload)
		if len(events) != 1 || events[0].Kind != "unknown" || events[0].Actor.Kind != "unknown" || strings.Join(events[0].Omissions, "|") != tc.gap {
			t.Fatalf("format change fabricated a known action: %+v", events)
		}
	}
	blocks := traeDecodeFixture(t, "response_item", map[string]any{"type": "message", "id": "native-message", "role": "user", "content": []any{
		map[string]any{"type": "input_text", "text": "before"}, map[string]any{"type": "input_image", "image_url": "local-image"}, map[string]any{"type": "input_text", "text": "after"},
	}})
	if len(blocks) != 3 || blocks[0].Text != "before" || blocks[1].Kind != "unknown" || blocks[2].Text != "after" || blocks[1].Omissions[0] != "unsupported_content_block:input_image" {
		t.Fatalf("unknown content block erased order or coverage gap: %+v", blocks)
	}
	if _, err := (TraeDecoder{}).Decode([]byte(`{"type":`), DecodeContext{}); err == nil {
		t.Fatal("invalid JSON accepted")
	}
}

func TestTrajectoryTraeDecoderUsageKeepsRecordedScope(t *testing.T) {
	events := traeDecodeFixture(t, "event_msg", map[string]any{"type": "token_count", "info": map[string]any{
		"total_token_usage": map[string]any{"input_tokens": 100, "output_tokens": 0},
		"last_token_usage":  map[string]any{"input_tokens": 20, "output_tokens": 3},
	}})
	u := events[0].Usage
	if u == nil || u.Scope != "session" || u.ScopeID != "local-session" || u.Aggregation != "cumulative" || len(u.Counters) != 2 || u.Counters["input_tokens"] != 100 || u.Counters["output_tokens"] != 0 {
		t.Fatalf("usage scopes mixed or recorded zero lost: %+v", u)
	}
	if _, exists := u.Counters["total_tokens"]; exists {
		t.Fatal("missing counter filled in")
	}
}
