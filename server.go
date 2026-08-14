package main

import (
	"context"
	"crypto/sha1"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"agentload/internal/httpencoding"
)

//go:embed ui/dist/* ui/dist/assets/* ui/tool-icons/*
var uiAssets embed.FS

// quitAPIHandoffDelay lets the quit response reach the client before the tray
// tears the process down.
const quitAPIHandoffDelay = 150 * time.Millisecond

func (a *trayApp) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handlePopoverPage)
	mux.HandleFunc("/dashboard", a.handleDashboardPage)
	mux.HandleFunc("/assets/", a.handleUIAsset)
	mux.HandleFunc("/api/snapshot", a.handleSnapshotAPI)
	mux.HandleFunc("/api/system-resources", a.handleSystemResourcesAPI)
	mux.HandleFunc("/api/live-token-rate", a.handleLiveTokenRateAPI)
	mux.HandleFunc("/api/diagnostic-export", a.handleDiagnosticExportAPI)
	mux.HandleFunc("/api/refresh", a.handleRefreshAPI)
	mux.HandleFunc("/api/quit", a.handleQuitAPI)
	mux.HandleFunc("/api/process-diagnostic/", a.handleProcessDiagnosticAPI)
	mux.HandleFunc("/api/tool-icon/", a.handleToolIconAPI)
	mux.HandleFunc("/api/host-app-icon/", a.handleHostAppIconAPI)
	mux.HandleFunc("/api/open-host-app/", a.handleOpenHostAppAPI)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasDotPathSegment(r.URL.EscapedPath()) {
			http.NotFound(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *trayApp) handleLiveTokenRateAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	sample := LiveTokenRateSample{}
	if a.liveTokenRate != nil {
		sample = a.liveTokenRate.sample(time.Now())
	} else {
		sample = liveTokenRateSampleFromFacts(liveTokenRateFacts{
			Window: liveTokenRateWindow, SampleInterval: liveTokenRateSampleInterval,
			StaleAfter: liveTokenRateStaleAfter, SampledAt: time.Now(),
		})
	}
	_ = json.NewEncoder(w).Encode(sanitizeLiveTokenRateSampleForClient(sample))
}

func (a *trayApp) handleSystemResourcesAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(sampleSystemResources())
}

func (a *trayApp) handleDiagnosticExportAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	snapshot := a.snapshotForClient(r.Context())
	export := buildDiagnosticExport(snapshot, time.Now())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Disposition", `attachment; filename="agentload-diagnostics.json"`)
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	_ = json.NewEncoder(w).Encode(export)
}

func (a *trayApp) handlePopoverPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	serveEmbeddedFile(w, r, "ui/dist/index.html", "text/html; charset=utf-8", true)
}

func (a *trayApp) handleDashboardPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/dashboard" {
		http.NotFound(w, r)
		return
	}
	serveEmbeddedFile(w, r, "ui/dist/index.html", "text/html; charset=utf-8", true)
}

func (a *trayApp) handleUIAsset(w http.ResponseWriter, r *http.Request) {
	cleaned := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if !strings.HasPrefix(cleaned, "assets/") || strings.Contains(strings.TrimPrefix(cleaned, "assets/"), "/") {
		http.NotFound(w, r)
		return
	}
	assetPath := "ui/dist/" + cleaned
	serveEmbeddedFile(w, r, assetPath, contentTypeFor(assetPath), true)
}

func (a *trayApp) handleSnapshotAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The refresh slot decides the ETag, so conditional requests can short-circuit
	// before the sanitize pass runs.
	snapshot, ok := a.snapshotForInternalUse(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Vary", "Accept-Encoding")
	if !ok {
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(Snapshot{})
		return
	}
	wantsGzip := httpencoding.AcceptsGzip(r.Header.Get("Accept-Encoding"))
	if wantsGzip {
		w.Header().Set("Content-Encoding", "gzip")
	}
	var snapshotETag string
	if snapshot.RefreshSlotID != "" {
		snapshotETag = strconv.Quote(snapshot.RefreshSlotID)
		w.Header().Set("X-Refresh-Slot-ID", snapshot.RefreshSlotID)
		w.Header().Set("ETag", snapshotETag)
	}
	if snapshotETag != "" && etagListMatches(r.Header.Get("If-None-Match"), snapshotETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	payload := a.clientSnapshotJSON(snapshot)
	if wantsGzip {
		payload = a.clientSnapshotGZIP(snapshot)
	}
	_, _ = w.Write(payload)
}

// clientSnapshotJSON caches the sanitized, encoded snapshot per refresh slot so
// repeated polls within one slot skip the sanitize pass.
func (a *trayApp) clientSnapshotJSON(snapshot Snapshot) []byte {
	slotID := snapshot.RefreshSlotID
	if slotID != "" {
		a.clientCacheMu.Lock()
		if a.clientCacheSlot == slotID && a.clientCacheJSON != nil {
			payload := a.clientCacheJSON
			a.clientCacheMu.Unlock()
			return payload
		}
		a.clientCacheMu.Unlock()
	}
	raw, err := json.Marshal(sanitizeSnapshotForClient(snapshot))
	if err != nil {
		return []byte("{}\n")
	}
	payload := append(raw, '\n')
	if slotID != "" {
		a.clientCacheMu.Lock()
		a.clientCacheSlot = slotID
		a.clientCacheJSON = payload
		a.clientCacheGZIP = nil
		a.clientCacheMu.Unlock()
	}
	return payload
}

func (a *trayApp) clientSnapshotGZIP(snapshot Snapshot) []byte {
	slotID := snapshot.RefreshSlotID
	if slotID != "" {
		a.clientCacheMu.Lock()
		if a.clientCacheSlot == slotID && a.clientCacheGZIP != nil {
			payload := a.clientCacheGZIP
			a.clientCacheMu.Unlock()
			return payload
		}
		a.clientCacheMu.Unlock()
	}

	payload := httpencoding.Gzip(a.clientSnapshotJSON(snapshot))
	if slotID != "" {
		a.clientCacheMu.Lock()
		if a.clientCacheSlot == slotID {
			a.clientCacheGZIP = payload
		}
		a.clientCacheMu.Unlock()
	}
	return payload
}

func etagListMatches(header, expected string) bool {
	for _, item := range strings.Split(header, ",") {
		item = strings.TrimSpace(item)
		if item == "*" || item == expected {
			return true
		}
	}
	return false
}

// sameOriginRequest guards state-changing POSTs against cross-origin calls from
// other local webpages. Requests without Origin/Referer (curl, native shell)
// stay allowed; the WKWebView popover sends this server's own http origin.
func sameOriginRequest(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		origin = strings.TrimSpace(r.Header.Get("Referer"))
	}
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return false
	}
	return originHostsEquivalent(parsed.Host, r.Host)
}

