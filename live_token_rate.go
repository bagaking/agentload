package main

import (
	"bufio"
	"bytes"
	"container/list"
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
	liveTokenRateDiscoverEvery     = 30 * time.Second
	liveTokenRateDirectoryRescan   = 2 * time.Minute
	liveTokenRateWindow            = 180 * time.Second
	liveTokenRateStaleAfter        = 5 * time.Minute
	liveTokenRateRecentFileAge     = 15 * time.Minute
	liveTokenRateTrackedFileMaxAge = 30 * time.Minute
	liveTokenRateMessageRetention  = 10 * time.Minute
	liveTokenRateBaselineReadLimit = 512 * 1024
	liveTokenRateMaxJSONLineBytes  = 16 * 1024 * 1024
	liveTokenRateBucketWidth       = time.Second
	liveTokenRateMaxFiles          = 96
	liveTokenRateMaxDirectories    = 2048
	liveTokenRateMaxMessages       = 2048
	liveTokenRateFutureSkew        = 5 * time.Second
	liveTokenRateFingerprintBytes  = 128
)

type liveTokenRateRoot struct {
	Tool     string
	Path     string
	Discover bool
}

type liveTokenRateTrackedDirectory struct {
	ModTime     time.Time
	LastScanned time.Time
	ChildDirs   []string
}

var liveTokenRateReadDir = os.ReadDir

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
}

// liveTokenRateSampler keeps collection baselines private to one background
// owner. API clients read the independently published bucket snapshot and never
// advance file offsets or cumulative counters.
type liveTokenRateSampler struct {
	pollMu sync.Mutex

	adapters        *codingAgentRegistry
	roots           []liveTokenRateRoot
	files           map[string]liveTokenRateTrackedFile
	directories     map[string]liveTokenRateTrackedDirectory
	buckets         []liveTokenRateEvent
	lastPoll        time.Time
	lastDiscover    time.Time
	initialized     bool
	latestSignal    time.Time
	latestEvent     time.Time
	limitedUntil    time.Time
	limitedReason   string
	sessionProjects map[string]string

	watchMu         sync.Mutex
	watchPending    map[string]struct{}
	watchCoverage   bool
	watchIncomplete bool
	watchOverflow   bool

	publishedMu sync.RWMutex
	published   liveTokenRatePublished

	lifecycleMu sync.Mutex
	running     bool
	stop        chan struct{}
	done        chan struct{}
	watcher     liveTokenRateWatcher
}

func newLiveTokenRateSampler(cfg Config, adapters *codingAgentRegistry) *liveTokenRateSampler {
	sampler := &liveTokenRateSampler{
		adapters:        adapters,
		roots:           liveTokenRateRootsFromConfig(cfg, true),
		files:           map[string]liveTokenRateTrackedFile{},
		directories:     map[string]liveTokenRateTrackedDirectory{},
		watchPending:    map[string]struct{}{},
		sessionProjects: map[string]string{},
	}
	sampler.publish(time.Now())
	return sampler
}

func liveTokenRateRootsFromConfig(cfg Config, discover bool) []liveTokenRateRoot {
	roots := make([]liveTokenRateRoot, 0, len(cfg.ClaudeRoots)+len(cfg.CodexRoots)*3+len(cfg.TraeRoots))
	for _, root := range cfg.ClaudeRoots {
		roots = append(roots, liveTokenRateRoot{Tool: "claude", Path: filepath.Join(root, "projects"), Discover: discover})
	}
	for _, root := range cfg.CodexRoots {
		roots = append(roots,
			liveTokenRateRoot{Tool: "codex", Path: filepath.Join(root, "sessions"), Discover: discover},
			liveTokenRateRoot{Tool: "codex", Path: filepath.Join(root, "archived_sessions"), Discover: discover},
			liveTokenRateRoot{Tool: "codex", Path: filepath.Join(root, ".codexl"), Discover: discover},
		)
	}
	for _, root := range cfg.TraeRoots {
		roots = append(roots, liveTokenRateRoot{Tool: "trae", Path: filepath.Join(root, "sessions"), Discover: discover})
	}
	return canonicalLiveTokenRateRoots(roots)
}

func liveTokenRateRootsFromSnapshotConfig(cfg SnapshotConfig) []liveTokenRateRoot {
	return liveTokenRateRootsFromConfig(Config{
		ClaudeRoots: cfg.ClaudeRoots,
		CodexRoots:  cfg.CodexRoots,
		TraeRoots:   cfg.TraeRoots,
	}, false)
}

