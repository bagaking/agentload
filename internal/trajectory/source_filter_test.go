package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestTrajectorySourceFilterPreservesExactCandidateSemantics(t *testing.T) {
	events := []snapshot.TrajectoryEvent{
		{ID: "one", Kind: "text", Role: "user", Actor: snapshot.TrajectoryActor{Kind: "human", ID: "person"}, Text: "Research bagakit-researcher 中文上下文 αβΓ hello\x00world a*[?]"},
		{ID: "two", Kind: "tool_call", Role: "assistant", Actor: snapshot.TrajectoryActor{Kind: "agent", ID: "actor"}, Tool: &snapshot.TrajectoryTool{Name: "Kernel", CallID: "call-id", Arguments: json.RawMessage(`{"path":"../研究/SKILL.md","note":"世界","symbol":"<>&"}`)}, Entities: []snapshot.TrajectoryEntityOccurrence{
			{Kind: "skill", EntityID: "ent.one", Label: "ſkill", Literal: "/recorded/skills/ſkill/SKILL.md", Predicate: "requested_read"},
			{Kind: "path", EntityID: "ent.two", Label: "file", Literal: "/recorded/file", Predicate: "mention"},
		}},
	}
	filter := newSourceFilter()
	for _, e := range events {
		if err := filter.add(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	encoded, err := filter.encode()
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeSourceFilter(encoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []snapshot.TrajectorySelector{
		{Text: "research"}, {Text: "bagakit-researcher"}, {Text: "世界"}, {Text: "中文上下文"}, {Text: "αβγ"}, {Text: "hello\x00world"}, {Text: "a*[?]"}, {Text: "a"}, {Text: "中"}, {Text: "ea"}, {Text: "Research bagakit"},
		{Kind: "tool_call", Role: "assistant", Tool: "Kernel", ActorKind: "agent", ActorID: "actor"},
		{Skill: "Skill", EntityKind: "skill", EntityID: "ent.one", Predicate: "requested_read"},
		{Skill: "/recorded/skills/ſkill/SKILL.md", EntityKind: "skill"},
	} {
		truth := false
		for _, e := range events {
			truth = truth || matches(e, q)
		}
		if !truth {
			t.Fatalf("test query has no exact witness: %+v", q)
		}
		if !decoded.maybe(q) {
			t.Fatalf("candidate index lost exact match: %+v", q)
		}
	}
	if !decoded.maybeCall("call-id") {
		t.Fatal("cross-range call candidate lost")
	}
	// Coarse features intentionally over-admit an entity conjunction spread
	// across occurrences. Only the existing exact predicate may admit a hit.
	q := snapshot.TrajectorySelector{EntityKind: "path", Predicate: "requested_read"}
	if !decoded.maybe(q) || MatchEntitySelector(events[1], q) {
		t.Fatal("same-occurrence truth became coarse candidate truth")
	}
}

func TestTrajectorySourceFilterRejectsCorruptionBeforeAllocation(t *testing.T) {
	value, err := newSourceFilter().encode()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []func([]byte) []byte{
		func(b []byte) []byte { b[len(b)-1] ^= 1; return b },
		func(b []byte) []byte { return b[:len(b)-1] },
		func(b []byte) []byte {
			binary.BigEndian.PutUint32(b[sha256.Size+4:], 64<<20)
			sum := sha256.Sum256(b[sha256.Size:])
			copy(b, sum[:])
			return b
		},
	} {
		broken := mutation(append([]byte{}, value...))
		if _, err := decodeSourceFilter(broken); err == nil {
			t.Fatal("damaged candidate filter could silently exclude facts")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newSourceFilter().add(ctx, snapshot.TrajectoryEvent{Text: "no write"}); err != context.Canceled {
		t.Fatal("filter ignored cancellation", err)
	}
}

func TestTrajectorySourceFilterUsesBoundedSizesForSmallBatches(t *testing.T) {
	e := snapshot.TrajectoryEvent{Text: "bagakit-researcher 中文上下文", Kind: "text", Role: "user"}
	for _, rawBytes := range []int64{1, 8192, 8193, 16385, 32769, 65537, 131073, 262145, 524289, 8 << 20} {
		filter := sizedSourceFilter(rawBytes)
		if err := filter.add(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		encoded, err := filter.encode()
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeSourceFilter(encoded)
		if err != nil {
			t.Fatal(err)
		}
		if !decoded.maybe(snapshot.TrajectorySelector{Text: "bagakit-researcher", Kind: "text", Role: "user"}) {
			t.Fatal("resize lost true candidate", rawBytes)
		}
		if len(encoded) > sha256.Size+8+maxSourceFilterBytes {
			t.Fatal("filter exceeded bound")
		}
		if rawBytes == 1 && decoded.bits.Cap() != 64*8 {
			t.Fatal("one record allocated a full bitmap")
		}
	}
}
