package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

type pacedArchiveDecoder struct {
	calls int
}

func (d *pacedArchiveDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	d.calls++
	time.Sleep(3 * time.Millisecond)
	return (CodexDecoder{}).Decode(raw, ctx)
}

func TestTrajectoryRecoveryQueryYieldsBeforeDrainingArchive(t *testing.T) {
	s, _ := fixture(t, strings.Repeat(request("recorded bagakit-researcher"), 2000))
	decoder := &pacedArchiveDecoder{}
	provider := s.provider
	s.provider = func(ctx context.Context) SourceSet {
		set := provider(ctx)
		set.Sources[0].Decoder = decoder
		return set
	}
	started := time.Now()
	q := searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "bagakit-researcher"})
	if elapsed := time.Since(started); elapsed > 800*time.Millisecond {
		t.Fatalf("foreground search drained background work: %v", elapsed)
	}
	if decoder.calls >= 256 || q.Coverage.Complete || !slices.Contains(q.Coverage.Gaps, "index_pending") {
		t.Fatal("cold query lost its short quantum or reported complete history", decoder.calls, q.Coverage)
	}
	if q.MatchedTotal == nil || *q.MatchedTotal != len(q.Sessions) {
		t.Fatal("prepared match count differs from searchable sources", q)
	}
	// The background path resumes the same checkpoint instead of starting again.
	src := s.provider(context.Background()).Sources[0]
	before := decoder.calls
	beforeCheckpoint, _, err := s.store.checkpoint(context.Background(), sourceID(src))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PrepareSource(context.Background(), src, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	s.opMu.Lock()
	st, _ := s.cachedSource(src)
	s.opMu.Unlock()
	if beforeCheckpoint.EventCount > 0 && beforeCheckpoint.Generation == "" {
		t.Fatal("committed baseline lacks generation")
	}
	if decoder.calls <= before || st.checkpoint.Line <= beforeCheckpoint.Line || st.checkpoint.Offset <= beforeCheckpoint.Offset || beforeCheckpoint.Generation != "" && st.checkpoint.Generation != beforeCheckpoint.Generation || st.checkpoint.Line != st.checkpoint.EventCount {
		t.Fatal("background quantum failed to resume the committed prefix", before, decoder.calls, st.checkpoint.Line)
	}
}

func TestTrajectoryRecoveryStoragePauseResumesCheckpoint(t *testing.T) {
	s, path := fixture(t, request("recorded before pause"))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{})
	src := s.provider(context.Background()).Sources[0]
	if err := os.WriteFile(path, []byte(request("recorded before pause")+request("recorded after pause")), 0600); err != nil {
		t.Fatal(err)
	}
	realCheck := s.storageCheck
	beforeCheckpoint, _, err := s.store.checkpoint(context.Background(), sourceID(src))
	if err != nil {
		t.Fatal(err)
	}
	beforeRaw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s.storageCheck = func(string) error { return historyfile.ErrStorageBudget }
	if _, err := s.PrepareSource(context.Background(), src, func() bool { return true }); !errors.Is(err, historyfile.ErrStorageBudget) {
		t.Fatal("low storage advanced background index", err)
	}
	if _, err := s.Query(context.Background(), snapshot.TrajectorySelector{}); !errors.Is(err, historyfile.ErrStorageBudget) {
		t.Fatal("query bypassed storage pause", err)
	}
	s.opMu.Lock()
	st, _ := s.cachedSource(src)
	count := st.checkpoint.EventCount
	s.opMu.Unlock()
	if count != 1 {
		t.Fatal("paused checkpoint advanced", count)
	}
	afterCheckpoint, _, err := s.store.checkpoint(context.Background(), sourceID(src))
	afterRaw, readErr := os.ReadFile(path)
	if err != nil || readErr != nil || !reflect.DeepEqual(beforeCheckpoint, afterCheckpoint) || string(beforeRaw) != string(afterRaw) {
		t.Fatal("storage refusal changed checkpoint or original source", err, readErr)
	}
	beforeJSON, _ := json.Marshal(beforeCheckpoint)
	afterJSON, _ := json.Marshal(afterCheckpoint)
	t.Logf(`CAPACITY_REFUSAL {"kind":"ordinary","before_checkpoint_sha256":"%x","after_checkpoint_sha256":"%x","before_original_sha256":"%x","after_original_sha256":"%x","actual_rejection":true}`, sha256.Sum256(beforeJSON), sha256.Sum256(afterJSON), sha256.Sum256(beforeRaw), sha256.Sum256(afterRaw))
	s.storageCheck = realCheck
	more, err := s.PrepareSource(context.Background(), src, func() bool { return true })
	if err != nil || more {
		t.Fatal("storage recovery did not resume", more, err)
	}
	q := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{})
	if len(q.Sessions) != 1 || q.Sessions[0].EventCount != 2 {
		t.Fatal("storage recovery lost/duplicated records", q)
	}
}

