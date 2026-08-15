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

func entityTestEvent(id, session string) snapshot.TrajectoryEvent {
	return snapshot.TrajectoryEvent{ID: id, SessionID: session, Role: "assistant", Actor: snapshot.TrajectoryActor{Kind: "unknown"}, Source: snapshot.TrajectorySourceRef{ID: "source-" + session, Generation: "g1", Line: 1, Offset: 0, Length: 100, Digest: "source-digest", NativeType: "fixture"}}
}

func entityTestExtract(e snapshot.TrajectoryEvent, raw string, cwd string) ([]snapshot.TrajectoryEntityOccurrence, []string) {
	return ExtractEntities(e, []byte(raw), EntityContext{Agent: "codex", WorkingDirectory: cwd})
}

func entityTestOccurrence(occurrences []snapshot.TrajectoryEntityOccurrence, kind, predicate, literal string) *snapshot.TrajectoryEntityOccurrence {
	for i := range occurrences {
		o := &occurrences[i]
		if o.Kind == kind && o.Predicate == predicate && o.Literal == literal {
			return o
		}
	}
	return nil
}

func TestTrajectoryEntityPredicatesKeepMentionRequestReadAndLoadSeparate(t *testing.T) {
	path := "/repo/skills/alpha/SKILL.md"
	e := entityTestEvent("command", "s1")
	e.Kind = "tool_call"
	e.Tool = &snapshot.TrajectoryTool{Name: "exec_command", CallID: "command-call", Arguments: json.RawMessage(`{"cmd":"cat /repo/skills/alpha/SKILL.md"}`)}
	command, _ := entityTestExtract(e, "", "/repo")
	if entityTestOccurrence(command, "tool", "called", "exec_command") == nil || entityTestOccurrence(command, "skill", "mention", path) == nil {
		t.Fatalf("recorded call or command mention missing: %+v", command)
	}
	for _, o := range command {
		if o.Kind != "tool" && o.Predicate != "mention" {
			t.Fatalf("shell command was promoted to a read or load: %+v", o)
		}
	}
	e.ID, e.Tool.Name, e.Tool.Arguments = "read-request", "read_file", json.RawMessage(`{"path":"skills/alpha/SKILL.md"}`)
	requested, _ := entityTestExtract(e, "", "/repo")
	request := entityTestOccurrence(requested, "skill", "requested_read", path)
	if request == nil || request.NativeField != "tool.arguments.path" || request.Source != e.Source {
		t.Fatalf("typed read request lacks evidence: %+v", requested)
	}
	for _, o := range requested {
		if o.Predicate == "loaded" || o.Predicate == "read" {
			t.Fatal("a read request proves neither a completed read nor loading")
		}
	}
	e.ID, e.Kind, e.Tool, e.Outcome = "successful-result", "tool_result", &snapshot.TrajectoryTool{CallID: "command-call"}, "success"
	result, _ := entityTestExtract(e, `{"type":"response_item","payload":{"type":"function_call_output","output":"Loaded $alpha successfully"}}`, "/repo")
	for _, o := range result {
		if o.Predicate != "mention" {
			t.Fatalf("success or result prose was promoted to factual usage: %+v", o)
		}
	}
	e.ID, e.Kind, e.Tool = "recorded-read", "unknown", nil
	read, _ := entityTestExtract(e, `{"type":"event_msg","payload":{"type":"file_read","path":"/repo/skills/alpha/SKILL.md"}}`, "/repo")
	recordedRead := entityTestOccurrence(read, "skill", "read", path)
	if recordedRead == nil || recordedRead.EntityID != request.EntityID || recordedRead.NativeField != "payload.path" {
		t.Fatalf("explicit read marker lost: %+v", read)
	}
	e.ID = "recorded-load"
	loaded, _ := entityTestExtract(e, `{"type":"event_msg","payload":{"type":"skill_loaded","skill_path":"/repo/skills/alpha/SKILL.md"}}`, "/repo")
	load := entityTestOccurrence(loaded, "skill", "loaded", path)
	if load == nil || load.EntityID != request.EntityID || load.NativeField != "payload.skill_path" {
		t.Fatalf("explicit load marker lost: %+v", loaded)
	}
	if e.Evidence != nil {
		t.Fatal("reading or loading created model-input membership")
	}
	e.ID, e.Kind, e.Tool = "skill-request", "tool_call", &snapshot.TrajectoryTool{Name: "Skill", Arguments: json.RawMessage(`{"skill":"alpha"}`)}
	requestedLoad, _ := entityTestExtract(e, "", "/repo")
	if entityTestOccurrence(requestedLoad, "skill", "requested_load", "alpha") == nil || entityTestOccurrence(requestedLoad, "skill", "loaded", "alpha") != nil {
		t.Fatalf("skill tool request was conflated with a load: %+v", requestedLoad)
	}
}

