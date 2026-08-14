package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"fyne.io/systray"
)

// trayShutdownTimeout bounds the graceful HTTP shutdown during tray exit.
const trayShutdownTimeout = 5 * time.Second

type trayApp struct {
	cfg           Config
	observer      *Observer
	logger        *log.Logger
	server        *http.Server
	listener      net.Listener
	baseURL       string
	popoverURL    string
	dashboardURL  string
	liveTokenRate *liveTokenRateSampler
	lifecycle     *lifecycleLog

	stopCh    chan struct{}
	refreshCh chan struct{}

	shutdownMu       sync.Mutex
	shutdownOnce     sync.Once
	stopOnce         sync.Once
	shutdownDone     chan struct{}
	shutdownCtx      context.Context
	shutdownCancel   context.CancelFunc
	refreshDone      chan struct{}
	menuDone         chan struct{}
	statusBoxDone    chan struct{}
	heartbeatDone    <-chan struct{}
	refreshStarted   bool
	menuStarted      bool
	statusBoxStarted bool
	heartbeatStarted bool

	lastMu            sync.RWMutex
	lastSnapshot      Snapshot
	haveSnapshot      bool
	closing           bool
	refreshing        bool
	pendingSlot       string
	activeSlot        string
	lastSlot          string
	history           localHistoryState
	throughputHistory *throughputHistoryStore

	// mergeRecordedSampleFunc overrides the merge performed under lastMu. Nil in
	// production; tests set it to inject a failing merge.
	mergeRecordedSampleFunc func(Snapshot, HistorySample, time.Time, error) Snapshot

	// openURLFunc is a test seam for the external URL opener. Nil in production;
	// a contained menu step can then be tested with a real injected failure.
	openURLFunc func(string)

	// openHostAppFunc is a test seam for the external host-app opener. Nil in
	// production; the handler uses /usr/bin/open directly in that case.
	openHostAppFunc func(context.Context, string) ([]byte, error)

	// historyFileMu serializes JSONL appends so disk I/O never runs under lastMu.
	historyFileMu sync.Mutex

	clientCacheMu   sync.Mutex
	clientCacheSlot string
	clientCacheJSON []byte
	clientCacheGZIP []byte

	mCurrent       *systray.MenuItem
	mFocus         *systray.MenuItem
	mPeak          *systray.MenuItem
	mMeta          *systray.MenuItem
	mOpenDashboard *systray.MenuItem
	mRefreshNow    *systray.MenuItem
	mQuit          *systray.MenuItem
}

func newTrayApp(cfg Config, observer *Observer, logger *log.Logger, listener net.Listener, url string, lifecycle *lifecycleLog) *trayApp {
	if lifecycle == nil {
		lifecycle = newLifecycleLog(cfg.HistoryFile)
	}
	history, err := loadLocalHistoryState(cfg.HistoryFile, time.Now())
	if err != nil && logger != nil {
		logger.Printf("local history load failed: %v", err)
	}
	throughputHistory, throughputErr := loadThroughputHistoryStore(cfg.HistoryFile, time.Now())
	if throughputErr != nil && logger != nil {
		logger.Printf("throughput history load failed: %v", throughputErr)
	}
	if throughputHistory == nil {
		throughputHistory = &throughputHistoryStore{path: throughputHistoryPath(cfg.HistoryFile)}
	}
	if err := migrateLegacyThroughputHistory(&history, throughputHistory); err != nil && logger != nil {
		logger.Printf("legacy throughput migration failed: %v", err)
	}
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	liveTokenRate := newLiveTokenRateSampler(observer.adapters, observer.evidenceIndex)
	liveTokenRate.bindThroughputHistory(throughputHistory)
	a := &trayApp{
		cfg:               cfg,
		observer:          observer,
		logger:            logger,
		listener:          listener,
		baseURL:           strings.TrimRight(url, "/"),
		popoverURL:        strings.TrimRight(url, "/") + "/",
		dashboardURL:      strings.TrimRight(url, "/") + "/dashboard",
		liveTokenRate:     liveTokenRate,
		lifecycle:         lifecycle,
		stopCh:            make(chan struct{}),
		refreshCh:         make(chan struct{}, 1),
		shutdownDone:      make(chan struct{}),
		shutdownCtx:       shutdownCtx,
		shutdownCancel:    shutdownCancel,
		history:           history,
		throughputHistory: throughputHistory,
	}
	a.server = &http.Server{
		Handler: a.handler(),
	}
	return a
}

func (a *trayApp) run() error {
	startSystemResourceSampler(systemResourceSampleInterval)
	a.observer.evidenceIndex.start()
	if a.liveTokenRate != nil {
		a.liveTokenRate.start(liveTokenRateSampleInterval)
	}
	a.shutdownMu.Lock()
	if a.lifecycle != nil {
		heartbeatDone := a.lifecycle.startHeartbeat(a.stopCh, lifecycleHeartbeatInterval)
		a.heartbeatDone = heartbeatDone
		a.heartbeatStarted = heartbeatDone != nil
	}
	a.shutdownMu.Unlock()
	go func() {
		defer recoverBackgroundPanic("http server")
		if err := a.server.Serve(a.listener); err != nil && err != http.ErrServerClosed {
			a.logger.Printf("http server failed: %v", err)
		}
	}()
	systrayRun(a.onReady, a.onExit)
	// On macOS, systray.Quit stops the native loop without necessarily sending
	// the applicationWillTerminate notification that invokes onExit. Calling the
	// idempotent owner here closes that normal-return path as well.
	a.onExit()
	return nil
}

