package trajectory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// Only independently observed raw-source or authorization changes enter repair.
// A malformed checkpoint or other damaged proof remains a hard error.
type migrationSourceInvalidated struct{ row int64 }

func (e *migrationSourceInvalidated) Error() string {
	return fmt.Sprintf("migration source %d changed: %v", e.row, ErrStale)
}
func (e *migrationSourceInvalidated) Unwrap() error { return ErrStale }

// Recheck every replay-dependent source at the publication boundary. Metadata
// paths grant no authority, and a completed source may change while the rest
// of a long migration is being copied. Stored exceptions need no raw source.
func (s *Service) validateMigrationSources(ctx context.Context, f *sourceStore) error {
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
	// Repair authoritative catalog absences before repeating physical checks
	// on earlier, still-present sources. This changes only repair order; once
	// no such source remains, every replay dependency is checked below.
	rows, err := f.db.QueryContext(ctx, "SELECT rowid,id FROM sources WHERE active=1 AND missing=0 ORDER BY rowid")
	if err != nil {
		return err
	}
	for rows.Next() {
		var row int64
		var id string
		if err = rows.Scan(&row, &id); err != nil {
			break
		}
		if _, ok := allowed[id]; !ok {
			rows.Close()
			if err := ctx.Err(); err != nil {
				return err
			}
			// Absence cannot turn a damaged checkpoint into repair authority.
			// Check only the selected row here; its unavailable raw cannot be
			// opened, and all remaining replay dependencies are checked below.
			var generation string
			var body []byte
			if err := f.db.QueryRowContext(ctx, "SELECT generation,checkpoint FROM sources WHERE rowid=?", row).Scan(&generation, &body); err != nil {
				return err
			}
			raw, err := decodeSourceValue(body, maxSourceRangeLogicalBytes)
			if err != nil {
				return err
			}
			var cp sourceCheckpoint
			if err := json.Unmarshal(raw, &cp); err != nil {
				return err
			}
			if cp.Generation != generation || cp.Version != projectionVersion {
				return ErrStale
			}
			return &migrationSourceInvalidated{row}
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	after := int64(0)
	for {
		rows, err := f.db.QueryContext(ctx, "SELECT rowid,id,generation,checkpoint,verified_size,verified_mtime FROM sources WHERE rowid>? AND active=1 AND missing=0 ORDER BY rowid LIMIT 64", after)
		if err != nil {
			return err
		}
		type item struct {
			row, size, mtime int64
			id, generation   string
			body             []byte
		}
		items := []item{}
		for rows.Next() {
			var i item
			if err = rows.Scan(&i.row, &i.id, &i.generation, &i.body, &i.size, &i.mtime); err != nil {
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
		if len(items) == 0 {
			return ctx.Err()
		}
		for _, i := range items {
			if err := ctx.Err(); err != nil {
				return err
			}
			raw, err := decodeSourceValue(i.body, maxSourceRangeLogicalBytes)
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
			src, ok := allowed[i.id]
			if !ok {
				return &migrationSourceInvalidated{i.row}
			}
			info, err := os.Stat(src.Path)
			if os.IsNotExist(err) {
				return &migrationSourceInvalidated{i.row}
			}
			if err != nil {
				return err
			}
			if info.Size() != i.size || info.ModTime().UnixNano() != i.mtime {
				return &migrationSourceInvalidated{i.row}
			}
			st := &sourceState{Source: src, ID: i.id, Generation: i.generation, Info: info, checkpoint: cp}
			file, before, err := openReplaySource(st)
			if err != nil {
				return err
			}
			if before.Size() != i.size || before.ModTime().UnixNano() != i.mtime {
				file.Close()
				return &migrationSourceInvalidated{i.row}
			}
			err = finishSourceOperation(ctx, st, file, before)
			file.Close()
			if err != nil {
				if errors.Is(err, ErrStale) {
					current, e := os.Stat(src.Path)
					if os.IsNotExist(e) || e == nil && (!os.SameFile(before, current) || before.Size() != current.Size() || !before.ModTime().Equal(current.ModTime())) {
						return &migrationSourceInvalidated{i.row}
					}
				}
				return err
			}
			after = i.row
		}
	}
}

func repairMigrationSource(ctx context.Context, run *sourceMigrationRun, state *sourceMigration, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var invalidated *migrationSourceInvalidated
	if !errors.As(cause, &invalidated) {
		return cause
	}
	if state.RepairReturn != 0 || invalidated.row <= 0 || invalidated.row >= state.Source {
		return errors.New("invalid source repair cursor; originals preserved")
	}
	count, err := run.inputFactCount(ctx, invalidated.row, -1)
	if err != nil {
		return err
	}
	if count < 0 || count > state.Records {
		return errors.New("invalid source repair cardinality; originals preserved")
	}
	if state.RepairPhase == "" {
		state.RepairPhase = state.Phase
	}
	state.RepairReturn, state.Source = state.Source, invalidated.row
	state.Records -= count
	state.Phase = "sources"
	state.Anchor, state.Mode = replayAnchor{}, "stored"
	state.FactOffset, state.FactBlock = -1, -1
	if err = saveSourceMigration(ctx, run.shadow, *state); err != nil {
		return err
	}
	return nil
}
