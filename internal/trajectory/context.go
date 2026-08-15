package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

const maxContextMemberProjection = 50
const maxContextEventSelection = 256
const maxContextQueryBytes = 64 * 1024

func contextScope(scope string) bool {
	return scope == "archive" || scope == "actual_input" || scope == "workspace" || scope == "query_window"
}

func contextSelector(q snapshot.TrajectorySelector) (snapshot.TrajectorySelector, error) {
	if q.Collection != "" && q.Collection != "contexts" {
		return q, ErrInvalid
	}
	if q.ContextScope == "" {
		q.ContextScope = "archive"
	}
	if !contextScope(q.ContextScope) || q.RelationKind != "" || len(q.ContextID) > 256 || (q.ContextID != "" && !strings.HasPrefix(q.ContextID, "ctx.")) {
		return q, ErrInvalid
	}
	base := q
	base.Collection, base.ContextScope, base.ContextID = "events", "", ""
	if err := ValidateSelector(&base); err != nil {
		return q, err
	}
	q.Collection, q.Limit = "contexts", base.Limit
	return q, nil
}

func contextEventID(e snapshot.TrajectoryEvent, scope string) string {
	return "ctx." + e.Source.ID + "." + e.Source.Generation + "." + scope + "." + strconv.FormatInt(e.Source.Offset, 36) + "." + strconv.Itoa(e.Source.Block) + "." + e.Source.Digest
}

func sourceArchive(st *sourceState) snapshot.TrajectoryContext {
	return snapshot.TrajectoryContext{
		ID: "ctx." + st.ID + "." + st.Generation + ".archive.base", Scope: "archive", SessionID: sessionID(st),
		SourceRevision: sourceRevision([]*sourceState{st}), Membership: "recorded",
		Source: snapshot.TrajectorySourceRef{ID: st.ID, Generation: st.Generation}, Coverage: copyWatchCoverage(st.checkpoint.Coverage),
	}
}

func inputRelations(e snapshot.TrajectoryEvent) []snapshot.TrajectoryRelationEvidence {
	var relations []snapshot.TrajectoryRelationEvidence
	if e.Evidence != nil {
		for _, relation := range e.Evidence.Relations {
			if relation.Kind == "input_inclusion" {
				relations = append(relations, relation)
			}
		}
	}
	return relations
}

func inputBoundary(e snapshot.TrajectoryEvent) bool {
	return e.Kind == "context" || e.Context != nil || len(inputRelations(e)) > 0
}

func eventContext(st *sourceState, e snapshot.TrajectoryEvent, scope string) snapshot.TrajectoryContext {
	c := snapshot.TrajectoryContext{
		ID: contextEventID(e, scope), Scope: scope, SessionID: e.SessionID, AnchorID: e.ID,
		SourceRevision: sourceRevision([]*sourceState{st}), Membership: "recorded", Source: e.Source, Coverage: copyWatchCoverage(st.checkpoint.Coverage),
	}
	if e.Evidence != nil {
		c.Branch = e.Evidence.Branch
	}
	if e.Context != nil {
		if e.Context.NativeField != "" {
			c.NativeID, c.NativeRevision = e.Context.NativeID, e.Context.Revision
		} else {
			gap(&c.Coverage, "context_native_field_missing")
		}
	}
	if scope == "workspace" {
		c.Workspace, c.KnownMembers = e.Workspace, 1
	}
	if scope == "actual_input" {
		c.KnownMembers = len(inputRelations(e))
		c.Membership = "unknown"
		if c.KnownMembers == 0 {
			gap(&c.Coverage, "model_input_membership_unknown")
		} else {
			c.Membership = "partial"
			if e.Context != nil && e.Context.MembershipComplete && e.Context.CompletenessNativeField != "" && st.checkpoint.Coverage.Complete {
				c.Membership = "complete"
			} else {
				gap(&c.Coverage, "model_input_scope_incomplete")
			}
		}
	}
	boundContext(&c)
	return c
}

