package main

import (
	"agentload/internal/snapshot"
	"bufio"
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	liveTokenRateSampleInterval    = 30 * time.Second
	liveTokenRateWindow            = 5 * time.Minute
	liveTokenRateStaleAfter        = 5 * time.Minute
	liveTokenRateRecentFileAge     = 15 * time.Minute
	liveTokenRateTrackedFileMaxAge = 30 * time.Minute
	liveTokenRateMessageRetention  = 10 * time.Minute
	liveTokenRateBaselineReadLimit = 512 * 1024
	liveTokenRateMaxJSONLineBytes  = 16 * 1024 * 1024
	liveTokenRateBucketWidth       = time.Second
	liveTokenRateMaxFiles          = 256
	liveTokenRateMaxMessages       = 2048
	liveTokenRateFutureSkew        = 5 * time.Second
	liveTokenRateFingerprintBytes  = 128
)

type liveTokenRateMessageUsage struct {
	Output   int64
	LastSeen time.Time
	order    *list.Element
}

type liveTokenRateTrackedFile struct {
	Tool             string
	Offset           int64
	LastSeen         time.Time
	Info             os.FileInfo
	Fingerprint      [sha256.Size]byte
	HasFingerprint   bool
	TotalInitialized bool
	LastTotal        int64
	LastTotalAt      time.Time
	MessageUsage     map[string]*liveTokenRateMessageUsage
	MessageOrder     *list.List

	// messageStateValidated records that MessageUsage and MessageOrder hold the
	// same identities one-for-one, with every usage.order pointing at its own
	// element. The mutators below preserve that invariant, so validation runs
	// once per foreign state instead of once per message. The zero value keeps
	// unvalidated state honest: a struct literal or a fresh file re-validates
	// before anything trusts the order list.
	messageStateValidated bool
}

type liveTokenRateObservation struct {
	At              time.Time
	OutputTokens    int64
	Cumulative      bool
	MessageIdentity string
}

type liveTokenRatePublished struct {
	Configured    bool
	Initialized   bool
	LimitedUntil  time.Time
	LimitedReason string
	LatestSignal  time.Time
	LatestEvent   time.Time
	Buckets       []liveTokenRateEvent
	Projects      map[string]string

	TrackedFiles  int
	EligibleFiles int
}

// liveTokenRateSampler keeps collection baselines private to one background
// owner. API clients read the independently published bucket snapshot and never
// advance file offsets or cumulative counters.
type liveTokenRateSampler struct {
	pollMu sync.Mutex

	adapters        *codingAgentRegistry
	evidenceIndex   *transcriptEvidenceIndex
	files           map[string]liveTokenRateTrackedFile
	buckets         []liveTokenRateEvent
	lastPoll        time.Time
	initialized     bool
	latestSignal    time.Time
	latestEvent     time.Time
	limitedUntil    time.Time
	limitedReason   string
	sessionProjects map[string]string
	// eligibleFileCount is how many recent transcripts the cap had to choose
	// from. Tracking fewer than this makes the rate a floor, not an unknown.
	eligibleFileCount int
	throughputStore   *throughputHistoryStore
	coverageStart     time.Time
	lastMinuteEnd     time.Time

	publishedMu sync.RWMutex
	published   liveTokenRatePublished

	// lifecycleTransitionMu serializes start/stop across the join. Without a
	// separate transition lock, a new generation could start while the old
	// goroutine was still unwinding and mutate the shared polling baseline.
	lifecycleTransitionMu sync.Mutex
	lifecycleMu           sync.Mutex
	generation            uint64
	running               bool
	stop                  chan struct{}
	done                  chan struct{}
	cancel                context.CancelFunc
}

func newLiveTokenRateSampler(adapters *codingAgentRegistry, evidenceIndex *transcriptEvidenceIndex) *liveTokenRateSampler {
	if adapters == nil || evidenceIndex == nil {
		panic("coding agent registry and evidence index are required")
	}
	sampler := &liveTokenRateSampler{
		adapters:        adapters,
		evidenceIndex:   evidenceIndex,
		files:           map[string]liveTokenRateTrackedFile{},
		sessionProjects: map[string]string{},
	}
	sampler.publish(time.Now())
	return sampler
}

func liveTokenRateSessionKey(tool, path string) string {
	tool = strings.TrimSpace(strings.ToLower(tool))
	path = canonicalEvidencePath(path)
	if tool == "" || path == "" || path == "." {
		return ""
	}
	return tool + "\x00" + path
}

func liveTokenRateProjectsFromSessions(sessions []snapshot.LiveSessionSnapshot) map[string]string {
	projects := make(map[string]string, len(sessions))
	for _, session := range sessions {
		key := liveTokenRateSessionKey(session.Tool, session.Path)
		if key == "" {
			continue
		}
		project := strings.TrimSpace(session.Project)
		if project == "" {
			project = liveTokenRateUnassignedProject
		}
		if existing, ok := projects[key]; ok && existing != project {
			projects[key] = liveTokenRateUnassignedProject
			continue
		}
		projects[key] = project
	}
	return projects
}

func (sampler *liveTokenRateSampler) updateSnapshotProjects(projects map[string]string) {
	if sampler == nil {
		return
	}
	sampler.pollMu.Lock()
	sampler.sessionProjects = cloneLiveTokenRateProjects(projects)
	sampler.publishLocked(time.Now())
	sampler.pollMu.Unlock()
}

