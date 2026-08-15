package trajectory

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
)

const sourceBlockUpgradeKey = "source-block-upgrade"

type sourceBlockUpgrade struct {
	Phase       string `json:"phase"`
	Source      int64  `json:"source"`
	Offset      int64  `json:"offset"`
	Block       int    `json:"block"`
	Records     int64  `json:"records"`
	Total       int64  `json:"total"`
	BytesBefore int64  `json:"bytes_before"`
	Digest      string `json:"logical_digest"`
	Controls    string `json:"control_digest"`
	InputSHA256 string `json:"input_sha256"`
}

func readSourceBlockUpgrade(ctx context.Context, f *sourceStore) (sourceBlockUpgrade, bool, error) {
	var state sourceBlockUpgrade
	var body string
	err := f.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", sourceBlockUpgradeKey).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return state, false, nil
	}
	if err == nil {
		err = json.Unmarshal([]byte(body), &state)
	}
	return state, true, err
}
func saveSourceBlockUpgrade(tx *sql.Tx, state sourceBlockUpgrade) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", sourceBlockUpgradeKey, string(raw))
	return err
}

// These owners are untouched by repacking. Bind and check their exact values,
// including opaque metadata and checkpoints, without another database copy.
func sourceUpgradeControlDigest(ctx context.Context, f *sourceStore) (string, error) {
	hash := sha256.New()
	for section, query := range []string{
		"SELECT key,value FROM meta WHERE key NOT IN ('revision','source-block-upgrade','source-block-upgrade-result') ORDER BY key",
		"SELECT path,sequence,body,bucket FROM metadata ORDER BY path",
		"SELECT * FROM sources ORDER BY rowid",
		"SELECT rowid,source,start,end,body,filter FROM ranges ORDER BY rowid",
	} {
		rows, err := f.db.QueryContext(ctx, query)
		if err != nil {
			return "", err
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			return "", err
		}
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		hash.Write([]byte(query))
		hash.Write([]byte{0})
		for rows.Next() {
			if err = rows.Scan(targets...); err != nil {
				break
			}
			// Keep the established control hash domain; exclude only the new
			// maintenance owner's progress and result, as with upgrade state.
			if section == 0 && (values[0] == sourcePackingKey || values[0] == sourcePackingResultKey) {
				continue
			}
			raw, e := json.Marshal(values)
			if e != nil {
				err = e
				break
			}
			hash.Write(raw)
			hash.Write([]byte{'\n'})
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func initializeSourceBlockUpgrade(ctx context.Context, f *sourceStore) error {
	_, has, err := readSourceBlockUpgrade(ctx, f)
	if err != nil || has {
		return err
	}
	var mode, pageSize int
	if err = f.db.QueryRowContext(ctx, "PRAGMA auto_vacuum").Scan(&mode); err != nil {
		return err
	}
	if err = f.db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
		return err
	}
	if mode != 2 || pageSize != 16384 {
		return errors.New("source upgrade requires existing incremental 16KiB store; preserved")
	}
	controls, err := sourceUpgradeControlDigest(ctx, f)
	if err != nil {
		return err
	}
	state := sourceBlockUpgrade{Phase: "exception_blocks", Controls: controls, Offset: -1, Block: -1}
	info, err := os.Stat(f.path)
	if err != nil {
		return err
	}
	state.BytesBefore = info.Size()
	state.InputSHA256, _, err = sourceFileSeal(ctx, f.path)
	if err != nil {
		return err
	}
	if err = f.db.QueryRowContext(ctx, "SELECT count(*) FROM exceptions").Scan(&state.Total); err != nil {
		return err
	}
	return f.write(ctx, 256*1024, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "ALTER TABLE exceptions ADD COLUMN end_offset INTEGER NOT NULL DEFAULT -1; ALTER TABLE exceptions ADD COLUMN end_block INTEGER NOT NULL DEFAULT -1; ALTER TABLE exceptions ADD COLUMN records INTEGER NOT NULL DEFAULT 0;"); err != nil {
			return err
		}
		return saveSourceBlockUpgrade(tx, state)
	})
}

type sourceUpgradeFact struct {
	row, source, rangeStart int64
	fact                    sourceException
	raw                     []byte // Full original DTO, retained for independent transactional proof.
}

