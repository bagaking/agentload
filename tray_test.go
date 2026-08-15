package main

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fyne.io/systray"
)

func TestClampDuration(t *testing.T) {
	tests := []struct {
		name    string
		value   time.Duration
		floor   time.Duration
		ceiling time.Duration
		want    time.Duration
	}{
		{name: "below floor uses floor", value: 10 * time.Second, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 45 * time.Second},
		{name: "zero value uses floor", value: 0, floor: 90 * time.Second, ceiling: 5 * time.Minute, want: 90 * time.Second},
		{name: "in range passes through", value: 2 * time.Minute, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 2 * time.Minute},
		{name: "above ceiling uses ceiling", value: 30 * time.Minute, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 5 * time.Minute},
		{name: "default lookback tenth is capped", value: 7 * 24 * time.Hour / 10, floor: 45 * time.Second, ceiling: 5 * time.Minute, want: 5 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampDuration(tt.value, tt.floor, tt.ceiling); got != tt.want {
				t.Fatalf("clampDuration(%s, %s, %s) = %s, want %s", tt.value, tt.floor, tt.ceiling, got, tt.want)
			}
		})
	}
}

// readLifecycleEventNames returns the ordered event names recorded in a
// lifecycle log so shutdown ordering can be asserted from the durable record.
func readLifecycleEventNames(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lifecycle log %q: %v", path, err)
	}
	names := []string{}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event lifecycleEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode lifecycle line %q: %v", line, err)
		}
		names = append(names, event.Event)
	}
	return names
}

// newShutdownTestApp builds a trayApp with a real listener, HTTP server and
// lifecycle log so onExit exercises the actual cleanup path.
func newShutdownTestApp(t *testing.T) (*trayApp, *lifecycleLog) {
	t.Helper()
	cfg := Config{HistoryFile: filepath.Join(t.TempDir(), "history.jsonl")}
	lifecycle := newLifecycleLog(cfg.HistoryFile)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	observer := newObserver(cfg)
	app := &trayApp{
		cfg:           cfg,
		observer:      observer,
		logger:        log.New(io.Discard, "", 0),
		listener:      listener,
		liveTokenRate: newLiveTokenRateSampler(observer.adapters, observer.evidenceIndex),
		lifecycle:     lifecycle,
		stopCh:        make(chan struct{}),
		refreshCh:     make(chan struct{}, 1),
	}
	app.server = &http.Server{Handler: app.handler()}
	return app, lifecycle
}

