package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func contextInclude(id string) snapshot.TrajectoryRelationEvidence {
	return snapshot.TrajectoryRelationEvidence{Kind: "input_inclusion", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: id}, NativeField: "/included_message_ids"}
}

func contextInput(id string, complete bool, relations ...snapshot.TrajectoryRelationEvidence) snapshot.TrajectoryEvent {
	e := relationEvent(id, "recorded input boundary", relations...)
	e.Kind = "context"
	e.Context = &snapshot.TrajectoryContextEvidence{NativeID: id, Revision: "native-revision-1", NativeField: "/context_id"}
	if complete {
		e.Context.MembershipComplete = true
		e.Context.CompletenessNativeField = "/input_membership_complete"
	}
	return e
}

func TestTrajectoryContextScopesDoNotInferInputFromArchiveDeliveryOrWorkspace(t *testing.T) {
	request := relationEvent("request", "read this file and remember it")
	sent := relationEvent("sent", "message sent and delivered", snapshot.TrajectoryRelationEvidence{Kind: "delivered", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: "request"}, NativeField: "/delivered_id"})
	read := relationEvent("read", "file contents")
	read.Kind = "tool_result"
	boundary := contextInput("turn", false)
	s, _ := relationService(t, relationRecord(request)+relationRecord(sent)+relationRecord(read)+relationRecord(boundary))
	ctx := context.Background()
	inputs, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input"})
	if err != nil || len(inputs.Contexts) != 1 || inputs.Contexts[0].Membership != "unknown" || inputs.Contexts[0].KnownMembers != 0 || inputs.Coverage.Complete {
		t.Fatalf("unknown actual input: %+v %v", inputs, err)
	}
	manifest, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: inputs.Contexts[0].ID})
	if err != nil || len(manifest.Members) != 0 || manifest.Coverage.Complete || manifest.Context.NativeRevision != "native-revision-1" {
		t.Fatalf("archive/delivery/read inferred input: %+v %v", manifest, err)
	}
	selected, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", ContextID: inputs.Contexts[0].ID})
	if err != nil || len(selected.Events) != 0 || selected.Coverage.Complete || selected.WatchCursor != "" {
		t.Fatalf("unknown membership event selection: %+v %v", selected, err)
	}
	archive, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{})
	if err != nil || len(archive.Contexts) != 1 || archive.Contexts[0].Scope != "archive" || archive.Contexts[0].KnownMembers != 4 || archive.Contexts[0].Membership != "recorded" {
		t.Fatalf("recorded archive: %+v %v", archive, err)
	}
	archived, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: archive.Contexts[0].ID})
	if err != nil || len(archived.Members) != 4 || archived.Members[0].Visibility != "archive_record" {
		t.Fatalf("archive manifest: %+v %v", archived, err)
	}
	workspace, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "workspace", Text: "file contents"})
	if err != nil || len(workspace.Contexts) != 1 || workspace.Contexts[0].Workspace.Path != "/shared/workspace" {
		t.Fatalf("workspace descriptor: %+v %v", workspace, err)
	}
	material, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: workspace.Contexts[0].ID})
	if err != nil || len(material.Members) != 1 || material.Members[0].Visibility != "workspace_reference" || material.Members[0].NativeField != "/cwd" {
		t.Fatalf("workspace manifest: %+v %v", material, err)
	}
	for _, id := range []string{archive.Contexts[0].ID, workspace.Contexts[0].ID} {
		if _, _, err := s.ContextMembers(ctx, id); !errors.Is(err, ErrInvalid) {
			t.Fatal("non-input scope supplied model input selection")
		}
	}
}

