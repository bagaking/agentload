package main

import (
	"os"
	"os/signal"
	"sync"
	"syscall"
)

func startLifecycleSignalLogger(lifecycle *lifecycleLog) func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, lifecycleSignals()...)

	go func() {
		select {
		case sig := <-signals:
			// Recording is best-effort and must never swallow the signal: if it
			// panics we still reset the handler and re-raise, so the process
			// honours the signal instead of ignoring the user's SIGINT/SIGTERM.
			runBackgroundStep("lifecycle signal logger", func() {
				_ = lifecycle.record(lifecycleEvent{
					Event:  "signal",
					Signal: sig.String(),
				})
			})
			signal.Reset(sig)
			if syscallSignal, ok := sig.(syscall.Signal); ok {
				_ = syscall.Kill(os.Getpid(), syscallSignal)
				return
			}
			os.Exit(128)
		case <-done:
			return
		}
	}()

	// The returned stop is idempotent so callers can invoke it from more than one
	// shutdown path without closing `done` twice.
	var stopOnce sync.Once
	return func() {
		stopOnce.Do(func() {
			signal.Stop(signals)
			close(done)
		})
	}
}

func lifecycleSignals() []os.Signal {
	return []os.Signal{
		os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGQUIT,
		syscall.SIGABRT,
		syscall.SIGTERM,
	}
}
