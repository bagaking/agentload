package main

import "testing"

// Grok reports each turn's own usage. Replaying a real session's turn sequence
// through the sampler's accumulation rule is what proves the distinction:
// summed as increments the session's output is the true total, whereas reading
// them as cumulative totals (the codex shape) would take the last value alone
// and, worse, treat every drop as a counter reset.
func TestGrokUsageIsPerTurnNotCumulative(t *testing.T) {
	decoder := newGrokOutputUsageDecoder()
	// Output falls between turns here, exactly as observed on disk.
	lines := []string{
		`{"timestamp":1788194984,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{"outputTokens":8792}}}}`,
		`{"timestamp":1788194990,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","prompt_id":"p2","usage":{"outputTokens":3086}}}}`,
		`{"timestamp":1788195000,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","prompt_id":"p3","usage":{"outputTokens":3391}}}}`,
	}
	var total int64
	for _, line := range lines {
		observation, ok := decoder.DecodeUsage([]byte(line))
		if !ok {
			t.Fatalf("expected usage from %s", line)
		}
		if observation.Cumulative {
			t.Fatal("grok usage must decode as per-turn; marking it cumulative makes the sampler read each turn as a fresh total and inflate the rate")
		}
		if observation.At.IsZero() {
			t.Fatal("expected the epoch-second timestamp to decode")
		}
		total += observation.OutputTokens
	}
	if total != 15269 {
		t.Fatalf("expected per-turn increments to sum to 15269, got %d", total)
	}
}

// A turn_completed with no usage object must yield nothing. Recording zero
// would assert a measured absence of output where there was no measurement —
// 8 of 130 turn_completed lines in one sampled session have no usage.
func TestGrokUsageSkipsTurnsWithoutUsageRatherThanRecordingZero(t *testing.T) {
	decoder := newGrokOutputUsageDecoder()
	for _, line := range []string{
		`{"timestamp":1788194984,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","prompt_id":"p1"}}}`,
		`{"timestamp":1788194984,"params":{"sessionId":"s","update":{"sessionUpdate":"agent_message_chunk"}}}`,
	} {
		if observation, ok := decoder.DecodeUsage([]byte(line)); ok {
			t.Fatalf("expected no observation, got %+v", observation)
		}
	}
}

// Grok repeats every counter inside usage.modelUsage.<model>. The trace-level
// capture walks nested objects, so a careless widening would add each turn
// twice. Verified against a real session: 3 turns summing to 28393 output
// tokens parse to exactly 28393, not 56786.
func TestParseGrokTraceDoesNotDoubleCountModelUsageBreakdown(t *testing.T) {
	usage := `"usage":{"inputTokens":100,"outputTokens":300,"totalTokens":400,` +
		`"modelUsage":{"grok-4.6-build":{"inputTokens":100,"outputTokens":300,"totalTokens":400}}}`
	body := `{"timestamp":1788194984,"params":{"sessionId":"s1","update":{"sessionUpdate":"turn_completed","prompt_id":"p1",` + usage + `}}}` + "\n" +
		`{"timestamp":1788194990,"params":{"sessionId":"s1","update":{"sessionUpdate":"turn_completed","prompt_id":"p2",` + usage + `}}}` + "\n"
	path := grokSessionFixture(t, "%2FUsers%2Fdev%2Fproj%2Fagentload", "s1", body)

	trace, err := parseGrokTrace(path)
	if err != nil {
		t.Fatalf("parseGrokTrace: %v", err)
	}
	if trace == nil {
		t.Fatalf("expected trace")
	}
	if trace.TokenUsage.OutputTokens != 600 {
		t.Fatalf("expected 2 turns x 300 = 600 output tokens, got %d (the per-model breakdown must not be added twice)", trace.TokenUsage.OutputTokens)
	}
	if trace.TokenUsage.InputTokens != 200 {
		t.Fatalf("expected 200 input tokens, got %d", trace.TokenUsage.InputTokens)
	}
}