func canonicalLiveTokenRateRoots(roots []liveTokenRateRoot) []liveTokenRateRoot {
	seen := map[string]int{}
	out := make([]liveTokenRateRoot, 0, len(roots))
	for _, root := range roots {
		root.Tool = strings.TrimSpace(strings.ToLower(root.Tool))
		root.Path = canonicalLiveTokenRatePath(root.Path)
		if root.Tool == "" || root.Path == "" || root.Path == "." {
			continue
		}
		key := root.Tool + "\x00" + root.Path
		if index, ok := seen[key]; ok {
			out[index].Discover = out[index].Discover || root.Discover
			continue
		}
		seen[key] = len(out)
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

func liveTokenRateSessionKey(tool, path string) string {
	tool = strings.TrimSpace(strings.ToLower(tool))
	path = canonicalLiveTokenRatePath(path)
	if tool == "" || path == "" || path == "." {
		return ""
	}
	return tool + "\x00" + path
}

func liveTokenRateProjectsFromSessions(sessions []LiveSessionSnapshot) map[string]string {
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

func (sampler *liveTokenRateSampler) addSnapshotRoots(cfg SnapshotConfig, priority []TranscriptFile, projects map[string]string) {
	if sampler == nil {
		return
	}
	additional := liveTokenRateRootsFromSnapshotConfig(cfg)
	sampler.pollMu.Lock()
	sampler.roots = canonicalLiveTokenRateRoots(append(sampler.roots, additional...))
	sampler.sessionProjects = cloneLiveTokenRateProjects(projects)
	now := time.Now()
	for _, file := range priority {
		sampler.trackPriorityFileLocked(file, now)
	}
	watchPaths := liveTokenRateWatchPaths(sampler.roots)
	sampler.publishLocked(time.Now())
	sampler.pollMu.Unlock()

	sampler.lifecycleMu.Lock()
	watcher := sampler.watcher
	sampler.lifecycleMu.Unlock()
	if watcher != nil {
		coverage := watcher.Update(watchPaths)
		sampler.setWatchCoverage(coverage)
	}
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
		project := strings.TrimSpace(projects[session])
		if project == "" {
			project = liveTokenRateUnassignedProject
		}
		relevant[session] = project
	}
	return relevant
}

func (sampler *liveTokenRateSampler) trackPriorityFileLocked(file TranscriptFile, now time.Time) {
	path := canonicalLiveTokenRatePath(file.Path)
	tool := strings.TrimSpace(strings.ToLower(file.Tool))
	if path == "" || tool == "" || !liveTokenRateShouldTrackJSONL(path) {
		return
	}
	rootTool, covered := sampler.liveTokenRateToolForPathLocked(path)
	if !covered || rootTool != tool {
		return
	}
	if _, tracked := sampler.files[path]; tracked {
		return
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return
	}
	if now.Sub(info.ModTime()) > liveTokenRateRecentFileAge {
		return
	}
	if len(sampler.files) >= liveTokenRateMaxFiles {
		sampler.markLimitedLocked(now, liveTokenRateUnavailableFileCapacity)
		return
	}
	sampler.files[path] = sampler.rebaselineFile(path, tool, info, now)
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
	sampler.pollMu.Lock()
	watcher := newLiveTokenRateWatcher(sampler.roots)
	sampler.watcher = watcher
	coverage := watcher != nil && watcher.Update(liveTokenRateWatchPaths(sampler.roots))
	sampler.pollMu.Unlock()
	sampler.setWatchCoverage(coverage)
	sampler.running = true
	sampler.stop = make(chan struct{})
	sampler.done = make(chan struct{})
	stop := sampler.stop
	done := sampler.done
	sampler.lifecycleMu.Unlock()

	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
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
	if watcher != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case batch, ok := <-watcher.Events():
					if !ok {
						sampler.setWatchCoverage(false)
						return
					}
					sampler.recordWatchBatch(batch)
				case <-stop:
					return
				}
			}
		}()
	}
	go func() {
		workers.Wait()
		close(done)
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
	watcher := sampler.watcher
	sampler.running = false
	sampler.stop = nil
	sampler.done = nil
	sampler.watcher = nil
	close(stop)
	sampler.lifecycleMu.Unlock()
	if watcher != nil {
		watcher.Stop()
	}
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
	sampler.consumeWatchPathsLocked(now)
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
	sampler.pruneLocked(now)
}

