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

func experienceClaudeRecord(uuid, parent string, block map[string]any) string {
	role, typ := "assistant", "assistant"
	if block["type"] == "tool_result" {
		role, typ = "user", "user"
	}
	record := map[string]any{"type": typ, "uuid": uuid, "sessionId": "native-experience-session", "cwd": "/explicit/workspace", "timestamp": "2026-10-01T00:00:00Z", "message": map[string]any{"id": "message-" + uuid, "role": role, "content": []any{block}}}
	if parent != "" {
		record["parentUuid"] = parent
	}
	raw, _ := json.Marshal(record)
	return string(raw) + "\n"
}

func experienceClaudeCall(id, parent, args string) string {
	return experienceClaudeRecord("call-"+id, parent, map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": json.RawMessage(args)})
}

func experienceClaudeResult(id string, outcome *bool) string {
	block := map[string]any{"type": "tool_result", "tool_use_id": id, "content": "Original source result; prose is not an outcome marker"}
	if outcome != nil {
		block["is_error"] = *outcome
	}
	return experienceClaudeRecord("result-"+id, "call-"+id, block)
}

func experienceBool(value bool) *bool { return &value }

func experienceQuery(t *testing.T, s *Service, q snapshot.TrajectorySelector) snapshot.TrajectoryQueryResult {
	t.Helper()
	q.Collection = "knowledge"
	result, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTrajectoryExperienceProxyChangedArgumentsRequireNativeRelation(t *testing.T) {
	first := `{"command":"curl --head https://example.test"}`
	second := `{"command":"HTTPS_PROXY=http://127.0.0.1:8999 curl --head https://example.test"}`
	for _, explicit := range []bool{false, true} {
		t.Run(fmt.Sprint(explicit), func(t *testing.T) {
			parent := ""
			if explicit {
				parent = "result-a"
			}
			body := experienceClaudeCall("a", "", first) + experienceClaudeResult("a", experienceBool(true)) + experienceClaudeCall("b", parent, second) + experienceClaudeResult("b", experienceBool(false))
			s := attentionDecoderFixture(t, body, "claude", ClaudeDecoder{})
			q := experienceQuery(t, s, snapshot.TrajectorySelector{Kind: "candidate"})
			if !explicit {
				if len(q.Knowledge) != 0 {
					t.Fatal("changed arguments were related by adjacency/cwd")
				}
				return
			}
			if len(q.Knowledge) != 1 {
				t.Fatalf("explicit related sequence absent: %+v", q)
			}
			r := q.Knowledge[0]
			if r.RuleID != experienceRuleID || r.RuleVersion != experienceRuleVersion || r.Origin != "deterministic" || r.Kind != "candidate" || r.State != "active" || r.Experience.Pattern != "outcome_change" || r.Experience.Basis != "native_relation" || r.Experience.Causality != "unproven" || r.Experience.TaskCompletion != "unproven" || r.Experience.Verification != "unprovided" {
				t.Fatalf("invented conclusion: %+v", r)
			}
			if r.Applicability != nil || r.ScopeStatus != "unprovided" || r.Experience.Scope.Kind != "source_session" || r.Experience.Scope.ID != r.Sources[0].SessionID {
				t.Fatalf("cwd or role manufactured applicability/turn: %+v", r)
			}
			if len(r.Sources) != 4 || len(r.Experience.Steps) != 2 || r.Experience.Steps[0].Outcome != "error" || r.Experience.Steps[1].Outcome != "not_error" || r.Experience.Relation.NativeField != "/parentUuid" || r.Experience.Relation.TargetEventID != r.Experience.Steps[0].ResultEventID {
				t.Fatalf("evidence binding: %+v", r)
			}
			got, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: r.ID})
			encoded, _ := json.Marshal(got)
			if err != nil || got.Knowledge.ID != r.ID || len(encoded) > MaxSliceBytes || len(got.Events) != 0 || strings.Contains(string(encoded), "8999") || strings.Contains(string(encoded), "Original source result") {
				t.Fatalf("source body copied into candidate: %s %v", encoded, err)
			}
			search := experienceQuery(t, s, snapshot.TrajectorySelector{Text: "HTTPS_PROXY", Tool: "Bash"})
			if len(search.Knowledge) != 1 || search.Knowledge[0].ID != r.ID {
				t.Fatalf("current arguments not searchable: %+v", search)
			}
			for _, step := range r.Experience.Steps {
				raw, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: step.ResultEventID, Raw: true})
				if err != nil || len(raw.Events) != 1 || !strings.Contains(raw.Events[0].Raw, `"is_error":`) || step.OutcomeField != "/message/content/0/is_error" {
					t.Fatalf("outcome is not native source evidence: %+v %v", raw, err)
				}
			}
			again := experienceQuery(t, s, snapshot.TrajectorySelector{Kind: "candidate"})
			if again.Knowledge[0].ID != r.ID || again.Revision != q.Revision {
				t.Fatal("candidate identity/revision changed without source change")
			}
		})
	}
}

