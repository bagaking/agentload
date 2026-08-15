package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTrajectoryStorageCodecBoundsChecksumsAndLiteralBytes(t *testing.T) {
	for _, text := range []string{"", "\x00AL\x01literal", strings.Repeat("中文词 research\x00", 4000)} {
		stored, err := encodeTextStored(text)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := decodeStored(stored)
		if err != nil || string(raw) != text {
			t.Fatal("text changed", err)
		}
	}
	compressed, err := encodeStored([]byte(strings.Repeat("research 中文词", 4000)))
	if err != nil {
		t.Fatal(err)
	}
	if len(compressed) > 1000 {
		t.Fatal("repetitive body not compressed")
	}
	compressed[len(compressed)-1] ^= 1
	if _, err = decodeStored(compressed); err == nil {
		t.Fatal("damaged checksum accepted")
	}
	if _, err = decodeStored([]byte{0, 'A', 'L', 1, 127, 255, 255, 255}); err == nil {
		t.Fatal("oversized allocation accepted")
	}
}

func TestTrajectoryStorageEventFactoringPreservesExplicitUnknownsAndForeignReferences(t *testing.T) {
	source := snapshot.TrajectorySourceRef{ID: "file", Generation: "gen", Offset: 21, Block: 2, Digest: "digest", Line: 3, Length: 50}
	e := snapshot.TrajectoryEvent{Source: source, ID: sourceEventID(source), SessionID: "s.file.gen", Role: "user", Kind: "text", Text: "research"}
	o := snapshot.TrajectoryEntityOccurrence{EventID: e.ID, SessionID: e.SessionID, Kind: "skill", Literal: "research", Label: "research", Scope: "session:" + e.SessionID, Predicate: "mentioned", NativeField: "text", Source: source}
	o.EntityID = occurrenceEntityID(o.Kind, o.Scope, o.Literal)
	o.ID = occurrenceID(e.ID, o.EntityID, o.Predicate, o.NativeField)
	e.Entities = []snapshot.TrajectoryEntityOccurrence{o}
	for _, variation := range []string{"derived", "empty", "foreign"} {
		candidate := e
		candidate.Entities = append([]snapshot.TrajectoryEntityOccurrence(nil), e.Entities...)
		if variation == "empty" {
			candidate.ID, candidate.SessionID = "", ""
			candidate.Entities[0] = snapshot.TrajectoryEntityOccurrence{Kind: "skill", Literal: "research"}
		}
		if variation == "foreign" {
			candidate.ID, candidate.SessionID = "recorded-event", "recorded-session"
			item := &candidate.Entities[0]
			item.ID, item.EntityID, item.EventID, item.SessionID = "occ-other", "entity-other", "event-other", "session-other"
			item.Scope, item.Label = "global", "Human label"
			item.Source = snapshot.TrajectorySourceRef{ID: "different-file"}
		}
		want, _ := json.Marshal(candidate)
		stored, err := encodeEventStored(candidate)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := decodeIndexed(stored)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(actual)
		if !bytes.Equal(want, got) || storedLogicalSize(stored) != len(want) {
			t.Fatal("factoring changed evidence or expanded allocation budget", variation)
		}
		if variation == "derived" {
			physical, _ := decodeStored(stored)
			if len(physical)*2 >= len(want) {
				t.Fatal("equal parent references remain duplicated")
			}
		}
	}
	if _, err := migrationEvent([]byte(`{"unknown_evidence":"preserve"}`)); err == nil {
		t.Fatal("migration silently dropped unknown evidence")
	}
}
