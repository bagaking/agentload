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

type trayApp struct {
	cfg          Config
	observer     *Observer
	logger       *log.Logger
	server       *http.Server
	listener     net.Listener
	baseURL      string
	popoverURL   string
	dashboardURL string

	stopCh    chan struct{}
	refreshCh chan struct{}

	lastMu       sync.RWMutex
	lastSnapshot Snapshot
	haveSnapshot bool
	refreshing   bool
	pendingSlot  string
	activeSlot   string
	lastSlot     string
	history      localHistoryState

	mCurrent       *systray.MenuItem
	mFocus         *systray.MenuItem
	mPeak          *systray.MenuItem
	mMeta          *systray.MenuItem
	mOpenDashboard *systray.MenuItem
	mRefreshNow    *systray.MenuItem
	mQuit          *systray.MenuItem
}

func newTrayApp(cfg Config, observer *Observer, logger *log.Logger, listener net.Listener, url string) *trayApp {
	history, err := loadLocalHistoryState(cfg.HistoryFile, time.Now())
	if err != nil && logger != nil {
		logger.Printf("local history load failed: %v", err)
	}
	a := &trayApp{
		cfg:          cfg,
		observer:     observer,
		logger:       logger,
		listener:     listener,
		baseURL:      strings.TrimRight(url, "/"),
		popoverURL:   strings.TrimRight(url, "/") + "/",
		dashboardURL: strings.TrimRight(url, "/") + "/dashboard",
		stopCh:       make(chan struct{}),
		refreshCh:    make(chan struct{}, 1),
		history:      history,
	}
	a.server = &http.Server{
		Handler: a.handler(),
	}
	return a
}

func (a *trayApp) run() error {
	go func() {
		if err := a.server.Serve(a.listener); err != nil && err != http.ErrServerClosed {
			a.logger.Printf("http server failed: %v", err)
		}
	}()
	systray.Run(a.onReady, a.onExit)
	return nil
}

func (a *trayApp) onReady() {
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

	go a.handleMenuClicks()
	go a.refreshLoop()
}

func (a *trayApp) onExit() {
	select {
	case <-a.stopCh:
	default:
		close(a.stopCh)
	}
	nativePopoverHide()
	nativePopoverInstallStatusClickFallback("")
	nativePopoverConfigureDashboard("")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.server.Shutdown(ctx); err != nil && !strings.Contains(strings.ToLower(err.Error()), "closed network connection") {
		a.logger.Printf("server shutdown failed: %v", err)
	}
}

func (a *trayApp) handleMenuClicks() {
	for {
		select {
		case <-a.mOpenDashboard.ClickedCh:
			a.openURL(a.dashboardURL)
		case <-a.mRefreshNow.ClickedCh:
			a.requestRefresh()
		case <-a.mQuit.ClickedCh:
			systrayQuit()
			return
		case <-a.stopCh:
			return
		}
	}
}