func TestTrajectoryExperienceRepeatedErrorsAndBoundContrastingOutcomes(t *testing.T) {
	body := experienceClaudeCall("a", "", `{"command":"inspect","mode":"safe"}`) + experienceClaudeResult("a", experienceBool(true)) +
		experienceClaudeCall("b", "", `{ "mode": "safe", "command": "inspect" }`) + experienceClaudeResult("b", experienceBool(true)) +
		experienceClaudeCall("c", "", `{"command":"inspect","mode":"safe"}`) + experienceClaudeResult("c", experienceBool(false))
	s := attentionDecoderFixture(t, body, "claude", ClaudeDecoder{})
	q := experienceQuery(t, s, snapshot.TrajectorySelector{})
	if len(q.Knowledge) != 2 {
		t.Fatalf("equivalent recorded action was not canonicalized: %+v", q)
	}
	var repeat snapshot.TrajectoryKnowledge
	for _, r := range q.Knowledge {
		if r.Experience.Pattern == "repeated_error" {
			repeat = r
		}
	}
	if repeat.ID == "" || repeat.Experience.Basis != "same_recorded_action" || len(repeat.Experience.Counterexamples) != 1 || repeat.Experience.Counterexamples[0].Outcome != "not_error" || len(repeat.Sources) != 6 || repeat.Applicability != nil {
		t.Fatalf("possible counterevidence unbound: %+v", repeat)
	}
	got, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: repeat.ID})
	if err != nil || got.Knowledge.Experience.Scope.Kind != "source_session" || got.Knowledge.Experience.TaskCompletion != "unproven" || got.Knowledge.Experience.Verification != "unprovided" {
		t.Fatalf("repeat conclusions: %+v %v", got, err)
	}
}