// Native metadata is either retained intact or explicitly omitted. Canonical
// IDs and source locators are never shortened into a different identity.
func boundContext(c *snapshot.TrajectoryContext) {
	if len(c.NativeID) > 256 || len(c.NativeRevision) > 256 {
		c.NativeID, c.NativeRevision = "", ""
		gap(&c.Coverage, "context_native_identity_omitted")
	}
	if c.Branch != nil && (c.Branch.ID == "" || c.Branch.NativeField == "" || len(c.Branch.ID) > 256 || len(c.Branch.NativeField) > 160) {
		c.Branch = nil
		gap(&c.Coverage, "context_branch_evidence_omitted")
	}
	if c.Workspace != nil && (c.Workspace.NativeField == "" || len(c.Workspace.Path) > 1024 || len(c.Workspace.NativeField) > 160) {
		c.Workspace = nil
		gap(&c.Coverage, "context_workspace_evidence_omitted")
	}
	if len(c.Source.NativeType) > 160 {
		c.Source.NativeType = ""
		gap(&c.Coverage, "context_native_type_omitted")
	}
	if c.Transformation != nil && len(c.Transformation.Source.NativeType) > 160 {
		c.Transformation.Source.NativeType = ""
		gap(&c.Coverage, "context_native_type_omitted")
	}
}

// archive contexts are source segments bounded by recorded transformation
// milestones; their membership never claims later model-input visibility.
func contextCatalog(ctx context.Context, st *sourceState, q snapshot.TrajectorySelector, visit func(snapshot.TrajectoryContext)) (snapshot.TrajectoryCoverage, error) {
	current := sourceArchive(st)
	hit := q.Text == "" && q.Tool == "" && q.Skill == "" && q.Kind == "" && q.Role == "" && q.ActorID == "" && q.ActorKind == "" && q.EntityID == "" && q.EntityKind == "" && q.Predicate == ""
	base := q
	base.ContextID, base.ContextScope = "", ""
	emit := func(c snapshot.TrajectoryContext, matches bool) {
		if matches && (q.ContextID == "" || q.ContextID == c.ID) {
			visit(c)
		}
	}
	local, err := scan(ctx, st, func(e snapshot.TrajectoryEvent) bool {
		matches := graphEventMatches(e, base)
		if q.ContextScope == "archive" {
			if e.Kind == "summary" || e.Kind == "compaction" {
				next := eventContext(st, e, "archive")
				current.AfterID, next.BeforeID = next.ID, current.ID
				next.Transformation = &snapshot.TrajectoryContextTransformation{Kind: e.Kind, EventID: e.ID, Source: e.Source, BeforeID: current.ID, AfterID: next.ID, OriginalsStatus: "unknown"}
				gap(&next.Coverage, "transformation_originals_unknown")
				boundContext(&next)
				emit(current, hit)
				current, hit = next, false
			}
			if current.AnchorID == "" {
				current.AnchorID, current.Source = e.ID, e.Source
			}
			current.KnownMembers++
			hit = hit || matches
			return true
		}
		if matches && ((q.ContextScope == "actual_input" && inputBoundary(e)) || (q.ContextScope == "workspace" && e.Workspace != nil) || q.ContextScope == "query_window") {
			emit(eventContext(st, e, q.ContextScope), true)
		}
		return true
	})
	if err != nil {
		return local, err
	}
	if q.ContextScope == "archive" && current.KnownMembers > 0 {
		emit(current, hit)
	}
	return local, nil
}

