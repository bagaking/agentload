package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func knowledgeEvents(t *testing.T, s *Service) []snapshot.TrajectoryEvent {
	t.Helper()
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events"})
	if len(q.Events) == 0 {
		t.Fatalf("source events: %+v", q)
	}
	return q.Events
}

func createKnowledge(t *testing.T, s *Service, eventID, kind, text, target string, a *snapshot.TrajectoryKnowledgeApplicability) snapshot.TrajectoryKnowledge {
	t.Helper()
	r, err := s.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: kind, SourceIDs: []string{eventID}, Text: text, TargetID: target, Applicability: a})
	if err != nil || r.Record == nil {
		t.Fatalf("annotation: %+v %v", r, err)
	}
	return *r.Record
}

func TestTrajectoryAnnotationsScopedIndependentRecords(t *testing.T) {
	s, _ := fixture(t, request("source body stays source-only")+call("c1")+result("c1"))
	events := knowledgeEvents(t, s)
	candidate := createKnowledge(t, s, events[0].ID, "candidate", "A locally proposed condition", "", nil)
	if candidate.Kind != "candidate" || candidate.State != "active" || candidate.ScopeStatus != "unprovided" || candidate.Applicability != nil || candidate.Origin != "annotation" {
		t.Fatalf("invented metadata: %+v", candidate)
	}
	encoded, _ := json.Marshal(candidate)
	if strings.Contains(string(encoded), "applicability") || strings.Contains(string(encoded), "source body stays") {
		t.Fatal("missing fields or source body were synthesized")
	}
	a := &snapshot.TrajectoryKnowledgeApplicability{Configuration: map[string]string{"mode": "explicit"}, Environment: map[string]string{"os": "fixture"}, Version: "v1", EvaluationRefs: []string{"https://example.invalid/evaluation"}, ArtifactRefs: []string{"file:///not/read/by/agentload"}}
	verification := createKnowledge(t, s, events[1].ID, "verification", "Observed under supplied scope", candidate.ID, a)
	counter := createKnowledge(t, s, events[2].ID, "counterexample", "Different observed outcome", candidate.ID, &snapshot.TrajectoryKnowledgeApplicability{Version: "v2"})
	if verification.ScopeStatus != "provided" || counter.ScopeStatus != "partial" || verification.Links[0].Kind != "verification_of" || counter.Links[0].Kind != "counterexample_to" || verification.ID == counter.ID {
		t.Fatalf("scope and links: %+v %+v", verification, counter)
	}
	got, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: candidate.ID})
	if err != nil || got.Knowledge.Kind != "candidate" || got.Knowledge.State != "active" || len(got.Knowledge.Links) != 0 {
		t.Fatalf("verification promoted candidate: %+v %v", got, err)
	}
	withdraw, err := s.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "withdraw", ID: candidate.ID})
	if err != nil || withdraw.Record.State != "withdrawn" {
		t.Fatalf("withdraw: %+v %v", withdraw, err)
	}
	again, err := s.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "withdraw", ID: candidate.ID})
	if err != nil || again.Revision != withdraw.Revision {
		t.Fatal("withdraw was not idempotent")
	}
	got, err = s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: verification.ID})
	if err != nil || got.Knowledge.Links[0].Status != "withdrawn" || got.Knowledge.State != "active" {
		t.Fatalf("independent verification: %+v %v", got, err)
	}
	deleted, err := s.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "delete", ID: candidate.ID})
	if err != nil || deleted.DeletedID != candidate.ID {
		t.Fatalf("delete: %+v %v", deleted, err)
	}
	if _, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: candidate.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	got, err = s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: counter.ID})
	if err != nil || got.Knowledge.Links[0].Status != "missing" || got.Coverage.Complete {
		t.Fatalf("deleted target not exposed: %+v %v", got, err)
	}
}

