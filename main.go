package main

import (
	"flag"
	"log"
	"os"
)

func main() {
	cfg := defaultConfig()

	listen := flag.String("listen", envOr("AGENTLOAD_LISTEN_ADDR", cfg.ListenAddr), "listen address")
	idleGap := flag.Duration("idle-gap", cfg.IdleGap, "idle gap for active burst segmentation")
	minInterval := flag.Duration("min-interval", cfg.MinInterval, "minimum interval for zero-length spans")
	lookback := flag.Duration("lookback", cfg.Lookback, "historic lookback window")
	cacheTTL := flag.Duration("cache-ttl", cfg.TranscriptCacheTTL, "transcript scan cache ttl")
	refreshInterval := flag.Duration("refresh-interval", cfg.RefreshInterval, "background refresh interval")
	historyFile := flag.String("history-file", envOr("AGENTLOAD_HISTORY_FILE", cfg.HistoryFile), "local JSONL history file")
	flag.Parse()

	cfg.ListenAddr = *listen
	cfg.IdleGap = *idleGap
	cfg.MinInterval = *minInterval
	cfg.Lookback = *lookback
	cfg.TranscriptCacheTTL = *cacheTTL
	cfg.RefreshInterval = normalizeRefreshInterval(*refreshInterval)
	cfg.HistoryFile = resolveHistoryFile(*historyFile)

	observer := newObserver(cfg)
	listener, url, err := listenWithFallback(cfg.ListenAddr)
	if err != nil {
		log.Fatalf("listen failed: %v", err)
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	logger.Printf("agentload dashboard on %s", url)
	app := newTrayApp(cfg, observer, logger, listener, url)
	if err := app.run(); err != nil {
		logger.Fatalf("tray failed: %v", err)
	}
}
