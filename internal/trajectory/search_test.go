package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func searchTestQuery(t *testing.T, s *Service, q snapshot.TrajectorySelector) snapshot.TrajectoryQueryResult {
	t.Helper()
	result, err := s.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("query %+v: %v", q, err)
	}
	return result
}

func TestTrajectorySearchDefaultPageAndExplicitCountAgree(t *testing.T) {
	root := t.TempDir()
	sources := []Source{}
	for i := 0; i < 45; i++ {
		path := filepath.Join(root, fmt.Sprintf("source-%02d.jsonl", i))
		text := "needle"
		if i%5 == 0 {
			text = "nee eed edl dle" // FTS membership, without a literal match.
		}
		if err := os.WriteFile(path, []byte(request(text)+request(text)), 0600); err != nil {
			t.Fatal(err)
		}
		at := time.Unix(10000-int64(i), 0)
		if err := os.Chtimes(path, at, at); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, Source{Agent: "codex", Path: path, Decoder: CodexDecoder{}})
	}
	s := New(func(context.Context) SourceSet {
		return SourceSet{Sources: sources, Coverage: coverage("fixture"), CatalogComplete: true}
	})
	defer s.Close()
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "needle"})
	for _, collection := range []string{"sessions", "events"} {
		plainCursor, countCursor := "", ""
		seen := map[string]bool{}
		for page := 0; page < 20; page++ {
			plain := searchTestQuery(t, s, snapshot.TrajectorySelector{Collection: collection, Text: "needle", Limit: 7, Cursor: plainCursor})
			counted := searchTestQuery(t, s, snapshot.TrajectorySelector{Collection: collection, Text: "needle", Limit: 7, Cursor: countCursor, Count: true})
			want := 36
			if collection == "events" {
				want *= 2
			}
			if plain.MatchedTotal != nil || counted.MatchedTotal == nil || *counted.MatchedTotal != want {
				t.Fatal("unknown was fabricated or exact total lost", plain.MatchedTotal, counted.MatchedTotal)
			}
			ids := []string{}
			if !reflect.DeepEqual(plain.Sessions, counted.Sessions) || !reflect.DeepEqual(plain.Events, counted.Events) {
				t.Fatal("retrieval/count pages differ", collection, page)
			}
			for _, session := range plain.Sessions {
				if session.MatchedCount != 2 || len(session.MatchedIDs) != 2 {
					t.Fatal("page lost exact references", session)
				}
				ids = append(ids, session.ID)
			}
			for _, event := range plain.Events {
				ids = append(ids, event.ID)
			}
			for _, id := range ids {
				if seen[id] {
					t.Fatal("duplicate continuation", id)
				}
				seen[id] = true
			}
			if (plain.Next == "") != (counted.Next == "") {
				t.Fatal("lookahead differs from exact total")
			}
			if plain.Next == "" {
				if len(seen) != want {
					t.Fatal("missing matches", len(seen), want)
				}
				break
			}
			if page == 0 {
				_, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: collection, Text: "needle", Cursor: plain.Next, Count: true})
				if !errors.Is(err, ErrStale) {
					t.Fatal("cursor mixed count modes", err)
				}
			}
			plainCursor, countCursor = plain.Next, counted.Next
		}
	}
	catalog := searchTestQuery(t, s, snapshot.TrajectorySelector{})
	if catalog.MatchedTotal != nil {
		t.Fatal("default catalog invented a total")
	}
	counted := searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true})
	if counted.MatchedTotal == nil || *counted.MatchedTotal != 45 {
		t.Fatal("catalog count lost")
	}
}

