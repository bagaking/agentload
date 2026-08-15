package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const maxRelationCandidates = 4
const maxRelationTargetBlocks = 8
const maxRelationNodes = 100
const maxRelationQueryBytes = 32 * 1024
const maxParticipantRecipients = 16

var relationKinds = map[string]bool{
	"parent": true, "reply": true, "delegation": true, "handoff": true,
	"sent": true, "delivered": true, "input_inclusion": true,
	"branch_parent": true, "resume": true, "tool_result": true,
}

func graphSelector(q snapshot.TrajectorySelector, collection string) (snapshot.TrajectorySelector, error) {
	if q.ContextID != "" || q.ContextScope != "" {
		return q, ErrInvalid
	}
	if q.Collection != "" && q.Collection != collection {
		return q, ErrInvalid
	}
	base := q
	base.Collection, base.RelationKind = "events", ""
	if err := ValidateSelector(&base); err != nil {
		return q, err
	}
	q.Limit = base.Limit
	q.Collection = collection
	if q.ActorKind != "" && q.ActorKind != "unknown" && q.ActorKind != "human" && q.ActorKind != "agent" && q.ActorKind != "tool" && q.ActorKind != "system" {
		return q, ErrInvalid
	}
	if len(q.ActorID) > 256 || (q.RelationKind != "" && !relationKinds[q.RelationKind]) || (collection == "actors" && q.RelationKind != "") {
		return q, ErrInvalid
	}
	return q, nil
}

func graphRevision(states []*sourceState) string {
	return sourceRevision(states)
}

func graphCursor(q snapshot.TrajectorySelector, revision string) string {
	q.Cursor = ""
	q.Limit = 0
	b, _ := json.Marshal(q)
	return "graph." + revision + "." + digest(b)
}

func graphStart(q snapshot.TrajectorySelector, revision string) (int, string, error) {
	prefix := graphCursor(q, revision)
	if q.Cursor == "" {
		return 0, prefix, nil
	}
	parts := strings.Split(q.Cursor, ":")
	if len(parts) != 2 || parts[0] != prefix {
		return 0, prefix, ErrStale
	}
	start, err := strconv.Atoi(parts[1])
	if err != nil || start < 0 {
		return 0, prefix, ErrInvalid
	}
	return start, prefix, nil
}

func actorObservations(e snapshot.TrajectoryEvent) []snapshot.TrajectoryActorObservation {
	sender := snapshot.TrajectoryIdentityEvidence{Actor: e.Actor}
	if e.Evidence != nil && e.Evidence.Sender != nil {
		sender = *e.Evidence.Sender
	}
	makeObservation := func(identity snapshot.TrajectoryIdentityEvidence, position string, index int) snapshot.TrajectoryActorObservation {
		if identity.Actor.Kind == "" {
			identity.Actor.Kind = "unknown"
		}
		return snapshot.TrajectoryActorObservation{
			ID: "a." + digest([]byte(e.ID+":"+position+":"+strconv.Itoa(index))), EventID: e.ID,
			SessionID: e.SessionID, Position: position, Actor: identity.Actor,
			Role: e.Role, ProtocolRole: e.ProtocolRole, NativeField: identity.NativeField, Source: e.Source,
		}
	}
	out := []snapshot.TrajectoryActorObservation{makeObservation(sender, "sender", 0)}
	if e.Evidence != nil {
		for i, recipient := range e.Evidence.Recipients {
			if i == maxParticipantRecipients {
				break
			}
			out = append(out, makeObservation(recipient, "recipient", i))
		}
	}
	return out
}