func TestTrajectoryExperienceInsufficientOrUnrelatedEvidenceProducesNothing(t *testing.T) {
	args := `{"command":"inspect"}`
	base := experienceClaudeCall("a", "", args) + experienceClaudeResult("a", experienceBool(true))
	cases := map[string]string{
		"single_action":           base,
		"unrelated_success":       base + experienceClaudeCall("b", "", `{"command":"unrelated"}`) + experienceClaudeResult("b", experienceBool(false)),
		"outcome_prose_only":      experienceClaudeCall("a", "", args) + experienceClaudeResult("a", nil) + experienceClaudeCall("b", "", args) + experienceClaudeResult("b", nil),
		"successes_only":          experienceClaudeCall("a", "", args) + experienceClaudeResult("a", experienceBool(false)) + experienceClaudeCall("b", "", args) + experienceClaudeResult("b", experienceBool(false)),
		"missing_arguments":       experienceClaudeCall("a", "", `null`) + experienceClaudeResult("a", experienceBool(true)) + experienceClaudeCall("b", "", `null`) + experienceClaudeResult("b", experienceBool(true)),
		"duplicate_call_id":       base + experienceClaudeCall("a", "", args) + experienceClaudeResult("a", experienceBool(false)),
		"duplicate_outcome":       base + experienceClaudeResult("a", experienceBool(false)) + experienceClaudeCall("b", "", args) + experienceClaudeResult("b", experienceBool(true)),
		"missing_native_parent":   base + experienceClaudeCall("b", "absent-record", `{"command":"changed"}`) + experienceClaudeResult("b", experienceBool(false)),
		"ambiguous_native_parent": base + experienceClaudeRecord("result-a", "", map[string]any{"type": "text", "text": "duplicate envelope"}) + experienceClaudeCall("b", "result-a", `{"command":"changed"}`) + experienceClaudeResult("b", experienceBool(false)),
		"overlapping_actions":     experienceClaudeCall("a", "", args) + experienceClaudeCall("b", "", args) + experienceClaudeResult("a", experienceBool(true)) + experienceClaudeResult("b", experienceBool(false)),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			s := attentionDecoderFixture(t, body, "claude", ClaudeDecoder{})
			q := experienceQuery(t, s, snapshot.TrajectorySelector{})
			if len(q.Knowledge) != 0 {
				t.Fatalf("unsupported inference: %+v", q.Knowledge)
			}
		})
	}
	s, _ := relationService(t, relationRecord(relationEvent("request", "proxy failure then success")))
	if q := experienceQuery(t, s, snapshot.TrajectorySelector{}); len(q.Knowledge) != 0 {
		t.Fatal("prose created an experience")
	}
}

func TestTrajectoryExperienceLiteralArgumentAndScopeIsolation(t *testing.T) {
	for _, args := range [][2]string{
		{`{"command":"<system-reminder>native</system-reminder>inspect"}`, `{"command":"inspect"}`},
		{`{"n":1}`, `{"n":1.0}`},
		{`{"paths":["a","b"]}`, `{"paths":["b","a"]}`},
		{`{"x":1,"x":2}`, `{"x":2}`},
	} {
		body := experienceClaudeCall("a", "", args[0]) + experienceClaudeResult("a", experienceBool(true)) + experienceClaudeCall("b", "", args[1]) + experienceClaudeResult("b", experienceBool(false))
		s := attentionDecoderFixture(t, body, "claude", ClaudeDecoder{})
		if q := experienceQuery(t, s, snapshot.TrajectorySelector{}); len(q.Knowledge) != 0 {
			t.Fatalf("native argument distinction erased: %v %+v", args, q.Knowledge)
		}
	}
	root := t.TempDir()
	var sources []Source
	for i, outcome := range []bool{true, false} {
		path := filepath.Join(root, fmt.Sprintf("session-%d.jsonl", i))
		if err := os.WriteFile(path, []byte(experienceClaudeCall("same-id", "", `{"command":"same"}`)+experienceClaudeResult("same-id", experienceBool(outcome))), 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, Source{Agent: "claude", Path: path, Decoder: ClaudeDecoder{}})
	}
	s := New(func(context.Context) SourceSet {
		return SourceSet{Sources: sources, Coverage: coverage("isolated physical sources")}
	})
	t.Cleanup(func() { _ = s.Close() })
	if q := experienceQuery(t, s, snapshot.TrajectorySelector{}); len(q.Knowledge) != 0 {
		t.Fatal("matching command/cwd/native session merged physical sessions")
	}
}