func TestTrajectoryEntityPathIdentityAndMissingScopes(t *testing.T) {
	e := entityTestEvent("read-a", "s1")
	e.Kind, e.Tool = "tool_call", &snapshot.TrajectoryTool{Name: "read_file", Arguments: json.RawMessage(`{"path":"skills/alpha/../alpha/SKILL.md"}`)}
	first, _ := entityTestExtract(e, "", "/repo")
	a := entityTestOccurrence(first, "path", "requested_read", "/repo/skills/alpha/SKILL.md")
	e.ID, e.SessionID, e.Tool.Arguments = "read-b", "s2", json.RawMessage(`{"path":"file:///repo/skills/alpha/SKILL.md"}`)
	second, _ := entityTestExtract(e, "", "/unrelated")
	b := entityTestOccurrence(second, "path", "requested_read", "/repo/skills/alpha/SKILL.md")
	if a == nil || b == nil || a.EntityID != b.EntityID || a.ID == b.ID {
		t.Fatalf("normalized full path identity failed across sessions: %+v %+v", first, second)
	}
	e.Tool.Arguments = json.RawMessage(`{"path":"/another/skills/alpha/SKILL.md"}`)
	third, _ := entityTestExtract(e, "", "/repo")
	c := entityTestOccurrence(third, "path", "requested_read", "/another/skills/alpha/SKILL.md")
	if c == nil || a.EntityID == c.EntityID {
		t.Fatal("same basename merged unrelated paths")
	}
	e.Tool.Arguments = json.RawMessage(`{"path":"skills/alpha/SKILL.md"}`)
	unknown1, gaps := entityTestExtract(e, "", "")
	u1 := entityTestOccurrence(unknown1, "path", "requested_read", "skills/alpha/SKILL.md")
	e.ID, e.SessionID = "read-c", "s3"
	unknown2, _ := entityTestExtract(e, "", "")
	u2 := entityTestOccurrence(unknown2, "path", "requested_read", "skills/alpha/SKILL.md")
	if u1 == nil || u2 == nil || u1.EntityID == u2.EntityID || !entityTestGap(gaps, "entity_relative_path_scope_unavailable") {
		t.Fatalf("unknown relative scope was guessed: %+v %+v %v", unknown1, unknown2, gaps)
	}
	e.SessionID, e.Source = "", snapshot.TrajectorySourceRef{}
	unscoped, gaps := ExtractEntities(e, nil, EntityContext{})
	if len(unscoped) != 0 || !entityTestGap(gaps, "entity_scope_unavailable") {
		t.Fatalf("missing namespace became global identity: %+v %v", unscoped, gaps)
	}
	e.ID = ""
	if occurrences, gaps := entityTestExtract(e, "", "/repo"); len(occurrences) != 0 || !entityTestGap(gaps, "entity_event_id_unavailable") {
		t.Fatal("unlocated facts were made navigable occurrences")
	}
}

