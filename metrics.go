package main

import (
	"sort"
	"strconv"
	"time"
)

func buildSessionSpans(traces map[string]*SessionTrace, minInterval time.Duration) []Interval {
	out := make([]Interval, 0, len(traces))
	for _, trace := range traces {
		if trace == nil || trace.FirstEvent.IsZero() {
			continue
		}
		span, ok := normalizedInterval(Interval{
			Tool:      trace.Tool,
			SessionID: trace.SessionID,
			Path:      trace.Path,
			Project:   trace.Project,
			Start:     trace.FirstEvent,
			End:       trace.LastEvent,
		}, minInterval)
		if ok {
			out = append(out, span)
		}
	}
	sortIntervals(out)
	return out
}

func buildBurstSpans(traces map[string]*SessionTrace, idleGap, minInterval time.Duration) []Interval {
	if idleGap <= 0 {
		idleGap = 90 * time.Second
	}
	out := make([]Interval, 0, len(traces))
	for _, trace := range traces {
		if trace == nil || len(trace.EventTimes) == 0 {
			continue
		}
		events := append([]time.Time(nil), trace.EventTimes...)
		sort.Slice(events, func(i, j int) bool {
			return events[i].Before(events[j])
		})
		start := events[0]
		prev := events[0]
		for _, ts := range events[1:] {
			if ts.Sub(prev) > idleGap {
				span, ok := normalizedInterval(Interval{
					Tool:      trace.Tool,
					SessionID: trace.SessionID,
					Path:      trace.Path,
					Project:   trace.Project,
					Start:     start,
					End:       prev,
				}, minInterval)
				if ok {
					out = append(out, span)
				}
				start = ts
			}
			prev = ts
		}
		span, ok := normalizedInterval(Interval{
			Tool:      trace.Tool,
			SessionID: trace.SessionID,
			Path:      trace.Path,
			Project:   trace.Project,
			Start:     start,
			End:       prev,
		}, minInterval)
		if ok {
			out = append(out, span)
		}
	}
	sortIntervals(out)
	return out
}

type sessionDurationMetrics struct {
	FirstEventAt            time.Time
	ObservedDurationSeconds int
	ActiveDurationSeconds   int
	IdleDurationSeconds     int
}

func buildSessionDurationMetrics(trace *SessionTrace, idleGap, minInterval time.Duration) (sessionDurationMetrics, bool) {
	if trace == nil || trace.FirstEvent.IsZero() {
		return sessionDurationMetrics{}, false
	}
	sessionSpan, ok := normalizedInterval(Interval{
		Tool:      trace.Tool,
		SessionID: trace.SessionID,
		Path:      trace.Path,
		Project:   trace.Project,
		Start:     trace.FirstEvent,
		End:       trace.LastEvent,
	}, minInterval)
	if !ok {
		return sessionDurationMetrics{}, false
	}

	activeDuration := time.Duration(0)
	if len(trace.EventTimes) > 0 {
		for _, span := range buildBurstSpans(map[string]*SessionTrace{trace.Path + "\x00" + trace.SessionID: trace}, idleGap, minInterval) {
			activeDuration += span.End.Sub(span.Start)
		}
	}
	observedDuration := sessionSpan.End.Sub(sessionSpan.Start)
	if activeDuration > observedDuration {
		activeDuration = observedDuration
	}
	idleDuration := observedDuration - activeDuration
	if idleDuration < 0 {
		idleDuration = 0
	}
	return sessionDurationMetrics{
		FirstEventAt:            sessionSpan.Start,
		ObservedDurationSeconds: wholeSeconds(observedDuration),
		ActiveDurationSeconds:   wholeSeconds(activeDuration),
		IdleDurationSeconds:     wholeSeconds(idleDuration),
	}, true
}

func wholeSeconds(duration time.Duration) int {
	if duration <= 0 {
		return 0
	}
	seconds := int(duration / time.Second)
	if duration%time.Second != 0 {
		seconds++
	}
	return seconds
}

