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
)

type sourceScanBoundaryDecoder struct {
	after int
	reads int
	fire  func()
}

func (d *sourceScanBoundaryDecoder) Decode(raw []byte, ctx DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	if d.fire != nil {
		d.reads++
		if d.reads == d.after {
			d.fire()
		}
	}
	return (recordedRelationDecoder{}).Decode(raw, ctx)
}

func sourceScanBoundaryFixture(t *testing.T) (*Service, *sourceState, *sourceScanBoundaryDecoder, int) {
	t.Helper()
	var body strings.Builder
	body.WriteString(relationRecord(contextInput("input", true, contextInclude("0"))))
	for n := 0; n < 100; n++ {
		e := relationEvent(fmt.Sprint(n), "$alpha "+strings.Repeat("recorded source evidence ", 150), recordedParent("0"))
		body.WriteString(relationRecord(e))
	}
	d := &sourceScanBoundaryDecoder{}
	s, st := replayFixture(t, "recorded", d, body.String())
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "$alpha"})
	ranges, err := s.store.sourceRanges(context.Background(), st, -1, 16)
	if err != nil || len(ranges) < 2 || ranges[0].value.Chunk.Events < 2 {
		t.Fatal("fixture lacks earlier complete callbacks and a later range", err)
	}
	return s, st, d, ranges[0].value.Chunk.Events
}

func TestTrajectorySourceScanFinalGuardOverridesEarlyStop(t *testing.T) {
	for _, mode := range []string{"cancel", "replace", "stop"} {
		t.Run(mode, func(t *testing.T) {
			s, st, _, _ := sourceScanBoundaryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			visits := 0
			_, err := scan(ctx, st, func(snapshot.TrajectoryEvent) bool {
				visits++
				if mode == "cancel" {
					cancel()
				} else if mode == "replace" {
					body, e := os.ReadFile(st.Path)
					if e != nil {
						t.Fatal(e)
					}
					path := filepath.Join(filepath.Dir(st.Path), "replacement.jsonl")
					if e = os.WriteFile(path, body, 0600); e != nil {
						t.Fatal(e)
					}
					if e = os.Rename(path, st.Path); e != nil {
						t.Fatal(e)
					}
				}
				return false
			})
			want := ErrStale
			if mode == "cancel" {
				want = context.Canceled
			} else if mode == "stop" {
				want = nil
			}
			if visits != 1 || !errors.Is(err, want) {
				t.Fatal("final source guard lost to bounded early stop", visits, err)
			}
			_ = s
		})
	}
}

func TestTrajectorySourceProjectionDiscardsEarlierCallbacks(t *testing.T) {
	for _, api := range []string{"entities", "contexts", "context_manifest", "input_manifest", "context_members", "context_query", "actors", "relations", "relations_detail", "attention", "attention_detail"} {
		for _, mode := range []string{"cancel", "replace"} {
			t.Run(api+"/"+mode, func(t *testing.T) {
				s, st, d, firstRangeEvents := sourceScanBoundaryFixture(t)
				input, err := s.store.event(context.Background(), st, 0, 0)
				if err != nil {
					t.Fatal(err)
				}
				inputContextID := eventContext(st, input, "actual_input").ID
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				fired := false
				// The walker invokes all first-range consumers before decoding
				// the following range. Fail the later read after those products
				// were accumulated; this is not a pre-cancelled request.
				d.after = firstRangeEvents + 1
				switch api {
				case "input_manifest", "context_members":
					// The anchor's three neighborhood reads, then the
					// resolver's first range.
					d.after = 4*firstRangeEvents + 1
				case "context_query":
					// Anchor + complete member resolution + the query's first
					// range, which already contains the included target event.
					d.after = 4*firstRangeEvents + st.checkpoint.EventCount + 1
				case "relations_detail":
					// Three bounded neighborhood reads before relation resolution.
					d.after = 4*firstRangeEvents + 1
				}
				d.fire = func() {
					fired = true
					if mode == "cancel" {
						cancel()
						return
					}
					body, err := os.ReadFile(st.Path)
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(filepath.Dir(st.Path), "replacement.jsonl")
					if err = os.WriteFile(path, body, 0600); err != nil {
						t.Fatal(err)
					}
					if err = os.Rename(path, st.Path); err != nil {
						t.Fatal(err)
					}
				}
				discarded := false
				switch api {
				case "entities":
					var out snapshot.TrajectoryQueryResult
					out, err = s.QueryEntities(ctx, snapshot.TrajectorySelector{})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryQueryResult{})
				case "contexts":
					var out snapshot.TrajectoryContextQueryResult
					out, err = s.QueryContexts(ctx, snapshot.TrajectorySelector{ContextScope: "query_window"})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryContextQueryResult{})
				case "context_manifest":
					var out snapshot.TrajectoryContextGetResult
					out, err = s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: sourceArchive(st).ID})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryContextGetResult{})
				case "input_manifest":
					var out snapshot.TrajectoryContextGetResult
					out, err = s.GetContext(ctx, snapshot.TrajectoryGetParams{ID: inputContextID})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryContextGetResult{})
				case "context_members":
					var ids []string
					ids, _, err = s.ContextMembers(ctx, inputContextID)
					discarded = ids == nil
				case "context_query":
					var out snapshot.TrajectoryQueryResult
					out, err = s.Query(ctx, snapshot.TrajectorySelector{Collection: "events", ContextID: inputContextID})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryQueryResult{})
				case "actors":
					var out snapshot.TrajectoryActorQueryResult
					out, err = s.QueryActors(ctx, snapshot.TrajectorySelector{})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryActorQueryResult{})
				case "relations":
					var out snapshot.TrajectoryRelationQueryResult
					out, err = s.QueryRelations(ctx, snapshot.TrajectorySelector{})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryRelationQueryResult{})
				case "relations_detail":
					var out snapshot.TrajectoryRelationQueryResult
					out, err = s.GetRelations(ctx, snapshot.TrajectoryGetParams{ID: input.ID})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryRelationQueryResult{})
				case "attention":
					var out snapshot.TrajectoryAttentionQueryResult
					out, err = s.QueryAttention(ctx, snapshot.TrajectorySelector{})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryAttentionQueryResult{})
				case "attention_detail":
					var out snapshot.TrajectoryAttentionGetResult
					out, err = s.GetAttention(ctx, snapshot.TrajectoryGetParams{ID: sessionID(st)})
					discarded = reflect.DeepEqual(out, snapshot.TrajectoryAttentionGetResult{})
				}
				want := ErrStale
				if mode == "cancel" {
					want = context.Canceled
				}
				if !fired || !errors.Is(err, want) || !discarded {
					t.Fatal("partial callback products were published", fired, discarded, err)
				}
			})
		}
	}
}

func TestTrajectorySourceProjectionSemanticGapStillReturnsEvidence(t *testing.T) {
	s, _ := relationService(t, relationRecord(relationEvent("unknown", "$alpha")))
	searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "$alpha"})
	out, err := s.QueryActors(context.Background(), snapshot.TrajectorySelector{ActorKind: "unknown"})
	if err != nil || len(out.Actors) != 1 || out.Coverage.Complete {
		t.Fatal("ordinary semantic uncertainty became an integrity error", err, out)
	}
	encoded, _ := json.Marshal(out)
	if !bytes.Contains(encoded, []byte("actor_identity_unknown")) {
		t.Fatal("semantic gap lost")
	}
}
