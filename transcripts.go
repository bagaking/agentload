package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	foregroundTranscriptMinLookback   = 2 * time.Hour
	foregroundTranscriptMaxLookback   = 6 * time.Hour
	transcriptHealthyWaitRetryLimit   = 2
	transcriptParseErrorRetryInterval = time.Minute
)

type Observer struct {
	cfg           Config
	adapters      *codingAgentRegistry
	evidenceIndex *transcriptEvidenceIndex
	mu            sync.Mutex
	cache         transcriptCacheState
	inflight      map[string]*transcriptScanFlight
	fileCache     map[string]fileTraceCache
	processMu     sync.RWMutex
	lastProcesses []LiveProcess
}

func newObserver(cfg Config) *Observer {
	return newObserverWithRegistry(cfg, defaultCodingAgentRegistry(cfg))
}

func newObserverWithRegistry(cfg Config, adapters *codingAgentRegistry) *Observer {
	if adapters == nil {
		panic("coding agent registry is required")
	}
	return &Observer{
		cfg:           cfg,
		adapters:      adapters,
		evidenceIndex: newTranscriptEvidenceIndex(adapters),
		inflight:      map[string]*transcriptScanFlight{},
		fileCache:     map[string]fileTraceCache{},
	}
}

type transcriptCandidate struct {
	File      TranscriptFile
	ModTime   time.Time
	Size      int64
	Priority  bool
	Deferred  bool
	TailParse bool
}

type fileTraceCache struct {
	ModTime         time.Time
	Size            int64
	EndsWithNewline bool
	Trace           *SessionTrace
	Traces          []*SessionTrace
	Err             string
	RetryAt         time.Time
}

type transcriptParseFunc func(TranscriptFile) (*SessionTrace, error)

type transcriptAppendParseFunc func(TranscriptFile, *SessionTrace, int64) (*SessionTrace, error)

type transcriptScanFlight struct {
	done     chan struct{}
	data     *TranscriptData
	complete bool
}

func (o *Observer) transcriptData(ctx context.Context, priority []TranscriptFile, now time.Time) (result *TranscriptData, cached bool) {
	if ctx == nil {
		ctx = context.Background()
	}
	var key string
	var evidenceRevision uint64
	healthyWaitRetries := 0
	// Root changes themselves advance the evidence revision. Establish that
	// cheap lifecycle state before capturing the scan revision, otherwise the
	// first cold scan would always be stamped one revision behind its contents.
	o.evidenceIndex.syncRoots()
	for {
		key = transcriptCacheKey(o.adapters.roots(), priority, o.cfg.IdleGap, o.cfg.MinInterval, o.cfg.Lookback)
		o.mu.Lock()
		// Capture the evidence revision while deciding the cache state. A watcher
		// mutation that lands after this point must not be stamped as included by
		// the scan that follows.
		evidenceRevision = o.evidenceIndex.cacheRevision()
		if o.cache.Data != nil && o.cache.Key == key && o.cache.EvidenceRevision == evidenceRevision && now.Before(o.cache.ExpiresAt) {
			data := cloneTranscriptData(o.cache.Data)
			o.mu.Unlock()
			// Close the small unlock window before returning a cache hit. A change
			// observed here is retried instead of publishing a known-stale clone.
			if o.evidenceIndex.cacheRevision() == evidenceRevision {
				return data, true
			}
			continue
		}
		if flight := o.inflight[key]; flight != nil {
			done := flight.done
			cachedData := cloneTranscriptData(o.cache.Data)
			o.mu.Unlock()
			select {
			case <-done:
				data := cloneTranscriptData(flight.data)
				if flight.complete && transcriptDataRevisionValid(o, data) {
					return data, false
				}
				if flight.complete {
					// A completed flight should already have removed itself before
					// closing done. Clear a stale guard defensively so a revision
					// change cannot turn this waiter into a permanent retry loop.
					o.mu.Lock()
					if o.inflight[key] == flight {
						delete(o.inflight, key)
					}
					o.mu.Unlock()
				}
				if flight.complete && ctx.Err() != nil {
					if data == nil {
						data = incompleteTranscriptData("transcript scan result became stale")
					} else {
						data.CoverageIncomplete = true
						data.Errors = append(data.Errors, "transcript evidence changed before scan waiter returned")
					}
					return data, false
				}
				if ctx.Err() == nil {
					healthyWaitRetries++
					if healthyWaitRetries < transcriptHealthyWaitRetryLimit {
						continue
					}
					if data == nil {
						data = &TranscriptData{Traces: map[string]*SessionTrace{}}
					}
					data.CoverageIncomplete = true
					data.Errors = append(data.Errors, "transcript scan remained incomplete after bounded waiter retries")
					return data, false
				}
				return data, false
			case <-ctx.Done():
				// The in-flight scan keeps running for other waiters; the
				// cancelled caller returns early with whatever cached data exists.
				cached := cachedData != nil
				if cachedData == nil {
					cachedData = &TranscriptData{Traces: map[string]*SessionTrace{}}
				}
				cachedData.CoverageIncomplete = true
				cachedData.Errors = append(cachedData.Errors, fmt.Sprintf("transcript scan wait cancelled: %v", ctx.Err()))
				return cachedData, cached
			}
		}
		break
	}
	flight := &transcriptScanFlight{done: make(chan struct{})}
	o.inflight[key] = flight
	o.mu.Unlock()

	finished := false
	finish := func(data *TranscriptData, complete bool, revision uint64) {
		if data == nil {
			data = incompleteTranscriptData("transcript scan returned no data")
		}
		o.mu.Lock()
		if o.inflight[key] == flight {
			flight.data = cloneTranscriptData(data)
			flight.complete = complete
			if complete {
				o.cache = transcriptCacheState{
					Key:              key,
					ExpiresAt:        time.Now().Add(o.cfg.TranscriptCacheTTL),
					EvidenceRevision: revision,
					Data:             cloneTranscriptData(data),
				}
			}
			delete(o.inflight, key)
			close(flight.done)
		}
		o.mu.Unlock()
		finished = true
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			data := incompleteTranscriptData("transcript scan recovered after panic")
			log.Printf("transcript scan panic recovered: %s\n%s",
				sanitizeTextForClient(fmt.Sprint(recovered)),
				sanitizeTextForClient(string(debug.Stack())))
			if !finished {
				finish(data, false, evidenceRevision)
			}
			result, cached = data, false
		}
	}()

	data := o.scanTranscriptsSafely(ctx, priority, transcriptScanOptions{
		HistoryCutoff:      now.Add(-o.cfg.Lookback),
		ForegroundCutoff:   foregroundTranscriptCutoff(now, o.cfg.IdleGap),
		HistoryLookback:    o.cfg.Lookback,
		ForegroundLookback: foregroundTranscriptLookback(o.cfg.IdleGap),
		DeferHistoryWalk:   true,
		IdleGap:            o.cfg.IdleGap,
		MinInterval:        o.cfg.MinInterval,
	})
	complete := ctx.Err() == nil && !data.CoverageIncomplete
	scanRevision := data.evidenceRevision
	if complete && o.evidenceIndex != nil {
		currentRevision := o.evidenceIndex.cacheRevision()
		if currentRevision != scanRevision {
			// A watcher mutation landed while the scan was reading files. The result
			// can still be useful to this caller, but it no longer describes one
			// coherent evidence revision and must not enter the cache or history.
			data.CoverageIncomplete = true
			data.Errors = append(data.Errors, "transcript evidence changed during scan")
			data.evidenceRevision = currentRevision
			complete = false
		}
	}
	finish(data, complete, scanRevision)
	return cloneTranscriptData(data), false
}

func transcriptDataRevisionValid(observer *Observer, data *TranscriptData) bool {
	if data == nil || observer == nil || observer.evidenceIndex == nil {
		return data != nil
	}
	return observer.evidenceIndex.cacheRevision() == data.evidenceRevision
}

func incompleteTranscriptData(reason string) *TranscriptData {
	data := &TranscriptData{Traces: map[string]*SessionTrace{}, CoverageIncomplete: true}
	if strings.TrimSpace(reason) != "" {
		data.Errors = []string{reason}
	}
	return data
}

// scanTranscriptsSafely is the owner-level boundary for the complete scan. The
// parser workers and evidence index each contain their own faults, but a future
// aggregation/helper panic must still release the transcript singleflight and
// publish an explicit incomplete result instead of poisoning the key.
func (o *Observer) scanTranscriptsSafely(ctx context.Context, priority []TranscriptFile, opts transcriptScanOptions) (data *TranscriptData) {
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("transcript scan panic recovered: %s\n%s",
				sanitizeTextForClient(fmt.Sprint(recovered)),
				sanitizeTextForClient(string(debug.Stack())))
			data = incompleteTranscriptData("transcript scan recovered after panic")
		}
		if data == nil {
			data = incompleteTranscriptData("transcript scan returned no data")
		}
	}()
	return o.scanTranscriptsWithOptions(ctx, priority, opts)
}