func normalizedInterval(in Interval, minInterval time.Duration) (Interval, bool) {
	if in.Start.IsZero() && in.End.IsZero() {
		return Interval{}, false
	}
	if in.Start.IsZero() {
		in.Start = in.End
	}
	if in.End.IsZero() {
		in.End = in.Start
	}
	if in.End.Before(in.Start) {
		in.End = in.Start
	}
	if minInterval <= 0 {
		minInterval = 15 * time.Second
	}
	if in.End.Sub(in.Start) < minInterval {
		in.End = in.Start.Add(minInterval)
	}
	return in, true
}

func peakConcurrency(intervals []Interval, windowStart, windowEnd time.Time) PeakPoint {
	if windowStart.IsZero() || windowEnd.IsZero() || !windowEnd.After(windowStart) {
		return PeakPoint{}
	}
	type point struct {
		At    time.Time
		Kind  int
		Delta int
	}
	points := make([]point, 0, len(intervals)*2)
	for _, interval := range intervals {
		if interval.End.Before(windowStart) || !interval.Start.Before(windowEnd) {
			continue
		}
		start := interval.Start
		if start.Before(windowStart) {
			start = windowStart
		}
		end := interval.End
		if end.After(windowEnd) {
			end = windowEnd
		}
		if !end.After(start) {
			continue
		}
		points = append(points,
			point{At: start, Kind: 1, Delta: 1},
			point{At: end, Kind: 0, Delta: -1},
		)
	}
	if len(points) == 0 {
		return PeakPoint{}
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].At.Equal(points[j].At) {
			return points[i].Kind < points[j].Kind
		}
		return points[i].At.Before(points[j].At)
	})
	current := 0
	best := PeakPoint{}
	for _, p := range points {
		current += p.Delta
		if p.Delta > 0 && current > best.Value {
			best.Value = current
			best.At = p.At.Format(time.RFC3339)
		}
	}
	return best
}

type trendSpec struct {
	label string
	span  time.Duration
	step  time.Duration
}

var defaultTrendSpecs = []trendSpec{
	{label: "1D", span: 24 * time.Hour, step: 30 * time.Minute},
	{label: "3D", span: 3 * 24 * time.Hour, step: 90 * time.Minute},
	{label: "7D", span: 7 * 24 * time.Hour, step: 3 * time.Hour},
	{label: "15D", span: 15 * 24 * time.Hour, step: 6 * time.Hour},
	{label: "30D", span: 30 * 24 * time.Hour, step: 12 * time.Hour},
}

func buildTranscriptTrendWindows(data *TranscriptData, now time.Time, sourceLookback time.Duration) TrendSet {
	if data == nil || now.IsZero() {
		return TrendSet{}
	}
	configuredSourceFrom := now.Add(-sourceLookback)
	actualSourceFrom, hasTranscriptEvidence := transcriptEvidenceStart(data, configuredSourceFrom, now)
	trends := TrendSet{Windows: make([]TrendWindow, 0, len(defaultTrendSpecs))}
	for _, spec := range defaultTrendSpecs {
		from := now.Add(-spec.span)
		pointsAt := trendPointTimes(from, now, spec.step)
		activeBurst := concurrencySeries(data.BurstSpans, pointsAt)
		sessions := concurrencySeries(data.SessionSpans, pointsAt)
		window := TrendWindow{
			Range:              spec.label,
			From:               from.Format(time.RFC3339),
			To:                 now.Format(time.RFC3339),
			GranularitySeconds: int(spec.step / time.Second),
			HistoryComplete:    hasTranscriptEvidence && !actualSourceFrom.After(from),
			Points:             make([]TrendPoint, 0, len(pointsAt)),
		}
		if hasTranscriptEvidence {
			window.SourceFrom = actualSourceFrom.Format(time.RFC3339)
			window.SourceLookbackHours = int(now.Sub(actualSourceFrom) / time.Hour)
		}
		for index, at := range pointsAt {
			point := TrendPoint{
				At: at.Format(time.RFC3339),
			}
			if hasTranscriptEvidence && !at.Before(actualSourceFrom) {
				point.ActiveBurstConcurrency = activeBurst[index]
				point.HasActiveBurst = true
				point.SessionConcurrency = sessions[index]
				point.HasSessionConcurrency = true
				point.TranscriptSampled = true
			}
			window.Points = append(window.Points, point)
		}
		trends.Windows = append(trends.Windows, window)
	}
	return trends
}

