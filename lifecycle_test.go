package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLifecycleLogPathUsesHistoryDirectory(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "AgentLoad", "history.jsonl")
	want := filepath.Join(filepath.Dir(historyPath), lifecycleLogFileName)
	if got := lifecycleLogPath(historyPath); got != want {
		t.Fatalf("lifecycleLogPath() = %q, want %q", got, want)
	}
}

func TestLifecycleLogRecordWritesJSONL(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "AgentLoad", "history.jsonl")
	log := newLifecycleLog(historyPath)
	err := log.record(lifecycleEvent{
		Event:         "snapshot_recorded",
		RefreshSlotID: "test-slot",
		Metrics: &lifecycleSnapshotMetrics{
			PIDConcurrency:         3,
			SessionConcurrency:     2,
			ActiveBurstConcurrency: 1,
			MappedProcesses:        2,
			UnmappedProcesses:      1,
			ProjectCount:           1,
			TopProject:             "agentload",
		},
	})
	if err != nil {
		t.Fatalf("record lifecycle event: %v", err)
	}

	raw, err := os.ReadFile(log.path)
	if err != nil {
		t.Fatalf("read lifecycle log: %v", err)
	}
	var event lifecycleEvent
	if err := json.Unmarshal(raw[:len(raw)-1], &event); err != nil {
		t.Fatalf("decode lifecycle event: %v", err)
	}
	if event.Event != "snapshot_recorded" || event.RefreshSlotID == "" || event.PID == 0 || event.PPID == 0 {
		t.Fatalf("unexpected lifecycle event: %+v", event)
	}
	if event.Metrics == nil || event.Metrics.PIDConcurrency != 3 || event.Metrics.TopProject != "agentload" {
		t.Fatalf("unexpected lifecycle metrics: %+v", event.Metrics)
	}
}

func TestLifecycleLogRecordAppendsQuitAndHeartbeatEvents(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "AgentLoad", "history.jsonl")
	log := newLifecycleLog(historyPath)
	for _, event := range []lifecycleEvent{
		{Event: "quit_requested", Reason: "api"},
		{Event: "heartbeat"},
	} {
		if err := log.record(event); err != nil {
			t.Fatalf("record lifecycle event %q: %v", event.Event, err)
		}
	}

	raw, err := os.ReadFile(log.path)
	if err != nil {
		t.Fatalf("read lifecycle log: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected two lifecycle events, got %d: %q", len(lines), raw)
	}
	var first, second lifecycleEvent
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("decode first lifecycle event: %v", err)
	}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("decode second lifecycle event: %v", err)
	}
	if first.Event != "quit_requested" || first.Reason != "api" || second.Event != "heartbeat" {
		t.Fatalf("unexpected lifecycle events: %+v %+v", first, second)
	}
}

func TestStartHeartbeatKeepsBeatingAfterABeatPanics(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "AgentLoad", "history.jsonl")
	lifecycle := newLifecycleLog(historyPath)
	stop := make(chan struct{})
	beats := make(chan int, 8)

	// The first beat panics. The loop must keep beating: a panic that unwinds the
	// goroutine stops heartbeats for the whole session, so a live run looks dead
	// in the lifecycle log with nothing to distinguish it from a crash.
	count := 0
	done := lifecycle.startHeartbeatWithBeat(stop, 5*time.Millisecond, func() {
		count++
		// Non-blocking: the beat must never wedge on a full channel, or the loop
		// would stall and this test would report a hang instead of a verdict.
		select {
		case beats <- count:
		default:
		}
		if count == 1 {
			panic("record failed")
		}
	})
	defer func() {
		close(stop)
		<-done
	}()

	deadline := time.After(10 * time.Second)
	seen := 0
	for seen < 3 {
		select {
		case seen = <-beats:
		case <-deadline:
			t.Fatalf("heartbeat loop stopped after the panicking beat: saw %d beats", seen)
		}
	}
}

func TestStartHeartbeatRecordsBeatsAndHonoursStop(t *testing.T) {
	historyPath := filepath.Join(t.TempDir(), "AgentLoad", "history.jsonl")
	lifecycle := newLifecycleLog(historyPath)
	stop := make(chan struct{})
	done := lifecycle.startHeartbeat(stop, 5*time.Millisecond)

	beat := false
	for i := 0; i < 1000 && !beat; i++ {
		if raw, err := os.ReadFile(lifecycle.path); err == nil && strings.Contains(string(raw), `"event":"heartbeat"`) {
			beat = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !beat {
		t.Fatal("expected the heartbeat loop to record a beat")
	}

	// Cancellation must still be honoured after the containment change: the loop
	// has to observe stop rather than only ever waking on the ticker.
	close(stop)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("heartbeat loop did not join after stop")
	}
	stopped := false
	for i := 0; i < 200; i++ {
		before, err := os.ReadFile(lifecycle.path)
		if err != nil {
			t.Fatalf("read lifecycle log: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
		after, err := os.ReadFile(lifecycle.path)
		if err != nil {
			t.Fatalf("read lifecycle log: %v", err)
		}
		if len(before) == len(after) {
			stopped = true
			break
		}
	}
	if !stopped {
		t.Fatal("expected the heartbeat loop to stop writing after stop was closed")
	}
}
