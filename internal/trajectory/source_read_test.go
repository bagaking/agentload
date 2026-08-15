package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTrajectoryPointReadUsesCommittedPrefixWhileTailAwaitsBackfill(t *testing.T) {
	s, path, _, _, _ := indexFixture(t, request("saved first witness"))
	page := queryOne(t, s)
	id := page.Sessions[0].MatchedIDs[0]
	source := s.provider(context.Background()).Sources[0]
	before, _, err := s.store.checkpoint(context.Background(), sourceID(source))
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(strings.Repeat(request("unprepared new tail"), 1000))
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
	s.store.checkCapacity = func(string, uint64) error {
		return errors.New("point read attempted a backfill write")
	}
	got, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id, View: "raw", Around: 0})
	if err != nil || got.FocusID != id || len(got.Events) != 1 || got.RawChunk == nil {
		t.Fatal("committed reference was blocked by its growing tail", got, err)
	}
	pending := false
	for _, gap := range got.Coverage.Gaps {
		pending = pending || gap == "index_pending"
	}
	if !pending {
		t.Fatal("unprepared new tail was hidden", got.Coverage)
	}
	after, _, err := s.store.checkpoint(context.Background(), sourceID(source))
	if err != nil || after.Offset != before.Offset || after.EventCount != before.EventCount || after.Generation != before.Generation {
		t.Fatal("point read rewrote the recovery frontier", before, after, err)
	}
}

func TestTrajectoryPointReadDoesNotPrepareUnrelatedHistory(t *testing.T) {
	s, selectedPath, _, decoder, visible := indexFixture(t, request("selected match"))
	q := queryOne(t, s)
	id := q.Sessions[0].MatchedIDs[0]
	unrelated := &countingDecoder{}
	path := filepath.Join(t.TempDir(), "unprepared.jsonl")
	if err := os.WriteFile(path, []byte(request("unrelated history")), 0600); err != nil {
		t.Fatal(err)
	}
	provider := s.provider
	s.provider = func(ctx context.Context) SourceSet {
		set := provider(ctx)
		set.Sources = append(set.Sources, Source{Agent: "codex", Path: path, Decoder: unrelated})
		return set
	}
	before := decoder.calls.Load()
	got, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id, Around: 3})
	if err != nil || got.FocusID != id || len(got.Events) != 1 {
		t.Fatalf("selected source unavailable: %+v %v", got, err)
	}
	if unrelated.calls.Load() != 0 || decoder.calls.Load()-before > 16 {
		t.Fatal("point read decoded unrelated history or exceeded its bounded selected-source replay", decoder.calls.Load()-before)
	}
	*visible = false
	if _, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("withdrawn source readable: %v", err)
	}
	*visible = true
	if err := os.Remove(selectedPath); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted source must report missing, not an internal error: %v", err)
	}
}

func TestTrajectoryIncompleteDiscoveryPreservesSourceIdentity(t *testing.T) {
	s, _, _, _, _ := indexFixture(t, request("stable physical record"))
	before := queryOne(t, s).Sessions[0]
	provider := s.provider
	s.provider = func(context.Context) SourceSet {
		cov := coverage("fixture")
		gap(&cov, "discovery_cancelled")
		return SourceSet{Coverage: cov}
	}
	partial := queryOne(t, s)
	if len(partial.Sessions) != 0 || partial.Coverage.Complete {
		t.Fatal("incomplete source catalog fabricated accessible data")
	}
	s.provider = provider
	after := queryOne(t, s).Sessions[0]
	if before.ID != after.ID || before.MatchedIDs[0] != after.MatchedIDs[0] {
		t.Fatal("incomplete discovery fabricated physical replacement")
	}
}