func originHostsEquivalent(originHost, requestHost string) bool {
	oh, op := splitHostPortDefault(originHost)
	rh, rp := splitHostPortDefault(requestHost)
	return op == rp && normalizeLoopbackHost(oh) == normalizeLoopbackHost(rh)
}

func splitHostPortDefault(hostport string) (string, string) {
	if host, port, err := net.SplitHostPort(hostport); err == nil {
		return host, port
	}
	return hostport, "80"
}

func normalizeLoopbackHost(host string) string {
	host = strings.ToLower(strings.Trim(host, "[]"))
	switch host {
	case "localhost", "::1", "0.0.0.0", "::":
		return "127.0.0.1"
	}
	return host
}

func rejectCrossOrigin(w http.ResponseWriter, r *http.Request) bool {
	if sameOriginRequest(r) {
		return false
	}
	http.Error(w, "cross-origin request rejected", http.StatusForbidden)
	return true
}

func (a *trayApp) handleRefreshAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rejectCrossOrigin(w, r) {
		return
	}
	slotID := a.requestRefreshForInterval(refreshIntervalFromRequest(r, a.cfg.RefreshInterval))
	jsonResponse(w, http.StatusAccepted, map[string]any{
		"ok":              true,
		"refreshing":      a.isRefreshing(),
		"refresh_slot_id": slotID,
	})
}

func refreshIntervalFromRequest(r *http.Request, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(r.URL.Query().Get("interval_ms"))
	if raw == "" {
		return fallback
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms < 0 {
		return fallback
	}
	return normalizeRefreshInterval(time.Duration(ms) * time.Millisecond)
}

func (a *trayApp) handleQuitAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rejectCrossOrigin(w, r) {
		return
	}
	a.recordLifecycle(lifecycleEvent{Event: "quit_requested", Reason: "api"})
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
	go func() {
		// The quit hand-off is deferred so the HTTP response flushes first; the
		// recover keeps a panic here from killing the process before the tray
		// gets its quit, which is the only way this cleanup path runs.
		defer recoverBackgroundPanic("quit api")
		time.Sleep(quitAPIHandoffDelay)
		systrayQuit()
	}()
}

func (a *trayApp) handleProcessDiagnosticAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawPID := strings.TrimPrefix(r.URL.Path, "/api/process-diagnostic/")
	if rawPID == "" || strings.Contains(rawPID, "/") {
		http.NotFound(w, r)
		return
	}
	pid, err := strconv.Atoi(rawPID)
	if err != nil || pid <= 0 {
		http.NotFound(w, r)
		return
	}
	snapshot := a.snapshotForClient(r.Context())
	if snapshot.GeneratedAt == "" {
		http.NotFound(w, r)
		return
	}
	for _, process := range snapshot.LiveProcesses {
		if process.PID != pid {
			continue
		}
		diagnostic := ProcessDiagnosticSnapshot{
			PID:          process.PID,
			Command:      sanitizeCommandForClient(process.Command),
			SessionIDs:   append([]string(nil), process.SessionIDs...),
			SessionPaths: nil,
			HostApp:      sanitizedHostApp(process.HostApp),
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = json.NewEncoder(w).Encode(diagnostic)
		return
	}
	http.NotFound(w, r)
}

func (a *trayApp) handleToolIconAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawTool := strings.TrimPrefix(r.URL.Path, "/api/tool-icon/")
	tool := normalizeToolIconName(rawTool)
	if tool == "" || strings.Contains(rawTool, "/") {
		http.NotFound(w, r)
		return
	}
	assetPath, ctype, ok := resolveEmbeddedToolIconFile(tool)
	if ok {
		serveEmbeddedToolIcon(w, r, assetPath, ctype)
		return
	}
	filePath, ctype, ok := resolveToolIconFile(tool)
	if ok {
		serveLocalToolIcon(w, r, filePath, ctype)
		return
	}
	http.NotFound(w, r)
}

func (a *trayApp) handleHostAppIconAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	app, ok := a.observedHostAppFromRequest(r, "/api/host-app-icon/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	filePath, ctype, ok := resolveHostAppIconFile(app)
	if !ok {
		http.NotFound(w, r)
		return
	}
	serveLocalToolIcon(w, r, filePath, ctype)
}