func (s *Service) QueryContexts(ctx context.Context, q snapshot.TrajectorySelector) (snapshot.TrajectoryContextQueryResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryContextQueryResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryContextQueryResult{Contexts: []snapshot.TrajectoryContext{}}
	var err error
	if q, err = contextSelector(q); err != nil {
		return out, err
	}
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryContextQueryResult{}, err
	}
	out.Coverage, out.Revision = cov, sourceRevision(states)
	if q.ContextID != "" {
		if _, _, _, err := contextSource(states, q.ContextID); err != nil {
			return out, err
		}
	}
	start, prefix, err := graphStart(q, out.Revision)
	if err != nil {
		return out, err
	}
	matched := 0
	for _, st := range states {
		if (q.Agent != "" && q.Agent != st.Agent) || (q.SessionID != "" && q.SessionID != sessionID(st)) {
			continue
		}
		local, err := contextCatalog(ctx, st, q, func(c snapshot.TrajectoryContext) {
			// Checkpoint omissions are merged once through the scan, rather
			// than once for every descriptor from the same source.
			semantic := c.Coverage
			semantic.Omitted = 0
			mergeCoverage(&out.Coverage, semantic)
			if c.Scope == "actual_input" && !cov.Complete && c.Membership == "complete" {
				c.Membership = "partial"
				gap(&c.Coverage, "context_source_discovery_incomplete")
			}
			if matched >= start && len(out.Contexts) < q.Limit {
				out.Contexts = append(out.Contexts, c)
			}
			matched++
		})
		if err != nil {
			return snapshot.TrajectoryContextQueryResult{}, err
		}
		mergeCoverage(&out.Coverage, local)
		for i := range out.Contexts {
			if out.Contexts[i].Source.ID == st.ID {
				// The descriptor already includes the source checkpoint.
				local.Omitted = 0
				mergeCoverage(&out.Contexts[i].Coverage, local)
			}
		}
	}
	for {
		out.Next = ""
		if matched > start+len(out.Contexts) {
			out.Next = fmt.Sprintf("%s:%d", prefix, start+len(out.Contexts))
		}
		b, _ := json.Marshal(out)
		if len(b) <= maxContextQueryBytes {
			break
		}
		if len(out.Contexts) <= 1 {
			return out, fmt.Errorf("%w: budget cannot include context catalog provenance", ErrInvalid)
		}
		out.Contexts = out.Contexts[:len(out.Contexts)-1]
		gap(&out.Coverage, "context_catalog_size_limit")
		out.Coverage.Omitted++
	}
	return out, nil
}

func contextSource(states []*sourceState, id string) (*sourceState, string, string, error) {
	parts := strings.Split(id, ".")
	if (len(parts) != 5 && len(parts) != 7) || parts[0] != "ctx" || !contextScope(parts[3]) || (len(parts) == 5 && (parts[3] != "archive" || parts[4] != "base")) {
		return nil, "", "", ErrInvalid
	}
	var st *sourceState
	for _, source := range states {
		if source.ID == parts[1] {
			st = source
			break
		}
	}
	if st == nil {
		return nil, "", "", ErrNotFound
	}
	if st.Generation != parts[2] {
		return nil, "", "", ErrStale
	}
	anchor := ""
	if len(parts) == 7 {
		anchor = "e." + strings.Join([]string{parts[1], parts[2], parts[4], parts[5], parts[6]}, ".")
	}
	return st, parts[3], anchor, nil
}

func contextAnchor(ctx context.Context, s *Service, st *sourceState, id string) (snapshot.TrajectoryEvent, error) {
	parts := strings.Split(id, ".")
	if len(parts) != 6 {
		return snapshot.TrajectoryEvent{}, ErrInvalid
	}
	offset, err := strconv.ParseInt(parts[3], 36, 64)
	if err != nil || offset < 0 {
		return snapshot.TrajectoryEvent{}, ErrInvalid
	}
	block, err := strconv.Atoi(parts[4])
	if err != nil || block < 0 {
		return snapshot.TrajectoryEvent{}, ErrInvalid
	}
	window, _, _, err := s.indexedWindow(ctx, st, snapshot.TrajectoryGetParams{ID: id}, offset, block)
	if err != nil {
		return snapshot.TrajectoryEvent{}, err
	}
	if len(window) != 1 {
		return snapshot.TrajectoryEvent{}, ErrNotFound
	}
	return window[0], nil
}

func memberForEvent(e snapshot.TrajectoryEvent, visibility string) snapshot.TrajectoryContextMember {
	return snapshot.TrajectoryContextMember{ID: "m." + digest([]byte(visibility+":"+e.ID)), EventIDs: []string{e.ID}, Target: snapshot.TrajectoryReference{Kind: "event", ID: e.ID}, Status: "recorded", Visibility: visibility, Source: e.Source}
}