func TestTrajectoryEntityRawTextCleaningAndBlockCoordinates(t *testing.T) {
	raw := `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<system-reminder>Use $alpha and /repo/one.md</system-reminder>Visible request v1.2.3"},{"type":"input_text","text":"Only $beta in neighboring block"}]}}`
	e := entityTestEvent("block-zero", "s1")
	e.Kind, e.Text = "text", "Visible request v1.2.3"
	occurrences, _ := entityTestExtract(e, raw, "/repo")
	if entityTestOccurrence(occurrences, "skill", "mention", "alpha") == nil || entityTestOccurrence(occurrences, "version", "mention", "v1.2.3") == nil {
		t.Fatalf("display cleaning erased source facts: %+v", occurrences)
	}
	for _, o := range occurrences {
		if o.Kind == "skill" && o.Literal == "beta" || o.NativeField != "payload.content[0].text" {
			t.Fatalf("neighboring block acquired wrong source or actor coordinate: %+v", o)
		}
	}
	e.Source.Block, e.ID = 1, "block-one"
	neighbor, _ := entityTestExtract(e, raw, "/repo")
	if entityTestOccurrence(neighbor, "skill", "mention", "beta") == nil || entityTestOccurrence(neighbor, "skill", "mention", "alpha") != nil {
		t.Fatal("native block selection did not follow the evidence locator")
	}
	e.Source.Block, e.ID = 0, "web-mention"
	web, _ := entityTestExtract(e, `{"text":"https://example.test/docs/one.md"}`, "/repo")
	for _, o := range web {
		if o.Kind == "path" {
			t.Fatalf("a web URL became a local file entity: %+v", o)
		}
	}
	e.ID, e.Kind = "reasoning", "reasoning"
	reasoning, _ := entityTestExtract(e, `{"type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"Discuss $visible"}],"content":[{"type":"text","text":"$unrelated in a different native block"}]}}`, "/repo")
	if entityTestOccurrence(reasoning, "skill", "mention", "visible") == nil || entityTestOccurrence(reasoning, "skill", "mention", "unrelated") != nil {
		t.Fatal("Codex summary event acquired another native content block")
	}
}

func TestTrajectoryQuerySelectorsEntityValidationAndPredicateMatch(t *testing.T) {
	for _, q := range []snapshot.TrajectorySelector{
		{EntityKind: "ontology_guess"}, {Predicate: "included_in_model"}, {Predicate: "success"}, {EntityID: "ent.not-a-valid-id"}, {EntityID: "ent.zzzzzzzzzzzzzzzz"}, {Skill: strings.Repeat("x", 513)},
	} {
		if err := ValidateEntitySelector(q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid structured selector accepted: %+v %v", q, err)
		}
	}
	e := entityTestEvent("a", "s1")
	e.Kind, e.Tool = "tool_call", &snapshot.TrajectoryTool{Name: "Read", Arguments: json.RawMessage(`{"file_path":"/repo/skills/alpha/SKILL.md"}`)}
	e.Entities, _ = entityTestExtract(e, "", "/repo")
	if !MatchEntitySelector(e, snapshot.TrajectorySelector{Skill: "alpha", Predicate: "requested_read"}) || MatchEntitySelector(e, snapshot.TrajectorySelector{Skill: "alpha", Predicate: "loaded"}) {
		t.Fatal("skill query merged read requests and explicit loads")
	}
	e.Entities, e.Text = nil, "$alpha"
	if MatchEntitySelector(e, snapshot.TrajectorySelector{Skill: "alpha"}) {
		t.Fatal("presentation text bypassed the evidence extraction contract")
	}
}

func entityTestService(t *testing.T, bodies ...string) (*Service, []string) {
	t.Helper()
	sources := []Source{}
	paths := []string{}
	for i, body := range bodies {
		path := filepath.Join(t.TempDir(), fmt.Sprintf("session-%d.jsonl", i))
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		sources = append(sources, Source{Agent: "codex", Path: path, NativeID: fmt.Sprintf("native-%d", i), Decoder: CodexDecoder{}})
	}
	s := New(func(context.Context) SourceSet {
		return SourceSet{Sources: sources, Coverage: coverage("entity fixtures")}
	})
	t.Cleanup(func() { _ = s.Close() })
	return s, paths
}

func entityReadRecord(id, path string) string {
	return codexRecord("response_item", map[string]any{"type": "function_call", "name": "read_file", "call_id": id, "arguments": map[string]any{"path": path}})
}

