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
	"time"
)

// This fixture serializes the decoder contract to real indexed physical lines;
// selectors and graph resolution are the production Service implementation.
type recordedRelationDecoder struct{}

func (recordedRelationDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	var record struct {
		Events []snapshot.TrajectoryEvent `json:"events"`
	}
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	for i := range record.Events {
		record.Events[i].SessionID = ctx.SessionID
		if record.Events[i].Actor.Kind == "" {
			record.Events[i].Actor.Kind = "unknown"
		}
	}
	return record.Events, nil
}

func relationRecord(events ...snapshot.TrajectoryEvent) string {
	b, _ := json.Marshal(map[string]any{"type": "recorded-fixture", "events": events})
	return string(b) + "\n"
}

func relationEvent(id, text string, relations ...snapshot.TrajectoryRelationEvidence) snapshot.TrajectoryEvent {
	timestamp := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	return snapshot.TrajectoryEvent{
		NativeID: "msg-" + id, NativeEnvelopeID: id, Kind: "text", Role: "user", ProtocolRole: "user", Text: text,
		Actor: snapshot.TrajectoryActor{Kind: "unknown"}, Evidence: &snapshot.TrajectoryEvidence{Relations: relations},
		Timestamp: &timestamp, Workspace: &snapshot.TrajectoryWorkspace{Path: "/shared/workspace", NativeField: "/cwd"},
	}
}

func recordedParent(id string) snapshot.TrajectoryRelationEvidence {
	return snapshot.TrajectoryRelationEvidence{Kind: "parent", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: id}, NativeField: "parentUuid"}
}

func relationService(t *testing.T, bodies ...string) (*Service, []string) {
	t.Helper()
	root := t.TempDir()
	sources := []Source{}
	paths := []string{}
	for i, body := range bodies {
		path := filepath.Join(root, fmt.Sprintf("source-%d.jsonl", i))
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
		sources = append(sources, Source{Agent: "recorded", Path: path, NativeID: fmt.Sprintf("native-source-%d", i), Decoder: recordedRelationDecoder{}})
	}
	s := New(func(context.Context) SourceSet {
		return SourceSet{Sources: sources, Coverage: coverage("recorded fixture sources")}
	})
	t.Cleanup(func() { _ = s.Close() })
	return s, paths
}

func TestTrajectoryActorsRecordedIdentityIsSeparateFromProtocolRole(t *testing.T) {
	unknown := relationEvent("unknown", "human-looking request")
	agent := relationEvent("agent", "agent message over user transport")
	agent.Evidence.Sender = &snapshot.TrajectoryIdentityEvidence{Actor: snapshot.TrajectoryActor{Kind: "agent", ID: "agent-a"}, NativeField: "sender.agent_id"}
	agent.Evidence.Recipients = []snapshot.TrajectoryIdentityEvidence{{Actor: snapshot.TrajectoryActor{Kind: "human", ID: "person-b"}, NativeField: "recipients[0].person_id"}}
	assistant := relationEvent("assistant", "response with unknown sender")
	assistant.Role, assistant.ProtocolRole = "assistant", "assistant"
	s, _ := relationService(t, relationRecord(unknown)+relationRecord(agent)+relationRecord(assistant))
	ctx := context.Background()
	q, err := s.QueryActors(ctx, snapshot.TrajectorySelector{ActorKind: "unknown"})
	if err != nil || len(q.Actors) != 2 || q.Coverage.Complete {
		t.Fatalf("unknown identity query: %+v %v", q, err)
	}
	if q.Actors[0].ID == q.Actors[1].ID || q.Actors[0].Actor.ID != "" || q.Actors[1].Actor.ID != "" {
		t.Fatal("unknown actors merged or identified")
	}
	agents, err := s.QueryActors(ctx, snapshot.TrajectorySelector{Role: "user", ActorKind: "agent"})
	if err != nil || len(agents.Actors) != 1 || agents.Actors[0].Actor.ID != "agent-a" || agents.Actors[0].ProtocolRole != "user" || agents.Actors[0].Position != "sender" || agents.Actors[0].NativeField != "sender.agent_id" {
		t.Fatalf("agent through user transport: %+v %v", agents, err)
	}
	recipients, err := s.QueryActors(ctx, snapshot.TrajectorySelector{ActorID: "person-b"})
	if err != nil || len(recipients.Actors) != 1 || recipients.Actors[0].Position != "recipient" || recipients.Actors[0].Source.Line != 2 {
		t.Fatalf("recorded recipient not queryable: %+v %v", recipients, err)
	}
	if _, err := s.QueryActors(ctx, snapshot.TrajectorySelector{ActorKind: "user"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("protocol role accepted as actor kind")
	}
}

func TestTrajectoryRelationsOutOfOrderReferencesAndBeyondWindowTargets(t *testing.T) {
	child := relationEvent("child", "resume work", recordedParent("parent"))
	parent := relationEvent("parent", "initial request")
	secondBlock := parent
	secondBlock.Kind, secondBlock.Text = "reasoning", "recorded reasoning"
	s, _ := relationService(t, relationRecord(child)+relationRecord(relationEvent("proximity", "resume work"))+relationRecord(parent, secondBlock))
	q, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{RelationKind: "parent"})
	if err != nil || len(q.Relations) != 1 || q.Relations[0].Status != "resolved" || len(q.Relations[0].TargetIDs) != 2 || !q.Relations[0].OutsideWindow || !q.Coverage.Complete {
		t.Fatalf("out-of-order parent reference: %+v %v", q, err)
	}
	r := q.Relations[0]
	if r.NativeField != "parentUuid" || r.Source.Line != 1 || r.Source.Digest == "" {
		t.Fatal("edge native basis lost")
	}
	read, err := s.GetRelations(context.Background(), snapshot.TrajectoryGetParams{ID: r.From, Around: 0})
	if err != nil || len(read.Relations) != 1 || read.Relations[0].ID != r.ID || len(read.Nodes) != 3 {
		t.Fatalf("canonical graph read: %+v %v", read, err)
	}
	for _, node := range read.Nodes {
		if node.ID != r.From && (node.Source.Line != 3 || node.NativeEnvelopeID != "parent") {
			t.Fatalf("target provenance lost: %+v", node)
		}
	}
	if _, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{Kind: "parent"}); err != nil {
		t.Fatal(err)
	}
	eventKind, _ := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{Kind: "parent"})
	if len(eventKind.Relations) != 0 {
		t.Fatal("event kind selector silently changed to relation kind")
	}
}

