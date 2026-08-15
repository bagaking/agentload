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
	if now.IsZero() {
		return
	}
	partitionOutputUsage(event, now.Add(-liveTokenRateWindow), now.Add(liveTokenRateFutureSkew), liveTokenRateBucketWidth, func(at time.Time, tokens int64) { buckets.addTokens(at, event.Session, tokens) })
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
