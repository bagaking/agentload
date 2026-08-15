package trajectory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"
)

const sourcePackingKey = "source-block-packing"
const sourcePackingResultKey = "source-block-packing-result"

// Exception rowids are private. Moving them into one ascending namespace packs
// sparse leaf pages without a shadow table or changing a source/event locator.
type sourceBlockPacking struct {
	Phase       string `json:"phase"`
	MaxOldRow   int64  `json:"max_old_row"`
	Cursor      int64  `json:"cursor"`
	NextRow     int64  `json:"next_row"`
	Blocks      int64  `json:"blocks"`
	Packed      int64  `json:"packed_blocks"`
	Records     int64  `json:"records"`
	Revision    int64  `json:"revision"`
	BytesBefore int64  `json:"bytes_before"`
	Digest      string `json:"logical_digest"`
	Controls    string `json:"control_digest"`
	InputSHA256 string `json:"input_sha256"`
	RequestHash string `json:"request_input_sha256"`
}

func readSourcePacking(ctx context.Context, f *sourceStore, key string) (sourceBlockPacking, bool, error) {
	var state sourceBlockPacking
	var raw string
	err := f.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(raw), &state)
	}
	return state, true, err
}

func saveSourcePacking(ctx context.Context, tx *sql.Tx, key string, state sourceBlockPacking) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(raw))
	return err
}

func sourceStoreRevision(ctx context.Context, f *sourceStore) (int64, error) {
	var raw string
	if err := f.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='revision'").Scan(&raw); err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err == nil && (n < 0 || n == math.MaxInt64) {
		err = ErrStale
	}
	return n, err
}

func beginSourcePacking(ctx context.Context, f *sourceStore, expected string, migrated bool) (sourceBlockPacking, error) {
	state, has, err := readSourcePacking(ctx, f, sourcePackingKey)
	if err != nil || has {
		if err == nil && expected != "" && expected != state.RequestHash {
			err = errors.New("source packing input SHA differs; preserved")
		}
		return state, err
	}
	revision, err := sourceStoreRevision(ctx, f)
	if err != nil {
		return state, err
	}
	ready, has, err := readSourcePacking(ctx, f, sourcePackingResultKey)
	if err != nil {
		return state, err
	}
	if has && ready.Phase == "ready" && ready.Revision == revision {
		if expected != "" && expected != ready.RequestHash {
			hash, _, e := sourceFileSeal(ctx, f.path)
			if e != nil {
				return state, e
			}
			if hash != expected {
				return state, errors.New("source packing input SHA differs; preserved")
			}
		}
		return ready, nil
	}
	state.Phase = "pack_blocks"
	state.Revision = revision + 1
	state.InputSHA256, state.BytesBefore, err = sourceFileSeal(ctx, f.path)
	if err != nil {
		return state, err
	}
	if expected != "" && expected != state.InputSHA256 {
		if !migrated {
			return state, errors.New("source packing input SHA differs; preserved")
		}
		original, _, e := sourceMigrationState(ctx, f)
		if e != nil {
			return state, e
		}
		var upgrade sourceBlockUpgrade
		var raw string
		e = f.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='source-block-upgrade-result'").Scan(&raw)
		if e == nil {
			e = json.Unmarshal([]byte(raw), &upgrade)
		}
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return state, e
		}
		if !(original.Phase == "ready" && original.InputSHA256 == expected || upgrade.Phase == "ready" && upgrade.InputSHA256 == expected) {
			return state, errors.New("source packing original input is unproved; preserved")
		}
	}
	state.RequestHash = state.InputSHA256
	if expected != "" {
		state.RequestHash = expected // Exact current input or a completed migration receipt.
	}
	if err = f.db.QueryRowContext(ctx, "SELECT count(*),coalesce(max(rowid),0) FROM exceptions").Scan(&state.Blocks, &state.MaxOldRow); err != nil {
		return state, err
	}
	var nonpositive bool
	if err = f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM exceptions WHERE rowid<=0)").Scan(&nonpositive); err != nil {
		return state, err
	}
	if nonpositive || state.MaxOldRow >= math.MaxInt64-state.Blocks {
		return state, errors.New("source packing row namespace exhausted; preserved")
	}
	state.NextRow = state.MaxOldRow + 1
	state.Digest, state.Records, err = sourceExceptionStoreDigest(ctx, f)
	if err != nil {
		return state, err
	}
	state.Controls, err = sourceUpgradeControlDigest(ctx, f)
	if err != nil {
		return state, err
	}
	err = f.write(ctx, 256*1024, func(tx *sql.Tx) error {
		return saveSourcePacking(ctx, tx, sourcePackingKey, state)
	})
	return state, err
}

type sourcePackingRow struct {
	row, source int64
	first, last sourcePosition
	records     int
	body        []byte
}

