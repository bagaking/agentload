package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	bolt "go.etcd.io/bbolt"
)

func directInputIdentity(r *factMigrationReader) string {
	return fmt.Sprintf("dual:%d:%s:%s:%d", r.state.SourceTx, r.state.SourceIdentity, r.state.SearchIdentity, r.state.SearchRevision)
}

func (s *Service) migrateDirectLegacy(ctx context.Context) error {
	if s.sourceMigration == nil {
		if err := s.capacityCheck(s.path, 256*1024*1024); err != nil {
			return err
		}
		r, err := openFactMigrationReader(s.path)
		if err != nil {
			return err
		}
		ids, err := directLegacyIDs(r)
		if err != nil {
			r.close()
			return err
		}
		shadow, err := openMigrationSourceStore(ctx, s.path+".source-migrating")
		if err != nil {
			r.close()
			return err
		}
		s.sourceMigration = &sourceMigrationRun{legacy: r, ids: ids, shadow: shadow}
		state, has, err := sourceMigrationState(ctx, shadow)
		if err != nil {
			return err
		}
		if !has {
			original := r.state
			state = sourceMigration{Version: sourceMigrationVersion, Input: directInputIdentity(r), Phase: "legacy-progress", Legacy: &original, BytesBefore: r.state.BytesBefore, FactOffset: -1, FactBlock: -1}
			if err = saveSourceMigration(ctx, shadow, state); err != nil {
				return err
			}
		}
		if has {
			if err = restoreRetiredSearchProgress(ctx, s.path, r, shadow, state); err != nil {
				return err
			}
		}
		if state.Version != sourceMigrationVersion || state.Legacy == nil || state.Input != directInputIdentity(r) {
			return errors.New("dual migration input changed; originals and shadow preserved")
		}
	}
	run := s.sourceMigration
	if run.legacy == nil {
		return errors.New("migration input kind changed; originals preserved")
	}
	state, _, err := sourceMigrationState(ctx, run.shadow)
	if err != nil {
		return err
	}
	if err = checkDirectInputIdentities(s.path, state, false); err != nil {
		return err
	}
	if state.Phase == "ready" {
		if _, err = os.Stat(s.path + ".source-ready.json"); os.IsNotExist(err) {
			state.Phase = "legacy-progress"
			state.Control, state.ControlSet = "", false
			state.Source, state.Records = 0, 0
			state.Anchor, state.Mode = replayAnchor{}, ""
			state.FactOffset, state.FactBlock = -1, -1
			state.Legacy.After, state.Legacy.Hash, state.Legacy.Expected = nil, nil, nil
			state.Legacy.Records, state.Legacy.Events, state.Legacy.Metadata, state.Legacy.SearchAfter, state.Legacy.SearchRows = 0, 0, 0, 0, 0
			if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			return s.installDirectLegacy(ctx, state)
		}
	}
	switch state.Phase {
	case "legacy-progress":
		keys := make([]string, 0, len(run.legacy.progress))
		for id := range run.legacy.progress {
			if !state.ControlSet || id > state.Control {
				keys = append(keys, id)
			}
		}
		sort.Strings(keys)
		keys = keys[:min(64, len(keys))]
		additional := uint64(256 * 1024)
		values := make([][]byte, len(keys))
		for i, id := range keys {
			values[i], err = marshalLegacyProgress(run.legacy.progress[id])
			if err != nil {
				return err
			}
			additional += uint64(2 * (len(values[i]) + 3*len(legacyProgressPath(id)) + len(id) + 512))
			if additional > maxSourceRangeLogicalBytes {
				return errors.New("legacy recovery progress exceeds bounded budget; originals preserved")
			}
		}
		if len(keys) == 0 {
			state.Phase = "legacy-copy"
		} else {
			err = run.shadow.write(ctx, additional, func(tx *sql.Tx) error {
				for i, id := range keys {
					if err := preserveMigrationMetadata(ctx, tx, legacyProgressPath(id), "0", values[i], 0); err != nil {
						return err
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			state.Control, state.ControlSet = keys[len(keys)-1], true
			state.Legacy.Metadata += int64(len(keys))
		}
	case "legacy-copy", "legacy-verify":
		if err = migrateDirectEntries(ctx, s, run, &state); err != nil {
			return err
		}
	case "sources":
		quantum := 25 * time.Millisecond
		if s.storageOffline {
			quantum = time.Second
		}
		return s.migrateSourceFacts(ctx, run, state, quantum)
	case "legacy-search":
		done, err := verifyDirectSearch(ctx, s, run, &state)
		if err != nil {
			return err
		}
		if done {
			if err = s.validateMigrationSources(ctx, run.shadow); err != nil {
				if e := repairMigrationSource(ctx, run, &state, err); e != nil {
					return e
				}
				return errStorageMigration
			}
			if err = verifyLegacyProgressDB(ctx, run.shadow.db, run.legacy.progress); err != nil {
				return err
			}
			if err = validateDirectLegacyCounts(ctx, run, state); err != nil {
				return err
			}
			state.Phase = "ready"
			if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
				return err
			}
			if err = writeSourceCutoverSeal(ctx, s.path, run, state); err != nil {
				return err
			}
			return errStorageMigration
		}
	default:
		return errors.New("unrecognized dual migration phase; originals preserved")
	}
	if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
		return err
	}
	return errStorageMigration
}

func migrateDirectEntries(ctx context.Context, s *Service, run *sourceMigrationRun, state *sourceMigration) error {
	verifying := state.Phase == "legacy-verify"
	if verifying && state.Legacy.Records == 0 {
		if err := run.shadow.write(ctx, 256*1024, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS migration_pairs(source TEXT,call TEXT,body BLOB,PRIMARY KEY(source,call))"); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "DELETE FROM migration_pairs")
			return err
		}); err != nil {
			return err
		}
	}
	entries := []storageEntry{}
	ended := false
	size := 0
	err := run.legacy.canonical.View(func(tx *bolt.Tx) error {
		after := state.Legacy.After
		for len(entries) < 64 {
			if err := ctx.Err(); err != nil {
				return err
			}
			e, ok, err := nextStorageEntry(tx, after)
			if err != nil {
				return err
			}
			if !ok {
				ended = true
				break
			}
			n := directEntryBytes(e)
			if n > maxSourceRangeLogicalBytes {
				return errors.New("legacy value exceeds bounded migration budget; originals preserved")
			}
			if len(entries) > 0 && size+n > 8*1024*1024 {
				break
			}
			entries = append(entries, e)
			size += n
			after = e.path
		}
		return nil
	})
	if err != nil {
		return err
	}
	h := sha256.New()
	if len(state.Legacy.Hash) > 0 {
		if err = h.(encoding.BinaryUnmarshaler).UnmarshalBinary(state.Legacy.Hash); err != nil {
			return err
		}
	}
	verifier := directFactVerifier{s: s, run: run}
	pairs := migrationPairProof{f: run.shadow, changes: map[migrationPairKey]*indexedPair{}}
	for _, e := range entries {
		logical := e
		if eventStorageEntry(e) {
			event, err := migrationEvent(e.value)
			run.decodedFacts++
			if err != nil {
				return err
			}
			if len(e.path) != 4 || !bytes.Equal(e.path[3], eventKey(event.Source.Offset, event.Source.Block)) || string(e.path[1]) != event.Source.ID {
				return errors.New("legacy canonical key differs; originals preserved")
			}
			logical.value, err = json.Marshal(event)
			if err != nil {
				return err
			}
			if state.Phase == "legacy-copy" {
				state.Legacy.Events++
			} else {
				actual, err := verifier.event(ctx, event.Source.ID, event.Source.Offset, event.Source.Block)
				if err != nil {
					return err
				}
				logical.value, err = json.Marshal(actual)
				if err != nil {
					return err
				}
				want, _ := json.Marshal(event)
				if !bytes.Equal(want, logical.value) {
					return errors.New("complete dual migration facts differ; originals preserved")
				}
				if err = pairs.event(ctx, actual); err != nil {
					return err
				}
			}
		} else if state.Phase == "legacy-copy" {
			err = run.shadow.write(ctx, uint64(2*size+256*1024), func(tx *sql.Tx) error {
				return preserveMigrationMetadata(ctx, tx, metadataPath(e.path), fmt.Sprint(e.sequence), e.value, boolInt(e.value == nil))
			})
			if err != nil {
				return err
			}
			state.Legacy.Metadata++
		} else {
			var seq string
			var bucket int
			if err = run.shadow.db.QueryRowContext(ctx, "SELECT sequence,body,bucket FROM metadata WHERE path=?", metadataPath(e.path)).Scan(&seq, &logical.value, &bucket); err != nil {
				return err
			}
			if seq != fmt.Sprint(e.sequence) || bucket != boolInt(e.value == nil) || (logical.value == nil) != (e.value == nil) || !bytes.Equal(logical.value, e.value) {
				return errors.New("dual migration recovery metadata differs; originals preserved")
			}
			if migrationPairEntry(e) {
				if err = pairs.verify(ctx, e); err != nil {
					return err
				}
			}
		}
		if verifying && len(e.path) == 2 && bytes.Equal(e.path[0], sourceBucket) && e.value == nil {
			if err = pairs.previousSourcesEmpty(ctx, string(e.path[1])); err != nil {
				return err
			}
		}
		hashStorageEntry(h, logical)
		state.Legacy.After = e.path
		state.Legacy.Records++
	}
	state.Legacy.Hash, err = h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return err
	}
	if ended {
		if state.Phase == "legacy-copy" {
			state.Legacy.Expected, state.Legacy.ExpectedRecords = h.Sum(nil), state.Legacy.Records
			state.Phase = "sources"
		} else {
			if !bytes.Equal(state.Legacy.Expected, h.Sum(nil)) || state.Legacy.ExpectedRecords != state.Legacy.Records {
				return errors.New("full dual migration digest differs; originals preserved")
			}
			state.Phase = "legacy-search"
		}
	}
	if verifying {
		body, err := json.Marshal(*state)
		if err != nil {
			return err
		}
		return run.shadow.write(ctx, uint64(2*size+256*1024+2*len(body)), func(tx *sql.Tx) error {
			if err := pairs.commit(ctx, tx); err != nil {
				return err
			}
			if ended {
				var count int
				if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM migration_pairs").Scan(&count); err != nil {
					return err
				}
				if count != 0 {
					return errors.New("canonical tool calls lack matching pair ledger; originals preserved")
				}
				if _, err := tx.ExecContext(ctx, "DROP TABLE migration_pairs"); err != nil {
					return err
				}
			}
			_, err := tx.ExecContext(ctx, "INSERT INTO meta VALUES('source_migration',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(body))
			return err
		})
	}
	return nil
}