func transcriptEvidenceStart(data *TranscriptData, configuredSourceFrom, now time.Time) (time.Time, bool) {
	if data == nil || now.IsZero() {
		return time.Time{}, false
	}
	if configuredSourceFrom.After(now) {
		configuredSourceFrom = now
	}

	var earliest time.Time
	consider := func(ts time.Time) {
		if ts.IsZero() || ts.Before(configuredSourceFrom) || ts.After(now) {
			return
		}
		if earliest.IsZero() || ts.Before(earliest) {
			earliest = ts
		}
	}

	for _, trace := range data.Traces {
		if trace == nil {
			continue
		}
		for _, ts := range trace.EventTimes {
			consider(ts)
		}
	}

	considerOverlapStarts := func(intervals []Interval) {
		for _, interval := range intervals {
			start := interval.Start
			end := interval.End
			if start.IsZero() && end.IsZero() {
				continue
			}
			if start.IsZero() {
				start = end
			}
			if end.IsZero() {
				end = start
			}
			if end.Before(start) {
				end = start
			}
			if end.Before(configuredSourceFrom) || start.After(now) {
				continue
			}
			if start.Before(configuredSourceFrom) {
				start = configuredSourceFrom
			}
			consider(start)
		}
	}

	considerOverlapStarts(data.SessionSpans)
	considerOverlapStarts(data.BurstSpans)

	if earliest.IsZero() {
		return time.Time{}, false
	}
	return earliest, true
}

func buildRealtimeTrendWindows(samples []TrendPoint, now time.Time) TrendSet {
	if now.IsZero() {
		return TrendSet{}
	}
	normalized := normalizeRuntimeSamples(samples)
	sourceFrom := time.Time{}
	if len(normalized) > 0 {
		sourceFrom = normalized[0].At
	}
	trends := TrendSet{Windows: make([]TrendWindow, 0, len(defaultTrendSpecs))}
	for _, spec := range defaultTrendSpecs {
		from := now.Add(-spec.span)
		window := TrendWindow{
			Range:              spec.label,
			From:               from.Format(time.RFC3339),
			To:                 now.Format(time.RFC3339),
			GranularitySeconds: int(spec.step / time.Second),
			HistoryComplete:    !sourceFrom.IsZero() && !sourceFrom.After(from),
			Points:             bucketRuntimeSamples(normalized, from, now, spec.step),
		}
		if !sourceFrom.IsZero() {
			window.SourceFrom = sourceFrom.Format(time.RFC3339)
			window.SourceLookbackHours = int(now.Sub(sourceFrom) / time.Hour)
		}
		trends.Windows = append(trends.Windows, window)
	}
	return trends
}

func trendPointTimes(from, to time.Time, step time.Duration) []time.Time {
	if from.IsZero() || to.IsZero() || step <= 0 {
		return nil
	}
	points := []time.Time{}
	for at := from; !at.After(to); at = at.Add(step) {
		points = append(points, at)
	}
	if len(points) == 0 || !points[len(points)-1].Equal(to) {
		points = append(points, to)
	}
	return points
}

func concurrencySeries(intervals []Interval, points []time.Time) []int {
	values := make([]int, len(points))
	if len(intervals) == 0 || len(points) == 0 {
		return values
	}
	type event struct {
		At    time.Time
		Kind  int
		Delta int
	}
	events := make([]event, 0, len(intervals)*2)
	from := points[0]
	to := points[len(points)-1]
	for _, interval := range intervals {
		if interval.Start.IsZero() || interval.End.IsZero() {
			continue
		}
		if interval.End.Before(from) || !interval.Start.Before(to) {
			continue
		}
		start := interval.Start
		if start.Before(from) {
			start = from
		}
		end := interval.End
		if end.After(to) {
			end = to
		}
		if !end.After(start) {
			continue
		}
		events = append(events,
			event{At: start, Kind: 1, Delta: 1},
			event{At: end, Kind: 0, Delta: -1},
		)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].At.Equal(events[j].At) {
			return events[i].Kind < events[j].Kind
		}
		return events[i].At.Before(events[j].At)
	})
	current := 0
	eventIndex := 0
	for pointIndex, at := range points {
		for eventIndex < len(events) && (events[eventIndex].At.Before(at) || events[eventIndex].At.Equal(at)) {
			current += events[eventIndex].Delta
			eventIndex++
		}
		values[pointIndex] = current
	}
	return values
}