func transcriptCacheKey(roots map[string][]string, priority []TranscriptFile, idleGap, minInterval, lookback time.Duration) string {
	priorityParts := make([]string, 0, len(priority))
	for _, file := range priority {
		priorityParts = append(priorityParts, file.Tool+":"+file.Path)
	}
	parts := []string{
		registryRootsCacheKey(roots),
		"priority:" + strings.Join(priorityParts, "|"),
		"idle_gap:" + idleGap.String(),
		"min_interval:" + minInterval.String(),
		"lookback:" + lookback.String(),
	}
	return strings.Join(parts, "\n")
}

func (o *Observer) scanTranscripts(priority []TranscriptFile, cutoff time.Time, idleGap, minInterval time.Duration) *TranscriptData {
	return o.scanTranscriptsWithOptions(context.Background(), priority, transcriptScanOptions{
		HistoryCutoff:      cutoff,
		ForegroundCutoff:   cutoff,
		HistoryLookback:    durationSinceCutoff(cutoff),
		ForegroundLookback: durationSinceCutoff(cutoff),
		DeferHistoryWalk:   false,
		IdleGap:            idleGap,
		MinInterval:        minInterval,
	})
}

type transcriptScanOptions struct {
	HistoryCutoff      time.Time
	ForegroundCutoff   time.Time
	HistoryLookback    time.Duration
	ForegroundLookback time.Duration
	DeferHistoryWalk   bool
	IdleGap            time.Duration
	MinInterval        time.Duration
}

func (o *Observer) scanTranscriptsWithOptions(ctx context.Context, priority []TranscriptFile, opts transcriptScanOptions) *TranscriptData {
	if ctx == nil {
		ctx = context.Background()
	}
	// The collection always runs at the history horizon, even when the history
	// walk is deferred. Collapsing it to the foreground cutoff would drop every
	// in-scope-but-stale file before it could be counted, so the app reported a
	// gap of 1 while 13 live sessions were actually missing their transcript.
	// These files are still not parsed -- addFile marks them deferred -- the
	// change is only that they are now counted.
	collection := collectTranscriptCandidatesWithCoverage(ctx, o.evidenceIndex, o.adapters, priority, opts.HistoryCutoff, opts.ForegroundCutoff)
	files, walkErrors := collection.Files, collection.Errors
	// Candidates the foreground cutoff deferred are counted, not scanned, so
	// ScannedFiles stays "files this pass actually read" rather than growing to
	// the whole in-horizon candidate set.
	scanned := 0
	for _, candidate := range files {
		if !candidate.Deferred {
			scanned++
		}
	}
	data := &TranscriptData{
		Traces:                           make(map[string]*SessionTrace, scanned),
		ScannedFiles:                     scanned,
		DeferredFiles:                    collection.FilteredByCutoff,
		HistoricalScanDeferred:           opts.DeferHistoryWalk,
		CoverageIncomplete:               !collection.Complete,
		ForegroundScanLookbackSeconds:    int(opts.ForegroundLookback / time.Second),
		ConfiguredHistoryLookbackSeconds: int(opts.HistoryLookback / time.Second),
		Errors:                           walkErrors,
		evidenceRevision:                 collection.Revision,
	}

	toParse := make([]transcriptCandidate, 0, len(files))
	toAppend := make([]transcriptAppendCandidate, 0)
	o.mu.Lock()
	// Keep the cache decision in a defer-scoped lock. Adapter capabilities are
	// extension points; a malformed CanAppend implementation must be converted
	// into an incomplete scan by scanTranscriptsSafely without stranding o.mu.
	func() {
		defer o.mu.Unlock()
		for _, candidate := range files {
			if candidate.Deferred {
				data.DeferredFiles++
				continue
			}
			cached, ok := o.fileCache[candidate.File.Path]
			switch {
			case !ok || isAgentDatabase(candidate.File):
				toParse = append(toParse, candidate)
				continue
			case cached.Size == candidate.Size && cached.ModTime.Equal(candidate.ModTime):
				if cached.Err != "" {
					if transcriptParseErrorRetryDue(cached.RetryAt, time.Now()) {
						toParse = append(toParse, candidate)
						continue
					}
					data.CoverageIncomplete = true
					data.Errors = append(data.Errors, fmt.Sprintf("%s: %s", candidate.File.Path, cached.Err))
				}
				if len(cached.Traces) > 0 {
					insertSessionTraces(data, cached.Traces)
					data.ParsedFiles++
					continue
				}
				if cached.Trace == nil || len(cached.Trace.EventTimes) == 0 {
					continue
				}
				data.Traces[candidate.File.Path] = cloneSessionTrace(cached.Trace)
				data.ParsedFiles++
				continue
			case canAppendParseTranscript(o.adapters, candidate.File, cached, candidate):
				toAppend = append(toAppend, transcriptAppendCandidate{
					Candidate: candidate,
					Base:      cloneSessionTrace(cached.Trace),
					Offset:    cached.Size,
				})
				continue
			case cached.Err != "":
				// The file changed since the previous failure, so that error no
				// longer describes the candidate being parsed now.
				toParse = append(toParse, candidate)
				continue
			default:
				toParse = append(toParse, candidate)
			}
		}
	}()

	updates := map[string]fileTraceCache{}
	parseResults := parseTranscriptCandidates(ctx, o.adapters, toParse)
	parseResults = append(parseResults, parseAppendTranscriptCandidates(ctx, o.adapters, toAppend)...)
	abortedParses := 0
	for _, result := range parseResults {
		candidate := result.Candidate
		if candidate.File.Path == "" {
			// Job was never dispatched because the context was cancelled.
			abortedParses++
			continue
		}
		if isContextError(result.Err) {
			// Skip the cache entry so the next scan retries this file.
			abortedParses++
			continue
		}
		endsWithNewline := fileEndsWithNewline(candidate.File.Path, candidate.Size)
		if result.Err != nil {
			// One file failing to parse is degraded evidence for that file, not a
			// gap in the scan's coverage. Raising CoverageIncomplete here would
			// mark the whole snapshot untrustworthy — and downstream that
			// suppresses the live sampler — because a single transcript is
			// malformed. The error is disclosed per file just below.
			data.Errors = append(data.Errors, fmt.Sprintf("%s: %v", candidate.File.Path, result.Err))
			// A parse may return a degraded trace alongside its error (for
			// example codex lane sidecar failures); keep both so the session
			// evidence stays visible while the error is disclosed.
			updates[candidate.File.Path] = fileTraceCache{
				ModTime:         candidate.ModTime,
				Size:            candidate.Size,
				EndsWithNewline: endsWithNewline,
				Trace:           cloneSessionTrace(result.Trace),
				Traces:          cloneSessionTraces(result.Traces),
				Err:             result.Err.Error(),
				RetryAt:         time.Now().Add(transcriptParseErrorRetryInterval),
			}
		} else {
			updates[candidate.File.Path] = fileTraceCache{
				ModTime:         candidate.ModTime,
				Size:            candidate.Size,
				EndsWithNewline: endsWithNewline,
				Trace:           cloneSessionTrace(result.Trace),
				Traces:          cloneSessionTraces(result.Traces),
			}
		}
		if len(result.Traces) > 0 {
			insertSessionTraces(data, result.Traces)
			data.ParsedFiles++
			continue
		}
		if result.Trace == nil || len(result.Trace.EventTimes) == 0 {
			continue
		}
		data.Traces[candidate.File.Path] = result.Trace
		data.ParsedFiles++
		if candidate.TailParse {
			data.TailParsedFiles++
		}
	}
	if len(updates) > 0 {
		o.mu.Lock()
		for path, update := range updates {
			o.fileCache[path] = update
		}
		o.mu.Unlock()
	}
	if ctx.Err() != nil {
		data.CoverageIncomplete = true
		data.Errors = append(data.Errors, fmt.Sprintf("transcript scan aborted early (%d files not parsed): %v", abortedParses, ctx.Err()))
	} else {
		o.pruneFileCache(files)
	}
	if o.evidenceIndex != nil {
		currentRevision := o.evidenceIndex.cacheRevision()
		if currentRevision != data.evidenceRevision {
			data.CoverageIncomplete = true
			data.Errors = append(data.Errors, "transcript evidence changed during scan")
			data.evidenceRevision = currentRevision
		}
	}
	data.SessionSpans = buildSessionSpans(data.Traces, opts.MinInterval)
	data.BurstSpans = buildBurstSpans(data.Traces, opts.IdleGap, opts.MinInterval)
	return data
}

