package main

import (
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultConfigUsesLowerEnergyRefreshCadence(t *testing.T) {
	cfg := defaultConfig()
	if cfg.RefreshInterval != 5*time.Minute {
		t.Fatalf("expected default refresh interval 5m, got %s", cfg.RefreshInterval)
	}
	if cfg.TranscriptCacheTTL != 60*time.Second {
		t.Fatalf("expected default transcript cache ttl 60s, got %s", cfg.TranscriptCacheTTL)
	}
	if !selectableRefreshInterval(30*time.Second) || !selectableRefreshInterval(5*time.Minute) || !selectableRefreshInterval(0) {
		t.Fatalf("expected 30s, 5m, and paused refresh intervals to be selectable")
	}
	if selectableRefreshInterval(5*time.Second) || selectableRefreshInterval(15*time.Second) {
		t.Fatalf("expected refresh intervals below 30s to be unavailable")
	}
}

func TestNormalizeRefreshIntervalFloorsNonZeroValuesAtThirtySeconds(t *testing.T) {
	if got := normalizeRefreshInterval(5 * time.Second); got != 30*time.Second {
		t.Fatalf("expected 5s refresh to normalize to 30s, got %s", got)
	}
	if got := normalizeRefreshInterval(0); got != 0 {
		t.Fatalf("expected paused refresh to remain 0, got %s", got)
	}
	if got := normalizeRefreshInterval(2 * time.Minute); got != 2*time.Minute {
		t.Fatalf("expected 2m refresh to remain 2m, got %s", got)
	}
}

func TestObserverSnapshotConfigUsesRefreshIntervalAndDiscoveredRoots(t *testing.T) {
	observer := newObserver(Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           48 * time.Hour,
		TranscriptCacheTTL: 30 * time.Second,
		RefreshInterval:    12 * time.Second,
		HistoryFile:        "/cfg/history.jsonl",
		ClaudeRoots:        []string{"/cfg/.claude"},
		CodexRoots:         []string{"/cfg/.codex"},
		TraeRoots:          []string{"/cfg/.trae/cli"},
	})

	claudeRoots := []string{"/live/.claude"}
	codexRoots := []string{"/live/.codex"}
	traeRoots := []string{"/live/.trae/cli"}
	got := observer.snapshotConfig(claudeRoots, codexRoots, traeRoots)

	if got.ProcessRefreshTarget != 12 {
		t.Fatalf("expected process refresh target 12, got %d", got.ProcessRefreshTarget)
	}
	if got.HistoryFile != "/cfg/history.jsonl" {
		t.Fatalf("expected history file to be preserved, got %q", got.HistoryFile)
	}
	if !slices.Equal(got.ClaudeRoots, claudeRoots) {
		t.Fatalf("expected discovered claude roots %v, got %v", claudeRoots, got.ClaudeRoots)
	}
	if !slices.Equal(got.CodexRoots, codexRoots) {
		t.Fatalf("expected discovered codex roots %v, got %v", codexRoots, got.CodexRoots)
	}
	if !slices.Equal(got.TraeRoots, traeRoots) {
		t.Fatalf("expected discovered trae roots %v, got %v", traeRoots, got.TraeRoots)
	}

	claudeRoots[0] = "/mutated/.claude"
	codexRoots[0] = "/mutated/.codex"
	traeRoots[0] = "/mutated/.trae/cli"
	if got.ClaudeRoots[0] != "/live/.claude" {
		t.Fatalf("expected snapshot claude roots to be copied, got %v", got.ClaudeRoots)
	}
	if got.CodexRoots[0] != "/live/.codex" {
		t.Fatalf("expected snapshot codex roots to be copied, got %v", got.CodexRoots)
	}
	if got.TraeRoots[0] != "/live/.trae/cli" {
		t.Fatalf("expected snapshot trae roots to be copied, got %v", got.TraeRoots)
	}
}

