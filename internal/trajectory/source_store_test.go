package trajectory

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStoredMigrationCleanupIsBoundedAndRefusalPreservesCursor(t *testing.T) {
	f := newSourceStoreFixture(t)
	state := sourceMigration{Version: sourceStoreVersion, Phase: "sources", Source: 1, Mode: "stored", FactOffset: -1, FactBlock: -1, Records: 123}
	if err := saveSourceMigration(context.Background(), f, state); err != nil {
		t.Fatal(err)
	}
	// A large unpublished partial replay, plus a different source that must
	// survive. Original facts have not yet been read or their cursor advanced.
	oldBody := bytes.Repeat([]byte("retained unpublished range "), 2600)
	err := f.write(context.Background(), 40*1024*1024, func(tx *sql.Tx) error {
		for row := 1; row <= 2; row++ {
			if _, err := tx.Exec("INSERT INTO sources(rowid,id,generation,agent,mtime,checkpoint) VALUES(?,?,?,'codex',0,?)", row, fmt.Sprintf("source-%d", row), "original", []byte("checkpoint")); err != nil {
				return err
			}
		}
		for offset := 0; offset < 257; offset++ {
			if _, err := tx.Exec("INSERT INTO ranges(source,start,end,body,filter) VALUES(1,?,?,?,?)", offset, offset+1, oldBody, []byte("filter")); err != nil {
				return err
			}
		}
		_, err := tx.Exec("INSERT INTO ranges(source,start,end,body,filter) VALUES(2,0,1,?,?)", oldBody, []byte("other-source"))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(f.path)
	if err != nil {
		t.Fatal(err)
	}
	f.checkCapacity = func(_ string, required uint64) error {
		if required < 2*64*uint64(len(oldBody)+len("filter"))+4*1024*1024 {
			t.Fatal("old rows and journal were omitted from capacity", required)
		}
		return historyfile.ErrStorageBudget
	}
	run := &sourceMigrationRun{shadow: f}
	done, err := migrateStoredSource(context.Background(), run, &state, 1, "source-1", "codex", 1, 0, sourceCheckpoint{}, 0, -1, -1)
	if done || !errors.Is(err, historyfile.ErrStorageBudget) || state.FactOffset != -1 || state.Records != 123 {
		t.Fatal("refusal advanced cleanup or original fact progress", done, err, state)
	}
	after, err := os.ReadFile(f.path)
	if err != nil || sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("capacity refusal mutated the target or checkpoint", err)
	}
	f.checkCapacity = historyfile.CheckStorageCapacity
	done, err = migrateStoredSource(context.Background(), run, &state, 1, "source-1", "codex", 1, 0, sourceCheckpoint{}, 0, -1, -1)
	if done || err != nil || state.FactOffset != -1 || state.Records != 123 {
		t.Fatal("partial cleanup read/advanced original facts", done, err, state)
	}
	var remaining, other int
	if err = f.db.QueryRow("SELECT count(*) FROM ranges WHERE source=1").Scan(&remaining); err != nil || remaining != 193 {
		t.Fatal("cleanup exceeded its 64-row batch", remaining, err)
	}
	if err = f.db.QueryRow("SELECT count(*) FROM ranges WHERE source=2").Scan(&other); err != nil || other != 1 {
		t.Fatal("cleanup touched another source", other, err)
	}
	committed, _, err := sourceMigrationState(context.Background(), f)
	if err != nil || committed.FactOffset != -1 || committed.Records != 123 {
		t.Fatal("cleanup changed durable original progress", committed, err)
	}
}

func newSourceStoreFixture(t *testing.T) *sourceStore {
	t.Helper()
	f, err := openSourceStore(context.Background(), filepath.Join(t.TempDir(), "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	return f
}

func canonicalFixtureFacts(t *testing.T, s *Service, st *sourceState) []sourceFact {
	t.Helper()
	facts := []sourceFact{}
	err := legacyOracleFor(t, st).walk(context.Background(), st, func(e snapshot.TrajectoryEvent, n int) error {
		facts = append(facts, sourceFact{e.Source.Offset, e.Source.Block, e, n})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return facts
}

func importFixtureFacts(t *testing.T, f *sourceStore, st *sourceState, facts []sourceFact) []replayChunk {
	t.Helper()
	chunks := []replayChunk{}
	anchor := replayAnchor{}
	for anchor.Offset < st.checkpoint.Offset {
		chunk, err := nextReplayChunk(context.Background(), st, anchor)
		if err != nil {
			t.Fatal(err)
		}
		part := []sourceFact{}
		for _, fact := range facts {
			if fact.offset >= chunk.Start.Offset && fact.offset < chunk.End.Offset {
				part = append(part, fact)
			}
		}
		if err = f.importRange(context.Background(), st, chunk, &part); err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
		anchor = chunk.End
	}
	lastOffset, lastBlock := int64(-1), -1
	if len(facts) > 0 {
		lastOffset, lastBlock = facts[len(facts)-1].offset, facts[len(facts)-1].block
	}
	if err := f.completeSource(context.Background(), st, len(facts), lastOffset, lastBlock); err != nil {
		t.Fatal(err)
	}
	return chunks
}

func sourceStoreFacts(t *testing.T, f *sourceStore, st *sourceState, q *snapshot.TrajectorySelector) []snapshot.TrajectoryEvent {
	t.Helper()
	result := []snapshot.TrajectoryEvent{}
	err := f.walkQuery(context.Background(), st, q, func(e snapshot.TrajectoryEvent, n int) error {
		raw, _ := json.Marshal(e)
		if n != len(raw) {
			t.Fatal("logical budget changed")
		}
		if q == nil || matches(e, *q) {
			result = append(result, e)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestTrajectorySourceStoreReopenFourVendors(t *testing.T) {
	for _, tc := range []struct {
		agent   string
		decoder Decoder
		body    string
	}{
		{"codex", CodexDecoder{}, request("bagakit-researcher") + call("one") + result("one")},
		{"claude", ClaudeDecoder{}, `{"type":"user","uuid":"one","cwd":"/recorded","message":{"role":"user","content":"bagakit-researcher"}}` + "\n" + `{"type":"assistant","uuid":"two","message":{"role":"assistant","content":[{"type":"tool_use","id":"one","name":"Read","input":{"file_path":"skills/bagakit-researcher/SKILL.md"}},{"type":"text","text":""}]}}` + "\n"},
		{"trae", TraeDecoder{}, codexRecord("history_mutation", map[string]any{"version": 1, "operation": "append", "commit_id": "commit", "items": []any{map[string]any{"type": "message", "id": "one", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "bagakit-researcher"}}}}})},
		{"grok", GrokDecoder{}, `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"bagakit-researcher"}}}}` + "\n"},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			s, st := replayFixture(t, tc.agent, tc.decoder, tc.body)
			facts := canonicalFixtureFacts(t, s, st)
			if len(facts) == 0 {
				t.Fatal("fixture has no witness")
			}
			f := newSourceStoreFixture(t)
			importFixtureFacts(t, f, st, facts)
			path := f.path
			if err := f.db.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			f, err = openSourceStore(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = f.db.Close() })
			cp, ok, err := f.checkpoint(context.Background(), st.ID)
			if err != nil || !ok || !reflect.DeepEqual(cp, st.checkpoint) {
				t.Fatal("checkpoint changed", err)
			}
			got := sourceStoreFacts(t, f, st, nil)
			expected := []snapshot.TrajectoryEvent{}
			for _, fact := range facts {
				expected = append(expected, fact.event)
				e, err := f.event(context.Background(), st, fact.offset, fact.block)
				if err != nil || !reflect.DeepEqual(e, fact.event) {
					t.Fatal("complete point-read DTO changed", err)
				}
				oldRaw, err := readRecord(st, fact.event.Source)
				if err != nil {
					t.Fatal(err)
				}
				newRaw, err := readRecord(st, e.Source)
				if err != nil || !reflect.DeepEqual(oldRaw, newRaw) {
					t.Fatal("raw changed", err)
				}
			}
			if !reflect.DeepEqual(got, expected) {
				t.Fatal("ordered complete DTOs changed")
			}
			q := snapshot.TrajectorySelector{Text: "bagakit-researcher"}
			hits := sourceStoreFacts(t, f, st, &q)
			if len(hits) == 0 {
				t.Fatal("candidate index omitted an exact match")
			}
			var exceptions int
			if err = f.db.QueryRow("SELECT COUNT(*) FROM exceptions").Scan(&exceptions); err != nil || exceptions != 0 {
				t.Fatal("reproducible facts were copied", exceptions, err)
			}
		})
	}
}

func TestTrajectorySourceStoreRetainsAddedChangedAndSuppressedFacts(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("original first")+request("must suppress")+request("original last"))
	facts := canonicalFixtureFacts(t, s, st)
	if len(facts) != 3 {
		t.Fatal(len(facts))
	}
	// Change fields that cannot be reconstructed from the current source. The
	// physical key remains independent of these complete canonical DTO fields.
	facts[0].event.ID = "legacy-opaque-id"
	facts[0].event.Text = "Only in canonical: bagakit-researcher"
	facts[0].event.Entities = []snapshot.TrajectoryEntityOccurrence{
		{Kind: "skill", EntityID: "foreign-entity", Label: "CanonicalSkill", Literal: "/original/SKILL.md", Predicate: "requested_read"},
		{Kind: "path", EntityID: "foreign-path", Label: "another", Literal: "/original/path", Predicate: "mention"},
	}
	facts[0].event.EntityCoverage = &snapshot.TrajectoryCoverage{Scope: "legacy", Complete: false, Gaps: []string{"original-gap"}}
	extra := facts[0]
	extra.block = 17
	extra.event.ID = "another-opaque-id"
	extra.event.Kind = "summary"
	extra.event.Text = "extra canonical event"
	extra.event.Entities = nil
	extra.event.EntityCoverage = nil
	facts = []sourceFact{facts[0], extra, facts[2]}
	f := newSourceStoreFixture(t)
	chunks := importFixtureFacts(t, f, st, facts)
	if len(chunks) != 1 {
		t.Fatal(len(chunks))
	}
	got := sourceStoreFacts(t, f, st, nil)
	expected := []snapshot.TrajectoryEvent{facts[0].event, extra.event, facts[2].event}
	if !reflect.DeepEqual(got, expected) {
		t.Fatal("exceptions changed or omitted canonical facts")
	}
	for _, q := range []snapshot.TrajectorySelector{{Text: "bagakit-researcher"}, {EntityID: "foreign-entity"}, {Skill: "CanonicalSkill", Predicate: "requested_read"}, {Kind: "summary"}} {
		if len(sourceStoreFacts(t, f, st, &q)) != 1 {
			t.Fatalf("exception absent from candidate index: %+v", q)
		}
	}
	q := snapshot.TrajectorySelector{EntityKind: "path", Predicate: "requested_read"}
	if len(sourceStoreFacts(t, f, st, &q)) != 0 {
		t.Fatal("candidate conjunction became same-occurrence truth")
	}
	if _, err := f.event(context.Background(), st, canonicalFixtureFacts(t, s, st)[1].offset, 0); !errors.Is(err, ErrNotFound) {
		t.Fatal("suppressed fact was invented", err)
	}
	if err := f.importRange(context.Background(), st, chunks[0], &facts); err != nil {
		t.Fatal(err)
	}
	if err := f.completeSource(context.Background(), st, len(facts), facts[2].offset, facts[2].block); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sourceStoreFacts(t, f, st, nil), expected) {
		t.Fatal("retry duplicated or altered exceptions")
	}
}

func TestTrajectorySourceStoreIncompleteOrCorruptRangeNeverReturnsEmpty(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request(strings.Repeat("filler ", 24000))+request("last"))
	facts := canonicalFixtureFacts(t, s, st)
	f := newSourceStoreFixture(t)
	chunk, err := nextReplayChunk(context.Background(), st, replayAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	if chunk.End.Offset >= st.checkpoint.Offset {
		t.Fatal("fixture is not multiple ranges")
	}
	part := []sourceFact{facts[0]}
	if err = f.importRange(context.Background(), st, chunk, &part); err != nil {
		t.Fatal(err)
	}
	emitted := 0
	visit := func(snapshot.TrajectoryEvent, int) error { emitted++; return nil }
	if err = f.walkQuery(context.Background(), st, nil, visit); !errors.Is(err, errStorageMigration) || emitted != 0 {
		t.Fatal("partial migration was published", emitted, err)
	}
	if err = f.completeSource(context.Background(), st, len(facts), facts[1].offset, 0); !errors.Is(err, ErrStale) {
		t.Fatal("hole marked complete", err)
	}
	importFixtureFacts(t, f, st, facts)
	if _, err = f.db.Exec("DELETE FROM ranges WHERE start=0"); err != nil {
		t.Fatal(err)
	}
	if err = f.walkQuery(context.Background(), st, nil, visit); !errors.Is(err, ErrStale) || emitted != 0 {
		t.Fatal("missing range became empty/partial success", emitted, err)
	}
	importFixtureFacts(t, f, st, facts)
	if _, err = f.db.Exec("UPDATE ranges SET filter=zeroblob(length(filter)) WHERE start=0"); err != nil {
		t.Fatal(err)
	}
	q := snapshot.TrajectorySelector{Text: "impossible"}
	if err = f.walkQuery(context.Background(), st, &q, visit); err == nil || emitted != 0 {
		t.Fatal("damaged filter excluded evidence silently", emitted, err)
	}
}

func TestTrajectorySourceStoreSourceMissingAndRewrite(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("first"))
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
	if err := os.WriteFile(st.Path, []byte(request("other")), 0600); err != nil {
		t.Fatal(err)
	}
	emitted := 0
	visit := func(snapshot.TrajectoryEvent, int) error { emitted++; return nil }
	if err := f.walkQuery(context.Background(), st, nil, visit); !errors.Is(err, ErrStale) || emitted != 0 {
		t.Fatal("rewrite emitted old facts", err)
	}
	if err := os.Remove(st.Path); err != nil {
		t.Fatal(err)
	}
	if err := f.walkQuery(context.Background(), st, nil, visit); !errors.Is(err, ErrStale) || emitted != 0 {
		t.Fatal("missing source became historical content access", err)
	}
	var sources, ranges int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM ranges").Scan(&ranges); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || ranges != 1 {
		t.Fatal("missing source deleted recovery state")
	}
}