func (sampler *liveTokenRateSampler) bindThroughputHistory(store *throughputHistoryStore) {
	if sampler == nil {
		return
	}
	sampler.pollMu.Lock()
	sampler.throughputStore = store
	sampler.pollMu.Unlock()
}

func cloneLiveTokenRateProjects(projects map[string]string) map[string]string {
	cloned := make(map[string]string, len(projects))
	for session, project := range projects {
		session = strings.TrimSpace(session)
		project = strings.TrimSpace(project)
		if session == "" {
			continue
		}
		if project == "" {
			project = liveTokenRateUnassignedProject
		}
		cloned[session] = project
	}
	return cloned
}

func liveTokenRateProjectsForBuckets(buckets []liveTokenRateEvent, projects map[string]string) map[string]string {
	relevant := make(map[string]string)
	for _, bucket := range buckets {
		session := strings.TrimSpace(bucket.Session)
		if session == "" {
			continue
		}
		if _, done := relevant[session]; done {
			// One session owns many buckets in a window, and recovery below walks
			// the filesystem. Resolve each session once.
			continue
		}
		project := strings.TrimSpace(projects[session])
		if project == "" {
			// Only sessions with a live process reach the snapshot's project
			// map. A transcript that is still being written by a process we
			// could not map is real throughput, so derive its project from the
			// transcript path rather than bucketing it as unassigned.
			project = liveTokenRateProjectFromSessionKey(session)
		}
		if project == "" {
			project = liveTokenRateUnassignedProject
		}
		relevant[session] = project
	}
	return relevant
}

// liveTokenRateProjectFromSessionKey recovers a project name from a session key
// (tool\x00path). Claude stores transcripts under an encoded project directory,
// which already names the project; anything else is attributed the same way a
// session path is.
func liveTokenRateProjectFromSessionKey(session string) string {
	_, path, found := strings.Cut(session, "\x00")
	if !found || strings.TrimSpace(path) == "" {
		return ""
	}
	if project := extractClaudeProjectFromPath(path); project != "" && project != "unknown" {
		return project
	}
	// Anything else is a transcript store, not a checkout: codex writes
	// sessions/2026/09/14/rollout-<uuid>.jsonl, whose leaf is a day number that
	// passes every generic-name check and would fabricate a project called "14"
	// shared by every unrelated session of that day. Only claim a name when the
	// path actually sits inside a repository.
	dir := filepath.Dir(path)
	if repoRoot, _, _ := resolveRepoBoundary(dir); repoRoot == "" {
		return ""
	}
	return trustedPathProjectName(dir)
}

func (sampler *liveTokenRateSampler) start(interval time.Duration) {
	if sampler == nil {
		return
	}
	if interval < liveTokenRateSampleInterval {
		interval = liveTokenRateSampleInterval
	}
	sampler.startLoopWithContext(interval, func(ctx context.Context, now time.Time) {
		sampler.pollContext(ctx, now)
	})
}

// startLoop owns the sampler goroutine and accepts a time-only poll seam for
// deterministic lifecycle tests. Production uses startLoopWithContext so a
// shutdown can cancel an evidence read in progress.
func (sampler *liveTokenRateSampler) startLoop(interval time.Duration, poll func(time.Time)) {
	if sampler == nil || poll == nil {
		return
	}
	sampler.startLoopWithContext(interval, func(_ context.Context, now time.Time) {
		poll(now)
	})
}

func (sampler *liveTokenRateSampler) startLoopWithContext(interval time.Duration, poll func(context.Context, time.Time)) {
	if sampler == nil || poll == nil {
		return
	}
	if interval <= 0 {
		interval = liveTokenRateSampleInterval
	}
	sampler.lifecycleTransitionMu.Lock()
	defer sampler.lifecycleTransitionMu.Unlock()
	sampler.lifecycleMu.Lock()
	if sampler.running {
		sampler.lifecycleMu.Unlock()
		return
	}
	sampler.generation++
	generation := sampler.generation
	sampler.running = true
	sampler.stop = make(chan struct{})
	sampler.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	sampler.cancel = cancel
	stop := sampler.stop
	done := sampler.done

	go func() {
		defer recoverBackgroundPanic("live token rate sampler")
		defer sampler.releaseSamplerLifecycle(generation, done)
		runBackgroundStep("live token rate sampler", func() { poll(ctx, time.Now()) })
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				runBackgroundStep("live token rate sampler", func() { poll(ctx, now) })
			case <-stop:
				return
			}
		}
	}()
	sampler.lifecycleMu.Unlock()
}

// releaseSamplerLifecycle makes an unexpected sampler exit restartable. The
// stop path clears the same fields before waiting, so the identity check keeps
// an old goroutine from clobbering a newer generation's lifecycle state.
func (sampler *liveTokenRateSampler) releaseSamplerLifecycle(generation uint64, done chan struct{}) {
	defer close(done)
	sampler.lifecycleMu.Lock()
	if sampler.running && sampler.generation == generation && sampler.done == done {
		sampler.running = false
		sampler.stop = nil
		sampler.done = nil
		sampler.cancel = nil
	}
	sampler.lifecycleMu.Unlock()
}

