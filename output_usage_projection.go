package main

import "time"

// Both archive replay and live collection use the same native counter
// transition. A first cumulative counter establishes a baseline; message
// identities count only increases. The caller owns durable or bounded state.
func outputUsageEvent(o liveTokenRateObservation, previous int64, previousAt time.Time, initialized bool, session string) liveTokenRateEvent {
	if o.Cumulative {
		if !initialized || o.OutputTokens <= previous || o.At.Before(previousAt) {
			return liveTokenRateEvent{}
		}
		return newLiveTokenRateIntervalEvent(previousAt, o.At, o.OutputTokens-previous, session)
	}
	tokens := o.OutputTokens
	if initialized {
		tokens = max(int64(0), tokens-previous)
	}
	return liveTokenRateEvent{At: o.At, Tokens: tokens, Session: session}
}

// Partition one native observation against fixed wall-time boundaries. Endpoint
// differences preserve token conservation, including clipped ranges. Minute
// replay and second-level live buckets therefore sum the same token intervals.
func partitionOutputUsage(event liveTokenRateEvent, cutoff, latest time.Time, width time.Duration, emit func(time.Time, int64)) {
	if event.Tokens <= 0 || width <= 0 {
		return
	}
	start, end := event.observedWindow()
	if start.IsZero() || end.IsZero() || end.Before(cutoff) || start.After(latest) {
		return
	}
	if !end.After(start) {
		if !event.At.Before(cutoff) && !event.At.After(latest) {
			emit(event.At.Truncate(width), event.Tokens)
		}
		return
	}
	originalStart := start
	duration := end.Sub(start)
	start = maxTime(start, cutoff)
	end = minTime(end, latest)
	for at := start.Truncate(width); at.Before(end); at = at.Add(width) {
		left := maxTime(start, at)
		right := minTime(end, at.Add(width))
		tokens := liveTokenRateProportionalTokens(event.Tokens, right.Sub(originalStart), duration) - liveTokenRateProportionalTokens(event.Tokens, left.Sub(originalStart), duration)
		if tokens > 0 {
			emit(at, tokens)
		}
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