type runtimeTrendSample struct {
	At    time.Time
	Point TrendPoint
}

const throughputTrendMaxPoints = 240

const (
	throughputSeriesKindMinuteRollup = "minute_rollup"
	throughputSeriesKindLegacy       = "legacy_rolling_rate"
)

var throughputRollupWindows = []time.Duration{
	time.Minute,
	5 * time.Minute,
	15 * time.Minute,
}

type throughputTimedMinute struct {
	at   time.Time
	fact ThroughputMinuteFact
}

func normalizeRuntimeSamples(samples []TrendPoint) []runtimeTrendSample {
	out := make([]runtimeTrendSample, 0, len(samples))
	for _, sample := range samples {
		if !sample.RuntimeSampled {
			continue
		}
		at, err := time.Parse(time.RFC3339, sample.At)
		if err != nil {
			continue
		}
		sample.At = at.Format(time.RFC3339)
		out = append(out, runtimeTrendSample{
			At:    at,
			Point: sample,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].At.Before(out[j].At)
	})
	return out
}

func bucketRuntimeSamples(samples []runtimeTrendSample, from, to time.Time, step time.Duration) []TrendPoint {
	if len(samples) == 0 || step <= 0 || from.IsZero() || to.IsZero() || !to.After(from) {
		return nil
	}
	out := make([]TrendPoint, 0, len(samples))
	lastBucket := -1
	for _, sample := range samples {
		if sample.At.Before(from) || sample.At.After(to) {
			continue
		}
		bucket := int(sample.At.Sub(from) / step)
		point := sample.Point
		point.At = sample.At.Format(time.RFC3339)
		point.RuntimeSampled = true
		point.OutputTokensPerSecond = 0
		point.HasOutputTokensPerSecond = false
		point.OutputTokenThroughputState = ""
		point.OutputTokenThroughputWindowSeconds = 0
		point.OutputTokenActiveSessions = 0
		point.HasOutputTokenActiveSessions = false
		point.OutputTokenProjects = nil
		point.ThroughputSampled = false
		if len(out) > 0 && bucket == lastBucket {
			out[len(out)-1] = point
			continue
		}
		out = append(out, point)
		lastBucket = bucket
	}
	return out
}

