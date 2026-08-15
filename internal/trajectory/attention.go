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

const attentionErrorThreshold = 3
const attentionActionWindow = 20
const maxAttentionTurns = 16
const maxAttentionRequests = 64
const maxAttentionInterventions = 16
const maxAttentionScanEvents = 100000
const maxAttentionQueryBytes = 64 * 1024
const maxAttentionProjectionBytes = 12 * 1024

func attentionRules() snapshot.TrajectoryAttentionRules {
	return snapshot.TrajectoryAttentionRules{
		Version: "native-attention-v1", ErrorThreshold: attentionErrorThreshold, ActionWindow: attentionActionWindow,
		ErrorScope:         "distinct native call IDs for the same recorded tool and turn, among the last 20 observed tool actions; native outcomes ordered by their observation",
		RecoveryConditions: []string{"explicit native not_error/completed outcome for that tool and turn", "next observed action for that tool in a different recorded turn", "errors leave the 20-action window"},
		PermissionClosure:  "one recorded request and one response with the same explicit native request_id/approval_id in the same session and intervention family; no adjacency or event-ID fallback",
		QuestionRule:       "a final original assistant text ending in ? or ？ is a hint only; it does not establish waiting for input",
	}
}

func attentionSelector(q snapshot.TrajectorySelector) (snapshot.TrajectorySelector, error) {
	if q.Collection != "" && q.Collection != "attention" {
		return q, ErrInvalid
	}
	if q.Text != "" || q.Skill != "" || q.Role != "" || q.ActorID != "" || q.ActorKind != "" || q.RelationKind != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != "" || q.ContextID != "" || q.ContextScope != "" {
		return q, fmt.Errorf("%w: attention selector not available", ErrInvalid)
	}
	if q.Limit == 0 {
		q.Limit = 20
	}
	if q.Limit < 1 || q.Limit > 50 || len(q.Agent) > 80 || len(q.SessionID) > 256 || len(q.Tool) > 256 || len(q.Cursor) > 512 {
		return q, ErrInvalid
	}
	if q.Kind != "" && q.Kind != "waiting_permission" && q.Kind != "waiting_input" && q.Kind != "stuck_candidate" && q.Kind != "question_hint" {
		return q, ErrInvalid
	}
	if q.State != "" && q.State != "open_observed" && q.State != "closed_observed" && q.State != "unknown" && q.State != "candidate" && q.State != "hint" && q.State != "unavailable" {
		return q, ErrInvalid
	}
	q.Collection = "attention"
	return q, nil
}

func attentionMatches(a snapshot.TrajectoryAttention, q snapshot.TrajectorySelector) bool {
	if q.Kind == "" && q.State == "" && q.Tool == "" {
		return true
	}
	// Combined predicates must refer to the same intervention.
	for _, intervention := range a.Interventions {
		if attentionInterventionMatches(intervention, q) {
			return true
		}
	}
	return false
}

func attentionInterventionMatches(i snapshot.TrajectoryIntervention, q snapshot.TrajectorySelector) bool {
	return (q.Kind == "" || q.Kind == i.Kind) && (q.State == "" || q.State == i.Status) && (q.Tool == "" || strings.EqualFold(q.Tool, i.Tool))
}

func attentionRef(e snapshot.TrajectoryEvent, nativeField string) snapshot.TrajectoryAttentionReference {
	source := e.Source
	source.NativeType = shorten(source.NativeType, 160)
	return snapshot.TrajectoryAttentionReference{EventID: e.ID, NativeField: nativeField, Source: source}
}

type attentionTurn struct {
	turn         snapshot.TrajectoryObservedTurn
	starts, ends int
	aborted      bool
}

type attentionRequest struct {
	id, requestID, kind, turn string
	requests, responses       int
	evidence                  []snapshot.TrajectoryAttentionReference
}

type attentionAction struct {
	call, tool, turn string
	calls, outcomes  int
	seq              int
	outcome          string
	ref              snapshot.TrajectoryAttentionReference
}

type attentionBuilder struct {
	attention                          snapshot.TrajectoryAttention
	turns                              []*attentionTurn
	requests                           []*attentionRequest
	actions                            []*attentionAction
	lastAssistant                      *snapshot.TrajectoryEvent
	events, seq                        int
	turnLimit, requestLimit, scanLimit bool
}

