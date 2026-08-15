package main

import (
	"agentload/internal/snapshot"
	"math"
	"strings"
	"time"
)

const (
	liveTokenRateStateLive                  = "live"
	liveTokenRateStateZero                  = "zero"
	liveTokenRateStateNoData                = "no_data"
	liveTokenRateStateStale                 = "stale"
	liveTokenRateStateUnavailable           = "unavailable"
	liveTokenRateUnavailableNotConfigured   = "not_configured"
	liveTokenRateUnavailableWatchIncomplete = "watch_incomplete"

	// Coverage qualifies a real number rather than replacing it. "partial"
	// means the rate is a floor measured from a subset of eligible transcripts.
	liveTokenRateCoveragePartial = "partial"

	liveTokenRateBasis             = "output_tokens"
	liveTokenRateSource            = "local_transcript_usage"
	liveTokenRateMethod            = "trailing_wall_time"
	liveTokenRateUnassignedProject = "unassigned"
)

type liveTokenRateEvent struct {
	Start   time.Time
	End     time.Time
	At      time.Time
	Tokens  int64
	Session string
}

type liveTokenRateFacts struct {
	Configured  bool
	Initialized bool
	Limited     bool
	// Partial marks a sample measured from a known subset of the eligible
	// transcripts. Unlike Limited it still carries a number: every token in it
	// was really observed, so the rate is a floor rather than an unknown.
	Partial           bool
	TrackedFileCount  int
	EligibleFileCount int
	UnavailableReason string
	TokensInWindow    int64
	ActiveSessions    int
	LatestSignal      time.Time
	LatestEvent       time.Time
	Window            time.Duration
	SampleInterval    time.Duration
	StaleAfter        time.Duration
	SampledAt         time.Time
}

type liveTokenRateProjectFacts struct {
	TokensInWindow int64
	Sessions       map[string]struct{}
}

func newLiveTokenRateIntervalEvent(start, end time.Time, tokens int64, session string) liveTokenRateEvent {
	if start.IsZero() {
		start = end
	}
	if end.IsZero() {
		end = start
	}
	if end.Before(start) {
		start, end = end, start
	}
	return liveTokenRateEvent{
		Start: start, End: end, At: end, Tokens: max(int64(0), tokens), Session: session,
	}
}

func (event liveTokenRateEvent) observedWindow() (time.Time, time.Time) {
	start := event.Start
	end := event.End
	if start.IsZero() {
		start = event.At
	}
	if end.IsZero() {
		end = event.At
	}
	if end.Before(start) {
		start, end = end, start
	}
	return start, end
}

// liveTokenRateEventTokensInWindow keeps point observations discrete. A safe
// cumulative delta covers only the interval between its two observations, so a
// trailing window receives the proportional overlap instead of the full delta.
func liveTokenRateEventTokensInWindow(event liveTokenRateEvent, now time.Time, window, futureSkew time.Duration) int64 {
	if event.Tokens <= 0 || window <= 0 {
		return 0
	}
	cutoff := now.Add(-window)
	maxEnd := now.Add(futureSkew)
	start, end := event.observedWindow()
	if end.Before(cutoff) || start.After(maxEnd) {
		return 0
	}
	duration := end.Sub(start)
	if duration <= 0 {
		if !event.At.Before(cutoff) && !event.At.After(maxEnd) {
			return event.Tokens
		}
		return 0
	}
	if start.Before(cutoff) {
		start = cutoff
	}
	if end.After(maxEnd) {
		end = maxEnd
	}
	overlap := end.Sub(start)
	if overlap <= 0 {
		return 0
	}
	scaled := math.Round(float64(event.Tokens) * overlap.Seconds() / duration.Seconds())
	if scaled <= 0 {
		return 0
	}
	if scaled >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(scaled)
}

func liveTokenRateWindowFacts(events []liveTokenRateEvent, now time.Time, window, futureSkew time.Duration) (int64, int) {
	tokens, activeSessions, _ := liveTokenRateWindowBreakdown(events, nil, now, window, futureSkew)
	return tokens, activeSessions
}

func liveTokenRateWindowBreakdown(events []liveTokenRateEvent, sessionProjects map[string]string, now time.Time, window, futureSkew time.Duration) (int64, int, map[string]liveTokenRateProjectFacts) {
	var tokens int64
	sessions := map[string]struct{}{}
	hasAnonymous := false
	projects := map[string]liveTokenRateProjectFacts{}
	for _, event := range events {
		contribution := liveTokenRateEventTokensInWindow(event, now, window, futureSkew)
		if contribution <= 0 {
			continue
		}
		tokens = liveTokenRateSaturatingAdd(tokens, contribution)
		if session := strings.TrimSpace(event.Session); session != "" {
			sessions[session] = struct{}{}
		} else {
			hasAnonymous = true
		}
		project := strings.TrimSpace(sessionProjects[event.Session])
		if project == "" {
			project = liveTokenRateUnassignedProject
		}
		facts := projects[project]
		facts.TokensInWindow = liveTokenRateSaturatingAdd(facts.TokensInWindow, contribution)
		if facts.Sessions == nil {
			facts.Sessions = map[string]struct{}{}
		}
		facts.Sessions[event.Session] = struct{}{}
		projects[project] = facts
	}
	if hasAnonymous {
		return tokens, len(sessions) + 1, projects
	}
	return tokens, len(sessions), projects
}

