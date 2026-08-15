package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func readinessFixture(t *testing.T) (*Service, *sourceState, sourceReadiness, []sourceFact) {
	t.Helper()
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("needle first")+call("readiness-call")+result("readiness-call")+request("needle last"))
	facts := canonicalFixtureFacts(t, s, st)
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if _, err := s.store.db.Exec("UPDATE sources SET search_count=0,search_offset=-1,search_block=-1,search_entity_gaps=0 WHERE id=? AND active=1", st.ID); err != nil {
		t.Fatal(err)
	}
	ready, err := s.store.readiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return s, st, ready[st.ID], facts
}

type readinessSlowDecoder struct {
	calls int
	delay bool
}

func (d *readinessSlowDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	d.calls++
	if d.delay {
		time.Sleep(40 * time.Millisecond)
	}
	return (CodexDecoder{}).Decode(raw, ctx)
}

func TestTrajectoryReadinessFinishesOneRangeAndSkipsExhaustedRanges(t *testing.T) {
	d := &readinessSlowDecoder{}
	large := strings.Repeat("recorded evidence ", 10000)
	// The zero-fact range must not cause a stalled cursor after the first hit.
	body := request(large) + strings.Repeat(" ", len(large)) + "\n" + request(large) + request(large)
	s, st := replayFixture(t, "codex", d, body)
	facts := canonicalFixtureFacts(t, s, st)
	if len(facts) != 3 {
		t.Fatal("fixture did not retain its three canonical facts", len(facts))
	}
	if _, err := s.store.db.Exec("UPDATE sources SET search_count=0,search_offset=-1,search_block=-1,search_entity_gaps=0 WHERE id=? AND active=1", st.ID); err != nil {
		t.Fatal(err)
	}
	d.calls, d.delay = 0, true
	for i, fact := range facts {
		cov := coverage("bounded range preparation")
		if err := s.syncSearchScope(context.Background(), []*sourceState{st}, &cov, false, 1); err != nil {
			t.Fatal("soft quantum discarded its first complete unit", err)
		}
		ready, err := s.store.readiness(context.Background())
		p := ready[st.ID]
		if err != nil || p.count != i+1 || p.offset != fact.offset || p.block != fact.block || d.calls != i+1 {
			t.Fatal("range progress reread completed/empty units or crossed into another unit", p, d.calls, err)
		}
	}
	d.delay = false
}

func TestTrajectoryReadinessCommitSurvivesChildCancellation(t *testing.T) {
	s, st, before, facts := readinessFixture(t)
	parent := context.Background()
	child, cancel := context.WithCancel(parent)
	defer cancel()
	calls := 0
	s.store.checkCapacity = func(string, uint64) error {
		calls++
		if child.Err() != nil || parent.Err() != nil {
			t.Fatal("cancellation hook ran before completed verification")
		}
		cancel()
		return nil
	}
	got, err := s.store.advanceReadiness(child, st, before)
	if err != nil || calls != 1 || got.count != len(facts) || !errors.Is(child.Err(), context.Canceled) {
		t.Fatal("verified readiness did not survive commit cancellation", got, err, calls)
	}
	last := facts[len(facts)-1]
	if got.offset != last.offset || got.block != last.block {
		t.Fatal("committed an unverified physical frontier", got, last)
	}
	s.store.checkCapacity = historyfile.CheckStorageCapacity
	durable, err := s.store.readiness(parent)
	if err != nil || !reflect.DeepEqual(durable[st.ID], got) {
		t.Fatal("frontier was not durable", durable, err)
	}
	query := searchTestQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Limit: 50})
	ids, expected := []string{}, []string{}
	for _, e := range query.Events {
		ids = append(ids, e.ID)
	}
	for _, fact := range facts {
		expected = append(expected, fact.event.ID)
	}
	if !reflect.DeepEqual(ids, expected) {
		t.Fatal("readiness changed canonical identities", ids, expected)
	}
}

func TestTrajectoryReadinessRefusalDoesNotAdvance(t *testing.T) {
	for _, mode := range []string{"cancelled", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			s, st, before, _ := readinessFixture(t)
			var revisionBefore int
			if err := s.store.db.QueryRow("SELECT CAST(value AS INTEGER) FROM meta WHERE key='revision'").Scan(&revisionBefore); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := context.Canceled
			if mode == "cancelled" {
				cancel()
			} else {
				want = historyfile.ErrStorageBudget
				s.store.checkCapacity = func(string, uint64) error { return want }
			}
			if _, err := s.store.advanceReadiness(ctx, st, before); !errors.Is(err, want) {
				t.Fatal("refusal lost its cause", err)
			}
			durable, err := s.store.readiness(context.Background())
			var revisionAfter int
			if err != nil || !reflect.DeepEqual(durable[st.ID], before) {
				t.Fatal("refusal changed readiness", durable, err)
			}
			if err = s.store.db.QueryRow("SELECT CAST(value AS INTEGER) FROM meta WHERE key='revision'").Scan(&revisionAfter); err != nil || revisionAfter != revisionBefore {
				t.Fatal("refusal changed revision", revisionAfter, revisionBefore, err)
			}
		})
	}
}

func TestTrajectoryReadinessPublicCancellationDoesNotPublish(t *testing.T) {
	s, st, _, facts := readinessFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	s.store.checkCapacity = func(string, uint64) error {
		calls++
		cancel()
		return nil
	}
	query, err := s.Query(ctx, snapshot.TrajectorySelector{Collection: "events"})
	if !errors.Is(err, context.Canceled) || len(query.Events) != 0 || calls != 1 {
		t.Fatal("cancelled caller published a result", query, err, calls)
	}
	durable, err := s.store.readiness(context.Background())
	if err != nil || durable[st.ID].count != len(facts) {
		t.Fatal("cancelled caller discarded an already verified frontier", durable, err)
	}
}