func buildThroughputTrendWindows(minutes []ThroughputMinuteFact, legacy []LegacyThroughputFact, now time.Time) TrendSet {
	if now.IsZero() {
		return TrendSet{}
	}
	type sourceSeries struct {
		key          string
		kind         string
		window       time.Duration
		samples      []runtimeTrendSample
		allowCurrent bool
	}
	sources := make([]sourceSeries, 0, len(throughputRollupWindows)+2)
	for _, window := range throughputRollupWindows {
		sources = append(sources, sourceSeries{
			key:          "minute:" + formatDurationSeconds(window),
			kind:         throughputSeriesKindMinuteRollup,
			window:       window,
			samples:      rollupThroughputMinutes(minutes, window),
			allowCurrent: true,
		})
	}
	legacyByWindow := map[int][]LegacyThroughputFact{}
	for _, fact := range legacy {
		if fact.WindowSeconds > 0 {
			legacyByWindow[fact.WindowSeconds] = append(legacyByWindow[fact.WindowSeconds], fact)
		}
	}
	legacyWindows := make([]int, 0, len(legacyByWindow))
	for window := range legacyByWindow {
		legacyWindows = append(legacyWindows, window)
	}
	sort.Ints(legacyWindows)
	for _, windowSeconds := range legacyWindows {
		window := time.Duration(windowSeconds) * time.Second
		sources = append(sources, sourceSeries{
			key:     "legacy:" + formatDurationSeconds(window),
			kind:    throughputSeriesKindLegacy,
			window:  window,
			samples: legacyThroughputSamples(legacyByWindow[windowSeconds]),
		})
	}

	trends := TrendSet{Windows: make([]TrendWindow, 0, len(defaultTrendSpecs))}
	for _, spec := range defaultTrendSpecs {
		from := now.Add(-spec.span)
		window := TrendWindow{
			Range:            spec.label,
			From:             from.Format(time.RFC3339),
			To:               now.Format(time.RFC3339),
			ThroughputSeries: make([]ThroughputTrendSeries, 0, len(sources)),
		}
		for _, source := range sources {
			rangeSamples := throughputSamplesInRange(source.samples, from, now)
			points, granularity := timeDistributedThroughputSamples(rangeSamples, throughputTrendMaxPoints)
			series := ThroughputTrendSeries{
				Key:                source.key,
				Kind:               source.kind,
				WindowSeconds:      int(source.window / time.Second),
				GranularitySeconds: int(granularity / time.Second),
				Summary:            summarizeThroughputTrend(rangeSamples, now, source.allowCurrent),
				Points:             points,
			}
			if len(source.samples) > 0 {
				sourceFrom := source.samples[0].At
				series.SourceFrom = sourceFrom.Format(time.RFC3339)
				series.HistoryComplete = !sourceFrom.After(from)
			}
			window.ThroughputSeries = append(window.ThroughputSeries, series)
		}
		trends.Windows = append(trends.Windows, window)
	}
	return trends
}

func rollupThroughputMinutes(minutes []ThroughputMinuteFact, window time.Duration) []runtimeTrendSample {
	windowCount := int(window / throughputMinuteResolution)
	if windowCount <= 0 {
		return nil
	}
	timed := make([]throughputTimedMinute, 0, len(minutes))
	for _, minute := range minutes {
		at, err := time.Parse(time.RFC3339, minute.At)
		if err != nil || !at.Equal(at.Truncate(throughputMinuteResolution)) {
			continue
		}
		timed = append(timed, throughputTimedMinute{at: at, fact: cloneThroughputMinute(minute)})
	}
	sort.Slice(timed, func(i, j int) bool { return timed[i].at.Before(timed[j].at) })
	out := make([]runtimeTrendSample, 0, len(timed))
	runStart := 0
	for index := range timed {
		if index > 0 && timed[index].at.Sub(timed[index-1].at) != throughputMinuteResolution {
			out = append(out, runtimeTrendSample{
				At: timed[index-1].at.Add(throughputMinuteResolution),
				Point: TrendPoint{
					At:                                 timed[index-1].at.Add(throughputMinuteResolution).Format(time.RFC3339),
					OutputTokenThroughputState:         liveTokenRateStateNoData,
					OutputTokenThroughputWindowSeconds: int(window / time.Second),
					ThroughputSampled:                  true,
				},
			})
			runStart = index
		}
		if index-runStart+1 < windowCount {
			continue
		}
		windowMinutes := timed[index-windowCount+1 : index+1]
		point := throughputRollupPoint(windowMinutes, window)
		out = append(out, runtimeTrendSample{At: timed[index].at, Point: point})
	}
	return out
}