// Reserve the encoded metadata path, resumable cursor and key overhead as
// well as the value. The caller reserves data plus rollback journal bytes.
func directEntryBytes(e storageEntry) int {
	return max(len(e.value), storedLogicalSize(e.value)) + 3*len(metadataPath(e.path)) + len(fmt.Sprint(e.sequence)) + 512
}

func validateDirectLegacyCounts(ctx context.Context, run *sourceMigrationRun, state sourceMigration) error {
	if state.Legacy == nil || state.Records != state.Legacy.Events {
		return errors.New("dual migration event cardinality differs; originals preserved")
	}
	var sources, metadata int64
	if err := run.shadow.db.QueryRowContext(ctx, "SELECT count(*) FROM sources").Scan(&sources); err != nil {
		return err
	}
	if err := run.shadow.db.QueryRowContext(ctx, "SELECT count(*) FROM metadata").Scan(&metadata); err != nil {
		return err
	}
	actual, err := countSourceMigrationFacts(ctx, run.shadow)
	if err != nil {
		return err
	}
	if sources != int64(len(run.ids)) || metadata != state.Legacy.Metadata+sources || actual != state.Records {
		return errors.New("dual migration target cardinality differs; originals preserved")
	}
	return nil
}

// During verification only one range is resident. Missing generations read
// complete preserved exceptions privately, never through a public fallback.
type directFactVerifier struct {
	s          *Service
	run        *sourceMigrationRun
	id         string
	start, end int64
	facts      []sourceFact
}