func TestTrajectoryEntitySearchCrossSessionCanonicalPathsAndEvidence(t *testing.T) {
	meta := func(id, cwd string) string { return codexRecord("session_meta", map[string]any{"id": id, "cwd": cwd}) }
	s, _ := entityTestService(t,
		meta("a", "/repo")+entityReadRecord("read-a", "skills/alpha/../alpha/SKILL.md"),
		meta("b", "/repo")+entityReadRecord("read-b", "/repo/skills/alpha/SKILL.md"),
		meta("c", "/other")+entityReadRecord("read-c", "skills/alpha/SKILL.md"),
	)
	ctx := context.Background()
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events"})
	query, err := s.QueryEntities(ctx, snapshot.TrajectorySelector{EntityKind: "path", Predicate: "requested_read"})
	if err != nil || len(query.Entities) != 2 {
		t.Fatalf("cross-session paths query: %+v %v", query, err)
	}
	var shared snapshot.TrajectoryEntity
	for _, entity := range query.Entities {
		if entity.Literal == "/repo/skills/alpha/SKILL.md" {
			shared = entity
		}
	}
	if shared.ID == "" || shared.Count != 2 || len(shared.Occurrences) != 2 || shared.OccurrenceQuery.EntityID != shared.ID {
		t.Fatalf("full-path identity or evidence continuation lost: %+v", shared)
	}
	events := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events", EntityID: shared.ID, Predicate: "requested_read"})
	if len(events.Events) != 2 || events.Events[0].SessionID == events.Events[1].SessionID {
		t.Fatalf("entity did not navigate to cross-session events: %+v %v", events, err)
	}
	for _, event := range events.Events {
		read, err := s.Get(ctx, snapshot.TrajectoryGetParams{ID: event.ID, Raw: true})
		if err != nil || len(read.Events) != 1 || read.Events[0].Raw == "" || read.Events[0].Source.Line != 2 {
			t.Fatalf("entity hit lost original evidence: %+v %v", read, err)
		}
	}
	sessions, err := s.Query(ctx, snapshot.TrajectorySelector{Skill: "alpha", Predicate: "requested_read"})
	if err != nil || len(sessions.Sessions) != 3 {
		t.Fatalf("skill did not find all request sessions without conflating definitions: %+v %v", sessions, err)
	}
}

func TestTrajectoryEntitySearchPaginationBoundsAndSourceLifecycle(t *testing.T) {
	body := codexRecord("session_meta", map[string]any{"id": "a", "cwd": "/repo"})
	for i := 0; i < 12; i++ {
		body += entityReadRecord(fmt.Sprintf("read-%d", i), fmt.Sprintf("/repo/file-%d.md", i))
	}
	s, paths := entityTestService(t, body)
	ctx := context.Background()
	selector := snapshot.TrajectorySelector{Collection: "entities", EntityKind: "path", Predicate: "requested_read", Limit: 3}
	first, err := s.QueryEntities(ctx, selector)
	if err != nil || len(first.Entities) != 3 || first.Next == "" {
		t.Fatalf("bounded entities page missing: %+v %v", first, err)
	}
	selector.Cursor = first.Next
	second, err := s.QueryEntities(ctx, selector)
	if err != nil || len(second.Entities) != 3 || second.Entities[0].ID == first.Entities[0].ID {
		t.Fatalf("entity cursor repeated first page: %+v %v", second, err)
	}
	selector.Limit = 1
	if smaller, err := s.QueryEntities(ctx, selector); err != nil || len(smaller.Entities) != 1 || smaller.Entities[0].ID != second.Entities[0].ID {
		t.Fatal("changing the page size invalidated an otherwise identical entity cursor")
	}
	selector.Predicate = "mention"
	if _, err := s.QueryEntities(ctx, selector); !errors.Is(err, ErrStale) {
		t.Fatal("cursor crossed a different selector")
	}
	if err := os.WriteFile(paths[0], []byte(codexRecord("session_meta", map[string]any{"id": "replacement", "cwd": "/new"})+entityReadRecord("new-read", "/new/file.md")), 0600); err != nil {
		t.Fatal(err)
	}
	selector.Predicate = "requested_read"
	if _, err := s.QueryEntities(ctx, selector); !errors.Is(err, ErrStale) {
		t.Fatal("source replacement did not invalidate entity cursor")
	}
	if _, err := s.GetEntity(ctx, snapshot.TrajectoryGetParams{ID: first.Entities[0].ID}); !errors.Is(err, ErrNotFound) {
		t.Fatal("entity get leaked removed source occurrences")
	}
	if _, err := s.QueryEntities(ctx, snapshot.TrajectorySelector{Limit: 51}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unbounded entity result accepted")
	}
}