func (sampler *liveTokenRateSampler) stopSampler() {
	if sampler == nil {
		return
	}
	sampler.lifecycleTransitionMu.Lock()
	defer sampler.lifecycleTransitionMu.Unlock()
	sampler.lifecycleMu.Lock()
	if !sampler.running {
		sampler.lifecycleMu.Unlock()
		return
	}
	stop := sampler.stop
	done := sampler.done
	cancel := sampler.cancel
	sampler.running = false
	sampler.stop = nil
	sampler.done = nil
	sampler.cancel = nil
	if stop != nil {
		close(stop)
	}
	sampler.lifecycleMu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

func (sampler *liveTokenRateSampler) poll(now time.Time) {
	sampler.pollContext(context.Background(), now)
}

func (sampler *liveTokenRateSampler) pollContext(ctx context.Context, now time.Time) {
	if sampler == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	sampler.pollMu.Lock()
	defer sampler.pollMu.Unlock()
	sampler.pollLocked(ctx, now)
	sampler.publishLocked(now)
}

func (sampler *liveTokenRateSampler) pollLocked(ctx context.Context, now time.Time) {
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return
	}
	if !sampler.lastPoll.IsZero() && now.Sub(sampler.lastPoll) < liveTokenRateSampleInterval {
		return
	}
	firstPoll := sampler.lastPoll.IsZero()
	observationGap := !firstPoll && now.Sub(sampler.lastPoll) > liveTokenRateWindow
	sampler.lastPoll = now
	if firstPoll || observationGap {
		sampler.coverageStart = now
		sampler.lastMinuteEnd = now.Truncate(throughputMinuteResolution)
	}
	if sampler.files == nil {
		sampler.files = map[string]liveTokenRateTrackedFile{}
	}
	sampler.syncEvidenceFilesLocked(ctx, now)
	if ctx.Err() != nil {
		return
	}
	for path, tracked := range sampler.files {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			delete(sampler.files, path)
			continue
		}
		if tracked.Info == nil || !os.SameFile(tracked.Info, info) {
			sampler.files[path] = sampler.rebaselineFile(path, tracked.Tool, info, now)
			continue
		}
		if info.Size() < tracked.Offset || (info.Size() == tracked.Info.Size() && !info.ModTime().Equal(tracked.Info.ModTime())) {
			sampler.files[path] = sampler.rebaselineFile(path, tracked.Tool, info, now)
			continue
		}
		if info.Size() == tracked.Info.Size() {
			if now.Sub(tracked.LastSeen) > liveTokenRateTrackedFileMaxAge {
				delete(sampler.files, path)
			}
			continue
		}
		if observationGap || !liveTokenRateBoundaryMatches(path, tracked) {
			sampler.files[path] = sampler.rebaselineFile(path, tracked.Tool, info, now)
			continue
		}
		decoder, ok := sampler.adapters.usageDecoder(tracked.Tool)
		if !ok {
			sampler.files[path] = sampler.rebaselineFile(path, tracked.Tool, info, now)
			continue
		}
		updated, buckets, latestSignal, latestEvent := liveTokenRateReadAppend(path, tracked, info, now, decoder)
		sampler.files[path] = updated
		if !latestSignal.IsZero() {
			sampler.markSignalLocked(latestSignal, now)
		}
		sampler.buckets = append(sampler.buckets, buckets...)
		if !latestEvent.IsZero() {
			sampler.markEventLocked(latestEvent, now)
		}
	}
	sampler.flushCompletedMinutesLocked(now)
	sampler.pruneLocked(now)
}

func (sampler *liveTokenRateSampler) flushCompletedMinutesLocked(now time.Time) {
	if sampler.throughputStore == nil || sampler.coverageStart.IsZero() || sampler.lastMinuteEnd.IsZero() {
		return
	}
	target := now.Truncate(throughputMinuteResolution)
	for sampler.lastMinuteEnd.Before(target) {
		minuteEnd := sampler.lastMinuteEnd.Add(throughputMinuteResolution)
		minuteStart := minuteEnd.Add(-throughputMinuteResolution)
		if minuteStart.Before(sampler.coverageStart) {
			sampler.lastMinuteEnd = minuteEnd
			continue
		}
		fact := sampler.minuteFactLocked(minuteStart, minuteEnd)
		if err := sampler.throughputStore.appendMinute(fact, now); err != nil {
			return
		}
		sampler.lastMinuteEnd = minuteEnd
	}
}

