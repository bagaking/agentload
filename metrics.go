package main

import (
	"sort"
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

func concurrencyAt(intervals []Interval, at time.Time) int {
	if at.IsZero() {
		return 0
	}
	count := 0
	for _, interval := range intervals {
		if interval.Start.IsZero() || interval.End.IsZero() {
			continue
		}
		if (interval.Start.Equal(at) || interval.Start.Before(at)) && interval.End.After(at) {
			count++
		}
	}
	return count
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
		if len(out) > 0 && bucket == lastBucket {
			out[len(out)-1] = point
			continue
		}
		out = append(out, point)
		lastBucket = bucket
	}
	return out
}

func appendRuntimeSample(samples []TrendPoint, sample TrendPoint, cutoff time.Time) []TrendPoint {
	out := samples[:0]
	for _, existing := range samples {
		ts, err := time.Parse(time.RFC3339, existing.At)
		if err != nil || ts.Before(cutoff) {
			continue
		}
		out = append(out, existing)
	}
	if len(out) > 0 && out[len(out)-1].At == sample.At {
		out[len(out)-1] = sample
		return out
	}
	out = append(out, sample)
	sort.Slice(out, func(i, j int) bool {
		ti, errI := time.Parse(time.RFC3339, out[i].At)
		tj, errJ := time.Parse(time.RFC3339, out[j].At)
		if errI != nil || errJ != nil {
			return out[i].At < out[j].At
		}
		return ti.Before(tj)
	})
	return out
}

func runtimeSamplesForWindow(samples []TrendPoint, from, to time.Time) []TrendPoint {
	out := make([]TrendPoint, 0, len(samples))
	for _, sample := range samples {
		ts, err := time.Parse(time.RFC3339, sample.At)
		if err != nil {
			continue
		}
		if ts.Before(from) || ts.After(to) {
			continue
		}
		out = append(out, sample)
	}
	return out
}

func sortIntervals(intervals []Interval) {
	sort.Slice(intervals, func(i, j int) bool {
		if intervals[i].Start.Equal(intervals[j].Start) {
			return intervals[i].End.Before(intervals[j].End)
		}
		return intervals[i].Start.Before(intervals[j].Start)
	})
}
