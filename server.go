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
	"syscall"
	"time"
)

//go:embed ui/dist/* ui/dist/assets/* ui/tool-icons/*
var uiAssets embed.FS

func (a *trayApp) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handlePopoverPage)
	mux.HandleFunc("/dashboard", a.handleDashboardPage)
	mux.HandleFunc("/assets/", a.handleUIAsset)
	mux.HandleFunc("/api/snapshot", a.handleSnapshotAPI)
	mux.HandleFunc("/api/refresh", a.handleRefreshAPI)
	mux.HandleFunc("/api/quit", a.handleQuitAPI)
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
	snapshot := a.snapshotForClient(r.Context())
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
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
	enc := json.NewEncoder(w)
	_ = enc.Encode(snapshot)
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

func (a *trayApp) handleRefreshAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
	jsonResponse(w, http.StatusOK, map[string]bool{"ok": true})
	go func() {
		time.Sleep(150 * time.Millisecond)
		systrayQuit()
	}()
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
	app, ok := a.observedHostAppFromRequest(r, "/api/open-host-app/")
	if !ok || strings.TrimSpace(app.BundlePath) == "" {
		http.NotFound(w, r)
		return
	}
	cmd := exec.CommandContext(r.Context(), "/usr/bin/open", app.BundlePath)
	if output, err := cmd.CombinedOutput(); err != nil {
		http.Error(w, strings.TrimSpace(string(output)), http.StatusBadGateway)
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
	ctx, cancel := context.WithTimeout(ctx, maxDuration(45*time.Second, a.cfg.Lookback/10))
	defer cancel()
	snapshot := a.observer.Snapshot(ctx)
	if snapshot.RefreshSlotID == "" {
		snapshot.RefreshSlotID = a.refreshSlotID(time.Now())
	}
	return a.rememberSnapshot(snapshot), true
}

func sanitizeSnapshotForClient(snapshot Snapshot) Snapshot {
	snapshot.Config.ClaudeRoots = []string{}
	snapshot.Config.CodexRoots = []string{}
	snapshot.Config.TraeRoots = []string{}
	snapshot.Config.HistoryFile = ""
	snapshot.History.StorePath = ""
	snapshot.TranscriptStats.Errors = sanitizeTextListForClient(snapshot.TranscriptStats.Errors)
	snapshot.LiveProcesses = sanitizeLiveProcessesForClient(snapshot.LiveProcesses)
	snapshot.LiveSessions = sanitizeLiveSessionsForClient(snapshot.LiveSessions)
	snapshot.Notes = sanitizeTextListForClient(snapshot.Notes)
	return snapshot
}

func sanitizeLiveProcessesForClient(processes []LiveProcessSnapshot) []LiveProcessSnapshot {
	if len(processes) == 0 {
		return processes
	}
	out := append([]LiveProcessSnapshot(nil), processes...)
	for i := range out {
		out[i].Command = sanitizeCommandForClient(out[i].Command)
		out[i].SessionIDs = append([]string(nil), out[i].SessionIDs...)
		out[i].SessionPaths = nil
		if out[i].HostApp != nil {
			host := *out[i].HostApp
			host.BundlePath = ""
			out[i].HostApp = &host
		}
	}
	return out
}

func sanitizeLiveSessionsForClient(sessions []LiveSessionSnapshot) []LiveSessionSnapshot {
	if len(sessions) == 0 {
		return sessions
	}
	out := append([]LiveSessionSnapshot(nil), sessions...)
	for i := range out {
		out[i].Path = ""
		out[i].RoleReasons = sanitizeTextListForClient(out[i].RoleReasons)
		out[i].ConfidenceReasons = sanitizeTextListForClient(out[i].ConfidenceReasons)
		out[i].ProjectAttributionReasons = sanitizeTextListForClient(out[i].ProjectAttributionReasons)
		out[i].Provenance = append([]string(nil), out[i].Provenance...)
		if len(out[i].HostApps) > 0 {
			hosts := append([]HostApp(nil), out[i].HostApps...)
			for j := range hosts {
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
	for i, field := range fields {
		fields[i] = sanitizeTokenForClient(field)
	}
	return strings.Join(fields, " ")
}

func sanitizeTextForClient(text string) string {
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
		return prefix + key + "=" + sanitizePathLikeValue(value) + suffix
	}
	return prefix + sanitizePathLikeValue(core) + suffix
}

func sanitizePathLikeValue(value string) string {
	if !filepath.IsAbs(value) {
		return value
	}
	base := filepath.Base(filepath.Clean(value))
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "local-path"
	}
	return base
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
	case "trae", "traex":
		return "trae"
	case "karp", "warp", "warposs", "warp-oss":
		return "karp"
	case "claude", "claude-code", "claude-cli", "anthropic":
		return "claude"
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
	"codex":  {"ui/tool-icons/codex.svg"},
	"trae":   {"ui/tool-icons/trae.svg"},
	"claude": {"ui/tool-icons/claude.svg"},
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
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		return ln, listenerURL(ln), nil
	}
	if !isAddrInUse(err) {
		return nil, "", err
	}
	host, _, splitErr := net.SplitHostPort(addr)
	if splitErr != nil {
		return nil, "", err
	}
	ln, fallbackErr := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if fallbackErr != nil {
		return nil, "", fallbackErr
	}
	return ln, listenerURL(ln), nil
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
	return fmt.Sprintf("http://%s:%s", host, port)
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