func TestTrajectorySearchPageClearsPriorLiteralProof(t *testing.T) {
	s, _ := fixture(t, request("oldneedle")+request("newneedle"))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "oldneedle"})
	src := s.provider(context.Background()).Sources[0]
	st, _ := s.cachedSource(src)
	oldStore := legacyOracleFor(t, st)
	refs, err := oldStore.sourceReferences(context.Background(), st, "1=1", "ASC", 50)
	if err != nil {
		t.Fatal(err)
	}
	legacyEvents := []snapshot.TrajectoryEvent{}
	for _, r := range refs {
		e, err := oldStore.event(context.Background(), st.ID, r.offset, r.block)
		if err != nil {
			t.Fatal(err)
		}
		legacyEvents = append(legacyEvents, e)
	}
	if err = oldStore.write(context.Background(), func(w *factWriter) error {
		for i, r := range refs {
			if err := w.project(r.row, legacyEvents[i]); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var old int64
	if err = oldStore.prepareTextMatches(context.Background(), snapshot.TrajectorySelector{Text: "oldneedle"}); err != nil {
		t.Fatal(err)
	}
	if err = oldStore.db.QueryRow("SELECT rowid FROM temp.trajectory_text_matches LIMIT 1").Scan(&old); err != nil {
		t.Fatal(err)
	}
	if _, err = oldStore.prepareTextPage(context.Background(), snapshot.TrajectorySelector{Collection: "sessions", Text: "newneedle", Limit: 1}, 0); err != nil {
		t.Fatal(err)
	}
	var stale bool
	if err = oldStore.db.QueryRow("SELECT EXISTS(SELECT 1 FROM temp.trajectory_text_matches WHERE rowid=?)", old).Scan(&stale); err != nil || stale {
		t.Fatal("legacy literal proof leaked", err)
	}

	plain := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "newneedle"})
	counted := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "newneedle", Count: true})
	if !reflect.DeepEqual(plain.Sessions, counted.Sessions) {
		t.Fatal("stale proof changed the public page")
	}
	watch, err := s.Watch(context.Background(), snapshot.TrajectoryWatchParams{Selector: snapshot.TrajectorySelector{Text: "newneedle"}, Cursor: counted.WatchCursor, TimeoutMS: 1})
	if err != nil || watch.ResetRequired {
		t.Fatal("count changed watch filtering", watch, err)
	}
}