func packSourceBlockBatch(ctx context.Context, f *sourceStore, state *sourceBlockPacking) error {
	rows, err := f.db.QueryContext(ctx, "SELECT rowid,source,offset,block,end_offset,end_block,records,body FROM exceptions WHERE rowid>? AND rowid<=? ORDER BY rowid LIMIT 64", state.Cursor, state.MaxOldRow)
	if err != nil {
		return err
	}
	var items []sourcePackingRow
	var peak uint64
	for rows.Next() {
		var item sourcePackingRow
		if err = rows.Scan(&item.row, &item.source, &item.first.Offset, &item.first.Block, &item.last.Offset, &item.last.Block, &item.records, &item.body); err != nil {
			break
		}
		if len(items) > 0 && peak+uint64(len(item.body))*2 > 16*1024*1024 {
			break
		}
		items = append(items, item)
		peak += uint64(len(item.body)) * 2
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(items) == 0 {
		digest, records, e := sourceExceptionStoreDigest(ctx, f)
		if e != nil {
			return e
		}
		controls, e := sourceUpgradeControlDigest(ctx, f)
		if e != nil {
			return e
		}
		if state.Packed != state.Blocks || records != state.Records || digest != state.Digest || controls != state.Controls {
			return errors.New("source packing complete evidence differs; preserved")
		}
		next := *state
		next.Phase = "reclaim"
		next.Revision++
		if e = f.write(ctx, 0, func(tx *sql.Tx) error { return saveSourcePacking(ctx, tx, sourcePackingKey, next) }); e != nil {
			return e
		}
		*state = next
		return nil
	}
	next := *state
	next.Revision++
	peak += uint64(len(items)) * 8 * 16384
	err = f.write(ctx, peak, func(tx *sql.Tx) error {
		for _, item := range items {
			result, e := tx.ExecContext(ctx, "UPDATE exceptions SET rowid=? WHERE rowid=?", next.NextRow, item.row)
			if e != nil {
				return e
			}
			n, e := result.RowsAffected()
			if e != nil || n != 1 {
				return ErrStale
			}
			var stored sourcePackingRow
			e = tx.QueryRowContext(ctx, "SELECT source,offset,block,end_offset,end_block,records,body FROM exceptions WHERE rowid=?", next.NextRow).Scan(&stored.source, &stored.first.Offset, &stored.first.Block, &stored.last.Offset, &stored.last.Block, &stored.records, &stored.body)
			if e != nil {
				return e
			}
			if stored.source != item.source || stored.first != item.first || stored.last != item.last || stored.records != item.records || !bytes.Equal(stored.body, item.body) {
				return errors.New("source packing block differs; transaction preserved")
			}
			next.Cursor = item.row
			next.NextRow++
			next.Packed++
		}
		return saveSourcePacking(ctx, tx, sourcePackingKey, next)
	})
	if err == nil {
		*state = next
	}
	return err
}

func stepSourceBlockPacking(ctx context.Context, f *sourceStore, state *sourceBlockPacking) error {
	revision, err := sourceStoreRevision(ctx, f)
	if err != nil {
		return err
	}
	if revision != state.Revision {
		return errors.New("source changed during packing; preserved")
	}
	switch state.Phase {
	case "pack_blocks":
		return packSourceBlockBatch(ctx, f, state)
	case "reclaim":
		done, e := reclaimSourcePages(ctx, f)
		if e != nil || !done {
			return e
		}
		next := *state
		next.Revision++
		next.Phase = "ready"
		err = f.write(ctx, 0, func(tx *sql.Tx) error {
			if e := saveSourcePacking(ctx, tx, sourcePackingResultKey, next); e != nil {
				return e
			}
			_, e := tx.ExecContext(ctx, "DELETE FROM meta WHERE key=?", sourcePackingKey)
			return e
		})
		if err == nil {
			*state = next
		}
		return err
	default:
		return errors.New("unknown source packing phase; preserved")
	}
}

// Resume before admitting ordinary source writes. A partially moved table is
// logically valid, but its captured row namespace must stay under one owner.
func (s *Service) resumeSourcePacking(ctx context.Context) error {
	if s.sourcePacking == nil {
		f, err := openSourceStore(ctx, s.path)
		if err != nil {
			return err
		}
		f.checkCapacity = s.capacityCheck
		s.sourcePacking = f
	}
	f := s.sourcePacking
	state, has, err := readSourcePacking(ctx, f, sourcePackingKey)
	if err != nil {
		return err
	}
	if has {
		if s.expectedInputSHA256 != "" && s.expectedInputSHA256 != state.RequestHash {
			return errors.New("source packing input SHA differs; preserved")
		}
		if err = stepSourceBlockPacking(ctx, f, &state); err != nil {
			return err
		}
		if state.Phase != "ready" {
			return errStorageMigration
		}
	}
	err = f.db.Close()
	s.sourcePacking = nil
	return err
}

func optimizeSourceBlockPacking(ctx context.Context, f *sourceStore, expected string, migrated bool, report func(StorageProgress)) (*sourceBlockPacking, error) {
	state, err := beginSourcePacking(ctx, f, expected, migrated)
	if err != nil {
		return nil, err
	}
	last := time.Time{}
	for state.Phase != "ready" {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		err = stepSourceBlockPacking(ctx, f, &state)
		if err != nil {
			return nil, err
		}
		if report != nil && time.Since(last) > time.Second {
			report(StorageProgress{Phase: state.Phase, BlockPacking: &state})
			last = time.Now()
		}
	}
	return &state, nil
}