func TestTranscriptScanSkipsUnchangedFileContent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	firstLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-1","cwd":"/tmp/agentload"}}` + "\n"
	if err := os.WriteFile(path, []byte(firstLine), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
	})
	candidate := TranscriptFile{Tool: "codex", Path: path}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat transcript: %v", err)
	}

	var reads atomic.Int32
	original := parseTranscriptFileFunc
	parseTranscriptFileFunc = func(file TranscriptFile) (*SessionTrace, error) {
		reads.Add(1)
		return original(file)
	}
	t.Cleanup(func() { parseTranscriptFileFunc = original })

	first := observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if reads.Load() != 1 {
		t.Fatalf("expected first scan to parse once, got %d", reads.Load())
	}
	if first.ParsedFiles != 1 {
		t.Fatalf("expected first scan to parse one file, got %+v", first)
	}

	secondLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-2","cwd":"/tmp/agentload"}}` + "\n"
	if len(secondLine) != len(firstLine) {
		t.Fatalf("test fixture lines should keep equal size")
	}
	if err := os.WriteFile(path, []byte(secondLine), 0o644); err != nil {
		t.Fatalf("rewrite transcript content: %v", err)
	}
	if rewritten, err := os.Stat(path); err != nil {
		t.Fatalf("stat rewritten transcript: %v", err)
	} else if rewritten.Size() != info.Size() {
		t.Fatalf("expected rewritten transcript size %d, got %d", info.Size(), rewritten.Size())
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatalf("restore transcript mtime: %v", err)
	}

	second := observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if reads.Load() != 1 {
		t.Fatalf("expected unchanged mtime/size scan to reuse cached parse without rereading content, got %d parses", reads.Load())
	}
	if second.ParsedFiles != 1 {
		t.Fatalf("expected second scan to reuse parsed trace, got %+v", second)
	}
}

