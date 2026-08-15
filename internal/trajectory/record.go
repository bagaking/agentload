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
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	if !utf8.Valid(body) {
		return nil, []string{fmt.Sprintf("invalid_utf8:L%d", line)}
	}
	decoded, err := st.Decoder.Decode(body, DecodeContext{SessionID: sessionID(st)})
	if err != nil {
		return nil, []string{fmt.Sprintf("invalid_record:L%d", line)}
	}
	var gaps []string
	if len(decoded) > 64 {
		decoded = decoded[:64]
		gaps = append(gaps, "content_block_limit")
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
	return decoded, gaps
}