func (b *attentionBuilder) lifecycle(e snapshot.TrajectoryEvent) {
	if e.Kind != "task_started" && e.Kind != "task_complete" && e.Kind != "turn_completed" && e.Kind != "turn_aborted" {
		return
	}
	var current *attentionTurn
	if e.TurnID != "" && len(e.TurnID) <= 256 {
		for _, turn := range b.turns {
			if turn.turn.TurnID == e.TurnID {
				current = turn
				break
			}
		}
	}
	if current == nil {
		id := e.TurnID
		if len(id) > 256 {
			id = ""
		}
		current = &attentionTurn{turn: snapshot.TrajectoryObservedTurn{TurnID: id, Status: "unknown", Starts: []snapshot.TrajectoryAttentionReference{}, Ends: []snapshot.TrajectoryAttentionReference{}}}
		if len(b.turns) == maxAttentionTurns {
			b.turns = b.turns[1:]
			b.turnLimit = true
			b.attention.Coverage.Omitted++
		}
		b.turns = append(b.turns, current)
	}
	if e.TurnID == "" || len(e.TurnID) > 256 {
		gap(&b.attention.Coverage, "attention_turn_reference_unavailable")
	}
	ref := attentionRef(e, "")
	if e.Kind == "task_started" {
		current.starts++
		if len(current.turn.Starts) < 2 {
			current.turn.Starts = append(current.turn.Starts, ref)
		}
	} else {
		current.ends++
		current.aborted = e.Kind == "turn_aborted"
		if len(current.turn.Ends) < 2 {
			current.turn.Ends = append(current.turn.Ends, ref)
		}
	}
}

func attentionFamily(kind string) string {
	if kind == "permission_request" || kind == "permission_response" {
		return "waiting_permission"
	}
	if kind == "waiting_input" || kind == "input_response" {
		return "waiting_input"
	}
	return ""
}

func (b *attentionBuilder) intervention(e snapshot.TrajectoryEvent) {
	kind := attentionFamily(e.Kind)
	if kind == "" {
		return
	}
	request := e.Kind == "permission_request" || e.Kind == "waiting_input"
	id, nativeField := "", ""
	if e.Attention != nil {
		id, nativeField = e.Attention.ResponseToID, e.Attention.ResponseToField
		if request {
			id, nativeField = e.Attention.RequestID, e.Attention.RequestIDField
		}
	}
	valid := id != "" && len(id) <= 256 && nativeField != "" && len(nativeField) <= 160
	// A request_id and an approval_id with equal string values are separate
	// identifier families. Neither a response's own ID nor call ID is a reply.
	namespace := nativeField[strings.LastIndex(nativeField, "/")+1:]
	key := kind + "\x00" + namespace + "\x00" + id
	if !valid {
		key = e.ID
		id, nativeField = "", ""
		gap(&b.attention.Coverage, "attention_request_reference_unavailable")
	}
	var current *attentionRequest
	for _, candidate := range b.requests {
		if candidate.id == key {
			current = candidate
			break
		}
	}
	if current == nil {
		turn := e.TurnID
		if len(turn) > 256 {
			turn = ""
			gap(&b.attention.Coverage, "attention_turn_reference_unavailable")
		}
		current = &attentionRequest{id: key, requestID: id, kind: kind, turn: turn, evidence: []snapshot.TrajectoryAttentionReference{}}
		if len(b.requests) == maxAttentionRequests {
			b.requests = b.requests[1:]
			b.requestLimit = true
			b.attention.Coverage.Omitted++
		}
		b.requests = append(b.requests, current)
	}
	if request {
		current.requests++
	} else {
		current.responses++
	}
	if len(current.evidence) < 4 {
		current.evidence = append(current.evidence, attentionRef(e, nativeField))
	}
}

func nativeAttentionOutcome(e snapshot.TrajectoryEvent) string {
	if e.Attention == nil || e.Attention.OutcomeField == "" || len(e.Attention.OutcomeField) > 160 {
		return ""
	}
	switch strings.ToLower(e.Outcome) {
	case "error", "failed":
		return "error"
	case "not_error", "completed":
		return "not_error"
	}
	return ""
}

