package main

import (
	"agentload/internal/snapshot"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Cold rows -- older than the hot window but still inside the retention window --
// must end up in a month partition, leave the hot file holding only hot rows, and
// still be visible to the reader afterwards.
func TestLoadLocalHistoryStateArchivesColdSamples(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	cold := []time.Time{
		now.Add(-20 * 24 * time.Hour),
		now.Add(-15 * 24 * time.Hour),
		now.Add(-10 * 24 * time.Hour),
	}
	hot := []time.Time{
		now.Add(-3 * time.Hour),
		now.Add(-time.Hour),
	}
	var content []byte
	for i, at := range append(append([]time.Time{}, cold...), hot...) {
		raw, err := json.Marshal(makeHistorySample(at, snapshot.CurrentMetrics{PIDConcurrency: i}, snapshot.SnapshotSummary{}, nil))
		if err != nil {
			t.Fatalf("marshal sample: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	// Enough stale rows to trip compaction.
	for i := 0; i < 600; i++ {
		raw, err := json.Marshal(makeHistorySample(now.Add(-40*24*time.Hour).Add(time.Duration(i)*time.Minute), snapshot.CurrentMetrics{}, snapshot.SnapshotSummary{}, nil))
		if err != nil {
			t.Fatalf("marshal stale sample: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write history fixture: %v", err)
	}

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("loadLocalHistoryState: %v", err)
	}
	if len(state.samples) != len(cold)+len(hot) {
		t.Fatalf("expected %d retained samples, got %d", len(cold)+len(hot), len(state.samples))
	}

	hotLines := historyFileLines(t, path)
	if len(hotLines) != len(hot) {
		t.Fatalf("expected hot file to hold %d rows, got %d", len(hot), len(hotLines))
	}

	partitions, err := archivePartitionsSince(path, now.Add(-historyRetentionWindow))
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	archived, err := readArchiveLines(partitions)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(archived) != len(cold) {
		t.Fatalf("expected %d archived rows, got %d across %d partitions", len(cold), len(archived), len(partitions))
	}

	// The reader must see the same window after the split as before it.
	reloaded, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.samples) != len(cold)+len(hot) {
		t.Fatalf("expected %d samples after reload, got %d", len(cold)+len(hot), len(reloaded.samples))
	}
	if reloaded.corruptLineCount != 0 {
		t.Fatalf("archive round trip reported %d corrupt lines", reloaded.corruptLineCount)
	}
	if first, ok := historySampleTime(reloaded.samples[0]); !ok || !first.Equal(cold[0]) {
		t.Fatalf("expected oldest archived sample first, got %+v", reloaded.samples[0].At)
	}
}

// Cold rows spanning two months must land in two partitions, and a later
// compaction must merge into the existing partition rather than replace it.
func TestArchivePartitionsSpanMonthsAndMergeOnSecondCompaction(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	writeHistoryLines(t, path, now, []time.Time{
		now.Add(-40 * 24 * time.Hour), // outside retention, dropped
		now.Add(-29 * 24 * time.Hour), // cold, May
		now.Add(-28 * 24 * time.Hour), // cold, May
		now.Add(-10 * 24 * time.Hour), // cold, June
		now.Add(-time.Hour),           // hot
	}, 600)

	if _, err := loadLocalHistoryState(path, now); err != nil {
		t.Fatalf("first load: %v", err)
	}
	partitions, err := archivePartitionsSince(path, now.Add(-historyRetentionWindow))
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	if len(partitions) != 2 {
		t.Fatalf("expected 2 month partitions, got %d: %v", len(partitions), partitions)
	}

	// A second batch of cold rows in an already-archived month must be added to
	// that partition, not overwrite it. Rewriting is idempotent only if the
	// existing rows are read back in first.
	writeHistoryLines(t, path, now, []time.Time{
		now.Add(-27 * 24 * time.Hour),
		now.Add(-2 * time.Hour),
	}, 600)
	if _, err := loadLocalHistoryState(path, now); err != nil {
		t.Fatalf("second load: %v", err)
	}
	partitions, err = archivePartitionsSince(path, now.Add(-historyRetentionWindow))
	if err != nil {
		t.Fatalf("relist partitions: %v", err)
	}
	archived, err := readArchiveLines(partitions)
	if err != nil {
		t.Fatalf("read merged archive: %v", err)
	}
	if len(archived) != 4 {
		t.Fatalf("expected 4 archived rows after merge, got %d", len(archived))
	}
}

// A crash between archiving and truncating the hot file leaves a row in both
// places. That duplicate must be merged by timestamp, never counted twice.
func TestArchiveAndHotDuplicateIsMergedNotDoubleCounted(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	coldAt := now.Add(-10 * 24 * time.Hour)
	hotAt := now.Add(-time.Hour)
	writeHistoryLines(t, path, now, []time.Time{coldAt, hotAt}, 600)
	if _, err := loadLocalHistoryState(path, now); err != nil {
		t.Fatalf("load: %v", err)
	}

	// Simulate the crash window: the archive holds the cold row and the hot file
	// still holds it too.
	raw, err := json.Marshal(makeHistorySample(coldAt, snapshot.CurrentMetrics{PIDConcurrency: 7}, snapshot.SnapshotSummary{}, nil))
	if err != nil {
		t.Fatalf("marshal duplicate: %v", err)
	}
	existing, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hot file: %v", err)
	}
	if err := os.WriteFile(path, append(append(raw, '\n'), existing...), 0o644); err != nil {
		t.Fatalf("seed duplicate: %v", err)
	}

	state, err := loadLocalHistoryState(path, now)
	if err != nil {
		t.Fatalf("reload with duplicate: %v", err)
	}
	if len(state.samples) != 2 {
		t.Fatalf("expected duplicate to merge into 2 samples, got %d", len(state.samples))
	}
	seen := map[string]int{}
	for _, sample := range state.samples {
		seen[sample.At]++
	}
	for at, count := range seen {
		if count != 1 {
			t.Fatalf("sample %s appears %d times after merge", at, count)
		}
	}
}

// A damaged partition must surface as an error. Reporting an unreadable archive
// as an empty history would silently erase the retained window, and the hot file
// must be left untouched so the rows it still holds are not lost too.
func TestUnreadableArchivePartitionFailsLoudlyAndKeepsHotFile(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")

	writeHistoryLines(t, path, now, []time.Time{now.Add(-10 * 24 * time.Hour), now.Add(-time.Hour)}, 600)
	if _, err := loadLocalHistoryState(path, now); err != nil {
		t.Fatalf("load: %v", err)
	}
	partitions, err := archivePartitionsSince(path, now.Add(-historyRetentionWindow))
	if err != nil || len(partitions) == 0 {
		t.Fatalf("expected a partition, got %v (%v)", partitions, err)
	}
	intact, err := os.ReadFile(partitions[0])
	if err != nil {
		t.Fatalf("read partition: %v", err)
	}
	hotBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read hot file: %v", err)
	}
	if err := os.WriteFile(partitions[0], intact[:len(intact)/2], 0o644); err != nil {
		t.Fatalf("truncate partition: %v", err)
	}

	if _, err := loadLocalHistoryState(path, now); err == nil {
		t.Fatal("expected truncated archive to fail the load instead of reading as empty history")
	}
	hotAfter, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reread hot file: %v", err)
	}
	if string(hotAfter) != string(hotBefore) {
		t.Fatal("hot file was modified while the archive was unreadable")
	}
}