func throughputRollupPoint(minutes []throughputTimedMinute, window time.Duration) TrendPoint {
	latest := minutes[len(minutes)-1]
	point := TrendPoint{
		At:                                 latest.at.Format(time.RFC3339),
		OutputTokenThroughputState:         latest.fact.State,
		OutputTokenThroughputWindowSeconds: int(window / time.Second),
		ThroughputSampled:                  true,
	}
	for _, minute := range minutes {
		if minute.fact.OutputTokens == nil {
			point.OutputTokenThroughputState = combinedThroughputMissingState(point.OutputTokenThroughputState, minute.fact.State)
			return point
		}
	}
	projectTokens := map[string]int64{}
	projectSessions := map[string]map[string]struct{}{}
	allSessions := map[string]struct{}{}
	var tokens int64
	for _, minute := range minutes {
		tokens = liveTokenRateSaturatingAdd(tokens, *minute.fact.OutputTokens)
		for _, session := range minute.fact.SessionHashes {
			allSessions[session] = struct{}{}
		}
		for _, project := range minute.fact.Projects {
			projectTokens[project.Project] = liveTokenRateSaturatingAdd(projectTokens[project.Project], project.OutputTokens)
			if projectSessions[project.Project] == nil {
				projectSessions[project.Project] = map[string]struct{}{}
			}
			for _, session := range project.SessionHashes {
				projectSessions[project.Project][session] = struct{}{}
			}
		}
	}
	point.OutputTokensPerSecond = float64(tokens) / window.Seconds()
	point.HasOutputTokensPerSecond = true
	point.OutputTokenActiveSessions = len(allSessions)
	point.HasOutputTokenActiveSessions = true
	point.OutputTokenThroughputState = liveTokenRateStateZero
	if tokens > 0 {
		point.OutputTokenThroughputState = liveTokenRateStateLive
	}
	// One floor minute makes the whole rolled-up window a floor: the sum can
	// only be missing tokens, never carrying extra.
	for _, minute := range minutes {
		if minute.fact.Coverage == liveTokenRateCoveragePartial {
			point.OutputTokenThroughputCoverage = liveTokenRateCoveragePartial
			break
		}
	}
	point.OutputTokenProjects = make([]LiveTokenRateProjectSample, 0, len(projectTokens))
	for project, projectTokens := range projectTokens {
		if projectTokens <= 0 {
			continue
		}
		point.OutputTokenProjects = append(point.OutputTokenProjects, LiveTokenRateProjectSample{
			Project:               project,
			OutputTokensPerSecond: float64(projectTokens) / window.Seconds(),
			ActiveSessions:        len(projectSessions[project]),
		})
	}
	sort.Slice(point.OutputTokenProjects, func(i, j int) bool {
		if point.OutputTokenProjects[i].OutputTokensPerSecond == point.OutputTokenProjects[j].OutputTokensPerSecond {
			return point.OutputTokenProjects[i].Project < point.OutputTokenProjects[j].Project
		}
		return point.OutputTokenProjects[i].OutputTokensPerSecond > point.OutputTokenProjects[j].OutputTokensPerSecond
	})
	return point
}

func combinedThroughputMissingState(left, right string) string {
	priority := map[string]int{
		liveTokenRateStateNoData:      1,
		liveTokenRateStateStale:       2,
		liveTokenRateStateUnavailable: 3,
	}
	if priority[right] > priority[left] {
		return right
	}
	return left
}

