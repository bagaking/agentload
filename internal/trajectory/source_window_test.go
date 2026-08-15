package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestTrajectorySourceWindowAndPairsMatchCanonicalOracle(t *testing.T) {
	body := codexRecord("session_meta", map[string]any{"id": "native", "cwd": "/recorded"}) +
		call("cross") + request(strings.Repeat("long evidence ", 13000)) + result("cross") +
		call("duplicate") + call("duplicate") + result("duplicate") + request("tail")
	s, st := replayFixture(t, "codex", CodexDecoder{}, body)
	facts := canonicalFixtureFacts(t, s, st)
	f := newSourceStoreFixture(t)
	chunks := importFixtureFacts(t, f, st, facts)
	if len(chunks) < 2 {
		t.Fatal("fixture does not cross ranges")
	}
	for _, call := range []string{"cross", "duplicate", "absent"} {
		old, err := legacyOracleFor(t, st).pair(context.Background(), st, call)
		if err != nil {
			t.Fatal(err)
		}
		got, err := f.pair(context.Background(), st, call)
		if err != nil || !reflect.DeepEqual(got, old) {
			t.Fatal("pair/ambiguity changed", call, got, old, err)
		}
	}
	for _, around := range []int{0, 1, 3, 5} {
		for _, fact := range facts {
			p := snapshot.TrajectoryGetParams{ID: fact.event.ID, Around: around, MaxBytes: MaxSliceBytes}
			old, ob, oa, err := legacyOracleFor(t, st).window(context.Background(), st, p, fact.offset, fact.block)
			if err != nil {
				t.Fatal(err)
			}
			got, gb, ga, err := f.window(context.Background(), st, p, fact.offset, fact.block)
			if err != nil || !reflect.DeepEqual(got, old) || gb != ob || ga != oa {
				t.Fatal("window/continuation/pair changed", around, fact.offset, err)
			}
		}
		p := snapshot.TrajectoryGetParams{ID: sessionID(st), Around: around, MaxBytes: MaxSliceBytes}
		old, ob, oa, err := legacyOracleFor(t, st).window(context.Background(), st, p, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		got, gb, ga, err := f.window(context.Background(), st, p, 0, 0)
		if err != nil || !reflect.DeepEqual(got, old) || gb != ob || ga != oa {
			t.Fatal("tail window changed", around, err)
		}
	}
}

func TestTrajectorySourceNeighborhoodKeepsPhysicalMultiBlockOrder(t *testing.T) {
	body := `{"type":"assistant","uuid":"one","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"text","text":"second"},{"type":"text","text":"third"}]}}` + "\n" +
		`{"type":"user","uuid":"two","message":{"role":"user","content":"last"}}` + "\n"
	s, st := replayFixture(t, "claude", ClaudeDecoder{}, body)
	facts := canonicalFixtureFacts(t, s, st)
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, facts)
	if len(facts) != 4 {
		t.Fatal(len(facts))
	}
	for i, fact := range facts {
		left, err := f.take(context.Background(), st, fact.offset, fact.block, true, false, 5)
		if err != nil || len(left) != i {
			t.Fatal("left same-line block order changed", i, left, err)
		}
		right, err := f.take(context.Background(), st, fact.offset, fact.block, false, false, 5)
		if err != nil || len(right) != len(facts)-i-1 {
			t.Fatal("right same-line block order changed", i, right, err)
		}
		for j, next := range right {
			if next.event.ID != facts[i+j+1].event.ID {
				t.Fatal("right physical ordering changed")
			}
		}
	}
}
