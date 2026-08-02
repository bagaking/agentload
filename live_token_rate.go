package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	liveTokenRateSampleInterval    = 30 * time.Second
	liveTokenRateDiscoverEvery     = 30 * time.Second
	liveTokenRateDirectoryRescan   = 2 * time.Minute
	liveTokenRateWindow            = 180 * time.Second
	liveTokenRateStaleAfter        = 5 * time.Minute
	liveTokenRateRecentFileAge     = 15 * time.Minute
	liveTokenRateTrackedFileMaxAge = 30 * time.Minute
	liveTokenRateMessageRetention  = 10 * time.Minute
	liveTokenRateMaxAppendRead     = 512 * 1024
	liveTokenRateMaxFiles          = 96
	liveTokenRateMaxDirectories    = 2048
	liveTokenRateMaxMessages       = 2048
	liveTokenRateMaxEvents         = 32768
	liveTokenRateFutureSkew        = 5 * time.Second
	liveTokenRateFingerprintBytes  = 128
)

type liveTokenRateRoot struct {
	Tool string
	Path string
}

type liveTokenRateTrackedDirectory struct {
	ModTime     time.Time
	LastScanned time.Time
	ChildDirs   []string
}

type liveTokenRateMessageUsage struct {
	Output   int64
	LastSeen time.Time
	Sequence uint64
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
	MessageUsage     map[string]liveTokenRateMessageUsage
	MessageSequence  uint64
}

type liveTokenRateObservation struct {
	At              time.Time
	OutputTokens    int64
	Cumulative      bool
	MessageIdentity string
}

type liveTokenRatePublished struct {
	Configured   bool
	Initialized  bool
	LimitedUntil time.Time
	LatestSignal time.Time
	LatestEvent  time.Time
	Events       []liveTokenRateEvent
}

// liveTokenRateSampler keeps collection baselines private to one background
// owner. API clients read the independently published event snapshot and never
// advance file offsets or cumulative counters.
type liveTokenRateSampler struct {
	pollMu sync.Mutex

	roots        []liveTokenRateRoot
	files        map[string]liveTokenRateTrackedFile
	directories  map[string]liveTokenRateTrackedDirectory
	events       []liveTokenRateEvent
	lastPoll     time.Time
	lastDiscover time.Time
	initialized  bool
	latestSignal time.Time
	latestEvent  time.Time
	limitedUntil time.Time

	publishedMu sync.RWMutex
	published   liveTokenRatePublished

	lifecycleMu sync.Mutex
	running     bool
	stop        chan struct{}
	done        chan struct{}
}

func newLiveTokenRateSampler(cfg Config) *liveTokenRateSampler {
	sampler := &liveTokenRateSampler{
		roots:       liveTokenRateRootsFromConfig(cfg),
		files:       map[string]liveTokenRateTrackedFile{},
		directories: map[string]liveTokenRateTrackedDirectory{},
	}
	sampler.publish(time.Now())
	return sampler
}

func liveTokenRateRootsFromConfig(cfg Config) []liveTokenRateRoot {
	roots := make([]liveTokenRateRoot, 0, len(cfg.ClaudeRoots)+len(cfg.CodexRoots)*3+len(cfg.TraeRoots))
	for _, root := range cfg.ClaudeRoots {
		roots = append(roots, liveTokenRateRoot{Tool: "claude", Path: filepath.Join(root, "projects")})
	}
	for _, root := range cfg.CodexRoots {
		roots = append(roots,
			liveTokenRateRoot{Tool: "codex", Path: filepath.Join(root, "sessions")},
			liveTokenRateRoot{Tool: "codex", Path: filepath.Join(root, "archived_sessions")},
			liveTokenRateRoot{Tool: "codex", Path: filepath.Join(root, ".codexl")},
		)
	}
	for _, root := range cfg.TraeRoots {
		roots = append(roots, liveTokenRateRoot{Tool: "trae", Path: filepath.Join(root, "sessions")})
	}
	return canonicalLiveTokenRateRoots(roots)
}

func liveTokenRateRootsFromSnapshotConfig(cfg SnapshotConfig) []liveTokenRateRoot {
	return liveTokenRateRootsFromConfig(Config{
		ClaudeRoots: cfg.ClaudeRoots,
		CodexRoots:  cfg.CodexRoots,
		TraeRoots:   cfg.TraeRoots,
	})
}

