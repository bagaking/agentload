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

func codexRecord(kind string, payload any) string {
	b, _ := json.Marshal(map[string]any{"timestamp": "2026-10-01T00:00:00Z", "type": kind, "payload": payload})
	return string(b) + "\n"
}
func fixture(t *testing.T, body string) (*Service, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	s := New(func(context.Context) SourceSet {
		return SourceSet{Sources: []Source{{Agent: "codex", Path: path, NativeID: "native-session", Decoder: CodexDecoder{}}}, Coverage: coverage("fixture")}
	})
	t.Cleanup(func() { _ = s.Close() })
	return s, path
}

func TestTrajectoryReadCancellationWhileCountOwnsStore(t *testing.T) {
	s, _ := fixture(t, request("needle"))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	calls := []struct {
		name string
		run  func(context.Context) error
	}{
		{"query", func(ctx context.Context) error { _, err := s.Query(ctx, snapshot.TrajectorySelector{}); return err }},
		{"get", func(ctx context.Context) error { _, err := s.Get(ctx, snapshot.TrajectoryGetParams{}); return err }},
		{"entities", func(ctx context.Context) error {
			_, err := s.QueryEntities(ctx, snapshot.TrajectorySelector{})
			return err
		}},
		{"knowledge", func(ctx context.Context) error {
			_, err := s.QueryKnowledge(ctx, snapshot.TrajectorySelector{})
			return err
		}},
		{"actors", func(ctx context.Context) error {
			_, err := s.QueryActors(ctx, snapshot.TrajectorySelector{})
			return err
		}},
		{"relations", func(ctx context.Context) error {
			_, err := s.QueryRelations(ctx, snapshot.TrajectorySelector{})
			return err
		}},
		{"contexts", func(ctx context.Context) error {
			_, err := s.QueryContexts(ctx, snapshot.TrajectorySelector{})
			return err
		}},
		{"attention", func(ctx context.Context) error {
			_, err := s.QueryAttention(ctx, snapshot.TrajectorySelector{})
			return err
		}},
		{"prepareCatalog", func(ctx context.Context) error {
			_, err := s.PrepareCatalog(ctx, SourceSet{}, func() bool { return true })
			return err
		}},
		{"prepareSource", func(ctx context.Context) error {
			_, err := s.PrepareSource(ctx, Source{}, func() bool { return true })
			return err
		}},
		{"watch", func(ctx context.Context) error { _, err := s.Watch(ctx, snapshot.TrajectoryWatchParams{}); return err }},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			s.opMu.Lock()
			defer s.opMu.Unlock()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- call.run(ctx) }()
			select {
			case err := <-done:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("read lost its deadline", err)
				}
			case <-time.After(time.Second):
				t.Fatal("read waited for the long owner instead of its context")
			}
			if s.opMu.TryLock() {
				s.opMu.Unlock()
				t.Fatal("cancelled waiter released the owner's lock")
			}
		})
	}
	if _, err := s.Query(context.Background(), snapshot.TrajectorySelector{Text: "needle"}); err != nil {
		t.Fatal("cancelled waiter left an owner", err)
	}
}

type catalogCancellationContext struct {
	context.Context
	cancel context.CancelFunc
	calls  atomic.Int64
}

func (c *catalogCancellationContext) Err() error {
	if c.calls.Add(1) >= 2 {
		c.cancel()
	}
	return c.Context.Err()
}