func (sampler *liveTokenRateSampler) minuteFactLocked(minuteStart, minuteEnd time.Time) ThroughputMinuteFact {
	tokens, _, projects := liveTokenRateWindowBreakdown(
		sampler.buckets,
		// Recover project names the same way the live sample does. Passing the
		// raw map here attributed every session without a mapped process to
		// "unassigned" *in the persisted history*, so the trend chart showed ~80%
		// unassigned while the live readout showed none — the same tokens, bucketed
		// two different ways depending on which path wrote them.
		liveTokenRateProjectsForBuckets(sampler.buckets, sampler.sessionProjects),
		minuteEnd,
		throughputMinuteResolution,
		0,
	)
	latestSignal := sampler.latestSignal
	latestEvent := sampler.latestEvent
	if latestSignal.After(minuteEnd.Add(liveTokenRateFutureSkew)) {
		latestSignal = time.Time{}
	}
	if latestEvent.After(minuteEnd.Add(liveTokenRateFutureSkew)) {
		latestEvent = time.Time{}
	}
	if tokens > 0 && latestSignal.IsZero() {
		latestSignal = minuteEnd
	}
	if tokens > 0 && latestEvent.IsZero() {
		latestEvent = minuteEnd
	}
	limitedStart := sampler.limitedUntil.Add(-liveTokenRateWindow)
	limited := sampler.limitedUntil.After(minuteStart) && limitedStart.Before(minuteEnd)
	sample := liveTokenRateSampleFromFacts(liveTokenRateFacts{
		Configured:        sampler.adapters.hasUsageRoots(),
		Initialized:       sampler.initialized && !latestSignal.IsZero(),
		Limited:           limited,
		UnavailableReason: sampler.limitedReason,
		Partial:           sampler.eligibleFileCount > len(sampler.files),
		TrackedFileCount:  len(sampler.files),
		EligibleFileCount: sampler.eligibleFileCount,
		TokensInWindow:    tokens,
		LatestSignal:      latestSignal,
		LatestEvent:       latestEvent,
		Window:            throughputMinuteResolution,
		SampleInterval:    liveTokenRateSampleInterval,
		StaleAfter:        liveTokenRateStaleAfter,
		SampledAt:         minuteEnd,
	})
	fact := ThroughputMinuteFact{
		At:                minuteEnd.Format(time.RFC3339),
		State:             sample.State,
		UnavailableReason: sample.UnavailableReason,
		Coverage:          sample.Coverage,
	}
	if sample.OutputTokensPerSecond == nil {
		return fact
	}
	fact.OutputTokens = &tokens
	allSessions := map[string]struct{}{}
	for project, projectFacts := range projects {
		if projectFacts.TokensInWindow <= 0 {
			continue
		}
		projectFact := ThroughputMinuteProjectFact{
			Project:      project,
			OutputTokens: projectFacts.TokensInWindow,
		}
		for session := range projectFacts.Sessions {
			hash := throughputSessionHash(session)
			projectFact.SessionHashes = append(projectFact.SessionHashes, hash)
			allSessions[hash] = struct{}{}
		}
		sort.Strings(projectFact.SessionHashes)
		fact.Projects = append(fact.Projects, projectFact)
	}
	for session := range allSessions {
		fact.SessionHashes = append(fact.SessionHashes, session)
	}
	sort.Strings(fact.SessionHashes)
	sort.Slice(fact.Projects, func(i, j int) bool { return fact.Projects[i].Project < fact.Projects[j].Project })
	if fact.Projects == nil {
		fact.Projects = []ThroughputMinuteProjectFact{}
	}
	return fact
}

func (sampler *liveTokenRateSampler) syncEvidenceFilesLocked(ctx context.Context, now time.Time) {
	if ctx == nil {
		ctx = context.Background()
	}
	indexed := sampler.evidenceIndex.snapshot(ctx, now.Add(-foregroundTranscriptMaxLookback), nil)
	if !indexed.Complete {
		sampler.markLimitedLocked(now, liveTokenRateUnavailableWatchIncomplete)
	}
	// The index is already sorted newest-modified first, so the files that walk
	// in first are the ones most likely to still be producing tokens.
	eligible := 0
	for _, candidate := range indexed.Files {
		path := candidate.File.Path
		if candidate.Info == nil || candidate.Info.Size() <= 0 || now.Sub(candidate.Info.ModTime()) > liveTokenRateRecentFileAge {
			continue
		}
		if _, ok := sampler.adapters.usageDecoder(candidate.File.Tool); !ok {
			continue
		}
		eligible++
		if _, tracked := sampler.files[path]; tracked {
			continue
		}
		if len(sampler.files) >= liveTokenRateMaxFiles && !sampler.evictColdestFileLocked(candidate.Info.ModTime()) {
			// Every tracked file is hotter than this candidate, so the cap is
			// holding the best subset available. Keep sampling and report the
			// coverage rather than dropping the metric.
			continue
		}
		sampler.files[path] = sampler.rebaselineFile(path, candidate.File.Tool, candidate.Info, now)
	}
	sampler.eligibleFileCount = eligible
}

// evictColdestFileLocked drops the least recently modified tracked file to make
// room for a hotter one, and reports whether it freed a slot. A file colder than
// everything already tracked frees nothing — the cap is then already holding the
// hottest set, so the caller keeps what it has.
func (sampler *liveTokenRateSampler) evictColdestFileLocked(hotterThan time.Time) bool {
	coldestPath := ""
	var coldest time.Time
	for path, tracked := range sampler.files {
		if tracked.Info == nil {
			return sampler.dropTrackedFileLocked(path)
		}
		if coldestPath == "" || tracked.Info.ModTime().Before(coldest) {
			coldestPath, coldest = path, tracked.Info.ModTime()
		}
	}
	if coldestPath == "" || !coldest.Before(hotterThan) {
		return false
	}
	return sampler.dropTrackedFileLocked(coldestPath)
}

func (sampler *liveTokenRateSampler) dropTrackedFileLocked(path string) bool {
	delete(sampler.files, path)
	return true
}