func (b *attentionBuilder) action(e snapshot.TrajectoryEvent) {
	if e.Kind != "tool_call" && e.Kind != "tool_result" && e.Kind != "tool_error" && e.Kind != "tool_update" {
		return
	}
	latest := attentionRef(e, "")
	if e.Attention != nil {
		latest.NativeField = e.Attention.OutcomeField
		if len(latest.NativeField) > 160 {
			latest.NativeField = ""
			gap(&b.attention.Coverage, "attention_outcome_field_unavailable")
		}
	}
	b.attention.LatestAction = &latest
	b.seq++
	call, tool := "", ""
	if e.Tool != nil {
		call, tool = e.Tool.CallID, e.Tool.Name
	}
	if len(call) > 256 || len(tool) > 256 || len(e.TurnID) > 256 {
		gap(&b.attention.Coverage, "attention_action_reference_unavailable")
		call, tool = "", ""
	}
	var current *attentionAction
	if call != "" {
		for _, action := range b.actions {
			if action.call == call {
				current = action
				break
			}
		}
	}
	if current == nil {
		current = &attentionAction{call: call, tool: tool, turn: e.TurnID, seq: b.seq, ref: latest}
		if len(b.actions) == attentionActionWindow {
			b.actions = b.actions[1:]
		}
		b.actions = append(b.actions, current)
	}
	if tool != "" {
		if current.tool != "" && current.tool != tool {
			current.calls = 2 // conflicting native descriptions are ambiguous
		}
		current.tool = tool
	}
	if e.TurnID != "" && len(e.TurnID) <= 256 {
		if current.turn != "" && current.turn != e.TurnID {
			current.calls = 2
		}
		current.turn = e.TurnID
	}
	if e.Kind == "tool_call" {
		current.calls++
	}
	if outcome := nativeAttentionOutcome(e); outcome != "" {
		current.outcomes++
		current.outcome, current.seq, current.ref = outcome, b.seq, latest
		if call == "" || current.tool == "" || current.turn == "" {
			gap(&b.attention.Coverage, "attention_error_scope_unavailable")
		}
	}
}

func (b *attentionBuilder) visit(e snapshot.TrajectoryEvent) bool {
	if b.events == maxAttentionScanEvents {
		b.scanLimit = true
		gap(&b.attention.Coverage, "attention_scan_limit")
		return false
	}
	b.events++
	b.lifecycle(e)
	b.intervention(e)
	b.action(e)
	if e.Role == "assistant" && (e.Kind == "text" || e.Kind == "message" || e.Kind == "output_observed") {
		// Only the source locator is needed until the final original block is
		// read; neither cleaned text nor neighboring blocks decide this rule.
		last := snapshot.TrajectoryEvent{ID: e.ID, Kind: e.Kind, Source: e.Source}
		b.lastAssistant = &last
	}
	return true
}

