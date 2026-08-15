package trajectory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"
)

type StorageProgress struct {
	Phase        string              `json:"phase"`
	Records      int64               `json:"records,omitempty"`
	Total        int64               `json:"total,omitempty"`
	BytesBefore  int64               `json:"bytes_before,omitempty"`
	BytesAfter   int64               `json:"bytes_after,omitempty"`
	Digest       string              `json:"logical_digest,omitempty"`
	Input        string              `json:"input_identity,omitempty"`
	StableInput  string              `json:"persistent_input_identity,omitempty"`
	InputHash    string              `json:"input_sha256,omitempty"`
	Sources      int64               `json:"source_count,omitempty"`
	TargetHash   string              `json:"target_sha256,omitempty"`
	BlockUpgrade *sourceBlockUpgrade `json:"block_upgrade,omitempty"`
	BlockPacking *sourceBlockPacking `json:"block_packing,omitempty"`
}

// The CLI holds the history owner lock. The migrator separately holds a
// read-only original lock and never manufactures a second whole shadow.
func OptimizeStorage(ctx context.Context, provider Provider, path string, report func(StorageProgress), expectedInputSHA256 string) error {
	s := NewPersistent(provider, path)
	s.storageOffline = true
	s.expectedInputSHA256 = expectedInputSHA256
	defer s.Close()
	last := time.Time{}
	migrated := false
	// An installed cutover can finish retirement in one open call, without a
	// pending migration handle. Capture its admission before consuming the seal.
	for _, suffix := range []string{".source-ready.json", ".source-legacy"} {
		if _, err := os.Stat(s.path + suffix); err == nil {
			migrated = true
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := s.openIndexContext(ctx)
		migrated = migrated || s.sourceUpgrade != nil || s.sourceMigration != nil || s.sourcePacking != nil || errors.Is(err, errStorageMigration)
		if s.sourcePacking != nil && report != nil && time.Since(last) > time.Second {
			state, _, e := readSourcePacking(ctx, s.sourcePacking, sourcePackingKey)
			if e != nil {
				return e
			}
			report(StorageProgress{Phase: state.Phase, BlockPacking: &state})
			last = time.Now()
		}
		if s.sourceUpgrade != nil && report != nil && time.Since(last) > time.Second {
			state, _, e := readSourceBlockUpgrade(ctx, s.sourceUpgrade)
			if e != nil {
				return e
			}
			info, e := os.Stat(s.sourceUpgrade.path)
			if e != nil {
				return e
			}
			report(StorageProgress{Phase: state.Phase, Records: state.Records, Total: state.Total, BytesBefore: state.BytesBefore, BytesAfter: info.Size(), Digest: state.Digest})
			last = time.Now()
		}

		if s.sourceMigration != nil && s.sourceMigration.shadow != nil {
			state, _, e := sourceMigrationState(ctx, s.sourceMigration.shadow)
			if e != nil {
				return e
			}
			var size int64
			if info, e := os.Stat(s.path + ".source-migrating"); e == nil {
				size = info.Size()
			}
			if report != nil && (state.Phase == "ready" || time.Since(last) > time.Second) {
				progress := StorageProgress{Phase: state.Phase, Records: state.Records, BytesBefore: state.BytesBefore, BytesAfter: size, Input: state.Input, StableInput: state.InputIdentity, InputHash: state.InputSHA256}
				if state.Phase == "ready" {
					// A verified seal is observable before retirement, even when
					// the last bounded batch completes within the progress interval.
					seal, e := readSourceSeal(s.path)
					if e != nil {
						return e
					}
					progress.Phase, progress.TargetHash = "sealed", seal.Hash
					if e = s.sourceMigration.shadow.db.QueryRowContext(ctx, "SELECT count(*) FROM sources").Scan(&progress.Sources); e != nil {
						return e
					}
				}
				report(progress)
				last = time.Now()
			}
		}
		if errors.Is(err, errStorageMigration) {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	// Fresh projection migrations append dense blocks into a new table. Keep
	// their verified cutover seal unchanged; only in-place upgrades leave gaps.
	var packing *sourceBlockPacking
	var inPlace bool
	if err := s.store.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM meta WHERE key IN ('source-block-upgrade-result','source-block-packing-result'))").Scan(&inPlace); err != nil {
		return err
	}
	if !migrated || inPlace {
		var err error
		packing, err = optimizeSourceBlockPacking(ctx, s.store, expectedInputSHA256, migrated, report)
		if err != nil {
			return err
		}
	}
	if report != nil {
		st, _, err := sourceMigrationState(ctx, s.store)
		if err != nil {
			return err
		}
		info, err := os.Stat(s.path)
		if err != nil {
			return err
		}
		hash, size, err := sourceFileSeal(ctx, s.path)
		if err != nil {
			return err
		}
		var sources int64
		if err = s.store.db.QueryRowContext(ctx, "SELECT count(*) FROM sources").Scan(&sources); err != nil {
			return err
		}
		if size != info.Size() {
			return ErrStale
		}
		progress := StorageProgress{Phase: "ready", Records: st.Records, BytesBefore: st.BytesBefore, BytesAfter: size, Input: st.Input, StableInput: st.InputIdentity, InputHash: st.InputSHA256, Sources: sources, TargetHash: hash, BlockPacking: packing}
		var body string
		err = s.store.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='source-block-upgrade-result'").Scan(&body)
		if err == nil {
			var upgrade sourceBlockUpgrade
			if err = json.Unmarshal([]byte(body), &upgrade); err != nil {
				return err
			}
			progress.BlockUpgrade = &upgrade
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		report(progress)
	}
	return nil
}
