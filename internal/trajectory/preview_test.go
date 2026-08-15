package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestTrajectoryQueryNativeMetadataBudget(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 80; i++ {
		b, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "id": strings.Repeat("i", 20000), "role": "user", "content": []any{map[string]any{"type": "input_text", "text": fmt.Sprint("native fixture ", i)}}}})
		body.Write(b)
		body.WriteByte('\n')
	}
	s, _, _, _, _ := indexFixture(t, body.String())
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{})
	q := snapshot.TrajectorySelector{Collection: "events", Limit: 50}
	seen := map[string]bool{}
	for {
		r, err := s.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		if len(b) > maxQueryBytes || len(r.Events) == 0 {
			t.Fatalf("wire budget or empty page: %d", len(b))
		}
		for _, e := range r.Events {
			if seen[e.ID] {
				t.Fatal("repeated page event")
			}
			seen[e.ID] = true
			if e.NativeID != "" || len(e.Omissions) == 0 {
				t.Fatal("native identity was not explicitly omitted")
			}
		}
		if q.Cursor == "" {
			for _, view := range []string{"slice", "raw"} {
				g, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: r.Events[0].ID, View: view})
				if err != nil {
					t.Fatal(err)
				}
				gb, _ := json.Marshal(g)
				if len(gb) > MaxSliceBytes || g.Events[0].Source.Digest == "" {
					t.Fatal("get lost bounded provenance")
				}
				if view == "raw" && (g.RawChunk == nil || g.RawChunk.NextOffset == nil) {
					t.Fatal("raw continuation unavailable")
				}
			}
		}
		if r.Next == "" {
			break
		}
		q.Cursor = r.Next
	}
	if len(seen) != 80 {
		t.Fatalf("skipped results: %d", len(seen))
	}
	r, err := s.Query(context.Background(), snapshot.TrajectorySelector{})
	if err != nil {
		t.Fatal(err)
	}
	if exactMatchCount(r.Sessions[0].MatchedCount) != 80 || len(r.Sessions[0].MatchedIDs) != 50 {
		t.Fatal("matching count confused with bounded references")
	}
}

func TestTrajectoryIndexOversizedCallIDDoesNotHideLaterEvidence(t *testing.T) {
	huge, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "function_call", "call_id": strings.Repeat("c", 40000), "name": "exec_command", "arguments": "{}"}})
	s, _, _, _, _ := indexFixture(t, string(huge)+"\n"+`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"later evidence"}]}}`+"\n")
	r, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Events) != 2 || r.Coverage.Complete {
		t.Fatalf("source aborted or fabricated complete: %+v", r.Coverage)
	}
}

func TestTrajectoryQueryBytePageDoesNotSkipResults(t *testing.T) {
	line, _ := json.Marshal(map[string]any{"type": "response_item", "payload": map[string]any{"type": "message", "id": strings.Repeat("n", 256), "turn_id": strings.Repeat("t", 256), "role": "user", "content": []any{map[string]any{"type": "input_text", "text": strings.Repeat("\"", 480)}}}})
	s, _, _, _, _ := indexFixture(t, strings.Repeat(string(line)+"\n", 80))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{})
	q := snapshot.TrajectorySelector{Collection: "events", Limit: 50}
	seen := map[string]bool{}
	for page := 0; ; page++ {
		r, err := s.Query(context.Background(), q)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		if len(b) > maxQueryBytes {
			t.Fatalf("oversized query: %d", len(b))
		}
		if page == 0 && (len(r.Events) >= 50 || r.Next == "") {
			t.Fatal("fixture did not exercise byte pagination")
		}
		for _, e := range r.Events {
			if seen[e.ID] {
				t.Fatal("duplicate event")
			}
			seen[e.ID] = true
		}
		if r.Next == "" {
			break
		}
		q.Cursor = r.Next
	}
	if len(seen) != 80 {
		t.Fatalf("byte paging skipped results: %d", len(seen))
	}
}

func TestTrajectoryGrokMetadataCallReferenceField(t *testing.T) {
	events, err := (GrokDecoder{}).Decode([]byte(`{"timestamp":1788194985,"method":"_x.ai/session/update","params":{"update":{"sessionUpdate":"tool_call_update","rawOutput":"done"},"_meta":{"eventId":"result","updateParams":{"status":"completed","toolCallId":"call-meta"}}}}`), DecodeContext{SessionID: "fixture"})
	if err != nil || len(events) != 1 || events[0].Evidence == nil || len(events[0].Evidence.Relations) != 1 {
		t.Fatal("missing unique relation")
	}
	if events[0].Evidence.Relations[0].NativeField != "/params/_meta/updateParams/toolCallId" {
		t.Fatal("native evidence field mismatch")
	}
}

func TestTrajectoryMissingProtocolRoleIsUnknown(t *testing.T) {
	for _, role := range []string{"", `,"role":{"nested":"user"}`, `,"role":"vendor-role"`} {
		body := []byte(`{"type":"response_item","payload":{"type":"message","content":[{"type":"input_text","text":"request"}]` + role + `}}`)
		events, err := (CodexDecoder{}).Decode(body, DecodeContext{SessionID: "fixture"})
		if err != nil || len(events) != 1 || events[0].Role != "unknown" || len(events[0].Omissions) == 0 {
			t.Fatalf("missing/malformed role became known: %+v %v", events, err)
		}
	}
}
