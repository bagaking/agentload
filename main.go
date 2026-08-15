package main

import (
	"agentload/internal/historyfile"
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "traj" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		code := runTrajectoryCLIContext(ctx, os.Args[2:], os.Stdout, os.Stderr)
		stop()
		os.Exit(code)
	}
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
	owner, err := historyfile.TryAcquire(cfg.HistoryFile + ".owner")
	if errors.Is(err, historyfile.ErrLocked) {
		log.Print("Agent Load is already running for this history")
		return
	}
	if err != nil {
		log.Fatalf("cannot reserve Agent Load history: %v", err)
	}
	defer owner.Release()
	lifecycle := newLifecycleLog(cfg.HistoryFile)
	defer lifecycle.recordPanic()

	observer := newObserver(cfg)
	listener, url, err := listenWithFallback(cfg.ListenAddr)
	if err != nil {
		_ = lifecycle.record(lifecycleEvent{Event: "startup_failed", Reason: "listen_failed", Error: err.Error(), ListenAddr: cfg.ListenAddr})
		log.Fatalf("listen failed: %v", err)
	}

	logger := log.New(os.Stdout, "", log.LstdFlags)
	stopSignalLogger := startLifecycleSignalLogger(lifecycle)
	defer stopSignalLogger()
	if err := lifecycle.record(lifecycleEvent{
		Event:                  "startup",
		ListenAddr:             cfg.ListenAddr,
		URL:                    url,
		RefreshIntervalSeconds: int(cfg.RefreshInterval / time.Second),
	}); err != nil {
		logger.Printf("lifecycle log startup record failed: %v", err)
	}
	logger.Printf("agentload dashboard on %s", url)
	app := newTrayApp(cfg, observer, logger, listener, url, lifecycle)
	if err := app.run(); err != nil {
		_ = lifecycle.record(lifecycleEvent{Event: "run_failed", Error: err.Error()})
		logger.Fatalf("tray failed: %v", err)
	}
}
