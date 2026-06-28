package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

type Observer struct {
	cfg              Config
	mu               sync.Mutex
	cache            transcriptCacheState
	inflight         map[string]*transcriptScanFlight
	fileCache        map[string]fileTraceCache
	knownClaudeRoots []string
	knownCodexRoots  []string
	knownTraeRoots   []string
}

func newObserver(cfg Config) *Observer {
	return &Observer{
		cfg:              cfg,
		inflight:         map[string]*transcriptScanFlight{},
		fileCache:        map[string]fileTraceCache{},
		knownClaudeRoots: append([]string(nil), cfg.ClaudeRoots...),
		knownCodexRoots:  append([]string(nil), cfg.CodexRoots...),
		knownTraeRoots:   append([]string(nil), cfg.TraeRoots...),
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
	Err             string
}

type transcriptParseFunc func(TranscriptFile) (*SessionTrace, error)

var parseTranscriptFileFunc transcriptParseFunc = parseTranscriptFile

type transcriptTailParseFunc = transcriptParseFunc

var parseTranscriptFileTailFunc transcriptTailParseFunc = parseTranscriptFileTail

type transcriptAppendParseFunc func(TranscriptFile, *SessionTrace, int64) (*SessionTrace, error)

var parseTranscriptFileAppendFunc transcriptAppendParseFunc = parseTranscriptFileAppend

type transcriptScanFlight struct {
	done chan struct{}
	data *TranscriptData
}

func (o *Observer) transcriptData(claudeRoots, codexRoots, traeRoots []string, priority []TranscriptFile, now time.Time) (*TranscriptData, bool) {
	key := transcriptCacheKey(claudeRoots, codexRoots, traeRoots, priority, o.cfg.IdleGap, o.cfg.MinInterval, o.cfg.Lookback)
	o.mu.Lock()
	if o.cache.Data != nil && o.cache.Key == key && now.Before(o.cache.ExpiresAt) {
		data := cloneTranscriptData(o.cache.Data)
		o.mu.Unlock()
		return data, true
	}
	if flight := o.inflight[key]; flight != nil {
		done := flight.done
		o.mu.Unlock()
		<-done
		return cloneTranscriptData(flight.data), false
	}
	flight := &transcriptScanFlight{done: make(chan struct{})}
	o.inflight[key] = flight
	o.mu.Unlock()

	data := o.scanTranscriptsWithOptions(claudeRoots, codexRoots, traeRoots, priority, transcriptScanOptions{
		HistoryCutoff:      now.Add(-o.cfg.Lookback),
		ForegroundCutoff:   foregroundTranscriptCutoff(now, o.cfg.IdleGap),
		HistoryLookback:    o.cfg.Lookback,
		ForegroundLookback: foregroundTranscriptLookback(o.cfg.IdleGap),
		DeferHistoryWalk:   true,
		IdleGap:            o.cfg.IdleGap,
		MinInterval:        o.cfg.MinInterval,
	})
	savedAt := time.Now()

	o.mu.Lock()
	flight.data = cloneTranscriptData(data)
	o.cache = transcriptCacheState{
		Key:       key,
		ExpiresAt: savedAt.Add(o.cfg.TranscriptCacheTTL),
		Data:      cloneTranscriptData(data),
	}
	delete(o.inflight, key)
	close(flight.done)
	o.mu.Unlock()
	return cloneTranscriptData(data), false
}

func transcriptCacheKey(claudeRoots, codexRoots, traeRoots []string, priority []TranscriptFile, idleGap, minInterval, lookback time.Duration) string {
	priorityParts := make([]string, 0, len(priority))
	for _, file := range priority {
		priorityParts = append(priorityParts, file.Tool+":"+file.Path)
	}
	parts := []string{
		"claude:" + strings.Join(claudeRoots, "|"),
		"codex:" + strings.Join(codexRoots, "|"),
		"trae:" + strings.Join(traeRoots, "|"),
		"priority:" + strings.Join(priorityParts, "|"),
		"idle_gap:" + idleGap.String(),
		"min_interval:" + minInterval.String(),
		"lookback:" + lookback.String(),
	}
	return strings.Join(parts, "\n")
}

func (o *Observer) scanTranscripts(claudeRoots, codexRoots, traeRoots []string, priority []TranscriptFile, cutoff time.Time, idleGap, minInterval time.Duration) *TranscriptData {
	return o.scanTranscriptsWithOptions(claudeRoots, codexRoots, traeRoots, priority, transcriptScanOptions{
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

func (o *Observer) scanTranscriptsWithOptions(claudeRoots, codexRoots, traeRoots []string, priority []TranscriptFile, opts transcriptScanOptions) *TranscriptData {
	collectionCutoff := opts.HistoryCutoff
	if opts.DeferHistoryWalk && !opts.ForegroundCutoff.IsZero() {
		collectionCutoff = opts.ForegroundCutoff
	}
	files := collectTranscriptCandidates(claudeRoots, codexRoots, traeRoots, priority, collectionCutoff, opts.ForegroundCutoff)
	data := &TranscriptData{
		Traces:                           make(map[string]*SessionTrace, len(files)),
		ScannedFiles:                     len(files),
		HistoricalScanDeferred:           opts.DeferHistoryWalk,
		ForegroundScanLookbackSeconds:    int(opts.ForegroundLookback / time.Second),
		ConfiguredHistoryLookbackSeconds: int(opts.HistoryLookback / time.Second),
	}

	toParse := make([]transcriptCandidate, 0, len(files))
	toAppend := make([]transcriptAppendCandidate, 0)
	o.mu.Lock()
	for _, candidate := range files {
		if candidate.Deferred {
			data.DeferredFiles++
			continue
		}
		cached, ok := o.fileCache[candidate.File.Path]
		switch {
		case !ok:
			toParse = append(toParse, candidate)
			continue
		case cached.Size == candidate.Size && cached.ModTime.Equal(candidate.ModTime):
			if cached.Err != "" {
				data.Errors = append(data.Errors, fmt.Sprintf("%s: %s", candidate.File.Path, cached.Err))
				continue
			}
			if cached.Trace == nil || len(cached.Trace.EventTimes) == 0 {
				continue
			}
			data.Traces[candidate.File.Path] = cloneSessionTrace(cached.Trace)
			data.ParsedFiles++
			continue
		case canAppendParseTranscript(candidate.File, cached, candidate):
			toAppend = append(toAppend, transcriptAppendCandidate{
				Candidate: candidate,
				Base:      cloneSessionTrace(cached.Trace),
				Offset:    cached.Size,
			})
			continue
		case cached.Err != "":
			data.Errors = append(data.Errors, fmt.Sprintf("%s: %s", candidate.File.Path, cached.Err))
			toParse = append(toParse, candidate)
			continue
		default:
			toParse = append(toParse, candidate)
		}
	}
	o.mu.Unlock()

	updates := map[string]fileTraceCache{}
	parseResults := parseTranscriptCandidates(toParse)
	parseResults = append(parseResults, parseAppendTranscriptCandidates(toAppend)...)
	for _, result := range parseResults {
		candidate := result.Candidate
		endsWithNewline := fileEndsWithNewline(candidate.File.Path, candidate.Size)
		if result.Err != nil {
			data.Errors = append(data.Errors, fmt.Sprintf("%s: %v", candidate.File.Path, result.Err))
			updates[candidate.File.Path] = fileTraceCache{
				ModTime:         candidate.ModTime,
				Size:            candidate.Size,
				EndsWithNewline: endsWithNewline,
				Err:             result.Err.Error(),
			}
			continue
		}
		updates[candidate.File.Path] = fileTraceCache{
			ModTime:         candidate.ModTime,
			Size:            candidate.Size,
			EndsWithNewline: endsWithNewline,
			Trace:           cloneSessionTrace(result.Trace),
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
	data.SessionSpans = buildSessionSpans(data.Traces, opts.MinInterval)
	data.BurstSpans = buildBurstSpans(data.Traces, opts.IdleGap, opts.MinInterval)
	return data
}

func foregroundTranscriptLookback(idleGap time.Duration) time.Duration {
	if idleGap <= 0 {
		idleGap = 90 * time.Second
	}
	lookback := idleGap * 80
	if lookback < 2*time.Hour {
		lookback = 2 * time.Hour
	}
	if lookback > 6*time.Hour {
		lookback = 6 * time.Hour
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
	Err       error
}

type transcriptAppendCandidate struct {
	Candidate transcriptCandidate
	Base      *SessionTrace
	Offset    int64
}

func canAppendParseTranscript(file TranscriptFile, cached fileTraceCache, candidate transcriptCandidate) bool {
	if candidate.Size <= cached.Size || !cached.EndsWithNewline {
		return false
	}
	if cached.Err != "" || cached.Trace == nil || len(cached.Trace.EventTimes) == 0 {
		return false
	}
	if file.Tool == "codex" && strings.Contains(file.Path, string(filepath.Separator)+".codexl"+string(filepath.Separator)) {
		return false
	}
	switch file.Tool {
	case "claude", "codex", "trae":
		return true
	default:
		return false
	}
}

func parseTranscriptCandidates(candidates []transcriptCandidate) []transcriptParseResult {
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
				parseFunc := parseTranscriptFileFunc
				if item.Candidate.TailParse {
					parseFunc = parseTranscriptFileTailFunc
				}
				trace, err := parseFunc(item.Candidate.File)
				results[item.Index] = transcriptParseResult{
					Candidate: item.Candidate,
					Trace:     trace,
					Err:       err,
				}
			}
		}()
	}
	for index, candidate := range candidates {
		jobs <- job{Index: index, Candidate: candidate}
	}
	close(jobs)
	wg.Wait()
	return results
}

func parseAppendTranscriptCandidates(candidates []transcriptAppendCandidate) []transcriptParseResult {
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
				trace, err := parseTranscriptFileAppendFunc(
					item.Candidate.Candidate.File,
					item.Candidate.Base,
					item.Candidate.Offset,
				)
				results[item.Index] = transcriptParseResult{
					Candidate: item.Candidate.Candidate,
					Trace:     trace,
					Err:       err,
				}
			}
		}()
	}
	for index, candidate := range candidates {
		jobs <- job{Index: index, Candidate: candidate}
	}
	close(jobs)
	wg.Wait()
	return results
}

func collectTranscriptCandidates(claudeRoots, codexRoots, traeRoots []string, priority []TranscriptFile, historyCutoff, foregroundCutoff time.Time) []transcriptCandidate {
	priorityKeys := map[string]struct{}{}
	for _, file := range priority {
		if file.Path == "" || file.Tool == "" {
			continue
		}
		priorityKeys[file.Tool+"\x00"+filepath.Clean(file.Path)] = struct{}{}
	}
	seen := map[string]transcriptCandidate{}
	addFile := func(file TranscriptFile, info os.FileInfo) {
		if file.Path == "" || file.Tool == "" {
			return
		}
		file.Path = filepath.Clean(file.Path)
		key := file.Tool + "\x00" + file.Path
		_, priorityFile := priorityKeys[key]
		deferred := false
		tailParse := false
		if !priorityFile && !foregroundCutoff.IsZero() {
			if info.ModTime().Before(foregroundCutoff) {
				deferred = true
			} else if !fileMayContainEventsAfterCutoff(file.Path, info, foregroundCutoff) {
				deferred = true
			} else {
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
	for _, file := range priority {
		info, err := os.Stat(file.Path)
		if err != nil || info.IsDir() {
			continue
		}
		addFile(file, info)
	}

	for _, root := range claudeRoots {
		projectsDir := filepath.Join(root, "projects")
		walkMatchingFiles(projectsDir, historyCutoff, func(path string, info os.FileInfo) {
			addFile(TranscriptFile{Tool: "claude", Path: path}, info)
		})
	}
	for _, root := range codexRoots {
		for _, dir := range []string{
			filepath.Join(root, "sessions"),
			filepath.Join(root, "archived_sessions"),
		} {
			walkMatchingFiles(dir, historyCutoff, func(path string, info os.FileInfo) {
				addFile(TranscriptFile{Tool: "codex", Path: path}, info)
			})
		}
		walkCodexLaneEvents(filepath.Join(root, ".codexl"), historyCutoff, func(path string, info os.FileInfo) {
			addFile(TranscriptFile{Tool: "codex", Path: path}, info)
		})
	}
	for _, root := range traeRoots {
		walkMatchingFiles(filepath.Join(root, "sessions"), historyCutoff, func(path string, info os.FileInfo) {
			addFile(TranscriptFile{Tool: "trae", Path: path}, info)
		})
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
	return files
}

func walkMatchingFiles(root string, cutoff time.Time, fn func(path string, info os.FileInfo)) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
			return nil
		}
		fn(filepath.Clean(path), info)
		return nil
	})
}

func walkCodexLaneEvents(root string, cutoff time.Time, fn func(path string, info os.FileInfo)) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return
	}
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if filepath.Base(path) != "events.jsonl" {
			return nil
		}
		if !cutoff.IsZero() && info.ModTime().Before(cutoff) {
			return nil
		}
		fn(filepath.Clean(path), info)
		return nil
	})
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
	return forEachJSONLTailLine(path, info.Size(), 512*1024, fn)
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

