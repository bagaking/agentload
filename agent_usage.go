package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

type claudeOutputUsageDecoder struct{}

func newClaudeOutputUsageDecoder() agentOutputUsageDecoder {
	return claudeOutputUsageDecoder{}
}

func (claudeOutputUsageDecoder) DecodeUsage(line []byte) (liveTokenRateObservation, bool) {
	if !bytes.Contains(line, []byte(`"output_tokens"`)) {
		return liveTokenRateObservation{}, false
	}
	var envelope claudeOutputUsageEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil || !envelope.Message.Usage.OutputTokens.Set {
		return liveTokenRateObservation{}, false
	}
	return liveTokenRateObservation{
		At:              envelope.timestamp(),
		OutputTokens:    envelope.Message.Usage.OutputTokens.Value,
		MessageIdentity: envelope.messageIdentity(),
	}, true
}

type codexOutputUsageDecoder struct{}

func newCodexOutputUsageDecoder() agentOutputUsageDecoder {
	return codexOutputUsageDecoder{}
}

func newTraeOutputUsageDecoder() agentOutputUsageDecoder {
	return codexOutputUsageDecoder{}
}

func (codexOutputUsageDecoder) DecodeUsage(line []byte) (liveTokenRateObservation, bool) {
	if !lineMayContainCodexOutputUsage(line) {
		return liveTokenRateObservation{}, false
	}
	if lineMayContainCumulativeOutputUsage(line) {
		var primary primaryCumulativeOutputUsageEnvelope
		if err := json.Unmarshal(line, &primary); err != nil {
			return liveTokenRateObservation{}, false
		}
		if output, ok := primary.output(); ok {
			return liveTokenRateObservation{
				At: primary.timestamp(), OutputTokens: output, Cumulative: true,
			}, true
		}
		var envelope cumulativeOutputUsageEnvelope
		if err := json.Unmarshal(line, &envelope); err != nil {
			return liveTokenRateObservation{}, false
		}
		if output, ok := envelope.output(); ok {
			return liveTokenRateObservation{
				At: envelope.timestamp(), OutputTokens: output, Cumulative: true,
			}, true
		}
	}
	var envelope incrementalOutputUsageEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return liveTokenRateObservation{}, false
	}
	output, ok := envelope.output()
	if !ok {
		return liveTokenRateObservation{}, false
	}
	return liveTokenRateObservation{At: envelope.timestamp(), OutputTokens: output}, true
}

// grokOutputUsageDecoder reads the usage record grok attaches to each
// turn_completed update.
//
// The counts are PER-TURN, not cumulative: replaying real sessions shows the
// output series rising and falling (917563 -> 282982 -> 146484) and numTurns
// differing line to line. Treating them as cumulative — the shape codex uses —
// would make every turn look like a fresh total and inflate the rate, which is
// exactly how ccusage shipped its 91x token overcount (#950).
//
// A turn_completed carrying no usage object is skipped rather than recorded as
// zero; 8 of 130 such lines in one sampled session have none, and a zero there
// would read as a real measurement of no output.
type grokOutputUsageDecoder struct{}

func newGrokOutputUsageDecoder() agentOutputUsageDecoder {
	return grokOutputUsageDecoder{}
}

type grokUsageEnvelope struct {
	Timestamp int64 `json:"timestamp"`
	Params    struct {
		Update struct {
			PromptID string `json:"prompt_id"`
			Usage    *struct {
				OutputTokens optionalTokenCount `json:"outputTokens"`
			} `json:"usage"`
		} `json:"update"`
		SessionID string `json:"sessionId"`
	} `json:"params"`
}