func TestTrajectoryKnowledgeSourceValidityAndWithdrawal(t *testing.T) {
	for _, mode := range []string{"changed", "missing", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session.jsonl")
			if err := os.WriteFile(path, []byte(request("PRIVATE_SOURCE_BODY")), 0600); err != nil {
				t.Fatal(err)
			}
			allowed := true
			s := New(func(context.Context) SourceSet {
				set := SourceSet{Coverage: coverage("fixture")}
				if allowed {
					set.Sources = []Source{{Agent: "codex", Path: path, Decoder: CodexDecoder{}}}
				}
				return set
			})
			t.Cleanup(func() { _ = s.Close() })
			event := knowledgeEvents(t, s)[0]
			note := createKnowledge(t, s, event.ID, "observation", "Authored local observation", "", nil)
			want := "unavailable"
			switch mode {
			case "changed":
				want = "stale"
				if err := os.WriteFile(path, []byte(request("REPLACED_SOURCE")), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing":
				want = "missing"
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "unauthorized":
				allowed = false
			}
			got, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: note.ID})
			if err != nil || got.Knowledge.EvidenceState != want || got.Knowledge.State != "active" || got.Coverage.Complete {
				t.Fatalf("validity and candidate state conflated: %+v %v", got, err)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "PRIVATE_SOURCE_BODY") || strings.Contains(string(encoded), "REPLACED_SOURCE") || len(got.Events) != 0 {
				t.Fatal("stored source body leaked")
			}
			q, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Collection: "knowledge", SessionID: event.SessionID, Agent: "codex"})
			if err != nil || len(q.Knowledge) != 1 || q.Knowledge[0].Sources[0].Status != want {
				t.Fatalf("retained source references: %+v %v", q, err)
			}
			withdraw, err := s.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "withdraw", ID: note.ID})
			if err != nil || withdraw.Record.EvidenceState != want {
				t.Fatalf("write response used stale cached status: %+v %v", withdraw, err)
			}
			if _, err := s.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "candidate", SourceIDs: []string{event.ID}, Text: "old source"}); err == nil {
				t.Fatal("invalid source accepted for a new annotation")
			}
		})
	}
}