func (a *trayApp) ensureShutdownState() chan struct{} {
	if a == nil {
		return nil
	}
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	if a.shutdownDone == nil {
		a.shutdownDone = make(chan struct{})
	}
	if a.shutdownCtx == nil {
		a.shutdownCtx, a.shutdownCancel = context.WithCancel(context.Background())
	}
	return a.shutdownDone
}

func (a *trayApp) refreshContext() context.Context {
	if a == nil {
		return context.Background()
	}
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	if a.shutdownCtx == nil {
		a.shutdownCtx, a.shutdownCancel = context.WithCancel(context.Background())
	}
	return a.shutdownCtx
}

func (a *trayApp) registerLoopDone(refresh bool) chan struct{} {
	if a == nil {
		return nil
	}
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	done := make(chan struct{})
	if refresh {
		a.refreshDone = done
		a.refreshStarted = true
	} else {
		a.menuDone = done
		a.menuStarted = true
	}
	return done
}

func (a *trayApp) registerStatusBoxDone() chan struct{} {
	if a == nil {
		return nil
	}
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	done := make(chan struct{})
	a.statusBoxDone = done
	a.statusBoxStarted = true
	return done
}

func (a *trayApp) statusBoxLoopDone() (chan struct{}, bool) {
	if a == nil {
		return nil, false
	}
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	return a.statusBoxDone, a.statusBoxStarted
}

func (a *trayApp) loopDone(refresh bool) (chan struct{}, bool) {
	if a == nil {
		return nil, false
	}
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	if refresh {
		return a.refreshDone, a.refreshStarted
	}
	return a.menuDone, a.menuStarted
}

func (a *trayApp) heartbeatLoopDone() (<-chan struct{}, bool) {
	if a == nil {
		return nil, false
	}
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	return a.heartbeatDone, a.heartbeatStarted
}

func (a *trayApp) markClosing() {
	if a == nil {
		return
	}
	a.lastMu.Lock()
	a.closing = true
	a.pendingSlot = ""
	a.lastMu.Unlock()
}

func (a *trayApp) isClosing() bool {
	if a == nil {
		return true
	}
	a.lastMu.RLock()
	defer a.lastMu.RUnlock()
	return a.closing
}

func (a *trayApp) cancelShutdownContext() {
	if a == nil {
		return
	}
	a.shutdownMu.Lock()
	cancel := a.shutdownCancel
	a.shutdownMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *trayApp) closeStopChannel() {
	if a == nil {
		return
	}
	a.stopOnce.Do(func() {
		if a.stopCh != nil {
			close(a.stopCh)
		}
	})
}

func (a *trayApp) waitForTrayLoops() error {
	deadline := time.NewTimer(trayShutdownTimeout)
	defer deadline.Stop()
	wait := func(done <-chan struct{}, started bool) error {
		if !started || done == nil {
			return nil
		}
		select {
		case <-done:
			return nil
		case <-deadline.C:
			return fmt.Errorf("tray background loop did not stop within %s", trayShutdownTimeout)
		}
	}
	for _, refresh := range []bool{true, false} {
		done, started := a.loopDone(refresh)
		if err := wait(done, started); err != nil {
			return err
		}
	}
	if done, started := a.statusBoxLoopDone(); wait(done, started) != nil {
		return fmt.Errorf("tray status box did not stop within %s", trayShutdownTimeout)
	}
	if done, started := a.heartbeatLoopDone(); wait(done, started) != nil {
		return fmt.Errorf("tray heartbeat did not stop within %s", trayShutdownTimeout)
	}
	return nil
}

func (a *trayApp) onReady() {
	a.ensureShutdownState()
	if a.isClosing() {
		return
	}
	icon := renderStatusIcon(CurrentMetrics{}, true)
	systray.SetTemplateIcon(icon, icon)
	systray.SetTitle("…")
	systray.SetTooltip("Agent Load is starting")
	systray.SetOnTapped(a.togglePopover)

	a.mCurrent = systray.AddMenuItem("Waiting for first snapshot…", "")
	a.mCurrent.Disable()
	a.mFocus = systray.AddMenuItem("Mapping and project focus will appear here.", "")
	a.mFocus.Disable()
	a.mPeak = systray.AddMenuItem("Peaks will appear after the first refresh.", "")
	a.mPeak.Disable()
	a.mMeta = systray.AddMenuItem("Opening dashboard on "+a.dashboardURL, "")
	a.mMeta.Disable()
	systray.AddSeparator()
	a.mOpenDashboard = systray.AddMenuItem("Open Dashboard", "Open the detailed local dashboard")
	a.mRefreshNow = systray.AddMenuItem("Refresh Now", "Refresh the local snapshot")
	systray.AddSeparator()
	a.mQuit = systray.AddMenuItem("Quit", "Quit Agent Load")

	if nativePopoverSupported() {
		nativePopoverConfigureDashboard(a.dashboardURL)
		nativePopoverInstallStatusClickFallback(a.popoverURL)
	} else {
		systray.SetTooltip("Agent Load: native popover unavailable, click opens dashboard")
	}

	menuDone := a.registerLoopDone(false)
	go func() {
		defer close(menuDone)
		defer recoverBackgroundPanic("tray menu clicks")
		a.handleMenuClicks()
	}()
	refreshDone := a.registerLoopDone(true)
	go func() {
		defer close(refreshDone)
		defer recoverBackgroundPanic("tray refresh loop")
		a.refreshLoop()
	}()
	statusBoxDone := a.registerStatusBoxDone()
	go func() {
		defer close(statusBoxDone)
		defer recoverBackgroundPanic("tray status box loop")
		a.statusBoxLoop()
	}()
}