func (grokOutputUsageDecoder) DecodeUsage(line []byte) (liveTokenRateObservation, bool) {
	if !bytes.Contains(line, []byte(`"outputTokens"`)) {
		return liveTokenRateObservation{}, false
	}
	var envelope grokUsageEnvelope
	if err := json.Unmarshal(line, &envelope); err != nil {
		return liveTokenRateObservation{}, false
	}
	usage := envelope.Params.Update.Usage
	if usage == nil || !usage.OutputTokens.Set {
		return liveTokenRateObservation{}, false
	}
	observation := liveTokenRateObservation{
		OutputTokens: usage.OutputTokens.Value,
	}
	if envelope.Timestamp > 0 {
		observation.At = time.Unix(envelope.Timestamp, 0).UTC()
	}
	// Each turn reports once, so the prompt id de-duplicates a re-read tail the
	// same way the claude message id does.
	if promptID := strings.TrimSpace(envelope.Params.Update.PromptID); promptID != "" {
		observation.MessageIdentity = strings.TrimSpace(envelope.Params.SessionID) + "\x00" + promptID
	}
	return observation, true
}

type optionalTokenCount struct {
	Value int64
	Set   bool
}

func (value *optionalTokenCount) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) >= 2 && data[0] == '"' && data[len(data)-1] == '"' {
		data = data[1 : len(data)-1]
	}
	if len(data) == 0 {
		return nil
	}
	var parsed int64
	const maxInt64 = int64(^uint64(0) >> 1)
	for _, digit := range data {
		if digit < '0' || digit > '9' {
			return nil
		}
		number := int64(digit - '0')
		if parsed > (maxInt64-number)/10 {
			return nil
		}
		parsed = parsed*10 + number
	}
	value.Value = parsed
	value.Set = true
	return nil
}

type outputUsageFields struct {
	OutputTokens         optionalTokenCount `json:"output_tokens"`
	CompletionTokens     optionalTokenCount `json:"completion_tokens"`
	CandidatesTokenCount optionalTokenCount `json:"candidatesTokenCount"`
	OutputTokenCount     optionalTokenCount `json:"outputTokenCount"`
}

func (fields outputUsageFields) output() (int64, bool) {
	if fields.OutputTokens.Set {
		return fields.OutputTokens.Value, true
	}
	if fields.CompletionTokens.Set {
		return fields.CompletionTokens.Value, true
	}
	if fields.CandidatesTokenCount.Set {
		return fields.CandidatesTokenCount.Value, true
	}
	if fields.OutputTokenCount.Set {
		return fields.OutputTokenCount.Value, true
	}
	return 0, false
}

type outputUsageTimestamp struct {
	Timestamp      string `json:"timestamp"`
	CreatedAt      string `json:"created_at"`
	CreatedAtCamel string `json:"createdAt"`
}

func (value outputUsageTimestamp) timestamp() time.Time {
	for _, raw := range []string{value.Timestamp, value.CreatedAt, value.CreatedAtCamel} {
		if parsed := parseTimestampString(raw); !parsed.IsZero() {
			return parsed
		}
	}
	return time.Time{}
}

