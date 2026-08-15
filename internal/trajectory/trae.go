package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// TraeDecoder reads the local rollout envelopes. An appended history item is
// archived evidence; it does not establish membership in a later model input.
type TraeDecoder struct{}

func (TraeDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	p := object(m["payload"])
	var events []snapshot.TrajectoryEvent
	switch typ := field(m, "type"); typ {
	case "session_meta":
		e := event("system", "session", "")
		e.NativeID = nativeString(p["id"])
		rolloutMetadata(&e, p, "trae")
		events = append(events, e)
	case "turn_context":
		e := event("system", "context", "")
		e.Workspace = nativeWorkspace(p["cwd"], "/payload/cwd")
		events = append(events, e)
	case "response_item":
		events = traeItem(p)
		for i := range events {
			if events[i].Kind == "tool_result" && events[i].Tool != nil {
				withRelation(&events[i], "tool_result", snapshot.TrajectoryReference{Kind: "call", ID: events[i].Tool.CallID}, "/payload/call_id")
			}
		}
	case "history_mutation":
		// Replacement snapshots can repeat earlier items. They are not new
		// actions, and reconstructing their context requires a separate contract.
		var version int
		if json.Unmarshal(p["version"], &version) != nil || version != 1 {
			events = []snapshot.TrajectoryEvent{traeUnknown("unsupported_history_version:" + field(p, "version"))}
		} else if field(p, "operation") != "append" {
			events = []snapshot.TrajectoryEvent{traeUnknown("unsupported_history_operation:" + field(p, "operation"))}
		} else {
			var items []map[string]json.RawMessage
			if json.Unmarshal(p["items"], &items) != nil || len(items) == 0 {
				events = []snapshot.TrajectoryEvent{traeUnknown("history_items_unavailable")}
			}
			for index, item := range items {
				decoded := traeItem(item)
				for i := range decoded {
					if decoded[i].Kind == "tool_result" && decoded[i].Tool != nil {
						withRelation(&decoded[i], "tool_result", snapshot.TrajectoryReference{Kind: "call", ID: decoded[i].Tool.CallID}, "/payload/items/"+strconv.Itoa(index)+"/call_id")
					}
				}
				events = append(events, decoded...)
			}
		}
		for i := range events {
			events[i].NativeEnvelopeID = nativeString(p["commit_id"])
		}
	case "event_msg":
		switch typ := field(p, "type"); typ {
		case "task_started", "task_complete", "turn_aborted":
			events = append(events, event("system", typ, field(p, "message")))
		case "context_compacted":
			events = append(events, event("system", "summary", field(p, "message")))
		case "token_count":
			e := event("system", "usage", "")
			counters := map[string]int64{}
			for key, raw := range object(object(p["info"])["total_token_usage"]) {
				if count, err := strconv.ParseInt(string(raw), 10, 64); err == nil {
					counters[key] = count
				}
			}
			if len(counters) > 0 {
				e.Usage = &snapshot.TrajectoryUsage{Scope: "session", ScopeID: ctx.SessionID, Aggregation: "cumulative", Counters: counters}
			} else {
				e.Omissions = []string{"usage_counters_unavailable"}
			}
			events = append(events, e)
		default:
			e := traeUnknown("unsupported_event_msg:" + typ)
			if typ == "item_completed" {
				e.NativeID = nativeString(object(p["item"])["id"])
			}
			events = append(events, e)
		}
	default:
		events = append(events, traeUnknown("unsupported_record:"+typ))
	}
	var ts *time.Time
	if parsed, err := time.Parse(time.RFC3339Nano, field(m, "timestamp")); err == nil {
		ts = &parsed
	}
	for i := range events {
		events[i].SessionID = ctx.SessionID
		events[i].Timestamp = ts
		if ts == nil {
			events[i].Omissions = append(events[i].Omissions, "timestamp_unavailable")
		}
		if events[i].TurnID == "" {
			events[i].TurnID = nativeString(p["turn_id"])
		}
		if events[i].NativeID == "" {
			events[i].NativeID = nativeString(p["id"])
		}
	}
	return events, nil
}

func traeUnknown(omission string) snapshot.TrajectoryEvent {
	e := event("system", "unknown", "")
	e.Omissions = []string{omission}
	return e
}

func traeItem(item map[string]json.RawMessage) []snapshot.TrajectoryEvent {
	var events []snapshot.TrajectoryEvent
	switch typ := field(item, "type"); typ {
	case "message", "reasoning":
		role := nativeString(item["role"])
		kind := "text"
		content := item["content"]
		if typ == "reasoning" {
			role, kind = "assistant", "reasoning"
			// Trae records visible reasoning in content; summary can be empty.
			if len(content) == 0 || string(content) == "null" {
				content = item["summary"]
			}
		}
		var blocks []map[string]json.RawMessage
		if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 {
			e := event(traeRole(role), kind, "")
			e.Omissions = []string{"no_visible_content"}
			events = append(events, e)
		}
		for _, block := range blocks {
			switch blockType := field(block, "type"); blockType {
			case "input_text", "output_text", "text", "reasoning_text", "summary_text":
				events = append(events, event(traeRole(role), kind, field(block, "text")))
			default:
				events = append(events, traeUnknown("unsupported_content_block:"+blockType))
			}
		}
	case "function_call", "custom_tool_call":
		args := item["arguments"]
		if typ == "custom_tool_call" {
			args = item["input"]
		}
		if typ == "function_call" {
			var text string
			if json.Unmarshal(args, &text) == nil && json.Valid([]byte(text)) {
				args = json.RawMessage(text)
			}
		}
		e := event("assistant", "tool_call", "")
		e.Tool = &snapshot.TrajectoryTool{Name: nativeString(item["name"]), CallID: nativeString(item["call_id"]), Arguments: args}
		if e.Tool.CallID == "" {
			e.Omissions = []string{"tool_call_id_missing"}
		}
		events = append(events, e)
	case "function_call_output", "custom_tool_call_output":
		text, omissions := traeOutput(item["output"])
		e := event("tool", "tool_result", text)
		e.Tool = &snapshot.TrajectoryTool{CallID: nativeString(item["call_id"])}
		e.Omissions = omissions
		if e.Tool.CallID == "" {
			e.Omissions = append(e.Omissions, "tool_call_id_missing")
		}
		events = append(events, e)
	default:
		events = append(events, traeUnknown("unsupported_response_item:"+typ))
	}
	for i := range events {
		events[i].NativeID = nativeString(item["id"])
		events[i].ProtocolRole = nativeString(item["role"])
		events[i].TurnID = nativeString(item["turn_id"])
		if events[i].TurnID == "" {
			events[i].TurnID = nativeString(object(item["internal_chat_message_metadata_passthrough"])["turn_id"])
		}
	}
	return events
}

func traeRole(role string) string {
	switch role {
	case "user", "assistant", "tool":
		return role
	case "system", "developer":
		return "system"
	default:
		return "unknown"
	}
}

// A block-array output is one receipt, including when it is empty. Keeping one
// result event prevents its text blocks from inventing multiple completions.
func traeOutput(raw json.RawMessage) (string, []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", []string{"tool_output_unavailable"}
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []map[string]json.RawMessage
	if json.Unmarshal(raw, &blocks) != nil {
		return "", []string{"unsupported_tool_output"}
	}
	var parts, omissions []string
	for _, block := range blocks {
		switch typ := field(block, "type"); typ {
		case "input_text", "output_text", "text":
			parts = append(parts, field(block, "text"))
		default:
			omissions = append(omissions, "unsupported_tool_output_block:"+typ)
		}
	}
	return strings.Join(parts, "\n"), omissions
}
