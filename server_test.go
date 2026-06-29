package main

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHandleUIAssetServesViteAssets(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()
	matches, err := fs.Glob(uiAssets, "ui/dist/assets/*.js")
	if err != nil {
		t.Fatalf("glob vite assets: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected at least one built vite js asset")
	}
	req := httptest.NewRequest(http.MethodGet, "/assets/"+filepath.Base(matches[0]), nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	contentType := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(contentType, "application/javascript") {
		t.Fatalf("expected javascript content type, got %q", contentType)
	}
	if !strings.Contains(rec.Body.String(), "Agent Load") {
		t.Fatalf("expected built UI asset to contain brand copy")
	}
}

func TestHandleUIAssetRejectsInvalidAssetPaths(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()
	paths := []string{
		"/assets/",
		"/assets/../index.html",
		"/assets/%2e%2e/index.html",
		"/app.js",
		"/locales/en.js",
	}

	for _, requestPath := range paths {
		t.Run(requestPath, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, requestPath, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected status 404 for %s, got %d", requestPath, rec.Code)
			}
		})
	}
}

func TestHandleRefreshAPIReturnsDedupedSlotID(t *testing.T) {
	app := &trayApp{
		cfg:       Config{RefreshInterval: 30 * time.Second},
		refreshCh: make(chan struct{}, 1),
	}
	handler := app.handler()

	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d with body %q", rec.Code, rec.Body.String())
	}
	var first struct {
		OK            bool   `json:"ok"`
		RefreshSlotID string `json:"refresh_slot_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode first response: %v", err)
	}
	if !first.OK || first.RefreshSlotID == "" {
		t.Fatalf("expected ok response with slot id, got %+v", first)
	}

	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202 on duplicate, got %d", rec.Code)
	}
	var second struct {
		RefreshSlotID string `json:"refresh_slot_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second response: %v", err)
	}
	if second.RefreshSlotID != first.RefreshSlotID {
		t.Fatalf("expected duplicate refresh to return same slot id, got %q and %q", first.RefreshSlotID, second.RefreshSlotID)
	}
	if queued := len(app.refreshCh); queued != 1 {
		t.Fatalf("expected duplicate refresh to queue once, got %d", queued)
	}
}

func TestHandleSnapshotAPIReturnsCompactJSONAndRefreshSlotHeader(t *testing.T) {
	app := &trayApp{}
	app.rememberSnapshot(Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
	})
	handler := app.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Refresh-Slot-ID"); got != "30s:2026-06-28T12:00:00Z" {
		t.Fatalf("expected refresh slot header, got %q", got)
	}
	if got := rec.Header().Get("ETag"); got != strconv.Quote("30s:2026-06-28T12:00:00Z") {
		t.Fatalf("expected refresh slot ETag, got %q", got)
	}
	body := rec.Body.String()
	if strings.Contains(body, "\n  \"") {
		t.Fatalf("expected compact JSON without pretty indentation, got %q", body)
	}
	if !strings.Contains(body, `"refresh_slot_id":"30s:2026-06-28T12:00:00Z"`) {
		t.Fatalf("expected compact JSON refresh slot body, got %q", body)
	}
}

func TestHandleSnapshotAPIHonorsRefreshSlotValidators(t *testing.T) {
	app := &trayApp{}
	app.rememberSnapshot(Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
	})
	handler := app.handler()

	req := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	req.Header.Set("If-None-Match", strconv.Quote("30s:2026-06-28T12:00:00Z"))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("expected status 304, got %d with body %q", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("expected not-modified response without body, got %q", rec.Body.String())
	}
	if got := rec.Header().Get("X-Refresh-Slot-ID"); got != "30s:2026-06-28T12:00:00Z" {
		t.Fatalf("expected refresh slot header on 304, got %q", got)
	}

	headReq := httptest.NewRequest(http.MethodHead, "/api/snapshot", nil)
	headRec := httptest.NewRecorder()
	handler.ServeHTTP(headRec, headReq)
	if headRec.Code != http.StatusOK {
		t.Fatalf("expected status 200 for HEAD, got %d", headRec.Code)
	}
	if headRec.Body.Len() != 0 {
		t.Fatalf("expected HEAD response without body, got %q", headRec.Body.String())
	}
	if got := headRec.Header().Get("ETag"); got != strconv.Quote("30s:2026-06-28T12:00:00Z") {
		t.Fatalf("expected HEAD ETag, got %q", got)
	}
}