func transcriptParseErrorRetryDue(retryAt, now time.Time) bool {
	return retryAt.IsZero() || !now.Before(retryAt)
}

func isContextError(err error) bool {
	return err != nil && (errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded))
}

// pruneFileCache drops cache entries whose transcript file no longer exists on
// disk. Only paths outside the current candidate set are stat'ed; candidates
// were already stat'ed during collection.
func (o *Observer) pruneFileCache(candidates []transcriptCandidate) {
	current := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		current[candidate.File.Path] = struct{}{}
	}
	o.mu.Lock()
	unseen := []string{}
	for path := range o.fileCache {
		if _, ok := current[path]; !ok {
			unseen = append(unseen, path)
		}
	}
	o.mu.Unlock()
	if len(unseen) == 0 {
		return
	}
	missing := []string{}
	for _, path := range unseen {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			missing = append(missing, path)
		}
	}
	if len(missing) == 0 {
		return
	}
	o.mu.Lock()
	for _, path := range missing {
		delete(o.fileCache, path)
	}
	o.mu.Unlock()
}

func foregroundTranscriptLookback(idleGap time.Duration) time.Duration {
	if idleGap <= 0 {
		idleGap = 90 * time.Second
	}
	lookback := idleGap * 80
	if lookback < foregroundTranscriptMinLookback {
		lookback = foregroundTranscriptMinLookback
	}
	if lookback > foregroundTranscriptMaxLookback {
		lookback = foregroundTranscriptMaxLookback
	}
	return lookback
}

func foregroundTranscriptCutoff(now time.Time, idleGap time.Duration) time.Time {
	if now.IsZero() {
		return time.Time{}
	}
	return now.Add(-foregroundTranscriptLookback(idleGap))
}

func durationSinceCutoff(cutoff time.Time) time.Duration {
	if cutoff.IsZero() {
		return 0
	}
	duration := time.Since(cutoff)
	if duration < 0 {
		return 0
	}
	return duration
}

type transcriptParseResult struct {
	Candidate transcriptCandidate
	Trace     *SessionTrace
	Traces    []*SessionTrace
	Err       error
}

type transcriptAppendCandidate struct {
	Candidate transcriptCandidate
	Base      *SessionTrace
	Offset    int64
}

func canAppendParseTranscript(adapters *codingAgentRegistry, file TranscriptFile, cached fileTraceCache, candidate transcriptCandidate) bool {
	if candidate.Size <= cached.Size || !cached.EndsWithNewline {
		return false
	}
	if cached.Err != "" || cached.Trace == nil || len(cached.Trace.EventTimes) == 0 {
		return false
	}
	parser, ok := adapters.transcriptParser(file.Tool)
	return ok && parser.CanAppend(file)
}

func recoverTranscriptParsePanic(scope string, candidate transcriptCandidate, result *transcriptParseResult, fallback *SessionTrace) {
	if recovered := recover(); recovered != nil {
		scope = strings.TrimSpace(scope)
		// Parser failures are local diagnostics, but their logs can be retained
		// outside the snapshot response. Keep the useful file identity while
		// avoiding a user/workspace path and redact paths in panic text/stack.
		safePath := filepath.Base(filepath.Clean(candidate.File.Path))
		if safePath == "." || safePath == string(filepath.Separator) || safePath == "" {
			safePath = "local-transcript"
		}
		log.Printf("%s panic recovered for %s: %s\n%s", scope, safePath,
			sanitizeTextForClient(fmt.Sprint(recovered)), sanitizeTextForClient(string(debug.Stack())))
		*result = transcriptParseResult{
			Candidate: candidate,
			// An append parse already has a known-good prefix. Preserve that
			// evidence when the parser panics while disclosing the failed job.
			Trace: fallback,
			Err:   fmt.Errorf("%s panic for %s: %v", scope, candidate.File.Path, recovered),
		}
	}
}

func parseTranscriptCandidate(ctx context.Context, adapters *codingAgentRegistry, candidate transcriptCandidate) (result transcriptParseResult) {
	result.Candidate = candidate
	defer recoverTranscriptParsePanic("transcript parser worker", candidate, &result, nil)
	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}
	parser, ok := adapters.transcriptParser(candidate.File.Tool)
	if !ok {
		result.Err = fmt.Errorf("%s transcript parser is unavailable", candidate.File.Tool)
		return result
	}
	if multi, ok := parser.(interface {
		ParseSessions(context.Context, TranscriptFile) ([]*SessionTrace, error)
	}); ok {
		result.Traces, result.Err = multi.ParseSessions(ctx, candidate.File)
		return result
	}
	if candidate.TailParse {
		result.Trace, result.Err = parser.ParseTail(candidate.File)
	} else {
		result.Trace, result.Err = parser.Parse(candidate.File)
	}
	return result
}

func parseAppendTranscriptCandidate(ctx context.Context, adapters *codingAgentRegistry, candidate transcriptAppendCandidate) (result transcriptParseResult) {
	result.Candidate = candidate.Candidate
	// Keep an isolated fallback so a panic cannot erase the cached prefix that
	// was supplied for this append job.
	fallback := cloneSessionTrace(candidate.Base)
	defer recoverTranscriptParsePanic("transcript append worker", candidate.Candidate, &result, fallback)
	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}
	parser, ok := adapters.transcriptParser(candidate.Candidate.File.Tool)
	if !ok {
		result.Err = fmt.Errorf("%s transcript parser is unavailable", candidate.Candidate.File.Tool)
		return result
	}
	result.Trace, result.Err = parser.ParseAppend(candidate.Candidate.File, candidate.Base, candidate.Offset)
	return result
}

func parseTranscriptCandidates(ctx context.Context, adapters *codingAgentRegistry, candidates []transcriptCandidate) []transcriptParseResult {
	results := make([]transcriptParseResult, len(candidates))
	if len(candidates) == 0 {
		return results
	}
	workerCount := runtime.NumCPU()
	if workerCount > 4 {
		workerCount = 4
	}
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(candidates) {
		workerCount = len(candidates)
	}

	type job struct {
		Index     int
		Candidate transcriptCandidate
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				results[item.Index] = parseTranscriptCandidate(ctx, adapters, item.Candidate)
			}
		}()
	}
dispatch:
	for index, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- job{Index: index, Candidate: candidate}:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

func parseAppendTranscriptCandidates(ctx context.Context, adapters *codingAgentRegistry, candidates []transcriptAppendCandidate) []transcriptParseResult {
	results := make([]transcriptParseResult, len(candidates))
	if len(candidates) == 0 {
		return results
	}
	workerCount := runtime.NumCPU()
	if workerCount > 4 {
		workerCount = 4
	}
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(candidates) {
		workerCount = len(candidates)
	}

	type job struct {
		Index     int
		Candidate transcriptAppendCandidate
	}
	jobs := make(chan job)
	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				results[item.Index] = parseAppendTranscriptCandidate(ctx, adapters, item.Candidate)
			}
		}()
	}
dispatch:
	for index, candidate := range candidates {
		if ctx.Err() != nil {
			break
		}
		select {
		case jobs <- job{Index: index, Candidate: candidate}:
		case <-ctx.Done():
			break dispatch
		}
	}
	close(jobs)
	wg.Wait()
	return results
}

type transcriptCandidateCollection struct {
	Files    []transcriptCandidate
	Errors   []string
	Complete bool
	// FilteredByCutoff counts files the collection cutoff excluded before they
	// became candidates. They are deferred just like a candidate marked
	// Deferred, and are counted so the reported gap matches the real one.
	FilteredByCutoff int
	Revision         uint64
}

func collectTranscriptCandidates(ctx context.Context, evidenceIndex *transcriptEvidenceIndex, adapters *codingAgentRegistry, priority []TranscriptFile, historyCutoff, foregroundCutoff time.Time) ([]transcriptCandidate, []string) {
	collection := collectTranscriptCandidatesWithCoverage(ctx, evidenceIndex, adapters, priority, historyCutoff, foregroundCutoff)
	return collection.Files, collection.Errors
}

