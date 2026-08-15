package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrajectoryWatchPreparationDeadlineRetainsCommittedPrefix(t *testing.T) {
	s, path, _ := watchFixture(t, call("baseline"))
	q := snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"}
	before := searchTestPreparedQuery(t, s, q)
	if len(before.Events) != 1 {
		t.Fatal("fixture baseline not prepared", before)
	}
	appendFile(t, path, call("later"))
	capacity := s.storageCheck
	var checks atomic.Int32
	s.storageCheck = func(path string) error {
		// Query checks capacity before it creates the preparation child. The
		// second check belongs to indexSourceQuantum, with that child active.
		if checks.Add(1) == 2 {
			time.Sleep(queryPreparationBudget + 25*time.Millisecond)
		}
		return capacity(path)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	after, err := s.Query(ctx, q)
	s.storageCheck = capacity
	if err != nil || ctx.Err() != nil || checks.Load() != 3 {
		t.Fatal("preparation child deadline was not exercised in the index quantum", err, ctx.Err(), checks.Load())
	}
	for _, label := range after.Coverage.Gaps {
		if strings.HasPrefix(label, "source_unreadable:") {
			t.Fatal("foreground preparation timeout invented a source error", after.Coverage)
		}
	}
	if len(after.Events) != 1 || after.Events[0].ID != before.Events[0].ID || after.Coverage.Complete {
		t.Fatal("pending preparation lost the committed prefix or claimed completion", after)
	}
	searchTestPreparedQuery(t, s, q)
	out := watchNext(t, s, q, before.WatchCursor)
	if out.ResetRequired || len(out.Changes) != 1 {
		t.Fatal("preparation timeout reset the source boundary", out)
	}
}

func TestTrajectoryWatchBackgroundPreparationWakesPendingRead(t *testing.T) {
	s, path, state := watchFixture(t, request("baseline"))
	q := snapshot.TrajectorySelector{Collection: "events"}
	before := searchTestPreparedQuery(t, s, q)
	if len(before.Events) != 1 {
		t.Fatal("fixture baseline not prepared", before)
	}
	src := s.provider(context.Background()).Sources[0]
	for len(state.observed) > 0 {
		<-state.observed
	}
	type receipt struct {
		result snapshot.TrajectoryWatchResult
		err    error
	}
	done := make(chan receipt, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		result, err := s.Watch(ctx, snapshot.TrajectoryWatchParams{Selector: q, Cursor: before.WatchCursor, TimeoutMS: 2000})
		done <- receipt{result, err}
	}()
	select {
	case <-state.observed:
	case <-time.After(time.Second):
		t.Fatal("watch did not establish its observation")
	}
	s.opMu.Lock()
	appendFile(t, path, request("background-only publication"))
	s.opMu.Unlock()
	// No file notification is delivered: the background owner's committed prefix
	// must wake the existing watch, without polling or a second writer append.
	_, err := s.PrepareSource(context.Background(), src, func() bool { return true })
	if err != nil {
		t.Fatal("background fixture did not commit", err)
	}
	cp, ok, err := s.store.checkpoint(context.Background(), sourceID(src))
	if err != nil || !ok || cp.EventCount != 2 {
		t.Fatal("background canonical prefix was not committed", cp, err)
	}
	select {
	case r := <-done:
		if r.err != nil || r.result.ResetRequired || len(r.result.Changes) != 1 || r.result.Changes[0].Source.Line != 2 {
			t.Fatal("committed background prefix did not wake watch", r)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("background commit did not notify the waiting consumer")
	}
}