func TestTrajectoryContextExplicitMembershipAndBranchRevisionIdentity(t *testing.T) {
	request := relationEvent("request", "included exact request")
	secondBlock := request
	secondBlock.Text = "second native content block"
	unrelated := relationEvent("unrelated", "same workspace and transport")
	boundary := contextInput("native-context", false, contextInclude("request"))
	boundary.Evidence.Branch = &snapshot.TrajectoryBranchEvidence{ID: "branch-a", NativeField: "/branch_id"}
	complete := boundary
	complete.Context = &snapshot.TrajectoryContextEvidence{NativeID: "native-context", Revision: "native-revision-2", NativeField: "/context_id", MembershipComplete: true, CompletenessNativeField: "/input_membership_complete"}
	complete.Evidence = &snapshot.TrajectoryEvidence{Relations: []snapshot.TrajectoryRelationEvidence{contextInclude("request")}, Branch: &snapshot.TrajectoryBranchEvidence{ID: "branch-b", NativeField: "/branch_id"}}
	s, _ := relationService(t, relationRecord(request, secondBlock)+relationRecord(unrelated)+relationRecord(boundary)+relationRecord(complete))
	ctx := context.Background()
	inputs, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input"})
	if err != nil || len(inputs.Contexts) != 2 || inputs.Contexts[0].ID == inputs.Contexts[1].ID || inputs.Contexts[0].Membership != "partial" || inputs.Contexts[1].Membership != "complete" {
		t.Fatalf("recorded branch revisions: %+v %v", inputs, err)
	}
	if inputs.Contexts[0].Branch.ID != "branch-a" || inputs.Contexts[1].Branch.ID != "branch-b" || inputs.Contexts[0].NativeID != inputs.Contexts[1].NativeID || inputs.Contexts[0].NativeRevision == inputs.Contexts[1].NativeRevision {
		t.Fatal("duplicate native context ID collapsed branch/revision")
	}
	for _, c := range inputs.Contexts {
		manifest, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: c.ID})
		if err != nil || len(manifest.Members) != 1 || manifest.Members[0].Status != "resolved" || len(manifest.Members[0].EventIDs) != 2 || manifest.Members[0].Source.Line != c.Source.Line || manifest.Members[0].NativeField != "/included_message_ids" || manifest.Members[0].Visibility != "input_included" {
			t.Fatalf("explicit membership: %+v %v", manifest, err)
		}
		ids, cov, err := s.ContextMembers(ctx, c.ID)
		if err != nil || len(ids) != 2 || ids[0] != manifest.Members[0].EventIDs[0] || cov.Complete != (c.Membership == "complete") {
			t.Fatalf("canonical input member selection: %+v %+v %v", ids, cov, err)
		}
		selected, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", ContextID: c.ID, ContextScope: "actual_input", Text: "exact request"})
		if err != nil || len(selected.Events) != 1 || selected.Events[0].ID != ids[0] || selected.Events[0].Source.Line != 1 {
			t.Fatalf("context and event selectors intersect: %+v %v", selected, err)
		}
	}
}

func TestTrajectoryContextMissingAmbiguousAndPartialSourceReferencesStayUnresolved(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, target, suffix, status string
	}{
		{"missing", "absent", relationRecord(relationEvent("other", "")), "missing"},
		{"duplicate", "duplicate", relationRecord(relationEvent("duplicate", "first")) + relationRecord(relationEvent("duplicate", "second")), "ambiguous"},
		{"incomplete", "candidate", relationRecord(relationEvent("candidate", "")) + `{"events":`, "coverage_incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := relationService(t, relationRecord(contextInput("context", true, contextInclude(tc.target)))+tc.suffix)
			q, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input"})
			if err != nil || len(q.Contexts) != 1 {
				t.Fatalf("descriptor: %+v %v", q, err)
			}
			got, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: q.Contexts[0].ID})
			if err != nil || len(got.Members) != 1 || got.Members[0].Status != tc.status || len(got.Members[0].EventIDs) != 0 || got.Coverage.Complete || got.Context.Membership != "partial" {
				t.Fatalf("reference resolution: %+v %v", got, err)
			}
			ids, _, err := s.ContextMembers(ctx, q.Contexts[0].ID)
			if err != nil || len(ids) != 0 {
				t.Fatal("unresolved native reference selected event IDs")
			}
		})
	}
	boundary := contextInput("context", true, contextInclude("request"))
	boundary.Evidence.Relations[0].NativeField = ""
	s, _ := relationService(t, relationRecord(boundary)+relationRecord(relationEvent("request", "")))
	q, _ := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input"})
	got, _ := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: q.Contexts[0].ID})
	if got.Members[0].Status != "missing_evidence" || len(got.Members[0].EventIDs) > 0 {
		t.Fatal("inclusion declaration without native evidence accepted")
	}

	s, _ = relationService(t, relationRecord(contextInput("context", true, contextInclude("request")))+relationRecord(relationEvent("request", "")))
	provider := s.provider
	s.provider = func(ctx context.Context) SourceSet {
		set := provider(ctx)
		gap(&set.Coverage, "fixture_discovery_incomplete")
		return set
	}
	q, _ = s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input"})
	got, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: q.Contexts[0].ID})
	if err != nil || got.Members[0].Status != "coverage_incomplete" || len(got.Members[0].EventIDs) != 0 {
		t.Fatalf("discovery incompleteness lost: %+v %v", got, err)
	}
	selected, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", ContextID: q.Contexts[0].ID})
	if err != nil || len(selected.Events) != 0 || selected.Coverage.Complete {
		t.Fatal("partial snapshot declared unique member")
	}
}