func (sampler *liveTokenRateSampler) rebaselineFile(path, tool string, info os.FileInfo, now time.Time) liveTokenRateTrackedFile {
	tracked := liveTokenRateTrackedFile{Tool: tool, LastSeen: now, Info: info}
	if info == nil || info.Size() <= 0 {
		return tracked
	}
	decoder, ok := sampler.adapters.usageDecoder(tool)
	if !ok {
		tracked.Offset = info.Size()
		return tracked
	}
	f, err := os.Open(path)
	if err != nil {
		return tracked
	}
	defer f.Close()
	if openedInfo, err := f.Stat(); err == nil {
		info = openedInfo
		tracked.Info = openedInfo
	}
	size := info.Size()
	readOffset := int64(0)
	if size > liveTokenRateBaselineReadLimit {
		readOffset = size - liveTokenRateBaselineReadLimit
	}
	if _, err := f.Seek(readOffset, io.SeekStart); err != nil {
		tracked.Offset = size
		return tracked
	}
	parseEnd, err := liveTokenRateLastCompleteLineOffset(f, readOffset, size)
	if err != nil {
		tracked.Offset = size
		return tracked
	}
	if parseEnd <= readOffset {
		if readOffset == 0 && liveTokenRateFileIsCompleteJSON(f, size) {
			parseEnd = size
		} else if readOffset > 0 {
			tracked.Offset = size
			tracked.Fingerprint, tracked.HasFingerprint = liveTokenRateBoundaryFingerprint(path, tracked.Offset)
			return tracked
		} else {
			return tracked
		}
	}
	scanner := bufio.NewScanner(io.NewSectionReader(f, readOffset, parseEnd-readOffset))
	scanner.Buffer(make([]byte, 0, 4*1024), liveTokenRateMaxJSONLineBytes)
	if readOffset > 0 && !scanner.Scan() {
		tracked.Offset = parseEnd
		tracked.Fingerprint, tracked.HasFingerprint = liveTokenRateBoundaryFingerprint(path, tracked.Offset)
		return tracked
	}
	for scanner.Scan() {
		observation, ok := decoder.DecodeUsage(scanner.Bytes())
		if !ok {
			continue
		}
		observation.At = normalizeLiveTokenRateSignalTime(observation.At, now)
		if observation.Cumulative {
			tracked.TotalInitialized = true
			tracked.LastTotal = observation.OutputTokens
			tracked.LastTotalAt = observation.At
		}
		if observation.MessageIdentity != "" {
			liveTokenRateRememberMessage(&tracked, observation.MessageIdentity, observation.OutputTokens, now)
		}
		sampler.markSignalLocked(observation.At, now)
	}
	liveTokenRatePruneMessages(&tracked, now)
	tracked.Offset = parseEnd
	tracked.Fingerprint, tracked.HasFingerprint = liveTokenRateBoundaryFingerprint(path, tracked.Offset)
	return tracked
}

func liveTokenRateFileIsCompleteJSON(file *os.File, size int64) bool {
	if file == nil || size <= 0 || size > liveTokenRateBaselineReadLimit {
		return false
	}
	data := make([]byte, size)
	n, err := file.ReadAt(data, 0)
	return err == nil && int64(n) == size && json.Valid(bytes.TrimSpace(data))
}