func TestOnExitCompletesEveryCleanupStep(t *testing.T) {
	app, lifecycle := newShutdownTestApp(t)
	serveErr := make(chan error, 1)
	go func() { serveErr <- app.server.Serve(app.listener) }()
	addr := app.listener.Addr().String()

	startSystemResourceSampler(time.Hour)
	t.Cleanup(stopSystemResourceSampler)

	app.onExit()

	// Shutdown must reach the HTTP server, not stop at an earlier owner.
	select {
	case err := <-serveErr:
		if err != http.ErrServerClosed {
			t.Fatalf("expected ErrServerClosed from Serve, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("http server was never shut down by onExit")
	}
	if _, err := net.DialTimeout("tcp", addr, 500*time.Millisecond); err == nil {
		t.Fatal("expected the listener to be closed after onExit")
	}

	// Background owners must be released, and the record must show shutdown ran
	// to completion rather than stopping midway.
	if _, ok := latestBackgroundSystemResourceSample(); ok {
		t.Fatal("expected onExit to stop the system resource sampler")
	}
	select {
	case <-app.stopCh:
	default:
		t.Fatal("expected onExit to close stopCh")
	}
	names := readLifecycleEventNames(t, lifecycle.path)
	if len(names) < 2 || names[0] != "shutdown_begin" || names[len(names)-1] != "shutdown_complete" {
		t.Fatalf("expected shutdown_begin..shutdown_complete, got %v", names)
	}
}

func TestRunCleansUpWhenSystrayReturnsWithoutOnExit(t *testing.T) {
	app, lifecycle := newShutdownTestApp(t)
	originalRun := systrayRun
	systrayRun = func(func(), func()) {}
	t.Cleanup(func() { systrayRun = originalRun })

	if err := app.run(); err != nil {
		t.Fatalf("run returned an error: %v", err)
	}
	names := readLifecycleEventNames(t, lifecycle.path)
	beginCount, completeCount := 0, 0
	for _, name := range names {
		if name == "shutdown_begin" {
			beginCount++
		}
		if name == "shutdown_complete" {
			completeCount++
		}
	}
	if beginCount != 1 || completeCount != 1 {
		t.Fatalf("run did not close the post-native-loop shutdown path: events=%v", names)
	}
}

func TestOnExitStillShutsDownWhenAnEarlierStepPanics(t *testing.T) {
	app, lifecycle := newShutdownTestApp(t)
	serveErr := make(chan error, 1)
	go func() { serveErr <- app.server.Serve(app.listener) }()

	// Inject a panic at the evidence-index owner's Stop boundary. The remaining
	// steps, the HTTP shutdown and the shutdown_complete record must still run:
	// a delayed or skipped API teardown is exactly the failure being guarded.
	done := make(chan struct{})
	close(done)
	index := &transcriptEvidenceIndex{
		running: true,
		stop:    make(chan struct{}),
		done:    done,
		watcher: &panickingStopEvidenceWatcher{events: make(chan evidenceWatchBatch)},
	}
	app.observer = &Observer{evidenceIndex: index}

	app.onExit()

	select {
	case err := <-serveErr:
		if err != http.ErrServerClosed {
			t.Fatalf("expected ErrServerClosed from Serve, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a panicking cleanup step skipped the http server shutdown")
	}
	names := readLifecycleEventNames(t, lifecycle.path)
	if len(names) == 0 || names[len(names)-1] != "shutdown_complete" {
		t.Fatalf("expected shutdown_complete after a panicking cleanup step, got %v", names)
	}
	index.lifecycleMu.Lock()
	running := index.running
	index.lifecycleMu.Unlock()
	if running {
		t.Fatal("evidence-index cleanup did not run before shutdown completed")
	}
}

func TestOnExitIsIdempotent(t *testing.T) {
	app, _ := newShutdownTestApp(t)
	go func() { _ = app.server.Serve(app.listener) }()
	app.onExit()
	// A second exit (menu quit plus API quit) must not panic on the closed
	// stopCh or the already-stopped owners.
	app.onExit()
}

func TestOnExitCancelsAndJoinsInFlightRefresh(t *testing.T) {
	originalDiscover := discoverLiveProcessesFunc
	entered := make(chan struct{}, 1)
	discoverLiveProcessesFunc = func(ctx context.Context, _ *codingAgentRegistry) ([]snapshot.LiveProcess, []string) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done()
		return nil, nil
	}
	t.Cleanup(func() { discoverLiveProcessesFunc = originalDiscover })

	observer := newObserver(Config{})
	app := &trayApp{
		cfg:           Config{RefreshInterval: time.Hour, Lookback: time.Hour},
		observer:      observer,
		logger:        log.New(io.Discard, "", 0),
		liveTokenRate: newLiveTokenRateSampler(observer.adapters, observer.evidenceIndex),
		stopCh:        make(chan struct{}),
		refreshCh:     make(chan struct{}, 1),
	}
	refreshDone := app.registerLoopDone(true)
	go func() {
		defer close(refreshDone)
		app.refreshLoop()
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not reach the cancellable process discovery")
	}
	started := time.Now()
	app.onExit()
	if elapsed := time.Since(started); elapsed > trayShutdownTimeout+time.Second {
		t.Fatalf("onExit exceeded its bounded refresh shutdown, took %s", elapsed)
	}
	select {
	case <-refreshDone:
	default:
		t.Fatal("onExit returned while the refresh loop was still running")
	}
	if app.isRefreshing() {
		t.Fatal("refresh guard remained set after joined shutdown")
	}
}

func TestOnExitConcurrentCallsShareOneShutdown(t *testing.T) {
	app, lifecycle := newShutdownTestApp(t)
	go func() { _ = app.server.Serve(app.listener) }()
	const callers = 8
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			app.onExit()
		}()
	}
	wg.Wait()
	names := readLifecycleEventNames(t, lifecycle.path)
	beginCount, completeCount := 0, 0
	for _, name := range names {
		if name == "shutdown_begin" {
			beginCount++
		}
		if name == "shutdown_complete" {
			completeCount++
		}
	}
	if beginCount != 1 || completeCount != 1 {
		t.Fatalf("concurrent onExit recorded begin=%d complete=%d events=%v", beginCount, completeCount, names)
	}
}

