package trajectory

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func TestTrajectoryCheckpointCachePreservesCurrentRowsAndOwnedValues(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("research")+call("read"))
	f := newSourceStoreFixture(t)
	importFixtureFacts(t, f, st, canonicalFixtureFacts(t, s, st))
	ctx := context.Background()
	before, ok, err := f.checkpoint(ctx, st.ID)
	if err != nil || !ok {
		t.Fatal(before, ok, err)
	}
	// Exercise all mutable fields, including ones absent in ordinary fixtures.
	now := time.Now().UTC()
	before.LastEvent = &now
	before.Coverage.Gaps = []string{"recorded-gap"}
	before.Tools = []string{"recorded-tool"}
	raw, _ := json.Marshal(before)
	body, err := encodeSourceValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Exec("UPDATE sources SET checkpoint=? WHERE id=?", body, st.ID); err != nil {
		t.Fatal(err)
	}
	c, _, err := f.checkpoint(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	c.Tools[0], c.Coverage.Gaps[0] = "caller-tool", "caller-gap"
	c.Prefix[0] ^= 1
	*c.LastEvent = c.LastEvent.Add(time.Hour)
	fresh, _, err := f.checkpoint(ctx, st.ID)
	if err != nil {
		t.Fatal(err)
	}
	actual, _ := json.Marshal(fresh)
	if string(actual) != string(raw) {
		t.Fatal("caller mutation corrupted cached evidence")
	}
	if _, err = f.db.Exec("UPDATE sources SET missing=1 WHERE id=?", st.ID); err != nil {
		t.Fatal(err)
	}
	missing, _, err := f.checkpoint(ctx, st.ID)
	if err != nil || !missing.Missing {
		t.Fatal("cache hid current removal", missing, err)
	}
	if _, err = f.db.Exec("UPDATE sources SET missing=0,generation='changed' WHERE id=?", st.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.checkpoint(ctx, st.ID); !errors.Is(err, ErrStale) {
		t.Fatal("cache granted access across a generation mismatch")
	}
	if _, err = f.db.Exec("UPDATE sources SET generation=? WHERE id=?", before.Generation, st.ID); err != nil {
		t.Fatal(err)
	}
	// Keep the claimed checksum intact while damaging the actual encoded body.
	body[len(body)-1] ^= 1
	if _, err = f.db.Exec("UPDATE sources SET checkpoint=? WHERE id=?", body, st.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.checkpoint(ctx, st.ID); err == nil {
		t.Fatal("cache trusted the claimed checksum of damaged bytes")
	}
}

func TestTrajectoryCheckpointCacheHonorsReadBudget(t *testing.T) {
	f := newSourceStoreFixture(t)
	before := sourceCheckpoint{Generation: "recorded", Tools: []string{"recorded-tool"}}
	raw, _ := json.Marshal(before)
	body, err := encodeSourceValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.decodeCheckpoint("source", body, len(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.decodeCheckpoint("source", body, len(raw)-1); err == nil {
		t.Fatal("larger read budget leaked through cache")
	}
}