func TestTrajectoryContextArchiveTransformationsAndStaleSourceGeneration(t *testing.T) {
	summary := relationEvent("summary", "recorded summary")
	summary.Kind = "summary"
	compaction := relationEvent("compaction", "recorded compaction")
	compaction.Kind = "compaction"
	s, paths := relationService(t, relationRecord(relationEvent("before", "original"))+relationRecord(summary)+relationRecord(relationEvent("middle", "after summary"))+relationRecord(compaction)+relationRecord(relationEvent("after", "after compaction")))
	ctx := context.Background()
	q, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "archive"})
	if err != nil || len(q.Contexts) != 3 || q.Contexts[0].KnownMembers != 1 || q.Contexts[1].KnownMembers != 2 || q.Contexts[2].KnownMembers != 2 || q.Contexts[0].AfterID != q.Contexts[1].ID || q.Contexts[1].BeforeID != q.Contexts[0].ID || q.Contexts[1].AfterID != q.Contexts[2].ID {
		t.Fatalf("source revision navigation: %+v %v", q, err)
	}
	for i, kind := range []string{"summary", "compaction"} {
		c := q.Contexts[i+1]
		if c.Transformation == nil || c.Transformation.Kind != kind || c.Transformation.OriginalsStatus != "unknown" || c.Coverage.Complete || c.SourceRevision == "" {
			t.Fatal("transformation recovered originals or lost source revision")
		}
		m, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: c.ID})
		if err != nil || len(m.Members) != 2 || m.Members[0].EventIDs[0] != c.Transformation.EventID || m.Context.BeforeID != c.BeforeID {
			t.Fatalf("transform manifest: %+v %v", m, err)
		}
	}
	inputs, _ := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input"})
	if len(inputs.Contexts) != 0 {
		t.Fatal("summary/compaction asserted model input context")
	}
	if err := os.WriteFile(paths[0]+".replacement", []byte(relationRecord(relationEvent("new", "replacement"))), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(paths[0]+".replacement", paths[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: q.Contexts[1].ID}); !errors.Is(err, ErrStale) {
		t.Fatalf("old context rebound to replacement source: %v", err)
	}
	if _, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextID: q.Contexts[1].ID}); !errors.Is(err, ErrStale) {
		t.Fatalf("query silently dropped stale context: %v", err)
	}
}