func (a *trayApp) handleOpenHostAppAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if rejectCrossOrigin(w, r) {
		return
	}
	app, ok := a.observedHostAppFromRequest(r, "/api/open-host-app/")
	if !ok || strings.TrimSpace(app.BundlePath) == "" {
		http.NotFound(w, r)
		return
	}
	openHostApp := a.openHostAppFunc
	if openHostApp == nil {
		openHostApp = func(ctx context.Context, bundlePath string) ([]byte, error) {
			return exec.CommandContext(ctx, "/usr/bin/open", bundlePath).CombinedOutput()
		}
	}
	if _, err := openHostApp(r.Context(), app.BundlePath); err != nil {
		// `open` may include the full bundle path or other local diagnostics in
		// stderr. The endpoint is client-facing, so expose only a stable error.
		http.Error(w, "failed to open observed host app", http.StatusBadGateway)
		return
	}
	jsonResponse(w, http.StatusAccepted, map[string]any{
		"ok":   true,
		"name": app.Name,
		"pid":  app.PID,
	})
}

func (a *trayApp) snapshotForClient(ctx context.Context) Snapshot {
	snapshot, ok := a.snapshotForInternalUse(ctx)
	if !ok {
		return Snapshot{}
	}
	return sanitizeSnapshotForClient(snapshot)
}

func (a *trayApp) snapshotForInternalUse(ctx context.Context) (Snapshot, bool) {
	if a == nil {
		return Snapshot{}, false
	}
	if snapshot, ok := a.cachedSnapshot(); ok {
		if snapshot.RefreshSlotID == "" {
			snapshot.RefreshSlotID = a.refreshSlotID(time.Now())
		}
		return snapshot, true
	}
	if a.observer == nil {
		return Snapshot{}, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, clampDuration(a.cfg.Lookback/10, 45*time.Second, 5*time.Minute))
	defer cancel()
	snapshot := a.observer.Snapshot(ctx)
	if snapshot.RefreshSlotID == "" {
		snapshot.RefreshSlotID = a.refreshSlotID(time.Now())
	}
	if snapshotScanAborted(ctx, snapshot) {
		// Serve the partial result to this caller only; committing it would
		// record undercounted history samples and cache incomplete state.
		a.recordLifecycle(lifecycleEventFromSnapshot("snapshot_aborted", snapshotAbortReason(ctx, snapshot), snapshot))
		return snapshot, true
	}
	return a.rememberSnapshot(snapshot), true
}

// snapshotSanitizePasses counts full sanitize passes; test hook for the cached
// /api/snapshot encode path.
var snapshotSanitizePasses atomic.Int64

func sanitizeSnapshotForClient(snapshot Snapshot) Snapshot {
	snapshotSanitizePasses.Add(1)
	snapshot.Config.ClaudeRoots = []string{}
	snapshot.Config.CodexRoots = []string{}
	snapshot.Config.TraeRoots = []string{}
	snapshot.Config.HistoryFile = ""
	snapshot.History.StorePath = ""
	snapshot.History.LastWriteError = sanitizeTextForClient(snapshot.History.LastWriteError)
	if snapshot.History.Throughput != nil {
		throughput := *snapshot.History.Throughput
		throughput.StorePath = ""
		throughput.LastWriteError = sanitizeTextForClient(throughput.LastWriteError)
		snapshot.History.Throughput = &throughput
	}
	snapshot.TranscriptStats.Errors = sanitizeTextListForClient(snapshot.TranscriptStats.Errors)
	snapshot.ProcessStats.Error = sanitizeTextForClient(snapshot.ProcessStats.Error)
	snapshot.CoordinationRisk = sanitizeCoordinationRiskForClient(snapshot.CoordinationRisk)
	snapshot.ProjectFocus = sanitizeProjectFocusForClient(snapshot.ProjectFocus)
	snapshot.ProjectHeatmaps = sanitizeProjectHeatmapsForClient(snapshot.ProjectHeatmaps)
	snapshot.ThroughputTrends = sanitizeThroughputTrendsForClient(snapshot.ThroughputTrends)
	snapshot.CandidateWorkitems = sanitizeCandidateWorkitemsForClient(snapshot.CandidateWorkitems)
	snapshot.LiveProcesses = sanitizeLiveProcessesForClient(snapshot.LiveProcesses)
	snapshot.LiveSessions = sanitizeLiveSessionsForClient(snapshot.LiveSessions)
	snapshot.RuntimeProcesses = sanitizeRuntimeProcessSummaryForClient(snapshot.RuntimeProcesses)
	snapshot.HostAppProcesses = sanitizeHostAppProcessSummaryForClient(snapshot.HostAppProcesses)
	snapshot.RuntimeTelemetry = sanitizeRuntimeTelemetryForClient(snapshot.RuntimeTelemetry)
	snapshot.Diagnostics = sanitizeDiagnosticsForClient(snapshot.Diagnostics)
	snapshot.Notes = sanitizeTextListForClient(snapshot.Notes)
	return snapshot
}

func sanitizeThroughputTrendsForClient(trends TrendSet) TrendSet {
	if len(trends.Windows) == 0 {
		return trends
	}
	windows := append([]TrendWindow(nil), trends.Windows...)
	for i := range windows {
		series := append([]ThroughputTrendSeries(nil), windows[i].ThroughputSeries...)
		for j := range series {
			series[j].Points = append([]TrendPoint(nil), series[j].Points...)
			for k := range series[j].Points {
				projects := cloneLiveTokenRateProjectSamples(series[j].Points[k].OutputTokenProjects)
				for l := range projects {
					projects[l].Project = sanitizeProjectNameForClient(projects[l].Project)
				}
				series[j].Points[k].OutputTokenProjects = projects
			}
		}
		windows[i].ThroughputSeries = series
	}
	trends.Windows = windows
	return trends
}