func TestTrajectoryEntitySearchCoverageStaysSeparateFromNativeEvidence(t *testing.T) {
	s, _ := entityTestService(t, entityReadRecord("read-a", "relative/SKILL.md"))
	ctx := context.Background()
	native, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Tool: "read_file"})
	if err != nil || len(native.Events) != 1 || !native.Coverage.Complete {
		t.Fatalf("unknown entity scope erased complete native action evidence: %+v %v", native, err)
	}
	entities, err := s.QueryEntities(ctx, snapshot.TrajectorySelector{EntityKind: "path", Predicate: "requested_read"})
	if err != nil || len(entities.Entities) != 1 || entities.Coverage.Complete || !entityTestGap(entities.Coverage.Gaps, "entity_relative_path_scope_unavailable") {
		t.Fatalf("entity query lost its own scope coverage: %+v %v", entities, err)
	}
	filtered, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Skill: "relative", Predicate: "requested_read"})
	if err != nil || len(filtered.Events) != 1 || filtered.Coverage.Complete {
		t.Fatalf("event entity selector hid the missing scope: %+v %v", filtered, err)
	}
}

func TestTrajectoryEntitySearchNamedSkillsAndMentionNegative(t *testing.T) {
	s, _ := entityTestService(t, request("Discuss $alpha v1.2.3"), request("Discuss $alpha v1.2.3")+codexRecord("response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "shell", "arguments": map[string]any{"cmd": "cat /repo/skills/alpha/SKILL.md"}}))
	ctx := context.Background()
	names, err := s.QueryEntities(ctx, snapshot.TrajectorySelector{EntityKind: "skill", Skill: "alpha", Predicate: "mention"})
	if err != nil || len(names.Entities) != 3 {
		t.Fatalf("unscoped names or path-backed mentions conflated: %+v %v", names, err)
	}
	loaded, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Skill: "alpha", Predicate: "loaded"})
	if err != nil || len(loaded.Events) != 0 {
		t.Fatalf("mention or shell request falsely proved load: %+v %v", loaded, err)
	}
	versions, err := s.QueryEntities(ctx, snapshot.TrajectorySelector{EntityKind: "version", Text: "v1.2.3"})
	if err != nil || len(versions.Entities) != 2 || versions.Entities[0].ID == versions.Entities[1].ID {
		t.Fatalf("version label guessed shared software identity: %+v %v", versions, err)
	}
}

func TestTrajectoryEntitySearchGetBoundedExamples(t *testing.T) {
	body := ""
	for i := 0; i < 20; i++ {
		body += entityReadRecord(fmt.Sprintf("read-%d", i), "/repo/one.md")
	}
	s, _ := entityTestService(t, body)
	q, err := s.QueryEntities(context.Background(), snapshot.TrajectorySelector{EntityKind: "path", Predicate: "requested_read"})
	if err != nil || len(q.Entities) != 1 || q.Entities[0].Count != 20 || len(q.Entities[0].Occurrences) != maxEntityExamples || q.Entities[0].Coverage.Complete {
		t.Fatalf("entity examples or coverage unbounded: %+v %v", q, err)
	}
	read, err := s.GetEntity(context.Background(), snapshot.TrajectoryGetParams{ID: q.Entities[0].ID, MaxBytes: 2048})
	encoded, _ := json.Marshal(read)
	if err != nil || read.Entity == nil || len(encoded) > 2048 || !read.Truncated || read.Coverage.Complete || read.Entity.OccurrenceQuery.EntityID != q.Entities[0].ID {
		t.Fatalf("bounded entity get or event-query continuation lost: %s %v", encoded, err)
	}
}

func TestTrajectoryEntityExtractionBounds(t *testing.T) {
	e := entityTestEvent("many", "s1")
	e.Kind, e.Tool = "tool_call", &snapshot.TrajectoryTool{Name: "read_file", Arguments: json.RawMessage(`{"path":"` + strings.Repeat("a", 513) + `"}`)}
	_, gaps := entityTestExtract(e, "", "/repo")
	if !entityTestGap(gaps, "entity_literal_size_limit") {
		t.Fatal("oversized literal escaped without coverage")
	}
	e.Tool, e.Kind = nil, "text"
	raw, _ := json.Marshal(map[string]string{"text": strings.Repeat(" $alpha /repo/one.md v1.2.3", 1000)})
	occurrences, gaps := ExtractEntities(e, raw, EntityContext{Agent: "codex"})
	if len(occurrences) > maxEntityOccurrences || !entityTestGap(gaps, "entity_text_scan_limit") {
		t.Fatalf("text extraction not bounded: %d %v", len(occurrences), gaps)
	}
}

func entityTestGap(gaps []string, want string) bool {
	for _, gap := range gaps {
		if gap == want {
			return true
		}
	}
	return false
}