func TestThroughputHistoryArchivesColdRecords(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC).Truncate(throughputMinuteResolution)
	dir := t.TempDir()
	historyPath := filepath.Join(dir, "history.jsonl")
	path := throughputHistoryPath(historyPath)

	var content []byte
	appendMinute := func(at time.Time) {
		minute := throughputTestMinute(at, 120)
		raw, err := json.Marshal(throughputHistoryRecord{SchemaVersion: throughputHistorySchemaVersion, Kind: throughputHistoryKindMinute, Minute: &minute})
		if err != nil {
			t.Fatalf("marshal minute: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	cold := []time.Time{now.Add(-20 * 24 * time.Hour), now.Add(-9 * 24 * time.Hour)}
	for _, at := range cold {
		appendMinute(at)
	}
	appendMinute(now.Add(-time.Hour))
	for i := 0; i < 600; i++ {
		appendMinute(now.Add(-40 * 24 * time.Hour).Add(time.Duration(i) * time.Minute))
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write throughput fixture: %v", err)
	}

	store, err := loadThroughputHistoryStore(historyPath, now)
	if err != nil {
		t.Fatalf("loadThroughputHistoryStore: %v", err)
	}
	if len(store.minutes) != 3 {
		t.Fatalf("expected 3 retained minutes, got %d", len(store.minutes))
	}
	if lines := historyFileLines(t, path); len(lines) != 1 {
		t.Fatalf("expected hot throughput file to hold 1 row, got %d", len(lines))
	}
	partitions, err := archivePartitionsSince(path, now.Add(-historyRetentionWindow))
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	archived, err := readArchiveLines(partitions)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(archived) != len(cold) {
		t.Fatalf("expected %d archived records, got %d", len(cold), len(archived))
	}

	reloaded, err := loadThroughputHistoryStore(historyPath, now)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.minutes) != 3 {
		t.Fatalf("expected 3 minutes after reload, got %d", len(reloaded.minutes))
	}
	if reloaded.corruptRecordCount != 0 {
		t.Fatalf("archive round trip reported %d corrupt records", reloaded.corruptRecordCount)
	}
}

// The compacted files must stay group/world readable. A missing Chmod silently
// left throughput.jsonl at 0600 while its siblings were 0644.
func TestCompactedStoresKeepReadablePermissions(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "history.jsonl")
	writeHistoryLines(t, path, now, []time.Time{now.Add(-10 * 24 * time.Hour), now.Add(-time.Hour)}, 600)
	if _, err := loadLocalHistoryState(path, now); err != nil {
		t.Fatalf("load: %v", err)
	}
	assertMode := func(target string) {
		info, err := os.Stat(target)
		if err != nil {
			t.Fatalf("stat %s: %v", target, err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Fatalf("%s has mode %v, want 0644", filepath.Base(target), info.Mode().Perm())
		}
	}
	assertMode(path)
	partitions, err := archivePartitionsSince(path, now.Add(-historyRetentionWindow))
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	for _, partition := range partitions {
		assertMode(partition)
	}
}

func TestLifecycleLogCompactionArchivesColdEvents(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	historyPath := filepath.Join(t.TempDir(), "AgentLoad", "history.jsonl")
	log := newLifecycleLog(historyPath)

	var content []byte
	for _, at := range []time.Time{
		now.Add(-20 * 24 * time.Hour),
		now.Add(-10 * 24 * time.Hour),
		now.Add(-time.Hour),
	} {
		raw, err := json.Marshal(lifecycleEvent{At: at.Format(time.RFC3339Nano), Event: "heartbeat"})
		if err != nil {
			t.Fatalf("marshal event: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	if err := os.MkdirAll(filepath.Dir(log.path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(log.path, content, 0o644); err != nil {
		t.Fatalf("write lifecycle fixture: %v", err)
	}

	if err := log.compact(now); err != nil {
		t.Fatalf("compact: %v", err)
	}
	if lines := historyFileLines(t, log.path); len(lines) != 1 {
		t.Fatalf("expected 1 hot lifecycle event, got %d", len(lines))
	}
	partitions, err := archivePartitionsSince(log.path, now.Add(-historyRetentionWindow))
	if err != nil {
		t.Fatalf("list partitions: %v", err)
	}
	archived, err := readArchiveLines(partitions)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if len(archived) != 2 {
		t.Fatalf("expected 2 archived lifecycle events, got %d", len(archived))
	}
	// Appending after a compaction must still work on the untouched write path.
	if err := log.record(lifecycleEvent{Event: "startup"}); err != nil {
		t.Fatalf("record after compaction: %v", err)
	}
	if lines := historyFileLines(t, log.path); len(lines) != 2 {
		t.Fatalf("expected 2 hot events after append, got %d", len(lines))
	}
}

// A recorded snapshot is mirrored into history.jsonl, which holds a superset of
// the process rosters, so lifecycle omits them. An aborted snapshot never
// reaches history, so there they are the only surviving evidence.
func TestLifecycleKeepsProcessRostersOnlyWhereHistoryHasNone(t *testing.T) {
	snap := snapshot.Snapshot{
		RuntimeProcesses: []snapshot.ProcessRuntimeSummary{{Tool: "claude", PIDCount: 2}},
		HostAppProcesses: []snapshot.HostAppProcessSummary{{Name: "agentload", PIDCount: 1}},
	}

	recorded := lifecycleEventFromSnapshot("snapshot_recorded", "", snap)
	if len(recorded.HostAppProcesses) != 0 || len(recorded.RuntimeProcesses) != 0 {
		t.Fatalf("snapshot_recorded must not repeat rosters history already stores, got %d host / %d runtime",
			len(recorded.HostAppProcesses), len(recorded.RuntimeProcesses))
	}

	aborted := lifecycleEventFromSnapshot("snapshot_aborted", "scan cancelled", snap)
	if len(aborted.HostAppProcesses) != 1 || len(aborted.RuntimeProcesses) != 1 {
		t.Fatalf("snapshot_aborted must keep rosters as the only record, got %d host / %d runtime",
			len(aborted.HostAppProcesses), len(aborted.RuntimeProcesses))
	}
}

func historyFileLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// writeHistoryLines seeds a hot file with samples at the given times plus enough
// out-of-retention rows to trip the compaction threshold.
func writeHistoryLines(t *testing.T, path string, now time.Time, times []time.Time, stale int) {
	t.Helper()
	var content []byte
	if existing, err := os.ReadFile(path); err == nil {
		content = existing
	}
	for i, at := range times {
		raw, err := json.Marshal(makeHistorySample(at, snapshot.CurrentMetrics{PIDConcurrency: i}, snapshot.SnapshotSummary{}, nil))
		if err != nil {
			t.Fatalf("marshal sample: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	for i := 0; i < stale; i++ {
		raw, err := json.Marshal(makeHistorySample(now.Add(-40*24*time.Hour).Add(time.Duration(i)*time.Minute), snapshot.CurrentMetrics{}, snapshot.SnapshotSummary{}, nil))
		if err != nil {
			t.Fatalf("marshal stale sample: %v", err)
		}
		content = append(content, append(raw, '\n')...)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}