func TestTranscriptScanUsesAppendParserForAppendOnlyGrowth(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	firstLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-1","cwd":"/tmp/agentload"}}` + "\n"
	if err := os.WriteFile(path, []byte(firstLine), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
	})
	candidate := TranscriptFile{Tool: "codex", Path: path}

	var fullReads atomic.Int32
	var appendReads atomic.Int32
	originalFull := parseTranscriptFileFunc
	originalAppend := parseTranscriptFileAppendFunc
	parseTranscriptFileFunc = func(file TranscriptFile) (*SessionTrace, error) {
		fullReads.Add(1)
		return originalFull(file)
	}
	parseTranscriptFileAppendFunc = func(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
		appendReads.Add(1)
		if offset != int64(len(firstLine)) {
			t.Fatalf("expected append offset %d, got %d", len(firstLine), offset)
		}
		return originalAppend(file, base, offset)
	}
	t.Cleanup(func() {
		parseTranscriptFileFunc = originalFull
		parseTranscriptFileAppendFunc = originalAppend
	})

	first := observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 1 || appendReads.Load() != 0 {
		t.Fatalf("expected first scan to use one full parse and no append parse, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}
	if first.ParsedFiles != 1 {
		t.Fatalf("expected first scan to parse one file, got %+v", first)
	}

	secondLine := `{"timestamp":"2026-06-28T12:05:00Z","type":"response_item"}` + "\n"
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open transcript append: %v", err)
	}
	if _, err := f.WriteString(secondLine); err != nil {
		_ = f.Close()
		t.Fatalf("append transcript: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close transcript: %v", err)
	}

	second := observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 1 || appendReads.Load() != 1 {
		t.Fatalf("expected second scan to append parse only, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}
	trace := second.Traces[path]
	if trace == nil {
		t.Fatalf("expected appended trace in second scan, got %+v", second)
	}
	if trace.SessionID != "session-1" {
		t.Fatalf("expected append parse to preserve base session id, got %q", trace.SessionID)
	}
	if trace.Project != "agentload" {
		t.Fatalf("expected append parse to preserve base project, got %q", trace.Project)
	}
	if len(trace.EventTimes) != 2 {
		t.Fatalf("expected base and appended event times, got %d", len(trace.EventTimes))
	}
	if got := trace.LastEvent.Format(time.RFC3339); got != "2026-06-28T12:05:00Z" {
		t.Fatalf("expected last event from appended line, got %s", got)
	}
}

func TestTranscriptScanFallsBackWhenCachedFileEndedWithoutNewline(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	firstLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-1","cwd":"/tmp/agentload"}}`
	if err := os.WriteFile(path, []byte(firstLine), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
	})
	candidate := TranscriptFile{Tool: "codex", Path: path}

	var fullReads atomic.Int32
	var appendReads atomic.Int32
	originalFull := parseTranscriptFileFunc
	originalAppend := parseTranscriptFileAppendFunc
	parseTranscriptFileFunc = func(file TranscriptFile) (*SessionTrace, error) {
		fullReads.Add(1)
		return originalFull(file)
	}
	parseTranscriptFileAppendFunc = func(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
		appendReads.Add(1)
		return originalAppend(file, base, offset)
	}
	t.Cleanup(func() {
		parseTranscriptFileFunc = originalFull
		parseTranscriptFileAppendFunc = originalAppend
	})

	observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 1 || appendReads.Load() != 0 {
		t.Fatalf("expected first scan to use full parse only, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}

	if err := os.WriteFile(path, []byte(firstLine+"\n"+`{"timestamp":"2026-06-28T12:05:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite grown transcript: %v", err)
	}
	observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 2 || appendReads.Load() != 0 {
		t.Fatalf("expected second scan to fall back to full parse, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}
}

func TestTranscriptScanKeepsCodexLLaneFilesOnFullParse(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".codex", ".codexl", "asagent", "lane-1", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir lane events: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"thread_id":"lane-1"}`+"\n"), 0o644); err != nil {
		t.Fatalf("write lane events: %v", err)
	}

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
	})
	candidate := TranscriptFile{Tool: "codex", Path: path}

	var fullReads atomic.Int32
	var appendReads atomic.Int32
	originalFull := parseTranscriptFileFunc
	originalAppend := parseTranscriptFileAppendFunc
	parseTranscriptFileFunc = func(file TranscriptFile) (*SessionTrace, error) {
		fullReads.Add(1)
		return originalFull(file)
	}
	parseTranscriptFileAppendFunc = func(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
		appendReads.Add(1)
		return originalAppend(file, base, offset)
	}
	t.Cleanup(func() {
		parseTranscriptFileFunc = originalFull
		parseTranscriptFileAppendFunc = originalAppend
	})

	observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if err := os.WriteFile(path, []byte(`{"thread_id":"lane-1"}`+"\n"+`{"event":"still-running"}`+"\n"), 0o644); err != nil {
		t.Fatalf("grow lane events: %v", err)
	}
	observer.scanTranscripts(nil, nil, nil, []TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 2 || appendReads.Load() != 0 {
		t.Fatalf("expected codexL lane events to stay on full parse, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}
}

func TestForegroundTranscriptScanDefersOlderNonPriorityFiles(t *testing.T) {
	tmp := t.TempDir()
	recentPath := filepath.Join(tmp, ".codex", "sessions", "recent.jsonl")
	oldPath := filepath.Join(tmp, ".codex", "sessions", "old.jsonl")
	priorityPath := filepath.Join(tmp, ".codex", "sessions", "priority.jsonl")
	for _, path := range []string{recentPath, oldPath, priorityPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir transcript dir: %v", err)
		}
	}
	if err := os.WriteFile(recentPath, []byte(`{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"recent","cwd":"/tmp/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write recent transcript: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte(`{"timestamp":"2026-06-28T09:00:00Z","payload":{"id":"old","cwd":"/tmp/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write old transcript: %v", err)
	}
	if err := os.WriteFile(priorityPath, []byte(`{"timestamp":"2026-06-28T09:10:00Z","payload":{"id":"priority","cwd":"/tmp/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write priority transcript: %v", err)
	}
	oldTime := time.Date(2026, 6, 28, 9, 30, 0, 0, time.UTC)
	recentTime := time.Date(2026, 6, 28, 12, 10, 0, 0, time.UTC)
	if err := os.Chtimes(oldPath, oldTime, oldTime); err != nil {
		t.Fatalf("set old mtime: %v", err)
	}
	if err := os.Chtimes(priorityPath, oldTime, oldTime); err != nil {
		t.Fatalf("set priority mtime: %v", err)
	}
	if err := os.Chtimes(recentPath, recentTime, recentTime); err != nil {
		t.Fatalf("set recent mtime: %v", err)
	}

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
	})
	var reads atomic.Int32
	var tailReads atomic.Int32
	original := parseTranscriptFileFunc
	originalTail := parseTranscriptFileTailFunc
	parseTranscriptFileFunc = func(file TranscriptFile) (*SessionTrace, error) {
		reads.Add(1)
		return original(file)
	}
	parseTranscriptFileTailFunc = func(file TranscriptFile) (*SessionTrace, error) {
		tailReads.Add(1)
		return originalTail(file)
	}
	t.Cleanup(func() {
		parseTranscriptFileFunc = original
		parseTranscriptFileTailFunc = originalTail
	})

	data := observer.scanTranscriptsWithOptions(nil, []string{filepath.Join(tmp, ".codex")}, nil, []TranscriptFile{{Tool: "codex", Path: priorityPath}}, transcriptScanOptions{
		HistoryCutoff:      time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
		ForegroundCutoff:   time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC),
		HistoryLookback:    24 * time.Hour,
		ForegroundLookback: time.Hour,
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
	})

	if data.ScannedFiles != 3 {
		t.Fatalf("expected three candidates, got %+v", data)
	}
	if data.DeferredFiles != 1 {
		t.Fatalf("expected one older non-priority file to be deferred, got %+v", data)
	}
	if reads.Load() != 1 || tailReads.Load() != 1 || data.ParsedFiles != 2 || data.TailParsedFiles != 1 {
		t.Fatalf("expected priority to full-parse and recent to tail-parse, full=%d tail=%d data=%+v", reads.Load(), tailReads.Load(), data)
	}
	if data.Traces[oldPath] != nil {
		t.Fatalf("expected old non-priority trace to be absent from foreground data")
	}
	if data.Traces[priorityPath] == nil || data.Traces[recentPath] == nil {
		t.Fatalf("expected priority and recent traces, got %+v", data.Traces)
	}
	if data.ForegroundScanLookbackSeconds != 3600 || data.ConfiguredHistoryLookbackSeconds != 86400 {
		t.Fatalf("expected scan window metadata, got %+v", data)
	}
}

func TestForegroundTranscriptScanCanDeferHistoryWalk(t *testing.T) {
	tmp := t.TempDir()
	recentPath := filepath.Join(tmp, ".codex", "sessions", "recent.jsonl")
	oldPath := filepath.Join(tmp, ".codex", "sessions", "old.jsonl")
	priorityPath := filepath.Join(tmp, ".codex", "sessions", "priority.jsonl")
	for _, path := range []string{recentPath, oldPath, priorityPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir transcript dir: %v", err)
		}
	}
	if err := os.WriteFile(recentPath, []byte(`{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"recent","cwd":"/tmp/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write recent transcript: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte(`{"timestamp":"2026-06-28T09:00:00Z","payload":{"id":"old","cwd":"/tmp/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write old transcript: %v", err)
	}
	if err := os.WriteFile(priorityPath, []byte(`{"timestamp":"2026-06-28T09:10:00Z","payload":{"id":"priority","cwd":"/tmp/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write priority transcript: %v", err)
	}
	oldTime := time.Date(2026, 6, 28, 9, 30, 0, 0, time.UTC)
	recentTime := time.Date(2026, 6, 28, 12, 10, 0, 0, time.UTC)
	for _, path := range []string{oldPath, priorityPath} {
		if err := os.Chtimes(path, oldTime, oldTime); err != nil {
			t.Fatalf("set old mtime: %v", err)
		}
	}
	if err := os.Chtimes(recentPath, recentTime, recentTime); err != nil {
		t.Fatalf("set recent mtime: %v", err)
	}

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
	})
	data := observer.scanTranscriptsWithOptions(nil, []string{filepath.Join(tmp, ".codex")}, nil, []TranscriptFile{{Tool: "codex", Path: priorityPath}}, transcriptScanOptions{
		HistoryCutoff:      time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
		ForegroundCutoff:   time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC),
		HistoryLookback:    24 * time.Hour,
		ForegroundLookback: time.Hour,
		DeferHistoryWalk:   true,
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
	})

	if data.ScannedFiles != 2 {
		t.Fatalf("expected only recent and priority candidates when history walk is deferred, got %+v", data)
	}
	if data.DeferredFiles != 0 || !data.HistoricalScanDeferred {
		t.Fatalf("expected historical directory enumeration to be marked deferred without per-file count, got %+v", data)
	}
	if data.Traces[oldPath] != nil || data.Traces[priorityPath] == nil || data.Traces[recentPath] == nil {
		t.Fatalf("expected priority and recent traces only, got %+v", data.Traces)
	}
}

func TestForegroundTranscriptScanDefersFreshMTimeWhenTailIsOlder(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, ".codex", "sessions", "old-tail.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	body := `{"timestamp":"2026-06-28T09:00:00Z","payload":{"id":"old-tail","cwd":"/tmp/agentload"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	freshMTime := time.Date(2026, 6, 28, 12, 10, 0, 0, time.UTC)
	if err := os.Chtimes(path, freshMTime, freshMTime); err != nil {
		t.Fatalf("set transcript mtime: %v", err)
	}

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    24 * time.Hour,
	})
	var reads atomic.Int32
	original := parseTranscriptFileFunc
	parseTranscriptFileFunc = func(file TranscriptFile) (*SessionTrace, error) {
		reads.Add(1)
		return original(file)
	}
	t.Cleanup(func() { parseTranscriptFileFunc = original })

	data := observer.scanTranscriptsWithOptions(nil, []string{filepath.Join(tmp, ".codex")}, nil, nil, transcriptScanOptions{
		HistoryCutoff:      time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
		ForegroundCutoff:   time.Date(2026, 6, 28, 11, 0, 0, 0, time.UTC),
		HistoryLookback:    24 * time.Hour,
		ForegroundLookback: time.Hour,
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
	})

	if data.ScannedFiles != 1 || data.DeferredFiles != 1 {
		t.Fatalf("expected fresh-mtime old-tail candidate to be deferred, got %+v", data)
	}
	if reads.Load() != 0 || data.ParsedFiles != 0 {
		t.Fatalf("expected deferred candidate not to parse, reads=%d data=%+v", reads.Load(), data)
	}
}