func TestTrajectoryExperienceWithdrawalMutationAndExplicitAnnotationLinks(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	body := experienceClaudeCall("a", "", `{"command":"inspect"}`) + experienceClaudeResult("a", experienceBool(true)) + experienceClaudeCall("b", "", `{"command":"inspect"}`) + experienceClaudeResult("b", experienceBool(false))
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	authorized, calls := true, 0
	s := New(func(context.Context) SourceSet {
		calls++
		set := SourceSet{Coverage: coverage("fixture")}
		if authorized {
			set.Sources = []Source{{Agent: "claude", Path: path, Decoder: ClaudeDecoder{}}}
		}
		return set
	})
	t.Cleanup(func() { _ = s.Close() })
	q := experienceQuery(t, s, snapshot.TrajectorySelector{})
	if len(q.Knowledge) != 1 || calls != 1 {
		t.Fatalf("query recollected provider: %d %+v", calls, q)
	}
	r := q.Knowledge[0]
	verification := createKnowledge(t, s, r.Experience.Steps[1].ResultEventID, "verification", "Scoped explicitly authored verification", r.ID, &snapshot.TrajectoryKnowledgeApplicability{Version: "fixture"})
	counter := createKnowledge(t, s, r.Experience.Steps[0].ResultEventID, "counterexample", "An independent local counterexample", r.ID, nil)
	got, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: verification.ID})
	if err != nil || got.Knowledge.Links[0].Status != "active" || got.Knowledge.Applicability.Version != "fixture" {
		t.Fatalf("derived candidate annotation link: %+v %v", got, err)
	}
	got, err = s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: r.ID})
	if err != nil || got.Knowledge.Experience.Verification != "unprovided" || got.Knowledge.Kind != "candidate" {
		t.Fatal("verification promoted derived candidate")
	}
	authorized = false
	q = experienceQuery(t, s, snapshot.TrajectorySelector{})
	if len(q.Knowledge) != 2 {
		t.Fatalf("source withdrawal lost authored notes or retained derived body: %+v", q)
	}
	for _, note := range q.Knowledge {
		if note.Origin != "annotation" || note.EvidenceState != "unavailable" || note.Links[0].Status != "missing" {
			t.Fatalf("withdrawn source semantics: %+v", note)
		}
	}
	if _, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: r.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	authorized = true
	if err := os.WriteFile(path, []byte(strings.ReplaceAll(body, `"is_error":true`, `"is_error":false`)), 0600); err != nil {
		t.Fatal(err)
	}
	q = experienceQuery(t, s, snapshot.TrajectorySelector{Kind: "candidate"})
	if len(q.Knowledge) != 0 {
		t.Fatalf("changed outcomes retained candidate: %+v", q)
	}
	if _, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: r.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got, err = s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: counter.ID})
	if err != nil || got.Knowledge.Links[0].Status != "missing" || got.Knowledge.EvidenceState != "stale" {
		t.Fatalf("source generation mutation erased local validity: %+v %v", got, err)
	}
}

func TestTrajectoryExperiencePaginationVersionAndBounds(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 9; i++ {
		id := fmt.Sprint(i)
		body.WriteString(experienceClaudeCall(id, "", `{"command":"inspect"}`))
		body.WriteString(experienceClaudeResult(id, experienceBool(true)))
	}
	s := attentionDecoderFixture(t, body.String(), "claude", ClaudeDecoder{})
	seen := map[string]bool{}
	cursor := ""
	revision := ""
	for {
		q := experienceQuery(t, s, snapshot.TrajectorySelector{Limit: 2, Cursor: cursor})
		encoded, _ := json.Marshal(q)
		if len(encoded) > maxKnowledgeQueryBytes {
			t.Fatal("knowledge byte bound exceeded")
		}
		if revision != "" && q.Revision != revision {
			t.Fatal("unchanged native evidence changed revision")
		}
		revision = q.Revision
		for _, r := range q.Knowledge {
			if seen[r.ID] {
				t.Fatal("candidate repeated across pages")
			}
			seen[r.ID] = true
			got, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: r.ID})
			raw, _ := json.Marshal(got)
			if err != nil || len(raw) > MaxSliceBytes {
				t.Fatalf("candidate details unbounded: %d %v", len(raw), err)
			}
		}
		if q.Next == "" {
			break
		}
		cursor = q.Next
	}
	if len(seen) != 8 {
		t.Fatalf("lost distinct native attempts: %d", len(seen))
	}
	states, _, _, err := s.knowledgeCollect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var a experienceAction
	q := experienceQuery(t, s, snapshot.TrajectorySelector{Limit: 1})
	step := q.Knowledge[0].Experience.Steps[0]
	a.call, err = s.knowledgeEvent(states[0], step.CallEventID)
	if err != nil {
		t.Fatal(err)
	}
	a.result, err = s.knowledgeEvent(states[0], step.ResultEventID)
	if err != nil {
		t.Fatal(err)
	}
	a.source, a.outcome = states[0], "error"
	b := a
	b.call.ID = "different-versioned-event"
	if experienceRecord(a, b, "same_recorded_action", nil).ID == q.Knowledge[0].ID {
		t.Fatal("candidate identity omitted evidence coordinates")
	}
	large := experienceClaudeCall("a", "", `{"command":"`+strings.Repeat("x", maxExperienceRecordBytes)+`"}`) + experienceClaudeResult("a", experienceBool(true)) + experienceClaudeCall("b", "", `{"command":"`+strings.Repeat("x", maxExperienceRecordBytes)+`"}`) + experienceClaudeResult("b", experienceBool(false))
	bounded := attentionDecoderFixture(t, large, "claude", ClaudeDecoder{})
	result := experienceQuery(t, bounded, snapshot.TrajectorySelector{})
	if len(result.Knowledge) != 0 || result.Coverage.Complete || !strings.Contains(strings.Join(result.Coverage.Gaps, " "), "experience_raw_evidence_limit") {
		t.Fatalf("oversized native evidence silently used: %+v", result)
	}
}

