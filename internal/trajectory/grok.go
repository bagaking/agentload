package trajectory

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// GrokDecoder reads the ACP session updates selected by Grok's existing
// discovery adapter. The separate events.jsonl diagnostics stream is not a
// transcript and does not supply the same call, content, or usage evidence.
type GrokDecoder struct{}

func (GrokDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, err
	}
	params := object(envelope["params"])
	update := object(params["update"])
	meta := object(params["_meta"])
	typ := grokString(update, "sessionUpdate")
	nativeID := grokString(meta, "eventId")
	turnID := grokString(update, "prompt_id")
	if turnID == "" {
		turnID = grokString(meta, "promptId")
	}
	var events []snapshot.TrajectoryEvent
	switch typ {
	case "user_message_chunk", "agent_message_chunk", "agent_thought_chunk":
		role, kind := "assistant", "text"
		if typ == "user_message_chunk" {
			role = "user"
		} else if typ == "agent_thought_chunk" {
			kind = "reasoning"
		}
		content := object(update["content"])
		e := event(role, kind, "")
		if grokString(content, "type") == "text" {
			e.Text = CleanText(grokString(content, "text"))
		} else {
			grokOmission(&e, "unsupported_grok_content:"+grokLabel(grokString(content, "type")))
		}
		events = append(events, e)
	case "tool_call", "tool_call_update":
		updateMeta := object(update["_meta"])
		toolMeta := object(updateMeta["x.ai/tool"])
		status := grokString(update, "status")
		// Grok also records the original ACP update parameters here. Only
		// explicit status and ID fields are used; titles are never identities.
		updateParams := object(meta["updateParams"])
		if status == "" {
			status = grokString(updateParams, "status")
		}
		callID := grokString(update, "toolCallId")
		callIDField := "/params/update/toolCallId"
		if callID == "" {
			callID = grokString(updateParams, "toolCallId")
			callIDField = "/params/_meta/updateParams/toolCallId"
		}
		role, kind := "assistant", "tool_call"
		if typ == "tool_call_update" {
			kind = "tool_update"
			if strings.EqualFold(status, "completed") || strings.EqualFold(status, "failed") {
				role, kind = "tool", "tool_result"
			}
		}
		e := event(role, kind, "")
		e.Tool = &snapshot.TrajectoryTool{Name: grokString(toolMeta, "name"), CallID: callID, Arguments: update["rawInput"]}
		if kind == "tool_result" {
			withRelation(&e, "tool_result", snapshot.TrajectoryReference{Kind: "call", ID: callID}, callIDField)
		}
		e.Outcome = status
		if status != "" {
			field := "/params/update/status"
			if grokString(update, "status") == "" {
				field = "/params/_meta/updateParams/status"
			}
			e.Attention = &snapshot.TrajectoryAttentionEvidence{OutcomeField: field}
		}
		if callID == "" {
			grokOmission(&e, "grok_call_id_unavailable")
		}
		if kind == "tool_call" && e.Tool.Name == "" {
			grokOmission(&e, "grok_tool_name_unavailable")
		}
		grokToolContent(&e, update)
		events = append(events, e)
	case "turn_completed":
		e := event("system", "turn_completed", "")
		e.Outcome = grokString(update, "stop_reason")
		events = append(events, e)
		usage, omissions := grokUsage(update["usage"], turnID)
		if usage != nil || len(omissions) > 0 {
			u := event("system", "usage", "")
			u.Usage = usage
			u.Omissions = omissions
			events = append(events, u)
		}
	case "session_recap":
		events = append(events, event("system", "summary", grokString(update, "summary")))
	default:
		e := event("system", "unknown", "")
		grokOmission(&e, "unsupported_grok_update:"+grokLabel(typ))
		events = append(events, e)
	}
	var timestamp *time.Time
	var epoch int64
	if json.Unmarshal(envelope["timestamp"], &epoch) == nil && epoch > 0 && epoch <= 253402300799 {
		t := time.Unix(epoch, 0).UTC()
		timestamp = &t
	}
	for i := range events {
		events[i].SessionID = ctx.SessionID
		events[i].NativeID = nativeID
		events[i].NativeEnvelopeID = nativeID
		events[i].TurnID = turnID
		events[i].Timestamp = timestamp
		if timestamp == nil {
			grokOmission(&events[i], "grok_timestamp_unavailable")
		}
		sort.Strings(events[i].Omissions)
	}
	return events, nil
}