func collectTranscriptCandidatesWithCoverage(ctx context.Context, evidenceIndex *transcriptEvidenceIndex, adapters *codingAgentRegistry, priority []TranscriptFile, historyCutoff, foregroundCutoff time.Time) transcriptCandidateCollection {
	if ctx == nil {
		ctx = context.Background()
	}
	scanErrors := []string{}
	priorityKeys := map[string]struct{}{}
	for _, file := range priority {
		if file.Path == "" || file.Tool == "" || !adapters.hasTranscript(file.Tool) {
			continue
		}
		priorityKeys[file.Tool+"\x00"+canonicalEvidencePath(file.Path)] = struct{}{}
	}
	seen := map[string]transcriptCandidate{}
	addFile := func(file TranscriptFile, info os.FileInfo) {
		if file.Path == "" || file.Tool == "" {
			return
		}
		file.Path = filepath.Clean(file.Path)
		key := file.Tool + "\x00" + canonicalEvidencePath(file.Path)
		_, priorityFile := priorityKeys[key]
		deferred := false
		tailParse := false
		if !foregroundCutoff.IsZero() {
			switch {
			case priorityFile:
				// A priority file is never deferred, but one that has gone quiet
				// still only needs its tail: the session is live, so recent events
				// place it, and a full read here would re-parse the whole file on
				// every append. The largest observed live transcript is 133MB.
				tailParse = info.ModTime().Before(foregroundCutoff)
			case info.ModTime().Before(foregroundCutoff):
				deferred = true
			case !isAgentDatabase(file) && !fileMayContainEventsAfterCutoff(file.Path, info, foregroundCutoff):
				deferred = true
			default:
				tailParse = true
			}
		}
		seen[key] = transcriptCandidate{
			File:      file,
			ModTime:   info.ModTime(),
			Size:      info.Size(),
			Priority:  priorityFile,
			Deferred:  deferred,
			TailParse: tailParse,
		}
	}
	indexed := evidenceIndex.snapshot(ctx, historyCutoff, priority)
	scanErrors = append(scanErrors, indexed.Errors...)
	if !indexed.Complete {
		scanErrors = append(scanErrors, "transcript evidence index coverage is incomplete")
	}
	for _, file := range indexed.Files {
		if adapters.hasTranscript(file.File.Tool) {
			addFile(file.File, file.Info)
		}
	}

	// The walk counts every file the cutoff excluded, but a priority file is
	// re-admitted afterwards and does get scanned, so it is not a gap.
	deferredByCutoff := indexed.FilteredByCutoff
	for _, candidate := range seen {
		if candidate.Priority && candidate.ModTime.Before(foregroundCutoff) {
			deferredByCutoff--
		}
	}
	if deferredByCutoff < 0 {
		deferredByCutoff = 0
	}
	files := make([]transcriptCandidate, 0, len(seen))
	for _, file := range seen {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Deferred != files[j].Deferred {
			return !files[i].Deferred
		}
		if files[i].Priority != files[j].Priority {
			return files[i].Priority
		}
		if !files[i].ModTime.Equal(files[j].ModTime) {
			return files[i].ModTime.After(files[j].ModTime)
		}
		if files[i].File.Tool == files[j].File.Tool {
			return files[i].File.Path < files[j].File.Path
		}
		return files[i].File.Tool < files[j].File.Tool
	})
	return transcriptCandidateCollection{
		Files:            files,
		Errors:           scanErrors,
		Complete:         indexed.Complete && ctx.Err() == nil,
		FilteredByCutoff: deferredByCutoff,
		Revision:         indexed.Revision,
	}
}

// transcriptScanErrorIsGlobal tells a scan-wide abort apart from a single bad
// file. Only the two markers produced when the scan itself is cut short — an
// early abort and a cancelled wait — mean the snapshot is incomplete as a whole;
// a parse failure on one transcript is degraded evidence for that file, and
// treating it as a global abort would suppress a snapshot that is otherwise
// entirely valid.
func transcriptScanErrorIsGlobal(err string) bool {
	return strings.HasPrefix(err, "transcript scan aborted early") ||
		strings.HasPrefix(err, "transcript scan wait cancelled")
}

func fileMayContainEventsAfterCutoff(path string, info os.FileInfo, cutoff time.Time) bool {
	if cutoff.IsZero() {
		return true
	}
	if info == nil || info.Size() <= 0 {
		return false
	}
	if info.ModTime().Before(cutoff) {
		return false
	}
	lastEvent, ok := latestTimestampFromJSONLTail(path, info.Size())
	if !ok {
		return true
	}
	return !lastEvent.Before(cutoff)
}

func latestTimestampFromJSONLTail(path string, size int64) (time.Time, bool) {
	if size <= 0 {
		return time.Time{}, false
	}
	const maxTailBytes int64 = 256 * 1024
	offset := int64(0)
	if size > maxTailBytes {
		offset = size - maxTailBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return time.Time{}, false
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return time.Time{}, false
		}
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return time.Time{}, false
	}
	if offset > 0 {
		if newline := bytes.IndexByte(buf, '\n'); newline >= 0 {
			buf = buf[newline+1:]
		} else {
			return time.Time{}, false
		}
	}
	lines := bytes.Split(buf, []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		if ts := parseTimestampString(jsonStringField(line, "timestamp")); !ts.IsZero() {
			return ts, true
		}
	}
	return time.Time{}, false
}

func forEachRecentJSONLTailLine(path string, fn func([]byte) bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	return forEachJSONLHeadTailLine(path, info.Size(), 128*1024, 512*1024, fn)
}

