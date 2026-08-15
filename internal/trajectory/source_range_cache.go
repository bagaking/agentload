package trajectory

import (
	"container/list"
	"crypto/sha256"
	"encoding/json"
	"slices"
)

// Cache sparse anchors only. Every caller still reads the actual SQL bytes
// and validates the current row, full sequence, authorization and raw source.
const maxRangeCacheBytes = 32 * 1024 * 1024

type cachedRange struct {
	hash  [sha256.Size]byte
	value sourceRange
	cost  int
}

func cloneRange(value sourceRange) sourceRange {
	value.Chunk.HashState = slices.Clone(value.Chunk.HashState)
	if value.First != nil {
		position := *value.First
		value.First = &position
	}
	if value.Last != nil {
		position := *value.Last
		value.Last = &position
	}
	return value
}

func (f *sourceStore) decodeRange(body []byte) (sourceRange, error) {
	// Hash all observed bytes, including the frame's claimed checksum.
	hash := sha256.Sum256(body)
	f.rangeMu.Lock()
	if element := f.rangeCache[hash]; element != nil {
		f.rangeLRU.MoveToFront(element)
		value := cloneRange(element.Value.(cachedRange).value)
		f.rangeMu.Unlock()
		return value, nil
	}
	f.rangeMu.Unlock()
	raw, err := decodeSourceValue(body, 3*maxRecordBytes)
	if err != nil {
		return sourceRange{}, err
	}
	var value sourceRange
	if err = json.Unmarshal(raw, &value); err != nil {
		return sourceRange{}, err
	}
	entry := cachedRange{hash: hash, value: value, cost: 2*len(raw) + 512}
	f.rangeMu.Lock()
	defer f.rangeMu.Unlock()
	if f.rangeCache[hash] == nil && entry.cost <= maxRangeCacheBytes {
		for f.rangeBytes+entry.cost > maxRangeCacheBytes {
			element := f.rangeLRU.Back()
			old := element.Value.(cachedRange)
			delete(f.rangeCache, old.hash)
			f.rangeLRU.Remove(element)
			f.rangeBytes -= old.cost
		}
		if f.rangeCache == nil {
			f.rangeCache = make(map[[sha256.Size]byte]*list.Element)
		}
		f.rangeCache[hash] = f.rangeLRU.PushFront(entry)
		f.rangeBytes += entry.cost
	}
	return cloneRange(value), nil
}