func (v *directFactVerifier) event(ctx context.Context, id string, offset int64, block int) (snapshot.TrajectoryEvent, error) {
	row := sort.SearchStrings(v.run.ids, id) + 1
	if row > len(v.run.ids) || v.run.ids[row-1] != id {
		return snapshot.TrajectoryEvent{}, ErrStale
	}
	var missing bool
	if err := v.run.shadow.db.QueryRowContext(ctx, "SELECT missing FROM sources WHERE rowid=? AND id=?", row, id).Scan(&missing); err != nil {
		return snapshot.TrajectoryEvent{}, err
	}
	if missing {
		e, err := exceptionAt(ctx, v.run.shadow.db, int64(row), offset, block)
		if err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		if e.Event == nil || e.Offset != offset || e.Block != block {
			return snapshot.TrajectoryEvent{}, ErrStale
		}
		return *e.Event, nil
	}
	if v.id != id || offset < v.start || offset >= v.end {
		set := v.s.provider(ctx)
		if err := ctx.Err(); err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		var src Source
		for _, candidate := range set.Sources {
			if sourceID(candidate) == id && candidate.Decoder != nil {
				src = candidate
				break
			}
		}
		if src.Decoder == nil {
			return snapshot.TrajectoryEvent{}, ErrStale
		}
		input, err := v.run.inputSource(ctx, int64(row))
		if err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		raw, err := decodeStored(input.body)
		if err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		var cp sourceCheckpoint
		if err = json.Unmarshal(raw, &cp); err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		info, err := os.Stat(src.Path)
		if err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		st := &sourceState{Source: src, ID: id, Generation: cp.Generation, Info: info, checkpoint: cp}
		var start int64
		if err = v.run.shadow.db.QueryRowContext(ctx, "SELECT start FROM ranges WHERE source=? AND start<=? AND end>? ORDER BY start DESC LIMIT 1", row, offset, offset).Scan(&start); err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		ranges, err := v.run.shadow.sourceRanges(ctx, st, start-1, 1)
		if err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		if len(ranges) != 1 {
			return snapshot.TrajectoryEvent{}, ErrStale
		}
		v.facts, err = v.run.shadow.readRange(ctx, st, ranges[0])
		if err != nil {
			return snapshot.TrajectoryEvent{}, err
		}
		v.id, v.start, v.end = id, ranges[0].value.Chunk.Start.Offset, ranges[0].value.Chunk.End.Offset
	}
	for _, fact := range v.facts {
		if fact.offset == offset && fact.block == block {
			return fact.event, nil
		}
	}
	return snapshot.TrajectoryEvent{}, ErrNotFound
}