func TestTrajectoryRelationsOnlyRecordedLinksEstablishCausalityAndDelivery(t *testing.T) {
	prose := relationEvent("prose", "spawn agent-a; sent, delivered and included")
	sent := relationEvent("sent", "sending request", snapshot.TrajectoryRelationEvidence{Kind: "sent", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: "prose"}, NativeField: "sent_message_id"})
	delegated := relationEvent("delegated", "delegating", snapshot.TrajectoryRelationEvidence{Kind: "delegation", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: "missing-child"}, NativeField: "child_message_id"})
	s, _ := relationService(t, relationRecord(prose)+relationRecord(sent)+relationRecord(delegated))
	q, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if err != nil || len(q.Relations) != 2 {
		t.Fatalf("explicit edges: %+v %v", q, err)
	}
	if q.Relations[0].Kind != "sent" || q.Relations[0].Status != "resolved" || q.Relations[1].Status != "missing" || len(q.Relations[1].TargetIDs) != 0 {
		t.Fatal("unobserved delivery or child fabricated")
	}
	for _, kind := range []string{"reply", "delivered", "input_inclusion", "resume"} {
		result, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{RelationKind: kind})
		if err != nil || len(result.Relations) != 0 {
			t.Fatalf("%s inferred from prose/proximity: %+v %v", kind, result, err)
		}
	}
	missingBasis := relationEvent("no-basis", "", snapshot.TrajectoryRelationEvidence{Kind: "reply", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: "prose"}})
	noBasis, _ := relationService(t, relationRecord(prose)+relationRecord(missingBasis))
	result, _ := noBasis.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if result.Relations[0].Status != "missing_evidence" || len(result.Relations[0].TargetIDs) != 0 {
		t.Fatal("unattributed normalized edge asserted as evidence")
	}
}