func forEachJSONLTailLine(path string, size, maxTailBytes int64, fn func([]byte) bool) error {
	if size <= 0 {
		return nil
	}
	if maxTailBytes <= 0 {
		maxTailBytes = 256 * 1024
	}
	offset := int64(0)
	if size > maxTailBytes {
		offset = size - maxTailBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if offset > 0 {
		if newline := bytes.IndexByte(buf, '\n'); newline >= 0 {
			buf = buf[newline+1:]
		} else {
			return nil
		}
	}
	for _, line := range bytes.Split(buf, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !fn(line) {
			return nil
		}
	}
	return nil
}

func forEachJSONLHeadTailLine(path string, size, maxHeadBytes, maxTailBytes int64, fn func([]byte) bool) error {
	if size <= 0 {
		return nil
	}
	if maxHeadBytes <= 0 {
		maxHeadBytes = 64 * 1024
	}
	if maxTailBytes <= 0 {
		maxTailBytes = 256 * 1024
	}
	if size <= maxHeadBytes+maxTailBytes {
		return forEachJSONLTailLine(path, size, size, fn)
	}
	tailOffset := size - maxTailBytes
	if err := forEachJSONLHeadSelectedLine(path, tailOffset, maxHeadBytes, fn); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Seek(tailOffset, io.SeekStart); err != nil {
		return err
	}
	tail, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if newline := bytes.IndexByte(tail, '\n'); newline >= 0 {
		tail = tail[newline+1:]
	} else {
		return nil
	}
	for _, line := range bytes.Split(tail, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if !fn(line) {
			return nil
		}
	}
	return nil
}

func forEachJSONLHeadSelectedLine(path string, limit, fullBytes int64, fn func([]byte) bool) error {
	if limit <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(io.NewSectionReader(f, 0, limit), 64*1024)
	var offset int64
	for offset < limit {
		lineStart := offset
		line, consumed, err := readBoundedJSONLLine(reader, 1024*1024)
		offset += consumed
		line = bytes.TrimSpace(line)
		if len(line) > 0 && (lineStart < fullBytes || jsonlLineLooksLikeTraceMetadata(line)) {
			if !fn(line) {
				return nil
			}
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if consumed == 0 {
			return nil
		}
	}
	return nil
}

func readBoundedJSONLLine(reader *bufio.Reader, maxLineBytes int) ([]byte, int64, error) {
	var line []byte
	var consumed int64
	truncated := false
	for {
		chunk, err := reader.ReadSlice('\n')
		if len(chunk) > 0 {
			consumed += int64(len(chunk))
			if !truncated {
				if len(line)+len(chunk) <= maxLineBytes {
					line = append(line, chunk...)
				} else {
					truncated = true
					line = nil
				}
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err == io.EOF && consumed > 0 && (truncated || !bytes.HasSuffix(line, []byte{'\n'})) {
			return nil, consumed, err
		}
		return line, consumed, err
	}
}

func jsonlLineLooksLikeTraceMetadata(line []byte) bool {
	return bytes.Contains(line, []byte(`"session_meta"`)) ||
		jsonlLineContainsKey(line, "project") ||
		jsonlLineContainsKey(line, "cwd") ||
		jsonlLineContainsKey(line, "thread_source") ||
		jsonlLineContainsKey(line, "parent_thread_id") ||
		jsonlLineContainsKey(line, "thread_spawn") ||
		jsonlLineContainsKey(line, "agent_nickname") ||
		jsonlLineContainsKey(line, "agent_role") ||
		jsonlLineContainsKey(line, "session_id") ||
		jsonlLineContainsKey(line, "sessionId")
}

func jsonlLineContainsKey(line []byte, key string) bool {
	raw := `"` + key + `"`
	return bytes.Contains(line, []byte(raw+`:`)) ||
		bytes.Contains(line, []byte(raw+` :`))
}

func parseClaudeTraceTail(file TranscriptFile) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "claude",
		Path:             file.Path,
		SessionID:        genericTranscriptSessionID(file.Path),
		IndependentlyRun: true,
	}
	setTraceProjectName(trace, extractClaudeProjectFromPath(file.Path), "transcript_path")
	if err := forEachRecentJSONLTailLine(file.Path, func(line []byte) bool {
		processClaudeTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func parseCodexTraceTail(file TranscriptFile) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "codex",
		Path:             file.Path,
		SessionID:        codexTranscriptSessionID(file.Path),
		IndependentlyRun: true,
	}
	if err := forEachRecentJSONLTailLine(file.Path, func(line []byte) bool {
		processCodexTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func parseTraeTraceTail(file TranscriptFile) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "trae",
		Path:             file.Path,
		SessionID:        genericTranscriptSessionID(file.Path),
		IndependentlyRun: true,
	}
	if err := forEachRecentJSONLTailLine(file.Path, func(line []byte) bool {
		processTraeTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func validateTranscriptAppend(base *SessionTrace, offset int64) error {
	if base == nil {
		return fmt.Errorf("missing cached trace for append parse")
	}
	if offset < 0 {
		return fmt.Errorf("invalid append offset %d", offset)
	}
	return nil
}

func parseClaudeTraceAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if err := validateTranscriptAppend(base, offset); err != nil {
		return nil, err
	}
	trace := cloneSessionTrace(base)
	trace.Tool = "claude"
	trace.Path = file.Path
	if err := forEachJSONLLineFromOffset(file.Path, offset, func(line []byte) bool {
		processClaudeTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func parseCodexTraceAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if err := validateTranscriptAppend(base, offset); err != nil {
		return nil, err
	}
	trace := cloneSessionTrace(base)
	trace.Tool = "codex"
	trace.Path = file.Path
	if err := forEachJSONLLineFromOffset(file.Path, offset, func(line []byte) bool {
		processCodexTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func parseTraeTraceAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if err := validateTranscriptAppend(base, offset); err != nil {
		return nil, err
	}
	trace := cloneSessionTrace(base)
	trace.Tool = "trae"
	trace.Path = file.Path
	if err := forEachJSONLLineFromOffset(file.Path, offset, func(line []byte) bool {
		processTraeTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func parseClaudeTrace(path string) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "claude",
		Path:             path,
		SessionID:        genericTranscriptSessionID(path),
		IndependentlyRun: true,
	}
	setTraceProjectName(trace, extractClaudeProjectFromPath(path), "transcript_path")
	err := forEachJSONLLine(path, func(line []byte) bool {
		processClaudeTraceLine(trace, line)
		return true
	})
	if err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func processClaudeTraceLine(trace *SessionTrace, line []byte) {
	ts := parseTimestampString(jsonStringField(line, "timestamp"))
	if ts.IsZero() {
		return
	}
	captureTokenUsage(trace, line)
	if sid := firstNonEmptyString(
		jsonStringField(line, "sessionId"),
		jsonStringField(line, "session_id"),
	); sid != "" {
		if jsonTrueField(line, "isSidechain") {
			// Claude stamps the PARENT's sessionId on every sidechain line, so
			// adopting it here collapses every subagent transcript of a session
			// into the parent's row — a session running ten subagents reported
			// one. The file name is this thread's only identity; the borrowed id
			// is the parent link.
			trace.ParentThreadID = sid
			trace.ThreadSource = "subagent"
			trace.RoleHintSource = firstNonEmptyString(trace.RoleHintSource, "claude_sidechain")
			trace.IndependentlyRun = false
		} else {
			trace.SessionID = sid
		}
	}
	if project := firstNonEmptyString(
		jsonStringField(line, "project"),
		jsonNestedStringField(line, "message", "project"),
	); project != "" {
		setTraceProjectName(trace, project, "transcript_project")
	}
	if cwd := firstNonEmptyString(
		jsonStringField(line, "cwd"),
		jsonNestedStringField(line, "message", "cwd"),
	); cwd != "" {
		setTraceProjectPath(trace, cwd, "transcript_cwd")
	}
	if isClaudeActiveType(jsonStringField(line, "type")) {
		trace.EventTimes = append(trace.EventTimes, ts)
	}
}

func parseCodexTrace(path string) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "codex",
		Path:             path,
		SessionID:        codexTranscriptSessionID(path),
		IndependentlyRun: true,
	}
	err := forEachJSONLLine(path, func(line []byte) bool {
		processCodexTraceLine(trace, line)
		return true
	})
	if err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func processCodexTraceLine(trace *SessionTrace, line []byte) {
	ts := parseTimestampString(jsonStringField(line, "timestamp"))
	if ts.IsZero() {
		return
	}
	captureTokenUsage(trace, line)
	captureTraceRoleMetadata(trace, line)
	if sid := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "id"),
		jsonNestedStringField(line, "payload", "session_id"),
		jsonNestedStringField(line, "payload", "sessionId"),
		jsonStringField(line, "session_id"),
		jsonStringField(line, "sessionId"),
	); sid != "" {
		trace.SessionID = sid
	}
	if project := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "project"),
		jsonStringField(line, "project"),
	); project != "" {
		setTraceProjectName(trace, project, "transcript_project")
	}
	if cwd := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "cwd"),
		jsonStringField(line, "cwd"),
	); cwd != "" {
		setTraceProjectPath(trace, cwd, "transcript_cwd")
	}
	trace.EventTimes = append(trace.EventTimes, ts)
}

func parseCodexLaneTrace(eventsPath string) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:           "codex",
		Path:           eventsPath,
		SessionID:      filepath.Base(filepath.Dir(eventsPath)),
		ThreadSource:   "subagent",
		RoleHintSource: "codexl_lane_path",
	}

	// Sidecar read failures degrade the trace instead of voiding it; they are
	// returned alongside the trace so the scan can disclose them.
	sidecarErrs := []error{}
	if err := forEachJSONLLine(eventsPath, func(line []byte) bool {
		if threadID := firstNonEmptyString(
			jsonStringField(line, "thread_id"),
			jsonStringField(line, "session_id"),
			jsonStringField(line, "sessionId"),
		); threadID != "" && trace.SessionID == filepath.Base(filepath.Dir(eventsPath)) {
			trace.SessionID = threadID
		}
		captureTokenUsage(trace, line)
		return true
	}); err != nil {
		sidecarErrs = append(sidecarErrs, fmt.Errorf("lane events: %w", err))
	}

	requestObj, err := readJSONFileMap(filepath.Join(filepath.Dir(eventsPath), "request.json"))
	if err != nil && !os.IsNotExist(err) {
		sidecarErrs = append(sidecarErrs, fmt.Errorf("lane request.json: %w", err))
	}
	runObj, err := readJSONFileMap(filepath.Join(filepath.Dir(eventsPath), "run.json"))
	if err != nil && !os.IsNotExist(err) {
		sidecarErrs = append(sidecarErrs, fmt.Errorf("lane run.json: %w", err))
	}
	requestedAt := firstNonZeroTime(
		parseTimestampString(nestedString(requestObj, "request", "requested_at")),
		parseTimestampString(nestedString(requestObj, "invocation", "requested_at")),
		parseTimestampString(stringValue(requestObj["requested_at"])),
		parseTimestampString(stringValue(runObj["requested_at"])),
	)
	startedAt := parseTimestampString(stringValue(runObj["started_at"]))
	completedAt := parseTimestampString(stringValue(runObj["completed_at"]))
	if project := firstNonEmptyString(
		nestedString(requestObj, "request", "contract", "project"),
		nestedString(requestObj, "contract", "project"),
		nestedString(requestObj, "request", "project"),
		stringValue(requestObj["project"]),
		nestedString(runObj, "contract", "project"),
		stringValue(runObj["project"]),
	); project != "" {
		setTraceProjectName(trace, project, "transcript_project")
	}
	if cwd := firstNonEmptyString(
		nestedString(requestObj, "request", "contract", "cwd"),
		nestedString(requestObj, "contract", "cwd"),
		nestedString(requestObj, "request", "cwd"),
		nestedString(runObj, "contract", "cwd"),
		stringValue(runObj["cwd"]),
	); cwd != "" {
		setTraceProjectPath(trace, cwd, "transcript_cwd")
	}
	if trustedTraceProjectName(trace.Project) == "" {
		if root := configRootFromPath(eventsPath, ".codex"); root != "" {
			setTraceProjectPath(trace, filepath.Dir(root), "config_root_parent")
		}
	}

	if runID := stringValue(runObj["run_id"]); runID != "" && trace.SessionID == filepath.Base(filepath.Dir(eventsPath)) {
		trace.SessionID = runID
	}
	if requestedAt.IsZero() {
		if info, err := os.Stat(eventsPath); err == nil {
			requestedAt = info.ModTime()
		}
	}
	lastEvent := firstNonZeroTime(completedAt, startedAt)
	if info, err := os.Stat(eventsPath); err == nil {
		lastEvent = firstNonZeroTime(info.ModTime(), lastEvent)
	}
	trace.EventTimes = appendNonZeroTimes(trace.EventTimes, requestedAt, startedAt, completedAt, lastEvent)
	finalizeTrace(trace)
	if len(trace.EventTimes) == 0 {
		return nil, errors.Join(sidecarErrs...)
	}
	return trace, errors.Join(sidecarErrs...)
}

func parseTraeTrace(path string) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "trae",
		Path:             path,
		SessionID:        genericTranscriptSessionID(path),
		IndependentlyRun: true,
	}
	err := forEachJSONLLine(path, func(line []byte) bool {
		processTraeTraceLine(trace, line)
		return true
	})
	if err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func processTraeTraceLine(trace *SessionTrace, line []byte) {
	ts := parseTimestampString(jsonStringField(line, "timestamp"))
	if ts.IsZero() {
		return
	}
	captureTokenUsage(trace, line)
	captureTraceRoleMetadata(trace, line)
	if sid := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "id"),
		jsonNestedStringField(line, "payload", "session_id"),
		jsonNestedStringField(line, "payload", "sessionId"),
		jsonStringField(line, "session_id"),
		jsonStringField(line, "sessionId"),
	); sid != "" {
		trace.SessionID = sid
	}
	if project := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "project"),
		jsonStringField(line, "project"),
	); project != "" {
		setTraceProjectName(trace, project, "transcript_project")
	}
	if cwd := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "cwd"),
		jsonStringField(line, "cwd"),
	); cwd != "" {
		setTraceProjectPath(trace, cwd, "transcript_cwd")
	}
	trace.EventTimes = append(trace.EventTimes, ts)
}