func verifyDirectSearch(ctx context.Context, s *Service, run *sourceMigrationRun, state *sourceMigration) (bool, error) {
	if run.legacy.search == nil {
		return true, nil
	}
	rows, err := run.legacy.search.QueryContext(ctx, "SELECT rowid,event_id,source_id,generation,offset,block,traj_text(search_text),traj_text(entities),tool_fold,entity_complete,fts_unsafe FROM docs_storage WHERE rowid>? ORDER BY rowid LIMIT 64", state.Legacy.SearchAfter)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	verifier := directFactVerifier{s: s, run: run}
	n := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		var rowid, offset int64
		var id, source, generation, text, entities, tool string
		var block, complete, unsafe int
		if err = rows.Scan(&rowid, &id, &source, &generation, &offset, &block, &text, &entities, &tool, &complete, &unsafe); err != nil {
			return false, err
		}
		n++
		state.Legacy.SearchAfter = rowid
		state.Legacy.SearchRows++
		p := run.legacy.progress[source]
		if !run.legacy.eligible[source] || p.generation != generation {
			continue
		}
		e, err := verifier.event(ctx, source, offset, block)
		if err != nil {
			return false, err
		}
		expectedText, expectedTool, expectedEntities, expectedComplete := searchProjection(e)
		expectedUnsafe := boolInt(stringsContainsNUL(expectedText))
		if !run.legacy.ready(e) || e.ID != id || e.Source.Generation != generation || expectedText != text || expectedTool != tool || expectedEntities != entities || expectedComplete != complete || expectedUnsafe != unsafe {
			return false, errors.New("legacy search projection differs from complete facts; originals preserved")
		}
	}
	return n < 64, rows.Err()
}