func inputManifest(ctx context.Context, states []*sourceState, st *sourceState, e snapshot.TrajectoryEvent, offset int, cov *snapshot.TrajectoryCoverage, sourceComplete bool) ([]snapshot.TrajectoryContextMember, int, error) {
	relations := inputRelations(e)
	selected := []selectedRelation{}
	for i := offset; i < len(relations) && len(selected) < maxContextMemberProjection; i++ {
		selected = append(selected, selectedRelation{from: relationNode(e), evidence: relations[i], source: st, index: i})
	}
	_, resolved, err := resolveRelations(ctx, states, selected, cov, map[string]bool{e.ID: true}, sourceComplete && st.checkpoint.Coverage.Complete)
	if err != nil {
		return nil, 0, err
	}
	members := make([]snapshot.TrajectoryContextMember, 0, len(resolved))
	for _, edge := range resolved {
		members = append(members, snapshot.TrajectoryContextMember{ID: edge.ID, EventIDs: edge.TargetIDs, Target: edge.Target, Status: edge.Status, Visibility: "input_included", NativeField: edge.NativeField, Source: edge.Source, Candidates: edge.CandidateIDs})
	}
	return members, len(relations), nil
}

func (s *Service) contextManifestLocked(ctx context.Context, states []*sourceState, p snapshot.TrajectoryGetParams, collectionCoverage snapshot.TrajectoryCoverage) (snapshot.TrajectoryContextGetResult, error) {
	out := snapshot.TrajectoryContextGetResult{Members: []snapshot.TrajectoryContextMember{}, FocusID: p.ID, Revision: sourceRevision(states), ByteLimit: MaxSliceBytes, MemberOffset: p.MemberOffset, Coverage: coverage("context manifest")}
	if p.Around < 0 || p.Around > 5 || p.MemberOffset < 0 || p.MaxBytes < 0 || p.MaxBytes > MaxSliceBytes || p.Raw || p.RawOffset != 0 || (p.View != "" && p.View != "context") {
		return out, ErrInvalid
	}
	if p.MaxBytes > 0 {
		out.ByteLimit = p.MaxBytes
	}
	if out.ByteLimit < 1024 {
		return out, ErrInvalid
	}
	mergeCoverage(&out.Coverage, collectionCoverage)
	st, scope, anchorID, err := contextSource(states, p.ID)
	if err != nil {
		return out, err
	}
	mergeCoverage(&out.Coverage, st.checkpoint.Coverage)
	total := 0
	if scope == "archive" {
		q := snapshot.TrajectorySelector{ContextScope: scope, ContextID: p.ID}
		local, err := contextCatalog(ctx, st, q, func(c snapshot.TrajectoryContext) { copy := c; out.Context = &copy })
		if err != nil {
			return snapshot.TrajectoryContextGetResult{}, err
		}
		local.Omitted = max(0, local.Omitted-st.checkpoint.Coverage.Omitted)
		mergeCoverage(&out.Coverage, local)
		if out.Context == nil {
			return out, ErrStale
		}
		active := p.ID == sourceArchive(st).ID
		local, err = scan(ctx, st, func(e snapshot.TrajectoryEvent) bool {
			if e.Kind == "summary" || e.Kind == "compaction" {
				active = contextEventID(e, "archive") == p.ID
			}
			if active {
				if total >= p.MemberOffset && len(out.Members) < maxContextMemberProjection {
					out.Members = append(out.Members, memberForEvent(e, "archive_record"))
				}
				total++
			}
			return true
		})
		if err != nil {
			return snapshot.TrajectoryContextGetResult{}, err
		}
		local.Omitted = max(0, local.Omitted-st.checkpoint.Coverage.Omitted)
		mergeCoverage(&out.Coverage, local)
	} else {
		e, err := contextAnchor(ctx, s, st, anchorID)
		if err != nil {
			return out, err
		}
		c := eventContext(st, e, scope)
		out.Context = &c
		switch scope {
		case "actual_input":
			if !inputBoundary(e) {
				return out, ErrNotFound
			}
			out.Members, total, err = inputManifest(ctx, states, st, e, p.MemberOffset, &out.Coverage, collectionCoverage.Complete)
			if err != nil {
				return snapshot.TrajectoryContextGetResult{}, err
			}
		case "workspace":
			if e.Workspace == nil {
				return out, ErrNotFound
			}
			total = 1
			if p.MemberOffset == 0 {
				member := memberForEvent(e, "workspace_reference")
				member.NativeField = e.Workspace.NativeField
				out.Members = append(out.Members, member)
			}
		case "query_window":
			parts := strings.Split(e.ID, ".")
			offset, _ := strconv.ParseInt(parts[3], 36, 64)
			window, before, after, err := s.indexedWindow(ctx, st, snapshot.TrajectoryGetParams{ID: e.ID, Around: p.Around}, offset, e.Source.Block)
			if err != nil {
				return out, err
			}
			out.Before, out.After = before, after
			total = len(window)
			out.Context.KnownMembers = total
			for i, event := range window {
				if i >= p.MemberOffset {
					out.Members = append(out.Members, memberForEvent(event, "retrieval_window"))
				}
			}
		}
	}
	if p.MemberOffset > total {
		return out, ErrInvalid
	}
	semantic := out.Context.Coverage
	semantic.Omitted = 0
	mergeCoverage(&out.Coverage, semantic)
	if scope == "actual_input" && !out.Coverage.Complete && out.Context.Membership == "complete" {
		out.Context.Membership = "partial"
	}
	baseOmitted := out.Coverage.Omitted
	for {
		consumed := p.MemberOffset + len(out.Members)
		out.NextOffset = nil
		if consumed < total {
			next := consumed
			out.NextOffset, out.Truncated = &next, true
		}
		out.Coverage.Omitted = baseOmitted + max(0, total-consumed)
		if consumed < total && len(out.Members) == 0 {
			return out, fmt.Errorf("%w: budget cannot include context member provenance", ErrInvalid)
		}
		b, _ := json.Marshal(out)
		if len(b) <= out.ByteLimit {
			break
		}
		if len(out.Members) == 0 {
			return out, fmt.Errorf("%w: budget cannot include context provenance", ErrInvalid)
		}
		out.Members = out.Members[:len(out.Members)-1]
		out.Truncated = true
	}
	return out, nil
}