// newGrokTrace seeds the identity grok encodes into the transcript path: the
// session directory is the id, and its parent is the percent-encoded working
// directory. Both are known before a single line is read.
func newGrokTrace(path string) *SessionTrace {
	trace := &SessionTrace{
		Tool:             "grok",
		Path:             path,
		SessionID:        grokTranscriptSessionID(path),
		IndependentlyRun: true,
	}
	if workdir := grokWorkdirFromTranscriptPath(path); workdir != "" {
		setTraceProjectPath(trace, workdir, "transcript_path")
	}
	return trace
}

func parseGrokTrace(path string) (*SessionTrace, error) {
	trace := newGrokTrace(path)
	if err := forEachJSONLLine(path, func(line []byte) bool {
		processGrokTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func parseGrokTraceTail(file TranscriptFile) (*SessionTrace, error) {
	trace := newGrokTrace(file.Path)
	if err := forEachRecentJSONLTailLine(file.Path, func(line []byte) bool {
		processGrokTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

func parseGrokTraceAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if err := validateTranscriptAppend(base, offset); err != nil {
		return nil, err
	}
	trace := cloneSessionTrace(base)
	trace.Tool = "grok"
	trace.Path = file.Path
	if err := forEachJSONLLineFromOffset(file.Path, offset, func(line []byte) bool {
		processGrokTraceLine(trace, line)
		return true
	}); err != nil {
		return nil, err
	}
	finalizeTrace(trace)
	return nonEmptyTrace(trace), nil
}

// processGrokTraceLine reads one ACP session-update record. The timestamp is an
// unquoted epoch-second number rather than the RFC3339 string every other
// vendor writes, and the session id lives under params.
func processGrokTraceLine(trace *SessionTrace, line []byte) {
	ts := jsonEpochSecondsField(line, "timestamp")
	if ts.IsZero() {
		return
	}
	captureTokenUsage(trace, line)
	if sid := jsonNestedStringField(line, "params", "sessionId"); sid != "" {
		trace.SessionID = sid
	}
	trace.EventTimes = append(trace.EventTimes, ts)
}

func captureTraceRoleMetadata(trace *SessionTrace, line []byte) {
	if trace == nil || len(line) == 0 {
		return
	}
	if source := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "thread_source"),
		jsonStringField(line, "thread_source"),
	); source != "" {
		trace.ThreadSource = normalizeSessionRoleSource(source)
		trace.RoleHintSource = firstNonEmptyString(trace.RoleHintSource, "thread_source")
		if trace.ThreadSource == "subagent" {
			trace.IndependentlyRun = false
		}
	}
	if parent := firstNonEmptyString(
		jsonNestedStringField(line, "thread_spawn", "parent_thread_id"),
		jsonNestedStringField(line, "subagent", "parent_thread_id"),
		jsonNestedStringField(line, "payload", "parent_thread_id"),
		jsonStringField(line, "parent_thread_id"),
	); parent != "" {
		trace.ParentThreadID = parent
		trace.RoleHintSource = firstNonEmptyString(trace.RoleHintSource, "parent_thread_id")
		trace.IndependentlyRun = false
	}
	if strings.Contains(string(line), "thread_spawn") {
		if parent := jsonStringField(line, "parent_thread_id"); parent != "" {
			trace.ParentThreadID = parent
			trace.RoleHintSource = firstNonEmptyString(trace.RoleHintSource, "thread_spawn")
			trace.IndependentlyRun = false
		}
	}
	if nickname := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "agent_nickname"),
		jsonStringField(line, "agent_nickname"),
	); nickname != "" {
		trace.AgentNickname = nickname
	}
	if role := firstNonEmptyString(
		jsonNestedStringField(line, "payload", "agent_role"),
		jsonStringField(line, "agent_role"),
	); role != "" {
		trace.AgentRole = role
	}
}

func captureTokenUsage(trace *SessionTrace, line []byte) {
	if trace == nil || !jsonlLineLooksLikeTokenUsage(line) {
		return
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(line, &obj); err != nil {
		return
	}
	if usage, ok := cumulativeTokenUsageFromJSONValue(obj); ok && !usage.Empty() {
		trace.TokenUsage.Max(usage)
		return
	}
	usage := tokenUsageFromJSONValue(obj)
	if usage.Empty() {
		return
	}
	trace.TokenUsage.Add(usage)
}

func jsonlLineLooksLikeTokenUsage(line []byte) bool {
	for _, key := range []string{
		"usage",
		"usage_metadata",
		"usageMetadata",
		"token_usage",
		"tokenUsage",
		"input_tokens",
		"output_tokens",
		"prompt_tokens",
		"completion_tokens",
		"total_tokens",
		"promptTokenCount",
		"candidatesTokenCount",
		"totalTokenCount",
	} {
		if jsonlLineContainsKey(line, key) {
			return true
		}
	}
	return false
}

func tokenUsageFromJSONValue(value interface{}) TokenUsage {
	var out TokenUsage
	collectTokenUsage(value, &out)
	return out
}

func cumulativeTokenUsageFromJSONValue(value interface{}) (TokenUsage, bool) {
	var out TokenUsage
	if collectCumulativeTokenUsage(value, &out) {
		return out, true
	}
	return TokenUsage{}, false
}

func collectCumulativeTokenUsage(value interface{}, out *TokenUsage) bool {
	switch v := value.(type) {
	case map[string]interface{}:
		if total, ok := mapFromKeys(v, "total_token_usage", "totalTokenUsage"); ok {
			if usage, found := directTokenUsage(total); found {
				out.Max(usage)
				return true
			}
		}
		found := false
		for key, child := range v {
			if shouldInspectTokenUsageChild(key) {
				found = collectCumulativeTokenUsage(child, out) || found
			}
		}
		return found
	case []interface{}:
		found := false
		for _, child := range v {
			found = collectCumulativeTokenUsage(child, out) || found
		}
		return found
	default:
		return false
	}
}

func collectTokenUsage(value interface{}, out *TokenUsage) {
	switch v := value.(type) {
	case map[string]interface{}:
		if usage, ok := directTokenUsage(v); ok {
			out.Add(usage)
			return
		}
		for key, child := range v {
			if shouldInspectTokenUsageChild(key) {
				collectTokenUsage(child, out)
			}
		}
	case []interface{}:
		for _, child := range v {
			collectTokenUsage(child, out)
		}
	}
}

func shouldInspectTokenUsageChild(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(key), "-", "_"), " ", "_"))
	if key == "" {
		return false
	}
	if strings.Contains(key, "usage") || strings.Contains(key, "token") {
		return true
	}
	switch key {
	// "params"/"update" reach grok's params.update.usage record.
	case "payload", "message", "response", "result", "metadata", "data", "output", "info", "params", "update":
		return true
	default:
		return false
	}
}