func TestTrajectorySourceStoreEmptyPhysicalSourceIsComplete(t *testing.T) {
	_, st := replayFixture(t, "codex", CodexDecoder{}, "")
	f := newSourceStoreFixture(t)
	for attempt := 0; attempt < 2; attempt++ {
		if err := f.completeSource(context.Background(), st, 0, -1, -1); err != nil {
			t.Fatal("empty source could not migrate/retry", err)
		}
		if got := sourceStoreFacts(t, f, st, nil); len(got) != 0 {
			t.Fatal("empty source invented events")
		}
		cp, ok, err := f.checkpoint(context.Background(), st.ID)
		if err != nil || !ok || !reflect.DeepEqual(cp, st.checkpoint) {
			t.Fatal("empty source checkpoint lost", err)
		}
	}
	var sources, ranges int
	if err := f.db.QueryRow("SELECT COUNT(*) FROM sources").Scan(&sources); err != nil {
		t.Fatal(err)
	}
	if err := f.db.QueryRow("SELECT COUNT(*) FROM ranges").Scan(&ranges); err != nil {
		t.Fatal(err)
	}
	if sources != 1 || ranges != 0 {
		t.Fatal("retry copied an empty slot", sources, ranges)
	}
}

func TestTrajectorySourceStoreNegativeCandidatesCannotTrustRewriteThenAppend(t *testing.T) {
	body := request(strings.Repeat("prefix ", 80)) + request("ordinary middle") + request("last anchor")
	s, st := replayFixture(t, "codex", CodexDecoder{}, body)
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
	q := snapshot.TrajectorySelector{Text: "newmatch"}
	ranges, err := f.sourceRanges(context.Background(), st, -1, 1)
	if err != nil || len(ranges) != 1 {
		t.Fatal(err)
	}
	if ranges[0].filter.maybe(q) {
		t.Fatal("fixture is not a negative candidate")
	}
	changed := strings.Replace(body, "ordinary", "newmatch", 1) + request("appended")
	if len(changed) <= int(st.checkpoint.Size) {
		t.Fatal("fixture does not grow")
	}
	if err = os.WriteFile(st.Path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	// The old prefix and final-record anchors remain exactly intact. They do
	// not authorize a negative bitmap decision about the middle of a file.
	file, _, err := openReplaySource(st)
	if err != nil {
		t.Fatal("fixture accidentally invalidated a small anchor", err)
	}
	_ = file.Close()
	emitted := 0
	err = f.walkQuery(context.Background(), st, &q, func(snapshot.TrajectoryEvent, int) error { emitted++; return nil })
	if !errors.Is(err, ErrStale) || emitted != 0 {
		t.Fatal("unverified negative hid rewritten evidence", emitted, err)
	}
	if err = f.completeSource(context.Background(), st, st.checkpoint.EventCount, canonicalFixtureFacts(t, s, st)[2].offset, 0); !errors.Is(err, ErrStale) {
		t.Fatal("rewrite advanced the verification seal", err)
	}
}

func TestTrajectorySourceStoreOrdinaryAppendDoesNotAdvanceSeal(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("first")+request("second"))
	f := newSourceStoreFixture(t)
	facts := canonicalFixtureFacts(t, s, st)
	importFixtureFacts(t, f, st, facts)
	before, err := f.readiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, st.Path, request("uncommitted research"))
	q := snapshot.TrajectorySelector{Text: "uncommitted research"}
	if got := sourceStoreFacts(t, f, st, &q); len(got) != 0 {
		t.Fatal("source append leaked uncommitted facts")
	}
	after, err := f.readiness(context.Background())
	if err != nil || after[st.ID].verifiedSize != before[st.ID].verifiedSize || after[st.ID].verifiedMtime != before[st.ID].verifiedMtime {
		t.Fatal("ordinary append/query advanced proof", err)
	}
}