func (s *Service) GetContext(ctx context.Context, p snapshot.TrajectoryGetParams) (snapshot.TrajectoryContextGetResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryContextGetResult{}, err
	}
	defer s.opMu.Unlock()
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryContextGetResult{}, err
	}
	return s.contextManifestLocked(ctx, states, p, cov)
}

// ContextID event filtering is only an explicit input-membership operation.
// Archived events, workspace references and a reading window cannot supply it.
func (s *Service) contextMembersLocked(ctx context.Context, states []*sourceState, id string, collectionCoverage snapshot.TrajectoryCoverage) ([]string, snapshot.TrajectoryCoverage, error) {
	cov := coverage("explicit input inclusion")
	mergeCoverage(&cov, collectionCoverage)
	st, scope, anchorID, err := contextSource(states, id)
	if err != nil {
		return nil, cov, err
	}
	if scope != "actual_input" {
		return nil, cov, fmt.Errorf("%w: context selector requires actual_input", ErrInvalid)
	}
	e, err := contextAnchor(ctx, s, st, anchorID)
	if err != nil {
		return nil, cov, err
	}
	if !inputBoundary(e) {
		return nil, cov, ErrNotFound
	}
	c := eventContext(st, e, scope)
	mergeCoverage(&cov, c.Coverage)
	members, total, err := inputManifest(ctx, states, st, e, 0, &cov, collectionCoverage.Complete)
	if err != nil {
		return nil, cov, err
	}
	if total > len(members) {
		gap(&cov, "context_member_projection_limit")
		cov.Omitted += total - len(members)
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, member := range members {
		for _, id := range member.EventIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			if len(ids) == maxContextEventSelection {
				gap(&cov, "context_event_selection_limit")
				cov.Omitted++
				continue
			}
			ids = append(ids, id)
		}
	}
	return ids, cov, nil
}

func (s *Service) ContextMembers(ctx context.Context, id string) ([]string, snapshot.TrajectoryCoverage, error) {
	if err := s.lockOperation(ctx); err != nil {
		return nil, snapshot.TrajectoryCoverage{}, err
	}
	defer s.opMu.Unlock()
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return nil, snapshot.TrajectoryCoverage{}, err
	}
	return s.contextMembersLocked(ctx, states, id, cov)
}
