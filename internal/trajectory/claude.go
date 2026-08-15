package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// ClaudeDecoder reads Claude Code's project transcript records. Protocol roles
// are preserved independently of the normalized tool role and actor identity.
type ClaudeDecoder struct{}

func (ClaudeDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	var record map[string]json.RawMessage
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	message := object(record["message"])
	role := field(message, "role")
	protocolRole := role
	typ := field(record, "type")
	if role == "" {
		role = typ
		if role != "user" && role != "assistant" && role != "system" {
			role = "unknown"
		}
	}
	var events []snapshot.TrajectoryEvent
	var compactSummary bool
	_ = json.Unmarshal(record["isCompactSummary"], &compactSummary)
	switch typ {
	case "user", "assistant":
		var text string
		if json.Unmarshal(message["content"], &text) == nil && string(message["content"]) != "null" {
			kind := "text"
			if compactSummary {
				kind = "summary"
			}
			events = append(events, event(role, kind, text))
		} else {
			var blocks []json.RawMessage
			if json.Unmarshal(message["content"], &blocks) != nil || len(blocks) == 0 {
				e := event(role, "message", "")
				e.Omissions = []string{"no_visible_content"}
				events = append(events, e)
			}
			for blockIndex, rawBlock := range blocks {
				block := object(rawBlock)
				var e snapshot.TrajectoryEvent
				switch field(block, "type") {
				case "text":
					kind := "text"
					if compactSummary {
						kind = "summary"
					}
					e = event(role, kind, field(block, "text"))
				case "thinking":
					e = event(role, "reasoning", field(block, "thinking"))
				case "redacted_thinking":
					e = event(role, "reasoning", "")
					e.Omissions = []string{"reasoning_redacted"}
				case "tool_use":
					e = event(role, "tool_call", "")
					e.Tool = &snapshot.TrajectoryTool{Name: field(block, "name"), CallID: field(block, "id"), Arguments: block["input"]}
					if e.Tool.CallID == "" {
						e.Omissions = append(e.Omissions, "tool_call_id_missing")
					}
				case "tool_result":
					text, omissions := claudeResultText(block["content"])
					e = event("tool", "tool_result", text)
					e.Omissions = omissions
					e.Tool = &snapshot.TrajectoryTool{CallID: nativeString(block["tool_use_id"])}
					withRelation(&e, "tool_result", snapshot.TrajectoryReference{Kind: "call", ID: e.Tool.CallID}, "/message/content/"+strconv.Itoa(blockIndex)+"/tool_use_id")
					if e.Tool.CallID == "" {
						e.Omissions = append(e.Omissions, "tool_call_id_missing")
					}
					var isError bool
					if json.Unmarshal(block["is_error"], &isError) == nil && string(block["is_error"]) != "null" {
						e.Attention = &snapshot.TrajectoryAttentionEvidence{OutcomeField: "/message/content/" + strconv.Itoa(blockIndex) + "/is_error"}
						e.Outcome = "not_error"
						if isError {
							e.Outcome = "error"
						}
					}
				default:
					e = event(role, "unknown", "")
					e.Omissions = []string{"unsupported_claude_content_block:" + field(block, "type")}
				}
				events = append(events, e)
			}
		}
		if usage := object(message["usage"]); len(usage) > 0 {
			e := event(role, "usage", "")
			e.Usage = &snapshot.TrajectoryUsage{Scope: "message", ScopeID: field(message, "id"), Aggregation: "last_nonempty", Counters: map[string]int64{}}
			claudeUsageCounters(usage, "", e.Usage.Counters)
			if e.Usage.ScopeID == "" {
				e.Omissions = append(e.Omissions, "usage_message_id_missing")
			}
			if len(e.Usage.Counters) == 0 {
				e.Omissions = append(e.Omissions, "no_supported_usage_counters")
			}
			events = append(events, e)
		}
	case "summary":
		events = append(events, event("system", "summary", field(record, "summary")))
	case "system":
		subtype := field(record, "subtype")
		switch subtype {
		case "compact_boundary":
			events = append(events, event("system", "compaction", field(record, "content")))
		case "local_command", "informational", "turn_duration", "stop_hook_summary":
			events = append(events, event("system", subtype, field(record, "content")))
		default:
			e := event("system", "unknown", "")
			e.Omissions = []string{"unsupported_claude_system_record:" + subtype}
			events = append(events, e)
		}
	default:
		e := event("system", "unknown", "")
		e.Omissions = []string{"unsupported_claude_record:" + typ}
		events = append(events, e)
	}
	var timestamp *time.Time
	if parsed, err := time.Parse(time.RFC3339Nano, field(record, "timestamp")); err == nil {
		timestamp = &parsed
	}
	nativeID := field(message, "id")
	if nativeID == "" {
		nativeID = field(record, "uuid")
	}
	for i := range events {
		events[i].SessionID = ctx.SessionID
		events[i].NativeID = nativeID
		events[i].NativeEnvelopeID = field(record, "uuid")
		events[i].ProtocolRole = protocolRole
		events[i].Timestamp = timestamp
	}
	if len(events) > 0 {
		events[0].Workspace = nativeWorkspace(record["cwd"], "/cwd")
		withRelation(&events[0], "parent", snapshot.TrajectoryReference{Kind: "native_envelope", ID: nativeString(record["parentUuid"])}, "/parentUuid")
	}
	return events, nil
}

// A result is one outer content block, including when it is empty. Nested text
// stays in recorded order; non-text evidence remains available through Raw.
func claudeResultText(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", []string{"no_visible_tool_result_content"}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && string(raw) != "null" {
		return text, nil
	}
	var blocks []json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", []string{"unsupported_claude_tool_result_content"}
	}
	var texts, omissions []string
	for _, rawBlock := range blocks {
		block := object(rawBlock)
		if field(block, "type") == "text" {
			texts = append(texts, field(block, "text"))
		} else {
			omissions = append(omissions, "unsupported_claude_tool_result_block:"+field(block, "type"))
		}
	}
	return strings.Join(texts, "\n"), omissions
}

// Names are native JSON paths. A nested breakdown is not added to its total;
// consumers use the stated message scope and replacement aggregation.
func claudeUsageCounters(usage map[string]json.RawMessage, prefix string, counters map[string]int64) {
	for name, raw := range usage {
		var n int64
		if string(raw) != "null" && json.Unmarshal(raw, &n) == nil && n >= 0 {
			counters[prefix+name] = n
		} else if nested := object(raw); len(nested) > 0 {
			claudeUsageCounters(nested, prefix+name+".", counters)
		}
	}
}