func (b *attentionBuilder) finish(sourceComplete bool) {
	a := &b.attention
	if b.turnLimit {
		gap(&a.Coverage, "attention_turn_limit")
	}
	if b.requestLimit {
		gap(&a.Coverage, "attention_request_limit")
	}
	a.Progress.Status = "unknown"
	if len(b.turns) == 0 {
		gap(&a.Coverage, "attention_lifecycle_unavailable")
	} else {
		a.Progress.Status = "observed"
		lifecycleIncomplete := false
		for _, current := range b.turns {
			turn := current.turn
			switch {
			case turn.TurnID == "" || current.starts != 1 || current.ends > 1:
				turn.Status = "unknown"
				lifecycleIncomplete = true
				gap(&a.Coverage, "attention_turn_boundary_ambiguous")
			case current.ends == 0:
				turn.Status = "open_observed"
				if !sourceComplete || b.scanLimit || b.turnLimit {
					turn.Status = "unknown"
				}
			case current.aborted:
				turn.Status = "aborted_observed"
			default:
				turn.Status = "closed_observed"
			}
			a.Progress.Turns = append(a.Progress.Turns, turn)
		}
		if !sourceComplete || b.scanLimit || b.turnLimit || lifecycleIncomplete {
			a.Progress.Status = "partial"
		}
	}
	for _, current := range b.requests {
		id := current.requestID
		intervention := snapshot.TrajectoryIntervention{ID: "att." + digest([]byte(a.SessionID+"\x00"+current.id)), Kind: current.kind, RequestID: id, TurnID: current.turn, Status: "unknown", Evidence: current.evidence, Omissions: []string{}}
		switch {
		case id == "":
			intervention.Omissions = append(intervention.Omissions, "native_request_reference_unavailable")
		case current.requests != 1 || current.responses > 1:
			intervention.Omissions = append(intervention.Omissions, "request_not_observed_or_ambiguous")
			gap(&a.Coverage, "attention_request_pair_unavailable")
		case b.requestLimit || b.scanLimit || !sourceComplete:
			intervention.Status = "unavailable"
			intervention.Omissions = append(intervention.Omissions, "source_or_request_coverage_incomplete")
		case current.responses == 1:
			intervention.Status = "closed_observed"
		default:
			intervention.Status = "open_observed"
		}
		a.Interventions = append(a.Interventions, intervention)
	}
	// Outcomes can arrive concurrently and in a different order from calls.
	// Recovery follows observed native outcomes, never launch adjacency.
	ordered := append([]*attentionAction{}, b.actions...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].seq < ordered[j].seq })
	type failures struct {
		turn      string
		refs      []snapshot.TrajectoryAttentionReference
		uncertain bool
	}
	byTool := map[string]*failures{}
	toolOrder := []string{}
	for _, action := range ordered {
		if action.tool == "" || action.turn == "" {
			// A newer action with an unavailable scope cannot establish that a
			// previous error run remains in the same scope. Earlier unknown
			// actions do not erase later exact native evidence.
			if action.tool == "" {
				for _, current := range byTool {
					current.uncertain = true
					gap(&a.Coverage, "attention_action_scope_unavailable")
				}
			} else if current := byTool[action.tool]; current != nil {
				current.uncertain = true
				gap(&a.Coverage, "attention_action_scope_unavailable")
			}
			continue
		}
		current := byTool[action.tool]
		if current == nil {
			current = &failures{turn: action.turn}
			byTool[action.tool] = current
			toolOrder = append(toolOrder, action.tool)
		}
		if current.turn != action.turn {
			current.turn, current.refs = action.turn, nil
			current.uncertain = false
		}
		if action.calls > 1 || action.outcomes > 1 || action.call == "" {
			gap(&a.Coverage, "attention_action_identity_ambiguous")
			current.refs = nil
			continue
		}
		switch action.outcome {
		case "not_error":
			current.refs = nil
			current.uncertain = false
		case "error":
			current.refs = append(current.refs, action.ref)
		}
	}
	for _, tool := range toolOrder {
		current := byTool[tool]
		if len(current.refs) < attentionErrorThreshold {
			continue
		}
		status, omissions := "candidate", []string{}
		if !sourceComplete || b.scanLimit || current.uncertain {
			status, omissions = "unavailable", []string{"source_coverage_incomplete"}
			if current.uncertain && sourceComplete && !b.scanLimit {
				omissions = []string{"action_scope_coverage_incomplete"}
			}
		}
		refs := current.refs[max(0, len(current.refs)-attentionErrorThreshold):]
		a.Interventions = append(a.Interventions, snapshot.TrajectoryIntervention{ID: "att." + digest([]byte(a.SessionID+"\x00error\x00"+tool+"\x00"+current.turn)), Kind: "stuck_candidate", Status: status, Tool: tool, TurnID: current.turn, Count: len(current.refs), Evidence: refs, Omissions: omissions})
	}
	if len(a.Interventions) > maxAttentionInterventions {
		a.Coverage.Omitted += len(a.Interventions) - maxAttentionInterventions
		a.Interventions = a.Interventions[len(a.Interventions)-maxAttentionInterventions:]
		gap(&a.Coverage, "attention_intervention_limit")
	}
}