func canonicalLiveTokenRateRoots(roots []liveTokenRateRoot) []liveTokenRateRoot {
	seen := map[string]struct{}{}
	out := make([]liveTokenRateRoot, 0, len(roots))
	for _, root := range roots {
		root.Tool = strings.TrimSpace(strings.ToLower(root.Tool))
		root.Path = canonicalLiveTokenRatePath(root.Path)
		if root.Tool == "" || root.Path == "" || root.Path == "." {
			continue
		}
		key := root.Tool + "\x00" + root.Path
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, root)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool == out[j].Tool {
			return out[i].Path < out[j].Path
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}

func canonicalLiveTokenRatePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if absolute, err := filepath.Abs(path); err == nil {
		path = absolute
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	return filepath.Clean(path)
}

func (sampler *liveTokenRateSampler) addSnapshotRoots(cfg SnapshotConfig) {
	if sampler == nil {
		return
	}
	additional := liveTokenRateRootsFromSnapshotConfig(cfg)
	sampler.pollMu.Lock()
	sampler.roots = canonicalLiveTokenRateRoots(append(sampler.roots, additional...))
	sampler.publishLocked(time.Now())
	sampler.pollMu.Unlock()
}

func (sampler *liveTokenRateSampler) start(interval time.Duration) {
	if sampler == nil {
		return
	}
	if interval < liveTokenRateSampleInterval {
		interval = liveTokenRateSampleInterval
	}
	sampler.lifecycleMu.Lock()
	if sampler.running {
		sampler.lifecycleMu.Unlock()
		return
	}
	sampler.running = true
	sampler.stop = make(chan struct{})
	sampler.done = make(chan struct{})
	stop := sampler.stop
	done := sampler.done
	sampler.lifecycleMu.Unlock()

	go func() {
		defer close(done)
		sampler.poll(time.Now())
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case now := <-ticker.C:
				sampler.poll(now)
			case <-stop:
				return
			}
		}
	}()
}

func (sampler *liveTokenRateSampler) stopSampler() {
	if sampler == nil {
		return
	}
	sampler.lifecycleMu.Lock()
	if !sampler.running {
		sampler.lifecycleMu.Unlock()
		return
	}
	stop := sampler.stop
	done := sampler.done
	sampler.running = false
	sampler.stop = nil
	sampler.done = nil
	close(stop)
	sampler.lifecycleMu.Unlock()
	<-done
}

func (sampler *liveTokenRateSampler) poll(now time.Time) {
	if sampler == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	sampler.pollMu.Lock()
	defer sampler.pollMu.Unlock()
	sampler.pollLocked(now)
	sampler.publishLocked(now)
}

func (sampler *liveTokenRateSampler) pollLocked(now time.Time) {
	if !sampler.lastPoll.IsZero() && now.Sub(sampler.lastPoll) < liveTokenRateSampleInterval {
		return
	}
	observationGap := !sampler.lastPoll.IsZero() && now.Sub(sampler.lastPoll) > liveTokenRateWindow
	sampler.lastPoll = now
	if sampler.files == nil {
		sampler.files = map[string]liveTokenRateTrackedFile{}
	}
	if sampler.directories == nil {
		sampler.directories = map[string]liveTokenRateTrackedDirectory{}
	}
	if sampler.lastDiscover.IsZero() || now.Sub(sampler.lastDiscover) >= liveTokenRateDiscoverEvery {
		sampler.discoverLocked(now)
		sampler.lastDiscover = now
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
		if observationGap || info.Size()-tracked.Offset > liveTokenRateMaxAppendRead || !liveTokenRateBoundaryMatches(path, tracked) {
			sampler.files[path] = sampler.rebaselineFile(path, tracked.Tool, info, now)
			continue
		}
		updated, events, signals := liveTokenRateReadAppend(path, tracked, info, now)
		sampler.files[path] = updated
		for _, signalAt := range signals {
			sampler.markSignalLocked(signalAt, now)
		}
		for _, event := range events {
			sampler.events = append(sampler.events, event)
			sampler.markEventLocked(event.At, now)
		}
	}
	sampler.pruneLocked(now)
}

