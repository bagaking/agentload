package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"reflect"
	"testing"
)

func TestTrajectoryNativeCandidatePreservesCanonicalWitness(t *testing.T) {
	for _, tc := range []struct {
		agent   string
		decoder Decoder
		body    string
	}{
		{"codex", CodexDecoder{}, codexRecord("session_meta", map[string]any{"id": "native", "cwd": "/recorded"}) + request("unrelated first record") + codexRecord("response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "one", "arguments": `{"cmd":"echo < > & 研究\u2028\u2029", "path":"skills/bagakit-researcher/SKILL.md"}`})},
		{"claude", ClaudeDecoder{}, "{\"type\":\"user\",\"uuid\":\"one\",\"cwd\":\"/recorded\",\"message\":{\"role\":\"user\",\"content\":\"unrelated first record\"}}\n" + "{\"type\":\"assistant\",\"uuid\":\"two\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"one\",\"name\":\"Read\",\"input\":{\"file_path\":\"skills/bagakit-researcher/SKILL.md\"}},{\"type\":\"text\",\"text\":\"研究\"}]}}\n"},
		{"trae", TraeDecoder{}, codexRecord("history_mutation", map[string]any{"version": 1, "operation": "append", "commit_id": "commit", "items": []any{map[string]any{"type": "message", "id": "one", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "unrelated first record"}}}, map[string]any{"type": "message", "id": "two", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "bagakit-researcher 研究"}}}}})},
		{"grok", GrokDecoder{}, "{\"timestamp\":1788194984,\"params\":{\"update\":{\"sessionUpdate\":\"user_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"unrelated first record\"}}}}\n" + "{\"timestamp\":1788194985,\"params\":{\"update\":{\"sessionUpdate\":\"user_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"bagakit-researcher 研究\"}}}}\n"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			s, st := replayFixture(t, tc.agent, tc.decoder, tc.body)
			oracle := canonicalFixtureFacts(t, s, st)
			f := newSourceStoreFixture(t)
			importFixtureFacts(t, f, st, oracle)
			ready, err := f.readiness(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			for _, text := range []string{"bagakit-researcher", "BAGAKIT-RESEARCHER", "u003c", "u2028", "研究", "no-hit"} {
				q := snapshot.TrajectorySelector{Text: text}
				var want, got []snapshot.TrajectoryEvent
				for _, fact := range oracle {
					if matches(fact.event, q) {
						want = append(want, fact.event)
					}
				}
				if text == "bagakit-researcher" && len(want) != 1 || tc.agent == "codex" && (text == "u003c" || text == "u2028") && len(want) != 1 {
					t.Fatal("fixture lacks its canonical witness", text, want)
				}
				p := ready[st.ID]
				err = f.walkSourceCandidateRanges(context.Background(), st, func(filter *sourceFilter) bool { return filter.maybe(q) }, &p, true, &q, func(fact sourceFact) error {
					if matches(fact.event, q) {
						got = append(got, fact.event)
					}
					return nil
				})
				if err != nil || !reflect.DeepEqual(got, want) {
					t.Fatal("candidate changed canonical fields, blocks, workspace, entities or escaped text", text, got, want, err)
				}
			}
		})
	}
}
