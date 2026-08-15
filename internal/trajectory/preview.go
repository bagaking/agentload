package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"fmt"
)

// Bounds apply only to transport projections. Canonical locators and the
// indexed native evidence stay intact; omitted native identifiers are never
// shortened into a different identifier. Raw byte reads remain available.
func boundedEventPreview(e snapshot.TrajectoryEvent) snapshot.TrajectoryEvent {
	e = omitEntities(e)
	omit := func(field *string, name string, limit int) {
		if len(*field) > limit {
			*field = ""
			e.Omissions = append(e.Omissions, name+"_omitted_for_size")
		}
	}
	omit(&e.NativeID, "native_id", 256)
	omit(&e.NativeEnvelopeID, "native_envelope_id", 256)
	omit(&e.ProtocolRole, "protocol_role", 80)
	omit(&e.Role, "role", 80)
	omit(&e.Kind, "kind", 80)
	omit(&e.Actor.Kind, "actor_kind", 80)
	omit(&e.TurnID, "turn_id", 256)
	omit(&e.Actor.ID, "actor_id", 256)
	omit(&e.Outcome, "outcome", 80)
	omit(&e.Source.NativeType, "native_type", 160)
	if e.Tool != nil {
		tool := *e.Tool
		e.Tool = &tool
		omit(&e.Tool.CallID, "call_id", 256)
		omit(&e.Tool.Name, "tool_name", 256)
	}
	fits := func(value any, n int) bool { b, err := json.Marshal(value); return err == nil && len(b) <= n }
	if !fits(e.Evidence, 640) {
		e.Evidence = nil
		e.Omissions = append(e.Omissions, "native_evidence_in_raw_view")
	}
	if !fits(e.Context, 512) {
		e.Context = nil
		e.Omissions = append(e.Omissions, "context_in_context_view")
	}
	if !fits(e.Attention, 384) {
		e.Attention = nil
		e.Omissions = append(e.Omissions, "attention_in_attention_view")
	}
	if !fits(e.Workspace, 384) {
		e.Workspace = nil
		e.Omissions = append(e.Omissions, "workspace_in_context_view")
	}
	e.Usage = boundedUsage(e.Usage, &e.Omissions)
	if len(e.Omissions) > 16 {
		e.Omissions = append(append([]string{}, e.Omissions[:15]...), "additional_omissions")
	}
	for i := range e.Omissions {
		e.Omissions[i] = shorten(e.Omissions[i], 160)
	}
	return e
}

const maxQueryBytes = 64 * 1024

// Continuation advances by the number actually returned, including byte-bound
// pages. It must not skip the entries removed to fit a response.
func boundQueryPage(out *snapshot.TrajectoryQueryResult, prefix string, start, matched int) error {
	count := func() int { return len(out.Events) + len(out.Sessions) }
	for {
		n := count()
		out.Next = ""
		if matched > start+n {
			out.Next = fmt.Sprintf("%s:%d", prefix, start+n)
		}
		encoded, _ := json.Marshal(out)
		if len(encoded) <= maxQueryBytes {
			return nil
		}
		if n <= 1 {
			return fmt.Errorf("%w: query budget cannot include required provenance", ErrInvalid)
		}
		if len(out.Events) > 0 {
			out.Events = out.Events[:len(out.Events)-1]
		} else {
			out.Sessions = out.Sessions[:len(out.Sessions)-1]
		}
		gap(&out.Coverage, "query_page_byte_limit")
	}
}
