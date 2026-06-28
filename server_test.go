package main

import (
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