func TestTrajectoryRecoveryStripsSkillCatalogPreservesEvidence(t *testing.T) {
	body := request("# AGENTS.md instructions for /fixture\n\n<INSTRUCTIONS>Research runtime belongs to bagakit-researcher.</INSTRUCTIONS>\n<skills_instructions>bagakit-researcher: available skill catalog</skills_instructions>Inspect project") + codexRecord("response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "read", "arguments": "{\"cmd\":\"cat /skills/bagakit-researcher/SKILL.md\"}"})
	s, _ := fixture(t, body)
	src := s.provider(context.Background()).Sources[0]
	if _, err := s.PrepareSource(context.Background(), src, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	q := searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "bagakit-researcher"})
	if len(q.Sessions) != 1 || exactMatchCount(q.Sessions[0].MatchedCount) != 1 || !strings.Contains(q.Sessions[0].MatchedPreview, "cat /skills/") {
		t.Fatal("injected catalog replaced actual skill read", q)
	}
	q = searchTestQuery(t, s, snapshot.TrajectorySelector{Text: "Inspect project"})
	raw, err := s.Get(context.Background(), snapshot.TrajectoryGetParams{ID: q.Sessions[0].MatchedIDs[0], View: "raw"})
	if err != nil || raw.RawChunk == nil {
		t.Fatal("raw evidence unavailable", err)
	}
	rawBytes, decodeErr := base64.StdEncoding.DecodeString(raw.RawChunk.Data)
	if decodeErr != nil || string(rawBytes) != body[:strings.IndexByte(body, '\n')+1] {
		t.Fatal("raw catalog evidence removed", raw, err)
	}
}

