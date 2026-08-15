package trajectory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"time"
)

func restartUnsealedSourceMigration(ctx context.Context, shadow *sourceStore, state *sourceMigration) error {
	state.Phase = "control"
	state.Control, state.ControlSet = "", false
	state.Metadata, state.MetadataSet = nil, false
	state.Source, state.Records = 0, 0
	state.Anchor, state.Mode = replayAnchor{}, ""
	state.FactOffset, state.FactBlock = -1, -1
	return saveSourceMigration(ctx, shadow, *state)
}

func checkMigrationInput(ctx context.Context, path string, state sourceMigration, full bool) error {
	if state.Legacy != nil {
		return nil
	} // direct legacy readers own their two inputs
	identity, _, err := persistentPathIdentity(path)
	if err != nil {
		return err
	}
	if state.InputIdentity == "" || identity != state.InputIdentity {
		return errors.New("migration input identity changed; source and shadow preserved")
	}
	if full {
		hash, _, err := sourceFileSeal(ctx, path)
		if err != nil {
			return err
		}
		if hash != state.InputSHA256 {
			return errors.New("migration input content changed; source and shadow preserved")
		}
	}
	return nil
}

func (s *Service) bindMigrationInput(ctx context.Context, run *sourceMigrationRun, state *sourceMigration) error {
	if state.InputIdentity != "" {
		if s.expectedInputSHA256 != "" && s.expectedInputSHA256 != state.InputSHA256 {
			return errors.New("legacy migration input SHA-256 differs; original and shadow preserved")
		}
		return checkMigrationInput(ctx, s.path, *state, true)
	}
	current, _, err := migrationFileIdentity(s.path)
	if err != nil {
		return err
	}
	expected := s.expectedInputSHA256
	if current != state.Input && expected == "" {
		return errors.New("legacy migration input changed: offline --expected-input-sha256 is required; original and shadow preserved")
	}
	if expected != "" {
		b, err := hex.DecodeString(expected)
		if err != nil || len(b) != 32 {
			return errors.New("expected input SHA-256 is invalid")
		}
	}
	identity, _, err := persistentPathIdentity(s.path)
	if err != nil {
		return err
	}
	hash, _, err := sourceFileSeal(ctx, s.path)
	if err != nil {
		return err
	}
	if expected != "" && hash != expected {
		return errors.New("legacy migration input SHA-256 differs; original and shadow preserved")
	}
	state.InputIdentity, state.InputSHA256 = identity, hash
	if state.Phase == "ready" {
		// The old seal was checked before entering here. Persist an unfinished
		// owner before changing any CP, so restart does not reuse the old seal.
		state.Phase = "sources"
	}
	return saveSourceMigration(ctx, run.shadow, *state)
}

func checkpointLegacyAnchorsMatch(src Source, cp sourceCheckpoint, info os.FileInfo) bool {
	f, err := os.Open(src.Path)
	if err != nil {
		return false
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || !os.SameFile(info, before) || !legacyFileIdentity(cp.Identity, before) {
		return false
	}
	cp.Identity, err = persistentFileIdentity(f, before)
	return err == nil && checkpointSourceMatch(f, before, cp)
}

func (s *Service) upgradeMigrationIdentities(ctx context.Context, run *sourceMigrationRun, state *sourceMigration) error {
	set := s.provider(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	if !set.CatalogComplete && !set.Coverage.Complete {
		return errStorageMigration
	}
	allowed := map[string]Source{}
	for _, src := range set.Sources {
		if src.Decoder != nil {
			allowed[sourceID(src)] = src
		}
	}
	rows, err := run.shadow.db.QueryContext(ctx, "SELECT rowid,id,generation,checkpoint FROM sources WHERE rowid>? AND active=1 AND missing=0 ORDER BY rowid LIMIT 64", state.IdentityAfter)
	if err != nil {
		return err
	}
	type item struct {
		row            int64
		id, generation string
		body           []byte
	}
	var items []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.row, &i.id, &i.generation, &i.body); err != nil {
			break
		}
		items = append(items, i)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, i := range items {
		raw, err := decodeSourceValue(i.body, 2*maxRecordBytes)
		if err != nil {
			return err
		}
		var cp sourceCheckpoint
		if err = json.Unmarshal(raw, &cp); err != nil {
			return err
		}
		if cp.Generation != i.generation || cp.Version != projectionVersion {
			return ErrStale
		}
		// Persistent CPs have already completed this physical upgrade.
		if validPersistentIdentity(cp.Identity) {
			state.IdentityAfter = i.row
			continue
		}
		if _, valid := parseLegacyFileIdentity(cp.Identity); !valid {
			return ErrStale
		}
		src, ok := allowed[i.id]
		info, err := os.Stat(src.Path)
		if !ok || os.IsNotExist(err) || err == nil && !checkpointLegacyAnchorsMatch(src, cp, info) {
			if i.row >= state.Source {
				state.IdentityAfter = i.row
				continue
			}
			return repairMigrationSource(ctx, run, state, &migrationSourceInvalidated{i.row})
		}
		if err != nil {
			return err
		}
		_, err = run.shadow.upgradeFileIdentity(ctx, src, cp, info, false, 8*1024*1024, time.Now().Add(25*time.Millisecond))
		if err != nil {
			if errors.Is(err, errStorageMigration) {
				if e := saveSourceMigration(ctx, run.shadow, *state); e != nil {
					return e
				}
			}
			return err
		}
		state.IdentityAfter = i.row
	}
	if len(items) == 0 {
		state.IdentitiesComplete = true
	}
	if err = saveSourceMigration(ctx, run.shadow, *state); err != nil {
		return err
	}
	return errStorageMigration
}