func TestTrajectoryCancelledCatalogNeverPublishesExactCount(t *testing.T) {
	s, path := fixture(t, request("needle"))
	second := filepath.Join(filepath.Dir(path), "second.jsonl")
	if err := os.WriteFile(second, []byte(request("needle")), 0600); err != nil {
		t.Fatal(err)
	}
	provider := s.provider
	s.provider = func(ctx context.Context) SourceSet {
		set := provider(ctx)
		set.Sources = append(set.Sources, Source{Agent: "codex", Path: second, Decoder: CodexDecoder{}})
		return set
	}
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	s.opMu.Lock()
	defer s.opMu.Unlock()
	states, cov := s.collect(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result, err := s.querySessionCatalog(&catalogCancellationContext{Context: ctx, cancel: cancel}, snapshot.TrajectorySelector{Count: true, Limit: 1}, states, cov)
	if !errors.Is(err, context.Canceled) || result.MatchedTotal != nil {
		t.Fatal("partial catalog became an exact total", result.MatchedTotal, err)
	}
	// Non-cancellation failures cannot subtract a known prepared source either.
	if _, err := s.store.db.Exec("DROP TABLE ranges"); err != nil {
		t.Fatal(err)
	}
	result, err = s.querySessionCatalog(context.Background(), snapshot.TrajectorySelector{Count: true, Limit: 1}, states, cov)
	if err == nil || result.MatchedTotal != nil {
		t.Fatal("reference failure became an exact total", result.MatchedTotal, err)
	}
}
func request(text string) string {
	return codexRecord("response_item", map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}})
}

func TestTrajectoryCatalogDefersEvidenceUntilOpened(t *testing.T) {
	s, _, _, decoder, _ := indexFixture(t, request("browser")+call("browser-call")+result("browser-call"))
	prepared := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "browser"})
	before := decoder.calls.Load()
	page, err := s.Query(context.Background(), snapshot.TrajectorySelector{Limit: 8})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatal(page, err)
	}
	item := page.Sessions[0]
	if decoder.calls.Load() != before || item.ID != prepared.Sessions[0].ID || item.Title != "browser" || item.MatchedCount != nil || len(item.MatchedIDs) != 0 || page.MatchedTotal != nil {
		t.Fatal("catalog read or fabricated matched evidence", item, decoder.calls.Load()-before)
	}
	opened, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: item.ID, Around: 3})
	if err != nil || len(opened.Events) != 3 {
		t.Fatal("on-demand session evidence missing", opened, err)
	}
}
func call(id string) string {
	return codexRecord("response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": id, "arguments": "{\"cmd\":\"curl --head https://example.test\"}"})
}
func result(id string) string {
	return codexRecord("response_item", map[string]any{"type": "function_call_output", "call_id": id, "output": "Exit code: 0\nHTTP 200"})
}

func TestTrajectoryReadPathEvidence(t *testing.T) {
	rawRequest := "<system-reminder>injected boilerplate</system-reminder>Check Proxy"
	s, _ := fixture(t, request(rawRequest)+call("c1")+result("c1"))
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "Proxy"})
	if len(q.Sessions) != 1 {
		t.Fatalf("query: %+v", q)
	}
	if q.Sessions[0].Title != "Check Proxy" {
		t.Fatal(q.Sessions[0].Title)
	}
	tools, err := s.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"})
	if err != nil || len(tools.Events) != 1 {
		t.Fatalf("tools: %+v %v", tools, err)
	}
	read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: tools.Events[0].ID, Around: 3, Raw: true})
	if err != nil || len(read.Events) != 3 {
		t.Fatalf("get: %+v %v", read, err)
	}
	if read.Events[1].PairID != read.Events[2].ID || read.Events[2].PairID != read.Events[1].ID {
		t.Fatal("lost native pairing")
	}
	if read.Events[0].Actor.Kind != "unknown" || read.Events[0].Role != "user" {
		t.Fatal("invented human actor")
	}
	if !strings.Contains(string(read.Events[0].Raw), "injected boilerplate") {
		t.Fatal("raw evidence was cleaned")
	}
	for _, e := range read.Events {
		if e.Source.Line < 1 || e.Source.Digest == "" {
			t.Fatal("missing source provenance")
		}
		if digest([]byte(e.Raw)) != e.Source.Digest || len(e.Raw) != e.Source.Length {
			t.Fatal("raw wire value differs from physical source bytes")
		}
	}
}