func liveTokenRateReadAppend(path string, tracked liveTokenRateTrackedFile, info os.FileInfo, now time.Time, decoder agentOutputUsageDecoder) (liveTokenRateTrackedFile, []liveTokenRateEvent, time.Time, time.Time) {
	if info == nil || info.Size() <= tracked.Offset {
		return tracked, nil, time.Time{}, time.Time{}
	}
	if decoder == nil {
		return tracked, nil, time.Time{}, time.Time{}
	}
	f, err := os.Open(path)
	if err != nil {
		return tracked, nil, time.Time{}, time.Time{}
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || openedInfo.Size() != info.Size() {
		return tracked, nil, time.Time{}, time.Time{}
	}
	parseEnd, err := liveTokenRateLastCompleteLineOffset(f, tracked.Offset, info.Size())
	if err != nil || parseEnd <= tracked.Offset {
		return tracked, nil, time.Time{}, time.Time{}
	}
	original := tracked
	tracked.MessageUsage, tracked.MessageOrder = cloneLiveTokenRateMessages(tracked.MessageUsage, tracked.MessageOrder)
	// The clone pairs every identity with exactly one element it owns, so the
	// append loop below starts from validated state regardless of the source.
	tracked.messageStateValidated = true
	buckets := liveTokenRateBucketAccumulator{}
	latestSignal := time.Time{}
	latestEvent := time.Time{}
	session := liveTokenRateSessionKey(tracked.Tool, path)
	scanner := bufio.NewScanner(io.NewSectionReader(f, tracked.Offset, parseEnd-tracked.Offset))
	scanner.Buffer(make([]byte, 0, 4*1024), liveTokenRateMaxJSONLineBytes)
	for scanner.Scan() {
		observation, ok := decoder.DecodeUsage(scanner.Bytes())
		if !ok {
			continue
		}
		observation.At = normalizeLiveTokenRateSignalTime(observation.At, now)
		if latestSignal.IsZero() || observation.At.After(latestSignal) {
			latestSignal = observation.At
		}
		tokens := observation.OutputTokens
		if observation.Cumulative {
			tokens = 0
			if tracked.TotalInitialized && observation.OutputTokens > tracked.LastTotal && !observation.At.Before(tracked.LastTotalAt) {
				buckets.add(newLiveTokenRateIntervalEvent(
					tracked.LastTotalAt, observation.At, observation.OutputTokens-tracked.LastTotal, session,
				), now)
				if latestEvent.IsZero() || observation.At.After(latestEvent) {
					latestEvent = observation.At
				}
			}
			tracked.TotalInitialized = true
			tracked.LastTotal = observation.OutputTokens
			tracked.LastTotalAt = observation.At
		} else if observation.MessageIdentity != "" {
			tokens = liveTokenRateMessageDelta(&tracked, observation.MessageIdentity, tokens, now)
		}
		if tokens > 0 {
			buckets.add(liveTokenRateEvent{At: observation.At, Tokens: tokens, Session: session}, now)
			if latestEvent.IsZero() || observation.At.After(latestEvent) {
				latestEvent = observation.At
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return original, nil, time.Time{}, time.Time{}
	}
	liveTokenRatePruneMessages(&tracked, now)
	tracked.Offset = parseEnd
	tracked.LastSeen = now
	tracked.Info = info
	tracked.Fingerprint, tracked.HasFingerprint = liveTokenRateBoundaryFingerprint(path, tracked.Offset)
	return tracked, buckets.events(), latestSignal, latestEvent
}

func liveTokenRateLastCompleteLineOffset(file *os.File, start, end int64) (int64, error) {
	if file == nil || end <= start {
		return start, nil
	}
	buffer := make([]byte, 64*1024)
	for cursor := end; cursor > start; {
		chunkStart := max(start, cursor-int64(len(buffer)))
		chunk := buffer[:cursor-chunkStart]
		n, err := file.ReadAt(chunk, chunkStart)
		if err != nil && err != io.EOF {
			return start, err
		}
		if index := bytes.LastIndexByte(chunk[:n], '\n'); index >= 0 {
			return chunkStart + int64(index) + 1, nil
		}
		cursor = chunkStart
	}
	return start, nil
}

func cloneLiveTokenRateMessages(source map[string]*liveTokenRateMessageUsage, order *list.List) (map[string]*liveTokenRateMessageUsage, *list.List) {
	if len(source) == 0 {
		return nil, nil
	}
	cloned := make(map[string]*liveTokenRateMessageUsage, len(source))
	clonedOrder := list.New()
	if order != nil {
		for element := order.Front(); element != nil; element = element.Next() {
			identity, _ := element.Value.(string)
			usage := source[identity]
			// A repeated identity in the source order would otherwise push a
			// second element while the map keeps only the last copy, leaving the
			// clone longer than its map with an orphaned element.
			if identity == "" || usage == nil || cloned[identity] != nil {
				continue
			}
			copy := *usage
			copy.order = clonedOrder.PushBack(identity)
			cloned[identity] = &copy
		}
	}
	validCount := 0
	for identity, usage := range source {
		if identity != "" && usage != nil {
			validCount++
		}
	}
	if len(cloned) != validCount {
		// A foreign or corrupted order cannot define a safe eviction sequence.
		// Rebuild deterministically from the retained timestamps rather than
		// relying on Go's deliberately randomized map iteration order.
		type entry struct {
			identity string
			usage    *liveTokenRateMessageUsage
		}
		entries := make([]entry, 0, validCount)
		for identity, usage := range source {
			if identity == "" || usage == nil {
				continue
			}
			entries = append(entries, entry{identity: identity, usage: usage})
		}
		sort.Slice(entries, func(i, j int) bool {
			left, right := entries[i].usage.LastSeen, entries[j].usage.LastSeen
			if left.IsZero() != right.IsZero() {
				return left.IsZero()
			}
			if left.Equal(right) {
				return entries[i].identity < entries[j].identity
			}
			return left.Before(right)
		})
		cloned = make(map[string]*liveTokenRateMessageUsage, len(entries))
		clonedOrder = list.New()
		for _, entry := range entries {
			copy := *entry.usage
			copy.order = clonedOrder.PushBack(entry.identity)
			cloned[entry.identity] = &copy
		}
		return cloned, clonedOrder
	}
	for identity, usage := range source {
		if identity == "" || usage == nil || cloned[identity] != nil {
			continue
		}
		copy := *usage
		copy.order = clonedOrder.PushBack(identity)
		cloned[identity] = &copy
	}
	return cloned, clonedOrder
}

func liveTokenRateMessageDelta(tracked *liveTokenRateTrackedFile, identity string, output int64, now time.Time) int64 {
	if tracked == nil || identity == "" {
		return output
	}
	previous, seen := tracked.MessageUsage[identity]
	delta := output
	if seen && previous != nil {
		delta = max(int64(0), output-previous.Output)
		output = max(output, previous.Output)
	}
	liveTokenRateRememberMessage(tracked, identity, output, now)
	return delta
}

func liveTokenRateRememberMessage(tracked *liveTokenRateTrackedFile, identity string, output int64, now time.Time) {
	if tracked == nil || identity == "" {
		return
	}
	liveTokenRateEnsureMessageState(tracked)
	if previous := tracked.MessageUsage[identity]; previous != nil {
		previous.Output = max(previous.Output, output)
		previous.LastSeen = now
		if previous.order == nil {
			previous.order = tracked.MessageOrder.PushBack(identity)
		} else {
			tracked.MessageOrder.MoveToBack(previous.order)
		}
		return
	}
	liveTokenRatePruneMessages(tracked, now)
	liveTokenRateEnsureMessageState(tracked)
	for len(tracked.MessageUsage) >= liveTokenRateMaxMessages {
		liveTokenRateForgetOldestMessage(tracked)
	}
	usage := &liveTokenRateMessageUsage{Output: max(int64(0), output), LastSeen: now}
	usage.order = tracked.MessageOrder.PushBack(identity)
	tracked.MessageUsage[identity] = usage
}

func liveTokenRateEnsureMessageState(tracked *liveTokenRateTrackedFile) {
	if tracked.MessageUsage == nil {
		tracked.MessageUsage = map[string]*liveTokenRateMessageUsage{}
	}
	if tracked.messageStateValidated && tracked.MessageOrder != nil {
		return
	}
	if len(tracked.MessageUsage) == 0 {
		tracked.MessageOrder = list.New()
		tracked.messageStateValidated = true
		return
	}
	if tracked.MessageOrder == nil {
		liveTokenRateRebuildMessageOrder(tracked)
		tracked.messageStateValidated = true
		return
	}
	if liveTokenRateRepairMessageOrder(tracked) {
		tracked.messageStateValidated = true
		return
	}
	liveTokenRateRebuildMessageOrder(tracked)
	tracked.messageStateValidated = true
}

func liveTokenRateRepairMessageOrder(tracked *liveTokenRateTrackedFile) bool {
	if tracked == nil || tracked.MessageOrder == nil {
		return false
	}
	seen := make(map[string]struct{}, len(tracked.MessageUsage))
	count := 0
	for element := tracked.MessageOrder.Front(); element != nil; element = element.Next() {
		identity, _ := element.Value.(string)
		usage := tracked.MessageUsage[identity]
		if identity == "" || usage == nil {
			return false
		}
		if _, ok := seen[identity]; ok {
			return false
		}
		seen[identity] = struct{}{}
		usage.order = element
		count++
	}
	return count == len(tracked.MessageUsage)
}

func liveTokenRateRebuildMessageOrder(tracked *liveTokenRateTrackedFile) {
	if tracked == nil {
		return
	}
	type entry struct {
		identity string
		usage    *liveTokenRateMessageUsage
	}
	entries := make([]entry, 0, len(tracked.MessageUsage))
	for identity, usage := range tracked.MessageUsage {
		if identity == "" || usage == nil {
			delete(tracked.MessageUsage, identity)
			continue
		}
		usage.order = nil
		entries = append(entries, entry{identity: identity, usage: usage})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left := entries[i].usage.LastSeen
		right := entries[j].usage.LastSeen
		if left.IsZero() != right.IsZero() {
			return left.IsZero()
		}
		if left.Equal(right) {
			return entries[i].identity < entries[j].identity
		}
		return left.Before(right)
	})
	tracked.MessageOrder = list.New()
	for _, entry := range entries {
		entry.usage.order = tracked.MessageOrder.PushBack(entry.identity)
	}
}

func liveTokenRatePruneMessages(tracked *liveTokenRateTrackedFile, now time.Time) {
	if tracked == nil {
		return
	}
	liveTokenRateEnsureMessageState(tracked)
	if len(tracked.MessageUsage) == 0 || tracked.MessageOrder == nil {
		return
	}
	for tracked.MessageOrder.Len() > 0 {
		identity, _ := tracked.MessageOrder.Front().Value.(string)
		usage := tracked.MessageUsage[identity]
		if usage != nil && !usage.LastSeen.IsZero() && now.Sub(usage.LastSeen) <= liveTokenRateMessageRetention {
			break
		}
		liveTokenRateForgetOldestMessage(tracked)
	}
	for len(tracked.MessageUsage) > liveTokenRateMaxMessages {
		liveTokenRateForgetOldestMessage(tracked)
	}
	if len(tracked.MessageUsage) == 0 {
		tracked.MessageUsage = nil
		tracked.MessageOrder = nil
		tracked.messageStateValidated = false
	}
}

func liveTokenRateForgetOldestMessage(tracked *liveTokenRateTrackedFile) {
	if tracked == nil {
		return
	}
	if tracked.MessageOrder != nil {
		if element := tracked.MessageOrder.Front(); element != nil {
			identity, _ := element.Value.(string)
			tracked.MessageOrder.Remove(element)
			if usage := tracked.MessageUsage[identity]; usage != nil {
				usage.order = nil
			}
			delete(tracked.MessageUsage, identity)
			return
		}
	}
	// The order list is empty while the map still holds entries, so no eviction
	// could name one. Drop a single entry directly: the callers bound the map,
	// not the list, and would otherwise spin without ever reaching their bound.
	for identity, usage := range tracked.MessageUsage {
		if usage != nil {
			usage.order = nil
		}
		delete(tracked.MessageUsage, identity)
		return
	}
}

func liveTokenRateBoundaryFingerprint(path string, offset int64) ([sha256.Size]byte, bool) {
	var empty [sha256.Size]byte
	if offset <= 0 {
		return sha256.Sum256(nil), true
	}
	start := max(int64(0), offset-liveTokenRateFingerprintBytes)
	f, err := os.Open(path)
	if err != nil {
		return empty, false
	}
	defer f.Close()
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return empty, false
	}
	data, err := io.ReadAll(io.LimitReader(f, offset-start))
	if err != nil || int64(len(data)) != offset-start {
		return empty, false
	}
	return sha256.Sum256(data), true
}

func liveTokenRateBoundaryMatches(path string, tracked liveTokenRateTrackedFile) bool {
	if !tracked.HasFingerprint {
		return false
	}
	current, ok := liveTokenRateBoundaryFingerprint(path, tracked.Offset)
	return ok && current == tracked.Fingerprint
}

func (sampler *liveTokenRateSampler) markSignalLocked(at, now time.Time) {
	at = normalizeLiveTokenRateSignalTime(at, now)
	sampler.initialized = true
	if sampler.latestSignal.IsZero() || at.After(sampler.latestSignal) {
		sampler.latestSignal = at
	}
}

func (sampler *liveTokenRateSampler) markEventLocked(at, now time.Time) {
	at = normalizeLiveTokenRateSignalTime(at, now)
	sampler.markSignalLocked(at, now)
	if sampler.latestEvent.IsZero() || at.After(sampler.latestEvent) {
		sampler.latestEvent = at
	}
}

func normalizeLiveTokenRateSignalTime(at, now time.Time) time.Time {
	if now.IsZero() {
		now = time.Now()
	}
	if at.IsZero() || at.After(now.Add(liveTokenRateFutureSkew)) {
		return now
	}
	return at
}

func (sampler *liveTokenRateSampler) markLimitedLocked(now time.Time, reason string) {
	until := now.Add(liveTokenRateWindow)
	if until.After(sampler.limitedUntil) {
		sampler.limitedUntil = until
		sampler.limitedReason = reason
	}
}

func (sampler *liveTokenRateSampler) pruneLocked(now time.Time) {
	buckets := liveTokenRateBucketAccumulator{}
	for _, event := range sampler.buckets {
		buckets.add(event, now)
	}
	sampler.buckets = buckets.events()
}

func (sampler *liveTokenRateSampler) publish(now time.Time) {
	sampler.pollMu.Lock()
	defer sampler.pollMu.Unlock()
	sampler.publishLocked(now)
}

func (sampler *liveTokenRateSampler) publishLocked(now time.Time) {
	published := liveTokenRatePublished{
		Configured:    sampler.adapters.hasUsageRoots(),
		Initialized:   sampler.initialized,
		LimitedUntil:  sampler.limitedUntil,
		LimitedReason: sampler.limitedReason,
		LatestSignal:  sampler.latestSignal,
		LatestEvent:   sampler.latestEvent,
		Buckets:       append([]liveTokenRateEvent(nil), sampler.buckets...),
		Projects:      liveTokenRateProjectsForBuckets(sampler.buckets, sampler.sessionProjects),
		TrackedFiles:  len(sampler.files),
		EligibleFiles: sampler.eligibleFileCount,
	}
	sampler.publishedMu.Lock()
	sampler.published = published
	sampler.publishedMu.Unlock()
}

func (sampler *liveTokenRateSampler) sample(now time.Time) snapshot.LiveTokenRateSample {
	if now.IsZero() {
		now = time.Now()
	}
	if sampler == nil {
		return liveTokenRateSampleFromFacts(liveTokenRateFacts{
			Window: liveTokenRateWindow, SampleInterval: liveTokenRateSampleInterval,
			StaleAfter: liveTokenRateStaleAfter, SampledAt: now,
		})
	}
	sampler.publishedMu.RLock()
	published := sampler.published
	sampler.publishedMu.RUnlock()
	tokens, activeSessions, projectFacts := liveTokenRateWindowBreakdown(published.Buckets, published.Projects, now, liveTokenRateWindow, liveTokenRateFutureSkew)
	sample := liveTokenRateSampleFromFacts(liveTokenRateFacts{
		Configured:        published.Configured,
		Initialized:       published.Initialized,
		Limited:           published.LimitedUntil.After(now),
		Partial:           published.EligibleFiles > published.TrackedFiles,
		TrackedFileCount:  published.TrackedFiles,
		EligibleFileCount: published.EligibleFiles,
		UnavailableReason: published.LimitedReason,
		TokensInWindow:    tokens,
		ActiveSessions:    activeSessions,
		LatestSignal:      published.LatestSignal,
		LatestEvent:       published.LatestEvent,
		Window:            liveTokenRateWindow,
		SampleInterval:    liveTokenRateSampleInterval,
		StaleAfter:        liveTokenRateStaleAfter,
		SampledAt:         now,
	})
	if sample.OutputTokensPerSecond != nil {
		sample.Projects = liveTokenRateProjectSamples(projectFacts, liveTokenRateWindow)
	}
	return sample
}

func liveTokenRateProjectSamples(projects map[string]liveTokenRateProjectFacts, window time.Duration) []snapshot.LiveTokenRateProjectSample {
	out := make([]snapshot.LiveTokenRateProjectSample, 0, len(projects))
	for project, facts := range projects {
		if facts.TokensInWindow <= 0 {
			continue
		}
		out = append(out, snapshot.LiveTokenRateProjectSample{
			Project:               project,
			OutputTokensPerSecond: float64(facts.TokensInWindow) / window.Seconds(),
			ActiveSessions:        len(facts.Sessions),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OutputTokensPerSecond != out[j].OutputTokensPerSecond {
			return out[i].OutputTokensPerSecond > out[j].OutputTokensPerSecond
		}
		return out[i].Project < out[j].Project
	})
	return out
}