func grokString(fields map[string]json.RawMessage, key string) string {
	var value string
	_ = json.Unmarshal(fields[key], &value)
	return value
}

func grokToolContent(e *snapshot.TrajectoryEvent, update map[string]json.RawMessage) {
	var blocks []map[string]json.RawMessage
	if raw := update["content"]; len(raw) > 0 && string(raw) != "null" {
		if json.Unmarshal(raw, &blocks) != nil {
			grokOmission(e, "invalid_grok_tool_content")
		}
	}
	texts := []string{}
	for _, block := range blocks {
		content := object(block["content"])
		if grokString(block, "type") == "content" && grokString(content, "type") == "text" {
			texts = append(texts, grokString(content, "text"))
		} else {
			grokOmission(e, "unsupported_grok_tool_content:"+grokLabel(grokString(block, "type")))
		}
	}
	if len(texts) > 0 {
		e.Text = CleanText(strings.Join(texts, "\n"))
	} else if raw := update["rawOutput"]; len(raw) > 0 && string(raw) != "null" {
		// Keep the native structured result as JSON when no text projection
		// was recorded. It is evidence, not inferred success or model input.
		e.Text = CleanText(String(raw))
	}
}

var grokUsageCounters = map[string]string{
	"inputTokens":         "input_tokens",
	"outputTokens":        "output_tokens",
	"totalTokens":         "total_tokens",
	"cachedReadTokens":    "cache_read_input_tokens",
	"cacheCreationTokens": "cache_creation_input_tokens",
	"reasoningTokens":     "reasoning_output_tokens",
	"costUsdTicks":        "cost_usd_ticks",
	"modelCalls":          "model_calls",
	"apiDurationMs":       "api_duration_ms",
	"numTurns":            "num_turns",
}

func grokUsage(raw json.RawMessage, turnID string) (*snapshot.TrajectoryUsage, []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return nil, []string{"invalid_grok_usage"}
	}
	e := snapshot.TrajectoryEvent{}
	counters := grokCounters(fields, &e)
	models := map[string]map[string]int64{}
	if rawModels := fields["modelUsage"]; len(rawModels) > 0 && string(rawModels) != "null" {
		var nativeModels map[string]json.RawMessage
		if json.Unmarshal(rawModels, &nativeModels) != nil {
			grokOmission(&e, "invalid_grok_model_usage")
		} else {
			names := make([]string, 0, len(nativeModels))
			for name := range nativeModels {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if len(models) >= 16 {
					grokOmission(&e, "grok_model_usage_limit")
					break
				}
				if name == "" || len(name) > 80 {
					grokOmission(&e, "grok_model_name_unavailable")
					continue
				}
				var fields map[string]json.RawMessage
				if json.Unmarshal(nativeModels[name], &fields) != nil || fields == nil {
					grokOmission(&e, "invalid_grok_model_usage")
					continue
				}
				if c := grokCounters(fields, &e); len(c) > 0 {
					models[name] = c
				}
			}
		}
	}
	if len(counters) == 0 && len(models) == 0 {
		return nil, e.Omissions
	}
	u := &snapshot.TrajectoryUsage{Scope: "turn", ScopeID: turnID, Aggregation: "last_nonempty", Counters: counters}
	if len(models) > 0 {
		u.Models = models
	}
	if turnID == "" {
		grokOmission(&e, "grok_usage_scope_id_unavailable")
	}
	return u, e.Omissions
}

func grokCounters(fields map[string]json.RawMessage, e *snapshot.TrajectoryEvent) map[string]int64 {
	counters := map[string]int64{}
	for native, raw := range fields {
		if native == "modelUsage" {
			continue
		}
		name, ok := grokUsageCounters[native]
		if !ok {
			grokOmission(e, "unsupported_grok_usage_counter")
			continue
		}
		var count int64
		if json.Unmarshal(raw, &count) != nil || count < 0 || string(raw) == "null" {
			grokOmission(e, "invalid_grok_usage_counter:"+name)
			continue
		}
		counters[name] = count
	}
	return counters
}

func grokLabel(value string) string {
	if len(value) > 80 {
		return "name_size_limit"
	}
	return value
}

func grokOmission(e *snapshot.TrajectoryEvent, omission string) {
	for _, existing := range e.Omissions {
		if existing == omission {
			return
		}
	}
	if len(e.Omissions) < 16 {
		e.Omissions = append(e.Omissions, omission)
	}
}