func searchTestPreparedQuery(t *testing.T, s *Service, q snapshot.TrajectorySelector) snapshot.TrajectoryQueryResult {
	t.Helper()
	for attempt := 0; attempt < 32; attempt++ {
		// Tests of prepared evidence must let the background queue catch up;
		// a foreground query deliberately contributes only a short quantum.
		actionable := false
		for _, src := range s.provider(context.Background()).Sources {
			more, err := s.PrepareSource(context.Background(), src, func() bool { return true })
			if err != nil {
				t.Fatal("background preparation", err)
			}
			actionable = actionable || more
		}
		result := searchTestQuery(t, s, q)
		pending, searchPending, partialRecord := false, false, false
		for _, label := range result.Coverage.Gaps {
			pending = pending || label == "index_pending"
			searchPending = searchPending || label == "search_index_pending"
			partialRecord = partialRecord || label == "partial_record"
		}
		if (!pending && !actionable) || (!actionable && !searchPending && partialRecord) {
			return result
		}
	}
	t.Fatal("bounded incremental preparation did not finish fixture")
	return snapshot.TrajectoryQueryResult{}
}
func searchTestBaseline(t *testing.T, s *Service, q snapshot.TrajectorySelector) []string {
	t.Helper()
	s.opMu.Lock()
	defer s.opMu.Unlock()
	states, _ := s.collect(context.Background())
	ids := []string{}
	for _, st := range states {
		if q.Agent != "" && q.Agent != st.Agent || q.SessionID != "" && q.SessionID != sessionID(st) {
			continue
		}
		scan(context.Background(), st, func(e snapshot.TrajectoryEvent) bool {
			if matches(e, q) {
				ids = append(ids, e.ID)
			}
			return true
		})
	}
	return ids
}
func TestTrajectorySearchLiteralCleanEvidenceAndUnicode(t *testing.T) {
	tool := codexRecord("response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "literal-args", "arguments": map[string]string{"cmd": "echo <system-reminder>argument-secret</system-reminder> cat /skills/bagakit-researcher/SKILL.md"}})
	s, _ := fixture(t, request("<system-reminder>catalog-only</system-reminder>实际读取 中文词 bagakit-researcher OR a\"b a*b literal% under_score CAFÉ Σ ſ")+request("bagakit-other AND ordinary")+request("zero\x00after-nul")+tool)
	for _, text := range []string{"bagakit-researcher", "researcher", "kit-re", "中文词", "中文", "读", "OR", "a\"b", "a*b", "literal%", "under_score", "CAFÉ", "σ", "ſ", "argument-secret", "catalog-only", "absent OR bagakit-researcher", "bagakit-researcher *", "\"", "after-nul", "zero\x00after"} {
		t.Run(text, func(t *testing.T) {
			selector := snapshot.TrajectorySelector{Collection: "events", Text: text, Limit: 50}
			expected := searchTestBaseline(t, s, selector)
			got := searchTestQuery(t, s, selector)
			ids := []string{}
			for _, e := range got.Events {
				ids = append(ids, e.ID)
			}
			if !reflect.DeepEqual(ids, expected) {
				t.Fatalf("literal mismatch %q: got %v expected %v", text, ids, expected)
			}
			if text == "catalog-only" && len(ids) != 0 {
				t.Fatal("stripped wrapper leaked into search")
			}
			if text == "argument-secret" && len(ids) != 1 {
				t.Fatal("tool argument evidence was cleaned")
			}
		})
	}
}

func TestTrajectorySearchSelectorsMatchCanonicalPredicates(t *testing.T) {
	first := relationEvent("first", "needle $alpha")
	first.Actor = snapshot.TrajectoryActor{Kind: "agent", ID: "worker-a"}
	first.Tool = &snapshot.TrajectoryTool{Name: "READ_Σ_ſ", Arguments: json.RawMessage(`{"path":"/skills/alpha/SKILL.md"}`)}
	first.Kind = "tool_call"
	second := relationEvent("second", "needle $alpha")
	second.Role = "assistant"
	third := relationEvent("third", "needle unrelated")
	s, _ := relationService(t, relationRecord(first)+relationRecord(second)+relationRecord(third))
	selectors := []snapshot.TrajectorySelector{
		{Text: "needle", Role: "user"}, {Text: "needle", ActorID: "worker-a"}, {Text: "needle", ActorKind: "agent"},
		{Text: "needle", Tool: "read_ς_s"}, {Text: "needle", Kind: "tool_call"}, {Text: "needle", Skill: "ALPHA"},
		{Text: "needle", Skill: "alpha", Predicate: "mention"}, {Text: "needle", Skill: "alpha", Predicate: "loaded"},
		{Text: "needle", EntityKind: "skill", Predicate: "mention"}, {Text: "needle", EntityKind: "tool", Predicate: "called"},
	}
	all := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Text: "needle"})
	if len(all.Events) != 3 {
		t.Fatal("prepared canonical selector fixture is incomplete", all)
	}
	detail, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: all.Events[0].ID, Around: 0})
	if err != nil {
		t.Fatal(err)
	}
	// Query previews omit entity bodies; inspect the canonical stored occurrences.
	s.opMu.Lock()
	states, _ := s.collect(context.Background())
	scan(context.Background(), states[0], func(e snapshot.TrajectoryEvent) bool {
		for _, o := range e.Entities {
			if o.Kind == "skill" {
				selectors = append(selectors, snapshot.TrajectorySelector{Text: "needle", EntityID: o.EntityID, Predicate: o.Predicate})
				return false
			}
		}
		return true
	})
	s.opMu.Unlock()
	if len(detail.Events) == 0 {
		t.Fatal("missing canonical detail")
	}
	for _, q := range selectors {
		q.Collection = "events"
		q.Limit = 50
		expected := searchTestBaseline(t, s, q)
		got := searchTestQuery(t, s, q)
		ids := []string{}
		for _, e := range got.Events {
			ids = append(ids, e.ID)
		}
		if !reflect.DeepEqual(ids, expected) {
			t.Fatalf("selector %+v mismatch got %v expected %v", q, ids, expected)
		}
	}
	if _, err := s.Query(context.Background(), snapshot.TrajectorySelector{Text: "needle", State: "verified"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsupported selector accepted")
	}
	s.opMu.Lock()
	_, err = s.querySearch(context.Background(), snapshot.TrajectorySelector{Text: "needle", ContextID: "ctx-native"}, states, coverage("fixture"))
	s.opMu.Unlock()
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("native context silently ignored")
	}
}

