package main

import (
	"context"
	"os"
	"testing"
)

// BenchmarkSnapshotAgainstRealRoots measures one full refresh against whatever
// transcripts this machine actually has. It is the number that matters for a
// background menubar process: the refresh runs forever, on battery.
//
// It is skipped unless AGENTLOAD_BENCH_REAL is set, because it reads the
// developer's own home directory and its result depends on local state.
func BenchmarkSnapshotAgainstRealRoots(b *testing.B) {
	if os.Getenv("AGENTLOAD_BENCH_REAL") == "" {
		b.Skip("set AGENTLOAD_BENCH_REAL=1 to profile against local transcripts")
	}
	cfg := defaultConfig()
	cfg.HistoryFile = ""
	observer := newObserver(cfg)
	ctx := context.Background()

	// Warm the caches the running app would already have warmed, so the
	// benchmark measures steady-state refresh cost rather than cold start.
	observer.Snapshot(ctx)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		observer.Snapshot(ctx)
	}
}