func TestTrajectoryContextBoundedManifestPaginationAndLongHistoryFocus(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 5002; i++ {
		body.WriteString(relationRecord(relationEvent(fmt.Sprintf("event-%d", i), fmt.Sprintf("entry-%d", i))))
	}
	s, _ := relationService(t, body.String())
	ctx := context.Background()
	// Manifest pagination consumes a prepared archive. Cold preparation is
	// bounded and explicitly pending, regardless of host scheduling speed.
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Limit: 1})
	archive, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{})
	if err != nil || len(archive.Contexts) != 1 || archive.Contexts[0].KnownMembers != 5002 {
		t.Fatalf("large archive: %+v %v", archive, err)
	}
	first, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: archive.Contexts[0].ID, MemberOffset: 4990})
	if err != nil || len(first.Members) == 0 || first.Members[0].Source.Line != 4991 || first.NextOffset == nil || *first.NextOffset <= 4990 || !first.Truncated {
		t.Fatalf("bounded manifest continuation: %+v %v", first, err)
	}
	b, _ := json.Marshal(first)
	if len(b) > MaxSliceBytes {
		t.Fatal("manifest exceeds 5 KiB wire budget")
	}
	next, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: archive.Contexts[0].ID, MemberOffset: *first.NextOffset})
	if err != nil || len(next.Members) == 0 || next.Members[0].Source.Line != first.Members[len(first.Members)-1].Source.Line+1 || next.Members[0].ID == first.Members[len(first.Members)-1].ID {
		t.Fatalf("manifest skipped/repeated continuation: %+v %v", next, err)
	}
	focus, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "query_window", Text: "entry-5001"})
	if err != nil || len(focus.Contexts) != 1 {
		t.Fatalf("late context focus: %+v %v", focus, err)
	}
	window, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: focus.Contexts[0].ID, Around: 2})
	if err != nil || len(window.Members) != 3 || window.Members[2].Source.Line != 5002 || window.Members[2].EventIDs[0] != focus.Contexts[0].AnchorID || window.Members[0].Visibility != "retrieval_window" || window.Before == "" {
		t.Fatalf("late indexed reading window: %+v %v", window, err)
	}
	if _, _, err := s.ContextMembers(ctx, focus.Contexts[0].ID); !errors.Is(err, ErrInvalid) {
		t.Fatal("reading window supplied model input membership")
	}
}

func TestTrajectoryContextCatalogBudgetSelectorsAndNativeMetadataBounds(t *testing.T) {
	var body strings.Builder
	for i := 0; i < 50; i++ {
		e := contextInput(fmt.Sprintf("context-%d", i), false)
		e.Workspace.Path = strings.Repeat("x", 1000)
		e.Context.NativeID, e.Context.Revision = strings.Repeat("n", 250), strings.Repeat("r", 250)
		e.Evidence.Branch = &snapshot.TrajectoryBranchEvidence{ID: strings.Repeat("b", 250), NativeField: "/branch_id"}
		body.WriteString(relationRecord(e))
	}
	s, _ := relationService(t, body.String())
	ctx := context.Background()
	q, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "workspace", Limit: 50})
	b, _ := json.Marshal(q)
	if err != nil || len(q.Contexts) == 0 || len(q.Contexts) == 50 || q.Next == "" || len(b) > maxContextQueryBytes || q.Coverage.Complete {
		t.Fatalf("catalog byte bounds: descriptors=%d bytes=%d coverage=%+v %v", len(q.Contexts), len(b), q.Coverage, err)
	}
	second, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "workspace", Limit: 50, Cursor: q.Next})
	if err != nil || len(second.Contexts)+len(q.Contexts) != 50 || second.Contexts[0].ID == q.Contexts[0].ID {
		t.Fatal("catalog byte continuation lost identities")
	}
	if _, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "actual_input", Cursor: q.Next}); !errors.Is(err, ErrStale) {
		t.Fatal("cursor scope not bound")
	}
	if _, err := s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: q.Contexts[0].ID, MaxBytes: 1024}); !errors.Is(err, ErrInvalid) {
		t.Fatal("metadata that exceeds requested budget accepted")
	}
	e := contextInput("oversize", false)
	e.Context.NativeID = strings.Repeat("x", 257)
	e.Evidence.Branch = &snapshot.TrajectoryBranchEvidence{ID: strings.Repeat("x", 257), NativeField: "/branch_id"}
	e.Workspace.Path = strings.Repeat("x", 1025)
	large, _ := relationService(t, relationRecord(e))
	bounded, err := large.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "workspace"})
	if err != nil || len(bounded.Contexts) != 1 || bounded.Contexts[0].NativeID != "" || bounded.Contexts[0].Branch != nil || bounded.Contexts[0].Workspace != nil || bounded.Coverage.Complete {
		t.Fatalf("native metadata silently altered identity: %+v %v", bounded, err)
	}
	for _, invalid := range []snapshot.TrajectorySelector{{ContextScope: "full_prompt"}, {ContextID: "not-a-context"}, {RelationKind: "parent"}, {Limit: 51}} {
		if _, err := s.QueryContexts(ctx, invalid); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid context selector accepted: %+v %v", invalid, err)
		}
	}
}
