package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
)

const experienceRuleID = "recorded-action-outcomes"
const experienceRuleVersion = "1"
const maxExperienceCandidates = 256
const maxExperienceActions = 512
const maxExperienceEvents = 50000
const maxExperienceIndexBytes = 8 * 1024 * 1024
const maxExperienceReferences = 4096
const maxExperienceRawBytes = 4 * 1024 * 1024
const maxExperienceRecordBytes = 64 * 1024

type experienceAction struct {
	call, result snapshot.TrajectoryEvent
	source       *sourceState
	canonical    string
	outcome      string
}

type experienceReference struct {
	record    string
	actions   []string
	ambiguous bool
}

func experienceActionKey(st *sourceState, call string) string {
	return st.ID + "." + st.Generation + "." + call
}

func experienceReferenceKey(st *sourceState, kind, id string) string {
	return st.ID + "\x00" + st.Generation + "\x00" + kind + "\x00" + id
}

// JSON object order and formatting are immaterial; number spellings, strings
// (including presentation wrappers), and array order remain distinct. Duplicate
// keys, excessive nesting, and absent arguments cannot establish equivalence.
func canonicalExperienceArguments(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 || len(raw) > 4096 {
		return "", false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(int) (any, error)
	tokens := 0
	read = func(depth int) (any, error) {
		if depth > 16 || tokens > 512 {
			return nil, ErrInvalid
		}
		token, err := d.Token()
		tokens++
		if err != nil {
			return nil, err
		}
		delim, compound := token.(json.Delim)
		if !compound {
			return token, nil
		}
		switch delim {
		case '{':
			m := map[string]any{}
			for d.More() {
				key, err := d.Token()
				tokens++
				name, ok := key.(string)
				if err != nil || !ok {
					return nil, ErrInvalid
				}
				if _, exists := m[name]; exists {
					return nil, ErrInvalid
				}
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				m[name] = value
			}
			if end, err := d.Token(); err != nil || end != json.Delim('}') {
				return nil, ErrInvalid
			}
			return m, nil
		case '[':
			values := []any{}
			for d.More() {
				value, err := read(depth + 1)
				if err != nil {
					return nil, err
				}
				values = append(values, value)
			}
			if end, err := d.Token(); err != nil || end != json.Delim(']') {
				return nil, ErrInvalid
			}
			return values, nil
		}
		return nil, ErrInvalid
	}
	value, err := read(0)
	if err != nil || value == nil {
		return "", false
	}
	if _, err := d.Token(); err != io.EOF {
		return "", false
	}
	canonical, err := json.Marshal(value)
	return string(canonical), err == nil
}

func (s *Service) experienceNative(ctx context.Context, st *sourceState, indexed snapshot.TrajectoryEvent, rawBytes *int, cov *snapshot.TrajectoryCoverage) (snapshot.TrajectoryEvent, bool) {
	if indexed.Source.Length > maxExperienceRecordBytes || indexed.Source.Length <= 0 || *rawBytes+indexed.Source.Length > maxExperienceRawBytes {
		gap(cov, "experience_raw_evidence_limit")
		return snapshot.TrajectoryEvent{}, false
	}
	if ctx.Err() != nil {
		return snapshot.TrajectoryEvent{}, false
	}
	*rawBytes += indexed.Source.Length
	raw, err := readRecord(st, indexed.Source)
	if err != nil {
		gap(cov, "experience_source_changed")
		return snapshot.TrajectoryEvent{}, false
	}
	decoded, err := st.Decoder.Decode(raw, DecodeContext{SessionID: indexed.SessionID})
	if err != nil || indexed.Source.Block < 0 || indexed.Source.Block >= len(decoded) {
		gap(cov, "experience_native_evidence_unavailable")
		return snapshot.TrajectoryEvent{}, false
	}
	e := decoded[indexed.Source.Block]
	e.ID, e.SessionID, e.Source = indexed.ID, indexed.SessionID, indexed.Source
	e.Text, e.Raw, e.Entities, e.EntityCoverage, e.Workspace, e.Usage = "", "", nil, nil, nil, nil
	return e, true
}

func experienceStep(a experienceAction) snapshot.TrajectoryKnowledgeStep {
	return snapshot.TrajectoryKnowledgeStep{CallEventID: a.call.ID, ResultEventID: a.result.ID, Tool: a.call.Tool.Name, Outcome: a.outcome, OutcomeField: a.result.Attention.OutcomeField}
}

func experienceRecord(first, second experienceAction, basis string, relation *snapshot.TrajectoryKnowledgeRelation) snapshot.TrajectoryKnowledge {
	pattern, text := "outcome_change", "A recorded tool error was followed by a recorded not_error outcome for the same action. Causality and task completion are unproven."
	if second.outcome == "error" {
		pattern, text = "repeated_error", "The same recorded action had two distinct tool-error outcomes. This observation provides no validated remedy."
	}
	if basis == "native_relation" {
		text = "Native-related actions of the same tool had a recorded error followed by a recorded not_error outcome. The changed arguments are evidence of an observed sequence; causality and task completion are unproven."
		if pattern == "repeated_error" {
			text = "Native-related actions of the same tool had two recorded error outcomes. This observation provides no validated remedy."
		}
	}
	scope := snapshot.TrajectoryKnowledgeScope{Kind: "source_session", ID: first.call.SessionID}
	if first.source.ID != second.source.ID {
		scope = snapshot.TrajectoryKnowledgeScope{Kind: "native_relation", ID: digest([]byte(first.call.ID + ":" + second.call.ID))}
	} else if first.call.TurnID != "" && first.call.TurnID == second.call.TurnID {
		scope = snapshot.TrajectoryKnowledgeScope{Kind: "recorded_turn", ID: first.call.TurnID}
	}
	identity, _ := json.Marshal([]any{experienceRuleID, experienceRuleVersion, first.call.ID, first.result.ID, second.call.ID, second.result.ID, basis, relation})
	hash := sha256.Sum256(identity)
	r := snapshot.TrajectoryKnowledge{ID: "k." + hex.EncodeToString(hash[:16]), Kind: "candidate", State: "active", Origin: "deterministic", RuleID: experienceRuleID, RuleVersion: experienceRuleVersion, Text: text, EvidenceState: "valid", ScopeStatus: "unprovided", Sources: []snapshot.TrajectoryKnowledgeSource{}}
	r.Experience = &snapshot.TrajectoryKnowledgeExperience{Pattern: pattern, Basis: basis, Scope: scope, Steps: []snapshot.TrajectoryKnowledgeStep{experienceStep(first), experienceStep(second)}, Relation: relation, Causality: "unproven", TaskCompletion: "unproven", Verification: "unprovided"}
	for _, a := range []experienceAction{first, second} {
		for _, e := range []snapshot.TrajectoryEvent{a.call, a.result} {
			r.Sources = append(r.Sources, snapshot.TrajectoryKnowledgeSource{EventID: e.ID, SessionID: e.SessionID, Agent: a.source.Agent, Source: e.Source, Status: "valid"})
		}
	}
	if first.call.Timestamp != nil {
		r.CreatedAt = first.call.Timestamp
	}
	if second.result.Timestamp != nil {
		r.UpdatedAt = second.result.Timestamp
	}
	return r
}

// deriveExperiences runs under opMu against one authorized source snapshot.
// Only source coordinates and a bounded argument equivalence key are retained;
// source bodies are validated and decoded locally without being persisted.
func (s *Service) deriveExperiences(ctx context.Context, states []*sourceState, cov *snapshot.TrajectoryCoverage) ([]snapshot.TrajectoryKnowledge, error) {
	if s.store == nil {
		return nil, nil
	}
	actions := []experienceAction{}
	references := map[string]experienceReference{}
	referenceComplete, scanned, rawBytes, indexBytes := true, 0, 0, 0
	var err error
	for _, st := range states {
		if len(st.Agent) > 80 {
			gap(cov, "experience_native_identity_limit")
			continue
		}
		if !st.checkpoint.Coverage.Complete {
			gap(cov, "experience_source_coverage_incomplete")
			continue
		}
		err = s.store.walk(ctx, st, func(e snapshot.TrajectoryEvent, logicalSize int) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if scanned == maxExperienceEvents {
				gap(cov, "experience_event_scan_limit")
				referenceComplete = false
				return io.EOF
			}
			if indexBytes+logicalSize > maxExperienceIndexBytes {
				gap(cov, "experience_index_byte_limit")
				referenceComplete = false
				return io.EOF
			}
			indexBytes += logicalSize
			scanned++
			actionKey := ""
			if e.Tool != nil && e.Tool.CallID != "" && len(e.Tool.CallID) <= 256 && (e.Kind == "tool_call" || e.Kind == "tool_result") {
				actionKey = experienceActionKey(st, e.Tool.CallID)
			}
			for _, ref := range [][2]string{{"event", e.ID}, {"native_id", e.NativeID}, {"native_envelope", e.NativeEnvelopeID}, {"call", func() string {
				if e.Kind == "tool_call" && e.Tool != nil {
					return e.Tool.CallID
				}
				return ""
			}()}} {
				if ref[1] == "" || len(ref[1]) > 256 {
					continue
				}
				key := experienceReferenceKey(st, ref[0], ref[1])
				known, exists := references[key]
				if !exists && len(references) == maxExperienceReferences {
					referenceComplete = false
					gap(cov, "experience_reference_limit")
					continue
				}
				record := e.Source.ID + "." + e.Source.Generation + "." + e.Source.Digest + "." + string(eventKey(e.Source.Offset, 0))
				if exists && known.record != record {
					known.ambiguous = true
				}
				known.record = record
				if actionKey != "" {
					found := false
					for _, id := range known.actions {
						if id == actionKey {
							found = true
						}
					}
					if !found {
						if len(known.actions) < 2 {
							known.actions = append(known.actions, actionKey)
						} else {
							known.ambiguous = true
						}
					}
				}
				references[key] = known
			}
			if e.Kind != "tool_call" || e.Tool == nil || e.Tool.CallID == "" || len(e.Tool.CallID) > 256 || strings.TrimSpace(e.Tool.Name) == "" || len(e.Tool.Name) > 256 {
				if e.Tool != nil && (len(e.Tool.CallID) > 256 || len(e.Tool.Name) > 256) {
					gap(cov, "experience_native_identity_limit")
				}
				return nil
			}
			if len(e.TurnID) > 256 || len(e.Source.NativeType) > 160 {
				gap(cov, "experience_native_identity_limit")
				return nil
			}
			if len(actions) == maxExperienceActions {
				gap(cov, "experience_action_limit")
				return nil
			}
			pair, pairErr := s.store.pair(ctx, st, e.Tool.CallID)
			if pairErr != nil || len(pair.Calls) != 1 || len(pair.Results) != 1 || pair.Calls[0] != e.ID {
				return nil
			}
			_, offset, block, err := knowledgeEventLocator(pair.Results[0])
			if err != nil {
				return nil
			}
			result, err := s.store.event(ctx, st, offset, block)
			if err != nil || result.ID != pair.Results[0] || result.Tool == nil || result.Tool.CallID != e.Tool.CallID {
				return nil
			}
			if len(result.TurnID) > 256 || len(result.Source.NativeType) > 160 {
				gap(cov, "experience_native_identity_limit")
				return nil
			}
			call, ok := s.experienceNative(ctx, st, e, &rawBytes, cov)
			if !ok {
				return nil
			}
			result, ok = s.experienceNative(ctx, st, result, &rawBytes, cov)
			if !ok {
				return nil
			}
			if call.Kind != "tool_call" || call.Tool == nil || call.Tool.CallID != e.Tool.CallID || result.Kind != "tool_result" || result.Tool == nil || result.Tool.CallID != call.Tool.CallID {
				return nil
			}
			canonical, ok := canonicalExperienceArguments(call.Tool.Arguments)
			outcome := nativeAttentionOutcome(result)
			if len(call.Tool.Arguments) > 4096 {
				gap(cov, "experience_arguments_limit")
			}
			if !ok || outcome == "" || len(call.Tool.Name) > 256 || call.Tool.Name == "" {
				return nil
			}
			if call.TurnID != "" && result.TurnID != "" && call.TurnID != result.TurnID {
				gap(cov, "experience_turn_identity_mismatch")
				return nil
			}
			if call.Evidence != nil && len(call.Evidence.Relations) > 16 {
				gap(cov, "experience_relation_evidence_limit")
				call.Evidence = nil
			}
			actions = append(actions, experienceAction{call: call, result: result, source: st, canonical: canonical, outcome: outcome})
			return nil
		})
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if scanned >= maxExperienceEvents || indexBytes >= maxExperienceIndexBytes || errors.Is(err, io.EOF) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	stableActions := actions[:0]
	for _, a := range actions {
		info, err := os.Stat(a.source.Path)
		if err != nil || !os.SameFile(a.source.Info, info) || info.Size() != a.source.Info.Size() || info.ModTime() != a.source.Info.ModTime() {
			gap(cov, "experience_source_changed")
			continue
		}
		stableActions = append(stableActions, a)
	}
	actions = stableActions
	byAction := map[string]int{}
	for i, a := range actions {
		byAction[experienceActionKey(a.source, a.call.Tool.CallID)] = i
	}
	result := []snapshot.TrajectoryKnowledge{}
	seen := map[string]bool{}
	add := func(first, second experienceAction, basis string, relation *snapshot.TrajectoryKnowledgeRelation) {
		if first.outcome != "error" || first.call.ID == second.call.ID || first.source.ID == second.source.ID && (first.call.Source.Offset >= second.call.Source.Offset || first.result.Source.Offset >= second.call.Source.Offset) {
			return
		}
		record := experienceRecord(first, second, basis, relation)
		if seen[record.ID] {
			return
		}
		seen[record.ID] = true
		if len(result) == maxExperienceCandidates {
			gap(cov, "experience_candidate_limit")
			return
		}
		for _, a := range actions {
			if a.source.ID != second.source.ID || a.canonical != second.canonical || a.call.Tool.Name != second.call.Tool.Name || a.result.Source.Offset <= second.result.Source.Offset || a.outcome == second.outcome {
				continue
			}
			if record.Experience.Scope.Kind == "recorded_turn" && a.call.TurnID != record.Experience.Scope.ID {
				continue
			}
			if len(record.Experience.Counterexamples) == 2 {
				record.Omissions = append(record.Omissions, "experience_counterexample_limit")
				break
			}
			record.Experience.Counterexamples = append(record.Experience.Counterexamples, experienceStep(a))
			for _, e := range []snapshot.TrajectoryEvent{a.call, a.result} {
				record.Sources = append(record.Sources, snapshot.TrajectoryKnowledgeSource{EventID: e.ID, SessionID: e.SessionID, Agent: a.source.Agent, Source: e.Source, Status: "valid"})
			}
		}
		for {
			encoded, _ := json.Marshal(record)
			if len(encoded) <= maxKnowledgeRecordBytes {
				break
			}
			if len(record.Experience.Counterexamples) == 0 {
				gap(cov, "experience_candidate_byte_limit")
				return
			}
			record.Experience.Counterexamples = record.Experience.Counterexamples[:len(record.Experience.Counterexamples)-1]
			record.Sources = record.Sources[:len(record.Sources)-2]
			if len(record.Omissions) == 0 || record.Omissions[len(record.Omissions)-1] != "experience_counterexamples_omitted_for_budget" {
				record.Omissions = append(record.Omissions, "experience_counterexamples_omitted_for_budget")
			}
		}
		result = append(result, record)
	}
	last := map[string]int{}
	for i, a := range actions {
		key := a.source.ID + "\x00" + a.source.Generation + "\x00" + a.call.Tool.Name + "\x00" + a.canonical
		if previous, ok := last[key]; ok {
			add(actions[previous], a, "same_recorded_action", nil)
		}
		last[key] = i
		if !referenceComplete || a.call.Evidence == nil {
			continue
		}
		for _, relation := range a.call.Evidence.Relations {
			if relation.Kind != "parent" && relation.Kind != "reply" && relation.Kind != "resume" && relation.Kind != "branch_parent" {
				continue
			}
			if relation.NativeField == "" || len(relation.NativeField) > 160 || !referenceKind(relation.Target.Kind) || relation.Target.Kind == "session" || relation.Target.ID == "" || len(relation.Target.ID) > 256 {
				continue
			}
			matched := -1
			for _, st := range states {
				if !referenceSource(relation.Target, a.source, st) {
					continue
				}
				ref, ok := references[experienceReferenceKey(st, relation.Target.Kind, relation.Target.ID)]
				if !ok {
					continue
				}
				if ref.ambiguous || len(ref.actions) != 1 {
					matched = -2
					break
				}
				candidate, exists := byAction[ref.actions[0]]
				if !exists || matched >= 0 {
					matched = -2
					break
				}
				matched = candidate
			}
			if matched < 0 {
				continue
			}
			previous := actions[matched]
			if previous.source.Agent != a.source.Agent || previous.call.Tool.Name != a.call.Tool.Name || previous.canonical == a.canonical {
				continue
			}
			target := previous.call.ID
			if referenceEvent(relation.Target, previous.result) {
				target = previous.result.ID
			}
			if previous.source.ID != a.source.ID && target != previous.result.ID {
				continue
			}
			add(previous, a, "native_relation", &snapshot.TrajectoryKnowledgeRelation{Kind: relation.Kind, NativeField: relation.NativeField, FromEventID: a.call.ID, TargetEventID: target})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, ctx.Err()
}