func TestTrajectoryRelationsNativeTypeSourceGenerationAndAmbiguity(t *testing.T) {
	wrongType := relationEvent("child", "", recordedParent("msg-parent"))
	s, _ := relationService(t, relationRecord(wrongType)+relationRecord(relationEvent("parent", "")))
	q, _ := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if len(q.Relations) != 1 || q.Relations[0].Status != "missing" {
		t.Fatal("message ID used as envelope UUID")
	}
	local := relationEvent("local-child", "", recordedParent("parent"))
	cross, _ := relationService(t, relationRecord(local), relationRecord(relationEvent("parent", "")))
	q, _ = cross.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if q.Relations[0].Status != "missing" {
		t.Fatal("matching ID in unrecorded cross-source scope linked")
	}
	local.Evidence.Relations[0].Target.SessionNativeID = "native-source-1"
	explicit, _ := relationService(t, relationRecord(local), relationRecord(relationEvent("parent", "")))
	q, _ = explicit.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if q.Relations[0].Status != "resolved" {
		t.Fatalf("explicit native session scope ignored: %+v", q)
	}
	local.Evidence.Relations[0].Target.Generation = "unobserved-old-generation"
	stale, _ := relationService(t, relationRecord(local), relationRecord(relationEvent("parent", "")))
	q, _ = stale.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if q.Relations[0].Status != "missing" || len(q.Relations[0].TargetIDs) != 0 {
		t.Fatal("stale native reference attached to current generation")
	}
	duplicate, _ := relationService(t, relationRecord(relationEvent("child", "", recordedParent("duplicate")))+relationRecord(relationEvent("duplicate", "first"))+relationRecord(relationEvent("duplicate", "second")))
	q, _ = duplicate.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if q.Relations[0].Status != "ambiguous" || len(q.Relations[0].TargetIDs) != 0 || len(q.Relations[0].CandidateIDs) != 2 || q.Coverage.Complete {
		t.Fatalf("duplicate native record picked arbitrarily: %+v", q)
	}
}

func TestTrajectoryRelationsIncompleteCoverageDoesNotProveUniqueTarget(t *testing.T) {
	s, _ := relationService(t, relationRecord(relationEvent("child", "", recordedParent("parent")))+relationRecord(relationEvent("parent", ""))+`{"events":`)
	q, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if err != nil || len(q.Relations) != 1 || q.Relations[0].Status != "coverage_incomplete" || len(q.Relations[0].TargetIDs) != 0 || len(q.Relations[0].CandidateIDs) != 1 {
		t.Fatalf("unique-looking target under partial coverage: %+v %v", q, err)
	}
}

func TestTrajectoryRelationsActorUncertaintyDoesNotEraseRecordedReference(t *testing.T) {
	s, _ := relationService(t, relationRecord(relationEvent("child", "", recordedParent("parent")))+relationRecord(relationEvent("parent", "")))
	q, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{ActorKind: "unknown"})
	if err != nil || len(q.Relations) != 1 || q.Relations[0].Status != "resolved" || q.Coverage.Complete {
		t.Fatalf("identity uncertainty mixed into native reference completeness: %+v %v", q, err)
	}
}

func TestTrajectoryBranchesMembershipAndResumeRemainExplicit(t *testing.T) {
	child := relationEvent("child", "", snapshot.TrajectoryRelationEvidence{Kind: "resume", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: "parent", SessionNativeID: "native-source-1"}, NativeField: "resume_from_uuid"})
	child.Evidence.Branch = &snapshot.TrajectoryBranchEvidence{ID: "shared-branch-name", NativeField: "branch_id"}
	parent := relationEvent("parent", "")
	parent.Evidence.Branch = &snapshot.TrajectoryBranchEvidence{ID: "shared-branch-name", NativeField: "branch_id"}
	s, _ := relationService(t, relationRecord(child), relationRecord(parent))
	q, err := s.QueryRelations(context.Background(), snapshot.TrajectorySelector{RelationKind: "resume"})
	if err != nil || len(q.Relations) != 1 || q.Relations[0].Status != "resolved" || len(q.Nodes) != 2 || q.Nodes[0].SessionID == q.Nodes[1].SessionID {
		t.Fatalf("resume scope collapsed by branch name: %+v %v", q, err)
	}
	for _, node := range q.Nodes {
		if node.Branch == nil || node.Branch.ID != "shared-branch-name" {
			t.Fatal("explicit branch membership lost")
		}
	}
	child.Evidence.Relations = nil
	independent, _ := relationService(t, relationRecord(child), relationRecord(parent))
	q, _ = independent.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if len(q.Relations) != 0 {
		t.Fatal("duplicate branch IDs inferred parent or resume")
	}
	parent2 := parent
	parent2.Text = "another record with duplicate envelope"
	ambiguous, _ := relationService(t, relationRecord(relationEvent("resume-child", "", snapshot.TrajectoryRelationEvidence{Kind: "resume", Target: snapshot.TrajectoryReference{Kind: "native_envelope", ID: "parent"}, NativeField: "resume_from_uuid"}))+relationRecord(parent)+relationRecord(parent2))
	q, _ = ambiguous.QueryRelations(context.Background(), snapshot.TrajectorySelector{})
	if q.Relations[0].Status != "ambiguous" || len(q.Relations[0].TargetIDs) != 0 {
		t.Fatal("duplicate branch target forced to last record")
	}
}