func liveTokenRateSampleFromFacts(facts liveTokenRateFacts) snapshot.LiveTokenRateSample {
	now := facts.SampledAt
	if now.IsZero() {
		now = time.Now()
	}
	window := facts.Window
	if window <= 0 {
		window = liveTokenRateWindow
	}
	sample := snapshot.LiveTokenRateSample{
		State:                 liveTokenRateStateNoData,
		Basis:                 liveTokenRateBasis,
		Source:                liveTokenRateSource,
		Method:                liveTokenRateMethod,
		WindowSeconds:         int(window / time.Second),
		SampleIntervalSeconds: int(facts.SampleInterval / time.Second),
		ActiveSessions:        facts.ActiveSessions,
		SampledAt:             now.Format(time.RFC3339Nano),
	}
	if !facts.LatestSignal.IsZero() {
		sample.LatestSignalAt = facts.LatestSignal.Format(time.RFC3339Nano)
	}
	if !facts.LatestEvent.IsZero() {
		sample.LatestEventAt = facts.LatestEvent.Format(time.RFC3339Nano)
	}
	if !facts.Configured {
		sample.State = liveTokenRateStateUnavailable
		sample.UnavailableReason = liveTokenRateUnavailableNotConfigured
		return sample
	}
	// Degraded coverage is only unknown when nothing positive was measured
	// behind it. With real tokens in the window, an incomplete file-event
	// interval means "there may be more than this", not "this is wrong", so the
	// reading survives below as a floor. With zero tokens it means exactly the
	// opposite: a zero floor is trivially true and reads as "nothing is
	// happening" when the truth is "we may have missed all of it" — so that
	// case still fails closed.
	if facts.Limited && facts.TokensInWindow <= 0 {
		sample.State = liveTokenRateStateUnavailable
		sample.UnavailableReason = facts.UnavailableReason
		return sample
	}
	if !facts.Initialized {
		return sample
	}
	if facts.StaleAfter > 0 && (facts.LatestSignal.IsZero() || now.Sub(facts.LatestSignal) > facts.StaleAfter) {
		sample.State = liveTokenRateStateStale
		return sample
	}
	rate := float64(max(int64(0), facts.TokensInWindow)) / window.Seconds()
	sample.OutputTokensPerSecond = &rate
	// A partial sample is a floor, not an unknown: the tokens in it were all
	// really observed, just not from every eligible transcript. Blanking it
	// would hide throughput exactly when there is the most of it, so the number
	// ships with the coverage that produced it and the UI labels it a floor.
	if facts.Partial {
		sample.Coverage = liveTokenRateCoveragePartial
		sample.TrackedFileCount = facts.TrackedFileCount
		sample.EligibleFileCount = facts.EligibleFileCount
	}
	// A watcher gap cannot be quantified the way a file cap can, so the floor
	// ships without counts and names what degraded it instead.
	if facts.Limited {
		sample.Coverage = liveTokenRateCoveragePartial
		sample.CoverageReason = facts.UnavailableReason
	}
	if facts.TokensInWindow > 0 {
		sample.State = liveTokenRateStateLive
	} else {
		sample.State = liveTokenRateStateZero
	}
	return sample
}

func liveTokenRateSaturatingAdd(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	if right < 0 && left < math.MinInt64-right {
		return math.MinInt64
	}
	return left + right
}

type SessionMetricFacts struct {
	Role             string
	KnownSession     bool
	RecentMovement   bool
	RecentSession    bool
	StaleSession     bool
	ProcessPressure  int
	ProcessIDs       []int
	ProcessResources ProcessResourceTotals
}

type ProcessResourceTotals struct {
	CPUPercent  float64
	MemoryBytes int64
}

type ProcessEvidenceMetricFacts struct {
	Role           string
	RecentMovement bool
	KnownSession   bool
}

// sessionNeedsReviewObservation is the backend source of truth for the
// "needs human review" cue: a main session whose local log shows no recent
// movement and whose freshness observation is idle or stale. It describes
// observed evidence only; it is not a judgment that the session is stuck.
func sessionNeedsReviewObservation(role string, observation liveSessionObservation) bool {
	if normalizedRole(role) != "main" || observation.ActiveBurst {
		return false
	}
	return observation.Freshness == "idle" || observation.Freshness == "stale"
}

func metricFactsForLiveSession(session snapshot.LiveSession, observation liveSessionObservation) SessionMetricFacts {
	role := observeSessionRole(session)
	processIDs := make([]int, 0, len(session.Processes))
	for pid := range session.Processes {
		processIDs = append(processIDs, pid)
	}
	return SessionMetricFacts{
		Role:            role.Role,
		KnownSession:    true,
		RecentMovement:  observation.ActiveBurst,
		RecentSession:   observation.Recent,
		StaleSession:    observation.Stale,
		ProcessPressure: len(session.Processes),
		ProcessIDs:      processIDs,
	}
}

func metricFactsForSessionSnapshot(session snapshot.LiveSessionSnapshot) SessionMetricFacts {
	return SessionMetricFacts{
		Role:            session.SessionRole,
		KnownSession:    true,
		RecentMovement:  session.ActiveBurst,
		ProcessPressure: session.ProcessCount,
		ProcessResources: ProcessResourceTotals{
			CPUPercent:  session.ProcessCPUPercent,
			MemoryBytes: session.ProcessMemoryBytes,
		},
	}
}

func metricFactsForProcessSessionEvidence(evidence snapshot.ProcessSessionEvidence) ProcessEvidenceMetricFacts {
	return ProcessEvidenceMetricFacts{
		Role:           normalizedRole(evidence.Role),
		RecentMovement: evidence.ActiveBurst,
		KnownSession:   evidence.SessionID != "",
	}
}