func (a *trayApp) refreshLoop() {
	a.requestRefreshForSlot(a.refreshSlotID(time.Now()))
	if a.cfg.RefreshInterval <= 0 {
		for {
			select {
			case <-a.refreshCh:
				slotID := a.claimRefreshSlot()
				if slotID == "" {
					continue
				}
				a.setRefreshing(true)
				a.applyLoadingState()
				a.refreshOnce(slotID)
				a.finishRefreshSlot(slotID)
				a.setRefreshing(false)
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
			slotID := a.claimRefreshSlot()
			if slotID == "" {
				continue
			}
			a.setRefreshing(true)
			a.applyLoadingState()
			a.refreshOnce(slotID)
			a.finishRefreshSlot(slotID)
			a.setRefreshing(false)
		case <-ticker.C:
			a.requestRefreshForSlot(a.refreshSlotID(time.Now()))
		case <-a.stopCh:
			return
		}
	}
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
	ctx, cancel := context.WithTimeout(context.Background(), maxDuration(90*time.Second, a.cfg.Lookback/10))
	defer cancel()
	snapshot := a.observer.Snapshot(ctx)
	snapshot.RefreshSlotID = slotID
	snapshot = a.rememberSnapshot(snapshot)
	a.applySnapshot(snapshot)
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
	a.lastMu.Lock()
	snapshot = a.mergeRuntimeTrendsLocked(snapshot)
	a.lastSnapshot = snapshot
	a.haveSnapshot = true
	a.lastMu.Unlock()
	return snapshot
}

func (a *trayApp) mergeRuntimeTrendsLocked(snapshot Snapshot) Snapshot {
	sample := historySampleFromSnapshot(snapshot)
	sampleTime, ok := historySampleTime(sample)
	if !ok {
		sampleTime = time.Now()
		sample.At = sampleTime.Format(time.RFC3339)
	}
	if err := a.history.recordSample(sample); err != nil && a.logger != nil {
		a.logger.Printf("local history append failed: %v", err)
	}
	snapshot.RealtimeTrends = buildRealtimeTrendWindows(a.history.trendPoints(), sampleTime)
	snapshot.History = a.history.snapshotMetadata()
	if snapshot.History.LastWriteError != "" {
		snapshot.Notes = uniqueSortedStrings(append(snapshot.Notes, "Local history append failed; see history.last_write_error."))
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
	icon := renderStatusIcon(CurrentMetrics{}, true)
	systray.SetTemplateIcon(icon, icon)
	if snapshot, ok := a.cachedSnapshot(); ok {
		systray.SetTitle(formatStatusTitle(snapshot))
		systray.SetTooltip("Agent Load is refreshing\n" + formatTooltip(snapshot))
		if a.mRefreshNow != nil {
			a.mRefreshNow.SetTitle("Refreshing…")
			a.mRefreshNow.Disable()
		}
		return
	}
	systray.SetTitle("…")
	systray.SetTooltip("Agent Load is refreshing")
	if a.mCurrent != nil {
		a.mCurrent.SetTitle("Refreshing snapshot…")
	}
	if a.mRefreshNow != nil {
		a.mRefreshNow.SetTitle("Refreshing…")
		a.mRefreshNow.Disable()
	}
}

func (a *trayApp) applySnapshot(snapshot Snapshot) {
	icon := renderStatusIcon(snapshot.Current, false)
	systray.SetTemplateIcon(icon, icon)
	systray.SetTitle(formatStatusTitle(snapshot))
	systray.SetTooltip(formatTooltip(snapshot))

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

func formatTooltip(snapshot Snapshot) string {
	codex := snapshot.CurrentByTool["codex"]
	claude := snapshot.CurrentByTool["claude"]
	firstProject := "none"
	if len(snapshot.ProjectFocus) > 0 {
		firstProject = snapshot.ProjectFocus[0].Project
	}
	return fmt.Sprintf(
		"Active %d · Sessions %d · PIDs %d\nProjects %d · Active projects %d · Mapping %.1f%%\nActive means local-log movement within %s\nCodex %d/%d/%d · Claude %d/%d/%d\nFirst project row %s · Updated %s",
		snapshot.Current.ActiveBurstConcurrency,
		snapshot.Current.SessionConcurrency,
		snapshot.Current.PIDConcurrency,
		snapshot.Summary.ProjectCount,
		snapshot.Summary.HotProjectCount,
		snapshot.Summary.MappingCoveragePct,
		formatActiveWindowSeconds(snapshot.Config.IdleGapSeconds),
		codex.ActiveBurstConcurrency,
		codex.SessionConcurrency,
		codex.PIDConcurrency,
		claude.ActiveBurstConcurrency,
		claude.SessionConcurrency,
		claude.PIDConcurrency,
		firstProject,
		formatTimestamp(snapshot.GeneratedAt),
	)
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

func maxDuration(values ...time.Duration) time.Duration {
	var best time.Duration
	for _, value := range values {
		if value > best {
			best = value
		}
	}
	return best
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

func systrayQuit() {
	systray.Quit()
}