func (sampler *liveTokenRateSampler) discoverLocked(now time.Time) {
	type candidate struct {
		Tool string
		Path string
		Info os.FileInfo
	}
	candidates := make([]candidate, 0)
	var discoverDirectory func(liveTokenRateRoot, string)
	discoverDirectory = func(root liveTokenRateRoot, dir string) {
		if len(sampler.directories) >= liveTokenRateMaxDirectories {
			sampler.markLimitedLocked(now)
			return
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			delete(sampler.directories, root.Tool+"\x00"+dir)
			return
		}
		key := root.Tool + "\x00" + dir
		tracked, seen := sampler.directories[key]
		if seen && info.ModTime().Equal(tracked.ModTime) && now.Sub(tracked.LastScanned) < liveTokenRateDirectoryRescan {
			for _, child := range tracked.ChildDirs {
				discoverDirectory(root, child)
			}
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		childDirs := make([]string, 0)
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				childDirs = append(childDirs, path)
				continue
			}
			if !liveTokenRateShouldTrackJSONL(path) {
				continue
			}
			path = canonicalLiveTokenRatePath(path)
			if _, exists := sampler.files[path]; exists {
				continue
			}
			entryInfo, err := entry.Info()
			if err != nil || entryInfo.Size() <= 0 || now.Sub(entryInfo.ModTime()) > liveTokenRateRecentFileAge {
				continue
			}
			candidates = append(candidates, candidate{Tool: root.Tool, Path: path, Info: entryInfo})
		}
		sort.Strings(childDirs)
		sampler.directories[key] = liveTokenRateTrackedDirectory{
			ModTime: info.ModTime(), LastScanned: now, ChildDirs: childDirs,
		}
		for _, child := range childDirs {
			discoverDirectory(root, child)
		}
	}
	for _, root := range sampler.roots {
		discoverDirectory(root, root.Path)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Info.ModTime().Equal(candidates[j].Info.ModTime()) {
			return candidates[i].Path < candidates[j].Path
		}
		return candidates[i].Info.ModTime().After(candidates[j].Info.ModTime())
	})
	available := liveTokenRateMaxFiles - len(sampler.files)
	if len(candidates) > available {
		sampler.markLimitedLocked(now)
	}
	for _, candidate := range candidates {
		if len(sampler.files) >= liveTokenRateMaxFiles {
			break
		}
		sampler.files[candidate.Path] = sampler.rebaselineFile(candidate.Path, candidate.Tool, candidate.Info, now)
	}
}