func (a *trayApp) onExit() {
	if a == nil {
		return
	}
	shutdownDone := a.ensureShutdownState()
	a.shutdownOnce.Do(func() {
		defer close(shutdownDone)
		a.performShutdown()
	})
	if shutdownDone != nil {
		<-shutdownDone
	}
}

func (a *trayApp) performShutdown() {
	var shutdownErr error
	runBackgroundStep("tray shutdown begin", func() {
		a.recordLifecycle(lifecycleEvent{Event: "shutdown_begin"})
	})
	a.markClosing()
	runBackgroundStep("tray shutdown signal", a.closeStopChannel)
	a.cancelShutdownContext()
	// Refresh/menu loops must stop before their shared evidence and sampler
	// owners are released. This closes the cancellation chain at its source and
	// prevents shutdown_complete from racing an in-flight refresh.
	runBackgroundStep("tray shutdown background loops", func() {
		if err := a.waitForTrayLoops(); err != nil {
			shutdownErr = err
		}
	})
	// Every cleanup step is contained separately: a panic in one owner (popover
	// teardown, a sampler stop) must not skip the remaining releases, the HTTP
	// shutdown, or the shutdown_complete record. Each owner is called inside the
	// closure rather than passed as a method value, so resolving the receiver is
	// contained too.
	runBackgroundStep("tray shutdown popover", func() {
		nativePopoverHide()
		nativePopoverInstallStatusClickFallback("")
		nativePopoverConfigureDashboard("")
	})
	ctx, cancel := context.WithTimeout(context.Background(), trayShutdownTimeout)
	defer cancel()
	runBackgroundStep("tray shutdown http server", func() {
		if a.server == nil {
			return
		}
		if err := a.server.Shutdown(ctx); err != nil && !strings.Contains(strings.ToLower(err.Error()), "closed network connection") {
			shutdownErr = err
			if a.logger != nil {
				a.logger.Printf("server shutdown failed: %v", err)
			}
		}
	})
	runBackgroundStep("tray shutdown live token rate", func() {
		if a.liveTokenRate != nil {
			a.liveTokenRate.stopSampler()
		}
	})
	runBackgroundStep("tray shutdown evidence index", func() {
		if a.observer != nil && a.observer.evidenceIndex != nil {
			a.observer.evidenceIndex.stopIndex()
		}
	})
	runBackgroundStep("tray shutdown system resources", stopSystemResourceSampler)
	event := lifecycleEvent{Event: "shutdown_complete"}
	if shutdownErr != nil {
		event.Error = shutdownErr.Error()
	}
	runBackgroundStep("tray shutdown complete record", func() { a.recordLifecycle(event) })
}

func (a *trayApp) handleMenuClicks() {
	for {
		select {
		case <-a.mOpenDashboard.ClickedCh:
			runBackgroundStep("tray menu click", func() { a.openURL(a.dashboardURL) })
		case <-a.mRefreshNow.ClickedCh:
			runBackgroundStep("tray menu click", func() { a.requestRefresh() })
		case <-a.mQuit.ClickedCh:
			runBackgroundStep("tray menu click", func() {
				a.recordLifecycle(lifecycleEvent{Event: "quit_requested", Reason: "menu"})
			})
			runBackgroundStep("tray menu quit", systrayQuit)
			return
		case <-a.stopCh:
			return
		}
	}
}

func (a *trayApp) refreshLoop() {
	// Contained: the very first request runs before the loop exists, so a panic
	// here would kill refreshLoop outright and the tray would never refresh
	// again for the whole session, ticker and menu clicks included.
	runBackgroundStep("tray refresh loop", func() {
		a.requestRefreshForSlot(a.refreshSlotID(time.Now()))
	})
	if a.cfg.RefreshInterval <= 0 {
		for {
			select {
			case <-a.refreshCh:
				a.runRefreshStep()
			case <-a.stopCh:
				return
			}
		}
	}
	ticker := time.NewTicker(a.cfg.RefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-a.refreshCh:
			a.runRefreshStep()
		case <-ticker.C:
			runBackgroundStep("tray refresh schedule", func() {
				a.requestRefreshForSlot(a.refreshSlotID(time.Now()))
			})
		case <-a.stopCh:
			return
		}
	}
}

// runRefreshStep performs one refresh and contains any panic to that refresh.
// The slot and refreshing guards are released in defers so a failed snapshot
// drops a single sample instead of stranding activeSlot/refreshing, which would
// make claimRefreshSlot reject every later slot and leave the UI reporting a
// refresh that never finishes.
func (a *trayApp) runRefreshStep() {
	if a.isClosing() {
		return
	}
	slotID := a.claimRefreshSlot()
	if slotID == "" {
		return
	}
	defer a.setRefreshing(false)
	defer a.finishRefreshSlot(slotID)
	a.setRefreshing(true)
	runBackgroundStep("tray refresh loop", func() {
		a.applyLoadingState()
		a.refreshOnce(slotID)
	})
}

func (a *trayApp) requestRefresh() string {
	return a.requestRefreshForSlot(a.refreshSlotID(time.Now()))
}

