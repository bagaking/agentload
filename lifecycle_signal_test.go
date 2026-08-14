package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestLifecycleSignalsCoverInterruptAndTerminate(t *testing.T) {
	want := map[os.Signal]bool{os.Interrupt: false, syscall.SIGTERM: false}
	for _, sig := range lifecycleSignals() {
		if _, tracked := want[sig]; tracked {
			want[sig] = true
		}
	}
	for sig, found := range want {
		if !found {
			t.Fatalf("expected lifecycleSignals() to include %v", sig)
		}
	}
}

func TestStartLifecycleSignalLoggerStopIsIdempotent(t *testing.T) {
	lifecycle := newLifecycleLog(filepath.Join(t.TempDir(), "history.jsonl"))
	stop := startLifecycleSignalLogger(lifecycle)

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		stop()
		// A second stop must not panic by closing `done` twice; shutdown paths
		// can reach the cleanup more than once.
		stop()
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("expected the signal logger stop to be idempotent and return")
	}
}

func TestStartLifecycleSignalLoggerStopUnregistersHandler(t *testing.T) {
	lifecycle := newLifecycleLog(filepath.Join(t.TempDir(), "history.jsonl"))
	stop := startLifecycleSignalLogger(lifecycle)
	stop()

	// After stop the handler must be unregistered, so the lifecycle log stays
	// empty rather than the goroutine still consuming and re-raising signals.
	if _, err := os.Stat(lifecycle.path); err == nil {
		names := readLifecycleEventNames(t, lifecycle.path)
		for _, name := range names {
			if name == "signal" {
				t.Fatal("expected no signal event to be recorded after stop")
			}
		}
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat lifecycle log: %v", err)
	}
}

func TestLifecycleSignalRecordingSurvivesAPanickingRecord(t *testing.T) {
	// A nil lifecycle log makes record panic on a nil receiver deref inside the
	// contained step. The signal path must still proceed to signal.Reset and
	// re-raise instead of swallowing the user's SIGINT/SIGTERM.
	var lifecycle *lifecycleLog
	recorded := make(chan struct{})
	go func() {
		defer close(recorded)
		runBackgroundStep("lifecycle signal logger", func() {
			_ = lifecycle.record(lifecycleEvent{Event: "signal", Signal: "terminated"})
			panic("record failed")
		})
	}()
	select {
	case <-recorded:
	case <-time.After(5 * time.Second):
		t.Fatal("expected a panicking signal record to be contained")
	}
}
