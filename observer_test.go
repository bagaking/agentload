package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	cfgHistory := filepath.Join("fixtures", "config", "history.jsonl")
	cfgClaudeRoot := filepath.Join("fixtures", "config", ".claude")
	cfgCodexRoot := filepath.Join("fixtures", "config", ".codex")
	cfgTraeRoot := filepath.Join("fixtures", "config", ".trae", "cli")
	observer := newObserver(Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           48 * time.Hour,
		TranscriptCacheTTL: 30 * time.Second,
		RefreshInterval:    12 * time.Second,
		HistoryFile:        cfgHistory,
		ClaudeRoots:        []string{cfgClaudeRoot},
		CodexRoots:         []string{cfgCodexRoot},
		TraeRoots:          []string{cfgTraeRoot},
	})

	claudeRoots := []string{filepath.Join("fixtures", "live", ".claude")}
	codexRoots := []string{filepath.Join("fixtures", "live", ".codex")}
	traeRoots := []string{filepath.Join("fixtures", "live", ".trae", "cli")}
	got := observer.snapshotConfig(map[string][]string{
		"claude": claudeRoots,
		"codex":  codexRoots,
		"trae":   traeRoots,
	})

	if got.ProcessRefreshTarget != 12 {
		t.Fatalf("expected process refresh target 12, got %d", got.ProcessRefreshTarget)
	}
	if got.HistoryFile != cfgHistory {
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

	claudeRoots[0] = filepath.Join("fixtures", "mutated", ".claude")
	codexRoots[0] = filepath.Join("fixtures", "mutated", ".codex")
	traeRoots[0] = filepath.Join("fixtures", "mutated", ".trae", "cli")
	if got.ClaudeRoots[0] != filepath.Join("fixtures", "live", ".claude") {
		t.Fatalf("expected snapshot claude roots to be copied, got %v", got.ClaudeRoots)
	}
	if got.CodexRoots[0] != filepath.Join("fixtures", "live", ".codex") {
		t.Fatalf("expected snapshot codex roots to be copied, got %v", got.CodexRoots)
	}
	if got.TraeRoots[0] != filepath.Join("fixtures", "live", ".trae", "cli") {
		t.Fatalf("expected snapshot trae roots to be copied, got %v", got.TraeRoots)
	}
}

func TestSnapshotNotesDescribeDeferredHistoricalParsing(t *testing.T) {
	notes := buildSnapshotNotes(Snapshot{
		TranscriptStats: TranscriptStats{
			HistoricalScanDeferred: true,
		},
	}, nil, nil)

	want := "Full historical transcript parsing was deferred from the foreground snapshot; live process files and foreground-window transcripts are still included."
	if !slices.Contains(notes, want) {
		t.Fatalf("expected snapshot notes to include %q, got %#v", want, notes)
	}
	for _, note := range notes {
		if strings.Contains(note, "directory enumeration") {
			t.Fatalf("expected snapshot notes not to claim directory enumeration was deferred, got %#v", notes)
		}
	}
}

func TestObserverSnapshotKeepsDetectedToolPIDMetricsWithoutSessions(t *testing.T) {
	originalDiscover := discoverLiveProcessesFunc
	discoverLiveProcessesFunc = func(context.Context, *codingAgentRegistry) ([]LiveProcess, []string) {
		return []LiveProcess{
			{PID: 11, Tool: "opencode", Command: "opencode run"},
			{PID: 12, Tool: "gemini", Command: "gemini --prompt hello"},
		}, nil
	}
	t.Cleanup(func() {
		discoverLiveProcessesFunc = originalDiscover
	})

	observer := newObserver(Config{
		IdleGap:     90 * time.Second,
		MinInterval: 15 * time.Second,
		Lookback:    time.Hour,
	})
	got := observer.Snapshot(context.Background())

	if got.Current.PIDConcurrency != 2 || got.Current.SessionConcurrency != 0 {
		t.Fatalf("unexpected aggregate metrics: %+v", got.Current)
	}
	if got.CurrentByTool["opencode"].PIDConcurrency != 1 {
		t.Fatalf("expected opencode pid metrics, got %+v", got.CurrentByTool["opencode"])
	}
	if got.CurrentByTool["gemini"].PIDConcurrency != 1 {
		t.Fatalf("expected gemini pid metrics, got %+v", got.CurrentByTool["gemini"])
	}
	if len(got.LiveProcesses) != 2 || got.LiveProcesses[0].Tool != "gemini" || got.LiveProcesses[1].Tool != "opencode" {
		t.Fatalf("expected sorted live processes for gemini and opencode, got %+v", got.LiveProcesses)
	}
}