func TestTrajectoryKnowledgeAccessPersistenceAndExplicitDelete(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(path, []byte(request("Source")), 0600); err != nil {
		t.Fatal(err)
	}
	enabled := true
	provider := func(context.Context) SourceSet {
		cov := coverage("fixture")
		if !enabled {
			gap(&cov, "content_access_disabled")
			return SourceSet{Coverage: cov}
		}
		return SourceSet{Sources: []Source{{Agent: "codex", Path: path, Decoder: CodexDecoder{}}}, Coverage: cov}
	}
	index := filepath.Join(root, "private", "index.bbolt")
	s := NewPersistent(provider, index)
	t.Cleanup(func() { _ = s.Close() })
	note := createKnowledge(t, s, knowledgeEvents(t, s)[0].ID, "candidate", "Retained authored note", "", nil)
	enabled = false
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(index); !os.IsNotExist(err) {
		t.Fatalf("derived index retained: %v", err)
	}
	annotationPath := filepath.Join(filepath.Dir(index), "annotations.bbolt")
	info, err := os.Stat(annotationPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("private annotation file: %v %v", info, err)
	}
	if _, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: note.ID}); !errors.Is(err, ErrAccess) {
		t.Fatal(err)
	}
	if _, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{}); !errors.Is(err, ErrAccess) {
		t.Fatal(err)
	}
	if _, err := s.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "delete", ID: note.ID}); !errors.Is(err, ErrAccess) {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	enabled = true
	reopened := NewPersistent(provider, index)
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := reopened.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: note.ID})
	if err != nil || got.Knowledge.ID != note.ID || got.Knowledge.Text != note.Text {
		t.Fatalf("authored note lost across toggle/restart: %+v %v", got, err)
	}
	if _, err := reopened.Annotate(context.Background(), snapshot.TrajectoryAnnotationParams{Operation: "delete", ID: note.ID}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	again := NewPersistent(provider, index)
	t.Cleanup(func() { _ = again.Close() })
	if _, err := again.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: note.ID}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestTrajectoryAnnotationsMalformedWritesAndBounds(t *testing.T) {
	s, _ := fixture(t, request("Source"))
	event := knowledgeEvents(t, s)[0]
	valid := snapshot.TrajectoryAnnotationParams{Operation: "create", Kind: "observation", Text: "text", SourceIDs: []string{event.ID}}
	cases := []snapshot.TrajectoryAnnotationParams{
		{}, {Operation: "create", Kind: "proven", Text: "x", SourceIDs: valid.SourceIDs},
		{Operation: "create", Kind: "verification", Text: "x", SourceIDs: valid.SourceIDs},
		{Operation: "create", Kind: "candidate", Text: "x"},
		{Operation: "create", Kind: "candidate", Text: "x", SourceIDs: []string{event.ID, event.ID}},
		{Operation: "create", Kind: "candidate", Text: "x", SourceIDs: []string{"outside-local-source"}},
		{Operation: "create", Kind: "candidate", Text: strings.Repeat("x", 2049), SourceIDs: valid.SourceIDs},
		{Operation: "create", Kind: "candidate", Text: "x", SourceIDs: valid.SourceIDs, Applicability: &snapshot.TrajectoryKnowledgeApplicability{Environment: map[string]string{"": "value"}}},
		{Operation: "delete", ID: "k." + strings.Repeat("a", 32), Text: "mixed operation"},
	}
	for _, p := range cases {
		if _, err := s.Annotate(context.Background(), p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("malformed write accepted: %+v %v", p, err)
		}
	}
	p := valid
	p.Text = strings.Repeat("x", 2048)
	p.Applicability = &snapshot.TrajectoryKnowledgeApplicability{ArtifactRefs: []string{strings.Repeat("y", 512), strings.Repeat("z", 512), strings.Repeat("a", 512), strings.Repeat("b", 512)}}
	if _, err := s.Annotate(context.Background(), p); !errors.Is(err, ErrInvalid) {
		t.Fatalf("whole-record bound bypassed: %v", err)
	}
	q, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{})
	if err != nil || len(q.Knowledge) != 0 {
		t.Fatalf("failed mutation left rows: %+v %v", q, err)
	}
	note := createKnowledge(t, s, event.ID, "observation", strings.Repeat("long note ", 200), "", nil)
	got, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: note.ID, MaxBytes: 1800})
	encoded, _ := json.Marshal(got)
	if err != nil || !got.Truncated || len(encoded) > 1800 || len(got.Knowledge.Sources) != 1 || got.Knowledge.ScopeStatus != "unprovided" {
		t.Fatalf("bounded details: %d %+v %v", len(encoded), got, err)
	}
	// A corrupt annotation version is an error, never a silent destructive rebuild.
	if err := s.annotationDB.Update(func(tx *bolt.Tx) error {
		return putJSON(tx.Bucket(annotationBucket), metaKey, annotationMeta{Version: 99, Count: 1})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{}); err == nil {
		t.Fatal("corrupt authored storage silently accepted")
	}
}

func TestTrajectoryKnowledgeSelectorsPaginationAndEntities(t *testing.T) {
	s, path := fixture(t, request("skill: deploy-skill")+call("c1"))
	events := knowledgeEvents(t, s)
	var tool snapshot.TrajectoryEvent
	for _, e := range events {
		if e.Kind == "tool_call" {
			tool = e
		}
	}
	if tool.ID == "" {
		t.Fatal("tool evidence missing")
	}
	for i := 0; i < 4; i++ {
		createKnowledge(t, s, tool.ID, "candidate", "matching local note", "", nil)
	}
	first, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "knowledge", Text: "matching", State: "active", Kind: "candidate", Tool: "exec_command", Predicate: "called", Agent: "codex", SessionID: tool.SessionID, Limit: 2})
	if err != nil || len(first.Knowledge) != 2 || first.Next == "" {
		t.Fatalf("first page: %+v %v", first, err)
	}
	second, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Collection: "knowledge", Text: "matching", State: "active", Kind: "candidate", Tool: "exec_command", Predicate: "called", Agent: "codex", SessionID: tool.SessionID, Limit: 2, Cursor: first.Next})
	if err != nil || len(second.Knowledge) != 2 || second.Next != "" {
		t.Fatalf("second page: %+v %v", second, err)
	}
	seen := map[string]bool{}
	for _, row := range append(first.Knowledge, second.Knowledge...) {
		if seen[row.ID] {
			t.Fatal("pagination repeated a record")
		}
		seen[row.ID] = true
	}
	for _, q := range []snapshot.TrajectorySelector{{ActorID: "actor"}, {Role: "user"}, {ContextID: "ctx.example"}, {ContextScope: "actual_input"}, {RelationKind: "parent"}, {State: "proven"}, {Kind: "task_complete"}} {
		if _, err := s.QueryKnowledge(context.Background(), q); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unsupported selector silently ignored: %+v %v", q, err)
		}
	}
	if _, err := s.GetKnowledge(context.Background(), snapshot.TrajectoryGetParams{ID: first.Knowledge[0].ID, MemberOffset: 1}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Collection: "knowledge", Text: "different", Cursor: first.Next}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	createKnowledge(t, s, tool.ID, "observation", "new annotation invalidates cursor", "", nil)
	if _, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Collection: "knowledge", Text: "matching", State: "active", Kind: "candidate", Tool: "exec_command", Predicate: "called", Agent: "codex", SessionID: tool.SessionID, Cursor: first.Next}); !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
	q, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Tool: "unrelated"})
	if err != nil || len(q.Knowledge) != 0 {
		t.Fatalf("unrelated entity matched: %+v %v", q, err)
	}
	if err := os.WriteFile(path, []byte(request("changed source")), 0600); err != nil {
		t.Fatal(err)
	}
	q, err = s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Tool: "exec_command"})
	if err != nil || len(q.Knowledge) != 0 {
		t.Fatalf("stale entity fact used to match: %+v %v", q, err)
	}
}