func TestTrajectorySearchIncrementalGenerationWithdrawalAndRestart(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "session.jsonl")
	index := filepath.Join(root, "private", "trajectory.sqlite")
	if err := os.WriteFile(path, []byte(request("old needle")), 0600); err != nil {
		t.Fatal(err)
	}
	authorized := true
	provider := func(context.Context) SourceSet {
		sources := []Source{}
		if authorized {
			sources = append(sources, Source{Agent: "codex", Path: path, NativeID: "native", Decoder: CodexDecoder{}})
		}
		return SourceSet{Sources: sources, Coverage: coverage("fixture")}
	}
	s := NewPersistent(provider, index)
	t.Cleanup(func() { _ = s.Close() })
	first := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if len(first.Sessions) != 1 || first.Sessions[0].MatchedCount != 1 {
		t.Fatal(first)
	}
	oldID := first.Sessions[0].MatchedIDs[0]
	oldRevision := s.search.revision
	_ = searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if s.search.revision != oldRevision {
		t.Fatal("unchanged sources rewrote derived index")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(request("new needle"))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	appended := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if appended.Sessions[0].MatchedCount != 2 || appended.Sessions[0].MatchedIDs[0] != oldID {
		t.Fatal("append duplicated events or changed generation", appended)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = NewPersistent(provider, index)
	t.Cleanup(func() { _ = s.Close() })
	restarted := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if restarted.Sessions[0].MatchedCount != 2 || restarted.Sessions[0].MatchedIDs[0] != oldID {
		t.Fatal("restart duplicated or lost index", restarted)
	}
	replacement := filepath.Join(root, "replacement")
	if err = os.WriteFile(replacement, []byte(request("replacement other")), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	stale := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if len(stale.Sessions) != 0 {
		t.Fatal("old generation remained searchable", stale)
	}
	var count int
	if err = s.search.db.QueryRow("SELECT COUNT(*) FROM sources WHERE id=? AND generation=? AND active=1 AND missing=0", strings.Split(oldID, ".")[1], strings.Split(oldID, ".")[2]).Scan(&count); err != nil || count != 0 {
		t.Fatal("old generation remained queryable", count, err)
	}
	latest := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "replacement"})
	if len(latest.Sessions) != 1 || latest.Sessions[0].ID == first.Sessions[0].ID {
		t.Fatal("replacement reused session generation", latest)
	}
	authorized = false
	revoked := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "replacement"})
	if len(revoked.Sessions) != 0 {
		t.Fatal("withdrawn source remained searchable")
	}
	if err = s.search.db.QueryRow("SELECT COUNT(*) FROM sources WHERE active=1 AND missing=0").Scan(&count); err != nil || count != 0 {
		t.Fatal("withdrawn content remained queryable", count, err)
	}
}

