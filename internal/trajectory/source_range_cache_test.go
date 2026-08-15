package trajectory

import (
	"encoding/json"
	"testing"
)

func TestTrajectoryRangeCachePreservesEncodedAuthority(t *testing.T) {
	value := sourceRange{Chunk: replayChunk{HashState: []byte{1, 2, 3}}, First: &sourcePosition{Offset: 10}, Last: &sourcePosition{Offset: 20}, Facts: 2}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	body, err := encodeSourceValue(raw)
	if err != nil {
		t.Fatal(err)
	}
	store := &sourceStore{}
	first, err := store.decodeRange(body)
	if err != nil {
		t.Fatal(err)
	}
	first.First.Offset = 999
	first.Last.Offset = 999
	first.Chunk.HashState[0] = 99
	second, err := store.decodeRange(body)
	if err != nil || second.First.Offset != 10 || second.Last.Offset != 20 || second.Chunk.HashState[0] != 1 {
		t.Fatalf("caller changed cached metadata: %+v %v", second, err)
	}
	damaged := append([]byte{}, body...)
	damaged[len(damaged)-1] ^= 1
	if _, err := store.decodeRange(damaged); err == nil {
		t.Fatal("cache hid encoded corruption")
	}
	value.Facts = 3
	raw, _ = json.Marshal(value)
	changed, _ := encodeSourceValue(raw)
	third, err := store.decodeRange(changed)
	if err != nil || third.Facts != 3 {
		t.Fatal("cache hid valid metadata change", third, err)
	}
}