func sanitizeLiveTokenRateSampleForClient(sample LiveTokenRateSample) LiveTokenRateSample {
	sample.State = sanitizeTokenForClient(sample.State)
	sample.Basis = sanitizeTokenForClient(sample.Basis)
	sample.Source = sanitizeTokenForClient(sample.Source)
	sample.Method = sanitizeTokenForClient(sample.Method)
	sample.UnavailableReason = sanitizeTextForClient(sample.UnavailableReason)
	if len(sample.Projects) == 0 {
		return sample
	}
	sample.Projects = append([]LiveTokenRateProjectSample(nil), sample.Projects...)
	for i := range sample.Projects {
		sample.Projects[i].Project = sanitizeProjectNameForClient(sample.Projects[i].Project)
	}
	return sample
}

func sanitizeRuntimeTelemetryForClient(telemetry RuntimeTelemetrySnapshot) RuntimeTelemetrySnapshot {
	telemetry.Status = sanitizeTokenForClient(telemetry.Status)
	telemetry.Detail = sanitizeTextForClient(telemetry.Detail)
	if len(telemetry.Adapters) > 0 {
		adapters := append([]RuntimeTelemetryAdapterState(nil), telemetry.Adapters...)
		for i := range adapters {
			adapters[i].Key = sanitizeTokenForClient(adapters[i].Key)
			adapters[i].Label = sanitizeTextForClient(adapters[i].Label)
			adapters[i].Status = sanitizeTokenForClient(adapters[i].Status)
			adapters[i].Detail = sanitizeTextForClient(adapters[i].Detail)
		}
		telemetry.Adapters = adapters
	}
	return telemetry
}

func sanitizeDiagnosticsForClient(diagnostics DiagnosticSnapshot) DiagnosticSnapshot {
	diagnostics.AnomalySignals = sanitizeDiagnosticSignalsForClient(diagnostics.AnomalySignals)
	diagnostics.EvidenceGaps = sanitizeDiagnosticSignalsForClient(diagnostics.EvidenceGaps)
	for i := range diagnostics.Baselines {
		diagnostics.Baselines[i].Key = sanitizeTokenForClient(diagnostics.Baselines[i].Key)
		diagnostics.Baselines[i].Label = sanitizeTextForClient(diagnostics.Baselines[i].Label)
		diagnostics.Baselines[i].Value = sanitizeTextForClient(diagnostics.Baselines[i].Value)
		diagnostics.Baselines[i].Status = sanitizeTokenForClient(diagnostics.Baselines[i].Status)
		diagnostics.Baselines[i].Detail = sanitizeTextForClient(diagnostics.Baselines[i].Detail)
		diagnostics.Baselines[i].MetricKey = sanitizeTokenForClient(diagnostics.Baselines[i].MetricKey)
	}
	for i := range diagnostics.Capabilities {
		diagnostics.Capabilities[i].Key = sanitizeTokenForClient(diagnostics.Capabilities[i].Key)
		diagnostics.Capabilities[i].Label = sanitizeTextForClient(diagnostics.Capabilities[i].Label)
		diagnostics.Capabilities[i].Status = sanitizeTokenForClient(diagnostics.Capabilities[i].Status)
		diagnostics.Capabilities[i].Detail = sanitizeTextForClient(diagnostics.Capabilities[i].Detail)
	}
	diagnostics.Export.Redaction = sanitizeTextForClient(diagnostics.Export.Redaction)
	diagnostics.Export.OmittedFields = sanitizeTextListForClient(diagnostics.Export.OmittedFields)
	return diagnostics
}

func sanitizeDiagnosticSignalsForClient(items []DiagnosticSignalSnapshot) []DiagnosticSignalSnapshot {
	if len(items) == 0 {
		return items
	}
	out := append([]DiagnosticSignalSnapshot(nil), items...)
	for i := range out {
		out[i].Kind = sanitizeTokenForClient(out[i].Kind)
		out[i].Severity = sanitizeTokenForClient(out[i].Severity)
		out[i].Title = sanitizeTextForClient(out[i].Title)
		out[i].Detail = sanitizeTextForClient(out[i].Detail)
		out[i].Evidence = sanitizeTextForClient(out[i].Evidence)
		out[i].MetricKey = sanitizeTokenForClient(out[i].MetricKey)
		out[i].Source = sanitizeTokenForClient(out[i].Source)
	}
	return out
}

func sanitizeCoordinationRiskForClient(risk CoordinationRiskSnapshot) CoordinationRiskSnapshot {
	risk.TopProject = sanitizeProjectNameForClient(risk.TopProject)
	if len(risk.Signals) == 0 {
		return risk
	}
	signals := append([]RiskSignalSnapshot(nil), risk.Signals...)
	for i := range signals {
		signals[i].Evidence = sanitizeTextForClient(signals[i].Evidence)
	}
	risk.Signals = signals
	return risk
}

func sanitizeProjectHeatmapsForClient(heatmaps ProjectHeatmapSet) ProjectHeatmapSet {
	if len(heatmaps.Windows) == 0 {
		return heatmaps
	}
	windows := append([]ProjectHeatmapWindow(nil), heatmaps.Windows...)
	for i := range windows {
		windows[i].Items = append([]ProjectHeatmapItem(nil), windows[i].Items...)
		for j := range windows[i].Items {
			windows[i].Items[j].Project = sanitizeProjectNameForClient(windows[i].Items[j].Project)
		}
	}
	heatmaps.Windows = windows
	return heatmaps
}

func sanitizeProjectFocusForClient(projects []ProjectSnapshot) []ProjectSnapshot {
	if len(projects) == 0 {
		return projects
	}
	out := append([]ProjectSnapshot(nil), projects...)
	for i := range out {
		out[i].Project = sanitizeProjectNameForClient(out[i].Project)
		out[i].ConfidenceReasons = sanitizeTextListForClient(out[i].ConfidenceReasons)
		out[i].ProjectAttributionReasons = sanitizeTextListForClient(out[i].ProjectAttributionReasons)
		out[i].Tools = append([]ProjectToolSnapshot(nil), out[i].Tools...)
	}
	return out
}

