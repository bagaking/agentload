package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrajectorySourceQueryDefaultDoesNotCountSessionTail(t *testing.T) {
	d := &countingDecoder{}
	s, st := replayFixture(t, "codex", d, strings.Repeat(request("research "+strings.Repeat("recorded evidence ", 80)), 1000))
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
	d.calls.Store(0)
	selector := snapshot.TrajectorySelector{Collection: "sessions", Text: "research", Limit: 1}
	page, err := f.query(context.Background(), selector, []*sourceState{st}, coverage("test"))
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].MatchedCount != nil || len(page.Sessions[0].MatchedIDs) != 1 {
		t.Fatal("default did not return an exact witness with unknown count", page, err)
	}
	if calls := d.calls.Load(); calls <= 0 || calls >= 1000 {
		t.Fatal("default decoded the entire matching session", calls)
	}
	wire, err := json.Marshal(page)
	if err != nil || !strings.Contains(string(wire), `"matched_count":null`) || strings.Contains(string(wire), `"matched_total"`) {
		t.Fatal("unknown count was fabricated or omitted", string(wire), err)
	}
	selector.Count = true
	counted, err := f.query(context.Background(), selector, []*sourceState{st}, coverage("test"))
	if err != nil || counted.MatchedTotal == nil || *counted.MatchedTotal != 1 || exactMatchCount(counted.Sessions[0].MatchedCount) != 1000 || len(counted.Sessions[0].MatchedIDs) != 50 || counted.Sessions[0].MatchedIDs[0] != page.Sessions[0].MatchedIDs[0] {
		t.Fatal("explicit count lost exact counts or changed the default witness", counted, err)
	}
}

func exactMatchCount(count *int) int {
	if count == nil {
		return -1 // Unknown cannot satisfy an exact count assertion, including 0.
	}
	return *count
}

func sameSessionWitnesses(plain, counted []snapshot.TrajectorySession) bool {
	if len(plain) != len(counted) {
		return false
	}
	for i, p := range plain {
		c := counted[i]
		if p.MatchedCount != nil || c.MatchedCount == nil || len(p.MatchedIDs) == 0 || len(c.MatchedIDs) < len(p.MatchedIDs) {
			return false
		}
		c.MatchedCount = nil
		c.MatchedIDs = c.MatchedIDs[:len(p.MatchedIDs)]
		if !reflect.DeepEqual(p, c) {
			return false
		}
	}
	return true
}

type pageCountDecoder struct {
	calls   atomic.Int64
	entered chan<- struct{}
	release <-chan struct{}
}

func (d *pageCountDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	if d.calls.Add(1) == 3 && d.entered != nil {
		// Both physical records were decoded for the discovery witness. Hold
		// the first record of the subsequent exact count until readers join.
		d.entered <- struct{}{}
		<-d.release
	}
	return (CodexDecoder{}).Decode(raw, ctx)
}

