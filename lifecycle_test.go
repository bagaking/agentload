package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