func sanitizeCandidateWorkitemsForClient(items []CandidateWorkitemSnapshot) []CandidateWorkitemSnapshot {
	if len(items) == 0 {
		return items
	}
	out := append([]CandidateWorkitemSnapshot(nil), items...)
	for i := range out {
		out[i].Key = sanitizeCandidateWorkitemKeyForClient(out[i].Key)
		out[i].Project = sanitizeProjectNameForClient(out[i].Project)
		out[i].SessionIDs = append([]string(nil), out[i].SessionIDs...)
		out[i].ConfidenceReasons = sanitizeTextListForClient(out[i].ConfidenceReasons)
		out[i].ProjectAttributionReasons = sanitizeTextListForClient(out[i].ProjectAttributionReasons)
	}
	return out
}

func sanitizeProjectNameForClient(project string) string {
	return sanitizePathLikeValue(strings.TrimSpace(project))
}

func sanitizeCandidateWorkitemKeyForClient(key string) string {
	if key == "" {
		return key
	}
	parts := strings.Split(key, "|")
	for i, part := range parts {
		name, value, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		if name == "project" {
			parts[i] = name + "=" + sanitizeProjectNameForClient(value)
		}
	}
	return strings.Join(parts, "|")
}

func sanitizeLiveProcessesForClient(processes []LiveProcessSnapshot) []LiveProcessSnapshot {
	if len(processes) == 0 {
		return processes
	}
	out := append([]LiveProcessSnapshot(nil), processes...)
	for i := range out {
		out[i].Command = sanitizeCommandForClient(out[i].Command)
		out[i].DisplayName = sanitizeTokenForClient(out[i].DisplayName)
		out[i].SessionIDs = append([]string(nil), out[i].SessionIDs...)
		out[i].SessionPaths = nil
		out[i].MatchMethods = append([]string(nil), out[i].MatchMethods...)
		if len(out[i].MappedSessionEvidence) > 0 {
			evidence := append([]ProcessSessionEvidence(nil), out[i].MappedSessionEvidence...)
			for j := range evidence {
				evidence[j].Project = sanitizeProjectNameForClient(evidence[j].Project)
				evidence[j].Provenance = append([]string(nil), evidence[j].Provenance...)
			}
			out[i].MappedSessionEvidence = evidence
		}
		if out[i].HostApp != nil {
			out[i].HostApp = sanitizedHostApp(out[i].HostApp)
		}
	}
	return out
}

func sanitizedHostApp(host *HostApp) *HostApp {
	if host == nil {
		return nil
	}
	next := *host
	next.Name = sanitizeTextForClient(next.Name)
	next.BundlePath = ""
	return &next
}

func sanitizeRuntimeProcessSummaryForClient(items []ProcessRuntimeSummary) []ProcessRuntimeSummary {
	if len(items) == 0 {
		return items
	}
	out := append([]ProcessRuntimeSummary(nil), items...)
	for i := range out {
		out[i].Key = sanitizeTokenForClient(out[i].Key)
		out[i].Tool = sanitizeTokenForClient(out[i].Tool)
		out[i].DisplayName = sanitizeTokenForClient(out[i].DisplayName)
	}
	return out
}

func sanitizeHostAppProcessSummaryForClient(items []HostAppProcessSummary) []HostAppProcessSummary {
	if len(items) == 0 {
		return items
	}
	out := append([]HostAppProcessSummary(nil), items...)
	for i := range out {
		out[i].Name = sanitizeTextForClient(out[i].Name)
	}
	return out
}

func sanitizeLiveSessionsForClient(sessions []LiveSessionSnapshot) []LiveSessionSnapshot {
	if len(sessions) == 0 {
		return sessions
	}
	out := append([]LiveSessionSnapshot(nil), sessions...)
	for i := range out {
		out[i].Project = sanitizeProjectNameForClient(out[i].Project)
		out[i].Path = ""
		out[i].RoleReasons = sanitizeTextListForClient(out[i].RoleReasons)
		out[i].ConfidenceReasons = sanitizeTextListForClient(out[i].ConfidenceReasons)
		out[i].ProjectAttributionReasons = sanitizeTextListForClient(out[i].ProjectAttributionReasons)
		out[i].Provenance = append([]string(nil), out[i].Provenance...)
		if len(out[i].HostApps) > 0 {
			hosts := append([]HostApp(nil), out[i].HostApps...)
			for j := range hosts {
				hosts[j].Name = sanitizeTextForClient(hosts[j].Name)
				hosts[j].BundlePath = ""
			}
			out[i].HostApps = hosts
		}
	}
	return out
}

func sanitizeTextListForClient(items []string) []string {
	if len(items) == 0 {
		return items
	}
	out := append([]string(nil), items...)
	for i, item := range out {
		out[i] = sanitizeTextForClient(item)
	}
	return out
}

func sanitizeCommandForClient(command string) string {
	fields := strings.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	out := []string{sanitizeTokenForClient(fields[0])}
	redactedTail := false
	for i := 1; i < len(fields); i++ {
		field := fields[i]
		if strings.HasPrefix(field, "-") {
			if key, _, ok := strings.Cut(field, "="); ok {
				out = append(out, sanitizeTokenForClient(key)+"=<value>")
				continue
			}
			out = append(out, sanitizeTokenForClient(field))
			if i+1 < len(fields) && !strings.HasPrefix(fields[i+1], "-") && !isCommandIdentityWord(fields[i+1]) {
				out = append(out, "<value>")
				i++
			}
			continue
		}
		if isCommandIdentityWord(field) {
			out = append(out, sanitizeTokenForClient(field))
			continue
		}
		if !redactedTail {
			out = append(out, "...")
			redactedTail = true
		}
		break
	}
	return strings.Join(out, " ")
}