func TestTrajectorySourceQueryPageCountsJoinBeforePublication(t *testing.T) {
	for _, mode := range []string{"success", "cancel", "replace"} {
		t.Run(mode, func(t *testing.T) {
			f := newSourceStoreFixture(t)
			var states []*sourceState
			entered, release := make(chan struct{}, 6), make(chan struct{})
			for i := 0; i < 6; i++ {
				d := &pageCountDecoder{}
				s, st := replayFixture(t, "codex", d, request("research first")+request("research second"))
				importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
				d.calls.Store(0)
				d.entered, d.release = entered, release
				states = append(states, st)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type result struct {
				page snapshot.TrajectoryQueryResult
				err  error
			}
			done := make(chan result, 1)
			go func() {
				page, err := f.query(ctx, snapshot.TrajectorySelector{Collection: "sessions", Text: "research", Limit: 6, Count: true}, states, coverage("test"))
				done <- result{page, err}
			}()
			timer := time.NewTimer(3 * time.Second)
			defer timer.Stop()
			for i := 0; i < 4; i++ {
				select {
				case <-entered:
				case <-timer.C:
					close(release)
					<-done
					t.Fatal("selected session counts were serialized")
				}
			}
			select {
			case <-entered:
				close(release)
				<-done
				t.Fatal("more than four source readers ran concurrently")
			case <-done:
				close(release)
				t.Fatal("page published before all exact readers joined")
			default:
			}
			if mode == "cancel" {
				cancel()
			} else if mode == "replace" {
				if err := os.WriteFile(states[0].Path, []byte(request("changed body")), 0600); err != nil {
					close(release)
					<-done
					t.Fatal(err)
				}
			}
			close(release)
			r := <-done
			if mode == "success" {
				if r.err != nil || len(r.page.Sessions) != 6 {
					t.Fatal("concurrent counts failed", r.page, r.err)
				}
				for _, session := range r.page.Sessions {
					if exactMatchCount(session.MatchedCount) != 2 || len(session.MatchedIDs) != 2 {
						t.Fatal("count or canonical references changed", session)
					}
				}
			} else {
				want := ErrStale
				if mode == "cancel" {
					want = context.Canceled
				}
				if !errors.Is(r.err, want) || len(r.page.Sessions) != 0 || len(r.page.Events) != 0 || r.page.Next != "" || r.page.MatchedTotal != nil {
					t.Fatal("failed reader leaked a partial page", r.page, r.err)
				}
			}
		})
	}
}

func TestTrajectorySourceQueryExactPagesCountAndScope(t *testing.T) {
	f := newSourceStoreFixture(t)
	states := []*sourceState{}
	for _, body := range []string{
		request("research first") + request("research second") + request("different"),
		request("research another"),
		request("research older") + request("research twice"),
		request("no match"),
	} {
		s, st := replayFixture(t, "codex", CodexDecoder{}, body)
		importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
		states = append(states, st)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].checkpoint.Mtime > states[j].checkpoint.Mtime })
	q := snapshot.TrajectorySelector{Collection: "sessions", Text: "research", Limit: 1}
	page, err := f.query(context.Background(), q, states, coverage("current authorized sources"))
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.MatchedTotal != nil || page.Next == "" {
		t.Fatal("default page/count contract changed", page)
	}
	first := page.Sessions[0]
	if first.ID != sessionID(states[1]) || first.MatchedCount != nil || len(first.MatchedIDs) != 1 {
		t.Fatal("default witness or unknown count changed", first)
	}
	q.Cursor = page.Next
	second, err := f.query(context.Background(), q, states, coverage("current authorized sources"))
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].ID != sessionID(states[2]) || second.Sessions[0].MatchedCount != nil || len(second.Sessions[0].MatchedIDs) != 1 {
		t.Fatal("second exact page changed", second)
	}
	q.Cursor = ""
	q.Count = true
	all, err := f.query(context.Background(), q, states, coverage("current authorized sources"))
	if err != nil || all.MatchedTotal == nil || *all.MatchedTotal != 3 {
		t.Fatal("explicit session count changed", all, err)
	}
	if exactMatchCount(all.Sessions[0].MatchedCount) != 2 || len(all.Sessions[0].MatchedIDs) != 2 || all.Sessions[0].MatchedIDs[0] != first.MatchedIDs[0] {
		t.Fatal("explicit count lost exact references or changed the default witness", all)
	}
	q.Collection = "events"
	all, err = f.query(context.Background(), q, states, coverage("current authorized sources"))
	if err != nil || all.MatchedTotal == nil || *all.MatchedTotal != 5 {
		t.Fatal("explicit event count changed", all, err)
	}
	q.Agent = "claude"
	q.Collection = "sessions"
	none, err := f.query(context.Background(), q, states, coverage("current authorized sources"))
	if err != nil || *none.MatchedTotal != 0 || len(none.Sessions) != 0 || none.Sessions == nil {
		t.Fatal("vendor scope leaked", none, err)
	}
	q.Agent = ""
	q.Collection = "events"
	current, err := f.query(context.Background(), q, states[2:3], coverage("restricted provider"))
	if err != nil || *current.MatchedTotal != 1 {
		t.Fatal("withdrawn sources influenced count", current, err)
	}
}