func TestRunRefreshStepReleasesGuardsWhenRefreshPanics(t *testing.T) {
	app := &trayApp{
		cfg:       Config{RefreshInterval: time.Minute},
		logger:    log.New(io.Discard, "", 0),
		stopCh:    make(chan struct{}),
		refreshCh: make(chan struct{}, 1),
	}
	// A nil observer makes refreshOnce panic inside the contained step.
	slotID := app.refreshSlotID(time.Now())
	app.requestRefreshForSlot(slotID)
	app.runRefreshStep()

	// Both guards must be released, otherwise the UI reports a refresh that
	// never finishes and claimRefreshSlot rejects every later slot forever.
	if app.isRefreshing() {
		t.Fatal("expected refreshing to be cleared after a panicking refresh")
	}
	app.lastMu.RLock()
	activeSlot := app.activeSlot
	app.lastMu.RUnlock()
	if activeSlot != "" {
		t.Fatalf("expected activeSlot to be released, got %q", activeSlot)
	}

	// A later slot must still be claimable, proving refreshes can resume.
	nextSlot := app.refreshSlotIDForInterval(time.Now().Add(2*time.Minute), time.Minute)
	app.requestRefreshForSlot(nextSlot)
	if claimed := app.claimRefreshSlot(); claimed != nextSlot {
		t.Fatalf("expected to claim the next slot %q after a panicking refresh, got %q", nextSlot, claimed)
	}
}

func TestRememberSnapshotReleasesLastMuWhenMergePanics(t *testing.T) {
	app := &trayApp{
		logger:    log.New(io.Discard, "", 0),
		history:   localHistoryState{path: filepath.Join(t.TempDir(), "history.jsonl")},
		refreshCh: make(chan struct{}, 1),
	}
	// Inject a failing merge. The real merge helpers are all nil-safe today, so
	// the panic has to be injected to exercise the lock scope at all.
	merged := false
	app.mergeRecordedSampleFunc = func(snapshot.Snapshot, HistorySample, time.Time, error) snapshot.Snapshot {
		merged = true
		panic("merge failed")
	}

	func() {
		defer func() { _ = recover() }()
		app.rememberSnapshot(snapshot.Snapshot{GeneratedAt: time.Now().Format(time.RFC3339)})
	}()
	if !merged {
		t.Fatal("expected the injected merge to run under lastMu")
	}

	// lastMu must not be stranded: a stranded write lock deadlocks every
	// snapshot reader, the tray and the whole HTTP API.
	acquired := make(chan struct{})
	go func() {
		app.lastMu.Lock()
		app.lastMu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("lastMu was left locked after a panic inside rememberSnapshot")
	}
}

func TestHandleMenuClicksSurvivesPanickingClickAndStillQuits(t *testing.T) {
	originalQuit := systrayQuit
	quit := make(chan struct{}, 1)
	systrayQuit = func() { quit <- struct{}{} }
	t.Cleanup(func() { systrayQuit = originalQuit })

	app := &trayApp{
		logger:    log.New(io.Discard, "", 0),
		stopCh:    make(chan struct{}),
		refreshCh: make(chan struct{}, 1),
		// A nil lifecycle is fine; recordLifecycle tolerates it. The URL opener is
		// injected below so the first click reaches the real contained step and
		// panics deterministically.
		mOpenDashboard: &systray.MenuItem{ClickedCh: make(chan struct{}, 1)},
		mRefreshNow:    &systray.MenuItem{ClickedCh: make(chan struct{}, 1)},
		mQuit:          &systray.MenuItem{ClickedCh: make(chan struct{}, 1)},
	}
	opened := make(chan struct{}, 1)
	app.dashboardURL = "http://127.0.0.1:1/dashboard"
	app.openURLFunc = func(string) {
		opened <- struct{}{}
		panic("synthetic URL opener failure")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		app.handleMenuClicks()
	}()

	// The first click must actually panic inside the production URL-opening step.
	app.mOpenDashboard.ClickedCh <- struct{}{}
	select {
	case <-opened:
	case <-time.After(5 * time.Second):
		t.Fatal("dashboard click did not reach the injected URL opener")
	}
	// The loop must survive that panic and still serve the later Quit click.
	app.mQuit.ClickedCh <- struct{}{}

	select {
	case <-quit:
	case <-time.After(5 * time.Second):
		t.Fatal("expected the menu loop to reach the quit hand-off")
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected handleMenuClicks to return after quit")
	}
}