func TestTrajectorySearchLargeWithdrawalPreservesEvidenceAfterRestart(t *testing.T) {
	s, _ := fixture(t, strings.Repeat(request("private needle"), 193))
	provider := s.provider
	initial := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	s.temporary = false // exercise an actual persisted store across restart
	defer os.RemoveAll(filepath.Dir(s.path))
	if len(initial.Sessions) != 1 || initial.Sessions[0].MatchedCount != 193 {
		t.Fatal("fixture not prepared", initial)
	}
	oldID := initial.Sessions[0].MatchedIDs[0]
	empty := SourceSet{Coverage: coverage("withdrawn fixture"), CatalogComplete: true}
	s.provider = func(context.Context) SourceSet { return empty }
	withdrawn := searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "needle"})
	if len(withdrawn.Sessions) != 0 || withdrawn.MatchedTotal == nil || *withdrawn.MatchedTotal != 0 {
		t.Fatal("withdrawn text influenced results or counts", withdrawn)
	}
	id := strings.Split(oldID, ".")[1]
	cp, ok, err := s.store.checkpoint(context.Background(), id)
	if err != nil || !ok || cp.EventCount != 193 || !cp.Missing {
		t.Fatal("withdrawal lost useful recovery state", err)
	}
	if _, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: oldID}); !errors.Is(err, ErrNotFound) {
		t.Fatal("withdrawn canonical evidence still accessible", err)
	}
	more, err := s.PrepareCatalog(context.Background(), empty, func() bool { return true })
	if err != nil {
		t.Fatal("withdrawn catalog maintenance failed", more, err)
	}
	var retainedRanges int
	if err = s.store.db.QueryRow("SELECT count(*) FROM ranges").Scan(&retainedRanges); err != nil || retainedRanges == 0 {
		t.Fatal("withdrawal erased sparse recovery evidence", err)
	}
	path := s.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s = NewPersistent(func(context.Context) SourceSet { return empty }, path)
	defer s.Close()
	for n := 0; n < 4; n++ {
		more, err = s.PrepareCatalog(context.Background(), empty, func() bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	cp, ok, err = s.store.checkpoint(context.Background(), id)
	if err != nil || !ok || cp.EventCount != 193 || !cp.Missing {
		t.Fatal("restart lost withdrawn recovery evidence", err)
	}
	var rangesAfter int
	if err = s.store.db.QueryRow("SELECT count(*) FROM ranges").Scan(&rangesAfter); err != nil || rangesAfter != retainedRanges {
		t.Fatal("background maintenance deleted useful sparse evidence", err)
	}
	// Reauthorization establishes a current generation; quarantine is preserved.
	s.provider = provider
	restored := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if len(restored.Sessions) != 1 || restored.Sessions[0].MatchedCount != 193 || restored.Sessions[0].MatchedIDs[0] == oldID {
		t.Fatal("reauthorization reused quarantine or lost records", restored)
	}
}

func TestTrajectorySearchSessionPageReferencesStayBoundedAndExact(t *testing.T) {
	root := t.TempDir()
	var sources []Source
	for i, count := range []int{73, 7, 0} {
		path := filepath.Join(root, fmt.Sprintf("session-%d.jsonl", i))
		// More than one batch of trigram candidates lacks the complete literal.
		body := strings.Repeat(request("nee eed edl dle"), 320) + strings.Repeat(request("needle"), count) + request("unrelated")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, Source{Agent: "codex", Path: path, Decoder: CodexDecoder{}})
	}
	s := NewPersistent(func(context.Context) SourceSet { return SourceSet{Sources: sources, Coverage: coverage("fixture")} }, filepath.Join(root, "private", "trajectory.sqlite"))
	defer s.Close()
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "needle"})
	if len(q.Sessions) != 2 || q.MatchedTotal == nil || *q.MatchedTotal != 2 {
		t.Fatal("incorrect session total", q)
	}
	for _, session := range q.Sessions {
		if session.MatchedCount != 73 && session.MatchedCount != 7 || len(session.MatchedIDs) != min(50, session.MatchedCount) {
			t.Fatal("reference page lost exact count or per-session bound", session)
		}
		for _, id := range session.MatchedIDs {
			detail, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id, Around: 0})
			if err != nil || len(detail.Events) != 1 || detail.Events[0].SessionID != session.ID || detail.Events[0].Text != "needle" {
				t.Fatal("page reference crossed session or matched unrelated evidence", id, detail, err)
			}
		}
	}
}