func attentionProjection(ctx context.Context, st *sourceState, global snapshot.TrajectoryCoverage) (snapshot.TrajectoryAttention, error) {
	b := attentionBuilder{attention: snapshot.TrajectoryAttention{
		ID: sessionID(st), SessionID: sessionID(st), Agent: st.Agent, Liveness: "unknown", Coverage: coverage("attention:" + sessionID(st)),
		Progress: snapshot.TrajectoryExecutionProgress{Status: "unknown", Turns: []snapshot.TrajectoryObservedTurn{}}, Interventions: []snapshot.TrajectoryIntervention{},
	}}
	mergeCoverage(&b.attention.Coverage, global)
	local, err := scan(ctx, st, b.visit)
	if err != nil {
		return snapshot.TrajectoryAttention{}, err
	}
	mergeCoverage(&b.attention.Coverage, local)
	b.finish(global.Complete && local.Complete)
	if b.lastAssistant != nil && !b.scanLimit {
		raw, err := readRecord(st, b.lastAssistant.Source)
		if err != nil {
			return snapshot.TrajectoryAttention{}, err
		} else {
			record := object(raw)
			text, field := entityNativeText(record, object(record["payload"]), *b.lastAssistant, st.Agent)
			text = strings.TrimSpace(text)
			if strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？") {
				b.attention.Interventions = append(b.attention.Interventions, snapshot.TrajectoryIntervention{ID: "att." + digest([]byte(b.lastAssistant.ID+"\x00question")), Kind: "question_hint", Status: "hint", Evidence: []snapshot.TrajectoryAttentionReference{attentionRef(*b.lastAssistant, field)}})
			}
		}
	}
	if len(b.attention.Interventions) > maxAttentionInterventions {
		b.attention.Interventions = b.attention.Interventions[1:]
		b.attention.Coverage.Omitted++
		gap(&b.attention.Coverage, "attention_intervention_limit")
	}
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryAttention{}, err
	}
	return b.attention, nil
}

// Trimming affects projection coverage, not the meaning of retained native
// facts. The complete source evidence remains navigable through its event IDs.
func trimAttention(a *snapshot.TrajectoryAttention) bool {
	return trimAttentionPreserving(a, "")
}

func trimAttentionPreserving(a *snapshot.TrajectoryAttention, interventionID string) bool {
	if len(a.Progress.Turns) > 0 {
		a.Progress.Turns = a.Progress.Turns[1:]
	} else if len(a.Interventions) > 0 {
		remove := -1
		for i, intervention := range a.Interventions {
			if intervention.ID != interventionID {
				remove = i
				break
			}
		}
		if remove == -1 {
			if a.LatestAction == nil {
				return false
			}
			a.LatestAction = nil
		} else {
			a.Interventions = append(a.Interventions[:remove], a.Interventions[remove+1:]...)
		}
	} else if a.LatestAction != nil {
		a.LatestAction = nil
	} else {
		return false
	}
	a.Coverage.Omitted++
	gap(&a.Coverage, "attention_projection_limit")
	if a.Progress.Status == "observed" {
		a.Progress.Status = "partial"
	}
	return true
}

// QueryAttention is an authenticated content-domain projection. Transports
// delegate here; resource metrics and public diagnostic exports do not use it.
func (s *Service) QueryAttention(ctx context.Context, q snapshot.TrajectorySelector) (snapshot.TrajectoryAttentionQueryResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryAttentionQueryResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryAttentionQueryResult{Attention: []snapshot.TrajectoryAttention{}, Rules: attentionRules()}
	var err error
	if q, err = attentionSelector(q); err != nil {
		return out, err
	}
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryAttentionQueryResult{}, err
	}
	out.Coverage, out.Revision = cov, sourceRevision(states)
	pagination := q
	pagination.Cursor, pagination.Limit = "", 0
	selector, _ := json.Marshal(pagination)
	prefix := "attention." + out.Revision + "." + digest(selector)
	start := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		if len(parts) != 2 || parts[0] != prefix {
			return out, ErrStale
		}
		if start, err = strconv.Atoi(parts[1]); err != nil || start < 0 {
			return out, ErrInvalid
		}
	}
	matched := 0
	for _, st := range states {
		if (q.Agent != "" && q.Agent != st.Agent) || (q.SessionID != "" && q.SessionID != sessionID(st)) {
			continue
		}
		a, err := attentionProjection(ctx, st, cov)
		if err != nil {
			return snapshot.TrajectoryAttentionQueryResult{}, err
		}
		local := a.Coverage
		local.Omitted = max(0, local.Omitted-cov.Omitted)
		mergeCoverage(&out.Coverage, local)
		if !attentionMatches(a, q) {
			continue
		}
		matched++
		if matched <= start {
			continue
		}
		if len(out.Attention) == q.Limit {
			out.Next = fmt.Sprintf("%s:%d", prefix, start+len(out.Attention))
			break
		}
		preserve := ""
		if q.Kind != "" || q.State != "" || q.Tool != "" {
			for _, intervention := range a.Interventions {
				if attentionInterventionMatches(intervention, q) {
					preserve = intervention.ID
					break
				}
			}
		}
		before := a.Coverage.Omitted
		for encoded, _ := json.Marshal(a); len(encoded) > maxAttentionProjectionBytes && trimAttentionPreserving(&a, preserve); encoded, _ = json.Marshal(a) {
		}
		if a.Coverage.Omitted > before {
			gap(&out.Coverage, "attention_projection_limit")
			out.Coverage.Omitted += a.Coverage.Omitted - before
		}
		out.Attention = append(out.Attention, a)
		if encoded, _ := json.Marshal(out); len(encoded) > maxAttentionQueryBytes-512 {
			out.Attention = out.Attention[:len(out.Attention)-1]
			gap(&out.Coverage, "attention_result_byte_limit")
			out.Next = fmt.Sprintf("%s:%d", prefix, start+len(out.Attention))
			break
		}
	}
	return out, nil
}