func directTokenUsage(obj map[string]interface{}) (TokenUsage, bool) {
	var usage TokenUsage
	found := false
	if value, ok := intFromKeys(obj, "input_tokens", "inputTokens", "prompt_tokens", "promptTokens", "promptTokenCount", "prompt_token_count"); ok {
		usage.InputTokens = value
		found = true
	}
	if value, ok := outputTokenCountFromMap(obj); ok {
		usage.OutputTokens = value
		found = true
	}
	if value, ok := intFromKeys(obj, "cache_creation_input_tokens", "cacheCreationInputTokens", "cache_creation_tokens", "cacheCreationTokens", "cache_write_input_tokens", "cacheWriteInputTokens"); ok {
		usage.CacheCreationInputTokens = value
		found = true
	}
	if value, ok := intFromKeys(obj, "cache_read_input_tokens", "cacheReadInputTokens", "cached_input_tokens", "cachedInputTokens", "cached_tokens", "cachedTokens", "cachedContentTokenCount", "cached_content_token_count"); ok {
		usage.CacheReadInputTokens = value
		found = true
	}
	if value, ok := intFromKeys(obj, "reasoning_output_tokens", "reasoningOutputTokens", "reasoning_tokens", "reasoningTokens", "thoughtsTokenCount", "thoughts_token_count"); ok {
		usage.ReasoningOutputTokens = value
		found = true
	}
	if value, ok := intFromKeys(obj, "total_tokens", "totalTokens", "totalTokenCount", "total_token_count"); ok {
		usage.TotalTokens = value
		found = true
	}
	if details, ok := mapFromKeys(obj, "prompt_tokens_details", "promptTokensDetails", "input_token_details", "inputTokenDetails"); ok {
		if value, ok := intFromKeys(details, "cached_tokens", "cachedTokens", "cache_read", "cacheRead", "cache_read_input_tokens", "cacheReadInputTokens"); ok {
			usage.CacheReadInputTokens += value
			found = true
		}
		if value, ok := intFromKeys(details, "cache_creation", "cacheCreation", "cache_creation_input_tokens", "cacheCreationInputTokens"); ok {
			usage.CacheCreationInputTokens += value
			found = true
		}
	}
	if details, ok := mapFromKeys(obj, "completion_tokens_details", "completionTokensDetails", "output_token_details", "outputTokenDetails"); ok {
		if value, ok := intFromKeys(details, "reasoning_tokens", "reasoningTokens", "reasoning_output_tokens", "reasoningOutputTokens"); ok {
			usage.ReasoningOutputTokens += value
			found = true
		}
	}
	if found && usage.TotalTokens == 0 {
		usage.TotalTokens = usage.DerivedTotal()
	}
	return usage, found
}

func outputTokenCountFromMap(obj map[string]interface{}) (int, bool) {
	return intFromKeys(
		obj,
		"output_tokens",
		"outputTokens",
		"completion_tokens",
		"completionTokens",
		"completionTokenCount",
		"completion_token_count",
		"candidatesTokenCount",
		"candidates_token_count",
		"response_tokens",
	)
}

func intFromKeys(obj map[string]interface{}, keys ...string) (int, bool) {
	for _, key := range keys {
		if value, ok := intFromValue(obj[key]); ok {
			return value, true
		}
	}
	return 0, false
}

func mapFromKeys(obj map[string]interface{}, keys ...string) (map[string]interface{}, bool) {
	for _, key := range keys {
		if value, ok := obj[key].(map[string]interface{}); ok {
			return value, true
		}
	}
	return nil, false
}

