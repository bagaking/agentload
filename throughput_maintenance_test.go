package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agentload/internal/historyfile"
)

func TestThroughputMaintenanceLatestKeysAndDurableArchive(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "throughput.jsonl")
	cold := now.Add(-40 * 24 * time.Hour) // Outside resident retention, still preserved.
	online := throughputTestMinute(cold, 10)
	replay := throughputTestMinute(cold, 20)
	replay.Origin = "session_replay"
	changed := throughputTestMinute(now, 30)
	changed.Origin = "session_replay"
	rate := 2.0
	a := LegacyThroughputFact{At: cold.Format(time.RFC3339), State: "live", WindowSeconds: 60, OutputTokensPerSecond: &rate}
	b := a
	b.WindowSeconds = 300
	records := []throughputHistoryRecord{
		{1, throughputHistoryKindMinute, &online, nil}, {1, throughputHistoryKindMinute, &replay, nil},
		{1, throughputHistoryKindLegacy, nil, &a}, {1, throughputHistoryKindLegacy, nil, &b},
		{1, throughputHistoryKindMinute, &changed, nil},
	}
	if err := appendThroughputHistoryRecords(path, records); err != nil {
		t.Fatal(err)
	}
	// Simulate archive publication before hot replacement, then retry.
	coldRows := map[string]throughputStoredRecord{}
	for _, r := range records[:4] {
		raw, _ := json.Marshal(r)
		parsed, e := parseThroughputStoredRecord(raw)
		if e != nil {
			t.Fatal(e)
		}
		foldThroughputRecord(coldRows, parsed)
	}
	archive := archivePartitionPath(path, archiveMonth(cold))
	if err := replaceThroughputLines(archive, sortedThroughputLines(coldRows), true); err != nil {
		t.Fatal(err)
	}
	allow := func(string, uint64) error { return nil }
	for i := 0; i < 2; i++ {
		if err := maintainThroughputHistoryFile(path, now, allow); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Count(raw, []byte{'\n'}) != 1 {
		t.Fatal("hot revisions not folded", string(raw))
	}
	lines, err := readArchivePartition(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatal("legacy windows or old facts lost", len(lines))
	}
	for _, line := range lines {
		row, e := parseThroughputStoredRecord(line)
		if e != nil {
			t.Fatal(e)
		}
		if row.record.Minute != nil && *row.record.Minute.OutputTokens != 10 {
			t.Fatal("online overwritten by replay")
		}
	}
}

func TestThroughputMaintenancePreservesCorruptAndLowSpace(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	for _, tail := range []string{"{broken}\n", `{"schema_version":99}` + "\n", `{"schema_version":1}`} {
		t.Run(tail, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "throughput.jsonl")
			fact := throughputTestMinute(now, 10)
			if e := appendThroughputHistoryRecords(path, []throughputHistoryRecord{{1, throughputHistoryKindMinute, &fact, nil}}); e != nil {
				t.Fatal(e)
			}
			file, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				t.Fatal(e)
			}
			_, _ = file.WriteString(tail)
			_ = file.Close()
			before, _ := os.ReadFile(path)
			if e := maintainThroughputHistoryFile(path, now, func(string, uint64) error { return nil }); e == nil {
				t.Fatal("corrupt view was replaced")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("original changed")
			}
		})
	}
	path := filepath.Join(t.TempDir(), "throughput.jsonl")
	fact := throughputTestMinute(now.Add(-72*time.Hour), 10)
	if e := appendThroughputHistoryRecords(path, []throughputHistoryRecord{{1, throughputHistoryKindMinute, &fact, nil}}); e != nil {
		t.Fatal(e)
	}
	before, _ := os.ReadFile(path)
	e := maintainThroughputHistoryFile(path, now, func(string, uint64) error { return historyfile.ErrStorageBudget })
	if !errors.Is(e, historyfile.ErrStorageBudget) {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("low space changed original")
	}
}

func TestThroughputMaintenanceRunsWhileStoreIsOpen(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store := &throughputHistoryStore{path: filepath.Join(t.TempDir(), "throughput.jsonl")}
	fact := throughputTestMinute(now.Add(-72*time.Hour), 10)
	fact.Origin = "session_replay"
	if e := store.mergeRecoveredMinutes([]ThroughputMinuteFact{fact}, now); e != nil {
		t.Fatal(e)
	}
	fact = throughputTestMinute(now.Add(-72*time.Hour), 20)
	fact.Origin = "session_replay"
	if e := store.mergeRecoveredMinutes([]ThroughputMinuteFact{fact}, now.Add(time.Hour+time.Minute)); e != nil {
		t.Fatal(e)
	}
	lines, e := readArchivePartition(archivePartitionPath(store.path, archiveMonth(now.Add(-72*time.Hour))))
	if e != nil {
		t.Fatal(e)
	}
	if len(lines) != 1 {
		t.Fatal("cold revision was appended instead of replaced", len(lines))
	}
	row, e := parseThroughputStoredRecord(lines[0])
	if e != nil {
		t.Fatal(e)
	}
	if *row.record.Minute.OutputTokens != 20 {
		t.Fatal("latest revision lost")
	}
	reloaded, e := loadThroughputHistoryStore(filepath.Join(filepath.Dir(store.path), "history.jsonl"), now)
	if e != nil {
		t.Fatal(e)
	}
	facts, _ := reloaded.snapshot()
	if len(facts) != 1 || *facts[0].OutputTokens != 20 {
		t.Fatal("reload truth differs", facts)
	}
}
