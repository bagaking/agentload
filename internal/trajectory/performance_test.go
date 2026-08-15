package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// This acceptance measurement is requested explicitly, so the ordinary unit
// suite does not repeat a 30k-record index build for unrelated edits.
func TestTrajectoryLongSessionAcceptance(t *testing.T) {
	proofPath := os.Getenv("AGENTLOAD_TRAJECTORY_LONG_PROOF")
	if proofPath == "" {
		t.Skip("long-session acceptance measurement not requested")
	}
	const records = 30000
	s, path, _, decoder, _ := indexFixture(t, strings.Repeat(request("historical Proxy evidence"), records))
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	started := time.Now()
	q := searchTestQuery(t, s, snapshot.TrajectorySelector{})
	// Canonical and full-text preparation is deliberately bounded per request.
	// Measure the complete cold preparation before claiming a warmed slice.
	for deadline := time.Now().Add(time.Minute); slices.Contains(q.Coverage.Gaps, "index_pending"); {
		if time.Now().After(deadline) {
			t.Fatal("long-session index did not finish within preparation deadline")
		}
		q = searchTestQuery(t, s, snapshot.TrajectorySelector{})
	}
	coldMS := float64(time.Since(started).Microseconds()) / 1000
	if len(q.Sessions) != 1 || q.Sessions[0].EventCount != records {
		t.Fatal("long-session coverage mismatch")
	}
	decoderCalls := decoder.calls.Load()
	maximumReplayed := int64(0)
	var durations []float64
	maximumBytes := 0
	for i := 0; i < 10; i++ {
		beforeReplay := decoder.calls.Load()
		start := time.Now()
		r, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: q.Sessions[0].ID, Around: 5, Raw: true})
		elapsed := float64(time.Since(start).Microseconds()) / 1000
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(r)
		maximumBytes = max(maximumBytes, len(b))
		if len(r.Events) > 11 || len(b) > MaxSliceBytes {
			t.Fatal("slice retention/budget violated")
		}
		if elapsed > 150 {
			t.Fatalf("warm slice over150ms: %.3f", elapsed)
		}
		durations = append(durations, elapsed)
		replayed := decoder.calls.Load() - beforeReplay
		maximumReplayed = max(maximumReplayed, replayed)
		if replayed > 5000 {
			t.Fatal("warm slice scanned the long session instead of bounded source ranges", replayed)
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	retained := int64(after.HeapAlloc) - int64(before.HeapAlloc)
	if retained > 16*1024*1024 {
		t.Fatalf("historical Go heap retained: %d", retained)
	}
	data, _ := os.ReadFile(path)
	proof := map[string]any{"records": records, "source_sha256_prefix": digest(data), "cold_index_ms": coldMS, "warm_slice_ms": durations, "max_result_bytes": maximumBytes, "max_events": 11, "retained_go_heap_delta_bytes": retained, "heap_limit_bytes": 16 * 1024 * 1024, "replayed_records": decoder.calls.Load() - decoderCalls, "max_replayed_records_per_slice": maximumReplayed, "replayed_record_limit_per_slice": 5000, "boundary": "authorized source-backed service; bounded on-demand replay; no native paint or whole-process RSS claim"}
	encoded, _ := json.MarshalIndent(proof, "", "  ")
	if err := os.WriteFile(proofPath, append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(s)
}
