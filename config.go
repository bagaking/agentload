package main

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

type Config struct {
	ListenAddr         string
	IdleGap            time.Duration
	MinInterval        time.Duration
	Lookback           time.Duration
	TranscriptCacheTTL time.Duration
	RefreshInterval    time.Duration
	HistoryFile        string
	ClaudeRoots        []string
	CodexRoots         []string
	TraeRoots          []string
}

var selectableRefreshIntervals = []time.Duration{
	30 * time.Second,
	60 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
	0,
}

func defaultConfig() Config {
	return Config{
		ListenAddr:         "127.0.0.1:8642",
		IdleGap:            90 * time.Second,
		MinInterval:        15 * time.Second,
		Lookback:           7 * 24 * time.Hour,
		TranscriptCacheTTL: 60 * time.Second,
		RefreshInterval:    5 * time.Minute,
		HistoryFile:        defaultHistoryFile(),
		ClaudeRoots:        defaultClaudeRoots(),
		CodexRoots:         defaultCodexRoots(),
		TraeRoots:          defaultTraeRoots(),
	}
}

func normalizeRefreshInterval(interval time.Duration) time.Duration {
	if interval == 0 {
		return 0
	}
	if interval < 30*time.Second {
		return 30 * time.Second
	}
	return interval
}

func selectableRefreshInterval(interval time.Duration) bool {
	if interval > 0 && interval < 30*time.Second {
		return false
	}
	for _, candidate := range selectableRefreshIntervals {
		if interval == candidate {
			return true
		}
	}
	return false
}

func (c Config) snapshotConfig() SnapshotConfig {
	return SnapshotConfig{
		IdleGapSeconds:       int(c.IdleGap / time.Second),
		MinIntervalSeconds:   int(c.MinInterval / time.Second),
		LookbackHours:        int(c.Lookback / time.Hour),
		TranscriptCacheTTL:   int(c.TranscriptCacheTTL / time.Second),
		ClaudeRoots:          append([]string(nil), c.ClaudeRoots...),
		CodexRoots:           append([]string(nil), c.CodexRoots...),
		TraeRoots:            append([]string(nil), c.TraeRoots...),
		ProcessRefreshTarget: int(c.RefreshInterval / time.Second),
		HistoryFile:          c.HistoryFile,
	}
}

func defaultClaudeRoots() []string {
	return parseRoots(
		firstNonEmpty(
			os.Getenv("AGENTLOAD_CLAUDE_DIRS"),
			os.Getenv("CLAUDE_CONFIG_DIR"),
		),
		defaultHomePath(".claude"),
	)
}

func defaultCodexRoots() []string {
	return parseRoots(
		firstNonEmpty(
			os.Getenv("AGENTLOAD_CODEX_DIRS"),
			os.Getenv("CODEX_HOME"),
		),
		defaultHomePath(".codex"),
	)
}

func defaultTraeRoots() []string {
	return parseRoots(
		firstNonEmpty(
			os.Getenv("AGENTLOAD_TRAE_DIRS"),
			os.Getenv("TRAE_CLI_HOME"),
		),
		defaultHomePath(filepath.Join(".trae", "cli")),
	)
}

func defaultHistoryFile() string {
	home := userHomeDir()
	if runtime.GOOS == "darwin" && home != "" {
		return filepath.Join(home, "Library", "Application Support", "AgentLoad", "history.jsonl")
	}
	if configDir, err := os.UserConfigDir(); err == nil && strings.TrimSpace(configDir) != "" {
		return filepath.Join(configDir, "agentload", "history.jsonl")
	}
	if home != "" {
		return filepath.Join(home, ".config", "agentload", "history.jsonl")
	}
	return filepath.Clean("agentload-history.jsonl")
}

func resolveHistoryFile(raw string) string {
	return cleanUserPath(firstNonEmpty(strings.TrimSpace(raw), defaultHistoryFile()))
}

func parseRoots(raw string, defaults []string) []string {
	if strings.TrimSpace(raw) == "" {
		return existingDirs(defaults)
	}
	parts := strings.Split(raw, string(os.PathListSeparator))
	if len(parts) == 1 && strings.Contains(raw, "\n") {
		parts = strings.Split(raw, "\n")
	}
	return existingDirs(parts)
}

func existingDirs(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, item := range items {
		item = cleanUserPath(item)
		if item == "" || item == "." {
			continue
		}
		info, err := os.Stat(item)
		if err != nil || !info.IsDir() {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	sort.Strings(out)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func cleanUserPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	raw = os.ExpandEnv(raw)
	if strings.HasPrefix(raw, "~") {
		home := userHomeDir()
		if home != "" {
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~"))
		}
	}
	return filepath.Clean(raw)
}

func userHomeDir() string {
	home, _ := os.UserHomeDir()
	return strings.TrimSpace(home)
}

func defaultHomePath(name string) []string {
	home := userHomeDir()
	if home == "" {
		return nil
	}
	return []string{filepath.Join(home, name)}
}