// Retain strict v2 decoding only in this migration owner. An unknown field
// cannot disappear during a typed rewrite.
func decodeLegacySourceException(body []byte, offset int64, block int) (sourceException, []byte, error) {
	var fact sourceException
	raw, err := decodeSourceValue(body, maxStoredValue)
	if err != nil {
		return fact, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&fact); err != nil {
		return fact, nil, err
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return fact, nil, errors.New("invalid legacy source exception tail; preserved")
	}
	if fact.Offset != offset || fact.Block != block {
		return fact, nil, ErrStale
	}
	original, err := json.Marshal(fact)
	return fact, original, err
}

func sourceBlockUpgradeBatch(ctx context.Context, f *sourceStore, state sourceBlockUpgrade) ([]sourceUpgradeFact, uint64, error) {
	rows, err := f.db.QueryContext(ctx, "SELECT e.rowid,e.source,e.offset,e.block,e.body,s.missing,s.active FROM exceptions e JOIN sources s ON s.rowid=e.source WHERE (e.source,e.offset,e.block)>(?,?,?) AND e.records=0 ORDER BY e.source,e.offset,e.block LIMIT 512", state.Source, state.Offset, state.Block)
	if err != nil {
		return nil, 0, err
	}
	var result []sourceUpgradeFact
	var peak uint64
	var readErr error
	for rows.Next() {
		var item sourceUpgradeFact
		var offset int64
		var block int
		var body []byte
		var missing, active bool
		if readErr = rows.Scan(&item.row, &item.source, &offset, &block, &body, &missing, &active); readErr != nil {
			break
		}
		fact, raw, e := decodeLegacySourceException(body, offset, block)
		if e != nil {
			readErr = e
			break
		}
		if len(result) > 0 && peak+2*uint64(len(raw)+len(body)) > 16*1024*1024 {
			break
		}
		item.fact, item.raw, item.rangeStart = fact, raw, -1
		// Resolve queryable anchors after closing the single SQLite reader.
		if !missing && active {
			item.rangeStart = -2
		}
		result = append(result, item)
		peak += 2 * uint64(len(raw)+len(body))
	}
	if readErr == nil {
		readErr = rows.Err()
	}
	rows.Close()
	if readErr != nil {
		return nil, 0, readErr
	}
	for i := range result {
		item := &result[i]
		if item.rangeStart != -2 {
			continue
		}
		var end int64
		err = f.db.QueryRowContext(ctx, "SELECT start,end FROM ranges WHERE source=? AND start<=? ORDER BY start DESC LIMIT 1", item.source, item.fact.Offset).Scan(&item.rangeStart, &end)
		if err != nil {
			return nil, 0, err
		}
		if item.fact.Offset >= end {
			return nil, 0, ErrStale
		}
	}
	return result, peak, nil
}

