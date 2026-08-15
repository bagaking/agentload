package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"

	"github.com/bits-and-blooms/bloom/v3"
)

// This filter is only a bounded candidate accelerator. A positive result does
// not prove an event match or a conjunction within one entity occurrence.
// Four-byte fragments operate on the exact Go-lowercased search projection;
// short terms safely fall through to source validation, including NUL/Unicode.
const maxSourceFilterBytes = 4096

type sourceFilter struct{ bits *bloom.BloomFilter }

func newSourceFilter() *sourceFilter {
	return &sourceFilter{bits: bloom.New(maxSourceFilterBytes*8, 1)}
}

// Small online batches do not consume a full 4 KiB bitmap each. The size is
// fixed before adding keys; no unsafe truncation of an existing filter occurs.
func sizedSourceFilter(rawBytes int64) *sourceFilter {
	n := 64
	for n < maxSourceFilterBytes && int64(n)*128 < rawBytes {
		n *= 2
	}
	return &sourceFilter{bits: bloom.New(uint(n*8), 1)}
}

func validSourceFilterSize(n int) bool {
	return n >= 64 && n <= maxSourceFilterBytes && n&(n-1) == 0
}

func filterKey(tag byte, value string) []byte {
	key := make([]byte, len(value)+1)
	key[0] = tag
	copy(key[1:], value)
	return key
}

func (f *sourceFilter) add(ctx context.Context, e snapshot.TrajectoryEvent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name, call := "", ""
	var arguments []byte
	if e.Tool != nil {
		name, call, arguments = e.Tool.Name, e.Tool.CallID, e.Tool.Arguments
	}
	text := factSearchText(e.Text, name, arguments, e.Tool != nil)
	var gram [5]byte // tag zero is reserved for text byte fragments
	for i := 0; i+4 <= len(text); i++ {
		if i%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		copy(gram[1:], text[i:i+4])
		f.bits.Add(gram[:])
	}
	for _, feature := range []struct {
		tag   byte
		value string
	}{{'k', e.Kind}, {'r', e.Role}, {'a', e.Actor.ID}, {'b', e.Actor.Kind}} {
		f.bits.Add(filterKey(feature.tag, feature.value))
	}
	if e.Tool != nil {
		f.bits.Add(filterKey('t', searchFold(name)))
		f.bits.Add(filterKey('c', call))
	}
	for _, o := range e.Entities {
		f.bits.Add(filterKey('K', o.Kind))
		f.bits.Add(filterKey('I', o.EntityID))
		f.bits.Add(filterKey('P', o.Predicate))
		f.bits.Add(filterKey('L', searchFold(o.Label)))
		f.bits.Add(filterKey('V', o.Literal))
	}
	return ctx.Err()
}

func (f *sourceFilter) maybe(q snapshot.TrajectorySelector) bool {
	for _, feature := range []struct {
		tag   byte
		value string
	}{{'k', q.Kind}, {'r', q.Role}, {'a', q.ActorID}, {'b', q.ActorKind}, {'t', searchFold(q.Tool)}, {'K', q.EntityKind}, {'I', q.EntityID}, {'P', q.Predicate}} {
		if feature.value != "" && !f.bits.Test(filterKey(feature.tag, feature.value)) {
			return false
		}
	}
	if q.Skill != "" && (!f.bits.Test(filterKey('K', "skill")) || (!f.bits.Test(filterKey('L', searchFold(q.Skill))) && !f.bits.Test(filterKey('V', q.Skill)))) {
		return false
	}
	var gram [5]byte
	for _, word := range strings.Fields(strings.ToLower(q.Text)) {
		for i := 0; i+4 <= len(word); i++ {
			copy(gram[1:], word[i:i+4])
			if !f.bits.Test(gram[:]) {
				return false
			}
		}
	}
	return true
}

func (f *sourceFilter) maybeCall(call string) bool {
	return f.bits.Test(filterKey('c', call))
}

// Store the mature library's bitset with a project-bounded, checksummed frame.
// Its generic decoder is intentionally not used on untrusted length headers.
func (f *sourceFilter) encode() ([]byte, error) {
	words := f.bits.BitSet().Bytes()
	n := int(f.bits.Cap() / 8)
	if !validSourceFilterSize(n) || f.bits.Cap()%8 != 0 || f.bits.K() != 1 || len(words) != n/8 {
		return nil, errors.New("invalid trajectory candidate filter")
	}
	raw := make([]byte, n)
	for i, word := range words {
		binary.BigEndian.PutUint64(raw[i*8:], word)
	}
	body, err := encodeTextStored(string(raw))
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(body)
	return append(hash[:], body...), nil
}

func decodeSourceFilter(value []byte) (*sourceFilter, error) {
	if len(value) < sha256.Size+8 || len(value) > sha256.Size+8+maxSourceFilterBytes {
		return nil, errors.New("invalid trajectory candidate filter size")
	}
	body := value[sha256.Size:]
	hash := sha256.Sum256(body)
	n := int(binary.BigEndian.Uint32(body[4:8]))
	if !bytes.Equal(hash[:], value[:sha256.Size]) || !validSourceFilterSize(n) {
		return nil, errors.New("invalid trajectory candidate filter checksum/length")
	}
	raw, err := decodeStored(body)
	if err != nil {
		return nil, err
	}
	if len(raw) != n {
		return nil, errors.New("invalid trajectory candidate filter decoded length")
	}
	words := make([]uint64, n/8)
	for i := range words {
		words[i] = binary.BigEndian.Uint64(raw[i*8:])
	}
	return &sourceFilter{bits: bloom.From(words, 1)}, nil
}
