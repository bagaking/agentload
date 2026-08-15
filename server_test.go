package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"agentload/internal/httpencoding"
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
	slices.Sort(matches)
	foundBrandCopy := false
	for index, match := range matches {
		req := newLoopbackRequest(http.MethodGet, "/assets/"+filepath.Base(match), nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200 for %s, got %d with body %q", match, rec.Code, rec.Body.String())
		}
		contentType := rec.Header().Get("Content-Type")
		if index == 0 && !strings.HasPrefix(contentType, "application/javascript") {
			t.Fatalf("expected javascript content type, got %q", contentType)
		}
		if strings.Contains(rec.Body.String(), "Agent Load") {
			foundBrandCopy = true
		}
	}
	if !foundBrandCopy {
		t.Fatalf("expected at least one built UI chunk to contain brand copy")
	}
}

func TestHandleCapabilitiesAPIUsesRegistryEvidence(t *testing.T) {
	root := t.TempDir()
	observer := newObserver(Config{ClaudeRoots: []string{root}})
	app := &trayApp{observer: observer}
	req := newLoopbackRequest(http.MethodGet, "/api/capabilities", nil)
	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var payload struct {
		Families []string                       `json:"families"`
		Rows     []snapshot.CapabilityMatrixRow `json:"rows"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode capabilities: %v", err)
	}
	if len(payload.Families) != 7 || len(payload.Rows) == 0 {
		t.Fatalf("capability payload = %+v", payload)
	}
	// Compare the names, not just the count. Two length-7 lists stay length-7
	// through a rename, so counting alone lets the endpoint publish a family key
	// the registry no longer emits.
	for _, row := range payload.Rows {
		if len(row.SignalFamilies) != 7 {
			t.Fatalf("row %q has %d signal families", row.Agent, len(row.SignalFamilies))
		}
		for i, family := range row.SignalFamilies {
			if payload.Families[i] != family.Key {
				t.Fatalf("row %q family %d is %q but the endpoint advertises %q", row.Agent, i, family.Key, payload.Families[i])
			}
		}
	}
	head := newLoopbackRequest(http.MethodHead, "/api/capabilities", nil)
	headRec := httptest.NewRecorder()
	app.handler().ServeHTTP(headRec, head)
	if headRec.Code != http.StatusOK || headRec.Body.Len() != 0 {
		t.Fatalf("HEAD response = status %d body %q", headRec.Code, headRec.Body.String())
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
			req := newLoopbackRequest(http.MethodGet, requestPath, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("expected status 404 for %s, got %d", requestPath, rec.Code)
			}
		})
	}
}

func TestHandleLiveTokenRateAPIReturnsPublishedSample(t *testing.T) {
	now := time.Now().UTC()
	sampler := newTestLiveTokenRateSampler(Config{ClaudeRoots: []string{t.TempDir()}})
	sampler.publishedMu.Lock()
	sampler.published = liveTokenRatePublished{
		Configured:   true,
		Initialized:  true,
		LatestSignal: now,
		LatestEvent:  now,
		Buckets:      []liveTokenRateEvent{{At: now, Tokens: 300, Session: "session-a"}},
		Projects:     map[string]string{"session-a": "project-a"},
	}
	sampler.publishedMu.Unlock()
	app := &trayApp{liveTokenRate: sampler}
	handler := app.handler()

	req := newLoopbackRequest(http.MethodGet, "/api/live-token-rate", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("cache control = %q, want no-store", got)
	}
	var sample snapshot.LiveTokenRateSample
	if err := json.Unmarshal(rec.Body.Bytes(), &sample); err != nil {
		t.Fatalf("decode live token rate: %v", err)
	}
	if sample.State != liveTokenRateStateLive || sample.OutputTokensPerSecond == nil || *sample.OutputTokensPerSecond != 1 {
		t.Fatalf("live token rate sample = %+v", sample)
	}
	if sample.Basis != liveTokenRateBasis || sample.WindowSeconds != 300 || sample.ActiveSessions != 1 {
		t.Fatalf("live token rate metadata = %+v", sample)
	}
	if len(sample.Projects) != 1 || sample.Projects[0].Project != "project-a" || sample.Projects[0].OutputTokensPerSecond != 1 || sample.Projects[0].ActiveSessions != 1 {
		t.Fatalf("project live token rate = %+v", sample.Projects)
	}

	head := newLoopbackRequest(http.MethodHead, "/api/live-token-rate", nil)
	headRec := httptest.NewRecorder()
	handler.ServeHTTP(headRec, head)
	if headRec.Code != http.StatusOK || headRec.Body.Len() != 0 {
		t.Fatalf("HEAD response = status %d body %q", headRec.Code, headRec.Body.String())
	}

	post := newLoopbackRequest(http.MethodPost, "/api/live-token-rate", nil)
	postRec := httptest.NewRecorder()
	handler.ServeHTTP(postRec, post)
	if postRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d, want 405", postRec.Code)
	}
}

func TestHandleLiveTokenRateAPIWithoutSamplerIsUnavailable(t *testing.T) {
	app := &trayApp{}
	req := newLoopbackRequest(http.MethodGet, "/api/live-token-rate", nil)
	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	var sample snapshot.LiveTokenRateSample
	if err := json.Unmarshal(rec.Body.Bytes(), &sample); err != nil {
		t.Fatal(err)
	}
	if sample.State != liveTokenRateStateUnavailable || sample.OutputTokensPerSecond != nil {
		t.Fatalf("missing sampler sample = %+v", sample)
	}
}

func TestHandleRefreshAPIReturnsDedupedSlotID(t *testing.T) {
	app := &trayApp{
		cfg:       Config{RefreshInterval: 30 * time.Second},
		refreshCh: make(chan struct{}, 1),
	}
	handler := app.handler()

	req := newLoopbackRequest(http.MethodPost, "/api/refresh", nil)
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

func TestHandleRefreshAPIUsesRequestedIntervalSlot(t *testing.T) {
	app := &trayApp{
		cfg:       Config{RefreshInterval: 5 * time.Minute},
		refreshCh: make(chan struct{}, 1),
	}
	handler := app.handler()

	req := newLoopbackRequest(http.MethodPost, "/api/refresh?interval_ms=30000", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected status 202, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got struct {
		RefreshSlotID string `json:"refresh_slot_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !strings.HasPrefix(got.RefreshSlotID, "30s:") {
		t.Fatalf("expected requested 30s refresh slot, got %q", got.RefreshSlotID)
	}
	if strings.HasPrefix(got.RefreshSlotID, "300s:") {
		t.Fatalf("expected requested interval to override app default slot, got %q", got.RefreshSlotID)
	}
	if queued := len(app.refreshCh); queued != 1 {
		t.Fatalf("expected requested interval refresh to queue once, got %d", queued)
	}
}