func TestTrajectorySourcePointOperationLastStageGuard(t *testing.T) {
	for _, action := range []string{"cancel", "replace"} {
		t.Run(action, func(t *testing.T) {
			s, st := replayFixture(t, "codex", CodexDecoder{}, request("canonical"))
			f := newSourceStoreFixture(t)
			importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
			file, before, err := openReplaySource(st)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			ranges, err := f.sourceRanges(context.Background(), st, -1, 1)
			if err != nil || len(ranges) != 1 {
				t.Fatal(err)
			}
			facts, err := f.readRange(context.Background(), st, ranges[0])
			if err != nil || len(facts) != 1 {
				t.Fatal("last-stage fixture did not complete merging", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := error(context.Canceled)
			if action == "cancel" {
				cancel()
			} else {
				replacement := st.Path + ".replacement"
				if err = os.WriteFile(replacement, []byte(request("canonical")), 0600); err != nil {
					t.Fatal(err)
				}
				if err = os.Rename(replacement, st.Path); err != nil {
					t.Fatal(err)
				}
				want = ErrStale
			}
			// The same owning finalizer used by event()/window()/walk executes
			// after the complete DTO exists, before a caller may publish it.
			if err = finishSourceOperation(ctx, st, file, before); !errors.Is(err, want) {
				t.Fatal("last-stage invalidation could publish a DTO", err)
			}
			event, err := f.event(ctx, st, facts[0].offset, facts[0].block)
			if err == nil || event.ID != "" {
				t.Fatal("invalid point operation returned a canonical DTO", event, err)
			}
		})
	}
}

func TestTrajectorySourceStoreReadinessBoundaryMustIdentifyExactPrefix(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("first")+request("second"))
	f := newSourceStoreFixture(t)
	facts := canonicalFixtureFacts(t, s, st)
	importFixtureFacts(t, f, st, facts)
	for _, p := range []struct {
		count  int
		offset int64
		block  int
	}{
		{2, facts[0].offset, facts[0].block}, {1, facts[1].offset, facts[1].block}, {1, facts[0].offset + 1, facts[0].block}, {0, 0, 0},
	} {
		if err := f.completeSource(context.Background(), st, p.count, p.offset, p.block); err == nil {
			t.Fatal("inconsistent readiness was published", p)
		}
	}
	ready, err := f.readiness(context.Background())
	if err != nil || ready[st.ID].count != 2 {
		t.Fatal("failed readiness transition changed the prior boundary", err)
	}
}
