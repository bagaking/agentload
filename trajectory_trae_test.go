package main

import (
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func trajectoryTraeRecord(t *testing.T, typ string, payload any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"timestamp": "2026-10-01T00:00:00Z", "type": typ, "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func trajectoryTraeFixture(t *testing.T, body string) (*trajectory.Service, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".trae", "cli")
	path := filepath.Join(root, "sessions", "2026", "10", "01", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	// Reuse registry-owned discovery so the evidence layout and artifacts
	// exclusion tested here are exactly those of the application's provider.
	registry := defaultCodingAgentRegistry(Config{TraeRoots: []string{root}})
	decoder := registry.adapters[registry.byID["trae"]].Capabilities.Trajectory
	if decoder == nil {
		t.Fatal("Trae trajectory capability not registered")
	}
	s := trajectory.New(func(ctx context.Context) trajectory.SourceSet {
		discovered := registry.discoverTranscripts(ctx, time.Time{})
		set := trajectory.SourceSet{Coverage: snapshot.TrajectoryCoverage{Complete: len(discovered.Errors) == 0, Scope: "Trae fixture roots", Gaps: discovered.Errors}}
		for _, discoveredFile := range discovered.Files {
			file := discoveredFile.File
			set.Sources = append(set.Sources, trajectory.Source{Agent: file.Tool, Path: file.Path, NativeID: genericTranscriptSessionID(file.Path), Decoder: decoder})
		}
		return set
	})
	t.Cleanup(func() { _ = s.Close() })
	source := trajectory.Source{Agent: "trae", Path: path, NativeID: genericTranscriptSessionID(path), Decoder: decoder}
	for attempt := 0; attempt < 32; attempt++ {
		more, err := s.PrepareSource(context.Background(), source, func() bool { return true })
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
	return s, path
}

func TestTrajectoryTraeNativeShapesShareQueryAndEvidence(t *testing.T) {
	for _, shape := range []string{"response_item", "history_mutation"} {
		t.Run(shape, func(t *testing.T) {
			wrap := func(item any) string {
				if shape == "history_mutation" {
					return trajectoryTraeRecord(t, shape, map[string]any{"version": 1, "operation": "append", "commit_id": "commit-fixture", "turn_id": "turn-fixture", "items": []any{item}})
				}
				return trajectoryTraeRecord(t, shape, item)
			}
			output := any("HTTP 200")
			if shape == "history_mutation" {
				output = []any{map[string]any{"type": "input_text", "text": "HTTP 200"}}
			}
			request := wrap(map[string]any{"type": "message", "id": "request-fixture", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "<system-reminder>injected constraint</system-reminder>Inspect Proxy"}}})
			call := wrap(map[string]any{"type": "function_call", "id": "native-call", "call_id": "call-fixture", "name": "exec_command", "arguments": `{"cmd":"curl --head https://example.test"}`})
			receipt := wrap(map[string]any{"type": "function_call_output", "id": "native-receipt", "call_id": "call-fixture", "output": output})
			s, _ := trajectoryTraeFixture(t, trajectoryTraeRecord(t, "session_meta", map[string]any{"id": "trae-native-session", "cwd": "workspace/project"})+request+call+receipt)
			q, err := s.Query(context.Background(), snapshot.TrajectorySelector{Text: "Proxy", Agent: "trae"})
			for attempt := 0; err == nil && slices.Contains(q.Coverage.Gaps, "index_pending") && attempt < 32; attempt++ {
				q, err = s.Query(context.Background(), snapshot.TrajectorySelector{Text: "Proxy", Agent: "trae"})
			}
			if err != nil || len(q.Sessions) != 1 || q.Sessions[0].Title != "Inspect Proxy" || q.Sessions[0].NativeID != "trae-native-session" {
				t.Fatalf("shared text query differs for %s: %+v %v", shape, q, err)
			}
			tools, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command", Agent: "trae"})
			if err != nil || len(tools.Events) != 1 {
				t.Fatalf("shared tool query differs: %+v %v", tools, err)
			}
			read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: tools.Events[0].ID, Around: 1, Raw: true, MaxBytes: trajectory.MaxSliceBytes})
			if err != nil || len(read.Events) != 3 {
				t.Fatalf("evidence read: %+v %v", read, err)
			}
			requestEvent, callEvent, receiptEvent := read.Events[0], read.Events[1], read.Events[2]
			if callEvent.ID != tools.Events[0].ID || callEvent.PairID != receiptEvent.ID || receiptEvent.PairID != callEvent.ID || receiptEvent.NativeID != "native-receipt" || receiptEvent.Text != "HTTP 200" {
				t.Fatalf("native receipt pairing lost: %+v", read.Events)
			}
			if requestEvent.Actor.Kind != "unknown" || requestEvent.Actor.ID != "" || requestEvent.ProtocolRole != "user" || requestEvent.NativeID != "request-fixture" || !strings.Contains(requestEvent.Raw, "injected constraint") {
				t.Fatalf("request identity or raw evidence changed: %+v", requestEvent)
			}
			for _, e := range read.Events {
				hash := sha256.Sum256([]byte(e.Raw))
				if e.Source.Line < 2 || e.Source.Length != len(e.Raw) || e.Source.Digest != hex.EncodeToString(hash[:])[:16] {
					t.Fatalf("source locator does not identify raw physical bytes: %+v", e.Source)
				}
				if shape == "history_mutation" && (e.NativeEnvelopeID != "commit-fixture" || e.TurnID != "turn-fixture") {
					t.Fatalf("history envelope identity lost: %+v", e)
				}
			}
		})
	}
}

