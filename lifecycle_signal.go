package main

import (
	"os"
	"os/signal"
	"syscall"
)

func startLifecycleSignalLogger(lifecycle *lifecycleLog) func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, lifecycleSignals()...)

	go func() {
		select {
		case sig := <-signals:
			_ = lifecycle.record(lifecycleEvent{
				Event:  "signal",
				Signal: sig.String(),
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

	return func() {
		signal.Stop(signals)
		close(done)
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