func TestSnapshotScanAborted(t *testing.T) {
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name     string
		ctx      context.Context
		snapshot snapshot.Snapshot
		want     bool
	}{
		{name: "live context and clean scan", ctx: context.Background(), snapshot: snapshot.Snapshot{}, want: false},
		{name: "cancelled context", ctx: cancelled, snapshot: snapshot.Snapshot{}, want: true},
		{
			name:     "scan aborted early marker",
			ctx:      context.Background(),
			snapshot: snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{Errors: []string{"transcript scan aborted early (3 files not parsed): context deadline exceeded"}}},
			want:     true,
		},
		{
			name:     "scan wait cancelled marker",
			ctx:      context.Background(),
			snapshot: snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{Errors: []string{"transcript scan wait cancelled: context canceled"}}},
			want:     true,
		},
		{
			name:     "ordinary transcript error is degraded evidence",
			ctx:      context.Background(),
			snapshot: snapshot.Snapshot{TranscriptStats: snapshot.TranscriptStats{Errors: []string{"session.jsonl: invalid JSON"}}},
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := snapshotScanAborted(tt.ctx, tt.snapshot); got != tt.want {
				t.Fatalf("snapshotScanAborted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRememberSnapshotRejectsIncompleteProcessEvidence(t *testing.T) {
	app := &trayApp{}
	got := app.rememberSnapshot(snapshot.Snapshot{
		GeneratedAt:  "current",
		ProcessStats: snapshot.ProcessObservationStats{Incomplete: true, LastKnown: true},
		LiveProcesses: []snapshot.LiveProcessSnapshot{{
			PID: 42,
		}},
	})
	if !got.ProcessStats.Incomplete || len(got.LiveProcesses) != 1 {
		t.Fatalf("expected incomplete snapshot to remain available to the caller, got %+v", got)
	}
	if _, ok := app.cachedSnapshot(); ok {
		t.Fatal("incomplete process evidence entered the snapshot cache")
	}
}

func TestStartLiveTokenRateStartsForIncompleteSnapshotWithoutReplacingMapping(t *testing.T) {
	sampler := newTestLiveTokenRateSampler(Config{})
	t.Cleanup(sampler.stopSampler)
	app := &trayApp{liveTokenRate: sampler}
	sampler.updateSnapshotProjects(map[string]string{"codex\x00known": "known-project"})

	app.startLiveTokenRate(snapshot.Snapshot{
		LiveTokenProjects: map[string]string{"codex\x00partial": "partial-project"},
	}, true)

	sampler.lifecycleMu.Lock()
	running := sampler.running
	sampler.lifecycleMu.Unlock()
	if !running {
		t.Fatal("incomplete snapshot prevented the live token sampler from starting")
	}
	sampler.pollMu.Lock()
	projects := cloneLiveTokenRateProjects(sampler.sessionProjects)
	sampler.pollMu.Unlock()
	if len(projects) != 1 || projects["codex\x00known"] != "known-project" {
		t.Fatalf("incomplete snapshot replaced the known project mapping: %+v", projects)
	}
	if _, ok := projects["codex\x00partial"]; ok {
		t.Fatalf("incomplete snapshot introduced a partial project mapping: %+v", projects)
	}
}

func TestFormatStatusBoxPayload(t *testing.T) {
	t.Run("zero values and uninitialized tps", func(t *testing.T) {
		got := formatStatusBoxPayload(snapshot.Snapshot{}, snapshot.SystemResourceSnapshot{}, -1, false)
		if got.Row1 != "A0 S0  M-- D--" {
			t.Errorf("Row1 = %q, want A0 S0  M-- D--", got.Row1)
		}
		if got.Row2 != "T --/s  ↓0 ↑0" {
			t.Errorf("Row2 = %q, want T --/s  ↓0 ↑0", got.Row2)
		}
	})

	t.Run("normal active values", func(t *testing.T) {
		snap := snapshot.Snapshot{
			Current: snapshot.CurrentMetrics{
				ActiveBurstConcurrency: 2,
				SessionConcurrency:     3,
			},
		}
		sysRes := snapshot.SystemResourceSnapshot{
			MemoryTotalBytes:     32 * 1024 * 1024 * 1024,
			MemoryUsedBytes:      16 * 1024 * 1024 * 1024,
			MemoryUsedPct:        96.2,
			DiskTotalBytes:       1024 * 1024 * 1024 * 1024,
			DiskUsedBytes:        240 * 1024 * 1024 * 1024,
			DiskUsedPct:          98.8,
			NetworkRxBytesPerSec: 700 * 1024,
			NetworkTxBytesPerSec: 100 * 1024,
		}
		got := formatStatusBoxPayload(snap, sysRes, 3200, true)
		if got.Row1 != "A2 S3  M96% D99%" {
			t.Errorf("Row1 = %q, want A2 S3  M96%%%% D99%%%%", got.Row1)
		}
		if got.Row2 != "T 3.2k/s  ↓700k ↑100k" {
			t.Errorf("Row2 = %q, want T 3.2k/s  ↓700k ↑100k", got.Row2)
		}
		if !got.Loading {
			t.Errorf("expected loading to be true")
		}
	})

	t.Run("fractional tps and single rate", func(t *testing.T) {
		snap := snapshot.Snapshot{}
		sysRes := snapshot.SystemResourceSnapshot{
			MemoryTotalBytes:     16 * 1024 * 1024 * 1024,
			MemoryUsedPct:        50.0,
			DiskTotalBytes:       500 * 1024 * 1024 * 1024,
			DiskUsedPct:          40.0,
			NetworkRxBytesPerSec: 1.2 * 1024 * 1024,
			NetworkTxBytesPerSec: 0,
		}
		got := formatStatusBoxPayload(snap, sysRes, 5.8, false)
		if got.Row1 != "A0 S0  M50% D40%" {
			t.Errorf("Row1 = %q, want A0 S0  M50%%%% D40%%%%", got.Row1)
		}
		if got.Row2 != "T 5.8/s  ↓1.2M ↑0" {
			t.Errorf("Row2 = %q, want T 5.8/s  ↓1.2M ↑0", got.Row2)
		}
	})
}

func TestDimMask(t *testing.T) {
	// The mask must dim only token-leading labels, never value glyphs or unit
	// suffixes. The regression: "↑1.2M" must keep its trailing "M" (megabytes)
	// bright even though "M" is also the Memory label.
	cases := []struct {
		row  string
		want string
	}{
		{"A2 S3  M96% D99%", "1001000100001000"},
		// ↓ and ↑ are 3-byte runes but dimMask emits one byte per rune.
		{"T 3.2k/s  ↓700k ↑100k", "100000000010000010000"},
		{"T 5.8/s  ↓1.2M ↑0", "10000000010000010"},
		{"A0 S0  M-- D--", "10010001000100"},
	}
	for _, c := range cases {
		got := dimMask(c.row)
		if got != c.want {
			t.Errorf("dimMask(%q) = %q, want %q", c.row, got, c.want)
		}
		if len([]rune(c.row)) != len(got) {
			t.Errorf("dimMask(%q): mask len %d != rune count %d", c.row, len(got), len([]rune(c.row)))
		}
	}
	// Explicit guard on the reported bug: the last char of "↑1.2M" is NOT dimmed.
	m := dimMask("T 152/s  ↓338k ↑1.2M")
	if m[len(m)-1] != '0' {
		t.Errorf("trailing megabytes unit was dimmed: mask=%q", m)
	}
}