func TestHandleSnapshotAPIFillsRefreshSlotForCachedSnapshot(t *testing.T) {
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = Snapshot{GeneratedAt: "2026-06-28T12:00:00Z"}
	app.haveSnapshot = true
	handler := app.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got struct {
		RefreshSlotID string `json:"refresh_slot_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if got.RefreshSlotID == "" {
		t.Fatalf("expected cached snapshot response to include refresh_slot_id, body=%q", rec.Body.String())
	}
	if header := rec.Header().Get("X-Refresh-Slot-ID"); header != got.RefreshSlotID {
		t.Fatalf("expected refresh slot header %q, got %q", got.RefreshSlotID, header)
	}
}

func TestHandleSnapshotAPIRedactsConfigPaths(t *testing.T) {
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		Config: SnapshotConfig{
			IdleGapSeconds:       90,
			ClaudeRoots:          []string{filepath.Join("private", "roots", ".claude")},
			CodexRoots:           []string{filepath.Join("private", "roots", ".codex")},
			TraeRoots:            []string{filepath.Join("private", "roots", ".trae", "cli")},
			HistoryFile:          filepath.Join("private", "state", "history.jsonl"),
			ProcessRefreshTarget: 300,
		},
		History: SnapshotHistory{StorePath: filepath.Join("private", "state", "history.jsonl"), LoadedSampleCount: 2},
	}
	app.haveSnapshot = true
	handler := app.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if len(got.Config.ClaudeRoots) != 0 || len(got.Config.CodexRoots) != 0 || len(got.Config.TraeRoots) != 0 {
		t.Fatalf("expected client config roots to be redacted, got %+v", got.Config)
	}
	if got.Config.HistoryFile != "" || got.History.StorePath != "" {
		t.Fatalf("expected client history paths to be redacted, got config=%q history=%q", got.Config.HistoryFile, got.History.StorePath)
	}
	if got.Config.IdleGapSeconds != 90 || got.Config.ProcessRefreshTarget != 300 || got.History.LoadedSampleCount != 2 {
		t.Fatalf("expected non-path metadata to remain, got config=%+v history=%+v", got.Config, got.History)
	}
	if app.lastSnapshot.Config.HistoryFile == "" || len(app.lastSnapshot.Config.CodexRoots) == 0 || app.lastSnapshot.History.StorePath == "" {
		t.Fatalf("expected cached internal snapshot to retain path metadata, got config=%+v history=%+v", app.lastSnapshot.Config, app.lastSnapshot.History)
	}
}

func TestHandleSnapshotAPIRedactsClientEvidencePaths(t *testing.T) {
	root := t.TempDir()
	executablePath := filepath.Join(root, "bin", "codex")
	workspacePath := filepath.Join(root, "workspace", "agentload")
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	bundlePath := filepath.Join(root, "Terminal.app")
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		TranscriptStats: TranscriptStats{
			Errors: []string{sessionPath + ": parse failed"},
		},
		LiveProcesses: []LiveProcessSnapshot{
			{
				PID:            42,
				Tool:           "codex",
				Command:        executablePath + " --cwd=" + workspacePath + " resume " + sessionPath,
				HostApp:        &HostApp{PID: 7, Name: "Terminal", BundlePath: bundlePath},
				SessionIDs:     []string{"session"},
				SessionPaths:   []string{sessionPath},
				MappedSessions: 1,
			},
		},
		LiveSessions: []LiveSessionSnapshot{
			{
				Tool:                      "codex",
				SessionID:                 "session",
				Path:                      sessionPath,
				HostApps:                  []HostApp{{PID: 7, Name: "Terminal", BundlePath: bundlePath}},
				RoleReasons:               []string{sessionPath + ": role metadata"},
				ConfidenceReasons:         []string{"read " + sessionPath},
				ProjectAttributionReasons: []string{"cwd=" + workspacePath},
				Provenance:                []string{"transcript_path"},
			},
		},
		Notes: []string{"checked " + sessionPath},
	}
	app.haveSnapshot = true
	handler := app.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	body := rec.Body.String()
	for _, leaked := range []string{root, executablePath, workspacePath, sessionPath, bundlePath} {
		if strings.Contains(body, leaked) {
			t.Fatalf("expected client snapshot to redact %q, got body %q", leaked, body)
		}
	}
	if len(got.LiveProcesses) != 1 || got.LiveProcesses[0].Command == "" || !strings.Contains(got.LiveProcesses[0].Command, "codex") {
		t.Fatalf("expected sanitized command to keep executable identity, got %+v", got.LiveProcesses)
	}
	if len(got.LiveProcesses[0].SessionPaths) != 0 {
		t.Fatalf("expected client session paths to be removed, got %+v", got.LiveProcesses[0].SessionPaths)
	}
	if got.LiveProcesses[0].HostApp == nil || got.LiveProcesses[0].HostApp.BundlePath != "" || got.LiveProcesses[0].HostApp.Name != "Terminal" {
		t.Fatalf("expected client host app name without bundle path, got %+v", got.LiveProcesses[0].HostApp)
	}
	if len(got.LiveSessions) != 1 || got.LiveSessions[0].Path != "" {
		t.Fatalf("expected client session path to be removed, got %+v", got.LiveSessions)
	}
	if len(got.LiveSessions[0].HostApps) != 1 || got.LiveSessions[0].HostApps[0].BundlePath != "" {
		t.Fatalf("expected client session host bundle path to be removed, got %+v", got.LiveSessions[0].HostApps)
	}
	if app.lastSnapshot.LiveProcesses[0].Command != executablePath+" --cwd="+workspacePath+" resume "+sessionPath ||
		len(app.lastSnapshot.LiveProcesses[0].SessionPaths) != 1 ||
		app.lastSnapshot.LiveProcesses[0].HostApp.BundlePath != bundlePath ||
		app.lastSnapshot.LiveSessions[0].Path != sessionPath ||
		app.lastSnapshot.LiveSessions[0].HostApps[0].BundlePath != bundlePath {
		t.Fatalf("expected internal cached snapshot to retain local evidence paths, got %+v", app.lastSnapshot)
	}
}

func TestHandleSnapshotAPIRedactsFreshObserverConfigPaths(t *testing.T) {
	originalDiscover := discoverLiveProcessesFunc
	discoverLiveProcessesFunc = func(context.Context) ([]LiveProcess, []string) {
		return nil, nil
	}
	t.Cleanup(func() {
		discoverLiveProcessesFunc = originalDiscover
	})

	tmp := t.TempDir()
	claudeRoot := filepath.Join(tmp, "roots", ".claude")
	codexRoot := filepath.Join(tmp, "roots", ".codex")
	traeRoot := filepath.Join(tmp, "roots", ".trae", "cli")
	for _, root := range []string{claudeRoot, codexRoot, traeRoot} {
		if err := os.MkdirAll(root, 0o755); err != nil {
			t.Fatalf("create root %q: %v", root, err)
		}
	}
	historyFile := filepath.Join(tmp, "state", "history.jsonl")
	cfg := Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           time.Hour,
		TranscriptCacheTTL: time.Minute,
		RefreshInterval:    5 * time.Minute,
		HistoryFile:        historyFile,
		ClaudeRoots:        []string{claudeRoot},
		CodexRoots:         []string{codexRoot},
		TraeRoots:          []string{traeRoot},
	}
	app := &trayApp{
		cfg:      cfg,
		observer: newObserver(cfg),
		history:  localHistoryState{path: historyFile},
	}
	handler := app.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if len(got.Config.ClaudeRoots) != 0 || len(got.Config.CodexRoots) != 0 || len(got.Config.TraeRoots) != 0 {
		t.Fatalf("expected fresh client config roots to be redacted, got %+v", got.Config)
	}
	if got.Config.HistoryFile != "" || got.History.StorePath != "" {
		t.Fatalf("expected fresh client history paths to be redacted, got config=%q history=%q", got.Config.HistoryFile, got.History.StorePath)
	}
	if got.Config.IdleGapSeconds != 90 || got.Config.ProcessRefreshTarget != 300 || got.History.LoadedSampleCount != 1 {
		t.Fatalf("expected fresh non-path metadata to remain, got config=%+v history=%+v", got.Config, got.History)
	}
	if !app.haveSnapshot {
		t.Fatalf("expected fresh observer snapshot to be cached internally")
	}
	if app.lastSnapshot.Config.HistoryFile != historyFile || app.lastSnapshot.History.StorePath != historyFile {
		t.Fatalf("expected internal fresh snapshot to retain history paths, got config=%q history=%q", app.lastSnapshot.Config.HistoryFile, app.lastSnapshot.History.StorePath)
	}
	if !stringSliceContains(app.lastSnapshot.Config.ClaudeRoots, claudeRoot) || !stringSliceContains(app.lastSnapshot.Config.CodexRoots, codexRoot) || !stringSliceContains(app.lastSnapshot.Config.TraeRoots, traeRoot) {
		t.Fatalf("expected internal fresh snapshot to retain root paths, got %+v", app.lastSnapshot.Config)
	}
}

func TestFormatTrayMetaTitleIncludesScanCoverage(t *testing.T) {
	got := formatTrayMetaTitle(Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		TranscriptStats: TranscriptStats{
			ScannedFiles:    19,
			ParsedFiles:     11,
			DeferredFiles:   4,
			TailParsedFiles: 3,
			Cached:          true,
		},
	})
	for _, want := range []string{"cache hit", "11/19 transcripts", "4 deferred", "3 tail"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected tray metadata %q to contain %q", got, want)
		}
	}
}

func TestNormalizeToolIconNameAllowlist(t *testing.T) {
	tests := map[string]string{
		"codex":            "codex",
		"codexL":           "codex",
		"com.openai.codex": "codex",
		"traex":            "trae",
		"Trae.app":         "trae",
		"warp":             "karp",
		"WarpOss":          "karp",
		"claude-code":      "claude",
		"../../etc/passwd": "",
		"unknown":          "",
	}
	for raw, want := range tests {
		t.Run(raw, func(t *testing.T) {
			if got := normalizeToolIconName(raw); got != want {
				t.Fatalf("normalizeToolIconName(%q) = %q, want %q", raw, got, want)
			}
		})
	}
}

func stringSliceContains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func TestTraeIconDoesNotFallbackToKarpApp(t *testing.T) {
	for _, candidate := range toolIconFiles["trae"] {
		lower := strings.ToLower(candidate)
		if strings.Contains(lower, "karp") || strings.Contains(lower, "warp") {
			t.Fatalf("trae icon must not reuse Karp/Warp app icon: %s", candidate)
		}
	}
}

func TestHandleToolIconAPIMethodAndPathGuards(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()

	t.Run("rejects post", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/tool-icon/codex", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("rejects traversal", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tool-icon/../codex", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("rejects unknown", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/tool-icon/not-a-tool", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})
}

func TestResolveToolIconFileServesAllowedPNG(t *testing.T) {
	tmp := t.TempDir()
	iconPath := tmp + "/codex.png"
	if err := os.WriteFile(iconPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write icon: %v", err)
	}
	original := toolIconFiles
	toolIconFiles = map[string][]string{"codex": {iconPath}}
	t.Cleanup(func() { toolIconFiles = original })

	gotPath, ctype, ok := resolveToolIconFile("codex")
	if !ok {
		t.Fatalf("expected icon to resolve")
	}
	if gotPath != iconPath {
		t.Fatalf("expected %q, got %q", iconPath, gotPath)
	}
	if ctype != "image/png" {
		t.Fatalf("expected image/png, got %q", ctype)
	}
}

func TestHandleToolIconAPIFallsBackToEmbeddedSVG(t *testing.T) {
	original := toolIconFiles
	toolIconFiles = map[string][]string{}
	t.Cleanup(func() { toolIconFiles = original })

	app := &trayApp{}
	handler := app.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/tool-icon/trae", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "image/svg+xml") {
		t.Fatalf("expected svg content type, got %q", got)
	}
	if !strings.Contains(rec.Body.String(), "<svg") {
		t.Fatalf("expected embedded svg body")
	}
}

func TestHandleToolIconAPIPrefersEmbeddedCodexCLIIcon(t *testing.T) {
	tmp := t.TempDir()
	iconPath := filepath.Join(tmp, "codex-app.png")
	if err := os.WriteFile(iconPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write icon: %v", err)
	}
	original := toolIconFiles
	toolIconFiles = map[string][]string{"codex": {iconPath}}
	t.Cleanup(func() { toolIconFiles = original })

	app := &trayApp{}
	handler := app.handler()
	req := httptest.NewRequest(http.MethodGet, "/api/tool-icon/codex", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "image/svg+xml") {
		t.Fatalf("expected embedded svg content type, got %q", got)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<svg") || !strings.Contains(body, "#F7F7F7") {
		t.Fatalf("expected embedded OpenAI-style Codex CLI svg body, got %q", body)
	}
}

func TestResolveHostAppIconFileUsesObservedBundleResources(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Example Host.app")
	resources := filepath.Join(bundlePath, "Contents", "Resources")
	if err := os.MkdirAll(resources, 0o755); err != nil {
		t.Fatalf("mkdir resources: %v", err)
	}
	iconPath := filepath.Join(resources, "AppIcon.png")
	if err := os.WriteFile(iconPath, []byte("png"), 0o644); err != nil {
		t.Fatalf("write icon: %v", err)
	}

	gotPath, ctype, ok := resolveHostAppIconFile(HostApp{
		PID:        321,
		Name:       "Example Host",
		BundlePath: bundlePath,
	})
	if !ok {
		t.Fatalf("expected host icon to resolve")
	}
	if gotPath != iconPath {
		t.Fatalf("expected %q, got %q", iconPath, gotPath)
	}
	if ctype != "image/png" {
		t.Fatalf("expected image/png, got %q", ctype)
	}
}

func TestObservedHostAppFromRequestRequiresCachedSnapshotEvidence(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Terminal.app")
	if err := os.MkdirAll(bundlePath, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	app := &trayApp{}
	app.rememberSnapshot(Snapshot{
		LiveProcesses: []LiveProcessSnapshot{
			{
				PID: 42,
				HostApp: &HostApp{
					PID:        42,
					Name:       "Terminal",
					BundlePath: bundlePath,
				},
			},
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/host-app-icon/42", nil)
	got, ok := app.observedHostAppFromRequest(req, "/api/host-app-icon/")
	if !ok {
		t.Fatalf("expected observed host app")
	}
	if got.Name != "Terminal" || got.BundlePath != bundlePath {
		t.Fatalf("unexpected host app: %#v", got)
	}

	missingReq := httptest.NewRequest(http.MethodGet, "/api/host-app-icon/43", nil)
	if _, ok := app.observedHostAppFromRequest(missingReq, "/api/host-app-icon/"); ok {
		t.Fatalf("expected unknown pid to be rejected")
	}
}

func TestObservedHostAppFromRequestUsesInternalFreshSnapshot(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Terminal.app")
	if err := os.MkdirAll(bundlePath, 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	originalDiscover := discoverLiveProcessesFunc
	discoverLiveProcessesFunc = func(context.Context) ([]LiveProcess, []string) {
		return []LiveProcess{
			{
				PID:     42,
				Tool:    "codex",
				Command: "codex resume",
				HostApp: &HostApp{
					PID:        42,
					Name:       "Terminal",
					BundlePath: bundlePath,
				},
			},
		}, nil
	}
	t.Cleanup(func() {
		discoverLiveProcessesFunc = originalDiscover
	})
	cfg := Config{
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           time.Hour,
		TranscriptCacheTTL: time.Minute,
		RefreshInterval:    5 * time.Minute,
		HistoryFile:        filepath.Join(root, "history.jsonl"),
	}
	app := &trayApp{
		cfg:      cfg,
		observer: newObserver(cfg),
		history:  localHistoryState{path: cfg.HistoryFile},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/host-app-icon/42", nil)
	got, ok := app.observedHostAppFromRequest(req, "/api/host-app-icon/")
	if !ok {
		t.Fatalf("expected observed host app from fresh internal snapshot")
	}
	if got.Name != "Terminal" || got.BundlePath != bundlePath {
		t.Fatalf("unexpected host app: %#v", got)
	}
	if !app.haveSnapshot {
		t.Fatalf("expected fresh internal snapshot to be cached")
	}
	if app.lastSnapshot.LiveProcesses[0].HostApp == nil || app.lastSnapshot.LiveProcesses[0].HostApp.BundlePath != bundlePath {
		t.Fatalf("expected cached internal snapshot to retain bundle path, got %+v", app.lastSnapshot.LiveProcesses)
	}
	clientSnapshot := app.snapshotForClient(context.Background())
	if clientSnapshot.LiveProcesses[0].HostApp == nil || clientSnapshot.LiveProcesses[0].HostApp.BundlePath != "" {
		t.Fatalf("expected client snapshot to redact bundle path, got %+v", clientSnapshot.LiveProcesses)
	}
}

func TestHandleHostAppIconAPIGuardsMethodAndPath(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()

	req := httptest.NewRequest(http.MethodPost, "/api/host-app-icon/42", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/host-app-icon/../42", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleOpenHostAppAPIGuardsMethodAndObservedEvidence(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()

	req := httptest.NewRequest(http.MethodGet, "/api/open-host-app/42", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/open-host-app/42", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected unobserved pid to be rejected with 404, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/open-host-app/../42", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected traversal path to be rejected with 404, got %d", rec.Code)
	}
}