func (sampler *liveTokenRateSampler) discoverLocked(now time.Time) {
	type candidate struct {
		Tool string
		Path string
		Info os.FileInfo
	}
	candidates := make([]candidate, 0)
	watchCoverage := sampler.hasCompleteWatchCoverage()
	var discoverDirectory func(liveTokenRateRoot, string)
	discoverDirectory = func(root liveTokenRateRoot, dir string) {
		key := root.Tool + "\x00" + dir
		tracked, seen := sampler.directories[key]
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			sampler.deleteDirectoryTreeLocked(root.Tool, dir)
			return
		}
		if seen && info.ModTime().Equal(tracked.ModTime) && (watchCoverage || now.Sub(tracked.LastScanned) < liveTokenRateDirectoryRescan) {
			for _, child := range tracked.ChildDirs {
				discoverDirectory(root, child)
			}
			return
		}
		if !seen && len(sampler.directories) >= liveTokenRateMaxDirectories {
			sampler.markLimitedLocked(now, liveTokenRateUnavailableDirectoryCapacity)
			return
		}
		entries, err := liveTokenRateReadDir(dir)
		if err != nil {
			return
		}
		childDirs := make([]string, 0)
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if liveTokenRateShouldDescendDirectory(root, path) {
					childDirs = append(childDirs, path)
				}
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
		if seen {
			nextChildren := make(map[string]struct{}, len(childDirs))
			for _, child := range childDirs {
				nextChildren[child] = struct{}{}
			}
			for _, oldChild := range tracked.ChildDirs {
				if _, ok := nextChildren[oldChild]; !ok {
					sampler.deleteDirectoryTreeLocked(root.Tool, oldChild)
				}
			}
		}
		sampler.directories[key] = liveTokenRateTrackedDirectory{
			ModTime: info.ModTime(), LastScanned: now, ChildDirs: childDirs,
		}
		for _, child := range childDirs {
			discoverDirectory(root, child)
		}
	}
	for _, root := range sampler.roots {
		if !root.Discover && watchCoverage {
			continue
		}
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
		sampler.markLimitedLocked(now, liveTokenRateUnavailableFileCapacity)
	}
	for _, candidate := range candidates {
		if len(sampler.files) >= liveTokenRateMaxFiles {
			break
		}
		sampler.files[candidate.Path] = sampler.rebaselineFile(candidate.Path, candidate.Tool, candidate.Info, now)
	}
}

func liveTokenRateShouldDescendDirectory(root liveTokenRateRoot, path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return root.Tool != "trae" || !strings.HasSuffix(base, ".artifacts")
}

func (sampler *liveTokenRateSampler) recordWatchBatch(batch liveTokenRateWatchBatch) {
	sampler.watchMu.Lock()
	defer sampler.watchMu.Unlock()
	if !batch.Complete {
		sampler.watchIncomplete = true
	}
	if sampler.watchPending == nil {
		sampler.watchPending = map[string]struct{}{}
	}
	for _, rawPath := range batch.Paths {
		path := strings.TrimSpace(rawPath)
		if path == "" {
			continue
		}
		if _, pending := sampler.watchPending[path]; pending {
			continue
		}
		if len(sampler.watchPending) >= liveTokenRateMaxFiles*4 {
			sampler.watchOverflow = true
			break
		}
		sampler.watchPending[path] = struct{}{}
	}
}

func (sampler *liveTokenRateSampler) consumeWatchPathsLocked(now time.Time) {
	pending, incomplete, overflow := sampler.takeWatchState()
	if incomplete {
		sampler.directories = map[string]liveTokenRateTrackedDirectory{}
		sampler.lastDiscover = time.Time{}
		sampler.markLimitedLocked(now, liveTokenRateUnavailableWatchIncomplete)
	}
	if overflow {
		sampler.markLimitedLocked(now, liveTokenRateUnavailableWatchPendingCapacity)
	}
	for rawPath := range pending {
		path := canonicalLiveTokenRatePath(rawPath)
		tool, ok := sampler.liveTokenRateToolForPathLocked(path)
		if !ok || !liveTokenRateShouldTrackJSONL(path) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			delete(sampler.files, path)
			continue
		}
		if _, tracked := sampler.files[path]; tracked {
			continue
		}
		if len(sampler.files) >= liveTokenRateMaxFiles {
			sampler.markLimitedLocked(now, liveTokenRateUnavailableFileCapacity)
			continue
		}
		sampler.files[path] = sampler.rebaselineFile(path, tool, info, now)
	}
}

func (sampler *liveTokenRateSampler) setWatchCoverage(complete bool) {
	sampler.watchMu.Lock()
	sampler.watchCoverage = complete
	sampler.watchMu.Unlock()
}

func (sampler *liveTokenRateSampler) hasCompleteWatchCoverage() bool {
	sampler.watchMu.Lock()
	defer sampler.watchMu.Unlock()
	return sampler.watchCoverage && !sampler.watchIncomplete && !sampler.watchOverflow
}

