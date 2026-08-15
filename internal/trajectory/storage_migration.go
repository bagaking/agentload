package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"os"
	"path/filepath"

	bolt "go.etcd.io/bbolt"
)

var storageBucket = []byte("_trajectory-storage")
var storageCodecKey = []byte("codec")
var storageMigrationKey = []byte("migration")
var errStorageMigration = errors.New("trajectory storage optimization pending")

type storageEntry struct {
	path     [][]byte
	value    []byte
	sequence uint64
}

// nextStorageEntry is a seekable preorder walk. The persisted physical keys
// resume without traversing already copied history. Empty nested buckets and
// their sequences are records too; unknown metadata is carried unchanged.
func nextStorageEntry(tx *bolt.Tx, after [][]byte) (storageEntry, bool, error) {
	path := make([][]byte, len(after))
	for i := range after {
		path[i] = bytes.Clone(after[i])
	}
	if len(path) == 0 {
		c := tx.Cursor()
		k, _ := c.First()
		if k == nil {
			return storageEntry{}, false, nil
		}
		return storageEntry{path: [][]byte{bytes.Clone(k)}, sequence: tx.Bucket(k).Sequence()}, true, nil
	}
	var bucket *bolt.Bucket
	for i, k := range path {
		if i == 0 {
			bucket = tx.Bucket(k)
		} else if bucket != nil {
			bucket = bucket.Bucket(k)
		}
	}
	if bucket != nil {
		if k, v := bucket.Cursor().First(); k != nil {
			seq := uint64(0)
			if v == nil {
				seq = bucket.Bucket(k).Sequence()
			}
			return storageEntry{path: append(path, bytes.Clone(k)), value: bytes.Clone(v), sequence: seq}, true, nil
		}
	}
	for len(path) > 0 {
		var c *bolt.Cursor
		var parent *bolt.Bucket
		if len(path) == 1 {
			c = tx.Cursor()
		} else {
			parent = tx.Bucket(path[0])
			for _, k := range path[1 : len(path)-1] {
				if parent == nil {
					return storageEntry{}, false, errors.New("migration parent missing")
				}
				parent = parent.Bucket(k)
			}
			if parent == nil {
				return storageEntry{}, false, errors.New("migration parent missing")
			}
			c = parent.Cursor()
		}
		k, v := c.Seek(path[len(path)-1])
		if bytes.Equal(k, path[len(path)-1]) {
			k, v = c.Next()
		}
		if k != nil {
			path[len(path)-1] = bytes.Clone(k)
			seq := uint64(0)
			if v == nil {
				if parent == nil {
					seq = tx.Bucket(k).Sequence()
				} else {
					seq = parent.Bucket(k).Sequence()
				}
			}
			return storageEntry{path: path, value: bytes.Clone(v), sequence: seq}, true, nil
		}
		path = path[:len(path)-1]
	}
	return storageEntry{}, false, nil
}

func eventStorageEntry(e storageEntry) bool {
	return len(e.path) >= 3 && bytes.Equal(e.path[0], sourceBucket) && bytes.Equal(e.path[len(e.path)-2], eventBucket) && e.value != nil
}

func hashStorageEntry(h hash.Hash, e storageEntry) {
	_ = binary.Write(h, binary.BigEndian, uint32(len(e.path)))
	for _, k := range e.path {
		_ = binary.Write(h, binary.BigEndian, uint32(len(k)))
		_, _ = h.Write(k)
	}
	_ = binary.Write(h, binary.BigEndian, e.sequence)
	if e.value == nil {
		_, _ = h.Write([]byte{0})
	} else {
		_, _ = h.Write([]byte{1})
		_ = binary.Write(h, binary.BigEndian, uint64(len(e.value)))
		_, _ = h.Write(e.value)
	}
}

// Prior full-DTO values are decoded only by the bounded format migration.
// Normal reads consume the current physical format directly.
func migrationEvent(value []byte) (snapshot.TrajectoryEvent, error) {
	if compactEventEncoding(value) {
		return decodeEventStored(value)
	}
	var event snapshot.TrajectoryEvent
	raw, err := decodeStored(value)
	if err != nil {
		return event, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&event); err != nil {
		return event, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return event, errors.New("invalid trailing trajectory event data")
	}
	return event, nil
}

func logicalEventJSON(value []byte) ([]byte, error) {
	event, err := migrationEvent(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(event)
}

func syncStorageDirectory(path string) error {
	f, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