func parseTranscriptFile(file TranscriptFile) (*SessionTrace, error) {
	switch {
	case file.Tool == "claude":
		return parseClaudeTrace(file.Path)
	case file.Tool == "codex" && strings.Contains(file.Path, string(filepath.Separator)+".codexl"+string(filepath.Separator)):
		return parseCodexLaneTrace(file.Path)
	case file.Tool == "codex":
		return parseCodexTrace(file.Path)
	case file.Tool == "trae":
		return parseTraeTrace(file.Path)
	default:
		return nil, fmt.Errorf("unsupported transcript type")
	}
}

func parseTranscriptFileTail(file TranscriptFile) (*SessionTrace, error) {
	if file.Tool == "codex" && strings.Contains(file.Path, string(filepath.Separator)+".codexl"+string(filepath.Separator)) {
		return parseTranscriptFile(file)
	}
	switch file.Tool {
	case "claude":
		trace := &SessionTrace{
			Tool:             "claude",
			Path:             file.Path,
			SessionID:        strings.TrimSuffix(filepath.Base(file.Path), filepath.Ext(file.Path)),
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
	case "codex":
		trace := &SessionTrace{
			Tool:             "codex",
			Path:             file.Path,
			SessionID:        fallbackSessionIDForFile(file),
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
	case "trae":
		trace := &SessionTrace{
			Tool:             "trae",
			Path:             file.Path,
			SessionID:        fallbackSessionIDForFile(file),
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
	default:
		return nil, fmt.Errorf("unsupported transcript type")
	}
}

func parseTranscriptFileAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if base == nil {
		return nil, fmt.Errorf("missing cached trace for append parse")
	}
	if offset < 0 {
		return nil, fmt.Errorf("invalid append offset %d", offset)
	}
	switch {
	case file.Tool == "claude":
		trace := cloneSessionTrace(base)
		trace.Tool = "claude"
		trace.Path = file.Path
		err := forEachJSONLLineFromOffset(file.Path, offset, func(line []byte) bool {
			processClaudeTraceLine(trace, line)
			return true
		})
		if err != nil {
			return nil, err
		}
		finalizeTrace(trace)
		return nonEmptyTrace(trace), nil
	case file.Tool == "codex" && strings.Contains(file.Path, string(filepath.Separator)+".codexl"+string(filepath.Separator)):
		return parseTranscriptFile(file)
	case file.Tool == "codex":
		trace := cloneSessionTrace(base)
		trace.Tool = "codex"
		trace.Path = file.Path
		err := forEachJSONLLineFromOffset(file.Path, offset, func(line []byte) bool {
			processCodexTraceLine(trace, line)
			return true
		})
		if err != nil {
			return nil, err
		}
		finalizeTrace(trace)
		return nonEmptyTrace(trace), nil
	case file.Tool == "trae":
		trace := cloneSessionTrace(base)
		trace.Tool = "trae"
		trace.Path = file.Path
		err := forEachJSONLLineFromOffset(file.Path, offset, func(line []byte) bool {
			processTraeTraceLine(trace, line)
			return true
		})
		if err != nil {
			return nil, err
		}
		finalizeTrace(trace)
		return nonEmptyTrace(trace), nil
	default:
		return nil, fmt.Errorf("unsupported transcript type")
	}
}

func parseClaudeTrace(path string) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "claude",
		Path:             path,
		SessionID:        strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
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
	if sid := firstNonEmptyString(
		jsonStringField(line, "sessionId"),
		jsonStringField(line, "session_id"),
	); sid != "" {
		trace.SessionID = sid
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
		SessionID:        fallbackSessionIDForFile(TranscriptFile{Tool: "codex", Path: path}),
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

	_ = forEachJSONLLineUntil(eventsPath, func(line []byte) bool {
		if threadID := firstNonEmptyString(
			jsonStringField(line, "thread_id"),
			jsonStringField(line, "session_id"),
			jsonStringField(line, "sessionId"),
		); threadID != "" {
			trace.SessionID = threadID
			return false
		}
		return true
	})

	requestObj, _ := readJSONFileMap(filepath.Join(filepath.Dir(eventsPath), "request.json"))
	runObj, _ := readJSONFileMap(filepath.Join(filepath.Dir(eventsPath), "run.json"))
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
		return nil, nil
	}
	return trace, nil
}

func parseTraeTrace(path string) (*SessionTrace, error) {
	trace := &SessionTrace{
		Tool:             "trae",
		Path:             path,
		SessionID:        fallbackSessionIDForFile(TranscriptFile{Tool: "trae", Path: path}),
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

func setTraceProjectPath(trace *SessionTrace, path, source string) {
	if project := trustedPathProjectName(path); project != "" {
		setTraceProjectName(trace, project, source)
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

func extractCodexSessionID(obj map[string]interface{}) string {
	return firstNonEmptyString(
		nestedString(obj, "payload", "id"),
		nestedString(obj, "payload", "session_id"),
		nestedString(obj, "payload", "sessionId"),
		stringValue(obj["session_id"]),
		stringValue(obj["sessionId"]),
	)
}

func extractCodexCWD(obj map[string]interface{}) string {
	return firstNonEmptyString(
		nestedString(obj, "payload", "cwd"),
		nestedString(obj, "cwd"),
	)
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

func forEachJSONLLineUntil(path string, fn func([]byte) bool) error {
	return forEachJSONLLine(path, fn)
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
		ForegroundScanLookbackSeconds:    in.ForegroundScanLookbackSeconds,
		ConfiguredHistoryLookbackSeconds: in.ConfiguredHistoryLookbackSeconds,
		Errors:                           append([]string(nil), in.Errors...),
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