func TestTrajectorySearchShowsMatchedSkillInCommandContext(t *testing.T) {
	command := strings.Repeat("padding ", 80) + "cat /skills/bagakit-researcher/SKILL.md"
	body := request("An unrelated session title") + codexRecord("response_item", map[string]any{
		"type": "function_call", "name": "exec_command", "call_id": "skill-read", "arguments": map[string]string{"cmd": command},
	}) + request("A later unrelated action")
	s, _ := fixture(t, body)
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "bagakit-researcher"})
	if len(q.Sessions) != 1 {
		t.Fatalf("search: %+v", q)
	}
	match := q.Sessions[0]
	if match.MatchedCount != nil || len(match.MatchedIDs) != 1 || !strings.Contains(match.MatchedPreview, "bagakit-researcher/SKILL.md") || len(match.MatchedPreview) > 324 {
		t.Fatalf("search hid the actual matched command: %+v", match)
	}
	read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: match.MatchedIDs[0], Around: 0})
	if err != nil || len(read.Events) != 1 || read.Events[0].Tool == nil || read.Events[0].Tool.CallID != "skill-read" {
		t.Fatalf("preview did not lead to its matched event: %+v %v", read, err)
	}
}

func TestTrajectoryReadPathSessionContinuation(t *testing.T) {
	s, _ := fixture(t, strings.Repeat(request("continued"), 8))
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{})
	read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: q.Sessions[0].ID, Around: 0})
	if err != nil || len(read.Events) != 1 || read.Before == "" || !read.Truncated || read.Coverage.Omitted != 7 {
		t.Fatalf("lost continuation: %+v %v", read, err)
	}
	earlier, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: read.Before, Around: 0})
	if err != nil || earlier.Events[0].Source.Line != 7 {
		t.Fatalf("earlier: %+v %v", earlier, err)
	}
}
func TestTrajectoryReadPathGapsAndUnknown(t *testing.T) {
	s, path := fixture(t, request("test")+result("missing")+"{\"type\":")
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{})
	if q.Coverage.Complete {
		t.Fatal("partial write reported complete")
	}
	read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: q.Sessions[0].ID, Around: 3})
	if err != nil {
		t.Fatal(err)
	}
	if read.Events[1].PairID != "" || !strings.Contains(strings.Join(read.Events[1].Omissions, "|"), "pair_not_observed") {
		t.Fatal("missing call was fabricated")
	}
	old := read.Events[0].ID
	if err := os.WriteFile(path, []byte(request("replacement")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: old}); !errors.Is(err, ErrStale) {
		t.Fatalf("old locator reused: %v", err)
	}
	if _, err := s.Query(context.Background(), snapshot.TrajectorySelector{State: "unknown"}); !errors.Is(err, ErrInvalid) {
		t.Fatal("unsupported selector silently ignored")
	}
}
func TestTrajectoryReadPathBudgetIncludesFocus(t *testing.T) {
	s, _ := fixture(t, request(strings.Repeat("long context ", 1000))+request(strings.Repeat("long request ", 1000))+call("focus")+result("focus"))
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"})
	id := q.Events[0].ID
	read, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: id, Around: 5, Raw: true, MaxBytes: 2048})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(read)
	if len(b) > 2048 {
		t.Fatalf("slice: %d", len(b))
	}
	found := false
	for _, e := range read.Events {
		found = found || e.ID == id
	}
	if !found {
		t.Fatal("budget dropped selected evidence")
	}
}
func TestTrajectoryReadPathEnvelopeBoundaries(t *testing.T) {
	if CleanText("<system-reminder>x</environment_context>intent") != "<system-reminder>x</environment_context>intent" {
		t.Fatal("mismatched wrappers erased text")
	}
	if CleanText("<environment_context>x</environment_context>  intent") != "intent" {
		t.Fatal("wrapper not removed")
	}
}