func (s *Service) GetAttention(ctx context.Context, p snapshot.TrajectoryGetParams) (snapshot.TrajectoryAttentionGetResult, error) {
	if err := s.lockOperation(ctx); err != nil {
		return snapshot.TrajectoryAttentionGetResult{}, err
	}
	defer s.opMu.Unlock()
	out := snapshot.TrajectoryAttentionGetResult{FocusID: p.ID, ByteLimit: MaxSliceBytes, Rules: attentionRules()}
	if p.ID == "" || len(p.ID) > 256 || p.Raw || p.RawOffset != 0 || p.MemberOffset != 0 || p.Around != 0 || p.MaxBytes < 0 || p.MaxBytes > MaxSliceBytes || (p.View != "" && p.View != "attention") {
		return out, ErrInvalid
	}
	if p.MaxBytes != 0 {
		out.ByteLimit = p.MaxBytes
	}
	if out.ByteLimit < 1024 {
		return out, ErrInvalid
	}
	parts := strings.Split(p.ID, ".")
	if (len(parts) != 3 || parts[0] != "s") && (len(parts) != 6 || parts[0] != "e") {
		return out, ErrInvalid
	}
	states, cov := s.collect(ctx)
	if err := ctx.Err(); err != nil {
		return snapshot.TrajectoryAttentionGetResult{}, err
	}
	out.Coverage, out.Revision = cov, sourceRevision(states)
	var st *sourceState
	for _, candidate := range states {
		if candidate.ID == parts[1] {
			st = candidate
			break
		}
	}
	if st == nil {
		return out, ErrNotFound
	}
	if st.Generation != parts[2] {
		return out, ErrStale
	}
	var offset int64
	var block int
	if parts[0] == "e" {
		var err error
		if offset, err = strconv.ParseInt(parts[3], 36, 64); err != nil || offset < 0 {
			return out, ErrInvalid
		}
		if block, err = strconv.Atoi(parts[4]); err != nil || block < 0 {
			return out, ErrInvalid
		}
	}
	if parts[0] == "e" || st.checkpoint.EventCount != 0 {
		if _, _, _, err := s.indexedWindow(ctx, st, p, offset, block); err != nil {
			return out, err
		}
	}
	a, err := attentionProjection(ctx, st, cov)
	if err != nil {
		return snapshot.TrajectoryAttentionGetResult{}, err
	}
	out.Attention = &a
	local := a.Coverage
	local.Omitted = max(0, local.Omitted-cov.Omitted)
	mergeCoverage(&out.Coverage, local)
	for {
		encoded, _ := json.Marshal(out)
		if len(encoded) <= out.ByteLimit {
			return out, nil
		}
		if !trimAttention(&a) {
			return out, fmt.Errorf("%w: budget cannot include attention provenance", ErrInvalid)
		}
		out.Truncated = true
		gap(&out.Coverage, "attention_projection_limit")
		out.Coverage.Omitted++
	}
}