type claudeOutputUsageEnvelope struct {
	outputUsageTimestamp
	SessionID      string `json:"sessionId"`
	SessionIDSnake string `json:"session_id"`
	UUID           string `json:"uuid"`
	Message        struct {
		ID    string `json:"id"`
		Usage struct {
			OutputTokens optionalTokenCount `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type primaryCumulativeOutputUsageEnvelope struct {
	outputUsageTimestamp
	Payload struct {
		Info struct {
			TotalTokenUsage outputUsageFields `json:"total_token_usage"`
			TotalTokenCamel outputUsageFields `json:"totalTokenUsage"`
		} `json:"info"`
	} `json:"payload"`
}

func (envelope primaryCumulativeOutputUsageEnvelope) output() (int64, bool) {
	if output, ok := envelope.Payload.Info.TotalTokenUsage.output(); ok {
		return output, true
	}
	return envelope.Payload.Info.TotalTokenCamel.output()
}

func (envelope claudeOutputUsageEnvelope) messageIdentity() string {
	messageID := strings.TrimSpace(envelope.Message.ID)
	if messageID == "" {
		messageID = strings.TrimSpace(envelope.UUID)
	}
	if messageID == "" {
		return ""
	}
	sessionID := strings.TrimSpace(envelope.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(envelope.SessionIDSnake)
	}
	return sessionID + "\x00" + messageID
}

type cumulativeOutputUsageEnvelope struct {
	outputUsageTimestamp
	Payload struct {
		Info struct {
			TotalTokenUsage outputUsageFields `json:"total_token_usage"`
			TotalTokenCamel outputUsageFields `json:"totalTokenUsage"`
		} `json:"info"`
		TotalTokenUsage outputUsageFields `json:"total_token_usage"`
		TotalTokenCamel outputUsageFields `json:"totalTokenUsage"`
	} `json:"payload"`
	Info struct {
		TotalTokenUsage outputUsageFields `json:"total_token_usage"`
		TotalTokenCamel outputUsageFields `json:"totalTokenUsage"`
	} `json:"info"`
	TotalTokenUsage   outputUsageFields  `json:"total_token_usage"`
	TotalTokenCamel   outputUsageFields  `json:"totalTokenUsage"`
	TotalOutputTokens optionalTokenCount `json:"total_output_tokens"`
	TotalOutputCamel  optionalTokenCount `json:"totalOutputTokens"`
}

func (envelope cumulativeOutputUsageEnvelope) output() (int64, bool) {
	for _, fields := range []outputUsageFields{
		envelope.Payload.Info.TotalTokenUsage,
		envelope.Payload.Info.TotalTokenCamel,
		envelope.Payload.TotalTokenUsage,
		envelope.Payload.TotalTokenCamel,
		envelope.Info.TotalTokenUsage,
		envelope.Info.TotalTokenCamel,
		envelope.TotalTokenUsage,
		envelope.TotalTokenCamel,
	} {
		if output, ok := fields.output(); ok {
			return output, true
		}
	}
	if envelope.TotalOutputTokens.Set {
		return envelope.TotalOutputTokens.Value, true
	}
	if envelope.TotalOutputCamel.Set {
		return envelope.TotalOutputCamel.Value, true
	}
	return 0, false
}

type incrementalOutputUsageEnvelope struct {
	outputUsageTimestamp
	Payload struct {
		Usage           outputUsageFields `json:"usage"`
		UsageMetadata   outputUsageFields `json:"usageMetadata"`
		UsageMetadataV2 outputUsageFields `json:"usage_metadata"`
		Response        struct {
			Usage outputUsageFields `json:"usage"`
		} `json:"response"`
	} `json:"payload"`
	Usage           outputUsageFields `json:"usage"`
	UsageMetadata   outputUsageFields `json:"usageMetadata"`
	UsageMetadataV2 outputUsageFields `json:"usage_metadata"`
	Response        struct {
		Usage outputUsageFields `json:"usage"`
	} `json:"response"`
}

func (envelope incrementalOutputUsageEnvelope) output() (int64, bool) {
	for _, fields := range []outputUsageFields{
		envelope.Payload.Response.Usage,
		envelope.Payload.Usage,
		envelope.Payload.UsageMetadata,
		envelope.Payload.UsageMetadataV2,
		envelope.Response.Usage,
		envelope.Usage,
		envelope.UsageMetadata,
		envelope.UsageMetadataV2,
	} {
		if output, ok := fields.output(); ok {
			return output, true
		}
	}
	return 0, false
}

func lineMayContainCodexOutputUsage(line []byte) bool {
	for _, key := range [][]byte{
		[]byte(`"output_tokens"`),
		[]byte(`"completion_tokens"`),
		[]byte(`"candidatesTokenCount"`),
		[]byte(`"outputTokenCount"`),
		[]byte(`"total_output_tokens"`),
		[]byte(`"totalOutputTokens"`),
	} {
		if bytes.Contains(line, key) {
			return true
		}
	}
	return false
}

func lineMayContainCumulativeOutputUsage(line []byte) bool {
	return bytes.Contains(line, []byte(`"total_token_usage"`)) ||
		bytes.Contains(line, []byte(`"totalTokenUsage"`)) ||
		bytes.Contains(line, []byte(`"total_output_tokens"`)) ||
		bytes.Contains(line, []byte(`"totalOutputTokens"`))
}