func isCommandIdentityWord(field string) bool {
	key := strings.Trim(strings.ToLower(field), "\"'.,;:()[]{}<>")
	switch key {
	case "apply", "auth", "chat", "diff", "doctor", "exec", "help", "login", "logout", "mcp", "resume", "review", "run", "serve", "server", "status", "update", "version":
		return true
	default:
		return false
	}
}

func sanitizeTextForClient(text string) string {
	// Scan before splitting on whitespace so a local path such as a workspace
	// with spaces is redacted as one value instead of leaking its later segments.
	text = sanitizeEmbeddedAbsolutePaths(text)
	fields := strings.Fields(strings.TrimSpace(text))
	if len(fields) == 0 {
		return text
	}
	changed := false
	for i, field := range fields {
		next := sanitizeTokenForClient(field)
		if next != field {
			changed = true
		}
		fields[i] = next
	}
	if !changed {
		return text
	}
	return strings.Join(fields, " ")
}

func sanitizeTokenForClient(token string) string {
	prefix, core, suffix := splitTokenPunctuation(token)
	if core == "" {
		return token
	}
	if key, value, ok := strings.Cut(core, "="); ok {
		return sanitizeEmbeddedAbsolutePaths(prefix + key + "=" + sanitizePathLikeValue(value) + suffix)
	}
	if key, value, ok := strings.Cut(core, ":"); ok && key != "" && value != "" {
		return sanitizeEmbeddedAbsolutePaths(prefix + key + ":" + sanitizePathLikeValue(value) + suffix)
	}
	return sanitizeEmbeddedAbsolutePaths(prefix + sanitizePathLikeValue(core) + suffix)
}

func sanitizePathLikeValue(value string) string {
	if parsed, err := url.Parse(value); err == nil && strings.EqualFold(parsed.Scheme, "file") && parsed.Path != "" {
		return sanitizePathLikeValue(filepath.FromSlash(parsed.Path))
	}
	if !filepath.IsAbs(value) {
		return value
	}
	cleaned := filepath.Clean(value)
	base := filepath.Base(cleaned)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "local-path"
	}
	// A shallow absolute path is ambiguous: on macOS it can be a home directory,
	// and the final component may be a username rather than a
	// useful filename. Keep basenames only when the path has enough structure
	// to make that distinction reasonably safe.
	if strings.Count(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator)) < 2 {
		return "local-path"
	}
	if strings.ContainsAny(base, " \t\r\n") {
		return "local-path"
	}
	return base
}

// sanitizeEmbeddedAbsolutePaths closes the gap between token-level redaction
// and structured/error text where an absolute path is surrounded by non-path
// characters. The surrounding syntax is retained, while every recognized local
// path is reduced to its base.
func sanitizeEmbeddedAbsolutePaths(token string) string {
	last := 0
	searchFrom := 0
	changed := false
	var out strings.Builder
	for searchFrom < len(token) {
		offset := strings.IndexByte(token[searchFrom:], '/')
		if offset < 0 {
			break
		}
		start := searchFrom + offset
		end := embeddedAbsolutePathEnd(token, start)
		candidate := token[start:end]
		if !filepath.IsAbs(candidate) {
			searchFrom = start + 1
			continue
		}
		if !changed {
			out.Grow(len(token))
		}
		prefixEnd := start
		if strings.HasSuffix(token[last:start], "file:") {
			filePrefixStart := start - len("file:")
			if filePrefixStart == 0 || strings.ContainsRune(" \t\r\n\"'([{:=", rune(token[filePrefixStart-1])) {
				// The token-level sanitizer treats file URIs as paths, so do not
				// expose the URI scheme when the whole-text pass handles a path
				// that contains spaces.
				prefixEnd = filePrefixStart
			}
		}
		out.WriteString(token[last:prefixEnd])
		out.WriteString(sanitizePathLikeValue(candidate))
		last = end
		searchFrom = end
		changed = true
	}
	if !changed {
		return token
	}
	out.WriteString(token[last:])
	return out.String()
}

func embeddedAbsolutePathEnd(text string, start int) int {
	end := start + 1
	for end < len(text) {
		if strings.ContainsRune("\"'[]{}()<>,;:!?&|", rune(text[end])) {
			return end
		}
		if !strings.ContainsRune(" \t\r\n", rune(text[end])) {
			end++
			continue
		}
		next := end
		for next < len(text) && strings.ContainsRune(" \t\r\n", rune(text[next])) {
			next++
		}
		if next == len(text) {
			return next
		}
		wordEnd := next
		for wordEnd < len(text) && !strings.ContainsRune(" \t\r\n", rune(text[wordEnd])) {
			wordEnd++
		}
		word := text[next:wordEnd]
		slash := strings.IndexByte(word, '/')
		colon := strings.IndexByte(word, ':')
		if strings.HasPrefix(word, "/") || strings.HasPrefix(word, "-") || strings.ContainsRune(word, '=') ||
			(colon >= 0 && (slash < 0 || colon < slash)) {
			return end
		}
		if slash < 0 && isClientPathBoundaryWord(word) && !nextWordContinuesPath(text, wordEnd) {
			return end
		}
		end = next
	}
	return end
}

func nextWordContinuesPath(text string, start int) bool {
	for start < len(text) && strings.ContainsRune(" \t\r\n", rune(text[start])) {
		start++
	}
	end := start
	for end < len(text) && !strings.ContainsRune(" \t\r\n", rune(text[end])) {
		end++
	}
	word := text[start:end]
	return word != "" && !strings.HasPrefix(word, "/") && strings.ContainsRune(word, '/')
}