func upgradeSourceBlockBatch(ctx context.Context, f *sourceStore, state *sourceBlockUpgrade) error {
	items, peak, err := sourceBlockUpgradeBatch(ctx, f, *state)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		if state.Records != state.Total {
			return errors.New("source block upgrade cardinality differs; preserved")
		}
		controls, e := sourceUpgradeControlDigest(ctx, f)
		if e != nil {
			return e
		}
		if controls != state.Controls {
			return errors.New("source upgrade changed control evidence; preserved")
		}
		digest, count, e := sourceExceptionStoreDigest(ctx, f)
		if e != nil {
			return e
		}
		if digest != state.Digest || count != state.Total {
			return errors.New("whole exception store proof differs; preserved")
		}
		state.Phase = "reclaim"
		return f.write(ctx, 0, func(tx *sql.Tx) error { return saveSourceBlockUpgrade(tx, *state) })
	}
	next := *state
	// Physical source/slot order is the durable cursor and proof order. Packing
	// sorts only this bounded batch; it never crosses an authorized replay range.
	for _, item := range items {
		next.Source, next.Offset, next.Block = item.source, item.fact.Offset, item.fact.Block
		next.Records++
		h := sha256.New()
		h.Write([]byte(next.Digest))
		h.Write([]byte(fmt.Sprintf("%d:", item.source)))
		h.Write(item.raw)
		next.Digest = hex.EncodeToString(h.Sum(nil))
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.source != b.source {
			return a.source < b.source
		}
		if a.rangeStart != b.rangeStart {
			return a.rangeStart < b.rangeStart
		}
		return exceptionPositionLess(sourcePosition{a.fact.Offset, a.fact.Block}, sourcePosition{b.fact.Offset, b.fact.Block})
	})
	// 512 sparse original rows may occupy separate table/index pages. Budget
	// their rollback copies independently of compressed or factored body sizes.
	peak += uint64(len(items)) * 8 * 16384
	err = f.write(ctx, peak, func(tx *sql.Tx) error {
		for start := 0; start < len(items); {
			end := start + 1
			for end < len(items) && items[end].source == items[start].source && items[end].rangeStart == items[start].rangeStart {
				end++
			}
			facts := make([]sourceException, 0, end-start)
			for _, item := range items[start:end] {
				facts = append(facts, item.fact)
			}
			blocks, e := encodeSourceExceptionBlocks(facts)
			if e != nil {
				return e
			}
			consumed := start
			for _, block := range blocks {
				first := items[consumed]
				// Update the first original row; its old content remains recoverable in
				// the rollback journal until independent decode and equality checks pass.
				_, e = tx.ExecContext(ctx, "UPDATE exceptions SET body=?,end_offset=?,end_block=?,records=? WHERE rowid=? AND records=0", block.body, block.last.Offset, block.last.Block, block.records, first.row)
				if e != nil {
					return e
				}
				var body []byte
				var lo, hi sourcePosition
				var count int
				e = tx.QueryRowContext(ctx, "SELECT offset,block,end_offset,end_block,records,body FROM exceptions WHERE rowid=?", first.row).Scan(&lo.Offset, &lo.Block, &hi.Offset, &hi.Block, &count, &body)
				if e != nil {
					return e
				}
				saved, _, e := decodeSourceExceptionBlock(body, lo, hi, count)
				if e != nil {
					return e
				}
				for i, record := range saved {
					raw, e := json.Marshal(record)
					if e != nil {
						return e
					}
					if !bytes.Equal(raw, items[consumed+i].raw) {
						return errors.New("complete source block fact differs; originals preserved")
					}
				}
				for _, old := range items[consumed+1 : consumed+block.records] {
					if _, e = tx.ExecContext(ctx, "DELETE FROM exceptions WHERE rowid=? AND records=0", old.row); e != nil {
						return e
					}
				}
				consumed += block.records
			}
			start = end
		}
		return saveSourceBlockUpgrade(tx, next)
	})
	if err == nil {
		*state = next
	}
	return err
}

func reclaimSourceBlockPages(ctx context.Context, f *sourceStore, state sourceBlockUpgrade) error {
	done, err := reclaimSourcePages(ctx, f)
	if err != nil {
		return err
	}
	if !done {
		return errStorageMigration
	}
	var zero bool
	if err := f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM exceptions WHERE records=0)").Scan(&zero); err != nil {
		return err
	}
	if zero {
		return ErrStale
	}
	state.Phase = "ready"
	return f.write(ctx, 0, func(tx *sql.Tx) error {
		raw, err := json.Marshal(state)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT OR REPLACE INTO meta(key,value) VALUES('source-block-upgrade-result',?)", string(raw)); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM meta WHERE key=?", sourceBlockUpgradeKey); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version=%d", sourceStoreVersion))
		return err
	})
}

func reclaimSourcePages(ctx context.Context, f *sourceStore) (bool, error) {
	var free int
	if err := f.db.QueryRowContext(ctx, "PRAGMA freelist_count").Scan(&free); err != nil {
		return false, err
	}
	if free > 0 {
		if err := f.checkCapacity(f.path, 8*1024*1024); err != nil {
			return false, err
		}
		// Consume every sqlite_step: stopping at the first returned row can reclaim
		// just one page, even when the pragma asks for a bounded batch.
		rows, err := f.db.QueryContext(ctx, "PRAGMA incremental_vacuum(128)")
		if err != nil {
			return false, err
		}
		for rows.Next() {
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return false, err
		}
		return false, nil
	}
	return true, nil
}

