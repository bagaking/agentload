package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

type partialUTFDecoder struct{}

func (partialUTFDecoder) Decode(b []byte, c DecodeContext) ([]snapshot.TrajectoryEvent, error) {
	events, err := (CodexDecoder{}).Decode(b, c)
	for i := range events {
		events[i].Text = "bounded text " + string([]byte{0xff})
	}
	return events, err
}

func TestTrajectorySourceReplayCanonicalEncodedSize(t *testing.T) {
	s, st := replayFixture(t, "codex", partialUTFDecoder{}, request("original recorded line"))
	facts := canonicalFixtureFacts(t, s, st)
	f := newSourceStoreFixture(t)
	chunks := importFixtureFacts(t, f, st, facts)
	if len(chunks) != 1 || len(facts) != 1 {
		t.Fatal("invalid size fixture")
	}
	ranges, err := f.sourceRanges(context.Background(), st, -1, 1)
	if err != nil || len(ranges) != 1 {
		t.Fatal(err)
	}
	got, err := f.readRange(context.Background(), st, ranges[0])
	if err != nil || len(got) != 1 {
		t.Fatal("canonical DTO size became false source mutation", err)
	}
	raw, err := json.Marshal(got[0].event)
	if err != nil || got[0].size != len(raw) || ranges[0].value.LogicalBytes != len(raw) || !reflect.DeepEqual(got[0].event, facts[0].event) {
		t.Fatal("canonical size or event changed", err, got[0].size, len(raw))
	}
}

func TestTrajectorySourceIndexCanonicalEncodedSize(t *testing.T) {
	s, st := replayFixture(t, "codex", partialUTFDecoder{}, request("original recorded line"))
	for i := 0; i < 2; i++ {
		if i > 0 {
			appendFile(t, st.Path, request("online append"))
			var err error
			st, err = s.indexSource(context.Background(), st.Source)
			if err != nil {
				t.Fatal(err)
			}
		}
		facts := canonicalFixtureFacts(t, s, st)
		ranges, err := s.store.sourceRanges(context.Background(), st, -1, 1)
		if err != nil || len(ranges) != 1 {
			t.Fatal("online range", err)
		}
		got, err := s.store.readRange(context.Background(), st, ranges[0])
		if err != nil || len(got) != len(facts) {
			t.Fatal("online canonical DTO size became false source mutation", err)
		}
		bytes := 0
		for j := range got {
			raw, err := json.Marshal(got[j].event)
			if err != nil || got[j].size != len(raw) || !reflect.DeepEqual(got[j].event, facts[j].event) {
				t.Fatal("online canonical event changed", err)
			}
			bytes += len(raw)
		}
		if ranges[0].value.LogicalBytes != bytes {
			t.Fatal("online and replay logical bytes disagree", ranges[0].value.LogicalBytes, bytes)
		}
	}
}