func TestHandleRefreshAPINormalizesRequestedIntervalSlot(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantPrefix string
	}{
		{name: "floors subminimum interval", query: "?interval_ms=5000", wantPrefix: "30s:"},
		{name: "paused interval uses default cadence", query: "?interval_ms=0", wantPrefix: "300s:"},
		{name: "invalid interval uses default cadence", query: "?interval_ms=not-a-number", wantPrefix: "300s:"},
		{name: "negative interval uses default cadence", query: "?interval_ms=-1", wantPrefix: "300s:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &trayApp{
				cfg:       Config{RefreshInterval: 5 * time.Minute},
				refreshCh: make(chan struct{}, 1),
			}
			handler := app.handler()

			req := newLoopbackRequest(http.MethodPost, "/api/refresh"+tt.query, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusAccepted {
				t.Fatalf("expected status 202, got %d with body %q", rec.Code, rec.Body.String())
			}
			var got struct {
				RefreshSlotID string `json:"refresh_slot_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if !strings.HasPrefix(got.RefreshSlotID, tt.wantPrefix) {
				t.Fatalf("expected refresh slot prefix %q, got %q", tt.wantPrefix, got.RefreshSlotID)
			}
			if queued := len(app.refreshCh); queued != 1 {
				t.Fatalf("expected refresh to queue once, got %d", queued)
			}
		})
	}
}

func TestHandleSnapshotAPIReturnsCompactJSONAndRefreshSlotHeader(t *testing.T) {
	currentRate := 2.5
	app := &trayApp{}
	app.lastSnapshot = snapshot.Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
		ThroughputTrends: snapshot.TrendSet{Windows: []snapshot.TrendWindow{{
			Range: "1D",
			ThroughputSeries: []snapshot.ThroughputTrendSeries{{
				Key:           "minute:300",
				Kind:          throughputSeriesKindMinuteRollup,
				WindowSeconds: 300,
				Summary: &snapshot.ThroughputTrendSummary{
					Max:           4,
					P95:           3.5,
					Avg:           1.25,
					Current:       &currentRate,
					CurrentAt:     "2026-06-28T12:00:00Z",
					WindowSeconds: 300,
					SampleCount:   42,
				},
			}},
		}}},
	}
	app.haveSnapshot = true
	handler := app.handler()
	req := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
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
	if !strings.Contains(body, `"throughput_series":[{"key":"minute:300","kind":"minute_rollup","window_seconds":300,"granularity_seconds":0,"history_complete":false,"summary":{"max":4,"p95":3.5,"avg":1.25,"current":2.5,"current_at":"2026-06-28T12:00:00Z","window_seconds":300,"sample_count":42},"points":null}]`) {
		t.Fatalf("expected throughput period summary in snapshot JSON, got %q", body)
	}
}

func TestHandleSnapshotAPIHonorsRefreshSlotValidators(t *testing.T) {
	app := &trayApp{}
	app.rememberSnapshot(snapshot.Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
	})
	handler := app.handler()

	req := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
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

	headReq := newLoopbackRequest(http.MethodHead, "/api/snapshot", nil)
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

func TestHandleSnapshotAPIHonorsETagLists(t *testing.T) {
	app := &trayApp{}
	app.rememberSnapshot(snapshot.Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
	})
	handler := app.handler()

	tests := []struct {
		name          string
		ifNoneMatch   string
		wantStatus    int
		wantEmptyBody bool
	}{
		{
			name:          "matches one validator in list",
			ifNoneMatch:   `"stale", "30s:2026-06-28T12:00:00Z"`,
			wantStatus:    http.StatusNotModified,
			wantEmptyBody: true,
		},
		{
			name:          "wildcard validator matches",
			ifNoneMatch:   "*",
			wantStatus:    http.StatusNotModified,
			wantEmptyBody: true,
		},
		{
			name:        "stale validator returns snapshot",
			ifNoneMatch: `"stale"`,
			wantStatus:  http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
			req.Header.Set("If-None-Match", tt.ifNoneMatch)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("expected status %d, got %d with body %q", tt.wantStatus, rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("X-Refresh-Slot-ID"); got != "30s:2026-06-28T12:00:00Z" {
				t.Fatalf("expected refresh slot header on validator response, got %q", got)
			}
			if tt.wantEmptyBody && rec.Body.Len() != 0 {
				t.Fatalf("expected empty body, got %q", rec.Body.String())
			}
			if !tt.wantEmptyBody && !strings.Contains(rec.Body.String(), `"refresh_slot_id":"30s:2026-06-28T12:00:00Z"`) {
				t.Fatalf("expected snapshot body with refresh slot, got %q", rec.Body.String())
			}
		})
	}
}

func TestHandleSnapshotAPIFillsRefreshSlotForCachedSnapshot(t *testing.T) {
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = snapshot.Snapshot{GeneratedAt: "2026-06-28T12:00:00Z"}
	app.haveSnapshot = true
	handler := app.handler()
	req := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
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

func TestHandleSnapshotAPINotModifiedSkipsSanitizeAndCachesPayload(t *testing.T) {
	app := &trayApp{}
	app.rememberSnapshot(snapshot.Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
	})
	handler := app.handler()
	base := snapshotSanitizePasses.Load()

	condReq := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	condReq.Header.Set("If-None-Match", strconv.Quote("30s:2026-06-28T12:00:00Z"))
	condRec := httptest.NewRecorder()
	handler.ServeHTTP(condRec, condReq)
	if condRec.Code != http.StatusNotModified {
		t.Fatalf("expected status 304, got %d with body %q", condRec.Code, condRec.Body.String())
	}
	if got := snapshotSanitizePasses.Load(); got != base {
		t.Fatalf("expected 304 path to skip sanitize, got %d extra passes", got-base)
	}

	firstReq := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	firstRec := httptest.NewRecorder()
	handler.ServeHTTP(firstRec, firstReq)
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", firstRec.Code, firstRec.Body.String())
	}
	if got := snapshotSanitizePasses.Load(); got != base+1 {
		t.Fatalf("expected one sanitize pass for first GET, got %d", got-base)
	}

	secondReq := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	secondRec := httptest.NewRecorder()
	handler.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", secondRec.Code, secondRec.Body.String())
	}
	if got := snapshotSanitizePasses.Load(); got != base+1 {
		t.Fatalf("expected repeat GET within slot to reuse cached payload, got %d passes", got-base)
	}
	if firstRec.Body.String() != secondRec.Body.String() {
		t.Fatalf("expected identical cached payloads, got %q and %q", firstRec.Body.String(), secondRec.Body.String())
	}
}

func TestHandleSnapshotAPIInvalidatesClientCacheOnSlotChange(t *testing.T) {
	app := &trayApp{}
	app.rememberSnapshot(snapshot.Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
	})
	handler := app.handler()

	firstRec := httptest.NewRecorder()
	handler.ServeHTTP(firstRec, newLoopbackRequest(http.MethodGet, "/api/snapshot", nil))
	if firstRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", firstRec.Code)
	}

	app.rememberSnapshot(snapshot.Snapshot{
		GeneratedAt:   "2026-06-28T12:00:30Z",
		RefreshSlotID: "30s:2026-06-28T12:00:30Z",
	})
	secondRec := httptest.NewRecorder()
	handler.ServeHTTP(secondRec, newLoopbackRequest(http.MethodGet, "/api/snapshot", nil))
	if secondRec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", secondRec.Code)
	}
	if !strings.Contains(secondRec.Body.String(), `"refresh_slot_id":"30s:2026-06-28T12:00:30Z"`) {
		t.Fatalf("expected new slot payload after cache invalidation, got %q", secondRec.Body.String())
	}
}

func TestHandleSnapshotAPIServesCachedGzipRepresentation(t *testing.T) {
	app := &trayApp{}
	app.rememberSnapshot(snapshot.Snapshot{
		GeneratedAt:   "2026-06-28T12:00:00Z",
		RefreshSlotID: "30s:2026-06-28T12:00:00Z",
		Notes:         []string{strings.Repeat("compressible snapshot evidence ", 200)},
	})
	handler := app.handler()

	identityRec := httptest.NewRecorder()
	handler.ServeHTTP(identityRec, newLoopbackRequest(http.MethodGet, "/api/snapshot", nil))
	gzipReq := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	gzipReq.Header.Set("Accept-Encoding", "gzip")
	gzipRec := httptest.NewRecorder()
	handler.ServeHTTP(gzipRec, gzipReq)

	if got := gzipRec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("content encoding = %q, want gzip", got)
	}
	if got := gzipRec.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("vary = %q, want Accept-Encoding", got)
	}
	if gzipRec.Body.Len() >= identityRec.Body.Len() {
		t.Fatalf("gzip payload %d bytes should be smaller than identity payload %d", gzipRec.Body.Len(), identityRec.Body.Len())
	}
	gzipPayload := append([]byte(nil), gzipRec.Body.Bytes()...)
	reader, err := gzip.NewReader(bytes.NewReader(gzipPayload))
	if err != nil {
		t.Fatalf("open gzip response: %v", err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read gzip response: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("close gzip response: %v", err)
	}
	if string(decoded) != identityRec.Body.String() {
		t.Fatal("gzip representation decoded to different snapshot JSON")
	}

	secondReq := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	secondReq.Header.Set("Accept-Encoding", "gzip")
	secondRec := httptest.NewRecorder()
	handler.ServeHTTP(secondRec, secondReq)
	if secondRec.Body.String() != string(gzipPayload) {
		t.Fatal("same refresh slot did not reuse stable gzip payload")
	}

	headReq := newLoopbackRequest(http.MethodHead, "/api/snapshot", nil)
	headReq.Header.Set("Accept-Encoding", "gzip")
	headRec := httptest.NewRecorder()
	handler.ServeHTTP(headRec, headReq)
	if headRec.Body.Len() != 0 || headRec.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("gzip HEAD response = encoding %q body %d", headRec.Header().Get("Content-Encoding"), headRec.Body.Len())
	}
}

func TestAcceptsGzipEncodingHonorsDisabledQuality(t *testing.T) {
	if httpencoding.AcceptsGzip("gzip;q=0") {
		t.Fatal("gzip;q=0 must disable gzip encoding")
	}
	if httpencoding.AcceptsGzip("gzip;q=0.0") {
		t.Fatal("gzip;q=0.0 must disable gzip encoding")
	}
	if !httpencoding.AcceptsGzip("br, gzip; q=1") {
		t.Fatal("gzip with positive quality should be accepted")
	}
}

func TestStateChangingPostsRejectCrossOriginRequests(t *testing.T) {
	tests := []struct {
		name     string
		origin   string
		referer  string
		wantCode int
	}{
		{name: "absent origin allowed", wantCode: http.StatusAccepted},
		{name: "same origin allowed", origin: "http://127.0.0.1:8123", wantCode: http.StatusAccepted},
		{name: "localhost equivalent allowed", origin: "http://localhost:8123", wantCode: http.StatusAccepted},
		{name: "same origin referer allowed", referer: "http://127.0.0.1:8123/dashboard", wantCode: http.StatusAccepted},
		{name: "cross host blocked", origin: "http://evil.example:8123", wantCode: http.StatusForbidden},
		{name: "cross port blocked", origin: "http://127.0.0.1:9999", wantCode: http.StatusForbidden},
		{name: "cross host referer blocked", referer: "http://evil.example/page", wantCode: http.StatusForbidden},
		{name: "null origin blocked", origin: "null", wantCode: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &trayApp{
				cfg:       Config{RefreshInterval: 30 * time.Second},
				refreshCh: make(chan struct{}, 1),
			}
			handler := app.handler()
			req := newLoopbackRequest(http.MethodPost, "/api/refresh", nil)
			req.Host = "127.0.0.1:8123"
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.referer != "" {
				req.Header.Set("Referer", tt.referer)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("expected status %d, got %d with body %q", tt.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestHandleQuitAPIRejectsCrossOrigin(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()
	req := newLoopbackRequest(http.MethodPost, "/api/quit", nil)
	req.Host = "127.0.0.1:8123"
	req.Header.Set("Origin", "http://evil.example")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rec.Code)
	}
}

func TestHandleOpenHostAppAPIRejectsCrossOrigin(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()

	req := newLoopbackRequest(http.MethodPost, "/api/open-host-app/42", nil)
	req.Host = "127.0.0.1:8123"
	req.Header.Set("Origin", "http://evil.example:8123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected status 403, got %d", rec.Code)
	}

	sameReq := newLoopbackRequest(http.MethodPost, "/api/open-host-app/42", nil)
	sameReq.Host = "127.0.0.1:8123"
	sameReq.Header.Set("Origin", "http://127.0.0.1:8123")
	sameRec := httptest.NewRecorder()
	handler.ServeHTTP(sameRec, sameReq)
	if sameRec.Code != http.StatusNotFound {
		t.Fatalf("expected same-origin request to pass origin check with 404, got %d", sameRec.Code)
	}
}

func TestHandleOpenHostAppAPIRedactsOpenFailure(t *testing.T) {
	root := t.TempDir()
	bundlePath := filepath.Join(root, "Terminal.app")
	if err := os.Mkdir(bundlePath, 0o755); err != nil {
		t.Fatalf("mkdir app bundle: %v", err)
	}
	app := &trayApp{}
	app.lastSnapshot = snapshot.Snapshot{
		LiveProcesses: []snapshot.LiveProcessSnapshot{{
			PID:     42,
			HostApp: &snapshot.HostApp{PID: 7, Name: "Terminal", BundlePath: bundlePath},
		}},
	}
	app.haveSnapshot = true
	app.openHostAppFunc = func(_ context.Context, gotPath string) ([]byte, error) {
		if gotPath != bundlePath {
			t.Fatalf("open path=%q, want %q", gotPath, bundlePath)
		}
		return []byte("open failed for " + bundlePath + " with private detail"), io.ErrUnexpectedEOF
	}

	req := newLoopbackRequest(http.MethodPost, "/api/open-host-app/7", nil)
	rec := httptest.NewRecorder()
	app.handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected status 502, got %d with body %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, bundlePath) || strings.Contains(body, "private detail") {
		t.Fatalf("open failure leaked subprocess output: %q", body)
	}
	if !strings.Contains(body, "failed to open observed host app") {
		t.Fatalf("expected stable open failure, got %q", body)
	}
}

func TestHandleSnapshotAPIRedactsConfigPaths(t *testing.T) {
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = snapshot.Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		LiveTokenRateFiles: []snapshot.TranscriptFile{{
			Tool: "codex",
			Path: filepath.Join("private", "roots", ".codex", "sessions", "active.jsonl"),
		}},
		LiveTokenProjects: map[string]string{
			"codex\x00" + filepath.Join("private", "roots", ".codex", "sessions", "active.jsonl"): "private-project",
		},
		Config: snapshot.SnapshotConfig{
			IdleGapSeconds:       90,
			ClaudeRoots:          []string{filepath.Join("private", "roots", ".claude")},
			CodexRoots:           []string{filepath.Join("private", "roots", ".codex")},
			TraeRoots:            []string{filepath.Join("private", "roots", ".trae", "cli")},
			HistoryFile:          filepath.Join("private", "state", "history.jsonl"),
			ProcessRefreshTarget: 300,
		},
		History: snapshot.SnapshotHistory{
			StorePath: filepath.Join("private", "state", "history.jsonl"), LoadedSampleCount: 2,
			Throughput: &snapshot.SnapshotThroughputHistory{StorePath: filepath.Join("private", "state", "throughput.jsonl"), MinuteFactCount: 12},
		},
	}
	app.haveSnapshot = true
	handler := app.handler()
	req := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got snapshot.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	if len(got.Config.ClaudeRoots) != 0 || len(got.Config.CodexRoots) != 0 || len(got.Config.TraeRoots) != 0 {
		t.Fatalf("expected client config roots to be redacted, got %+v", got.Config)
	}
	if got.Config.HistoryFile != "" || got.History.StorePath != "" {
		t.Fatalf("expected client history paths to be redacted, got config=%q history=%q", got.Config.HistoryFile, got.History.StorePath)
	}
	if got.History.Throughput == nil || got.History.Throughput.StorePath != "" || got.History.Throughput.MinuteFactCount != 12 {
		t.Fatalf("expected throughput history path to be redacted without losing metadata, got %+v", got.History.Throughput)
	}
	if got.Config.IdleGapSeconds != 90 || got.Config.ProcessRefreshTarget != 300 || got.History.LoadedSampleCount != 2 {
		t.Fatalf("expected non-path metadata to remain, got config=%+v history=%+v", got.Config, got.History)
	}
	if app.lastSnapshot.Config.HistoryFile == "" || len(app.lastSnapshot.Config.CodexRoots) == 0 || app.lastSnapshot.History.StorePath == "" || app.lastSnapshot.History.Throughput.StorePath == "" {
		t.Fatalf("expected cached internal snapshot to retain path metadata, got config=%+v history=%+v", app.lastSnapshot.Config, app.lastSnapshot.History)
	}
	if strings.Contains(rec.Body.String(), "active.jsonl") {
		t.Fatalf("expected live token priority paths to remain private, got %q", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private-project") {
		t.Fatalf("expected private live token project mapping to stay out of snapshot JSON, got %q", rec.Body.String())
	}
}

func TestHandleSnapshotAPIRedactsClientEvidencePaths(t *testing.T) {
	root := t.TempDir()
	executablePath := filepath.Join(root, "bin", "codex")
	workspacePath := filepath.Join(root, "workspace", "agentload")
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	bundlePath := filepath.Join(root, "Terminal.app")
	projectPath := filepath.Join(root, "projects", "agentload")
	sessionFileURI := (&url.URL{Scheme: "file", Path: sessionPath}).String()
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = snapshot.Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		TranscriptStats: snapshot.TranscriptStats{
			Errors: []string{sessionPath + ": parse failed", "opened " + sessionFileURI},
		},
		ProcessStats: snapshot.ProcessObservationStats{Incomplete: true, LastKnown: true, Error: "ps failed near " + sessionPath},
		LiveProcesses: []snapshot.LiveProcessSnapshot{
			{
				PID:            42,
				Tool:           "codex",
				Command:        executablePath + " --cwd=" + workspacePath + " resume " + sessionPath + " --source=" + sessionFileURI,
				HostApp:        &snapshot.HostApp{PID: 7, Name: "Terminal", BundlePath: bundlePath},
				SessionIDs:     []string{"session"},
				SessionPaths:   []string{sessionPath},
				MappedSessions: 1,
			},
		},
		LiveSessions: []snapshot.LiveSessionSnapshot{
			{
				Tool:                      "codex",
				SessionID:                 "session",
				Project:                   projectPath,
				Path:                      sessionPath,
				HostApps:                  []snapshot.HostApp{{PID: 7, Name: "Terminal", BundlePath: bundlePath}},
				RoleReasons:               []string{sessionPath + ": role metadata"},
				ConfidenceReasons:         []string{"read " + sessionPath},
				ProjectAttributionReasons: []string{"cwd=" + workspacePath},
				Provenance:                []string{"transcript_path"},
			},
		},
		ProjectFocus: []snapshot.ProjectSnapshot{
			{
				Project:                   projectPath,
				ConfidenceReasons:         []string{"read " + sessionFileURI},
				ProjectAttributionReasons: []string{"cwd=" + workspacePath},
			},
		},
		CandidateWorkitems: []snapshot.CandidateWorkitemSnapshot{
			{
				Key:                       "project=" + projectPath + "|tool=codex|freshness=active",
				Project:                   projectPath,
				Tool:                      "codex",
				ConfidenceReasons:         []string{"group includes " + sessionFileURI},
				ProjectAttributionReasons: []string{"cwd=" + workspacePath},
			},
		},
		CoordinationRisk: snapshot.CoordinationRiskSnapshot{
			TopProject: projectPath,
			Signals: []snapshot.RiskSignalSnapshot{
				{Kind: "evidence_note", Severity: "observed", Evidence: "checked " + sessionPath},
			},
		},
		ThroughputTrends: snapshot.TrendSet{Windows: []snapshot.TrendWindow{{
			Range: "1D",
			ThroughputSeries: []snapshot.ThroughputTrendSeries{{
				Key:           "minute:300",
				Kind:          throughputSeriesKindMinuteRollup,
				WindowSeconds: 300,
				Points: []snapshot.TrendPoint{{
					At:                       "2026-06-28T12:00:00Z",
					OutputTokensPerSecond:    2,
					HasOutputTokensPerSecond: true,
					OutputTokenProjects: []snapshot.LiveTokenRateProjectSample{{
						Project:               projectPath,
						OutputTokensPerSecond: 2,
						ActiveSessions:        1,
					}},
					ThroughputSampled: true,
				}},
			}},
		}}},
		Notes: []string{"checked " + sessionPath, "opened " + sessionFileURI},
	}
	app.haveSnapshot = true
	handler := app.handler()
	req := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got snapshot.Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode snapshot response: %v", err)
	}
	body := rec.Body.String()
	for _, leaked := range []string{root, executablePath, workspacePath, sessionPath, sessionFileURI, bundlePath, projectPath} {
		if strings.Contains(body, leaked) {
			t.Fatalf("expected client snapshot to redact %q, got body %q", leaked, body)
		}
	}
	if len(got.LiveProcesses) != 1 || got.LiveProcesses[0].Command == "" || !strings.Contains(got.LiveProcesses[0].Command, "codex") {
		t.Fatalf("expected sanitized command to keep executable identity, got %+v", got.LiveProcesses)
	}
	if !got.ProcessStats.Incomplete || !got.ProcessStats.LastKnown || !strings.Contains(got.ProcessStats.Error, "session.jsonl") {
		t.Fatalf("expected sanitized process coverage state, got %+v", got.ProcessStats)
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
	if got.LiveSessions[0].Project != "agentload" ||
		got.ProjectFocus[0].Project != "agentload" ||
		got.CandidateWorkitems[0].Project != "agentload" ||
		got.CandidateWorkitems[0].Key != "project=agentload|tool=codex|freshness=active" ||
		got.CoordinationRisk.TopProject != "agentload" ||
		got.ThroughputTrends.Windows[0].ThroughputSeries[0].Points[0].OutputTokenProjects[0].Project != "agentload" {
		t.Fatalf("expected client project labels to be path-safe, got sessions=%+v projects=%+v candidates=%+v risk=%+v", got.LiveSessions, got.ProjectFocus, got.CandidateWorkitems, got.CoordinationRisk)
	}
	if len(got.LiveSessions[0].HostApps) != 1 || got.LiveSessions[0].HostApps[0].BundlePath != "" {
		t.Fatalf("expected client session host bundle path to be removed, got %+v", got.LiveSessions[0].HostApps)
	}
	if app.lastSnapshot.LiveProcesses[0].Command != executablePath+" --cwd="+workspacePath+" resume "+sessionPath+" --source="+sessionFileURI ||
		len(app.lastSnapshot.LiveProcesses[0].SessionPaths) != 1 ||
		app.lastSnapshot.LiveProcesses[0].HostApp.BundlePath != bundlePath ||
		app.lastSnapshot.LiveSessions[0].Path != sessionPath ||
		app.lastSnapshot.LiveSessions[0].HostApps[0].BundlePath != bundlePath {
		t.Fatalf("expected internal cached snapshot to retain local evidence paths, got %+v", app.lastSnapshot)
	}
	if app.lastSnapshot.LiveSessions[0].Project != projectPath ||
		app.lastSnapshot.ProjectFocus[0].Project != projectPath ||
		app.lastSnapshot.CandidateWorkitems[0].Project != projectPath ||
		app.lastSnapshot.CandidateWorkitems[0].Key != "project="+projectPath+"|tool=codex|freshness=active" ||
		app.lastSnapshot.CoordinationRisk.TopProject != projectPath ||
		app.lastSnapshot.ThroughputTrends.Windows[0].ThroughputSeries[0].Points[0].OutputTokenProjects[0].Project != projectPath {
		t.Fatalf("expected internal project labels to retain local paths, got sessions=%+v projects=%+v candidates=%+v risk=%+v", app.lastSnapshot.LiveSessions, app.lastSnapshot.ProjectFocus, app.lastSnapshot.CandidateWorkitems, app.lastSnapshot.CoordinationRisk)
	}
	if !strings.Contains(app.lastSnapshot.ProjectFocus[0].ConfidenceReasons[0], sessionPath) ||
		!strings.Contains(app.lastSnapshot.CandidateWorkitems[0].ConfidenceReasons[0], sessionPath) ||
		!strings.Contains(app.lastSnapshot.CoordinationRisk.Signals[0].Evidence, sessionPath) {
		t.Fatalf("expected internal aggregate evidence to retain local paths, got projects=%+v candidates=%+v risk=%+v", app.lastSnapshot.ProjectFocus, app.lastSnapshot.CandidateWorkitems, app.lastSnapshot.CoordinationRisk)
	}
	if !strings.Contains(app.lastSnapshot.TranscriptStats.Errors[1], sessionFileURI) ||
		!strings.Contains(app.lastSnapshot.Notes[1], sessionFileURI) {
		t.Fatalf("expected internal file URI evidence to remain, got errors=%+v notes=%+v", app.lastSnapshot.TranscriptStats.Errors, app.lastSnapshot.Notes)
	}
}

func TestHandleDiagnosticExportAPIRedactsLocalEvidence(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	workspacePath := filepath.Join(root, "workspace", "agentload")
	bundlePath := filepath.Join(root, "Terminal.app")
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = snapshot.Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		Config: snapshot.SnapshotConfig{
			HistoryFile: sessionPath,
			CodexRoots:  []string{workspacePath},
		},
		History: snapshot.SnapshotHistory{StorePath: sessionPath, LastWriteError: "open " + filepath.Join(root, "state", "history.jsonl") + ": permission denied"},
		LiveProcesses: []snapshot.LiveProcessSnapshot{
			{
				PID:          42,
				Tool:         "codex",
				Command:      filepath.Join(root, "bin", "codex") + " --cwd=" + workspacePath + " " + sessionPath,
				SessionPaths: []string{sessionPath},
				HostApp:      &snapshot.HostApp{PID: 7, Name: "Terminal", BundlePath: bundlePath},
			},
		},
		LiveSessions: []snapshot.LiveSessionSnapshot{
			{Tool: "codex", SessionID: "session", Project: workspacePath, Path: sessionPath},
		},
		Diagnostics: snapshot.DiagnosticSnapshot{
			EvidenceGaps: []snapshot.DiagnosticSignalSnapshot{
				{Kind: "path_gap", Evidence: "read " + sessionPath, Detail: "cwd=" + workspacePath},
			},
		},
	}
	app.haveSnapshot = true
	handler := app.handler()
	req := newLoopbackRequest(http.MethodGet, "/api/diagnostic-export", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, "agentload-diagnostics.json") {
		t.Fatalf("expected diagnostic attachment header, got %q", got)
	}
	body := rec.Body.String()
	for _, leaked := range []string{root, sessionPath, workspacePath, bundlePath} {
		if strings.Contains(body, leaked) {
			t.Fatalf("expected diagnostic export to redact %q, got body %q", leaked, body)
		}
	}
	var got snapshot.DiagnosticExportSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode diagnostic export: %v", err)
	}
	if got.FormatVersion != 1 || got.Snapshot.Config.HistoryFile != "" || len(got.Snapshot.Config.CodexRoots) != 0 {
		t.Fatalf("expected sanitized export snapshot, got %+v", got)
	}
	if strings.Contains(got.Snapshot.History.LastWriteError, root) {
		t.Fatalf("expected sanitized history write error, got %q", got.Snapshot.History.LastWriteError)
	}
	if !slices.Contains(got.OmittedFields, "raw prompts") {
		t.Fatalf("expected omitted fields to document raw prompts, got %+v", got.OmittedFields)
	}
	for _, omitted := range []string{"raw prompts", "absolute local paths", "full command arguments", "environment variables", "transcript file paths", "app bundle paths"} {
		if !slices.Contains(got.OmittedFields, omitted) {
			t.Fatalf("expected omitted fields to include %q, got %+v", omitted, got.OmittedFields)
		}
	}
}

func TestHandleProcessDiagnosticAPIRedactsLocalEvidence(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	workspacePath := filepath.Join(root, "workspace", "agentload")
	bundlePath := filepath.Join(root, "Terminal.app")
	app := &trayApp{cfg: Config{RefreshInterval: 5 * time.Minute}}
	app.lastSnapshot = snapshot.Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		LiveProcesses: []snapshot.LiveProcessSnapshot{
			{
				PID:          42,
				Tool:         "codex",
				Command:      filepath.Join(root, "bin", "codex") + " --cwd=" + workspacePath + " resume " + sessionPath,
				SessionIDs:   []string{"session"},
				SessionPaths: []string{sessionPath},
				HostApp:      &snapshot.HostApp{PID: 7, Name: "Terminal", BundlePath: bundlePath},
			},
		},
	}
	app.haveSnapshot = true
	handler := app.handler()
	req := newLoopbackRequest(http.MethodGet, "/api/process-diagnostic/42", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, leaked := range []string{root, sessionPath, workspacePath, bundlePath} {
		if strings.Contains(body, leaked) {
			t.Fatalf("expected process diagnostic to redact %q, got body %q", leaked, body)
		}
	}
	var got snapshot.ProcessDiagnosticSnapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode process diagnostic: %v", err)
	}
	if got.Command == "" || !strings.Contains(got.Command, "codex") || len(got.SessionPaths) != 0 {
		t.Fatalf("expected sanitized process diagnostic, got %+v", got)
	}
	if got.HostApp == nil || got.HostApp.Name != "Terminal" || got.HostApp.BundlePath != "" {
		t.Fatalf("expected host app without bundle path, got %+v", got.HostApp)
	}
}

func TestSanitizeTextForClientKeepsProseSlashesWhileRedactingPaths(t *testing.T) {
	// A slash inside a word is prose, not a path. Treating it as one used to eat
	// the separator and the word after it, so the attribution reason shown to the
	// user read "no parsed transcript cwdlocal-path" — a sanitizer turning an
	// explanation into gibberish.
	tests := []struct{ in, want string }{
		{"no parsed transcript cwd/project evidence", "no parsed transcript cwd/project evidence"},
		{"anchored under a home/global directory", "anchored under a home/global directory"},
		{"and/or", "and/or"},
		// Real absolute paths must still be redacted to their basename.
		{"see /Users/me/secret/deep/file.jsonl now", "see file.jsonl now"},
		{"path=/Users/me/secret/deep/file.jsonl", "path=file.jsonl"},
		{"error at /Users/me/proj/a/b/c.jsonl: boom", "error at c.jsonl: boom"},
	}
	for _, tt := range tests {
		if got := sanitizeTextForClient(tt.in); got != tt.want {
			t.Fatalf("sanitizeTextForClient(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSanitizeTextForClientRedactsColonSeparatedPaths(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace", "agentload")
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	sessionFileURI := (&url.URL{Scheme: "file", Path: sessionPath}).String()

	got := sanitizeTextForClient("codex cwd:" + workspacePath + " source:" + sessionFileURI + " --session=" + sessionPath + " " + workspacePath + ":")
	for _, leaked := range []string{root, workspacePath, sessionPath, sessionFileURI} {
		if strings.Contains(got, leaked) {
			t.Fatalf("expected sanitized text to redact %q, got %q", leaked, got)
		}
	}
	for _, want := range []string{"cwd:agentload", "source:session.jsonl", "--session=session.jsonl", "agentload:"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected sanitized text %q to preserve %q", got, want)
		}
	}
}

func TestSanitizeTextForClientRedactsPathsInsideStructuredTokens(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "workspace", "agentload", "session.jsonl")
	spacedPath := filepath.Join(root, "User Name", "Workspace Name", "session.jsonl")
	cases := []string{
		"map[" + path + "]",
		`{"path":"` + path + `"}`,
		"diagnostic=" + path + ":42:7",
		"opened " + spacedPath + ": parse failed",
		path + " and " + spacedPath + " both failed",
		filepath.Join(root, "alice and Team", "session.jsonl") + ": parse failed",
		filepath.Join(root, "alice.smith Work", "session.jsonl") + ": parse failed",
	}
	for _, raw := range cases {
		got := sanitizeTextForClient(raw)
		for _, leaked := range []string{root, path, "User", "Workspace", "alice and Team", "alice.smith Work"} {
			if strings.Contains(got, leaked) {
				t.Fatalf("expected structured path %q to redact %q, got %q", raw, leaked, got)
			}
		}
		if !strings.Contains(got, "session.jsonl") {
			t.Fatalf("expected basename to remain useful for %q, got %q", raw, got)
		}
	}
	context := sanitizeTextForClient(path + " and " + spacedPath + " both failed")
	if !strings.Contains(context, " and ") || !strings.Contains(context, " both failed") {
		t.Fatalf("path sanitization swallowed diagnostic context: %q", context)
	}
}

func TestSanitizeTextForClientKeepsUnknownFileContextWithoutLeakingDottedDirectories(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name string
		path string
		want []string
	}{
		{name: "unknown extension", path: filepath.Join(root, "Profile Name", "session.py"), want: []string{"session.py", "parse failed"}},
		{name: "dotted directory with connector", path: filepath.Join(root, "cache.log and Team", "session.py"), want: []string{"session.py", "parse failed"}},
		{name: "directory without extension", path: filepath.Join(root, "workspace", "agentload"), want: []string{"agentload", "parse failed"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeTextForClient("opened " + tc.path + " parse failed")
			for _, leaked := range []string{root, "Profile Name", "cache.log", "Team"} {
				if strings.Contains(got, leaked) {
					t.Fatalf("expected path component %q to be redacted, got %q", leaked, got)
				}
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Fatalf("expected sanitized context %q to contain %q", got, want)
				}
			}
		})
	}
}

func TestListenWithFallbackRequiresLoopbackAddress(t *testing.T) {
	for _, tc := range []struct {
		addr     string
		valid    bool
		contains string
	}{
		{addr: "127.0.0.1:0", valid: true},
		{addr: "[::1]:0", valid: true},
		{addr: "0.0.0.0:0", contains: "loopback"},
		{addr: ":0", contains: "loopback"},
		{addr: "[::]:0", contains: "loopback"},
		{addr: "localhost:0", valid: true},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			err := validateLoopbackListenAddr(tc.addr)
			if tc.valid {
				if err != nil {
					t.Fatalf("validateLoopbackListenAddr(%q): %v", tc.addr, err)
				}
				return
			}
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tc.contains) {
				t.Fatalf("validateLoopbackListenAddr(%q) = %v, want loopback error", tc.addr, err)
			}
		})
	}
}

func TestListenWithFallbackNormalizesLocalhost(t *testing.T) {
	listener, url, err := listenWithFallback("localhost:0")
	if err != nil {
		t.Fatalf("listenWithFallback(localhost): %v", err)
	}
	defer listener.Close()
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("localhost URL %q is not normalized to IPv4 loopback", url)
	}
}

func TestListenWithFallbackUsesLoopbackEphemeralPortWhenBusy(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()

	listener, url, err := listenWithFallback(occupied.Addr().String())
	if err != nil {
		t.Fatalf("listenWithFallback: %v", err)
	}
	defer listener.Close()
	if listener.Addr().String() == occupied.Addr().String() {
		t.Fatalf("fallback reused occupied address %q", occupied.Addr())
	}
	if !strings.HasPrefix(url, "http://127.0.0.1:") {
		t.Fatalf("fallback URL %q is not loopback", url)
	}
}

func TestListenerURLFormatsIPv6Loopback(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer listener.Close()
	got := listenerURL(listener)
	if !strings.HasPrefix(got, "http://[::1]:") {
		t.Fatalf("listener URL %q is not bracketed IPv6 loopback", got)
	}
	if parsed, err := url.Parse(got); err != nil || parsed.Hostname() != "::1" || parsed.Port() == "" {
		t.Fatalf("listener URL %q is not a valid IPv6 URL: %v", got, err)
	}
}

func TestSanitizeCommandForClientKeepsIdentityOnly(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace", "agentload")
	sessionPath := filepath.Join(root, "sessions", "session.jsonl")
	sessionFileURI := (&url.URL{Scheme: "file", Path: sessionPath}).String()
	raw := filepath.Join(root, "bin", "codex") +
		` -c model_providers.local.base_url="http://127.0.0.1:12345"` +
		" --cwd=" + workspacePath +
		" --source=" + sessionFileURI +
		" --yolo resume " + sessionPath +
		" write a private operator note"

	got := sanitizeCommandForClient(raw)
	for _, leaked := range []string{root, workspacePath, sessionPath, sessionFileURI, "http://127.0.0.1:12345", "private operator note"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("expected command identity to redact %q, got %q", leaked, got)
		}
	}
	for _, want := range []string{"codex", "-c <value>", "--cwd=<value>", "--source=<value>", "--yolo", "resume", "..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected command identity %q to contain %q", got, want)
		}
	}
}

func TestHandleSnapshotAPIRedactsFreshObserverConfigPaths(t *testing.T) {
	originalDiscover := discoverLiveProcessesFunc
	discoverLiveProcessesFunc = func(context.Context, *codingAgentRegistry) ([]snapshot.LiveProcess, []string) {
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
	req := newLoopbackRequest(http.MethodGet, "/api/snapshot", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}
	var got snapshot.Snapshot
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
	got := formatTrayMetaTitle(snapshot.Snapshot{
		GeneratedAt: "2026-06-28T12:00:00Z",
		TranscriptStats: snapshot.TranscriptStats{
			ScannedFiles:                  19,
			ParsedFiles:                   11,
			DeferredFiles:                 4,
			TailParsedFiles:               3,
			HistoricalScanDeferred:        true,
			ForegroundScanLookbackSeconds: 3600,
			Cached:                        true,
		},
	})
	for _, want := range []string{"cache hit", "11/19 transcripts", "4 deferred", "3 tail", "foreground 1h", "history deferred"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected tray metadata %q to contain %q", got, want)
		}
	}
}

func TestNormalizeToolIconNameAllowlist(t *testing.T) {
	tests := map[string]string{
		"codex":              "codex",
		"codexL":             "codex",
		"com.openai.codex":   "codex",
		"traex":              "trae",
		"trae_cli":           "trae",
		"Trae.app":           "trae",
		"warp":               "karp",
		"WarpOss":            "karp",
		"claude-code":        "claude",
		"opencode-ai":        "opencode",
		"gemini-cli":         "gemini",
		"@google/gemini-cli": "gemini",
		"../../etc/passwd":   "",
		"unknown":            "",
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
		req := newLoopbackRequest(http.MethodPost, "/api/tool-icon/codex", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("expected 405, got %d", rec.Code)
		}
	})

	t.Run("rejects traversal", func(t *testing.T) {
		req := newLoopbackRequest(http.MethodGet, "/api/tool-icon/../codex", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("expected 404, got %d", rec.Code)
		}
	})

	t.Run("rejects unknown", func(t *testing.T) {
		req := newLoopbackRequest(http.MethodGet, "/api/tool-icon/not-a-tool", nil)
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
	req := newLoopbackRequest(http.MethodGet, "/api/tool-icon/trae", nil)
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
	req := newLoopbackRequest(http.MethodGet, "/api/tool-icon/codex", nil)
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

	gotPath, ctype, ok := resolveHostAppIconFile(snapshot.HostApp{
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
	app.rememberSnapshot(snapshot.Snapshot{
		LiveProcesses: []snapshot.LiveProcessSnapshot{
			{
				PID: 42,
				HostApp: &snapshot.HostApp{
					PID:        42,
					Name:       "Terminal",
					BundlePath: bundlePath,
				},
			},
		},
	})

	req := newLoopbackRequest(http.MethodGet, "/api/host-app-icon/42", nil)
	got, ok := app.observedHostAppFromRequest(req, "/api/host-app-icon/")
	if !ok {
		t.Fatalf("expected observed host app")
	}
	if got.Name != "Terminal" || got.BundlePath != bundlePath {
		t.Fatalf("unexpected host app: %#v", got)
	}

	missingReq := newLoopbackRequest(http.MethodGet, "/api/host-app-icon/43", nil)
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
	discoverLiveProcessesFunc = func(context.Context, *codingAgentRegistry) ([]snapshot.LiveProcess, []string) {
		return []snapshot.LiveProcess{
			{
				PID:     42,
				Tool:    "codex",
				Command: "codex resume",
				HostApp: &snapshot.HostApp{
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

	req := newLoopbackRequest(http.MethodGet, "/api/host-app-icon/42", nil)
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

	req := newLoopbackRequest(http.MethodPost, "/api/host-app-icon/42", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}

	req = newLoopbackRequest(http.MethodGet, "/api/host-app-icon/../42", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestHandleOpenHostAppAPIGuardsMethodAndObservedEvidence(t *testing.T) {
	app := &trayApp{}
	handler := app.handler()

	req := newLoopbackRequest(http.MethodGet, "/api/open-host-app/42", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}

	req = newLoopbackRequest(http.MethodPost, "/api/open-host-app/42", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected unobserved pid to be rejected with 404, got %d", rec.Code)
	}

	req = newLoopbackRequest(http.MethodPost, "/api/open-host-app/../42", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected traversal path to be rejected with 404, got %d", rec.Code)
	}
}

func TestHandleQuitAPIHandsOffToSystrayQuit(t *testing.T) {
	originalQuit := systrayQuit
	quit := make(chan struct{}, 1)
	systrayQuit = func() { quit <- struct{}{} }
	t.Cleanup(func() { systrayQuit = originalQuit })

	lifecycle := newLifecycleLog(filepath.Join(t.TempDir(), "history.jsonl"))
	app := &trayApp{logger: log.New(io.Discard, "", 0), lifecycle: lifecycle}
	handler := app.handler()

	req := newLoopbackRequest(http.MethodPost, "/api/quit", nil)
	req.Host = "127.0.0.1:8123"
	req.Header.Set("Origin", "http://127.0.0.1:8123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d with body %q", rec.Code, rec.Body.String())
	}

	// The quit hand-off is deferred so the response flushes first, but it must
	// still arrive: a dropped hand-off leaves the app running after the user
	// asked it to quit.
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("quit api never handed off to systrayQuit")
	}

	names := readLifecycleEventNames(t, lifecycle.path)
	if len(names) == 0 || names[0] != "quit_requested" {
		t.Fatalf("expected quit_requested to be recorded, got %v", names)
	}
}

func TestHandleQuitAPIHandsOffEvenWhenLifecycleRecordPanics(t *testing.T) {
	originalQuit := systrayQuit
	quit := make(chan struct{}, 1)
	systrayQuit = func() { quit <- struct{}{} }
	t.Cleanup(func() { systrayQuit = originalQuit })

	// No lifecycle log: recordLifecycle tolerates it, and the hand-off must
	// still reach the tray so the API-initiated shutdown is not skipped.
	app := &trayApp{logger: log.New(io.Discard, "", 0)}
	handler := app.handler()

	req := newLoopbackRequest(http.MethodPost, "/api/quit", nil)
	req.Host = "127.0.0.1:8123"
	req.Header.Set("Origin", "http://127.0.0.1:8123")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", rec.Code)
	}
	select {
	case <-quit:
	case <-time.After(10 * time.Second):
		t.Fatal("quit api never handed off to systrayQuit")
	}
}

// TestHostHeaderMustBeLoopback locks the DNS rebinding guard.
//
// The same-origin check cannot cover this: under rebinding the browser puts the
// attacker's name in BOTH Origin and Host, so they agree and the check passes.
// The literal address is the thing the attacker cannot forge, because the
// browser fills Host from the URL it navigated to.
func TestHostHeaderMustBeLoopback(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		wantCode int
	}{
		{name: "loopback literal allowed", host: "127.0.0.1:8123", wantCode: http.StatusOK},
		{name: "ipv6 loopback allowed", host: "[::1]:8123", wantCode: http.StatusOK},
		{name: "localhost allowed", host: "localhost:8123", wantCode: http.StatusOK},
		{name: "absent host allowed for native clients", host: "", wantCode: http.StatusOK},
		{name: "rebound name blocked", host: "evil.example:8123", wantCode: http.StatusForbidden},
		{name: "public ip blocked", host: "203.0.113.5:8123", wantCode: http.StatusForbidden},
		{name: "loopback-looking name blocked", host: "127.0.0.1.evil.example:8123", wantCode: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := &trayApp{}
			req := newLoopbackRequest(http.MethodGet, "/api/live-token-rate", nil)
			req.Host = tt.host
			rec := httptest.NewRecorder()
			app.handler().ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("host %q: expected status %d, got %d with body %q", tt.host, tt.wantCode, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestRebindingGuardCoversReadsNotJustWrites pins the scope decision: the
// snapshot carries project paths and session identity, so a rebound read is the
// payload rather than a lesser case of a rebound write.
func TestRebindingGuardCoversReadsNotJustWrites(t *testing.T) {
	app := &trayApp{}
	for _, path := range []string{"/", "/dashboard", "/api/snapshot", "/api/live-token-rate", "/api/diagnostic-export"} {
		req := newLoopbackRequest(http.MethodGet, path, nil)
		req.Host = "evil.example:8123"
		// Rebinding makes Origin agree with Host, which is exactly why the
		// same-origin check cannot catch it.
		req.Header.Set("Origin", "http://evil.example:8123")
		rec := httptest.NewRecorder()
		app.handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("%s: expected a rebound read to be rejected, got %d", path, rec.Code)
		}
	}
}

// newLoopbackRequest builds a request the way the real server can actually see
// one. httptest.NewRequest defaults Host to "example.com", which the DNS
// rebinding guard rejects on purpose -- no browser talking to this loopback
// listener ever sends that. Tests that exercise the guard itself overwrite Host
// after calling this.
func newLoopbackRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	req.Host = "127.0.0.1:8123"
	return req
}