func (sampler *liveTokenRateSampler) rebaselineFile(path, tool string, info os.FileInfo, now time.Time) liveTokenRateTrackedFile {
	tracked, observations := liveTokenRateReadBaseline(path, tool, info, now)
	for _, observation := range observations {
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
	return tracked
}

func liveTokenRateReadBaseline(path, tool string, info os.FileInfo, now time.Time) (liveTokenRateTrackedFile, []liveTokenRateObservation) {
	tracked := liveTokenRateTrackedFile{Tool: tool, LastSeen: now, Info: info}
	if info == nil || info.Size() <= 0 {
		return tracked, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return tracked, nil
	}
	defer f.Close()
	if openedInfo, err := f.Stat(); err == nil {
		info = openedInfo
		tracked.Info = openedInfo
	}
	size := info.Size()
	readOffset := int64(0)
	if size > liveTokenRateMaxAppendRead {
		readOffset = size - liveTokenRateMaxAppendRead
	}
	if _, err := f.Seek(readOffset, io.SeekStart); err != nil {
		tracked.Offset = size
		return tracked, nil
	}
	data, err := io.ReadAll(io.LimitReader(f, size-readOffset))
	if err != nil {
		tracked.Offset = size
		return tracked, nil
	}
	parseStart := 0
	if readOffset > 0 {
		firstNewline := bytes.IndexByte(data, '\n')
		if firstNewline < 0 {
			tracked.Offset = size
			return tracked, nil
		}
		parseStart = firstNewline + 1
	}
	parseEnd := len(data)
	lastNewline := bytes.LastIndexByte(data[parseStart:], '\n')
	lastLineStart := parseStart
	if lastNewline >= 0 {
		lastLineStart = parseStart + lastNewline + 1
	}
	lastLine := bytes.TrimSpace(data[lastLineStart:])
	if len(lastLine) > 0 && !json.Valid(lastLine) {
		parseEnd = lastLineStart
		if lastLineStart == parseStart && lastNewline < 0 {
			tracked.Offset = readOffset + int64(parseStart)
			return tracked, nil
		}
	}
	observations := liveTokenRateParseLines(data[parseStart:parseEnd], now)
	tracked.Offset = readOffset + int64(parseEnd)
	tracked.Fingerprint, tracked.HasFingerprint = liveTokenRateBoundaryFingerprint(path, tracked.Offset)
	return tracked, observations
}

func liveTokenRateReadAppend(path string, tracked liveTokenRateTrackedFile, info os.FileInfo, now time.Time) (liveTokenRateTrackedFile, []liveTokenRateEvent, []time.Time) {
	if info == nil || info.Size() <= tracked.Offset {
		return tracked, nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return tracked, nil, nil
	}
	defer f.Close()
	openedInfo, err := f.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || openedInfo.Size() != info.Size() {
		return tracked, nil, nil
	}
	if _, err := f.Seek(tracked.Offset, io.SeekStart); err != nil {
		return tracked, nil, nil
	}
	want := info.Size() - tracked.Offset
	data, err := io.ReadAll(io.LimitReader(f, want))
	if err != nil || int64(len(data)) != want {
		return tracked, nil, nil
	}
	lastNewline := bytes.LastIndexByte(data, '\n')
	if lastNewline < 0 {
		return tracked, nil, nil
	}
	parseEnd := lastNewline + 1
	observations := liveTokenRateParseLines(data[:parseEnd], now)
	events := make([]liveTokenRateEvent, 0, len(observations))
	signals := make([]time.Time, 0, len(observations))
	session := tracked.Tool + "\x00" + path
	for _, observation := range observations {
		observation.At = normalizeLiveTokenRateSignalTime(observation.At, now)
		signals = append(signals, observation.At)
		tokens := observation.OutputTokens
		if observation.Cumulative {
			tokens = 0
			if tracked.TotalInitialized && observation.OutputTokens > tracked.LastTotal && !observation.At.Before(tracked.LastTotalAt) {
				events = append(events, newLiveTokenRateIntervalEvent(
					tracked.LastTotalAt, observation.At, observation.OutputTokens-tracked.LastTotal, session,
				))
			}
			tracked.TotalInitialized = true
			tracked.LastTotal = observation.OutputTokens
			tracked.LastTotalAt = observation.At
		} else if observation.MessageIdentity != "" {
			tokens = liveTokenRateMessageDelta(&tracked, observation.MessageIdentity, tokens, now)
		}
		if tokens > 0 {
			events = append(events, liveTokenRateEvent{At: observation.At, Tokens: tokens, Session: session})
		}
	}
	liveTokenRatePruneMessages(&tracked, now)
	tracked.Offset += int64(parseEnd)
	tracked.LastSeen = now
	tracked.Info = info
	tracked.Fingerprint, tracked.HasFingerprint = liveTokenRateBoundaryFingerprint(path, tracked.Offset)
	return tracked, events, signals
}

func liveTokenRateParseLines(data []byte, fallback time.Time) []liveTokenRateObservation {
	observations := []liveTokenRateObservation{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		if observation, ok := liveTokenRateObservationFromJSONLine(scanner.Bytes(), fallback); ok {
			observations = append(observations, observation)
		}
	}
	return observations
}

func liveTokenRateObservationFromJSONLine(line []byte, fallback time.Time) (liveTokenRateObservation, bool) {
	lower := bytes.ToLower(line)
	if !bytes.Contains(lower, []byte("token")) && !bytes.Contains(lower, []byte("usage")) {
		return liveTokenRateObservation{}, false
	}
	var obj map[string]interface{}
	dec := json.NewDecoder(bytes.NewReader(line))
	dec.UseNumber()
	if err := dec.Decode(&obj); err != nil {
		return liveTokenRateObservation{}, false
	}
	at := liveTokenRateTimestamp(obj, fallback)
	if total, ok := liveTokenRateCumulativeOutput(obj); ok {
		return liveTokenRateObservation{At: at, OutputTokens: total, Cumulative: true}, true
	}
	if !liveTokenRateHasOutputDimension(obj) {
		return liveTokenRateObservation{}, false
	}
	usage := tokenUsageFromJSONValue(obj)
	return liveTokenRateObservation{
		At: at, OutputTokens: int64(max(0, usage.OutputTokens)), MessageIdentity: liveTokenRateMessageIdentity(obj),
	}, true
}

func liveTokenRateCumulativeOutput(value interface{}) (int64, bool) {
	switch item := value.(type) {
	case map[string]interface{}:
		if total, ok := mapFromKeys(item, "total_token_usage", "totalTokenUsage"); ok {
			if output, found := liveTokenRateDirectOutput(total); found {
				return output, true
			}
		}
		for _, key := range []string{"total_output_tokens", "totalOutputTokens"} {
			if raw, ok := item[key]; ok {
				if output, valid := liveTokenRateInt64(raw); valid {
					return max(int64(0), output), true
				}
			}
		}
		keys := make([]string, 0, len(item))
		for key := range item {
			if shouldInspectTokenUsageChild(key) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			if output, ok := liveTokenRateCumulativeOutput(item[key]); ok {
				return output, true
			}
		}
	case []interface{}:
		for _, child := range item {
			if output, ok := liveTokenRateCumulativeOutput(child); ok {
				return output, true
			}
		}
	}
	return 0, false
}

func liveTokenRateHasOutputDimension(value interface{}) bool {
	switch item := value.(type) {
	case map[string]interface{}:
		if _, ok := liveTokenRateDirectOutput(item); ok {
			return true
		}
		keys := make([]string, 0, len(item))
		for key := range item {
			if shouldInspectTokenUsageChild(key) {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			if liveTokenRateHasOutputDimension(item[key]) {
				return true
			}
		}
	case []interface{}:
		for _, child := range item {
			if liveTokenRateHasOutputDimension(child) {
				return true
			}
		}
	}
	return false
}

func liveTokenRateDirectOutput(obj map[string]interface{}) (int64, bool) {
	if output, ok := outputTokenCountFromMap(obj); ok {
		return int64(max(0, output)), true
	}
	return 0, false
}

func liveTokenRateInt64(value interface{}) (int64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		return parsed, err == nil && parsed >= 0
	case float64:
		if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > math.MaxInt64 {
			return 0, false
		}
		return int64(number), true
	case int64:
		return number, number >= 0
	case int:
		return int64(number), number >= 0
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(number), 10, 64)
		return parsed, err == nil && parsed >= 0
	default:
		return 0, false
	}
}