// Decode every completed physical block once, in canonical slot order. This
// independent post-write pass catches omitted/overlapping members and checks
// the original complete-fact digest without per-event database queries.
func sourceExceptionStoreDigest(ctx context.Context, f *sourceStore) (string, int64, error) {
	rows, err := f.db.QueryContext(ctx, "SELECT source,offset,block,end_offset,end_block,records,body FROM exceptions ORDER BY source,offset,block")
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	var digest string
	var count, previousSource int64
	var previous sourcePosition
	for rows.Next() {
		var source int64
		var first, last sourcePosition
		var n int
		var body []byte
		if err = rows.Scan(&source, &first.Offset, &first.Block, &last.Offset, &last.Block, &n, &body); err != nil {
			return "", 0, err
		}
		if source == previousSource && !exceptionPositionLess(previous, first) {
			return "", 0, ErrStale
		}
		facts, _, e := decodeSourceExceptionBlock(body, first, last, n)
		if e != nil {
			return "", 0, e
		}
		for _, fact := range facts {
			raw, e := json.Marshal(fact)
			if e != nil {
				return "", 0, e
			}
			h := sha256.New()
			h.Write([]byte(digest))
			h.Write([]byte(fmt.Sprintf("%d:", source)))
			h.Write(raw)
			digest = hex.EncodeToString(h.Sum(nil))
			count++
		}
		previousSource, previous = source, last
	}
	return digest, count, rows.Err()
}

func checkSourceUpgradeInput(ctx context.Context, f *sourceStore, expected, physical string) error {
	if expected == "" || expected == physical {
		return nil
	}
	// A sealed v2 target can be the next stage of the original request. Its
	// completed cutover receipt retains that lineage; the upgrade still records
	// the actual v2 bytes separately as InputSHA256.
	original, has, err := sourceMigrationState(ctx, f)
	if err != nil {
		return err
	}
	if has && original.Phase == "ready" && original.InputSHA256 == expected {
		return nil
	}
	return errors.New("source block upgrade input SHA differs; preserved")
}

func (s *Service) upgradeSourceBlocks(ctx context.Context, path string) error {
	if s.sourceUpgrade == nil {
		f, err := openMigrationSourceStore(ctx, path)
		if err != nil {
			return err
		}
		f.checkCapacity = s.capacityCheck
		s.sourceUpgrade = f
	}
	f := s.sourceUpgrade
	if f.path != path {
		return errors.New("source upgrade owner path differs; preserved")
	}
	if s.expectedInputSHA256 != "" {
		state, has, e := readSourceBlockUpgrade(ctx, f)
		if e != nil {
			return e
		}
		if !has {
			state.InputSHA256, _, e = sourceFileSeal(ctx, path)
			if e != nil {
				return e
			}
		}
		if e = checkSourceUpgradeInput(ctx, f, s.expectedInputSHA256, state.InputSHA256); e != nil {
			return e
		}
	}
	if err := initializeSourceBlockUpgrade(ctx, f); err != nil {
		return err
	}
	state, _, err := readSourceBlockUpgrade(ctx, f)
	if err != nil {
		return err
	}
	if err = checkSourceUpgradeInput(ctx, f, s.expectedInputSHA256, state.InputSHA256); err != nil {
		return err
	}
	if state.Phase == "exception_blocks" {
		if err = upgradeSourceBlockBatch(ctx, f, &state); err != nil {
			return err
		}
		return errStorageMigration
	}
	if state.Phase != "reclaim" {
		return errors.New("unknown source block upgrade phase; preserved")
	}
	if err = reclaimSourceBlockPages(ctx, f, state); err != nil {
		return err
	}
	err = f.db.Close()
	s.sourceUpgrade = nil
	return err
}

// A ready sealed v2 shadow must retain its exact bytes until its existing
// cutover is reconciled. An unsealed/repairing shadow has no such publication
// claim and can upgrade before the current body writer resumes.
func (s *Service) upgradeUnsealedSourceShadow(ctx context.Context) error {
	path := s.path + ".source-migrating"
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	version, err := sqliteVersion(ctx, path)
	if err != nil || version != 2 {
		return err
	}
	f, err := openMigrationSourceStore(ctx, path)
	if err != nil {
		return err
	}
	state, has, err := sourceMigrationState(ctx, f)
	f.db.Close()
	if err != nil {
		return err
	}
	if has && state.Phase == "ready" {
		if _, err = os.Stat(s.path + ".source-ready.json"); err == nil {
			return checkSourceCutoverSeal(ctx, s.path, path, state)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if s.sourceMigration != nil && s.sourceMigration.shadow != nil {
		if err = closeSourceShadow(s.sourceMigration); err != nil {
			return err
		}
	}
	if err = s.upgradeSourceBlocks(ctx, path); err != nil {
		return err
	}
	if s.sourceMigration != nil {
		s.sourceMigration.shadow, err = openSourceStore(ctx, path)
	}
	return err
}