func TestTrajectoryRecoveryWithoutQueryAndRestart(t *testing.T) {
	s, path := fixture(t, strings.Repeat(request("<system-reminder>hidden catalogue</system-reminder>bagakit-researcher evidence"), 900))
	src := s.provider(context.Background()).Sources[0]
	var more bool
	var err error
	var first sourceCheckpoint
	for attempt := 0; attempt < 32; attempt++ {
		more, err = s.PrepareSource(context.Background(), src, func() bool { return true })
		if err != nil || !more {
			t.Fatal(more, err)
		}
		s.opMu.Lock()
		st, _ := s.cachedSource(src)
		first = st.checkpoint
		s.opMu.Unlock()
		if first.Line > 0 {
			break
		}
	}
	if first.Line > 256 || first.Line == 0 {
		t.Fatal("unbounded or absent preparation", first.Line)
	}
	indexPath := s.path
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := NewPersistent(func(context.Context) SourceSet {
		return SourceSet{Sources: []Source{src}, Coverage: coverage("fixture")}
	}, indexPath)
	defer restarted.Close()
	for i := 0; i < 32; i++ {
		more, err = restarted.PrepareSource(context.Background(), src, func() bool { return true })
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if more {
		t.Fatal("did not finish without queries")
	}
	q := searchTestQuery(t, restarted, snapshot.TrajectorySelector{Count: true, Text: "bagakit-researcher"})
	if len(q.Sessions) != 1 || q.Sessions[0].EventCount != 900 || q.MatchedTotal == nil || *q.MatchedTotal != 1 || q.Coverage.Index.SearchableSources != 1 {
		t.Fatal(q)
	}
	if strings.Contains(q.Sessions[0].MatchedPreview, "hidden catalogue") {
		t.Fatal("normalization differs in background")
	}
	before := q.Sessions[0].MatchedIDs[0]
	if err = os.WriteFile(path, []byte(request("replacement")), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.PrepareSource(context.Background(), src, func() bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, err = restarted.Get(context.Background(), snapshot.TrajectoryGetParams{ID: before}); err == nil {
		t.Fatal("replacement kept stale identity")
	}
}

func TestTrajectoryArchiveFairness(t *testing.T) {
	dir := t.TempDir()
	var sources []Source
	for i, name := range []string{"large", "older-a", "older-b", "older-c"} {
		body := request("bagakit-researcher " + name)
		if i == 0 {
			body = strings.Repeat(body, 3000)
		}
		path := filepath.Join(dir, name+".jsonl")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, Source{Agent: "codex", Path: path, Decoder: CodexDecoder{}})
	}
	s := New(func(context.Context) SourceSet { return SourceSet{Sources: sources, Coverage: coverage("fixture")} })
	defer s.Close()
	// The fair background queue offers one quantum to every source before
	// revisiting a growing file. Foreground search may intentionally yield sooner.
	for _, src := range sources {
		if _, err := s.PrepareSource(context.Background(), src, func() bool { return true }); err != nil {
			t.Fatal(err)
		}
	}
	q := searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "bagakit-researcher"})
	if len(q.Sessions) != 4 {
		t.Fatalf("old files starved: %d", len(q.Sessions))
	}
	if q.Coverage.Index.KnownSources != 4 || q.Coverage.Index.SearchableSources < 3 {
		t.Fatal(q.Coverage)
	}
	if q.Coverage.Omitted != 0 {
		t.Fatal("pending preparation reported as omitted evidence", q.Coverage)
	}
	for i := 0; i < 3; i++ {
		f, err := os.OpenFile(sources[0].Path, os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(strings.Repeat(request("newer"), 700))
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		q = searchTestQuery(t, s, snapshot.TrajectorySelector{Count: true, Text: "bagakit-researcher"})
		if len(q.Sessions) != 4 {
			t.Fatal("growing newest source starved archived matches")
		}
	}
}

func TestTrajectoryReplayEquivalence(t *testing.T) {
	body := strings.Repeat(request("<system-reminder>strip me</system-reminder>Read bagakit-researcher"), 10) + codexRecord("response_item", map[string]any{"type": "function_call", "name": "exec_command", "call_id": "c", "arguments": "{\"cmd\":\"cat /skills/bagakit-researcher/SKILL.md\"}"}) + codexRecord("response_item", map[string]any{"type": "function_call_output", "call_id": "c", "output": "Exit code: 0\nsuccess"})
	s, path := fixture(t, body)
	defer s.Close()
	src := s.provider(context.Background()).Sources[0]
	stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(path, stamp, stamp)
	full := func(s *Service) []byte {
		s.opMu.Lock()
		defer s.opMu.Unlock()
		if err := s.openIndex(); err != nil {
			t.Fatal(err)
		}
		st, err := s.indexSource(context.Background(), src)
		if err != nil {
			t.Fatal(err)
		}
		var events []snapshot.TrajectoryEvent
		scan(context.Background(), st, func(e snapshot.TrajectoryEvent) bool { events = append(events, e); return true })
		b, _ := json.Marshal(events)
		return b
	}
	expected := full(s)
	// Same physical source, first prepared in chunks, then appended. Source
	// identity and the initial prefix stay constant; both flows share Decoder.
	split := strings.Index(body, "\n") + 1
	for split < 256 {
		split += strings.Index(body[split:], "\n") + 1
	}
	if err := os.WriteFile(path, []byte(body[:split]), 0600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(path, stamp, stamp)
	other := NewPersistent(s.provider, filepath.Join(t.TempDir(), "index.bbolt"))
	defer other.Close()
	if _, err := other.PrepareSource(context.Background(), src, func() bool { return true }); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	f.WriteString(body[split:])
	f.Close()
	os.Chtimes(path, stamp, stamp)
	if got := full(other); !reflect.DeepEqual(got, expected) {
		t.Fatalf("offline and append projection differ\n%s\n%s", expected, got)
	}
}
