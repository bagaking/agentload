package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrajectoryTraeNativeIdentityMalformedIDsNeverPair(t *testing.T) {
	for _, shape := range []string{"response_item", "history_mutation"} {
		for _, callType := range []string{"function_call", "custom_tool_call"} {
			for _, malformed := range []struct {
				name  string
				value any
			}{
				{"object", map[string]any{"native": "identical"}},
				{"array", []any{"identical"}},
				{"number", 42},
				{"boolean", true},
				{"null", nil},
			} {
				t.Run(shape+"/"+callType+"/"+malformed.name, func(t *testing.T) {
					record := func(item map[string]any) string {
						payload := any(item)
						if shape == "history_mutation" {
							payload = map[string]any{"version": 1, "operation": "append", "id": malformed.value, "commit_id": malformed.value, "turn_id": malformed.value, "items": []any{item}}
						}
						body, err := json.Marshal(map[string]any{"type": shape, "timestamp": "2026-10-01T00:00:00Z", "payload": payload})
						if err != nil {
							t.Fatal(err)
						}
						return string(body) + "\n"
					}
					call := record(map[string]any{"type": callType, "id": malformed.value, "call_id": malformed.value, "turn_id": malformed.value, "role": malformed.value, "name": malformed.value, "arguments": `{"cmd":"pwd"}`, "input": "pwd"})
					receipt := record(map[string]any{"type": callType + "_output", "id": malformed.value, "call_id": malformed.value, "turn_id": malformed.value, "role": malformed.value, "output": "recorded receipt"})
					path := filepath.Join(t.TempDir(), "session.jsonl")
					if err := os.WriteFile(path, []byte(call+receipt), 0600); err != nil {
						t.Fatal(err)
					}
					s := New(func(context.Context) SourceSet {
						return SourceSet{Sources: []Source{{Agent: "trae", Path: path, Decoder: TraeDecoder{}}}, Coverage: coverage("native identity fixture")}
					})
					t.Cleanup(func() { _ = s.Close() })
					// Identity assertions consume prepared evidence, not an unfinished
					// foreground quantum on a loaded machine. Keep native gaps intact.
					query := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Agent: "trae"})
					if len(query.Events) != 2 || query.Coverage.Complete {
						t.Fatalf("malformed identity coverage: %+v", query)
					}
					for _, projected := range query.Events {
						read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: projected.ID, Around: 0, Raw: true})
						if err != nil || len(read.Events) != 1 {
							t.Fatalf("read malformed native evidence: %+v %v", read, err)
						}
						e := read.Events[0]
						if e.Tool == nil || e.Tool.CallID != "" || e.Tool.Name != "" || e.PairID != "" || e.NativeID != "" || e.NativeEnvelopeID != "" || e.TurnID != "" || e.ProtocolRole != "" {
							t.Fatalf("malformed native field became an identity or pair: %+v", e)
						}
						omissions := strings.Join(e.Omissions, "|")
						if !strings.Contains(omissions, "tool_call_id_missing") || !strings.Contains(omissions, "pair_not_observed") {
							t.Fatalf("missing native reference was not explicit: %+v", e.Omissions)
						}
						if e.Evidence != nil && len(e.Evidence.Relations) != 0 {
							t.Fatalf("malformed reference created relation evidence: %+v", e.Evidence)
						}
						expected := receipt
						if e.Kind == "tool_call" {
							expected = call
						}
						if e.Raw != expected {
							t.Fatalf("strict projection altered raw evidence: %q != %q", e.Raw, expected)
						}
					}
				})
			}
		}
	}
}

func TestTrajectoryTraeNativeIdentityUnknownProtocolRole(t *testing.T) {
	for _, tc := range []struct {
		name     string
		value    any
		recorded string
	}{
		{"unsupported_string", "unsupported_native_role", "unsupported_native_role"},
		{"object", map[string]any{"role": "assistant"}, ""},
		{"array", []any{"assistant"}, ""},
		{"number", 7, ""},
		{"null", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := traeDecodeFixture(t, "response_item", map[string]any{"type": "message", "id": "native-message", "role": tc.value, "content": []any{map[string]any{"type": "input_text", "text": "recorded text"}}})
			if len(events) != 1 || events[0].Role != "unknown" || events[0].ProtocolRole != tc.recorded || events[0].Actor.Kind != "unknown" || events[0].NativeID != "native-message" || events[0].Text != "recorded text" {
				t.Fatalf("unsupported role fabricated protocol or actor semantics: %+v", events)
			}
		})
	}
}

func TestTrajectoryTraeNativeIdentityFallbacksStayRecorded(t *testing.T) {
	malformed := map[string]any{"native": "not a string"}
	events := traeDecodeFixture(t, "history_mutation", map[string]any{"version": 1, "operation": "append", "id": malformed, "turn_id": malformed, "commit_id": malformed, "items": []any{
		map[string]any{"type": "message", "id": malformed, "turn_id": malformed, "role": "user", "internal_chat_message_metadata_passthrough": map[string]any{"turn_id": malformed}, "content": []any{map[string]any{"type": "input_text", "text": "recorded text"}}},
	}})
	if len(events) != 1 || events[0].NativeID != "" || events[0].NativeEnvelopeID != "" || events[0].TurnID != "" {
		t.Fatalf("malformed fallback created native identity: %+v", events)
	}
	unknown := traeDecodeFixture(t, "event_msg", map[string]any{"type": "item_completed", "id": malformed, "turn_id": malformed, "item": map[string]any{"id": malformed}})
	if len(unknown) != 1 || unknown[0].Kind != "unknown" || unknown[0].NativeID != "" || unknown[0].TurnID != "" {
		t.Fatalf("unknown native item created identity: %+v", unknown)
	}
}