func legacyThroughputSamples(facts []LegacyThroughputFact) []runtimeTrendSample {
	out := make([]runtimeTrendSample, 0, len(facts))
	for _, fact := range facts {
		at, ok := parseObservedTime(fact.At)
		if !ok || fact.WindowSeconds <= 0 {
			continue
		}
		point := TrendPoint{
			At:                                 at.Format(time.RFC3339Nano),
			OutputTokenThroughputState:         fact.State,
			OutputTokenThroughputWindowSeconds: fact.WindowSeconds,
			ThroughputSampled:                  true,
		}
		if fact.OutputTokensPerSecond != nil && fact.Projects != nil {
			point.OutputTokensPerSecond = *fact.OutputTokensPerSecond
			point.HasOutputTokensPerSecond = true
			point.OutputTokenActiveSessions = fact.ActiveSessions
			point.HasOutputTokenActiveSessions = true
			point.OutputTokenProjects = cloneLiveTokenRateProjectSamples(fact.Projects)
		}
		out = append(out, runtimeTrendSample{At: at, Point: point})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func throughputSamplesInRange(samples []runtimeTrendSample, from, to time.Time) []runtimeTrendSample {
	filtered := make([]runtimeTrendSample, 0, len(samples))
	for _, sample := range samples {
		if !sample.At.Before(from) && !sample.At.After(to) {
			filtered = append(filtered, sample)
		}
	}
	return filtered
}

func timeDistributedThroughputSamples(filtered []runtimeTrendSample, maxPoints int) ([]TrendPoint, time.Duration) {
	if len(filtered) == 0 {
		return nil, 0
	}
	if maxPoints < 2 || len(filtered) <= maxPoints {
		return throughputTrendPoints(filtered), medianTrendSampleInterval(filtered)
	}
	span := filtered[len(filtered)-1].At.Sub(filtered[0].At)
	divisor := time.Duration(maxPoints - 1)
	step := (span + divisor - 1) / divisor
	if step <= 0 {
		return throughputTrendPoints(filtered[:1]), 0
	}
	out := []runtimeTrendSample{filtered[0]}
	lastBucket := 0
	for _, sample := range filtered[1:] {
		bucket := int(sample.At.Sub(filtered[0].At) / step)
		if bucket == 0 {
			if !sample.Point.HasOutputTokensPerSecond {
				out[0] = sample
			}
			continue
		}
		if len(out) > 0 && bucket == lastBucket {
			if out[len(out)-1].Point.HasOutputTokensPerSecond || !sample.Point.HasOutputTokensPerSecond {
				out[len(out)-1] = sample
			}
			continue
		}
		out = append(out, sample)
		lastBucket = bucket
	}
	return throughputTrendPoints(out), step
}

func summarizeThroughputTrend(samples []runtimeTrendSample, now time.Time, allowCurrent bool) *ThroughputTrendSummary {
	rates := make([]float64, 0, len(samples))
	sum := 0.0
	maximum := 0.0
	windowSeconds := 0
	for _, sample := range samples {
		if !sample.Point.HasOutputTokensPerSecond {
			continue
		}
		if windowSeconds == 0 {
			windowSeconds = sample.Point.OutputTokenThroughputWindowSeconds
		}
		rate := sample.Point.OutputTokensPerSecond
		rates = append(rates, rate)
		sum += rate
		if len(rates) == 1 || rate > maximum {
			maximum = rate
		}
	}
	if len(rates) == 0 {
		return nil
	}
	sort.Float64s(rates)
	p95Index := (95*len(rates) + 99) / 100
	summary := &ThroughputTrendSummary{
		Max:           maximum,
		P95:           rates[p95Index-1],
		Avg:           sum / float64(len(rates)),
		WindowSeconds: windowSeconds,
		SampleCount:   len(rates),
	}
	latest := samples[len(samples)-1]
	currentAge := now.Sub(latest.At)
	if allowCurrent && latest.Point.HasOutputTokensPerSecond && currentAge >= -liveTokenRateFutureSkew && currentAge <= throughputMinuteResolution+liveTokenRateFutureSkew {
		current := latest.Point.OutputTokensPerSecond
		summary.Current = &current
		summary.CurrentAt = latest.At.Format(time.RFC3339)
	}
	return summary
}

func formatDurationSeconds(duration time.Duration) string {
	return strconv.Itoa(int(duration / time.Second))
}

func throughputTrendPoints(samples []runtimeTrendSample) []TrendPoint {
	points := make([]TrendPoint, 0, len(samples))
	for _, sample := range samples {
		point := sample.Point
		point.At = sample.At.Format(time.RFC3339Nano)
		points = append(points, point)
	}
	return points
}

func medianTrendSampleInterval(samples []runtimeTrendSample) time.Duration {
	if len(samples) < 2 {
		return 0
	}
	intervals := make([]time.Duration, 0, len(samples)-1)
	for index := 1; index < len(samples); index++ {
		if interval := samples[index].At.Sub(samples[index-1].At); interval > 0 {
			intervals = append(intervals, interval)
		}
	}
	if len(intervals) == 0 {
		return 0
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i] < intervals[j] })
	return intervals[len(intervals)/2]
}

func sortIntervals(intervals []Interval) {
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].Start.Equal(intervals[j].Start) {
			return intervals[i].End.Before(intervals[j].End)
		}
		return intervals[i].Start.Before(intervals[j].Start)
	})
}
