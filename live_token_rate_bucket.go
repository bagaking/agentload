package main

import (
	"math"
	"sort"
	"time"
)

type liveTokenRateBucketKey struct {
	UnixSecond int64
	Session    string
}

type liveTokenRateBucketAccumulator map[liveTokenRateBucketKey]int64

func (buckets liveTokenRateBucketAccumulator) add(event liveTokenRateEvent, now time.Time) {
	if event.Tokens <= 0 || now.IsZero() {
		return
	}
	start, end := event.observedWindow()
	if start.IsZero() || end.IsZero() {
		return
	}
	cutoff := now.Add(-liveTokenRateWindow)
	latest := now.Add(liveTokenRateFutureSkew)
	if end.Before(cutoff) || start.After(latest) {
		return
	}
	tokens := liveTokenRateEventTokensInWindow(event, now, liveTokenRateWindow, liveTokenRateFutureSkew)
	if tokens <= 0 {
		return
	}
	if !end.After(start) {
		buckets.addTokens(start, event.Session, tokens)
		return
	}
	if start.Before(cutoff) {
		start = cutoff
	}
	if end.After(latest) {
		end = latest
	}
	duration := end.Sub(start)
	if duration <= 0 {
		buckets.addTokens(start, event.Session, tokens)
		return
	}
	assigned := int64(0)
	for bucketStart := start.Truncate(liveTokenRateBucketWidth); bucketStart.Before(end); bucketStart = bucketStart.Add(liveTokenRateBucketWidth) {
		bucketEnd := bucketStart.Add(liveTokenRateBucketWidth)
		overlapEnd := bucketEnd
		if overlapEnd.After(end) {
			overlapEnd = end
		}
		cumulative := liveTokenRateProportionalTokens(tokens, overlapEnd.Sub(start), duration)
		buckets.addTokens(bucketStart, event.Session, cumulative-assigned)
		assigned = cumulative
	}
}

func (buckets liveTokenRateBucketAccumulator) addTokens(at time.Time, session string, tokens int64) {
	if tokens <= 0 {
		return
	}
	key := liveTokenRateBucketKey{
		UnixSecond: at.Truncate(liveTokenRateBucketWidth).Unix(),
		Session:    session,
	}
	buckets[key] = liveTokenRateSaturatingAdd(buckets[key], tokens)
}

func (buckets liveTokenRateBucketAccumulator) events() []liveTokenRateEvent {
	events := make([]liveTokenRateEvent, 0, len(buckets))
	for key, tokens := range buckets {
		start := time.Unix(key.UnixSecond, 0).UTC()
		events = append(events, liveTokenRateEvent{
			Start: start, End: start.Add(liveTokenRateBucketWidth), At: start.Add(liveTokenRateBucketWidth),
			Tokens: tokens, Session: key.Session,
		})
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Start.Equal(events[j].Start) {
			return events[i].Session < events[j].Session
		}
		return events[i].Start.Before(events[j].Start)
	})
	return events
}

func liveTokenRateProportionalTokens(tokens int64, elapsed, duration time.Duration) int64 {
	if tokens <= 0 || elapsed <= 0 || duration <= 0 {
		return 0
	}
	if elapsed >= duration {
		return tokens
	}
	scaled := math.Round(float64(tokens) * float64(elapsed) / float64(duration))
	if scaled <= 0 {
		return 0
	}
	if scaled >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(scaled)
}