func (sampler *liveTokenRateSampler) takeWatchState() (map[string]struct{}, bool, bool) {
	sampler.watchMu.Lock()
	defer sampler.watchMu.Unlock()
	pending := sampler.watchPending
	incomplete := sampler.watchIncomplete
	overflow := sampler.watchOverflow
	sampler.watchPending = map[string]struct{}{}
	sampler.watchIncomplete = false
	sampler.watchOverflow = false
	return pending, incomplete, overflow
}

func (sampler *liveTokenRateSampler) liveTokenRateToolForPathLocked(path string) (string, bool) {
	bestTool := ""
	bestRootLength := -1
	for _, root := range sampler.roots {
		relative, err := filepath.Rel(root.Path, path)
		if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		if root.Tool == "trae" {
			blocked := false
			for _, part := range strings.Split(relative, string(filepath.Separator)) {
				if strings.HasSuffix(strings.ToLower(part), ".artifacts") {
					blocked = true
					break
				}
			}
			if blocked {
				continue
			}
		}
		if len(root.Path) > bestRootLength {
			bestTool = root.Tool
			bestRootLength = len(root.Path)
		}
	}
	return bestTool, bestTool != ""
}

func (sampler *liveTokenRateSampler) deleteDirectoryTreeLocked(tool, path string) {
	key := tool + "\x00" + path
	tracked, ok := sampler.directories[key]
	if !ok {
		return
	}
	delete(sampler.directories, key)
	for _, child := range tracked.ChildDirs {
		sampler.deleteDirectoryTreeLocked(tool, child)
	}
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
			if identity == "" || usage == nil {
				continue
			}
			copy := *usage
			copy.order = clonedOrder.PushBack(identity)
			cloned[identity] = &copy
		}
	}
	for identity, usage := range source {
		if usage == nil || cloned[identity] != nil {
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
	if tracked.MessageUsage == nil {
		tracked.MessageUsage = map[string]*liveTokenRateMessageUsage{}
	}
	if tracked.MessageOrder == nil {
		tracked.MessageOrder = list.New()
	}
	if previous := tracked.MessageUsage[identity]; previous != nil {
		previous.Output = max(previous.Output, output)
		previous.LastSeen = now
		tracked.MessageOrder.MoveToBack(previous.order)
		return
	}
	liveTokenRatePruneMessages(tracked, now)
	for len(tracked.MessageUsage) >= liveTokenRateMaxMessages {
		liveTokenRateForgetOldestMessage(tracked)
	}
	usage := &liveTokenRateMessageUsage{Output: max(int64(0), output), LastSeen: now}
	usage.order = tracked.MessageOrder.PushBack(identity)
	tracked.MessageUsage[identity] = usage
}

func liveTokenRatePruneMessages(tracked *liveTokenRateTrackedFile, now time.Time) {
	if tracked == nil || len(tracked.MessageUsage) == 0 || tracked.MessageOrder == nil {
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
	}
}

func liveTokenRateForgetOldestMessage(tracked *liveTokenRateTrackedFile) {
	if tracked == nil || tracked.MessageOrder == nil {
		return
	}
	element := tracked.MessageOrder.Front()
	if element == nil {
		return
	}
	identity, _ := element.Value.(string)
	tracked.MessageOrder.Remove(element)
	delete(tracked.MessageUsage, identity)
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
		Configured:    len(sampler.roots) > 0,
		Initialized:   sampler.initialized,
		LimitedUntil:  sampler.limitedUntil,
		LimitedReason: sampler.limitedReason,
		LatestSignal:  sampler.latestSignal,
		LatestEvent:   sampler.latestEvent,
		Buckets:       append([]liveTokenRateEvent(nil), sampler.buckets...),
		Projects:      liveTokenRateProjectsForBuckets(sampler.buckets, sampler.sessionProjects),
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
	sampler.publishedMu.RUnlock()
	tokens, activeSessions, projectFacts := liveTokenRateWindowBreakdown(published.Buckets, published.Projects, now, liveTokenRateWindow, liveTokenRateFutureSkew)
	sample := liveTokenRateSampleFromFacts(liveTokenRateFacts{
		Configured:        published.Configured,
		Initialized:       published.Initialized,
		Limited:           published.LimitedUntil.After(now),
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

func liveTokenRateProjectSamples(projects map[string]liveTokenRateProjectFacts, window time.Duration) []LiveTokenRateProjectSample {
	out := make([]LiveTokenRateProjectSample, 0, len(projects))
	for project, facts := range projects {
		if facts.TokensInWindow <= 0 {
			continue
		}
		out = append(out, LiveTokenRateProjectSample{
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