func (a *trayApp) requestRefreshForInterval(interval time.Duration) string {
	if interval <= 0 {
		return a.requestRefresh()
	}
	return a.requestRefreshForSlot(a.refreshSlotIDForInterval(time.Now(), interval))
}

func (a *trayApp) requestRefreshForSlot(slotID string) string {
	slotID = strings.TrimSpace(slotID)
	if slotID == "" {
		slotID = a.refreshSlotID(time.Now())
	}
	a.lastMu.Lock()
	if a.closing {
		a.lastMu.Unlock()
		return slotID
	}
	if slotID == a.lastSlot || slotID == a.activeSlot || slotID == a.pendingSlot {
		a.lastMu.Unlock()
		return slotID
	}
	a.pendingSlot = slotID
	a.lastMu.Unlock()
	select {
	case a.refreshCh <- struct{}{}:
	default:
	}
	return slotID
}

func (a *trayApp) claimRefreshSlot() string {
	a.lastMu.Lock()
	defer a.lastMu.Unlock()
	if a.closing {
		a.pendingSlot = ""
		return ""
	}
	slotID := a.pendingSlot
	if slotID == "" {
		slotID = a.refreshSlotID(time.Now())
	}
	if slotID == a.lastSlot || slotID == a.activeSlot {
		a.pendingSlot = ""
		return ""
	}
	a.pendingSlot = ""
	a.activeSlot = slotID
	return slotID
}

func (a *trayApp) finishRefreshSlot(slotID string) {
	a.lastMu.Lock()
	defer a.lastMu.Unlock()
	if slotID != "" {
		a.lastSlot = slotID
	}
	if a.activeSlot == slotID {
		a.activeSlot = ""
	}
}

func (a *trayApp) refreshSlotID(now time.Time) string {
	return a.refreshSlotIDForInterval(now, a.cfg.RefreshInterval)
}

func (a *trayApp) refreshSlotIDForInterval(now time.Time, interval time.Duration) string {
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	start := now.Truncate(interval)
	return fmt.Sprintf("%ds:%s", int(interval/time.Second), start.Format(time.RFC3339))
}

func (a *trayApp) refreshOnce(slotID string) {
	if a.isClosing() {
		return
	}
	ctx, cancel := context.WithTimeout(a.refreshContext(), clampDuration(a.cfg.Lookback/10, 90*time.Second, 5*time.Minute))
	defer cancel()
	snapshot := a.observer.Snapshot(ctx)
	if a.isClosing() {
		return
	}
	snapshot.RefreshSlotID = slotID
	scanAborted := snapshotScanAborted(ctx, snapshot)
	// A partial transcript scan must not replace the sampler's project mapping,
	// but it must not prevent the sampler from starting either. Persistent parser
	// errors are common enough that gating this independent live metric on a clean
	// snapshot would leave it disabled for the rest of the session.
	a.startLiveTokenRate(snapshot, scanAborted)
	if scanAborted {
		// Show the partial result but keep it out of history/cache so trends
		// and heatmaps only build from complete samples; the next slot rescans.
		a.applySnapshot(snapshot)
		a.recordLifecycle(lifecycleEventFromSnapshot("snapshot_aborted", snapshotAbortReason(ctx, snapshot), snapshot))
		return
	}
	snapshot = a.rememberSnapshot(snapshot)
	a.applySnapshot(snapshot)
}

// startLiveTokenRate starts the independent token metric for every refresh. A
// complete transcript snapshot may update attribution; an incomplete one keeps
// the previous mapping but still allows the metric to report unassigned data.
func (a *trayApp) startLiveTokenRate(snapshot Snapshot, scanAborted bool) {
	if a == nil || a.liveTokenRate == nil || a.isClosing() {
		return
	}
	if !scanAborted {
		a.liveTokenRate.updateSnapshotProjects(snapshot.LiveTokenProjects)
	}
	a.liveTokenRate.start(liveTokenRateSampleInterval)
}

// snapshotScanAborted reports whether a snapshot lacks global evidence
// coverage. A local parser error is disclosed as degraded evidence, while
// cancellation, discovery failure, and coverage gaps remain non-durable.
func snapshotScanAborted(ctx context.Context, snapshot Snapshot) bool {
	if ctx != nil && ctx.Err() != nil {
		return true
	}
	if snapshot.TranscriptStats.CoverageIncomplete {
		return true
	}
	if snapshot.ProcessStats.Incomplete {
		return true
	}
	for _, err := range snapshot.TranscriptStats.Errors {
		if transcriptScanErrorIsGlobal(err) {
			return true
		}
	}
	return false
}

func snapshotAbortReason(ctx context.Context, snapshot Snapshot) string {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err().Error()
	}
	if snapshot.TranscriptStats.CoverageIncomplete {
		return "transcript evidence coverage incomplete"
	}
	if snapshot.ProcessStats.Incomplete {
		if strings.TrimSpace(snapshot.ProcessStats.Error) != "" {
			return "process evidence incomplete: " + snapshot.ProcessStats.Error
		}
		return "process evidence incomplete"
	}
	if len(snapshot.TranscriptStats.Errors) > 0 {
		return snapshot.TranscriptStats.Errors[0]
	}
	return ""
}

func (a *trayApp) setRefreshing(value bool) {
	a.lastMu.Lock()
	a.refreshing = value
	a.lastMu.Unlock()
}

func (a *trayApp) isRefreshing() bool {
	a.lastMu.RLock()
	defer a.lastMu.RUnlock()
	return a.refreshing
}

