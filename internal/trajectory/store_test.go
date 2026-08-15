package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrajectoryFactStoreLosslessAndSparse(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	src := snapshot.TrajectorySourceRef{ID: "source", Generation: "generation", Line: 2, Offset: 81, Length: 140, Block: 1, Digest: "digest", NativeType: "native"}
	now := time.Now().UTC().Truncate(time.Millisecond)
	event := snapshot.TrajectoryEvent{ID: sourceEventID(src), SessionID: "s.source.generation", Timestamp: &now, Kind: "tool_call", Role: "assistant", Actor: snapshot.TrajectoryActor{ID: "agentA", Kind: "agent"}, Text: "Hello 世界\x00BAGAKIT", Tool: &snapshot.TrajectoryTool{Name: "EXEC_Command", CallID: "call-1", Arguments: json.RawMessage(`{"cmd":"echo Bagakit-Researcher","nested":{"x":"<>&"}}`)}, Source: src, Usage: &snapshot.TrajectoryUsage{Scope: "session", Aggregation: "delta", Counters: map[string]int64{"output_tokens": 19}}}
	foreign := src
	foreign.Generation = "other"
	event.Entities = []snapshot.TrajectoryEntityOccurrence{
		{EntityID: occurrenceEntityID("skill", "session:"+event.SessionID, "bagakit-researcher"), Kind: "skill", Literal: "bagakit-researcher", Label: "BAGAKIT-RESEARCHER", Scope: "session:" + event.SessionID, Predicate: "uses", NativeField: "tool.args", Source: src},
		{ID: "external-occ", EntityID: "foreign-entity", EventID: "foreign-event", SessionID: "foreign-session", Kind: "file", Literal: "/a", Label: "label", Scope: "global", Predicate: "mentions", NativeField: "native.field", Source: foreign},
	}
	event.Entities[0].EventID = event.ID
	event.Entities[0].SessionID = event.SessionID
	event.Entities[0].ID = occurrenceID(event.ID, event.Entities[0].EntityID, "uses", "tool.args")
	expected, err := encodeEventStored(event)
	if err != nil {
		t.Fatal(err)
	}
	want, err := decodeEventStored(expected)
	if err != nil {
		t.Fatal(err)
	}
	var source, row int64
	err = f.write(context.Background(), func(w *factWriter) error {
		source, err = w.source(src.ID, "codex", sourceCheckpoint{Version: projectionVersion, Generation: src.Generation, EventCount: 2, Coverage: coverage("test")})
		if err != nil {
			return err
		}
		row, err = w.event(source, want, true)
		if err != nil {
			return err
		}
		empty := snapshot.TrajectoryEvent{ID: "custom-empty-id", SessionID: "custom-session", Kind: "unknown", Role: "unknown", Actor: snapshot.TrajectoryActor{Kind: "unknown"}, Source: src}
		empty.Source.Offset = 222
		empty.Source.Block = 0
		_, err = w.event(source, empty, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.event(context.Background(), src.ID, 81, 1)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(want)
	b, _ := json.Marshal(got)
	if string(a) != string(b) {
		t.Fatalf("event changed\n%s\n%s", a, b)
	}
	var contents, fts, size int
	if err = f.db.QueryRow("SELECT count(*) FROM contents").Scan(&contents); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM text_fts").Scan(&fts); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("PRAGMA page_size").Scan(&size); err != nil {
		t.Fatal(err)
	}
	if contents != 1 || fts != 1 || size != 4096 {
		t.Fatalf("content=%d fts=%d page=%d", contents, fts, size)
	}
	// FTS is candidate-only; NUL and exact normalized content remain readable.
	var body []byte
	var bodyOffset, bodyLength int
	if err = f.db.QueryRow("SELECT b.body,c.offset,c.length FROM contents c JOIN blocks b ON b.rowid=c.block WHERE c.rowid=?", row).Scan(&body, &bodyOffset, &bodyLength); err != nil {
		t.Fatal(err)
	}
	raw, err := factBlockSlice(context.Background(), body, bodyOffset, bodyLength)
	if err != nil {
		t.Fatal(err)
	}
	text, args, err := parseFactContent(raw)
	if err != nil {
		t.Fatal(err)
	}
	projection, _, _, _ := searchProjection(want)
	if factSearchText(text, want.Tool.Name, args, true) != projection {
		t.Fatal("search projection changed")
	}
	if _, err = f.db.Exec("DELETE FROM events WHERE rowid=?", row); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM text_fts").Scan(&fts); err != nil || fts != 0 {
		t.Fatalf("contentless delete failed: %d %v", fts, err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM contents").Scan(&contents); err != nil || contents != 0 {
		t.Fatalf("content cascade failed: %d %v", contents, err)
	}
}

func TestTrajectoryFactContentBoundsAndIntegrity(t *testing.T) {
	for _, text := range []string{"", "世界\x00ABC", strings.Repeat("some text with ", 5000)} {
		args := []byte(`{"a": "TEST"}`)
		body, err := encodeFactContent(text, args)
		if err != nil {
			t.Fatal(err)
		}
		got, a, err := decodeFactContent(body)
		if err != nil || text != got || string(a) != string(args) {
			t.Fatalf("round trip: %v", err)
		}
		if len(body) > 8 && body[3] == 4 {
			body[len(body)-1] ^= 1
			if _, _, err = decodeFactContent(body); err == nil {
				t.Fatal("corrupt checksum accepted")
			}
		}
	}
	if _, _, err := decodeFactContent([]byte("bad")); err == nil {
		t.Fatal("bad frame accepted")
	}
}

func TestTrajectoryFactStoreExactQueriesAndOccurrenceConjunction(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	events := []snapshot.TrajectoryEvent{
		{Kind: "text", Role: "user", Text: strings.Repeat("a", 32767) + "世界Σ\x00Bagakit-researcher[*?"},
		{Kind: "tool_call", Role: "assistant", Text: "before", Tool: &snapshot.TrajectoryTool{Name: "Exec_Command", CallID: "one", Arguments: json.RawMessage(`{"cmd":"AFTER 世界"}`)}},
		{Kind: "text", Role: "user", Text: "abXXbcXXcd"},
		{Kind: "unknown", Role: "unknown"},
	}
	for i := range events {
		e := &events[i]
		e.Source = snapshot.TrajectorySourceRef{ID: "a", Generation: "b", Offset: int64(i), Digest: "hash"}
		e.ID = sourceEventID(e.Source)
		e.SessionID = "s.a.b"
	}
	events[0].Entities = []snapshot.TrajectoryEntityOccurrence{
		{EntityID: "ent.skill", Kind: "skill", Literal: "bagakit-researcher", Label: "bagakit-researcher", Predicate: "mention", Scope: "session:s.a.b"},
		{EntityID: "ent.other", Kind: "path", Literal: "/a", Label: "/a", Predicate: "loaded", Scope: "local_path"},
	}
	for i := range events[0].Entities {
		o := &events[0].Entities[i]
		o.EventID = events[0].ID
		o.SessionID = events[0].SessionID
		o.Source = events[0].Source
		o.ID = occurrenceID(o.EventID, o.EntityID, o.Predicate, o.NativeField)
	}
	err = f.write(context.Background(), func(w *factWriter) error {
		s, err := w.source("a", "codex", sourceCheckpoint{Generation: "b"})
		if err != nil {
			return err
		}
		for _, e := range events {
			if _, err = w.event(s, e, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		q    snapshot.TrajectorySelector
		want int
	}{
		{snapshot.TrajectorySelector{Text: "世界σ"}, 1},
		{snapshot.TrajectorySelector{Text: "\x00bagakit-researcher[*?"}, 1},
		{snapshot.TrajectorySelector{Text: "界"}, 2},
		{snapshot.TrajectorySelector{Text: "after", Tool: "EXEC_COMMAND"}, 1},
		{snapshot.TrajectorySelector{Text: "abcd"}, 0},
		{snapshot.TrajectorySelector{Skill: "BAGAKIT-RESEARCHER", Predicate: "mention"}, 1},
		{snapshot.TrajectorySelector{Skill: "BAGAKIT-RESEARCHER", Predicate: "loaded"}, 0},
		{snapshot.TrajectorySelector{EntityKind: "path", Predicate: "loaded"}, 1},
	}
	for _, c := range cases {
		where, args := factWhere(c.q)
		if c.q.Text != "" {
			if err = f.prepareTextMatches(context.Background(), c.q); err != nil {
				t.Fatal(err)
			}
			defer f.db.Exec("DROP TABLE IF EXISTS temp.trajectory_text_matches")
			where += " AND d.rowid IN (SELECT rowid FROM temp.trajectory_text_matches)"
		}
		var n int
		if err = f.db.QueryRow("SELECT count(*) FROM events d JOIN sources s ON s.rowid=d.source WHERE "+where, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != c.want {
			t.Fatalf("query %+v: got %d want %d", c.q, n, c.want)
		}
	}
}

func TestTrajectoryFactGenerationPairingAndConnectionRecovery(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	makeEvent := func(generation string, off int64, kind string) snapshot.TrajectoryEvent {
		src := snapshot.TrajectorySourceRef{ID: "src", Generation: generation, Offset: off, Digest: "hash"}
		return snapshot.TrajectoryEvent{ID: sourceEventID(src), SessionID: "s.src." + generation, Kind: kind, Role: "tool", Tool: &snapshot.TrajectoryTool{Name: "exec", CallID: "call", Arguments: json.RawMessage(`{"a":1}`)}, Source: src}
	}
	err = f.write(context.Background(), func(w *factWriter) error {
		s, err := w.source("src", "codex", sourceCheckpoint{Generation: "old"})
		if err != nil {
			return err
		}
		for _, e := range []snapshot.TrajectoryEvent{makeEvent("old", 0, "tool_call"), makeEvent("old", 1, "tool_result"), makeEvent("old", 2, "tool_call")} {
			if _, err = w.event(s, e, true); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pair, err := f.pair(context.Background(), &sourceState{ID: "src", Generation: "old"}, "call")
	if err != nil || len(pair.Calls) != 2 || len(pair.Results) != 1 {
		t.Fatalf("ambiguity lost: %+v %v", pair, err)
	}
	// Retire exactly as failed COMMIT recovery does. New connections must retain
	// foreign keys, so a later delete cannot leak a body or a stale pairing link.
	conn, err := f.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	_ = conn.Close()
	var fk int
	if err = f.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
		t.Fatalf("foreign keys lost: %d %v", fk, err)
	}
	err = f.write(context.Background(), func(w *factWriter) error {
		s, err := w.source("src", "codex", sourceCheckpoint{Generation: "new"})
		if err != nil {
			return err
		}
		if _, err = w.event(s, makeEvent("old", 3, "tool_call"), true); !errors.Is(err, ErrStale) {
			t.Fatalf("foreign generation accepted: %v", err)
		}
		_, err = w.event(s, makeEvent("new", 0, "tool_call"), true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.event(context.Background(), "src", 0, 0)
	if err != nil || got.Source.Generation != "new" || got.ID != makeEvent("new", 0, "tool_call").ID {
		t.Fatalf("generation inherited old body: %+v %v", got, err)
	}
	// Retired evidence stays immutable until the bounded reclaim path runs.
	var oldEvents int
	if err = f.db.QueryRow("SELECT count(*) FROM events e JOIN sources s ON s.rowid=e.source WHERE s.generation='old'").Scan(&oldEvents); err != nil || oldEvents != 3 {
		t.Fatalf("retired originals changed: %d %v", oldEvents, err)
	}
	if pending, err := f.reclaim(context.Background()); err != nil || pending {
		t.Fatalf("reclaim: %v %v", pending, err)
	}
	var retired int
	if err = f.db.QueryRow("SELECT count(*) FROM sources WHERE active=0").Scan(&retired); err != nil || retired != 0 {
		t.Fatalf("completed reclaim left retired sources: %d %v", retired, err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM blocks WHERE refs<>((SELECT count(*) FROM contents WHERE block=blocks.rowid)+(SELECT count(*) FROM event_facts WHERE block=blocks.rowid))").Scan(&oldEvents); err != nil || oldEvents != 0 {
		t.Fatalf("block references drift: %d %v", oldEvents, err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM events e JOIN sources s ON s.rowid=e.source WHERE s.generation='old'").Scan(&oldEvents); err != nil || oldEvents != 0 {
		t.Fatalf("retired events retained: %d %v", oldEvents, err)
	}
	if _, err = f.db.Exec("DELETE FROM events"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM blocks").Scan(&oldEvents); err != nil || oldEvents != 0 {
		t.Fatalf("orphan blocks: %d %v", oldEvents, err)
	}
	var pointer sql.NullInt64
	if err = f.db.QueryRow("SELECT call1 FROM pairs p JOIN sources s ON s.rowid=p.source WHERE s.generation='new'").Scan(&pointer); err != nil || pointer.Valid {
		t.Fatalf("dead pointer survived: %+v %v", pointer, err)
	}
}

func TestTrajectoryFactReclaimDrainsEmptySourcesAndSymbolTail(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	err = f.write(context.Background(), func(w *factWriter) error {
		for i := 0; i < 130; i++ {
			id := fmt.Sprint(i)
			if _, err := w.source(id, "old-agent-"+id, sourceCheckpoint{Generation: "old"}); err != nil {
				return err
			}
			if _, err := w.source(id, "current-agent", sourceCheckpoint{Generation: "new"}); err != nil {
				return err
			}
		}
		for i := 0; i < 900; i++ {
			if _, err := w.symbol(fmt.Sprintf("unused-%d", i)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.reclaim(context.Background())
	if err != nil || !pending {
		t.Fatalf("tail disappeared from maintenance: %v %v", pending, err)
	}
	var retired, unused int
	if err = f.db.QueryRow("SELECT count(*) FROM sources WHERE active=0").Scan(&retired); err != nil || retired != 66 {
		t.Fatalf("unbounded empty-source batch: %d %v", retired, err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM symbols WHERE value LIKE 'unused-%'").Scan(&unused); err != nil || unused < 644 {
		t.Fatalf("unbounded symbol batch: %d %v", unused, err)
	}
	for i := 0; i < 10 && pending; i++ {
		pending, err = f.reclaim(context.Background())
		if err != nil {
			t.Fatal(err)
		}
	}
	if pending {
		t.Fatal("empty-source/symbol tail did not converge")
	}
	var active, symbols int
	if err = f.db.QueryRow("SELECT count(*) FROM sources WHERE active=1").Scan(&active); err != nil || active != 130 {
		t.Fatalf("current sources changed: %d %v", active, err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM symbols").Scan(&symbols); err != nil || symbols != 2 {
		t.Fatalf("unreferenced tail retained or current agent lost: %d %v", symbols, err)
	}
}

func TestTrajectoryFactReclaimBoundsLogicalBytes(t *testing.T) {
	for _, size := range []int{80 << 10, 500 << 10} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer f.db.Close()
			err = f.write(context.Background(), func(w *factWriter) error {
				s, err := w.source("src", "codex", sourceCheckpoint{Generation: "old"})
				if err != nil {
					return err
				}
				for i := 0; i < 5; i++ {
					src := snapshot.TrajectorySourceRef{ID: "src", Generation: "old", Offset: int64(i), Digest: "digest"}
					e := snapshot.TrajectoryEvent{ID: sourceEventID(src), SessionID: "s.src.old", Source: src, Kind: "text", Role: "user", Text: strings.Repeat("x", size)}
					if _, err = w.event(s, e, false); err != nil {
						return err
					}
				}
				_, err = w.source("src", "codex", sourceCheckpoint{Generation: "new"})
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			pending, err := f.reclaim(context.Background())
			if err != nil || !pending {
				t.Fatalf("bounded reclaim lost pending: %v %v", pending, err)
			}
			want := 2 // three 80 KiB records fit; the next exceeds 256 KiB.
			if size > 256<<10 {
				want = 4
			} // one oversized native record progresses alone.
			var remaining int
			if err = f.db.QueryRow("SELECT count(*) FROM events").Scan(&remaining); err != nil || remaining != want {
				t.Fatalf("byte budget: remaining=%d want=%d err=%v", remaining, want, err)
			}
		})
	}
}

func TestTrajectoryFactQueryCorruptionAndCancellation(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	err = f.write(context.Background(), func(w *factWriter) error {
		s, err := w.source("src", "codex", sourceCheckpoint{Generation: "gen"})
		if err != nil {
			return err
		}
		src := snapshot.TrajectorySourceRef{ID: "src", Generation: "gen", Digest: "hash"}
		_, err = w.event(s, snapshot.TrajectoryEvent{ID: sourceEventID(src), SessionID: "s.src.gen", Text: strings.Repeat("research-x ", 10000), Kind: "text", Role: "user", Source: src}, true)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	var row int64
	var body []byte
	if err = f.db.QueryRow("SELECT b.rowid,b.body FROM blocks b WHERE b.kind=1").Scan(&row, &body); err != nil {
		t.Fatal(err)
	}
	body[len(body)-1] ^= 1
	if _, err = f.db.Exec("UPDATE blocks SET body=? WHERE rowid=?", body, row); err != nil {
		t.Fatal(err)
	}
	if err = f.prepareTextMatches(context.Background(), snapshot.TrajectorySelector{Text: "research"}); err == nil {
		t.Fatal("matched prefix bypassed block checksum")
	}
	var remains bool
	if err = f.db.QueryRow("SELECT EXISTS(SELECT 1 FROM sqlite_temp_master WHERE name='trajectory_text_matches')").Scan(&remains); err != nil || remains {
		t.Fatalf("failed-query rows survived: %v %v", remains, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = f.prepareTextMatches(ctx, snapshot.TrajectorySelector{Text: "research"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation ignored: %v", err)
	}
}

func TestTrajectoryCancelledFactOpenPreservesExistingAndMissingStores(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	missing := filepath.Join(t.TempDir(), "absent.sqlite")
	if _, err := openFactStoreContext(ctx, missing); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled creation", err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("cancelled creation touched file", err)
	}
	existing := filepath.Join(t.TempDir(), "existing.sqlite")
	f, err := openFactStore(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("INSERT INTO meta VALUES('preserved','value')"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openFactStoreContext(ctx, existing); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled reopen", err)
	}
	after, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled reopen changed existing facts")
	}
	f, err = openFactStore(existing)
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	var cancelledValue string
	if err = f.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='preserved'").Scan(&cancelledValue); !errors.Is(err, context.Canceled) {
		t.Fatal("metadata read ignored cancellation", err)
	}
	var value string
	if err = f.db.QueryRow("SELECT value FROM meta WHERE key='preserved'").Scan(&value); err != nil || value != "value" {
		t.Fatal("cancelled operations lost evidence", value, err)
	}
}

func TestTrajectoryCheckpointSnapshotCancellationJoinsDecoders(t *testing.T) {
	f, err := openFactStore(filepath.Join(t.TempDir(), "trajectory.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.db.Close()
	c := sourceCheckpoint{Version: projectionVersion, Generation: "generation", Coverage: coverage("fixture")}
	for i := range 600 {
		c.Tools = append(c.Tools, fmt.Sprintf("recorded-tool-%04d", i))
	}
	const sources = 400
	if err = f.write(context.Background(), func(w *factWriter) error {
		for i := range sources {
			if _, err := w.source(fmt.Sprintf("source-%04d", i), "codex", c); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
		got, err := f.checkpoints(ctx)
		cancel()
		if err == nil {
			if len(got) != sources {
				t.Fatal("successful read published unfinished decoders", len(got))
			}
		} else if !errors.Is(err, context.DeadlineExceeded) || got != nil {
			t.Fatal("cancelled snapshot published partial metadata", len(got), err)
		}
	}
	got, err := f.checkpoints(context.Background())
	if err != nil || len(got) != sources {
		t.Fatal("cancelled readers stranded the owner or lost checkpoints", len(got), err)
	}
	want, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for id, restored := range got {
		body, err := json.Marshal(restored)
		if err != nil || !bytes.Equal(want, body) {
			t.Fatal("checkpoint fields crossed sources", id, err)
		}
	}
}
