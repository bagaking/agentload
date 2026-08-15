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

// Freshness is the recent-movement vocabulary. It lives here because the
// semantic matrix says recent movement is decided by transcript event age and
// nothing else, and because a string literal spelled inline in an aggregation
// is exactly how that rule gets quietly restated somewhere it does not hold.
//
// TestMetricVocabularyStaysInTheSemanticLayer fails if these spellings appear
// outside this file.
const (
	freshnessActive  = "active"
	freshnessIdle    = "idle"
	freshnessStale   = "stale"
	freshnessUnknown = "unknown"
)

// Attention state is a routing signal, not a productivity judgment. It is
// emitted only from session role, transcript timing, and recent movement.
const (
	attentionStateWorking     = "working"
	attentionStateNeedsReview = "needs_review"
	attentionStateUnknown     = "unknown"
)

// freshnessFromEventAge is the only place an age becomes a freshness bucket.
// Callers pass the already-clamped age; a session with no transcript timing
// never reaches here, because absent timing is unknown rather than stale.
func freshnessFromEventAge(age, idleGap time.Duration) string {
	switch {
	case age <= idleGap:
		return freshnessActive
	case age <= staleSessionThreshold(idleGap):
		return freshnessIdle
	default:
		return freshnessStale
	}
}

// freshnessRank orders buckets most-recent-first for display. "unknown" sorts
// last: it is an absence of evidence, not a degree of staleness.
func freshnessRank(freshness string) int {
	switch freshness {
	case freshnessActive:
		return 0
	case freshnessIdle:
		return 1
	case freshnessStale:
		return 2
	default:
		return 3
	}
}

// freshnessCountsAsPresent reports the buckets that mean "this session still
// has live evidence behind it". Stale is deliberately excluded: an overlap
// cluster built from stale rows describes history, not a present collision.
func freshnessCountsAsPresent(freshness string) bool {
	return freshness == freshnessActive || freshness == freshnessIdle
}

// baselineStatusIdle is the diagnostics baseline-status family's word for "no
// recent movement right now". It is spelled the same as freshnessIdle and
// means something different, which is the whole reason it is named here.
//
// Freshness describes one session's transcript age. A baseline status
// describes a whole metric row. A machine whose every session is stale reports
// baseline status idle -- zero recent movement -- while not one session is
// freshnessIdle. Reading either number as the other is wrong, and the two are
// one keystroke apart, so they sit next to each other where the difference is
// visible instead of in two files that never get read together.
const baselineStatusIdle = "idle"

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
// "needs human review" cue: a main session whose local log stopped moving
// recently enough that a human may still be the thing it is waiting for. It
// describes observed evidence only; it is not a judgment that the session is
// stuck.
//
// Stale is deliberately excluded. Stale means "last event older than
// staleSessionThreshold" with no upper bound, so on a machine that leaves
// terminals parked it matches sessions whose transcript last moved weeks ago
// while a process lingers -- measured here at 65 of 106 live sessions, every
// one of them stale and none idle. A cue that fires on 61% of sessions ranks
// nothing, and "may be waiting for human input" is false for a session that
// last moved 23 days ago. Staleness itself is not lost: coordination_risk's
// stale_session_count, the sessions_without_recent_event signal, and the
// session_hygiene insight each report it under its own name.
func sessionNeedsReviewObservation(role string, observation liveSessionObservation) bool {
	if normalizedRole(role) != "main" || observation.ActiveBurst {
		return false
	}
	return observation.Freshness == freshnessIdle
}

func attentionStateForSession(role string, observation liveSessionObservation) (string, string) {
	if observation.MissingTranscript || normalizedRole(role) == "unknown" {
		return attentionStateUnknown, "session role or transcript timing is unavailable"
	}
	if observation.ActiveBurst {
		return attentionStateWorking, "recent transcript movement observed"
	}
	if sessionNeedsReviewObservation(role, observation) {
		return attentionStateNeedsReview, "main session has measured evidence but no recent movement"
	}
	// Stale lands in unknown by design (see docs/neutral-observation-principles.md:
	// attention routing deliberately stops at the idle window). But it got there
	// from a measured transcript age, so it must not borrow the reason belonging
	// to sessions whose timing is genuinely absent -- that would report a
	// successful measurement as missing evidence. Measured here at 81 of 92
	// unknown sessions.
	if observation.Freshness == freshnessStale {
		return attentionStateUnknown, "transcript movement measured but older than the idle window"
	}
	return attentionStateUnknown, "no current attention state evidence"
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