func (a *trayApp) rememberSnapshot(snapshot Snapshot) Snapshot {
	if snapshot.TranscriptStats.CoverageIncomplete || snapshot.ProcessStats.Incomplete {
		// Incomplete evidence may be displayed for this refresh, but it must not
		// become the durable in-memory or JSONL history source of truth.
		return snapshot
	}
	sample := historySampleFromSnapshot(snapshot)
	var sampleTime time.Time
	sample.At, sampleTime = normalizeHistorySampleTimestamp(sample.At, time.Now())
	// The JSONL append runs before taking lastMu so /api/snapshot readers never
	// wait on disk I/O; historyFileMu alone keeps append ordering.
	appendErr := a.appendHistorySample(sample)
	if appendErr != nil && a.logger != nil {
		a.logger.Printf("local history append failed: %v", appendErr)
	}
	a.lastMu.Lock()
	// The unlock is deferred inside its own scope: mergeRecordedSampleLocked
	// builds trend/heatmap windows, so a panic there must not strand the write
	// lock every snapshot reader and refresh guard waits on.
	func() {
		defer a.lastMu.Unlock()
		snapshot = a.mergeRecordedSampleUnderLock(snapshot, sample, sampleTime, appendErr)
		a.lastSnapshot = snapshot
		a.haveSnapshot = true
	}()
	event := lifecycleEventFromSnapshot("snapshot_recorded", "", snapshot)
	if appendErr != nil {
		event.Error = appendErr.Error()
	}
	a.recordLifecycle(event)
	return snapshot
}

func (a *trayApp) recordLifecycle(event lifecycleEvent) {
	if a == nil || a.lifecycle == nil {
		return
	}
	if err := a.lifecycle.record(event); err != nil && a.logger != nil {
		a.logger.Printf("lifecycle log record failed: %v", err)
	}
}

func (a *trayApp) appendHistorySample(sample HistorySample) error {
	a.historyFileMu.Lock()
	defer a.historyFileMu.Unlock()
	return appendHistorySampleFile(a.history.path, sample)
}

func cloneLiveTokenRateProjectSamples(projects []LiveTokenRateProjectSample) []LiveTokenRateProjectSample {
	if projects == nil {
		return nil
	}
	return append([]LiveTokenRateProjectSample{}, projects...)
}

// mergeRecordedSampleUnderLock is the merge step as invoked while lastMu is
// held. It exists as its own field-backed seam so a test can inject a panicking
// merge and prove the lock is still released; production leaves it nil and uses
// mergeRecordedSampleLocked.
func (a *trayApp) mergeRecordedSampleUnderLock(snapshot Snapshot, sample HistorySample, sampleTime time.Time, appendErr error) Snapshot {
	if a.mergeRecordedSampleFunc != nil {
		return a.mergeRecordedSampleFunc(snapshot, sample, sampleTime, appendErr)
	}
	return a.mergeRecordedSampleLocked(snapshot, sample, sampleTime, appendErr)
}

func (a *trayApp) mergeRecordedSampleLocked(snapshot Snapshot, sample HistorySample, sampleTime time.Time, appendErr error) Snapshot {
	a.history.recordSampleInMemory(sample, sampleTime, appendErr)
	trendPoints := a.history.trendPoints()
	minuteFacts, legacyFacts := a.throughputHistory.snapshot()
	snapshot.RealtimeTrends = buildRealtimeTrendWindows(trendPoints, sampleTime)
	snapshot.ThroughputTrends = buildThroughputTrendWindows(minuteFacts, legacyFacts, sampleTime)
	snapshot.ProjectHeatmaps = buildProjectHeatmapWindows(a.history.samples, sampleTime)
	snapshot.History = a.history.snapshotMetadata()
	snapshot.History.Throughput = a.throughputHistory.snapshotMetadata()
	if snapshot.History.LastWriteError != "" {
		snapshot.Notes = uniqueSortedStrings(append(snapshot.Notes, "Local history append failed; see history.last_write_error."))
	}
	if snapshot.History.Throughput != nil && snapshot.History.Throughput.LastWriteError != "" {
		snapshot.Notes = uniqueSortedStrings(append(snapshot.Notes, "Throughput history append failed; see history.throughput.last_write_error."))
	}
	return snapshot
}

func (a *trayApp) cachedSnapshot() (Snapshot, bool) {
	a.lastMu.RLock()
	defer a.lastMu.RUnlock()
	if !a.haveSnapshot {
		return Snapshot{}, false
	}
	return a.lastSnapshot, true
}

func (a *trayApp) applyLoadingState() {
	a.updateStatusBox()
	if a.mRefreshNow != nil {
		a.mRefreshNow.SetTitle("Refreshing…")
		a.mRefreshNow.Disable()
	}
	if _, ok := a.cachedSnapshot(); ok {
		return
	}
	if a.mCurrent != nil {
		a.mCurrent.SetTitle("Refreshing snapshot…")
	}
}