func isClientPathBoundaryWord(word string) bool {
	word = strings.Trim(word, "\"'([{<,;:!?&|])}>")
	switch strings.ToLower(word) {
	case "after", "and", "at", "before", "because", "both", "context", "deadline", "denied", "during", "error", "exceeded", "failed", "failure", "for", "from", "in", "is", "not", "now", "open", "opened", "or", "parse", "parsing", "permission", "readable", "scan", "scanning", "then", "to", "while", "with":
		return true
	default:
		return false
	}
}

func splitTokenPunctuation(token string) (string, string, string) {
	start := 0
	end := len(token)
	for start < end && strings.ContainsRune("\"'([{<", rune(token[start])) {
		start++
	}
	for end > start && strings.ContainsRune("\"')]}>,;:", rune(token[end-1])) {
		end--
	}
	return token[:start], token[start:end], token[end:]
}

func serveEmbeddedFile(w http.ResponseWriter, r *http.Request, path, ctype string, noCache bool) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	f, err := uiAssets.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if stat, err := fs.Stat(uiAssets, path); err == nil && stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	if noCache {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
	} else {
		w.Header().Set("Cache-Control", "private, max-age=300")
	}
	_, _ = io.Copy(w, f)
}

func (a *trayApp) observedHostAppFromRequest(r *http.Request, prefix string) (HostApp, bool) {
	rawPID := strings.TrimPrefix(r.URL.Path, prefix)
	if rawPID == "" || strings.Contains(rawPID, "/") {
		return HostApp{}, false
	}
	pid, err := strconv.Atoi(rawPID)
	if err != nil || pid <= 0 {
		return HostApp{}, false
	}
	snapshot, ok := a.cachedSnapshot()
	if !ok {
		var loaded bool
		snapshot, loaded = a.snapshotForInternalUse(r.Context())
		if !loaded {
			return HostApp{}, false
		}
	}
	for _, process := range snapshot.LiveProcesses {
		if app := process.HostApp; app != nil && app.PID == pid && validObservedHostApp(*app) {
			return *app, true
		}
	}
	for _, session := range snapshot.LiveSessions {
		for _, app := range session.HostApps {
			if app.PID == pid && validObservedHostApp(app) {
				return app, true
			}
		}
	}
	return HostApp{}, false
}

func validObservedHostApp(app HostApp) bool {
	if app.PID <= 0 || strings.TrimSpace(app.Name) == "" {
		return false
	}
	bundlePath := strings.TrimSpace(app.BundlePath)
	if bundlePath == "" {
		return true
	}
	cleaned := filepath.Clean(bundlePath)
	if !filepath.IsAbs(cleaned) || !strings.HasSuffix(cleaned, ".app") || strings.Contains(cleaned, "..") {
		return false
	}
	info, err := os.Stat(cleaned)
	return err == nil && info.IsDir()
}

func jsonResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func contentTypeFor(path string) string {
	switch {
	case strings.HasSuffix(path, ".css"):
		return "text/css; charset=utf-8"
	case strings.HasSuffix(path, ".js"):
		return "application/javascript; charset=utf-8"
	case strings.HasSuffix(path, ".html"):
		return "text/html; charset=utf-8"
	case strings.HasSuffix(path, ".svg"):
		return "image/svg+xml; charset=utf-8"
	default:
		return "application/octet-stream"
	}
}

func normalizeToolIconName(raw string) string {
	key := strings.TrimSpace(strings.ToLower(raw))
	key = strings.TrimSuffix(key, ".app")
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "codex", "codexl", "com.openai.codex":
		return "codex"
	case "trae", "traex", "trae-cli", "trae_cli", "traecli":
		return "trae"
	case "karp", "warp", "warposs", "warp-oss":
		return "karp"
	case "claude", "claude-code", "claude-cli", "anthropic":
		return "claude"
	case "opencode", "opencode-ai":
		return "opencode"
	case "gemini", "gemini-cli", "@google/gemini-cli":
		return "gemini"
	default:
		return ""
	}
}

var toolIconFiles = map[string][]string{
	"trae": {
		"/Applications/Trae.app/Contents/Resources/AppIcon.icns",
		"/Applications/Trae.app/Contents/Resources/icon.icns",
	},
	"karp": {
		"/Applications/Karp.app/Contents/Resources/WarpOss.icns",
	},
	"claude": {
		"/Applications/Claude.app/Contents/Resources/icon.icns",
		"/Applications/Claude.app/Contents/Resources/AppIcon.icns",
	},
}

var embeddedToolIconFiles = map[string][]string{
	"codex":    {"ui/tool-icons/codex.svg"},
	"trae":     {"ui/tool-icons/trae.svg"},
	"claude":   {"ui/tool-icons/claude.svg"},
	"opencode": {"ui/tool-icons/opencode.svg"},
	"gemini":   {"ui/tool-icons/gemini.svg"},
}

func resolveToolIconFile(tool string) (string, string, bool) {
	for _, candidate := range toolIconFiles[tool] {
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(candidate)) {
		case ".png":
			return candidate, "image/png", true
		case ".jpg", ".jpeg":
			return candidate, "image/jpeg", true
		case ".icns":
			if converted, err := cachedPNGForICNS(tool, candidate); err == nil {
				return converted, "image/png", true
			}
		}
	}
	return "", "", false
}