func TestTrajectoryExperienceRecordedTurnAndAbsentTimestampRemainHonest(t *testing.T) {
	makeAction := func(id, kind, outcome, turn string) snapshot.TrajectoryEvent {
		e := relationEvent(id, "")
		e.Timestamp = nil
		e.Kind, e.TurnID, e.Outcome = kind, turn, outcome
		e.Tool = &snapshot.TrajectoryTool{CallID: strings.TrimSuffix(id, "-result")}
		if kind == "tool_call" {
			e.Tool.Name = "exec_command"
			e.Tool.Arguments = json.RawMessage(`{"cmd":"inspect"}`)
		} else {
			e.Attention = &snapshot.TrajectoryAttentionEvidence{OutcomeField: "/events/0/outcome"}
		}
		return e
	}
	body := relationRecord(makeAction("a", "tool_call", "", "native-turn")) + relationRecord(makeAction("a-result", "tool_result", "error", "native-turn")) + relationRecord(makeAction("b", "tool_call", "", "native-turn")) + relationRecord(makeAction("b-result", "tool_result", "not_error", "native-turn"))
	s, _ := relationService(t, body)
	q := experienceQuery(t, s, snapshot.TrajectorySelector{})
	if len(q.Knowledge) != 1 {
		t.Fatalf("recorded-turn sequence missing: %+v", q)
	}
	r := q.Knowledge[0]
	encoded, _ := json.Marshal(r)
	if r.Experience.Scope.Kind != "recorded_turn" || r.Experience.Scope.ID != "native-turn" || r.CreatedAt != nil || r.UpdatedAt != nil || strings.Contains(string(encoded), "created_at") || strings.Contains(string(encoded), "0001-") {
		t.Fatalf("missing native facts replaced by defaults: %s", encoded)
	}
	// Build the contradictory result explicitly; JSON field order is irrelevant.
	contradiction := relationRecord(makeAction("a", "tool_call", "", "native-turn")) + relationRecord(makeAction("a-result", "tool_result", "error", "contradictory-turn")) + relationRecord(makeAction("b", "tool_call", "", "native-turn")) + relationRecord(makeAction("b-result", "tool_result", "not_error", "native-turn"))
	bad, _ := relationService(t, contradiction)
	if result := experienceQuery(t, bad, snapshot.TrajectorySelector{}); len(result.Knowledge) != 0 || result.Coverage.Complete {
		t.Fatalf("conflicting native turn IDs accepted: %+v", result)
	}
}