func TestTrajectoryTraePairingRequiresNativeCallEvidence(t *testing.T) {
	items := []any{
		map[string]any{"type": "function_call", "id": "native-a", "call_id": "a", "name": "exec_command", "arguments": `{"cmd":"one"}`},
		map[string]any{"type": "function_call", "id": "native-b", "call_id": "b", "name": "exec_command", "arguments": `{"cmd":"two"}`},
		map[string]any{"type": "function_call_output", "id": "receipt-b", "call_id": "b", "output": "second completed first"},
		map[string]any{"type": "function_call_output", "id": "receipt-a", "call_id": "a", "output": ""},
		map[string]any{"type": "function_call_output", "id": "native-b", "output": "same item ID is not a call reference"},
		map[string]any{"type": "function_call_output", "id": "orphan", "call_id": "missing", "output": "no observed request"},
	}
	var body strings.Builder
	for _, item := range items {
		body.WriteString(trajectoryTraeRecord(t, "history_mutation", map[string]any{"version": 1, "operation": "append", "commit_id": "commit", "items": []any{item}}))
	}
	s, _ := trajectoryTraeFixture(t, body.String())
	q, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Kind: "tool_result", Agent: "trae"})
	if err != nil || len(q.Events) != 4 {
		t.Fatalf("receipts: %+v %v", q, err)
	}
	for _, receipt := range q.Events {
		read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: receipt.ID, Around: 0})
		if err != nil || len(read.Events) != 1 {
			t.Fatalf("focused receipt: %+v %v", read, err)
		}
		e := read.Events[0]
		if e.Tool.CallID == "a" || e.Tool.CallID == "b" {
			if e.PairID == "" {
				t.Fatalf("out-of-window native call was lost: %+v", e)
			}
			call, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: e.PairID, Around: 0})
			if err != nil || call.Events[0].Kind != "tool_call" || call.Events[0].Tool.CallID != e.Tool.CallID || call.Events[0].PairID != e.ID {
				t.Fatalf("paired by adjacency or name: %+v %v", call, err)
			}
		} else if e.PairID != "" || !strings.Contains(strings.Join(e.Omissions, "|"), "pair_not_observed") {
			t.Fatalf("orphan paired without native call evidence: %+v", e)
		}
	}
}

func TestTrajectoryTraeUnknownCoverageAndArtifactExclusion(t *testing.T) {
	unknown := trajectoryTraeRecord(t, "inter_agent_communication", map[string]any{"id": "communication", "author": "agent-a", "recipient": "agent-b", "content": "recorded communication"})
	replacement := trajectoryTraeRecord(t, "history_mutation", map[string]any{"version": 1, "operation": "replace", "commit_id": "replacement", "items": []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "replayed history"}}}}})
	s, path := trajectoryTraeFixture(t, unknown+replacement)
	artifact := filepath.Join(filepath.Dir(path), "session.artifacts", "nested", "output.jsonl")
	if err := os.MkdirAll(filepath.Dir(artifact), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact, []byte(trajectoryTraeRecord(t, "response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "artifact sentinel"}}})), 0600); err != nil {
		t.Fatal(err)
	}
	q, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Agent: "trae"})
	if err != nil || len(q.Events) != 2 || q.Coverage.Complete {
		t.Fatalf("unsupported records or artifacts lost their boundary: %+v %v", q, err)
	}
	gaps := strings.Join(q.Coverage.Gaps, "|")
	if !strings.Contains(gaps, "unsupported_record:inter_agent_communication") || !strings.Contains(gaps, "unsupported_history_operation:replace") {
		t.Fatalf("missing format coverage gaps: %+v", q.Coverage)
	}
	for _, e := range q.Events {
		if e.Kind != "unknown" || e.Actor.Kind != "unknown" || e.Actor.ID != "" || e.PairID != "" {
			t.Fatalf("unsupported evidence became a known actor or relation: %+v", e)
		}
	}
	raw, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: q.Events[0].ID, Around: 0, Raw: true})
	if err != nil || raw.Events[0].Raw != unknown {
		t.Fatalf("unsupported evidence cannot be inspected: %+v %v", raw, err)
	}
	for _, text := range []string{"replayed history", "artifact sentinel"} {
		filtered, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Text: text, Agent: "trae"})
		if err != nil || len(filtered.Events) != 0 {
			t.Fatalf("unverified projection exposed %q as new content: %+v %v", text, filtered, err)
		}
	}
}