func resolveHostAppIconFile(app HostApp) (string, string, bool) {
	bundlePath := filepath.Clean(strings.TrimSpace(app.BundlePath))
	if bundlePath == "" {
		return "", "", false
	}
	resourcesDir := filepath.Join(bundlePath, "Contents", "Resources")
	candidates := []string{}
	for _, name := range bundleIconResourceNames(bundlePath) {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if filepath.Ext(name) == "" {
			candidates = append(candidates, filepath.Join(resourcesDir, name+".icns"))
			candidates = append(candidates, filepath.Join(resourcesDir, name+".png"))
		}
		candidates = append(candidates, filepath.Join(resourcesDir, name))
	}
	for _, name := range []string{"AppIcon.icns", "appicon.icns", "Icon.icns", "icon.icns"} {
		candidates = append(candidates, filepath.Join(resourcesDir, name))
	}
	for _, pattern := range []string{"*.icns", "*.png", "*.jpg", "*.jpeg"} {
		matches, _ := filepath.Glob(filepath.Join(resourcesDir, pattern))
		sort.Strings(matches)
		candidates = append(candidates, matches...)
	}
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(candidate)) {
		case ".png":
			return candidate, "image/png", true
		case ".jpg", ".jpeg":
			return candidate, "image/jpeg", true
		case ".icns":
			if converted, err := cachedPNGForICNS("host-"+safeIconCacheKey(app.Name), candidate); err == nil {
				return converted, "image/png", true
			}
		}
	}
	return "", "", false
}

func bundleIconResourceNames(bundlePath string) []string {
	infoPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	commands := []string{
		"Print :CFBundleIconFile",
		"Print :CFBundleIcons:CFBundlePrimaryIcon:CFBundleIconFiles",
	}
	out := []string{}
	for _, command := range commands {
		output, err := exec.Command("/usr/libexec/PlistBuddy", "-c", command, infoPath).Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(output), "\n") {
			value := strings.TrimSpace(line)
			if value == "" || value == "Array {" || value == "}" || strings.HasSuffix(value, "Dict {") {
				continue
			}
			value = strings.Trim(value, `"'`)
			if value != "" {
				out = append(out, value)
			}
		}
	}
	return uniqueSortedStrings(out)
}

func safeIconCacheKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	key := strings.Trim(b.String(), "-")
	if key == "" {
		return "app"
	}
	return key
}

func resolveEmbeddedToolIconFile(tool string) (string, string, bool) {
	for _, candidate := range embeddedToolIconFiles[tool] {
		info, err := fs.Stat(uiAssets, candidate)
		if err != nil || info.IsDir() {
			continue
		}
		ctype := contentTypeFor(candidate)
		if ctype != "application/octet-stream" {
			return candidate, ctype, true
		}
	}
	return "", "", false
}

func serveLocalToolIcon(w http.ResponseWriter, r *http.Request, filePath, ctype string) {
	f, err := os.Open(filePath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, filepath.Base(filePath), stat.ModTime(), f)
}

func serveEmbeddedToolIcon(w http.ResponseWriter, r *http.Request, assetPath, ctype string) {
	f, err := uiAssets.Open(assetPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if stat, err := fs.Stat(uiAssets, assetPath); err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, f)
}

func cachedPNGForICNS(tool, source string) (string, error) {
	sourceInfo, err := os.Stat(source)
	if err != nil {
		return "", err
	}
	cacheRoot, err := os.UserCacheDir()
	if err != nil || cacheRoot == "" {
		cacheRoot = os.TempDir()
	}
	sum := sha1.Sum([]byte(source))
	targetDir := filepath.Join(cacheRoot, "agentload", "tool-icons")
	target := filepath.Join(targetDir, fmt.Sprintf("%s-%x.png", tool, sum[:8]))
	if targetInfo, err := os.Stat(target); err == nil && !targetInfo.IsDir() && !targetInfo.ModTime().Before(sourceInfo.ModTime()) {
		return target, nil
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", err
	}
	cmd := exec.Command("/usr/bin/sips", "-s", "format", "png", source, "--out", target)
	if output, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(target)
		return "", fmt.Errorf("convert %s: %w: %s", source, err, strings.TrimSpace(string(output)))
	}
	return target, nil
}

func hasDotPathSegment(rawPath string) bool {
	for _, segment := range strings.Split(rawPath, "/") {
		if segment == "." || segment == ".." {
			return true
		}
		decoded, err := url.PathUnescape(segment)
		if err == nil && (decoded == "." || decoded == "..") {
			return true
		}
	}
	return false
}

func listenWithFallback(addr string) (net.Listener, string, error) {
	normalizedAddr, err := normalizeLoopbackListenAddr(addr)
	if err != nil {
		return nil, "", err
	}
	ln, err := net.Listen("tcp", normalizedAddr)
	if err == nil {
		return ln, listenerURL(ln), nil
	}
	if !isAddrInUse(err) {
		return nil, "", err
	}
	host, _, splitErr := net.SplitHostPort(normalizedAddr)
	if splitErr != nil {
		return nil, "", err
	}
	ln, fallbackErr := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if fallbackErr != nil {
		return nil, "", fallbackErr
	}
	return ln, listenerURL(ln), nil
}

func validateLoopbackListenAddr(addr string) error {
	_, err := normalizeLoopbackListenAddr(addr)
	return err
}

func normalizeLoopbackListenAddr(addr string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return "", fmt.Errorf("listen address must be host:port: %w", err)
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		host = "127.0.0.1"
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", fmt.Errorf("listen address must use a loopback IP, got %q", host)
	}
	return net.JoinHostPort(host, port), nil
}

func listenerURL(ln net.Listener) string {
	addr := ln.Addr().String()
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "http://" + addr
	}
	if host == "" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	host = strings.Trim(host, "[]")
	if host == "0.0.0.0" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "address already in use")
}
