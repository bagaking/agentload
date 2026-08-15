package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

type DecodeContext struct{ SessionID string }

// Decode must allow concurrent calls across sources. Built-in decoders keep
// record state local to the call; shared instrumentation must synchronize it.
type Decoder interface {
	Decode([]byte, DecodeContext) ([]snapshot.TrajectoryEvent, error)
}
type CodexDecoder struct{}

var envelopes = func() []*regexp.Regexp {
	var patterns []*regexp.Regexp
	for _, tag := range []string{"system-reminder", "environment_context", "permissions_instructions", "developer_instructions", "skills_instructions"} {
		patterns = append(patterns, regexp.MustCompile(`(?s)<`+tag+`>.*?</`+tag+`>`))
	}
	patterns = append(patterns, regexp.MustCompile(`(?s)<amux from="amux">\s*AgentMux runtime guide:.*?</amux>`))
	patterns = append(patterns, regexp.MustCompile(`(?s)^# AGENTS\.md instructions(?: for [^\n]+)?\s*\n+<INSTRUCTIONS>.*?</INSTRUCTIONS>`))
	return patterns
}()

// CleanText is presentation-only. State and knowledge rules read source evidence.
// Unmatched wrappers stay present; removing them could erase a real request.
func CleanText(text string) string {
	for _, pattern := range envelopes {
		text = pattern.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(text)
}

func String(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	return string(raw)
}

func object(raw json.RawMessage) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	return m
}
func field(m map[string]json.RawMessage, key string) string { return String(m[key]) }
func event(role, kind, text string) snapshot.TrajectoryEvent {
	e := snapshot.TrajectoryEvent{Role: role, Kind: kind, Text: CleanText(text), Actor: snapshot.TrajectoryActor{Kind: "unknown"}}
	if role != "user" && role != "assistant" && role != "tool" && role != "system" && role != "unknown" {
		e.Role = "unknown"
		e.Omissions = []string{"protocol_role_unavailable"}
	}
	return e
}

func (CodexDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	p := object(m["payload"])
	typ := field(m, "type")
	ptyp := field(p, "type")
	var events []snapshot.TrajectoryEvent
	switch typ {
	case "session_meta":
		e := event("system", "session", "")
		e.NativeID = nativeString(p["id"])
		rolloutMetadata(&e, p, "codex")
		events = append(events, e)
	case "turn_context":
		e := event("system", "context", "")
		e.TurnID = nativeString(p["turn_id"])
		e.Workspace = nativeWorkspace(p["cwd"], "/payload/cwd")
		events = append(events, e)
	case "compacted":
		events = append(events, event("system", "summary", field(p, "message")))
	case "response_item":
		switch ptyp {
		case "message", "reasoning":
			role := nativeString(p["role"])
			if role == "" && ptyp == "reasoning" {
				role = "assistant"
			}
			var blocks []map[string]json.RawMessage
			content := p["content"]
			if ptyp == "reasoning" {
				content = p["summary"]
			}
			if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 {
				e := event(role, ptyp, "")
				e.Omissions = []string{"no_visible_content"}
				events = append(events, e)
			}
			for _, b := range blocks {
				kind := "text"
				if ptyp == "reasoning" {
					kind = "reasoning"
				}
				e := event(role, kind, field(b, "text"))
				e.ProtocolRole = nativeString(p["role"])
				e.NativeID = nativeString(p["id"])
				events = append(events, e)
			}
		case "function_call", "custom_tool_call":
			args := p["arguments"]
			if ptyp == "custom_tool_call" {
				args = p["input"]
			}
			var s string
			if json.Unmarshal(args, &s) == nil && json.Valid([]byte(s)) {
				args = json.RawMessage(s)
			}
			e := event("assistant", "tool_call", "")
			e.Tool = &snapshot.TrajectoryTool{Name: field(p, "name"), CallID: nativeString(p["call_id"]), Arguments: args}
			e.NativeID = nativeString(p["id"])
			events = append(events, e)
		case "function_call_output", "custom_tool_call_output":
			e := event("tool", "tool_result", field(p, "output"))
			e.Tool = &snapshot.TrajectoryTool{CallID: nativeString(p["call_id"])}
			withRelation(&e, "tool_result", snapshot.TrajectoryReference{Kind: "call", ID: e.Tool.CallID}, "/payload/call_id")
			events = append(events, e)
		case "web_search_call":
			e := event("assistant", "tool_call", "")
			e.Tool = &snapshot.TrajectoryTool{Name: "web_search", CallID: nativeString(p["id"]), Arguments: p["action"]}
			events = append(events, e)
		default:
			e := event("system", "unknown", "")
			e.Omissions = []string{"unsupported_response_item:" + ptyp}
			events = append(events, e)
		}
	case "event_msg":
		switch ptyp {
		case "user_message":
			events = append(events, event("user", "input_observed", field(p, "message")))
		case "agent_message":
			events = append(events, event("assistant", "output_observed", field(p, "message")))
		case "task_started", "task_complete", "turn_aborted", "permission_request", "permission_response", "waiting_input", "input_response", "tool_error":
			e := event("system", ptyp, field(p, "message"))
			e.TurnID = nativeString(p["turn_id"])
			nativeIntervention(&e, p, "/payload")
			if ptyp == "tool_error" {
				e.Outcome = "error"
				e.Tool = &snapshot.TrajectoryTool{Name: nativeString(p["name"]), CallID: nativeString(p["call_id"])}
				e.Attention = &snapshot.TrajectoryAttentionEvidence{OutcomeField: "/payload/type"}
			}
			events = append(events, e)
		case "token_count":
			events = append(events, event("system", "usage", ""))
		case "context_compacted":
			events = append(events, event("system", "summary", field(p, "message")))
		default:
			e := event("system", "unknown", "")
			e.Omissions = []string{"unsupported_event_msg:" + ptyp}
			events = append(events, e)
		}
	default:
		e := event("system", "unknown", "")
		e.Omissions = []string{"unsupported_record:" + typ}
		events = append(events, e)
	}
	var ts *time.Time
	if t, err := time.Parse(time.RFC3339Nano, field(m, "timestamp")); err == nil {
		ts = &t
	}
	for i := range events {
		events[i].Timestamp = ts
		events[i].SessionID = ctx.SessionID
		if events[i].TurnID == "" {
			events[i].TurnID = nativeString(p["turn_id"])
		}
	}
	return events, nil
}