func (a *trayApp) applySnapshot(snapshot Snapshot) {
	a.updateStatusBox()

	if a.mCurrent != nil {
		a.mCurrent.SetTitle(fmt.Sprintf(
			"Live field: %d bursts · %d sessions · %d pids",
			snapshot.Current.ActiveBurstConcurrency,
			snapshot.Current.SessionConcurrency,
			snapshot.Current.PIDConcurrency,
		))
	}
	if a.mFocus != nil {
		focus := "No live project focus yet."
		if len(snapshot.ProjectFocus) > 0 {
			top := snapshot.ProjectFocus[0]
			focus = fmt.Sprintf(
				"Focus: %s · %dA/%dS/%dP · %.1f%% mapped",
				top.Project,
				top.ActiveBurstCount,
				top.SessionCount,
				top.ProcessCount,
				snapshot.Summary.MappingCoveragePct,
			)
		}
		a.mFocus.SetTitle(focus)
	}
	if a.mPeak != nil {
		a.mPeak.SetTitle(fmt.Sprintf(
			"Peaks: today %s · 7d %s",
			formatCompactPeak(snapshot.HistoricPeaks.Today),
			formatCompactPeak(snapshot.HistoricPeaks.SevenDay),
		))
	}
	if a.mMeta != nil {
		a.mMeta.SetTitle(formatTrayMetaTitle(snapshot))
	}
	if a.mRefreshNow != nil {
		a.mRefreshNow.SetTitle("Refresh Now")
		a.mRefreshNow.Enable()
	}
}

func (a *trayApp) statusBoxLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	a.updateStatusBox()
	for {
		select {
		case <-ticker.C:
			a.updateStatusBox()
		case <-a.stopCh:
			return
		}
	}
}

func (a *trayApp) updateStatusBox() {
	if a == nil || a.isClosing() {
		return
	}
	snapshot, _ := a.cachedSnapshot()
	sysRes, ok := latestBackgroundSystemResourceSample()
	if !ok {
		sysRes = snapshot.SystemResources
	}
	now := time.Now()
	var tps float64 = -1
	if a.liveTokenRate != nil {
		sample := a.liveTokenRate.sample(now)
		if sample.OutputTokensPerSecond != nil {
			tps = *sample.OutputTokensPerSecond
		} else if sample.State == "zero" {
			tps = 0
		}
	}
	payload := formatStatusBoxPayload(snapshot, sysRes, tps, a.isRefreshing())
	if nativeStatusBoxSupported() {
		nativeStatusBoxUpdate(payload)
	} else {
		icon := renderStatusIcon(snapshot.Current, payload.Loading)
		systray.SetTemplateIcon(icon, icon)
		if payload.Loading && snapshot.Current.ActiveBurstConcurrency == 0 && snapshot.Current.SessionConcurrency == 0 {
			systray.SetTitle("…")
		} else {
			systray.SetTitle(formatStatusTitle(snapshot))
		}
	}
	if payload.Loading {
		systray.SetTooltip("Agent Load is refreshing\n" + formatTooltip(snapshot, sysRes, tps))
	} else {
		systray.SetTooltip(formatTooltip(snapshot, sysRes, tps))
	}
}

type statusBoxPayload struct {
	Row1     string
	Row2     string
	Row1Mask string
	Row2Mask string
	Loading  bool
}

func formatStatusBoxPayload(snapshot Snapshot, sysRes SystemResourceSnapshot, tps float64, loading bool) statusBoxPayload {
	row1 := fmt.Sprintf("A%d S%d  %s", snapshot.Current.ActiveBurstConcurrency, snapshot.Current.SessionConcurrency, formatMetroResources(sysRes))
	row2 := fmt.Sprintf("%s  %s", formatMetroTPS(tps), formatMetroNetwork(sysRes.NetworkRxBytesPerSec, sysRes.NetworkTxBytesPerSec))
	return statusBoxPayload{
		Row1:     row1,
		Row2:     row2,
		Row1Mask: dimMask(row1),
		Row2Mask: dimMask(row2),
		Loading:  loading,
	}
}