func intFromValue(value interface{}) (int, bool) {
	switch v := value.(type) {
	case float64:
		if v < 0 {
			return 0, false
		}
		return int(v), true
	case int:
		if v < 0 {
			return 0, false
		}
		return v, true
	case json.Number:
		parsed, err := strconv.Atoi(v.String())
		if err != nil || parsed < 0 {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || parsed < 0 {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func (u TokenUsage) Empty() bool {
	return u.InputTokens <= 0 &&
		u.OutputTokens <= 0 &&
		u.CacheCreationInputTokens <= 0 &&
		u.CacheReadInputTokens <= 0 &&
		u.ReasoningOutputTokens <= 0 &&
		u.TotalTokens <= 0
}

func (u TokenUsage) DerivedTotal() int {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

func (u *TokenUsage) Add(other TokenUsage) {
	if u == nil || other.Empty() {
		return
	}
	u.InputTokens += other.InputTokens
	u.OutputTokens += other.OutputTokens
	u.CacheCreationInputTokens += other.CacheCreationInputTokens
	u.CacheReadInputTokens += other.CacheReadInputTokens
	u.ReasoningOutputTokens += other.ReasoningOutputTokens
	if other.TotalTokens > 0 {
		u.TotalTokens += other.TotalTokens
	} else {
		u.TotalTokens += other.DerivedTotal()
	}
}

func (u *TokenUsage) Max(other TokenUsage) {
	if u == nil || other.Empty() {
		return
	}
	u.InputTokens = maxInt(u.InputTokens, other.InputTokens)
	u.OutputTokens = maxInt(u.OutputTokens, other.OutputTokens)
	u.CacheCreationInputTokens = maxInt(u.CacheCreationInputTokens, other.CacheCreationInputTokens)
	u.CacheReadInputTokens = maxInt(u.CacheReadInputTokens, other.CacheReadInputTokens)
	u.ReasoningOutputTokens = maxInt(u.ReasoningOutputTokens, other.ReasoningOutputTokens)
	u.TotalTokens = maxInt(u.TotalTokens, other.TotalTokens)
	if u.TotalTokens == 0 {
		u.TotalTokens = u.DerivedTotal()
	}
}

func maxInt(a, b int) int {
	if b > a {
		return b
	}
	return a
}

func normalizeSessionRoleSource(raw string) string {
	raw = strings.TrimSpace(strings.ToLower(raw))
	switch raw {
	case "user", "human", "main":
		return "user"
	case "subagent", "agent", "derived":
		return "subagent"
	default:
		return raw
	}
}

func setTraceProjectName(trace *SessionTrace, project, source string) {
	project = trustedTraceProjectName(project)
	if trace == nil || project == "" {
		return
	}
	if trustedTraceProjectName(trace.Project) != "" && projectAttributionSourceRank(source) < projectAttributionSourceRank(trace.ProjectSource) {
		return
	}
	trace.Project = project
	trace.ProjectSource = source
}

// localPathFromFileURL turns a file:// cwd into the plain path the rest of
// attribution expects. Codex emits tool-payload cwds as file:// URLs, and the
// whole-line cwd scan picks those up alongside the plain session_meta one. Left
// as-is, the scheme makes every os.Stat in the repo-boundary walk miss, so a
// git worktree never rolls up to its main repo and the worktree directory name
// becomes a project of its own. Anything that is not a file URL is returned
// untouched — relative fixtures and percent-encoded vendor paths must keep
// flowing through unchanged.
func localPathFromFileURL(path string) string {
	trimmed := strings.TrimSpace(path)
	if !strings.HasPrefix(strings.ToLower(trimmed), "file:") {
		return path
	}
	parsed, err := url.Parse(trimmed)
	// A file URL names a local path only with an empty or localhost host;
	// file://server/share is a remote UNC path this process cannot resolve.
	if err != nil || parsed.Path == "" || (parsed.Host != "" && !strings.EqualFold(parsed.Host, "localhost")) {
		return path
	}
	return parsed.Path
}

func setTraceProjectPath(trace *SessionTrace, path, source string) {
	path = localPathFromFileURL(path)
	repoRoot, worktree, branch := resolveRepoBoundary(path)
	// An agent that mktemp'd a sandbox mid-session (audit checkout, extracted
	// archive) and cd'd into it reports that scratch path as another cwd. Both
	// cwds arrive at the same rank, so the scratch one wins by recency and
	// renames the project after the temp directory. It may still name a project
	// when nothing else does; it must not displace one already attributed.
	// A repo living under /tmp resolves normally and is unaffected.
	scratchOverwrite := repoRoot == "" && isGenericTemporaryPath(path) &&
		trace != nil && trustedTraceProjectName(trace.Project) != ""
	if !scratchOverwrite {
		if project := trustedPathProjectName(path); project != "" {
			setTraceProjectName(trace, project, source)
		}
	}
	if trace != nil {
		if worktree != "" {
			if trace.Worktree == "" {
				trace.Worktree = worktree
			}
			if trace.Branch == "" && branch != "" {
				trace.Branch = branch
			}
		} else if _, wt := resolvePathWorktree(path); wt != "" {
			if trace.Worktree == "" {
				trace.Worktree = wt
			}
		}
	}
}

func isClaudeActiveType(kind string) bool {
	kind = strings.TrimSpace(strings.ToLower(kind))
	switch kind {
	case "", "queue-operation", "last-prompt":
		return false
	default:
		return true
	}
}

func finalizeTrace(trace *SessionTrace) {
	if trace == nil || len(trace.EventTimes) == 0 {
		return
	}
	sort.Slice(trace.EventTimes, func(i, j int) bool {
		return trace.EventTimes[i].Before(trace.EventTimes[j])
	})
	trace.EventTimes = dedupeTimes(trace.EventTimes)
	trace.FirstEvent = trace.EventTimes[0]
	trace.LastEvent = trace.EventTimes[len(trace.EventTimes)-1]
}

func nonEmptyTrace(trace *SessionTrace) *SessionTrace {
	if trace == nil || len(trace.EventTimes) == 0 {
		return nil
	}
	return trace
}

func dedupeTimes(times []time.Time) []time.Time {
	if len(times) < 2 {
		return times
	}
	out := make([]time.Time, 0, len(times))
	for _, ts := range times {
		if len(out) > 0 && out[len(out)-1].Equal(ts) {
			continue
		}
		out = append(out, ts)
	}
	return out
}

func forEachJSONLLine(path string, fn func([]byte) bool) error {
	return forEachJSONLLineFromOffset(path, 0, fn)
}

func forEachJSONLLineFromOffset(path string, offset int64, fn func([]byte) bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSpace(line)
			if len(line) > 0 {
				if !fn(line) {
					return nil
				}
			}
		}
		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func fileEndsWithNewline(path string, size int64) bool {
	if size <= 0 {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if _, err := f.Seek(size-1, io.SeekStart); err != nil {
		return false
	}
	var last [1]byte
	if _, err := io.ReadFull(f, last[:]); err != nil {
		return false
	}
	return last[0] == '\n'
}

func parseTimestampString(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts
		}
	}
	return time.Time{}
}

func jsonStringField(line []byte, key string) string {
	if len(line) == 0 || key == "" {
		return ""
	}
	pattern := []byte(`"` + key + `"`)
	index := bytes.Index(line, pattern)
	for index >= 0 {
		valueStart := index + len(pattern)
		for valueStart < len(line) && isJSONSpace(line[valueStart]) {
			valueStart++
		}
		if valueStart >= len(line) || line[valueStart] != ':' {
			next := bytes.Index(line[index+len(pattern):], pattern)
			if next < 0 {
				return ""
			}
			index += len(pattern) + next
			continue
		}
		valueStart++
		for valueStart < len(line) && isJSONSpace(line[valueStart]) {
			valueStart++
		}
		if valueStart >= len(line) || line[valueStart] != '"' {
			return ""
		}
		value, ok := readJSONString(line[valueStart:])
		if !ok {
			return ""
		}
		return value
	}
	return ""
}

// jsonTrueField reports whether key is present with the literal value true.
// The transcript scanners stay on byte scans rather than full unmarshalling,
// and an absent or false key must read the same: not true.
func jsonTrueField(line []byte, key string) bool {
	if len(line) == 0 || key == "" {
		return false
	}
	pattern := []byte(`"` + key + `":`)
	index := bytes.Index(line, pattern)
	if index < 0 {
		return false
	}
	value := index + len(pattern)
	for value < len(line) && isJSONSpace(line[value]) {
		value++
	}
	return bytes.HasPrefix(line[value:], []byte("true"))
}

// jsonEpochSecondsField reads an unquoted integer field as epoch seconds. Grok
// stamps every update line with one, where the other vendors write RFC3339
// strings; a value outside a sane range is treated as absent rather than
// projected onto 1970 or the far future.
func jsonEpochSecondsField(line []byte, key string) time.Time {
	if len(line) == 0 || key == "" {
		return time.Time{}
	}
	pattern := []byte(`"` + key + `":`)
	index := bytes.Index(line, pattern)
	if index < 0 {
		return time.Time{}
	}
	value := index + len(pattern)
	for value < len(line) && isJSONSpace(line[value]) {
		value++
	}
	end := value
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	if end == value {
		return time.Time{}
	}
	seconds, err := strconv.ParseInt(string(line[value:end]), 10, 64)
	if err != nil || seconds < 1_000_000_000 || seconds > 100_000_000_000 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}

func jsonNestedStringField(line []byte, parent, key string) string {
	if len(line) == 0 || parent == "" || key == "" {
		return ""
	}
	pattern := []byte(`"` + parent + `"`)
	index := bytes.Index(line, pattern)
	for index >= 0 {
		objectStart := index + len(pattern)
		for objectStart < len(line) && isJSONSpace(line[objectStart]) {
			objectStart++
		}
		if objectStart >= len(line) || line[objectStart] != ':' {
			next := bytes.Index(line[index+len(pattern):], pattern)
			if next < 0 {
				return ""
			}
			index += len(pattern) + next
			continue
		}
		objectStart++
		for objectStart < len(line) && isJSONSpace(line[objectStart]) {
			objectStart++
		}
		if objectStart >= len(line) || line[objectStart] != '{' {
			return ""
		}
		objectEnd := findJSONObjectEnd(line, objectStart)
		if objectEnd <= objectStart {
			return ""
		}
		return jsonStringField(line[objectStart:objectEnd+1], key)
	}
	return ""
}

func readJSONString(raw []byte) (string, bool) {
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	escaped := false
	for i := 1; i < len(raw); i++ {
		switch {
		case escaped:
			escaped = false
		case raw[i] == '\\':
			escaped = true
		case raw[i] == '"':
			value := raw[1:i]
			if bytes.IndexByte(value, '\\') < 0 {
				return string(value), true
			}
			var decoded string
			if err := json.Unmarshal(raw[:i+1], &decoded); err != nil {
				return "", false
			}
			return decoded, true
		}
	}
	return "", false
}

func findJSONObjectEnd(raw []byte, start int) int {
	if start < 0 || start >= len(raw) || raw[start] != '{' {
		return -1
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(raw); i++ {
		ch := raw[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case ch == '\\':
				escaped = true
			case ch == '"':
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

func isJSONSpace(ch byte) bool {
	return ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t'
}

func stringValue(v interface{}) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func nestedString(obj map[string]interface{}, path ...string) string {
	current := interface{}(obj)
	for _, part := range path {
		m, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		current = m[part]
	}
	return stringValue(current)
}

func extractClaudeProjectFromPath(path string) string {
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	for i := 0; i < len(parts)-2; i++ {
		if parts[i] == ".claude" && i+2 < len(parts) && parts[i+1] == "projects" {
			encoded := parts[i+2]
			// A worktree's encoded directory carries the main repo, then the
			// worktree suffix. The project is the part before that suffix, so a
			// worktree rolls up to the project body rather than becoming its own.
			for _, marker := range []string{"--worktrees-", "--claude-worktrees-", "-worktrees-"} {
				if idx := strings.Index(encoded, marker); idx != -1 {
					encoded = encoded[:idx]
					break
				}
			}
			chunks := strings.FieldsFunc(encoded, func(r rune) bool { return r == '-' })
			if len(chunks) == 0 {
				return "unknown"
			}
			return chunks[len(chunks)-1]
		}
	}
	return "unknown"
}

func readJSONFileMap(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	return obj, nil
}

func appendNonZeroTimes(base []time.Time, values ...time.Time) []time.Time {
	for _, value := range values {
		if !value.IsZero() {
			base = append(base, value)
		}
	}
	return base
}

func firstNonZeroTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}

func cloneTranscriptData(in *TranscriptData) *TranscriptData {
	if in == nil {
		return nil
	}
	out := &TranscriptData{
		Traces:                           make(map[string]*SessionTrace, len(in.Traces)),
		SessionSpans:                     append([]Interval(nil), in.SessionSpans...),
		BurstSpans:                       append([]Interval(nil), in.BurstSpans...),
		ScannedFiles:                     in.ScannedFiles,
		ParsedFiles:                      in.ParsedFiles,
		DeferredFiles:                    in.DeferredFiles,
		TailParsedFiles:                  in.TailParsedFiles,
		HistoricalScanDeferred:           in.HistoricalScanDeferred,
		CoverageIncomplete:               in.CoverageIncomplete,
		ForegroundScanLookbackSeconds:    in.ForegroundScanLookbackSeconds,
		ConfiguredHistoryLookbackSeconds: in.ConfiguredHistoryLookbackSeconds,
		Errors:                           append([]string(nil), in.Errors...),
		evidenceRevision:                 in.evidenceRevision,
	}
	for path, trace := range in.Traces {
		if trace == nil {
			continue
		}
		cloned := *trace
		cloned.EventTimes = append([]time.Time(nil), trace.EventTimes...)
		out.Traces[path] = &cloned
	}
	return out
}

func cloneSessionTrace(trace *SessionTrace) *SessionTrace {
	if trace == nil {
		return nil
	}
	cloned := *trace
	cloned.EventTimes = append([]time.Time(nil), trace.EventTimes...)
	return &cloned
}