func TestTrajectoryRelationsLongHistoryFocusAndBoundedGraph(t *testing.T) {
	var body strings.Builder
	body.WriteString(relationRecord(relationEvent("early-parent", "initial")))
	for i := 0; i < 5000; i++ {
		body.WriteString(relationRecord(relationEvent(fmt.Sprintf("unrelated-%d", i), "unrelated")))
	}
	body.WriteString(relationRecord(relationEvent("late-focus", "late-focus", recordedParent("early-parent"))))
	s, _ := relationService(t, body.String())
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Text: "late-focus"})
	if len(q.Events) != 1 {
		t.Fatalf("late focus query: %+v", q)
	}
	read, err := s.GetRelations(context.Background(), snapshot.TrajectoryGetParams{ID: q.Events[0].ID, Around: 0, MaxBytes: 2048})
	if err != nil || len(read.Relations) != 1 || read.Relations[0].Status != "resolved" || len(read.Nodes) != 2 {
		t.Fatalf("long-history focus omitted: %+v %v", read, err)
	}
	if read.Nodes[0].Source.Line != 5002 || read.Nodes[1].Source.Line != 1 || !read.Relations[0].OutsideWindow {
		t.Fatal("graph used oldest prefix instead of actual focus")
	}
	b, _ := json.Marshal(read)
	if len(b) > 2048 {
		t.Fatalf("relation byte budget exceeded: %d", len(b))
	}
}

func TestTrajectoryRelationsBoundsAndCursorUseCanonicalPage(t *testing.T) {
	var body strings.Builder
	refs := []snapshot.TrajectoryRelationEvidence{}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("target-%d", i)
		body.WriteString(relationRecord(relationEvent(id, "")))
		refs = append(refs, recordedParent(id))
	}
	body.WriteString(relationRecord(relationEvent("focus", "focus", refs...)))
	s, _ := relationService(t, body.String())
	ctx := context.Background()
	page, err := s.QueryRelations(ctx, snapshot.TrajectorySelector{Limit: 50})
	if err != nil || page.Next == "" || !page.Truncated || len(page.Relations) == 0 || len(page.Relations) >= 50 {
		t.Fatalf("bounded graph page: %+v %v", page, err)
	}
	b, _ := json.Marshal(page)
	if len(b) > maxRelationQueryBytes || len(page.Nodes) > maxRelationNodes {
		t.Fatal("query graph exceeded hard bounds")
	}
	next, err := s.QueryRelations(ctx, snapshot.TrajectorySelector{Limit: 50, Cursor: page.Next})
	if err != nil || len(page.Relations)+len(next.Relations) != 50 {
		t.Fatalf("relation pagination lost edges: %d + %d: %v", len(page.Relations), len(next.Relations), err)
	}
	seen := map[string]bool{}
	for _, edge := range append(page.Relations, next.Relations...) {
		if seen[edge.ID] {
			t.Fatal("pagination repeated canonical edge")
		}
		seen[edge.ID] = true
	}
	if _, err := s.QueryRelations(ctx, snapshot.TrajectorySelector{Limit: 50, Cursor: page.Next, RelationKind: "reply"}); !errors.Is(err, ErrStale) {
		t.Fatal("cursor accepted under different selector meaning")
	}
	query, _ := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", Text: "focus"})
	read, err := s.GetRelations(ctx, snapshot.TrajectoryGetParams{ID: query.Events[0].ID, Around: 0, MaxBytes: 2048})
	if err != nil || !read.Truncated || read.Coverage.Omitted == 0 {
		t.Fatalf("get graph failed to disclose omitted edges: %+v %v", read, err)
	}
	b, _ = json.Marshal(read)
	if len(b) > 2048 {
		t.Fatal("get relation budget exceeded")
	}
	focusFound := false
	for _, node := range read.Nodes {
		focusFound = focusFound || node.ID == query.Events[0].ID
	}
	if !focusFound {
		t.Fatal("byte trimming dropped focus provenance")
	}
}
