package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type watchFixtureState struct {
	access   atomic.Bool
	visible  atomic.Bool
	revision atomic.Uint64
	reads    atomic.Int64
	observed chan struct{}
	notify   func()
}

func watchFixture(t *testing.T, body string) (*Service, string, *watchFixtureState) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	state := &watchFixtureState{observed: make(chan struct{}, 32)}
	state.access.Store(true)
	state.visible.Store(true)
	s := New(func(context.Context) SourceSet {
		state.reads.Add(1)
		select {
		case state.observed <- struct{}{}:
		default:
		}
		set := SourceSet{Revision: state.revision.Load(), Coverage: coverage("authorized fixture roots")}
		if !state.access.Load() {
			gap(&set.Coverage, "content_access_disabled")
		} else if state.visible.Load() {
			set.Sources = []Source{{Agent: "codex", Path: path, NativeID: "fixture-session", Decoder: CodexDecoder{}}}
		}
		return set
	})
	state.notify = func() { state.revision.Add(1); s.NotifyEvidence() }
	t.Cleanup(func() { _ = s.Close() })
	return s, path, state
}

func watchBaseline(t *testing.T, s *Service, q snapshot.TrajectorySelector) string {
	t.Helper()
	// A foreground preparation quantum is not a barrier for this fixture's
	// initial history. Prepare its committed baseline before testing appends.
	for _, src := range s.provider(context.Background()).Sources {
		for attempt := 0; ; attempt++ {
			more, err := s.PrepareSource(context.Background(), src, func() bool { return true })
			if err != nil {
				t.Fatal("watch fixture preparation", err)
			}
			if !more {
				break
			}
			if attempt == 31 {
				t.Fatal("watch fixture did not finish preparation")
			}
		}
	}
	result, err := s.Query(context.Background(), q)
	if err != nil || result.WatchCursor == "" {
		t.Fatalf("query lacks atomic watch baseline: %+v %v", result, err)
	}
	return result.WatchCursor
}
func watchNext(t *testing.T, s *Service, q snapshot.TrajectorySelector, cursor string) snapshot.TrajectoryWatchResult {
	t.Helper()
	result, err := s.Watch(context.Background(), snapshot.TrajectoryWatchParams{Selector: q, Cursor: cursor, TimeoutMS: 10})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTrajectoryWatchQuerySearchProgressDoesNotResetBaseline(t *testing.T) {
	s, path, state := watchFixture(t, call("baseline-call"))
	provider := s.provider
	s.provider = func(ctx context.Context) SourceSet {
		set := provider(ctx)
		gap(&set.Coverage, "outside_content_roots:fixture")
		return set
	}
	q := snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"}
	cursor := watchBaseline(t, s, q)
	for i := 0; i < 2; i++ {
		out := watchNext(t, s, q, cursor)
		if out.ResetRequired || len(out.Changes) != 0 {
			t.Fatalf("unchanged source reset query baseline after search preparation: %+v", out)
		}
		cursor = out.Cursor
	}
	appendFile(t, path, call("new-call"))
	state.notify()
	out := watchNext(t, s, q, cursor)
	if out.ResetRequired || len(out.Changes) != 1 {
		t.Fatalf("index progress with unchanged gaps reset the event stream: %+v", out)
	}
}

func TestTrajectoryWatchReadinessGapsDoNotResetObservation(t *testing.T) {
	s, _, _ := watchFixture(t, call("baseline-call"))
	provider := s.provider
	pending := true
	s.provider = func(ctx context.Context) SourceSet {
		set := provider(ctx)
		gap(&set.Coverage, "outside_content_roots:fixture")
		if pending {
			gap(&set.Coverage, "index_pending")
			gap(&set.Coverage, "search_index_pending")
		}
		return set
	}
	q := snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"}
	cursor := watchBaseline(t, s, q)
	pending = false
	out := watchNext(t, s, q, cursor)
	if out.ResetRequired || len(out.Changes) != 0 || out.Coverage.Complete {
		t.Fatalf("readiness change invalidated unchanged evidence or concealed its gap: %+v", out)
	}
	s.provider = func(ctx context.Context) SourceSet {
		set := provider(ctx)
		gap(&set.Coverage, "outside_content_roots:fixture")
		gap(&set.Coverage, "source_unreadable:fixture")
		return set
	}
	if changed := watchNext(t, s, q, out.Cursor); !changed.ResetRequired {
		t.Fatal("an actual observation gap kept the old baseline", changed)
	}
}

func TestTrajectoryWatchQueryBoundaryReplayAndFilters(t *testing.T) {
	s, path, state := watchFixture(t, request("baseline"))
	q := snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command", Text: "head", Limit: 1}
	cursor := watchBaseline(t, s, q)
	// The append occurs after query and before watch starts. It must not be
	// absorbed into a new baseline or lost between the two API calls.
	appendFile(t, path, request("head in unrelated prose")+call("call-one")+result("call-one"))
	state.notify()
	first := watchNext(t, s, q, cursor)
	if first.ResetRequired || len(first.Changes) != 1 || first.Changes[0].EventKind != "tool_call" || first.Cursor == cursor {
		t.Fatalf("query/watch boundary lost an append or ignored predicates: %+v", first)
	}
	query, err := s.Query(context.Background(), q)
	if err != nil || len(query.Events) != 1 || first.Changes[0].EventID != query.Events[0].ID || first.Revision != query.Revision {
		t.Fatalf("watch has different event identity or meaning: %+v %+v %v", first, query, err)
	}
	replay := watchNext(t, s, q, cursor)
	if len(replay.Changes) != 1 || replay.Changes[0] != first.Changes[0] {
		t.Fatalf("same cursor is not replayable: %+v", replay)
	}
	next := watchNext(t, s, q, first.Cursor)
	if len(next.Changes) != 0 || next.ResetRequired {
		t.Fatalf("cursor continuation duplicated consumed event: %+v", next)
	}
	encoded, _ := json.Marshal(first)
	if len(encoded) > WatchResponseBytes || strings.Contains(string(encoded), "curl") || strings.Contains(string(encoded), "HTTP 200") || strings.Contains(string(encoded), `"text":`) || strings.Contains(string(encoded), `"raw":`) {
		t.Fatal("change notification contains transcript body or exceeds byte budget")
	}
}

func TestTrajectoryWatchUsesNotificationWithoutPolling(t *testing.T) {
	s, path, state := watchFixture(t, request("baseline"))
	q := snapshot.TrajectorySelector{Collection: "events"}
	cursor := watchBaseline(t, s, q)
	for len(state.observed) > 0 {
		<-state.observed
	}
	done := make(chan snapshot.TrajectoryWatchResult, 1)
	errors := make(chan error, 1)
	go func() {
		out, err := s.Watch(context.Background(), snapshot.TrajectoryWatchParams{Selector: q, Cursor: cursor, TimeoutMS: 1000})
		done <- out
		errors <- err
	}()
	select {
	case <-state.observed:
	case <-time.After(time.Second):
		t.Fatal("watch did not establish its authorized snapshot")
	}
	// This lock establishes that the first observation has finished. A file
	// append without a registry callback must not start a second observation.
	s.opMu.Lock()
	appendFile(t, path, request("notification-only sentinel"))
	s.opMu.Unlock()
	before := state.reads.Load()
	select {
	case result := <-done:
		t.Fatalf("unnotified append was polled: %+v", result)
	case <-time.After(25 * time.Millisecond):
	}
	if state.reads.Load() != before {
		t.Fatal("idle watch repeatedly consulted provider")
	}
	state.notify()
	select {
	case result := <-done:
		if err := <-errors; err != nil || len(result.Changes) != 1 {
			t.Fatalf("registry callback did not wake shared watch: %+v %v", result, err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watch ignored registry notification")
	}
}

func TestTrajectoryWatchBoundedBatchesAndIndependentConsumers(t *testing.T) {
	s, path, state := watchFixture(t, request("baseline"))
	q := snapshot.TrajectorySelector{Collection: "events", Limit: 50}
	cursor := watchBaseline(t, s, q)
	t.Logf("baseline source frontiers: %+v", s.watch.sources)
	appendFile(t, path, strings.Repeat(request(strings.Repeat("private body ", 80)), 55))
	state.notify()
	first := watchNext(t, s, q, cursor)
	if len(first.Changes) != 50 || !first.More || first.ResetRequired {
		t.Fatalf("batch bound or continuation lost: %+v", first)
	}
	t.Logf("first event line=%d sequence=%d, last line=%d; frontiers=%+v", first.Changes[0].Source.Line, first.Changes[0].Sequence, first.Changes[len(first.Changes)-1].Source.Line, s.watch.sources)
	second := watchNext(t, s, q, first.Cursor)
	if len(second.Changes) != 5 || second.More || second.Changes[0].Sequence <= first.Changes[49].Sequence {
		t.Fatalf("bounded continuation reordered or omitted records: %+v", second)
	}
	otherConsumer := watchNext(t, s, q, cursor)
	if len(otherConsumer.Changes) != 50 || otherConsumer.Changes[0].EventID != first.Changes[0].EventID {
		t.Fatal("one consumer advanced another consumer's cursor")
	}
	for _, batch := range []snapshot.TrajectoryWatchResult{first, second} {
		body, _ := json.Marshal(batch)
		if len(body) > WatchResponseBytes || strings.Contains(string(body), "private body") {
			t.Fatal("NDJSON projection is unbounded or contains bodies")
		}
	}
}

func TestTrajectoryWatchPredicatesMatchSharedQuery(t *testing.T) {
	for name, selector := range map[string]snapshot.TrajectorySelector{
		"role":        {Collection: "events", Role: "user"},
		"kind":        {Collection: "events", Kind: "tool_result"},
		"actor":       {Collection: "events", ActorKind: "unknown"},
		"agent":       {Collection: "events", Agent: "claude"},
		"tool_entity": {Collection: "events", EntityKind: "tool", Predicate: "called"},
	} {
		t.Run(name, func(t *testing.T) {
			s, path, state := watchFixture(t, request("baseline"))
			before, err := s.Query(context.Background(), selector)
			if err != nil || before.WatchCursor == "" {
				t.Fatalf("predicate baseline: %+v %v", before, err)
			}
			seen := map[string]bool{}
			for _, event := range before.Events {
				seen[event.ID] = true
			}
			appendFile(t, path, request("new request")+call("native-call")+result("native-call"))
			state.notify()
			watched := watchNext(t, s, selector, before.WatchCursor)
			after, err := s.Query(context.Background(), selector)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{}
			for _, event := range after.Events {
				if !seen[event.ID] {
					want = append(want, event.ID)
				}
			}
			if watched.ResetRequired || len(watched.Changes) != len(want) {
				t.Fatalf("watch ignored shared %s predicate: %+v want %v", name, watched, want)
			}
			for i, change := range watched.Changes {
				if change.EventID != want[i] {
					t.Fatalf("watch changed predicate meaning or order: %+v want %v", watched, want)
				}
			}
		})
	}
}

func TestTrajectoryCursorScopeTamperingExpiryAndWindowLoss(t *testing.T) {
	s, path, state := watchFixture(t, request("baseline"))
	q := snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"}
	cursor := watchBaseline(t, s, q)
	changed := q
	changed.Tool = "apply_patch"
	for _, p := range []snapshot.TrajectoryWatchParams{
		{Selector: changed, Cursor: cursor, TimeoutMS: 1},
		{Selector: q, Cursor: "query-revision:20", TimeoutMS: 1},
		{Selector: snapshot.TrajectorySelector{Cursor: "query-revision:20"}, TimeoutMS: 1},
		{Selector: q, Cursor: cursor[:len(cursor)-1] + "x", TimeoutMS: 1},
		{Selector: q, Cursor: cursor, TimeoutMS: 5001},
	} {
		if _, err := s.Watch(context.Background(), p); !errors.Is(err, ErrInvalid) {
			t.Fatalf("unbound or invalid cursor accepted: %+v %v", p, err)
		}
	}
	s.watch.mu.Lock()
	now := time.Now().Add(WatchCursorTTL + time.Minute)
	s.watch.now = func() time.Time { return now }
	s.watch.mu.Unlock()
	expired := watchNext(t, s, q, cursor)
	if !expired.ResetRequired || len(expired.Changes) != 0 || !strings.Contains(strings.Join(expired.Coverage.Gaps, "|"), "watch_cursor_expired") {
		t.Fatalf("long absence became fake continuity: %+v", expired)
	}
	s.watch.mu.Lock()
	s.watch.now = time.Now
	s.watch.mu.Unlock()
	cursor = watchBaseline(t, s, q)
	appendFile(t, path, strings.Repeat(call("native"), WatchHistoryLimit+20))
	state.notify()
	lost := watchNext(t, s, q, cursor)
	if !lost.ResetRequired || lost.Coverage.Complete || len(lost.Changes) != 0 || !strings.Contains(strings.Join(lost.Coverage.Gaps, "|"), "watch_change_window_lost") {
		t.Fatalf("slow consumer got a fake empty interval: %+v", lost)
	}
	s.watch.mu.Lock()
	retained := len(s.watch.entries)
	s.watch.mu.Unlock()
	if retained > WatchHistoryLimit {
		t.Fatal("resident change history is unbounded")
	}
}

func TestTrajectoryWatchSourceReplacementRemovalAndBaseline(t *testing.T) {
	s, path, state := watchFixture(t, request("baseline"))
	q := snapshot.TrajectorySelector{Collection: "events"}
	cursor := watchBaseline(t, s, q)
	if err := os.WriteFile(path, []byte(request("replacement")), 0600); err != nil {
		t.Fatal(err)
	}
	state.notify()
	replaced := watchNext(t, s, q, cursor)
	if !replaced.ResetRequired || len(replaced.Changes) != 0 || !strings.Contains(strings.Join(replaced.Coverage.Gaps, "|"), "watch_source_generation_changed") {
		t.Fatalf("replacement reused old stream identity: %+v", replaced)
	}
	cursor = watchBaseline(t, s, q)
	state.visible.Store(false)
	state.notify()
	removed := watchNext(t, s, q, cursor)
	if !removed.ResetRequired || len(removed.Changes) != 0 || !strings.Contains(strings.Join(removed.Coverage.Gaps, "|"), "watch_source_scope_changed") {
		t.Fatalf("root removal leaked cached source metadata: %+v", removed)
	}
	baseline := watchNext(t, s, q, "")
	if !baseline.ResetRequired || baseline.Cursor == "" || !strings.Contains(strings.Join(baseline.Coverage.Gaps, "|"), "watch_baseline_required") {
		t.Fatalf("cursor-less watch silently skipped prior history: %+v", baseline)
	}
}

func TestTrajectoryWatchCancellationRevocationAndClose(t *testing.T) {
	for _, mode := range []string{"cancel", "revoke", "close"} {
		t.Run(mode, func(t *testing.T) {
			s, _, state := watchFixture(t, request("private baseline"))
			q := snapshot.TrajectorySelector{Collection: "events"}
			cursor := watchBaseline(t, s, q)
			for len(state.observed) > 0 {
				<-state.observed
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			type receipt struct {
				out snapshot.TrajectoryWatchResult
				err error
			}
			done := make(chan receipt, 1)
			go func() {
				out, err := s.Watch(ctx, snapshot.TrajectoryWatchParams{Selector: q, Cursor: cursor, TimeoutMS: 5000})
				done <- receipt{out, err}
			}()
			select {
			case <-state.observed:
			case <-time.After(time.Second):
				t.Fatal("watch did not start")
			}
			var want error
			switch mode {
			case "cancel":
				cancel()
				want = context.Canceled
			case "revoke":
				state.access.Store(false)
				if err := s.Reset(); err != nil {
					t.Fatal(err)
				}
				want = ErrAccess
			case "close":
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
				want = ErrClosed
			}
			select {
			case result := <-done:
				if !errors.Is(result.err, want) || len(result.out.Changes) != 0 || result.out.Cursor != "" || result.out.Revision != "" {
					t.Fatalf("idle watch retained content after %s: %+v", mode, result)
				}
			case <-time.After(500 * time.Millisecond):
				t.Fatalf("%s did not release long poll", mode)
			}
		})
	}
}

func TestTrajectoryCursorRevisionIncludesCheckpointProgress(t *testing.T) {
	before := &sourceState{ID: "source", Generation: "generation", checkpoint: sourceCheckpoint{Size: 1000, Mtime: 9, Offset: 100, EventCount: 1, Coverage: coverage("source")}}
	after := *before
	after.checkpoint.Offset = 200
	after.checkpoint.EventCount = 2
	if sourceRevision([]*sourceState{before}) == sourceRevision([]*sourceState{&after}) {
		t.Fatal("same source metadata hid additional indexed events")
	}
	after = *before
	after.checkpoint.Version++
	if sourceRevision([]*sourceState{before}) == sourceRevision([]*sourceState{&after}) {
		t.Fatal("projection migration reused the old query revision")
	}
}

func TestTrajectoryWatchUnavailableIndexReportsGap(t *testing.T) {
	s, path, _ := watchFixture(t, request("baseline"))
	// A regular transcript file cannot also be the index's parent directory.
	s.path = filepath.Join(path, "index.bbolt")
	result, err := s.Watch(context.Background(), snapshot.TrajectoryWatchParams{Selector: snapshot.TrajectorySelector{Collection: "events"}, TimeoutMS: 1})
	if err != nil || !result.ResetRequired || result.Coverage.Complete || len(result.Changes) != 0 || !strings.Contains(strings.Join(result.Coverage.Gaps, "|"), "index_unavailable") {
		t.Fatalf("unavailable index became a fake empty observation: %+v %v", result, err)
	}
}
