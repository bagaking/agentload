package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// shapeRecord is shared by online indexing and source replay. cwd is the
// recorded state immediately BEFORE this physical line, never a guessed path.
// Updating it between content blocks preserves the existing entity semantics.
func shapeRecord(st *sourceState, line int, offset int64, body []byte, cwd *string) ([]snapshot.TrajectoryEvent, []string) {
	events, gaps, _ := shapeRecordSelected(st, line, offset, body, cwd, nil)
	return events, gaps
}

// This is a negative check, never a source of public DTOs. ASCII word queries
// inspect native text and canonical tool arguments before entity extraction.
// Other queries retain full shaping. Both use the same native decoder.
func nativeTextCandidate(q snapshot.TrajectorySelector) bool {
	if q.Text == "" || q.Skill != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != "" {
		return false
	}
	for _, ch := range q.Text {
		if ch != ' ' && ch != '\t' && ch != '\n' && ch != '\r' && ch != '-' && ch != '_' && !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') && !(ch >= '0' && ch <= '9') {
			return false
		}
	}
	return true
}

func shapeRecordSelected(st *sourceState, line int, offset int64, body []byte, cwd *string, q *snapshot.TrajectorySelector) ([]snapshot.TrajectoryEvent, []string, int) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil, 0
	}
	if !utf8.Valid(body) {
		return nil, []string{fmt.Sprintf("invalid_utf8:L%d", line)}, 0
	}
	decoded, err := st.Decoder.Decode(body, DecodeContext{SessionID: sessionID(st)})
	if err != nil {
		return nil, []string{fmt.Sprintf("invalid_record:L%d", line)}, 0
	}
	var gaps []string
	if len(decoded) > 64 {
		decoded = decoded[:64]
		gaps = append(gaps, "content_block_limit")
	}
	if q != nil {
		possible := false
		for _, event := range decoded {
			possible = possible || matches(event, *q)
			if !possible && event.Tool != nil && len(event.Tool.Arguments) > 0 {
				// Public RawMessage arguments are compacted and HTML-escaped
				// by canonical encoding. Check that exact search representation
				// too; e.g. a native '<' may introduce a literal "u003c" match.
				arguments, err := json.Marshal(event.Tool.Arguments)
				if err != nil {
					possible = true // let the standard path report this error
				} else {
					tool := *event.Tool
					tool.Arguments = arguments
					event.Tool = &tool
					possible = matches(event, *q)
				}
			}
		}
		if !possible {
			for _, event := range decoded {
				if event.Workspace != nil {
					*cwd = event.Workspace.Path
				}
			}
			return nil, gaps, len(decoded)
		}
	}
	var record map[string]json.RawMessage
	recordErr := json.Unmarshal(body, &record)
	payload := object(record["payload"])
	hash, typ := digest(body), nativeType(record, payload)
	for block := range decoded {
		e := &decoded[block]
		e.ID = eventID(st, offset, block, hash)
		e.Source = snapshot.TrajectorySourceRef{ID: st.ID, Generation: st.Generation, Line: line, Offset: offset, Length: len(body), Block: block, Digest: hash, NativeType: typ}
		if e.Workspace != nil {
			*cwd = e.Workspace.Path
		}
		var entityGaps []string
		e.Entities, entityGaps = extractRecordEntities(*e, record, payload, recordErr, EntityContext{Agent: st.Agent, WorkingDirectory: *cwd})
		if len(entityGaps) > 0 {
			c := coverage("event entities:" + e.ID)
			for _, g := range entityGaps {
				gap(&c, g)
			}
			e.EntityCoverage = &c
		}
		if e.Tool != nil && len(e.Tool.CallID) > 512 {
			e.Omissions = append(e.Omissions, "tool_call_id_size_limit")
		}
		gaps = append(gaps, e.Omissions...)
	}
	return decoded, gaps, 0
}