func graphEventMatches(e snapshot.TrajectoryEvent, q snapshot.TrajectorySelector) bool {
	base := q
	base.ActorID, base.ActorKind, base.RelationKind = "", "", ""
	if !matches(e, base) {
		return false
	}
	if q.ActorID == "" && q.ActorKind == "" {
		return true
	}
	identities := []snapshot.TrajectoryActor{e.Actor}
	if e.Evidence != nil {
		if e.Evidence.Sender != nil {
			identities[0] = e.Evidence.Sender.Actor
		}
		for _, recipient := range e.Evidence.Recipients {
			identities = append(identities, recipient.Actor)
		}
	}
	for _, actor := range identities {
		if actor.Kind == "" {
			actor.Kind = "unknown"
		}
		if (q.ActorID == "" || q.ActorID == actor.ID) && (q.ActorKind == "" || q.ActorKind == actor.Kind) {
			return true
		}
	}
	return false
}

// QueryActors returns recorded participations. Unknown observations carry their
// event identity; neither protocol role nor a shared ID-less kind merges them.
func (s *Service) QueryActors(ctx context.Context, q snapshot.TrajectorySelector) (snapshot.TrajectoryActorQueryResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryActorQueryResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryActorQueryResult{Actors: []snapshot.TrajectoryActorObservation{}}
	var err error
	if q, err = graphSelector(q, "actors"); err != nil {
		return out, err
	}
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryActorQueryResult{}, err
	}
	out.Coverage, out.Revision = cov, graphRevision(states)
	start, prefix, err := graphStart(q, out.Revision)
	if err != nil {
		return out, err
	}
	matched := 0
	for _, st := range states {
		if (q.Agent != "" && q.Agent != st.Agent) || (q.SessionID != "" && q.SessionID != sessionID(st)) {
			continue
		}
		local, err := scan(ctx, st, func(e snapshot.TrajectoryEvent) bool {
			if e.Evidence != nil && len(e.Evidence.Recipients) > maxParticipantRecipients {
				gap(&out.Coverage, "recipient_projection_limit")
				out.Coverage.Omitted += len(e.Evidence.Recipients) - maxParticipantRecipients
			}
			base := q
			base.ActorID, base.ActorKind = "", ""
			if !matches(e, base) {
				return true
			}
			for _, observation := range actorObservations(e) {
				if observation.Actor.Kind == "unknown" || observation.Actor.ID == "" {
					gap(&out.Coverage, "actor_identity_unknown")
				}
				if (q.ActorID != "" && q.ActorID != observation.Actor.ID) || (q.ActorKind != "" && q.ActorKind != observation.Actor.Kind) {
					continue
				}
				if matched >= start && len(out.Actors) < q.Limit {
					if len(observation.Actor.ID) > 256 {
						observation.Actor.ID = ""
						gap(&out.Coverage, "actor_id_omitted")
					}
					if len(observation.NativeField) > 160 {
						observation.NativeField = ""
						gap(&out.Coverage, "actor_native_field_omitted")
					}
					out.Actors = append(out.Actors, observation)
				}
				matched++
			}
			return true
		})
		if err != nil {
			return snapshot.TrajectoryActorQueryResult{}, err
		}
		mergeCoverage(&out.Coverage, local)
	}
	if matched > start+len(out.Actors) {
		out.Next = fmt.Sprintf("%s:%d", prefix, start+len(out.Actors))
	}
	return out, nil
}

type selectedRelation struct {
	from     snapshot.TrajectoryRelationNode
	evidence snapshot.TrajectoryRelationEvidence
	source   *sourceState
	index    int
}

func relationNode(e snapshot.TrajectoryEvent) snapshot.TrajectoryRelationNode {
	n := snapshot.TrajectoryRelationNode{
		ID: e.ID, Type: "event", SessionID: e.SessionID, NativeID: e.NativeID,
		NativeEnvelopeID: e.NativeEnvelopeID, Kind: e.Kind, Role: e.Role,
		ProtocolRole: e.ProtocolRole, Actor: e.Actor, Source: e.Source,
	}
	if e.Evidence != nil {
		n.Branch = e.Evidence.Branch
		if e.Evidence.Sender != nil {
			n.Actor = e.Evidence.Sender.Actor
		}
	}
	return n
}

