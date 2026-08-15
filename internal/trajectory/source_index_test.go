package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTrajectorySourceOnlineAppendIsSameModelAsReplay(t *testing.T) {
	legacy, st := replayFixture(t, "codex", CodexDecoder{}, request("first"))
	f := newSourceStoreFixture(t)
	current, err := f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
	if err != nil || !reflect.DeepEqual(current.checkpoint, st.checkpoint) {
		t.Fatal("initial online checkpoint changed", err)
	}
	for i := 0; i < 20; i++ {
		appendFile(t, st.Path, request("research append"))
		st, err = legacy.indexSource(context.Background(), st.Source)
		if err != nil {
			t.Fatal(err)
		}
		current, err = f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(current.checkpoint, st.checkpoint) {
			t.Fatal("append checkpoint/model changed", i)
		}
		got := sourceStoreFacts(t, f, current, nil)
		want := canonicalFixtureFacts(t, legacy, st)
		if len(got) != len(want) {
			t.Fatal("append lost/duplicated facts", i)
		}
		for j, fact := range want {
			if !reflect.DeepEqual(got[j], fact.event) {
				t.Fatal("online and replay disagree on complete DTO", i, j)
			}
		}
	}
	var ranges, exceptions int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM ranges").Scan(&ranges); err != nil {
		t.Fatal(err)
	}
	if err = f.db.QueryRow("SELECT COUNT(*) FROM exceptions").Scan(&exceptions); err != nil {
		t.Fatal(err)
	}
	if ranges != 1 || exceptions != 0 {
		t.Fatal("one permanent projection per append", ranges, exceptions)
	}
	query, err := f.query(context.Background(), snapshot.TrajectorySelector{Collection: "events", Text: "research", Count: true}, []*sourceState{current}, coverage("current source"))
	if err != nil || query.MatchedTotal == nil || *query.MatchedTotal != 20 {
		t.Fatal("online prepared facts are not searchable", query, err)
	}
	var verifiedSize int64
	if err = f.db.QueryRow("SELECT verified_size FROM sources WHERE active=1").Scan(&verifiedSize); err != nil || verifiedSize != -1 {
		t.Fatal("online append fabricated whole-prefix proof", verifiedSize, err)
	}
}

func TestTrajectorySourceOnlineTailPartialInvalidAndLargeRecord(t *testing.T) {
	legacy, st := replayFixture(t, "codex", CodexDecoder{}, request("first"))
	f := newSourceStoreFixture(t)
	current, err := f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	appendFile(t, st.Path, "invalid json\n\xff\n"+request(strings.Repeat("large evidence ", 12000))+"{\"unfinished\":")
	st, err = legacy.indexSource(context.Background(), st.Source)
	if err != nil {
		t.Fatal(err)
	}
	current, err = f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
	if err != nil || !reflect.DeepEqual(current.checkpoint, st.checkpoint) {
		t.Fatal("partial/invalid records changed semantics", err)
	}
	if current.checkpoint.Offset >= current.Info.Size() {
		t.Fatal("partial line advanced checkpoint")
	}
	want := canonicalFixtureFacts(t, legacy, st)
	got := sourceStoreFacts(t, f, current, nil)
	if len(got) != len(want) {
		t.Fatal("range boundary lost a valid fact")
	}
	for i, fact := range want {
		if !reflect.DeepEqual(got[i], fact.event) {
			t.Fatal("large/invalid boundary changed DTO")
		}
	}
	appendFile(t, st.Path, "true}\n"+request("last"))
	current, err = f.indexSource(context.Background(), st.Source, 1, maxScanBytes, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if current.checkpoint.Offset >= current.Info.Size() {
		t.Fatal("one-record quantum ignored its limit")
	}
	current, err = f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	st, err = legacy.indexSource(context.Background(), st.Source)
	if err != nil || !reflect.DeepEqual(current.checkpoint, st.checkpoint) {
		t.Fatal("resumed tail hash/shape changed", err)
	}
}

func TestTrajectorySourceOnlineReopenAndMigratedExceptionsSurviveAppend(t *testing.T) {
	legacy, st := replayFixture(t, "codex", CodexDecoder{}, request("native"))
	f := newSourceStoreFixture(t)
	facts := canonicalFixtureFacts(t, legacy, st)
	facts[0].event.Text = "canonical exception retained"
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
	appendFile(t, st.Path, request("new append"))
	current, err := f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	got := sourceStoreFacts(t, f, current, nil)
	if len(got) != 2 || !reflect.DeepEqual(got[0], facts[0].event) || got[1].Text != "new append" {
		t.Fatal("append replaced canonical migration exceptions", got)
	}
	var exceptions int
	if err = f.db.QueryRow("SELECT COUNT(*) FROM exceptions").Scan(&exceptions); err != nil || exceptions != 1 {
		t.Fatal("append erased exception storage", err)
	}
}

func TestTrajectorySourceOnlineReplacementRetainsPreviousGeneration(t *testing.T) {
	_, st := replayFixture(t, "codex", CodexDecoder{}, request("first"))
	f := newSourceStoreFixture(t)
	first, err := f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	replacement := st.Path + ".replacement"
	if err = os.WriteFile(replacement, []byte(request("second")), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacement, st.Path); err != nil {
		t.Fatal(err)
	}
	second, err := f.indexSource(context.Background(), st.Source, 0, maxScanBytes, time.Time{})
	if err != nil || second.Generation == first.Generation {
		t.Fatal("replacement reused a prior generation", err)
	}
	if _, err = f.event(context.Background(), first, 0, 0); err == nil {
		t.Fatal("retired generation still grants content access")
	}
	var sources, retired int
	if err = f.db.QueryRow("SELECT COUNT(*),SUM(active=0) FROM sources").Scan(&sources, &retired); err != nil || sources != 2 || retired != 1 {
		t.Fatal("replacement deleted previous recovery metadata", sources, retired, err)
	}
}