func liveTokenRateMessageIdentity(obj map[string]interface{}) string {
	message, _ := obj["message"].(map[string]interface{})
	messageID := ""
	if message != nil {
		messageID, _ = message["id"].(string)
	}
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		messageID, _ = obj["uuid"].(string)
		messageID = strings.TrimSpace(messageID)
	}
	if messageID == "" {
		return ""
	}
	sessionID := ""
	for _, key := range []string{"sessionId", "session_id"} {
		if value, _ := obj[key].(string); strings.TrimSpace(value) != "" {
			sessionID = strings.TrimSpace(value)
			break
		}
	}
	return sessionID + "\x00" + messageID
}

func liveTokenRateTimestamp(obj map[string]interface{}, fallback time.Time) time.Time {
	for _, key := range []string{"timestamp", "ts", "created_at", "createdAt"} {
		if parsed, ok := liveTokenRateParseTimestamp(obj[key]); ok {
			return parsed
		}
	}
	if fallback.IsZero() {
		return time.Now()
	}
	return fallback
}

func liveTokenRateParseTimestamp(value interface{}) (time.Time, bool) {
	switch item := value.(type) {
	case string:
		item = strings.TrimSpace(item)
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
			if parsed, err := time.Parse(layout, item); err == nil {
				return parsed, true
			}
		}
	case json.Number:
		if parsed, err := item.Int64(); err == nil {
			return liveTokenRateUnixTimestamp(parsed)
		}
	case float64:
		return liveTokenRateUnixTimestamp(int64(item))
	case int64:
		return liveTokenRateUnixTimestamp(item)
	}
	return time.Time{}, false
}

func liveTokenRateUnixTimestamp(value int64) (time.Time, bool) {
	if value <= 0 {
		return time.Time{}, false
	}
	if value > 1_000_000_000_000 {
		return time.UnixMilli(value), true
	}
	return time.Unix(value, 0), true
}