// dimMask marks which runes are metric labels (dimmed in the menubar widget):
// the leading glyph of each whitespace-delimited token, when it is a known
// label. Value glyphs and unit suffixes (k/M/G/B/T) are never token-leading, so
// they stay bright. Output is one byte ('1'/'0') per rune of row.
func dimMask(row string) string {
	const labels = "ASTMD↓↑"
	var b strings.Builder
	atStart := true
	for _, r := range row {
		dim := false
		if r == ' ' {
			atStart = true
		} else {
			dim = atStart && strings.ContainsRune(labels, r)
			atStart = false
		}
		if dim {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
	}
	return b.String()
}

func formatMetroResources(sysRes SystemResourceSnapshot) string {
	mem := "M--"
	if sysRes.MemoryTotalBytes > 0 {
		mem = fmt.Sprintf("M%.0f%%", sysRes.MemoryUsedPct)
	}
	disk := "D--"
	if sysRes.DiskTotalBytes > 0 {
		disk = fmt.Sprintf("D%.0f%%", sysRes.DiskUsedPct)
	}
	return mem + " " + disk
}

func formatMetroTPS(tps float64) string {
	switch {
	case tps < 0:
		return "T --/s"
	case tps == 0:
		return "T 0/s"
	case tps < 10:
		if tps == float64(int(tps)) {
			return fmt.Sprintf("T %d/s", int(tps))
		}
		return fmt.Sprintf("T %.1f/s", tps)
	case tps < 1000:
		return fmt.Sprintf("T %.0f/s", tps)
	default:
		return fmt.Sprintf("T %.1fk/s", tps/1000)
	}
}

func formatRateUnit(r float64) string {
	switch {
	case r <= 0:
		return "0"
	case r < 1024:
		return fmt.Sprintf("%.0fB", r)
	case r < 1000*1024:
		return fmt.Sprintf("%.0fk", r/1024)
	case r < 1000*1024*1024:
		return fmt.Sprintf("%.1fM", r/(1024*1024))
	default:
		return fmt.Sprintf("%.1fG", r/(1024*1024*1024))
	}
}

func formatMetroNetwork(rx, tx float64) string {
	if rx <= 0 && tx <= 0 {
		return "↓0 ↑0"
	}
	return fmt.Sprintf("↓%s ↑%s", formatRateUnit(rx), formatRateUnit(tx))
}

func formatTrayMetaTitle(snapshot Snapshot) string {
	cacheState := "fresh scan"
	if snapshot.TranscriptStats.Cached {
		cacheState = "cache hit"
	}
	parts := []string{
		fmt.Sprintf("Updated %s", formatTimestamp(snapshot.GeneratedAt)),
		cacheState,
		fmt.Sprintf("%d/%d transcripts", snapshot.TranscriptStats.ParsedFiles, snapshot.TranscriptStats.ScannedFiles),
		fmt.Sprintf("%d deferred", snapshot.TranscriptStats.DeferredFiles),
		fmt.Sprintf("%d tail", snapshot.TranscriptStats.TailParsedFiles),
	}
	if snapshot.TranscriptStats.HistoricalScanDeferred {
		if snapshot.TranscriptStats.ForegroundScanLookbackSeconds > 0 {
			parts = append(parts, "foreground "+formatDurationLabel(time.Duration(snapshot.TranscriptStats.ForegroundScanLookbackSeconds)*time.Second))
		}
		parts = append(parts, "history deferred")
	}
	return strings.Join(parts, " · ")
}

func (a *trayApp) togglePopover() {
	if !nativePopoverSupported() {
		a.openURL(a.dashboardURL)
		return
	}
	nativePopoverConfigureDashboard(a.dashboardURL)
	nativePopoverShow(a.popoverURL)
}

func (a *trayApp) openURL(url string) {
	url = strings.TrimSpace(url)
	if url == "" {
		return
	}
	if a != nil && a.openURLFunc != nil {
		a.openURLFunc(url)
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		a.logger.Printf("open url failed: %v", err)
	}
}

func formatStatusTitle(snapshot Snapshot) string {
	if snapshot.Current.ActiveBurstConcurrency == 0 && snapshot.Current.SessionConcurrency == 0 {
		return "Idle"
	}
	return fmt.Sprintf("%dA %dS", snapshot.Current.ActiveBurstConcurrency, snapshot.Current.SessionConcurrency)
}

func formatCompactBytes(b uint64, suffix string) string {
	if b == 0 {
		return strings.TrimSpace("-- " + suffix)
	}
	gb := float64(b) / (1024 * 1024 * 1024)
	if gb >= 1024 {
		return strings.TrimSpace(fmt.Sprintf("%.1fT %s", gb/1024, suffix))
	}
	if gb >= 10 {
		return strings.TrimSpace(fmt.Sprintf("%.0fG %s", gb, suffix))
	}
	return strings.TrimSpace(fmt.Sprintf("%.1fG %s", gb, suffix))
}

func capitalizeTool(tool string) string {
	switch strings.ToLower(tool) {
	case "codex":
		return "Codex"
	case "claude":
		return "Claude"
	case "trae":
		return "Trae"
	case "grok":
		return "Grok"
	default:
		return tool
	}
}

func formatTooltip(snapshot Snapshot, sysRes SystemResourceSnapshot, tps float64) string {
	var tpsStr string
	if tps >= 0 {
		if tps < 10 {
			tpsStr = fmt.Sprintf("%.1f tok/s", tps)
		} else {
			tpsStr = fmt.Sprintf("%.0f tok/s", tps)
		}
	} else {
		tpsStr = "--"
	}
	memStr := "--"
	if sysRes.MemoryTotalBytes > 0 {
		memStr = fmt.Sprintf("%.0f%%", sysRes.MemoryUsedPct)
	}
	diskStr := "--"
	if sysRes.DiskTotalBytes > 0 {
		diskStr = fmt.Sprintf("%.0f%%", sysRes.DiskUsedPct)
	}
	netStr := formatMetroNetwork(sysRes.NetworkRxBytesPerSec, sysRes.NetworkTxBytesPerSec)

	var lines []string

	// Direct 1-to-1 mapping to menubar items (3 compact lines, never wrap)
	lines = append(lines, fmt.Sprintf("[A]ctive: %d · [S]essions: %d", snapshot.Current.ActiveBurstConcurrency, snapshot.Current.SessionConcurrency))
	lines = append(lines, fmt.Sprintf("[M]emory: %s · [D]isk: %s", memStr, diskStr))
	lines = append(lines, fmt.Sprintf("[T]PS: %s · %s", tpsStr, netStr))

	// Top projects
	var projectLines []string
	for i, p := range snapshot.ProjectFocus {
		if i >= 3 {
			break
		}
		if p.ActiveBurstCount > 0 || p.SessionCount > 0 {
			projectLines = append(projectLines, fmt.Sprintf("  • %s (%dA/%dS)", p.Project, p.ActiveBurstCount, p.SessionCount))
		}
	}
	if len(projectLines) > 0 {
		lines = append(lines, "", "Top Projects:")
		lines = append(lines, projectLines...)
	}

	// Tools, Peaks, Host
	var metaLines []string

	var toolParts []string
	for _, tool := range []string{"codex", "claude", "trae", "grok"} {
		if m, ok := snapshot.CurrentByTool[tool]; ok && (m.ActiveBurstConcurrency > 0 || m.SessionConcurrency > 0) {
			toolParts = append(toolParts, fmt.Sprintf("%s (%dA/%dS)", capitalizeTool(tool), m.ActiveBurstConcurrency, m.SessionConcurrency))
		}
	}
	if len(toolParts) > 0 {
		metaLines = append(metaLines, fmt.Sprintf("Tools: %s", strings.Join(toolParts, " · ")))
	}

	today := snapshot.HistoricPeaks.Today
	sevenDay := snapshot.HistoricPeaks.SevenDay
	if today.ActiveBurstConcurrency.Value > 0 || today.SessionConcurrency.Value > 0 ||
		sevenDay.ActiveBurstConcurrency.Value > 0 || sevenDay.SessionConcurrency.Value > 0 {
		metaLines = append(metaLines, fmt.Sprintf("Peaks: Today %dA/%dS · 7-Day %dA/%dS",
			today.ActiveBurstConcurrency.Value,
			today.SessionConcurrency.Value,
			sevenDay.ActiveBurstConcurrency.Value,
			sevenDay.SessionConcurrency.Value,
		))
	}

	if sysRes.Supported {
		var hostParts []string
		if sysRes.CPUPercent > 0 || sysRes.LoadAverage1 > 0 {
			hostParts = append(hostParts, fmt.Sprintf("CPU %.1f%%", sysRes.CPUPercent))
		}
		if sysRes.MemoryTotalBytes > 0 {
			hostParts = append(hostParts, fmt.Sprintf("RAM %s/%s", formatCompactBytes(sysRes.MemoryUsedBytes, ""), formatCompactBytes(sysRes.MemoryTotalBytes, "")))
		}
		if sysRes.DiskTotalBytes > 0 {
			hostParts = append(hostParts, fmt.Sprintf("Disk %s/%s", formatCompactBytes(sysRes.DiskUsedBytes, ""), formatCompactBytes(sysRes.DiskTotalBytes, "")))
		}
		if len(hostParts) > 0 {
			metaLines = append(metaLines, fmt.Sprintf("Host: %s", strings.Join(hostParts, " · ")))
		}
	}

	metaParts := []string{fmt.Sprintf("Updated %s", formatTimestamp(snapshot.GeneratedAt))}
	if snapshot.TranscriptStats.ScannedFiles > 0 {
		cacheState := "fresh scan"
		if snapshot.TranscriptStats.Cached {
			cacheState = "cache hit"
		}
		metaParts = append(metaParts, fmt.Sprintf("%d transcripts (%s)", snapshot.TranscriptStats.ScannedFiles, cacheState))
	}
	metaLines = append(metaLines, strings.Join(metaParts, " · "))

	if len(metaLines) > 0 {
		lines = append(lines, "")
		lines = append(lines, metaLines...)
	}

	return strings.Join(lines, "\n")
}

func formatActiveWindowSeconds(seconds int) string {
	if seconds <= 0 {
		return "configured window"
	}
	if seconds%3600 == 0 {
		return fmt.Sprintf("%dh", seconds/3600)
	}
	if seconds%60 == 0 {
		return fmt.Sprintf("%dm", seconds/60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func formatCompactPeak(window PeakWindow) string {
	return fmt.Sprintf("%dA/%dS", window.ActiveBurstConcurrency.Value, window.SessionConcurrency.Value)
}

func formatTimestamp(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "--"
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, raw); err == nil {
			return ts.Local().Format("15:04:05")
		}
	}
	return raw
}

func clampDuration(value, floor, ceiling time.Duration) time.Duration {
	if value < floor {
		return floor
	}
	if value > ceiling {
		return ceiling
	}
	return value
}

func renderStatusIcon(metrics CurrentMetrics, loading bool) []byte {
	const (
		width    = 18
		height   = 18
		baseline = 14
	)
	img := image.NewNRGBA(image.Rect(0, 0, width, height))
	black := color.NRGBA{R: 0, G: 0, B: 0, A: 255}
	drawRect := func(x0, y0, x1, y1 int) {
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if x >= 0 && x < width && y >= 0 && y < height {
					img.SetNRGBA(x, y, black)
				}
			}
		}
	}
	drawRect(2, baseline+1, 16, baseline+2)
	if loading {
		for _, x := range []int{4, 8, 12} {
			drawRect(x, baseline-1, x+2, baseline+1)
		}
	} else {
		values := []int{
			metrics.ActiveBurstConcurrency,
			metrics.SessionConcurrency,
			metrics.PIDConcurrency,
		}
		maxValue := 1
		for _, value := range values {
			if value > maxValue {
				maxValue = value
			}
		}
		for i, value := range values {
			h := 3 + (value*8+maxValue-1)/maxValue
			if value == 0 {
				h = 2
			}
			x := 3 + i*4
			drawRect(x, baseline-h, x+2, baseline)
		}
		drawRect(15, 4, 16, 6)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil
	}
	return buf.Bytes()
}

// systrayQuit is a seam so tests can observe the quit hand-off without driving a
// real Cocoa tray; production always quits the systray.
var systrayQuit = func() {
	systray.Quit()
}

// systrayRun is a seam for the post-native-loop cleanup contract. Production
// delegates to fyne.io/systray; tests can model a normal native-loop return
// without starting a Cocoa status item.
var systrayRun = systray.Run