func TestTrajectorySearchExactCountsBoundedReferencesAndPagination(t *testing.T) {
	s, _ := fixture(t, request("Unrelated title")+strings.Repeat(request(strings.Repeat("prefix ", 100)+"hit-needle"), 73))
	sessions := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "hit-needle"})
	if len(sessions.Sessions) != 1 {
		t.Fatal(sessions)
	}
	match := sessions.Sessions[0]
	if match.MatchedCount != 73 || len(match.MatchedIDs) != 50 || !strings.Contains(match.MatchedPreview, "hit-needle") || match.Title != "Unrelated title" {
		t.Fatal("count/snippet/reference mismatch", match)
	}
	first := searchTestQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Text: "hit-needle", Limit: 50})
	if len(first.Events) != 50 || first.Next == "" {
		t.Fatal("missing bounded continuation", first)
	}
	second := searchTestQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Text: "hit-needle", Limit: 50, Cursor: first.Next})
	if len(second.Events) != 23 || second.Next != "" {
		t.Fatal("missing final page", second)
	}
	seen := map[string]bool{}
	for _, e := range append(first.Events, second.Events...) {
		if seen[e.ID] || !strings.Contains(e.Text, "hit-needle") {
			t.Fatal("duplicate/irrelevant preview", e)
		}
		seen[e.ID] = true
	}
	fpath := s.search.path
	for _, path := range []string{s.path, fpath, filepath.Dir(fpath)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		expected := os.FileMode(0600)
		if info.IsDir() {
			expected = 0700
		}
		if info.Mode().Perm() != expected {
			t.Fatalf("unsafe mode %s: %o", path, info.Mode().Perm())
		}
	}
	// Closing access preserves useful private recovery and exception evidence.
	before, err := os.ReadFile(fpath)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(fpath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reset deleted or changed useful private evidence", err)
	}
}

