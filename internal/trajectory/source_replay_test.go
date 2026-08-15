package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func replayFixture(t *testing.T, agent string, decoder Decoder, body string) (*Service, *sourceState) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.jsonl")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	src := Source{Agent: agent, Path: path, NativeID: "fixture", Decoder: decoder}
	s := New(func(context.Context) SourceSet {
		return SourceSet{Sources: []Source{src}, Coverage: coverage("replay fixture"), CatalogComplete: true}
	})
	t.Cleanup(func() { _ = s.Close() })
	if err := s.openIndex(); err != nil {
		t.Fatal(err)
	}
	st, err := s.indexSource(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	return s, st
}

func replayAll(t *testing.T, st *sourceState) ([]replayChunk, []snapshot.TrajectoryEvent, snapshot.TrajectoryCoverage) {
	t.Helper()
	var chunks []replayChunk
	events := []snapshot.TrajectoryEvent{}
	cov := coverage("source:" + st.ID)
	start := replayAnchor{}
	for start.Offset < st.checkpoint.Offset {
		c, err := nextReplayChunk(context.Background(), st, start)
		if err != nil {
			t.Fatal(err)
		}
		part, err := replaySourceChunk(context.Background(), st, c, func(e snapshot.TrajectoryEvent, logical int) error {
			b, _ := json.Marshal(e)
			if logical != len(b) {
				t.Fatal("logical budget changed")
			}
			events = append(events, e)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		mergeCoverage(&cov, part)
		chunks = append(chunks, c)
		start = c.End
	}
	if start.Line != st.checkpoint.Line || start.WorkingDirectory != st.checkpoint.WorkingDirectory {
		t.Fatal("source state did not reproduce the committed checkpoint")
	}
	return chunks, events, cov
}

func TestTrajectoryReplayFourVendorsCompleteFacts(t *testing.T) {
	long := strings.Repeat("evidence α ", 14000)
	quote := func(s string) string { b, _ := json.Marshal(s); return string(b) }
	codexBody := codexRecord("session_meta", map[string]any{"id": "native", "cwd": "/recorded/one"}) +
		codexRecord("turn_context", map[string]any{"turn_id": "turn", "cwd": "/recorded/one"}) +
		codexRecord("response_item", map[string]any{"type": "function_call", "name": "read_file", "call_id": "cross", "arguments": `{"path":"src/main.go"}`}) +
		request(long) + codexRecord("compacted", map[string]any{"message": "prior work"}) +
		codexRecord("turn_context", map[string]any{"turn_id": "turn-2", "cwd": "/recorded/two"}) +
		result("cross") + call("duplicate") + call("duplicate") + result("duplicate") +
		codexRecord("response_item", map[string]any{"type": "function_call", "name": "read_file", "call_id": "after", "arguments": `{"path":"src/main.go"}`}) +
		codexRecord("event_msg", map[string]any{"type": "token_count", "info": map[string]any{"total_token_usage": map[string]int{"input_tokens": 20, "output_tokens": 0}}})
	claudeBody := `{"type":"user","uuid":"u1","cwd":"/recorded/one","message":{"role":"user","content":"request"}}` + "\n" +
		`{"type":"assistant","uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"tool_use","id":"cross","name":"Read","input":{"file_path":"src/main.go"}},{"type":"text","text":` + quote(long) + `}]}}` + "\n" +
		`{"type":"user","uuid":"u2","cwd":"/recorded/two","isCompactSummary":true,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"cross","content":""}]}}` + "\n" +
		`{"type":"assistant","uuid":"a2","message":{"role":"assistant","content":[{"type":"tool_use","id":"after","name":"Read","input":{"file_path":"src/main.go"}}]}}` + "\n"
	traeBody := codexRecord("session_meta", map[string]any{"id": "native", "cwd": "/recorded/one"}) +
		codexRecord("history_mutation", map[string]any{"version": 1, "operation": "append", "commit_id": "commit", "items": []any{
			map[string]any{"type": "message", "id": "native-1", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": long}, map[string]any{"type": "input_text", "text": "second"}}},
			map[string]any{"type": "function_call", "name": "read_file", "call_id": "cross", "arguments": `{"path":"src/main.go"}`}}}) +
		codexRecord("event_msg", map[string]any{"type": "context_compacted", "message": "summary"}) + result("cross")
	grokBody := `{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":` + quote(long) + `}}}}` + "\n" +
		`{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"tool_call","toolCallId":"cross","rawInput":{"path":"src/main.go"},"_meta":{"x.ai/tool":{"name":"Read"}}}}}` + "\n" +
		`{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"tool_call_update","toolCallId":"cross","status":"completed","rawOutput":""}}}` + "\n" +
		`{"timestamp":1788194984,"params":{"update":{"sessionUpdate":"turn_completed","prompt_id":"p1","usage":{"inputTokens":20,"outputTokens":0}}}}` + "\n"
	for _, tc := range []struct {
		agent   string
		decoder Decoder
		body    string
	}{{"codex", CodexDecoder{}, codexBody}, {"claude", ClaudeDecoder{}, claudeBody}, {"trae", TraeDecoder{}, traeBody}, {"grok", GrokDecoder{}, grokBody}} {
		t.Run(tc.agent, func(t *testing.T) {
			s, st := replayFixture(t, tc.agent, tc.decoder, tc.body)
			var expected []snapshot.TrajectoryEvent
			if err := legacyOracleFor(t, st).walk(context.Background(), st, func(e snapshot.TrajectoryEvent, _ int) error { expected = append(expected, e); return nil }); err != nil {
				t.Fatal(err)
			}
			chunks, got, cov := replayAll(t, st)
			if len(chunks) < 2 || !reflect.DeepEqual(expected, got) {
				for i := range expected {
					if i >= len(got) {
						break
					}
					left, right := reflect.ValueOf(expected[i]), reflect.ValueOf(got[i])
					for field := 0; field < left.NumField(); field++ {
						if !reflect.DeepEqual(left.Field(field).Interface(), right.Field(field).Interface()) {
							t.Logf("event %d differs at %s", i, left.Type().Field(field).Name)
						}
					}
				}
				t.Fatalf("complete DTOs/identity changed: chunks=%d expected=%d got=%d", len(chunks), len(expected), len(got))
			}
			if !reflect.DeepEqual(cov, st.checkpoint.Coverage) {
				t.Fatalf("coverage changed: replay=%+v canonical=%+v", cov, st.checkpoint.Coverage)
			}
			if tc.agent == "codex" {
				if chunks[1].Start.WorkingDirectory != "/recorded/one" {
					t.Fatal("anchor lost preceding cwd")
				}
				var first, second bool
				for _, e := range got {
					for _, o := range e.Entities {
						first = first || o.Literal == "/recorded/one/src/main.go"
						second = second || o.Literal == "/recorded/two/src/main.go"
					}
				}
				if !first || !second {
					t.Fatal("entity paths were not restored with each recorded cwd")
				}
			}
			// Feed actual replayed facts into the existing consumer contract. This
			// is an isolated small fixture, not a production database copy.
			copy := New(s.provider)
			t.Cleanup(func() { _ = copy.Close() })
			if err := copy.openIndex(); err != nil {
				t.Fatal(err)
			}
			facts := []sourceFact{}
			for _, e := range got {
				raw, _ := json.Marshal(e)
				facts = append(facts, sourceFact{e.Source.Offset, e.Source.Block, e, len(raw)})
			}
			importFixtureFacts(t, copy.store, st, facts)

			selector := snapshot.TrajectorySelector{Collection: "events", Kind: "tool_call", Limit: 20, Count: true}
			originalQuery := searchTestPreparedQuery(t, s, selector)
			replayedQuery := searchTestPreparedQuery(t, copy, selector)
			if !reflect.DeepEqual(originalQuery.Events, replayedQuery.Events) || !reflect.DeepEqual(originalQuery.MatchedTotal, replayedQuery.MatchedTotal) {
				t.Fatal("replayed query facts changed")
			}
			contextParams := snapshot.TrajectoryGetParams{ID: sourceArchive(st).ID, View: "context", MaxBytes: MaxSliceBytes}
			ca, err := s.GetContext(context.Background(), contextParams)
			if err != nil {
				t.Fatal(err)
			}
			cb, err := copy.GetContext(context.Background(), contextParams)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ca, cb) {
				t.Fatal("replayed context changed")
			}
			relationParams := snapshot.TrajectoryGetParams{ID: sessionID(st), View: "relations", Around: 3, MaxBytes: MaxSliceBytes}
			ra, err := s.GetRelations(context.Background(), relationParams)
			if err != nil {
				t.Fatal(err)
			}
			rb, err := copy.GetRelations(context.Background(), relationParams)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ra, rb) {
				t.Fatal("replayed relations changed")
			}
			for _, p := range []snapshot.TrajectoryGetParams{
				{ID: originalQuery.Events[0].ID, Around: 1, Raw: true, MaxBytes: MaxSliceBytes},
			} {
				a, err := s.Get(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				b, err := copy.Get(context.Background(), p)
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(a, b) {
					t.Fatalf("replayed public view changed: %s", p.View)
				}
			}
		})
	}
}

func TestTrajectoryReplayCommittedPrefixAndInvalidation(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("first")+"invalid json\n"+"\xff\n"+request("second")+"{\"unfinished\":")
	_, expected, cov := replayAll(t, st)
	if cov.Complete || len(expected) != 2 || st.checkpoint.Offset == st.Info.Size() {
		t.Fatal("partial/invalid physical records invented facts")
	}
	chunk, err := nextReplayChunk(context.Background(), st, replayAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, st.Path, `true}`+"\n"+request("appended"))
	got := []snapshot.TrajectoryEvent{}
	_, err = replaySourceChunk(context.Background(), st, chunk, func(e snapshot.TrajectoryEvent, _ int) error { got = append(got, e); return nil })
	if err != nil || !reflect.DeepEqual(expected, got) {
		t.Fatal("append changed the committed prefix", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = replaySourceChunk(ctx, st, chunk, func(snapshot.TrajectoryEvent, int) error { t.Fatal("cancelled read emitted facts"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	f, err := os.OpenFile(st.Path, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteAt([]byte("SECOND"), int64(strings.Index(request("first")+"invalid json\n"+"\xff\n"+request("second"), "second")))
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = replaySourceChunk(context.Background(), st, chunk, func(snapshot.TrajectoryEvent, int) error { t.Fatal("modified prefix emitted facts"); return nil }); !errors.Is(err, ErrStale) {
		t.Fatal("rewritten prefix not stale", err)
	}
	_ = s
}

func TestTrajectoryReplayOversizedRecordKeepsOmission(t *testing.T) {
	_, st := replayFixture(t, "codex", CodexDecoder{}, request("before")+strings.Repeat("x", maxRecordBytes+100)+"\n"+request("after"))
	_, events, cov := replayAll(t, st)
	if len(events) != 2 || !reflect.DeepEqual(cov, st.checkpoint.Coverage) {
		t.Fatal("omitted oversized line changed", len(events), cov)
	}
}

func TestTrajectoryReplayExceptionsPreserveEveryCanonicalDifference(t *testing.T) {
	_, st := replayFixture(t, "codex", CodexDecoder{}, request("evidence"))
	_, events, _ := replayAll(t, st)
	replayed := events[0]
	value, err := encodeReplayException(replayed, replayed)
	if err != nil || value != nil {
		t.Fatal("identical replay needs no persistent DTO", err)
	}
	for _, change := range []func(*snapshot.TrajectoryEvent){
		func(e *snapshot.TrajectoryEvent) { e.ID = ""; e.SessionID = "foreign-session" },
		func(e *snapshot.TrajectoryEvent) {
			e.Context = &snapshot.TrajectoryContextEvidence{NativeID: "legacy-context"}
		},
		func(e *snapshot.TrajectoryEvent) {
			e.Text = "old decoder text"
			e.Omissions = []string{"old-format-gap"}
		},
		func(e *snapshot.TrajectoryEvent) {
			e.Entities = []snapshot.TrajectoryEntityOccurrence{{ID: "", EntityID: "foreign-id", Literal: "literal", Label: "", Source: snapshot.TrajectorySourceRef{ID: "foreign-source"}}}
		},
	} {
		original := replayed
		change(&original)
		value, err = encodeReplayException(original, replayed)
		if err != nil || len(value) == 0 {
			t.Fatal("canonical difference discarded", err)
		}
		got, err := decodeReplayException(value)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(original)
		b, _ := json.Marshal(got)
		if string(a) != string(b) {
			t.Fatal("exception lost facts or explicit empty values")
		}
	}
}

func TestTrajectoryReplaySourceReplacementAndCancellation(t *testing.T) {
	_, st := replayFixture(t, "codex", CodexDecoder{}, request("old source"))
	chunk, err := nextReplayChunk(context.Background(), st, replayAnchor{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	_, err = replaySourceChunk(ctx, st, chunk, func(snapshot.TrajectoryEvent, int) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal("last-record cancellation could publish", err)
	}
	if err = os.Rename(st.Path, st.Path+".old"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(st.Path, []byte(request("new source")), 0600); err != nil {
		t.Fatal(err)
	}
	noVisit := func(snapshot.TrajectoryEvent, int) error { t.Fatal("replaced source emitted facts"); return nil }
	if _, err = replaySourceChunk(context.Background(), st, chunk, noVisit); !errors.Is(err, ErrStale) {
		t.Fatal("replacement crossed generation", err)
	}
	if err = os.Remove(st.Path); err != nil {
		t.Fatal(err)
	}
	if _, err = replaySourceChunk(context.Background(), st, chunk, noVisit); !errors.Is(err, ErrStale) {
		t.Fatal("missing source invented facts", err)
	}
}

func TestTrajectoryReplayInFlightChangesCannotCommit(t *testing.T) {
	for _, mode := range []string{"replace", "rewrite", "append"} {
		t.Run(mode, func(t *testing.T) {
			_, st := replayFixture(t, "codex", CodexDecoder{}, request("old source"))
			chunk, err := nextReplayChunk(context.Background(), st, replayAnchor{})
			if err != nil {
				t.Fatal(err)
			}
			visited := false
			_, err = replaySourceChunk(context.Background(), st, chunk, func(snapshot.TrajectoryEvent, int) error {
				visited = true
				switch mode {
				case "replace":
					if err := os.Rename(st.Path, st.Path+".old"); err != nil {
						return err
					}
					return os.WriteFile(st.Path, []byte(request("new source")), 0600)
				case "rewrite":
					f, err := os.OpenFile(st.Path, os.O_WRONLY, 0600)
					if err != nil {
						return err
					}
					_, err = f.WriteAt([]byte("NEW"), int64(strings.Index(request("old source"), "old")))
					_ = f.Close()
					return err
				default:
					appendFile(t, st.Path, request("append during read"))
					return nil
				}
			})
			committed := err == nil // owner commits staged visitors only on success
			if !visited || !errors.Is(err, ErrStale) || committed {
				t.Fatal("in-flight changed source could publish", err)
			}
		})
	}
}
