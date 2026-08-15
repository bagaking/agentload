package trajectory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// A published target whose raw dependencies changed is returned to the same
// small shadow, with its canonical original restored as the migration input.
// Interrupted renames still leave the sealed target and original recoverable.
func (s *Service) restoreSourceMigration(ctx context.Context, state sourceMigration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(s.path + ".source-migrating"); err == nil {
		return errors.New("existing migration shadow must be reconciled; originals preserved")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(s.path, s.path+".source-migrating"); err != nil {
		return err
	}
	if err := syncStorageDirectory(s.path); err != nil {
		return err
	}
	if state.Legacy == nil {
		if err := os.Rename(s.path+".source-legacy", s.path); err != nil {
			return err
		}
		if err := syncStorageDirectory(s.path); err != nil {
			return err
		}
	}
	return errStorageMigration
}

func legacyProgressDigest(progress map[string]searchProgress) string {
	keys := make([]string, 0, len(progress))
	for key := range progress {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, key := range keys {
		body, _ := marshalLegacyProgress(progress[key])
		hashStorageEntry(h, storageEntry{path: [][]byte{[]byte(key)}, value: body})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// The search cache may already be retired while canonical recovery is still
// possible. Its previously verified progress is checksummed by the durable
// external seal, not inferred from stored paths or an untrusted stale prefix.
func restoreRetiredSearchProgress(ctx context.Context, path string, r *factMigrationReader, f *sourceStore, state sourceMigration) error {
	if r.search != nil || state.Legacy == nil || state.Legacy.SearchIdentity == "absent" {
		return nil
	}
	seal, err := readSourceSeal(path)
	if err != nil {
		return err
	}
	if seal.Input != state.Input || seal.ProgressHash == "" || (seal.Retiring != "search.sqlite" && !slices.Contains(seal.Retired, "search.sqlite")) {
		return errors.New("search disappeared without verified retirement; canonical preserved")
	}
	rows, err := f.db.QueryContext(ctx, "SELECT path,body FROM metadata")
	if err != nil {
		return err
	}
	progress := map[string]searchProgress{}
	for rows.Next() {
		var key, body []byte
		if err = rows.Scan(&key, &body); err != nil {
			break
		}
		var parts [][]byte
		if json.Unmarshal(key, &parts) != nil || len(parts) != 2 || !bytes.Equal(parts[0], []byte("legacy-search-progress")) {
			continue
		}
		var p legacySearchProgress
		if err = json.Unmarshal(body, &p); err != nil {
			break
		}
		progress[string(parts[1])] = searchProgress{generation: p.Generation, count: p.Count, offset: p.Offset, block: p.Block, mtime: p.Mtime, agent: p.Agent}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if legacyProgressDigest(progress) != seal.ProgressHash {
		return errors.New("retired search progress checksum differs; canonical preserved")
	}
	r.progress = progress
	r.state.SearchIdentity, r.state.SearchRevision = state.Legacy.SearchIdentity, state.Legacy.SearchRevision
	r.eligible = map[string]bool{}
	return r.canonical.View(func(tx *bolt.Tx) error {
		for id, p := range r.progress {
			c, err := r.checkpoint(tx, []byte(id))
			if err != nil {
				continue
			}
			r.eligible[id] = !c.Missing && c.Generation == p.generation && p.count >= 0 && p.count <= c.EventCount
		}
		return nil
	})
}

func discardUnusedSeal(path string, state sourceMigration) error {
	if state.Legacy != nil {
		if seal, err := readSourceSeal(path); err == nil && (seal.Retiring != "" || len(seal.Retired) > 0) {
			return nil
		}
	}
	if err := os.Remove(path + ".source-ready.json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncStorageDirectory(path)
}