func TestTranscriptScanSkipsUnchangedFileContent(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	firstLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-1","cwd":"workspace/agentload"}}` + "\n"
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
	var tailReads atomic.Int32
	installTranscriptParserProbe(t, observer, "codex", &reads, &tailReads, nil, nil)

	first := observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if reads.Load() != 1 {
		t.Fatalf("expected first scan to parse once, got %d", reads.Load())
	}
	if first.ParsedFiles != 1 {
		t.Fatalf("expected first scan to parse one file, got %+v", first)
	}

	secondLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-2","cwd":"workspace/agentload"}}` + "\n"
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

	second := observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if reads.Load() != 1 {
		t.Fatalf("expected unchanged mtime/size scan to reuse cached parse without rereading content, got %d parses", reads.Load())
	}
	if tailReads.Load() != 0 {
		t.Fatalf("expected unchanged mtime/size scan to avoid tail parsing, got %d tail parses", tailReads.Load())
	}
	if second.ParsedFiles != 1 {
		t.Fatalf("expected second scan to reuse parsed trace, got %+v", second)
	}
}

func TestTranscriptScanUsesAppendParserForAppendOnlyGrowth(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "session.jsonl")
	firstLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-1","cwd":"workspace/agentload"}}` + "\n"
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
	var appendOffset atomic.Int64
	installTranscriptParserProbe(t, observer, "codex", &fullReads, nil, &appendReads, appendOffset.Store)

	first := observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
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

	second := observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 1 || appendReads.Load() != 1 {
		t.Fatalf("expected second scan to append parse only, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}
	if appendOffset.Load() != int64(len(firstLine)) {
		t.Fatalf("expected append offset %d, got %d", len(firstLine), appendOffset.Load())
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
	firstLine := `{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"session-1","cwd":"workspace/agentload"}}`
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
	installTranscriptParserProbe(t, observer, "codex", &fullReads, nil, &appendReads, nil)

	observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 1 || appendReads.Load() != 0 {
		t.Fatalf("expected first scan to use full parse only, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}

	if err := os.WriteFile(path, []byte(firstLine+"\n"+`{"timestamp":"2026-06-28T12:05:00Z"}`+"\n"), 0o644); err != nil {
		t.Fatalf("rewrite grown transcript: %v", err)
	}
	observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
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
	installTranscriptParserProbe(t, observer, "codex", &fullReads, nil, &appendReads, nil)

	observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if err := os.WriteFile(path, []byte(`{"thread_id":"lane-1"}`+"\n"+`{"event":"still-running"}`+"\n"), 0o644); err != nil {
		t.Fatalf("grow lane events: %v", err)
	}
	observer.scanTranscripts([]TranscriptFile{candidate}, time.Time{}, 90*time.Second, 15*time.Second)
	if fullReads.Load() != 2 || appendReads.Load() != 0 {
		t.Fatalf("expected codexL lane events to stay on full parse, got full=%d append=%d", fullReads.Load(), appendReads.Load())
	}
}

func TestForegroundTranscriptScanDefersOlderNonPriorityFiles(t *testing.T) {
	tmp := t.TempDir()
	codexRoot := filepath.Join(tmp, ".codex")
	sessionsDir := filepath.Join(codexRoot, "sessions", "2026", "06", "28")
	recentPath := filepath.Join(sessionsDir, "recent.jsonl")
	oldPath := filepath.Join(sessionsDir, "old.jsonl")
	priorityPath := filepath.Join(sessionsDir, "priority.jsonl")
	for _, path := range []string{recentPath, oldPath, priorityPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir transcript dir: %v", err)
		}
	}
	if err := os.WriteFile(recentPath, []byte(`{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"recent","cwd":"workspace/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write recent transcript: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte(`{"timestamp":"2026-06-28T09:00:00Z","payload":{"id":"old","cwd":"workspace/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write old transcript: %v", err)
	}
	if err := os.WriteFile(priorityPath, []byte(`{"timestamp":"2026-06-28T09:10:00Z","payload":{"id":"priority","cwd":"workspace/agentload"}}`+"\n"), 0o644); err != nil {
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
		CodexRoots:  []string{codexRoot},
	})
	var reads atomic.Int32
	var tailReads atomic.Int32
	installTranscriptParserProbe(t, observer, "codex", &reads, &tailReads, nil, nil)

	data := observer.scanTranscriptsWithOptions(context.Background(), []TranscriptFile{{Tool: "codex", Path: priorityPath}}, transcriptScanOptions{
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
	codexRoot := filepath.Join(tmp, ".codex")
	sessionsDir := filepath.Join(codexRoot, "sessions", "2026", "06", "28")
	recentPath := filepath.Join(sessionsDir, "recent.jsonl")
	oldPath := filepath.Join(sessionsDir, "old.jsonl")
	priorityPath := filepath.Join(sessionsDir, "priority.jsonl")
	for _, path := range []string{recentPath, oldPath, priorityPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir transcript dir: %v", err)
		}
	}
	if err := os.WriteFile(recentPath, []byte(`{"timestamp":"2026-06-28T12:00:00Z","payload":{"id":"recent","cwd":"workspace/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write recent transcript: %v", err)
	}
	if err := os.WriteFile(oldPath, []byte(`{"timestamp":"2026-06-28T09:00:00Z","payload":{"id":"old","cwd":"workspace/agentload"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write old transcript: %v", err)
	}
	if err := os.WriteFile(priorityPath, []byte(`{"timestamp":"2026-06-28T09:10:00Z","payload":{"id":"priority","cwd":"workspace/agentload"}}`+"\n"), 0o644); err != nil {
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
		CodexRoots:  []string{codexRoot},
	})
	data := observer.scanTranscriptsWithOptions(context.Background(), []TranscriptFile{{Tool: "codex", Path: priorityPath}}, transcriptScanOptions{
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
		t.Fatalf("expected full historical parsing to be marked deferred without per-file count, got %+v", data)
	}
	if data.Traces[oldPath] != nil || data.Traces[priorityPath] == nil || data.Traces[recentPath] == nil {
		t.Fatalf("expected priority and recent traces only, got %+v", data.Traces)
	}
}

func TestForegroundTranscriptScanDefersFreshMTimeWhenTailIsOlder(t *testing.T) {
	tmp := t.TempDir()
	codexRoot := filepath.Join(tmp, ".codex")
	path := filepath.Join(codexRoot, "sessions", "2026", "06", "28", "old-tail.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	body := `{"timestamp":"2026-06-28T09:00:00Z","payload":{"id":"old-tail","cwd":"workspace/agentload"}}` + "\n"
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
		CodexRoots:  []string{codexRoot},
	})
	var reads atomic.Int32
	installTranscriptParserProbe(t, observer, "codex", &reads, nil, nil, nil)

	data := observer.scanTranscriptsWithOptions(context.Background(), nil, transcriptScanOptions{
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

type transcriptParserProbe struct {
	base        agentTranscriptParser
	fullReads   *atomic.Int32
	tailReads   *atomic.Int32
	appendReads *atomic.Int32
	checkOffset func(int64)
}

func (p transcriptParserProbe) Parse(file TranscriptFile) (*SessionTrace, error) {
	if p.fullReads != nil {
		p.fullReads.Add(1)
	}
	return p.base.Parse(file)
}

func (p transcriptParserProbe) ParseTail(file TranscriptFile) (*SessionTrace, error) {
	if p.tailReads != nil {
		p.tailReads.Add(1)
	}
	return p.base.ParseTail(file)
}

func (p transcriptParserProbe) ParseAppend(file TranscriptFile, base *SessionTrace, offset int64) (*SessionTrace, error) {
	if p.appendReads != nil {
		p.appendReads.Add(1)
	}
	if p.checkOffset != nil {
		p.checkOffset(offset)
	}
	return p.base.ParseAppend(file, base, offset)
}

func (p transcriptParserProbe) CanAppend(file TranscriptFile) bool {
	return p.base.CanAppend(file)
}

func installTranscriptParserProbe(t *testing.T, observer *Observer, agentID string, fullReads, tailReads, appendReads *atomic.Int32, checkOffset func(int64)) {
	t.Helper()
	observer.adapters.mu.Lock()
	defer observer.adapters.mu.Unlock()
	index, ok := observer.adapters.byID[agentID]
	if !ok {
		t.Fatalf("missing adapter %s", agentID)
	}
	base := observer.adapters.adapters[index].Capabilities.Transcript
	if base == nil {
		t.Fatalf("missing transcript parser for %s", agentID)
	}
	observer.adapters.adapters[index].Capabilities.Transcript = transcriptParserProbe{
		base:        base,
		fullReads:   fullReads,
		tailReads:   tailReads,
		appendReads: appendReads,
		checkOffset: checkOffset,
	}
}