func TestTrajectorySourceQueryDefaultStopsBeforeUnneededHistoricalBody(t *testing.T) {
	f := newSourceStoreFixture(t)
	states := []*sourceState{}
	for i := 0; i < 4; i++ {
		s, st := replayFixture(t, "codex", CodexDecoder{}, request("research"))
		importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
		states = append(states, st)
	}
	sort.Slice(states, func(i, j int) bool { return states[i].checkpoint.Mtime > states[j].checkpoint.Mtime })
	// The oldest body is outside this page and its exact lookahead. Default
	// existence discovery should not turn into an implicit full-corpus count.
	if err := os.WriteFile(states[3].Path, []byte(request("rewritten")), 0600); err != nil {
		t.Fatal(err)
	}
	q := snapshot.TrajectorySelector{Collection: "sessions", Text: "research", Limit: 1}
	page, err := f.query(context.Background(), q, states, coverage("current authorized sources"))
	if err != nil || len(page.Sessions) != 1 || page.Next == "" {
		t.Fatal("default visited an unnecessary old body", page, err)
	}
	q.Count = true
	failed, err := f.query(context.Background(), q, states, coverage("current authorized sources"))
	if !errors.Is(err, ErrStale) || len(failed.Sessions) != 0 || failed.MatchedTotal != nil {
		t.Fatal("count skipped a changed source or published discovery witnesses", failed, err)
	}
}

func TestTrajectorySourceQueryCanonicalAndSearchReadinessStaySeparate(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("research first")+request("research second")+request("research third"))
	facts := canonicalFixtureFacts(t, s, st)
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, facts)
	if err := f.completeSource(context.Background(), st, 2, facts[1].offset, facts[1].block); err != nil {
		t.Fatal(err)
	}
	cov := coverage("current authorized sources")
	cov.Index = &snapshot.TrajectoryIndexProgress{KnownSources: 1, DecodedSources: 1, DecodedEvents: 3}
	q := snapshot.TrajectorySelector{Collection: "sessions", Text: "research", Count: true}
	got, err := f.query(context.Background(), q, []*sourceState{st}, cov)
	if err != nil || got.MatchedTotal == nil || *got.MatchedTotal != 1 || len(got.Sessions) != 1 || exactMatchCount(got.Sessions[0].MatchedCount) != 2 {
		t.Fatal("canonical tail was exposed as searchable", got, err)
	}
	if got.Coverage.Complete || got.Coverage.Index.SearchableEvents != 2 || got.Coverage.Index.DecodedEvents != 3 {
		t.Fatal("preparation gaps erased", got.Coverage)
	}
	if cov.Index.SearchableEvents != 0 {
		t.Fatal("query mutated caller coverage")
	}
	third, err := f.event(context.Background(), st, facts[2].offset, facts[2].block)
	if err != nil || !reflect.DeepEqual(third, facts[2].event) {
		t.Fatal("canonical point read incorrectly waits for search", err)
	}
	oldCursor := got.Next
	q.Collection = "events"
	q.Limit = 1
	first, err := f.query(context.Background(), q, []*sourceState{st}, cov)
	if err != nil || first.Next == "" {
		t.Fatal(first, err)
	}
	oldCursor = first.Next
	if err = f.completeSource(context.Background(), st, 3, facts[2].offset, facts[2].block); err != nil {
		t.Fatal(err)
	}
	q.Cursor = oldCursor
	if _, err = f.query(context.Background(), q, []*sourceState{st}, cov); !errors.Is(err, ErrStale) {
		t.Fatal("backfill reused an old result cursor", err)
	}
	q.Cursor = ""
	got, err = f.query(context.Background(), q, []*sourceState{st}, cov)
	if err != nil || *got.MatchedTotal != 3 || got.Coverage.Index.SearchableEvents != 3 {
		t.Fatal("search backfill failed", got, err)
	}
}

func TestTrajectorySourceQueryEntityGapSurvivesNegativeCandidate(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("one"))
	facts := canonicalFixtureFacts(t, s, st)
	facts[0].event.EntityCoverage = &snapshot.TrajectoryCoverage{Scope: "legacy", Complete: false, Gaps: []string{"entity-size-limit"}}
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, facts)
	q := snapshot.TrajectorySelector{Collection: "events", EntityID: "ent." + digest([]byte("entity-not-present")), Count: true}
	got, err := f.query(context.Background(), q, []*sourceState{st}, coverage("current authorized sources"))
	if err != nil || got.MatchedTotal == nil || *got.MatchedTotal != 0 || len(got.Events) != 0 {
		t.Fatal(got, err)
	}
	if got.Coverage.Complete {
		t.Fatal("negative candidate hid entity extraction gaps")
	}
	found := false
	for _, gap := range got.Coverage.Gaps {
		found = found || gap == "entity_coverage_incomplete"
	}
	if !found {
		t.Fatal("query omitted entity coverage boundary", got.Coverage)
	}
}