// QueryRelations filters observing events with the ordinary event selectors;
// RelationKind filters edges and ActorKind filters recorded participants.
func (s *Service) QueryRelations(ctx context.Context, q snapshot.TrajectorySelector) (snapshot.TrajectoryRelationQueryResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryRelationQueryResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryRelationQueryResult{Nodes: []snapshot.TrajectoryRelationNode{}, Relations: []snapshot.TrajectoryRelation{}, ByteLimit: maxRelationQueryBytes}
	var err error
	if q, err = graphSelector(q, "relations"); err != nil {
		return out, err
	}
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryRelationQueryResult{}, err
	}
	out.Coverage, out.Revision = cov, graphRevision(states)
	start, prefix, err := graphStart(q, out.Revision)
	if err != nil {
		return out, err
	}
	selected := []selectedRelation{}
	matched := 0
	resolutionComplete := cov.Complete
	for _, st := range states {
		if (q.Agent != "" && q.Agent != st.Agent) || (q.SessionID != "" && q.SessionID != sessionID(st)) {
			continue
		}
		local, err := scan(ctx, st, func(e snapshot.TrajectoryEvent) bool {
			if e.Evidence == nil {
				return true
			}
			base := q
			base.ActorID, base.ActorKind = "", ""
			if matches(e, base) && (q.ActorKind != "" || q.ActorID != "") {
				for _, participant := range actorObservations(e) {
					if (q.ActorKind != "" && participant.Actor.Kind == "unknown") || (q.ActorID != "" && participant.Actor.ID == "") {
						gap(&out.Coverage, "actor_identity_unknown")
					}
				}
			}
			if !graphEventMatches(e, q) {
				return true
			}
			for i, relation := range e.Evidence.Relations {
				if q.RelationKind != "" && relation.Kind != q.RelationKind {
					continue
				}
				if matched >= start && len(selected) < q.Limit {
					selected = append(selected, selectedRelation{relationNode(e), relation, st, i})
				}
				matched++
			}
			return true
		})
		if err != nil {
			return snapshot.TrajectoryRelationQueryResult{}, err
		}
		mergeCoverage(&out.Coverage, local)
		resolutionComplete = resolutionComplete && local.Complete
	}
	window := map[string]bool{}
	for _, selected := range selected {
		window[selected.from.ID] = true
	}
	out.Nodes, out.Relations, err = resolveRelations(ctx, states, selected, &out.Coverage, window, resolutionComplete)
	if err != nil {
		return snapshot.TrajectoryRelationQueryResult{}, err
	}
	fitRelations(&out, map[string]bool{}, "")
	if matched > start+len(out.Relations) {
		out.Next = fmt.Sprintf("%s:%d", prefix, start+len(out.Relations))
	}
	return out, nil
}

type relationCandidate struct {
	key   string
	nodes []snapshot.TrajectoryRelationNode
}
type relationLookup struct {
	selected selectedRelation
	relation snapshot.TrajectoryRelation
	groups   []relationCandidate
	complete bool
}

func referenceSource(ref snapshot.TrajectoryReference, observing, candidate *sourceState) bool {
	agent := ref.Agent
	if agent == "" {
		agent = observing.Agent
	}
	if candidate.Agent != agent || (ref.SourceID != "" && candidate.ID != ref.SourceID) || (ref.Generation != "" && candidate.Generation != ref.Generation) {
		return false
	}
	if ref.SessionNativeID != "" && candidate.NativeID != ref.SessionNativeID {
		return false
	}
	if ref.Kind == "session" {
		return candidate.NativeID == ref.ID || sessionID(candidate) == ref.ID
	}
	if ref.SourceID == "" && ref.SessionNativeID == "" {
		return candidate.ID == observing.ID && candidate.Generation == observing.Generation
	}
	return true
}

func referenceEvent(ref snapshot.TrajectoryReference, e snapshot.TrajectoryEvent) bool {
	switch ref.Kind {
	case "event":
		return ref.ID == e.ID
	case "native_id":
		return ref.ID == e.NativeID
	case "native_envelope":
		return ref.ID == e.NativeEnvelopeID
	case "call":
		return e.Kind == "tool_call" && e.Tool != nil && e.Tool.CallID == ref.ID
	}
	return false
}

