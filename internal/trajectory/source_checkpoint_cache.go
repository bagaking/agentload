package trajectory

import (
	"container/list"
	"crypto/sha256"
	"slices"

	fastjson "github.com/goccy/go-json"
)

// Cache small recovery metadata, never historical events. SQL rows and their
// complete encoded bytes remain the authority on every read. The byte budget
// includes a conservative allowance for decoded fields and container overhead.
const maxCheckpointCacheBytes = 128 * 1024 * 1024

type cachedCheckpoint struct {
	id                 string
	hash               [sha256.Size]byte
	value              sourceCheckpoint
	decodedBytes, cost int
}

func cloneCheckpoint(c sourceCheckpoint) sourceCheckpoint {
	c.Prefix = slices.Clone(c.Prefix)
	c.Tools = slices.Clone(c.Tools)
	c.Coverage.Gaps = slices.Clone(c.Coverage.Gaps)
	if c.LastEvent != nil {
		value := *c.LastEvent
		c.LastEvent = &value
	}
	if c.Coverage.Index != nil {
		value := *c.Coverage.Index
		c.Coverage.Index = &value
	}
	return c
}

func (f *sourceStore) decodeCheckpoint(id string, body []byte, max int) (sourceCheckpoint, error) {
	hash := sha256.Sum256(body)
	f.checkpointMu.Lock()
	if element := f.checkpointCache[id]; element != nil {
		entry := element.Value.(cachedCheckpoint)
		if entry.hash == hash && entry.decodedBytes <= max && len(body) <= sha256.Size+8+max {
			f.checkpointLRU.MoveToFront(element)
			c := cloneCheckpoint(entry.value)
			f.checkpointMu.Unlock()
			return c, nil
		}
	}
	f.checkpointMu.Unlock()
	raw, err := decodeSourceValue(body, max)
	if err != nil {
		return sourceCheckpoint{}, err
	}
	var c sourceCheckpoint
	if err = fastjson.Unmarshal(raw, &c); err != nil {
		return c, err
	}
	entry := cachedCheckpoint{id: id, hash: hash, value: c, decodedBytes: len(raw), cost: 2*len(raw) + 1024}
	f.checkpointMu.Lock()
	defer f.checkpointMu.Unlock()
	if element := f.checkpointCache[id]; element != nil {
		f.checkpointBytes -= element.Value.(cachedCheckpoint).cost
		f.checkpointLRU.Remove(element)
		delete(f.checkpointCache, id)
	}
	if entry.cost <= maxCheckpointCacheBytes {
		for f.checkpointBytes+entry.cost > maxCheckpointCacheBytes {
			element := f.checkpointLRU.Back()
			old := element.Value.(cachedCheckpoint)
			f.checkpointBytes -= old.cost
			delete(f.checkpointCache, old.id)
			f.checkpointLRU.Remove(element)
		}
		if f.checkpointCache == nil {
			f.checkpointCache = make(map[string]*list.Element)
		}
		f.checkpointCache[id] = f.checkpointLRU.PushFront(entry)
		f.checkpointBytes += entry.cost
	}
	return cloneCheckpoint(c), nil
}