func liveTokenRateMessageDelta(tracked *liveTokenRateTrackedFile, identity string, output int64, now time.Time) int64 {
	if tracked == nil || identity == "" {
		return output
	}
	if tracked.MessageUsage == nil {
		tracked.MessageUsage = map[string]liveTokenRateMessageUsage{}
	}
	previous, seen := tracked.MessageUsage[identity]
	delta := output
	if seen {
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
	if tracked.MessageUsage == nil {
		tracked.MessageUsage = map[string]liveTokenRateMessageUsage{}
	}
	previous := tracked.MessageUsage[identity]
	tracked.MessageSequence++
	tracked.MessageUsage[identity] = liveTokenRateMessageUsage{
		Output: max(previous.Output, output), LastSeen: now, Sequence: tracked.MessageSequence,
	}
}

func liveTokenRatePruneMessages(tracked *liveTokenRateTrackedFile, now time.Time) {
	if tracked == nil || len(tracked.MessageUsage) == 0 {
		return
	}
	type age struct {
		Identity string
		Usage    liveTokenRateMessageUsage
	}
	ages := make([]age, 0, len(tracked.MessageUsage))
	for identity, usage := range tracked.MessageUsage {
		if usage.LastSeen.IsZero() || now.Sub(usage.LastSeen) > liveTokenRateMessageRetention {
			delete(tracked.MessageUsage, identity)
			continue
		}
		ages = append(ages, age{Identity: identity, Usage: usage})
	}
	if len(ages) <= liveTokenRateMaxMessages {
		return
	}
	sort.Slice(ages, func(i, j int) bool {
		if ages[i].Usage.LastSeen.Equal(ages[j].Usage.LastSeen) {
			if ages[i].Usage.Sequence == ages[j].Usage.Sequence {
				return ages[i].Identity < ages[j].Identity
			}
			return ages[i].Usage.Sequence < ages[j].Usage.Sequence
		}
		return ages[i].Usage.LastSeen.Before(ages[j].Usage.LastSeen)
	})
	for _, item := range ages[:len(ages)-liveTokenRateMaxMessages] {
		delete(tracked.MessageUsage, item.Identity)
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

func liveTokenRateShouldTrackJSONL(path string) bool {
	if !strings.HasSuffix(strings.ToLower(path), ".jsonl") {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	for _, marker := range []string{"summary", "aggregate", "snapshot", "live-rate", "live_rate"} {
		if strings.Contains(base, marker) {
			return false
		}
	}
	return true
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

func (sampler *liveTokenRateSampler) markLimitedLocked(now time.Time) {
	until := now.Add(liveTokenRateWindow)
	if until.After(sampler.limitedUntil) {
		sampler.limitedUntil = until
	}
}

func (sampler *liveTokenRateSampler) pruneLocked(now time.Time) {
	cutoff := now.Add(-liveTokenRateWindow)
	events := sampler.events[:0]
	for _, event := range sampler.events {
		start, end := event.observedWindow()
		if event.Tokens > 0 && !end.Before(cutoff) && !start.After(now.Add(liveTokenRateFutureSkew)) {
			events = append(events, event)
		}
	}
	sampler.events = events
	if len(sampler.events) <= liveTokenRateMaxEvents {
		return
	}
	sort.Slice(sampler.events, func(i, j int) bool {
		_, left := sampler.events[i].observedWindow()
		_, right := sampler.events[j].observedWindow()
		return left.Before(right)
	})
	sampler.events = append([]liveTokenRateEvent(nil), sampler.events[len(sampler.events)-liveTokenRateMaxEvents:]...)
	sampler.markLimitedLocked(now)
}

func (sampler *liveTokenRateSampler) publish(now time.Time) {
	sampler.pollMu.Lock()
	defer sampler.pollMu.Unlock()
	sampler.publishLocked(now)
}

func (sampler *liveTokenRateSampler) publishLocked(now time.Time) {
	published := liveTokenRatePublished{
		Configured:   len(sampler.roots) > 0,
		Initialized:  sampler.initialized,
		LimitedUntil: sampler.limitedUntil,
		LatestSignal: sampler.latestSignal,
		LatestEvent:  sampler.latestEvent,
		Events:       append([]liveTokenRateEvent(nil), sampler.events...),
	}
	sampler.publishedMu.Lock()
	sampler.published = published
	sampler.publishedMu.Unlock()
}

func (sampler *liveTokenRateSampler) sample(now time.Time) LiveTokenRateSample {
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
	events := append([]liveTokenRateEvent(nil), published.Events...)
	sampler.publishedMu.RUnlock()
	tokens, activeSessions := liveTokenRateWindowFacts(events, now, liveTokenRateWindow, liveTokenRateFutureSkew)
	return liveTokenRateSampleFromFacts(liveTokenRateFacts{
		Configured:     published.Configured,
		Initialized:    published.Initialized,
		Limited:        published.LimitedUntil.After(now),
		TokensInWindow: tokens,
		ActiveSessions: activeSessions,
		LatestSignal:   published.LatestSignal,
		LatestEvent:    published.LatestEvent,
		Window:         liveTokenRateWindow,
		SampleInterval: liveTokenRateSampleInterval,
		StaleAfter:     liveTokenRateStaleAfter,
		SampledAt:      now,
	})
}