func addRelationCandidate(lookup *relationLookup, node snapshot.TrajectoryRelationNode, cov *snapshot.TrajectoryCoverage) {
	key := fmt.Sprintf("%s:%s:%d:%s", node.Source.ID, node.Source.Generation, node.Source.Offset, node.Source.Digest)
	if node.Type == "session" {
		key = node.ID
	}
	for i := range lookup.groups {
		if lookup.groups[i].key != key {
			continue
		}
		for _, existing := range lookup.groups[i].nodes {
			if existing.ID == node.ID {
				return
			}
		}
		if len(lookup.groups[i].nodes) == maxRelationTargetBlocks {
			lookup.complete = false
			gap(cov, "relation_target_block_limit")
			cov.Omitted++
			return
		}
		lookup.groups[i].nodes = append(lookup.groups[i].nodes, node)
		return
	}
	if len(lookup.groups) == maxRelationCandidates {
		lookup.complete = false
		gap(cov, "relation_candidate_limit")
		cov.Omitted++
		return
	}
	lookup.groups = append(lookup.groups, relationCandidate{key: key, nodes: []snapshot.TrajectoryRelationNode{node}})
}

func resolveRelations(ctx context.Context, states []*sourceState, selected []selectedRelation, cov *snapshot.TrajectoryCoverage, window map[string]bool, sourceComplete bool) ([]snapshot.TrajectoryRelationNode, []snapshot.TrajectoryRelation, error) {
	lookups := make([]relationLookup, len(selected))
	nodes := []snapshot.TrajectoryRelationNode{}
	seenNodes := map[string]bool{}
	addNode := func(node snapshot.TrajectoryRelationNode) {
		if seenNodes[node.ID] {
			return
		}
		if len(nodes) == maxRelationNodes {
			gap(cov, "relation_node_limit")
			cov.Omitted++
			return
		}
		seenNodes[node.ID] = true
		if len(node.NativeID) > 256 {
			node.NativeID = ""
			gap(cov, "relation_native_id_omitted")
		}
		if len(node.NativeEnvelopeID) > 256 {
			node.NativeEnvelopeID = ""
			gap(cov, "relation_native_id_omitted")
		}
		if len(node.Actor.ID) > 256 {
			node.Actor.ID = ""
			gap(cov, "actor_id_omitted")
		}
		if node.Branch != nil && (len(node.Branch.ID) > 256 || len(node.Branch.NativeField) > 160) {
			node.Branch = nil
			gap(cov, "branch_identity_omitted")
		}
		nodes = append(nodes, node)
	}
	for i, pick := range selected {
		b, _ := json.Marshal(pick.evidence)
		r := snapshot.TrajectoryRelation{
			ID: "r." + digest([]byte(pick.from.ID+":"+strconv.Itoa(pick.index)+":"+string(b))), From: pick.from.ID,
			Kind: pick.evidence.Kind, Target: pick.evidence.Target, NativeField: pick.evidence.NativeField,
			Source: pick.from.Source, TargetIDs: []string{}, Status: "pending",
		}
		if !relationKinds[r.Kind] || !referenceKind(r.Target.Kind) || r.Target.ID == "" {
			r.Status = "unsupported"
			gap(cov, "relation_evidence_unsupported")
		}
		if r.NativeField == "" {
			r.Status = "missing_evidence"
			gap(cov, "relation_native_field_missing")
		}
		if len(r.Target.ID) > 256 || len(r.Target.SourceID) > 256 || len(r.Target.SessionNativeID) > 256 || len(r.Target.Generation) > 256 || len(r.Target.Agent) > 64 || len(r.NativeField) > 160 {
			r.Target = snapshot.TrajectoryReference{Kind: r.Target.Kind}
			r.NativeField = ""
			r.Status = "unsupported"
			r.Omissions = []string{"relation_reference_size_limit"}
			gap(cov, "relation_reference_size_limit")
		}
		lookups[i] = relationLookup{selected: pick, relation: r, complete: sourceComplete}
		addNode(pick.from)
	}
	for _, st := range states {
		active := []int{}
		for i := range lookups {
			lookup := &lookups[i]
			if lookup.relation.Status != "pending" || !referenceSource(lookup.relation.Target, lookup.selected.source, st) {
				continue
			}
			if lookup.relation.Target.Kind == "session" {
				node := snapshot.TrajectoryRelationNode{ID: sessionID(st), Type: "session", SessionID: sessionID(st), NativeID: st.NativeID, Kind: "session", Actor: snapshot.TrajectoryActor{Kind: "unknown"}, Source: snapshot.TrajectorySourceRef{ID: st.ID, Generation: st.Generation}}
				addRelationCandidate(lookup, node, cov)
				lookup.complete = lookup.complete && st.checkpoint.Coverage.Complete
			} else {
				active = append(active, i)
			}
		}
		if len(active) == 0 {
			continue
		}
		local, err := scan(ctx, st, func(e snapshot.TrajectoryEvent) bool {
			for _, i := range active {
				if referenceEvent(lookups[i].relation.Target, e) {
					addRelationCandidate(&lookups[i], relationNode(e), cov)
				}
			}
			return true
		})
		if err != nil {
			return nil, nil, err
		}
		mergeCoverage(cov, local)
		for _, i := range active {
			lookups[i].complete = lookups[i].complete && local.Complete
		}
	}
	relations := make([]snapshot.TrajectoryRelation, 0, len(lookups))
	for _, lookup := range lookups {
		r := lookup.relation
		if r.Status == "pending" {
			switch {
			case len(lookup.groups) > 1:
				r.Status = "ambiguous"
				gap(cov, "relation_target_ambiguous")
			case !lookup.complete:
				r.Status = "coverage_incomplete"
				gap(cov, "relation_target_coverage_incomplete")
			case len(lookup.groups) == 0:
				r.Status = "missing"
				gap(cov, "relation_target_missing")
			default:
				r.Status = "resolved"
			}
		}
		for _, group := range lookup.groups {
			for _, node := range group.nodes {
				addNode(node)
				if r.Status == "resolved" {
					r.TargetIDs = append(r.TargetIDs, node.ID)
				} else {
					r.CandidateIDs = append(r.CandidateIDs, node.ID)
				}
				if !window[node.ID] {
					r.OutsideWindow = true
				}
			}
		}
		relations = append(relations, r)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return nodes, relations, nil
}

func referenceKind(kind string) bool {
	return kind == "event" || kind == "native_id" || kind == "native_envelope" || kind == "call" || kind == "session"
}

func fitRelations(out *snapshot.TrajectoryRelationQueryResult, window map[string]bool, focus string) {
	for {
		needed := map[string]bool{}
		for id := range window {
			needed[id] = true
		}
		for _, relation := range out.Relations {
			needed[relation.From] = true
			for _, id := range append(append([]string{}, relation.TargetIDs...), relation.CandidateIDs...) {
				needed[id] = true
			}
		}
		nodes := out.Nodes[:0]
		for _, node := range out.Nodes {
			if needed[node.ID] {
				nodes = append(nodes, node)
			}
		}
		out.Nodes = nodes
		b, _ := json.Marshal(out)
		limit := out.ByteLimit
		if out.FocusID == "" {
			limit -= 160 // The query continuation is added after trimming.
		}
		if len(b) <= limit {
			return
		}
		out.Truncated = true
		gap(&out.Coverage, "relation_slice_limit")
		out.Coverage.Omitted++
		if len(out.Relations) > 0 {
			out.Relations = out.Relations[:len(out.Relations)-1]
			continue
		}
		removed := false
		for i := len(out.Nodes) - 1; i >= 0; i-- {
			if out.Nodes[i].ID != focus {
				delete(window, out.Nodes[i].ID)
				removed = true
				break
			}
		}
		if !removed {
			return
		}
	}
}

// GetRelations uses the actual indexed focus neighborhood, then resolves its
// explicit outgoing references across their recorded scopes. Event bodies are
// never retained in a whole-history graph.
func (s *Service) GetRelations(ctx context.Context, p snapshot.TrajectoryGetParams) (snapshot.TrajectoryRelationQueryResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryRelationQueryResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryRelationQueryResult{Nodes: []snapshot.TrajectoryRelationNode{}, Relations: []snapshot.TrajectoryRelation{}, FocusID: p.ID, ByteLimit: MaxSliceBytes}
	if p.MemberOffset != 0 || p.RawOffset != 0 || p.Around < 0 || p.Around > 5 || p.MaxBytes < 0 || p.MaxBytes > MaxSliceBytes || p.Raw || (p.View != "" && p.View != "relations") {
		return out, ErrInvalid
	}
	if p.MaxBytes > 0 {
		out.ByteLimit = p.MaxBytes
	}
	if out.ByteLimit < 1024 {
		return out, ErrInvalid
	}
	parts := strings.Split(p.ID, ".")
	if (len(parts) != 3 && len(parts) != 6) || (parts[0] == "s" && len(parts) != 3) || (parts[0] == "e" && len(parts) != 6) || (parts[0] != "s" && parts[0] != "e") {
		return out, ErrInvalid
	}
	var offset int64
	var block int
	if parts[0] == "e" {
		var err error
		offset, err = strconv.ParseInt(parts[3], 36, 64)
		if err != nil || offset < 0 {
			return out, ErrInvalid
		}
		block, err = strconv.Atoi(parts[4])
		if err != nil || block < 0 {
			return out, ErrInvalid
		}
	}
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryRelationQueryResult{}, err
	}
	out.Coverage, out.Revision = cov, graphRevision(states)
	var source *sourceState
	for _, st := range states {
		if st.ID == parts[1] {
			source = st
			break
		}
	}
	if source == nil {
		return out, ErrNotFound
	}
	if source.Generation != parts[2] {
		return out, ErrStale
	}
	windowEvents, before, after, err := s.indexedWindow(ctx, source, p, offset, block)
	if err != nil {
		return out, err
	}
	out.Before, out.After = before, after
	out.Truncated = before != "" || after != ""
	mergeCoverage(&out.Coverage, source.checkpoint.Coverage)
	resolutionComplete := out.Coverage.Complete
	window := map[string]bool{}
	selected := []selectedRelation{}
	focus := p.ID
	if parts[0] == "s" && len(windowEvents) > 0 {
		focus = windowEvents[len(windowEvents)-1].ID
	}
	// Give the requested event's evidence priority when the bounded graph trims.
	sort.SliceStable(windowEvents, func(i, j int) bool { return windowEvents[i].ID == focus && windowEvents[j].ID != focus })
	for _, e := range windowEvents {
		window[e.ID] = true
		out.Nodes = append(out.Nodes, relationNode(e))
		if e.Evidence == nil {
			continue
		}
		for i, relation := range e.Evidence.Relations {
			if len(selected) == 50 {
				gap(&out.Coverage, "relation_projection_limit")
				out.Coverage.Omitted++
				continue
			}
			selected = append(selected, selectedRelation{relationNode(e), relation, source, i})
		}
	}
	nodes, relations, err := resolveRelations(ctx, states, selected, &out.Coverage, window, resolutionComplete)
	if err != nil {
		return snapshot.TrajectoryRelationQueryResult{}, err
	}
	seen := map[string]bool{}
	for _, node := range out.Nodes {
		seen[node.ID] = true
	}
	for _, node := range nodes {
		if !seen[node.ID] {
			out.Nodes = append(out.Nodes, node)
			seen[node.ID] = true
		}
	}
	out.Relations = relations
	fitRelations(&out, window, focus)
	b, _ := json.Marshal(out)
	if len(b) > out.ByteLimit {
		return out, fmt.Errorf("%w: budget cannot include relation provenance", ErrInvalid)
	}
	return out, nil
}