func TestTrajectorySearchPendingCommittedPrefixAndAtomicResume(t *testing.T) {
	// Build canonical truth independently, then simulate a committed first
	// backfill batch. This avoids a timing-sensitive test of the one-second gate.
	s, _ := fixture(t, strings.Repeat(request("pending needle"), searchBatchEvents+19))
	s.opMu.Lock()
	defer s.opMu.Unlock()
	if err := s.openIndex(); err != nil {
		t.Fatal(err)
	}
	src := s.provider(context.Background()).Sources[0]
	st, err := s.indexSource(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.openSearch(); err != nil {
		t.Fatal(err)
	}

	if _, err = s.store.db.Exec("UPDATE sources SET search_count=0,search_offset=-1,search_block=-1 WHERE active=1"); err != nil {
		t.Fatal(err)
	}
	p := sourceReadiness{generation: st.Generation, complete: true, offset: -1, block: -1}
	facts, err := s.store.take(context.Background(), st, -1, -1, false, false, searchBatchEvents)
	batch := []snapshot.TrajectoryEvent{}
	bytes := 0
	for _, fact := range facts {
		if bytes > 0 && bytes+fact.size > searchBatchBytes {
			break
		}
		bytes += fact.size
		batch = append(batch, fact.event)
	}
	if err != nil || len(batch) == 0 || len(batch) > searchBatchEvents || len(batch) >= st.checkpoint.EventCount {
		t.Fatal("unbounded batch", len(batch), err)
	}
	preparedCount := len(batch)
	if bytes > searchBatchBytes {
		t.Fatal("batch exceeded the text-index byte quantum", bytes)
	}
	if p, err = s.store.advanceReadiness(context.Background(), st, p); err != nil {
		t.Fatal(err)
	}
	if p.count != preparedCount || p.offset != batch[len(batch)-1].Source.Offset || p.block != batch[len(batch)-1].Source.Block {
		t.Fatal("durable readiness differs from bounded committed batch", p)
	}

	cov := coverage("fixture")
	gap(&cov, "search_index_pending")
	gap(&cov, "index_pending")
	cov.Omitted = st.checkpoint.EventCount - preparedCount
	partial, err := s.querySearch(context.Background(), snapshot.TrajectorySelector{Text: "needle"}, []*sourceState{st}, cov)
	if err != nil || len(partial.Sessions) != 1 || partial.Sessions[0].MatchedCount != preparedCount || partial.Coverage.Complete {
		t.Fatal("pending falsely reported complete", partial, err)
	}
	partialEvents, err := s.querySearch(context.Background(), snapshot.TrajectorySelector{Collection: "events", Text: "needle", Limit: 50}, []*sourceState{st}, cov)
	if err != nil || partialEvents.Next == "" {
		t.Fatal("pending hits have no bounded continuation", partialEvents, err)
	}
	for n := 0; n < 20; n++ {
		cov = coverage("fixture")
		if err = s.syncSearch(context.Background(), []*sourceState{st}, &cov); err != nil {
			t.Fatal(err)
		}
		if cov.Complete {
			break
		}
	}
	full, err := s.querySearch(context.Background(), snapshot.TrajectorySelector{Text: "needle"}, []*sourceState{st}, cov)
	if err != nil || full.Sessions[0].MatchedCount != searchBatchEvents+19 || !full.Coverage.Complete {
		t.Fatal("backfill missed or duplicated events", full, err)
	}
	if _, err = s.querySearch(context.Background(), snapshot.TrajectorySelector{Collection: "events", Text: "needle", Cursor: partialEvents.Next}, []*sourceState{st}, cov); !errors.Is(err, ErrStale) {
		t.Fatal("pending cursor survived revision change", err)
	}
	// A cancelled batch transaction cannot advance the durable source cursor.
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	before := p
	if p, err = s.store.advanceReadiness(canceled, st, p); err == nil || p != before {
		t.Fatal("cancelled write advanced progress", err, p, before)
	}
}

func TestTrajectorySearchDoesNotReadCanonicalNonmatches(t *testing.T) {
	s, _ := fixture(t, request("needle actual")+strings.Repeat(request("unrelated "+strings.Repeat("background ", 20)), 800))
	_ = searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	// Seal the whole current source before using negative candidates. Corrupt
	// an exception in a different physical range; the exact hit still succeeds.
	s.opMu.Lock()
	states, _ := s.collect(context.Background())
	st := states[0]
	for {
		more, err := s.store.auditSource(context.Background(), st)
		if err != nil {
			s.opMu.Unlock()
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	facts, err := s.store.take(context.Background(), st, st.checkpoint.Offset, 0, true, false, 1)
	if err != nil || len(facts) != 1 {
		s.opMu.Unlock()
		t.Fatal("cannot locate negative candidate range", err)
	}
	_, err = s.store.db.Exec("INSERT INTO exceptions(source,offset,block,body) SELECT rowid,?,?,? FROM sources WHERE id=? AND generation=?", facts[0].offset, facts[0].block, []byte("malformed unrelated event"), st.ID, st.Generation)
	if err != nil {
		s.opMu.Unlock()
		t.Fatal(err)
	}
	got, err := s.querySearch(context.Background(), snapshot.TrajectorySelector{Text: "needle"}, states, coverage("fixture"))
	s.opMu.Unlock()
	if err != nil || len(got.Sessions) != 1 {
		t.Fatal("searched canonical nonmatches", got, err)
	}
}

func TestTrajectorySearchSessionLimitAndStableOrdering(t *testing.T) {
	bodies := make([]string, 23)
	for i := range bodies {
		bodies[i] = relationRecord(relationEvent(fmt.Sprint(i), "needle"))
	}
	s, _ := relationService(t, bodies...)
	first := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle", Limit: 50})
	if len(first.Sessions) != 20 || first.Next == "" {
		t.Fatal("session limit not bounded", len(first.Sessions), first.Next)
	}
	second := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "needle", Limit: 50, Cursor: first.Next})
	if len(second.Sessions) != 3 || second.Next != "" {
		t.Fatal("missing session continuation", len(second.Sessions), second.Next)
	}
	// Revision includes source and FTS progress; an explicit source mutation is
	// tested separately above. Repeated queries retain both ordering and cursor.
	repeat := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "needle", Limit: 50})
	if repeat.Next != first.Next || repeat.Revision != first.Revision {
		t.Fatal("unchanged cursor drifted")
	}
}