func TestTrajectoryKnowledgeCoverageAndResidencyBounds(t *testing.T) {
	s, _ := fixture(t, strings.Repeat(request("source"), 280)+codexRecord("unknown-native-record", map[string]any{"unprojected": true}))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{})
	// Distinct physical records retain distinct IDs even when their text matches.
	var all []snapshot.TrajectoryEvent
	cursor := ""
	for {
		page, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page.Events...)
		if page.Next == "" {
			break
		}
		cursor = page.Next
	}
	if len(all) != 281 {
		t.Fatalf("native fixture projected %d records", len(all))
	}
	states, allowed, _, err := s.knowledgeCollect(context.Background())
	if err != nil || len(states) != 1 || states[0].checkpoint.Coverage.Complete {
		t.Fatalf("missing decoder coverage: %v %v", states, err)
	}
	cache := map[string]knowledgeSourceCheck{}
	if err := s.annotationDB.View(func(tx *bolt.Tx) error {
		cov := coverage("source facts")
		for _, e := range all {
			record := snapshot.TrajectoryKnowledge{Sources: []snapshot.TrajectoryKnowledgeSource{{EventID: e.ID, SessionID: e.SessionID, Agent: "codex", Source: e.Source}}}
			got, include := s.hydrateKnowledge(context.Background(), tx, states, allowed, record, snapshot.TrajectorySelector{}, cache, nil, &cov)
			if !include || got.EvidenceState != "valid" {
				t.Fatalf("cache saturation lost valid evidence: %+v", got)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(cache) > 256 {
		t.Fatalf("unbounded source-check residency: %d", len(cache))
	}
	refs := []string{strings.Repeat("a", 512), strings.Repeat("b", 512), strings.Repeat("c", 512), strings.Repeat("d", 512)}
	for i := 0; i < 30; i++ {
		createKnowledge(t, s, all[i].ID, "observation", "bounded note", "", &snapshot.TrajectoryKnowledgeApplicability{ArtifactRefs: refs})
	}
	q, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Limit: 50})
	encoded, _ := json.Marshal(q)
	if err != nil || len(encoded) > 64*1024 || q.Next == "" || len(q.Knowledge) == 0 || q.Coverage.Complete {
		t.Fatalf("query bounds/coverage: %d %+v %v", len(encoded), q.Coverage, err)
	}
	for _, r := range q.Knowledge {
		if r.EvidenceState != "valid" {
			t.Fatalf("decoding gap changed independent referenced evidence status: %+v", r)
		}
	}
	more, err := s.QueryKnowledge(context.Background(), snapshot.TrajectorySelector{Limit: 50, Cursor: q.Next})
	if err != nil || len(q.Knowledge)+len(more.Knowledge) != 30 {
		t.Fatalf("byte-bound continuation lost records: %d %d %v", len(q.Knowledge), len(more.Knowledge), err)
	}
}