func stringsContainsNUL(s string) bool { return bytes.IndexByte([]byte(s), 0) >= 0 }
func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func checkDirectInputIdentities(path string, state sourceMigration, retiring bool) error {
	if state.Legacy == nil {
		return ErrStale
	}
	seal, sealErr := readSourceSeal(path)
	if retiring && sealErr != nil {
		return sealErr
	}
	for _, item := range []struct{ name, identity string }{{"index.bbolt", state.Legacy.SourceIdentity}, {"search.sqlite", state.Legacy.SearchIdentity}} {
		identity, _, err := migrationFileIdentity(filepath.Join(filepath.Dir(path), item.name))
		if err != nil {
			return err
		}
		if identity == "absent" && (retiring || item.name == "search.sqlite") && (item.identity == "absent" || seal.Retiring == item.name || slices.Contains(seal.Retired, item.name)) {
			continue
		}
		if identity != item.identity {
			return errors.New("dual migration original identity changed; originals preserved")
		}
	}
	return nil
}

func (s *Service) installDirectLegacy(ctx context.Context, state sourceMigration) error {
	if err := checkSourceCutoverSeal(ctx, s.path, s.path+".source-migrating", state); err != nil {
		return err
	}
	if s.sourceMigration != nil {
		if err := s.validateMigrationSources(ctx, s.sourceMigration.shadow); err != nil {
			if e := repairMigrationSource(ctx, s.sourceMigration, &state, err); e != nil {
				return e
			}
			if e := discardUnusedSeal(s.path, state); e != nil {
				return e
			}
			return errStorageMigration
		}
	}
	if err := checkDirectInputIdentities(s.path, state, false); err != nil {
		return err
	}
	if err := checkSourceCutoverSeal(ctx, s.path, s.path+".source-migrating", state); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.sourceMigration != nil {
		if err := s.sourceMigration.closeInput(); err != nil {
			return err
		}
		if err := closeSourceShadow(s.sourceMigration); err != nil {
			return err
		}
		s.sourceMigration = nil
	}
	if _, err := os.Stat(s.path); err == nil {
		return errors.New("dual migration destination already exists; originals preserved")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(s.path+".source-migrating", s.path); err != nil {
		return err
	}
	if err := syncStorageDirectory(s.path); err != nil {
		return err
	}
	return s.finishDirectLegacy(ctx, state)
}

func (s *Service) finishDirectLegacy(ctx context.Context, state sourceMigration) error {
	// Even if one original is already retired, the unchanged complete target
	// must still match the external seal before the remaining file is touched.
	if err := checkSourceCutoverSeal(ctx, s.path, s.path, state); err != nil {
		return err
	}
	if err := checkDirectInputIdentities(s.path, state, true); err != nil {
		return err
	}
	f, err := openMigrationSourceStore(ctx, s.path)
	if err != nil {
		return err
	}
	var integrity string
	err = f.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity)
	defer f.db.Close()
	if err != nil {
		return err
	}
	if integrity != "ok" {
		return errors.New("dual migration target integrity differs; originals preserved")
	}
	for _, name := range []string{"search.sqlite", "index.bbolt"} {
		if err := ctx.Err(); err != nil {
			return err
		}
		seal, err := readSourceSeal(s.path)
		if err != nil {
			return err
		}
		if slices.Contains(seal.Retired, name) {
			continue
		}
		if _, e := os.Stat(filepath.Join(filepath.Dir(s.path), "index.bbolt")); e == nil {
			if e = s.validateMigrationSources(ctx, f); e != nil {
				var invalidated *migrationSourceInvalidated
				if errors.As(e, &invalidated) {
					f.db.Close()
					return s.restoreSourceMigration(ctx, state)
				}
				return e
			}
		} else if !os.IsNotExist(e) {
			return e
		}
		seal.Retiring = name
		if err = saveSourceSeal(s.path, seal); err != nil {
			return err
		}
		if err = checkDirectInputIdentities(s.path, state, true); err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(filepath.Dir(s.path), name)); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := syncStorageDirectory(s.path); err != nil {
			return err
		}
		seal.Retiring = ""
		seal.Retired = append(seal.Retired, name)
		if err = saveSourceSeal(s.path, seal); err != nil {
			return err
		}
	}
	if err := os.Remove(s.path + ".source-ready.json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncStorageDirectory(s.path)
}