func TestTrajectorySearchCanonicalCorruptionIsPreserved(t *testing.T) {
	s, _ := fixture(t, request("needle"))
	_ = searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	s.temporary = false
	path, provider := s.path, s.provider
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("not a canonical SQLite database")
	if err := os.WriteFile(path, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	reopened := NewPersistent(provider, path)
	defer reopened.Close()
	if err := reopened.openIndex(); err == nil {
		t.Fatal("corrupt canonical store accepted")
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(corrupt) {
		t.Fatal("canonical evidence was destroyed", err)
	}
}

func TestTrajectorySearchRejectsUnsynchronizedAuthorizationSnapshot(t *testing.T) {
	s, _ := fixture(t, request("private needle"))
	first := searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if len(first.Sessions) != 1 {
		t.Fatal(first)
	}
	s.opMu.Lock()
	defer s.opMu.Unlock()
	// A failed source reconciliation cannot let the prior sidecar authorization
	// set participate in the next query, including its count and continuation.
	got, err := s.querySearch(context.Background(), snapshot.TrajectorySelector{Text: "needle", Count: true}, nil, coverage("withdrawn"))
	if err != nil || len(got.Sessions) != 0 || len(got.Events) != 0 || got.Next != "" || got.MatchedTotal == nil || *got.MatchedTotal != 0 {
		t.Fatal("old authorization leaked through sidecar", got, err)
	}
}

func TestTrajectorySearchLargeRecordedMatchMakesBoundedProgress(t *testing.T) {
	s, _ := fixture(t, request(strings.Repeat("padding recorded text ", 25000)+"large-match-needle"))
	got := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "large-match-needle"})
	if len(got.Sessions) != 1 || got.Sessions[0].MatchedCount != 1 || !strings.Contains(got.Sessions[0].MatchedPreview, "large-match-needle") {
		t.Fatal("large recorded event stalled or hid its match", got)
	}
}

func TestTrajectorySearchCommonWordAcrossLongBodiesAndRestart(t *testing.T) {
	root := t.TempDir()
	var sources []Source
	const sessions, matchesPerSession = 32, 81
	for i := 0; i < sessions; i++ {
		path := filepath.Join(root, fmt.Sprintf("archive-%d.jsonl", i))
		// Common words occur late in long bodies. Counting a page must not read
		// every body again, and positional phrases must not match split tokens.
		body := strings.Repeat(request(strings.Repeat("padding ", 1024)+"research ababa a\"b café 中文词"), matchesPerSession-1)
		body += request("zero\x00research ababa a\"b café 中文词")
		body += request("res earch aba baba a\" b cafe 中文 词")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, Source{Agent: "codex", Path: path, Decoder: CodexDecoder{}})
	}
	index := filepath.Join(root, "private", "trajectory.sqlite")
	provider := func(context.Context) SourceSet { return SourceSet{Sources: sources, Coverage: coverage("fixture")} }
	s := NewPersistent(provider, index)
	defer func() { _ = s.Close() }()
	prepared := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "research"})
	firstID := prepared.Sessions[0].MatchedIDs[0]
	// Reopen a fully prepared source store without rebuilding its evidence or
	// readiness. Prior-format migration is covered by dedicated migration fixtures.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = NewPersistent(provider, index)
	for _, text := range []string{"research", "research ababa", "research a\"b", "research café", "research 中文词", "research aba", "zero\x00research"} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		q, err := s.Query(ctx, snapshot.TrajectorySelector{Count: true, Text: text, Limit: 16})
		cancel()
		if err != nil || q.MatchedTotal == nil || *q.MatchedTotal != sessions || len(q.Sessions) != 16 || q.Next == "" {
			t.Fatalf("common-word query %q: rows=%d total=%v err=%v", text, len(q.Sessions), q.MatchedTotal, err)
		}
		expected := matchesPerSession
		if strings.ContainsRune(text, 0) {
			expected = 1
		}
		for _, session := range q.Sessions {
			if session.MatchedCount != expected || len(session.MatchedIDs) != min(50, expected) {
				t.Fatalf("%q lost exact counts/references: %d / %d", text, session.MatchedCount, len(session.MatchedIDs))
			}
		}
		second := searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: text, Limit: 16, Cursor: q.Next})
		if len(second.Sessions) != 16 || second.Next != "" {
			t.Fatal("missing final session page")
		}
	}
	upgraded := searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "research"})
	if upgraded.Sessions[0].MatchedIDs[0] != firstID || upgraded.Coverage.Index.SearchableEvents != prepared.Coverage.Index.SearchableEvents {
		t.Fatal("structural upgrade lost evidence identity or backfill progress")
	}
}
