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
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const sourceMigrationVersion = 2

type sourceMigration struct {
	Version            int            `json:"version"`
	Input              string         `json:"input"`
	InputIdentity      string         `json:"input_identity,omitempty"`
	InputSHA256        string         `json:"input_sha256,omitempty"`
	IdentityAfter      int64          `json:"identity_after,omitempty"`
	IdentitiesComplete bool           `json:"identities_complete,omitempty"`
	Phase              string         `json:"phase"`
	Source             int64          `json:"source"`
	Anchor             replayAnchor   `json:"anchor"`
	Metadata           []byte         `json:"metadata,omitempty"`
	Control            string         `json:"control,omitempty"`
	ControlSet         bool           `json:"control_set,omitempty"`
	MetadataSet        bool           `json:"metadata_set,omitempty"`
	Mode               string         `json:"mode,omitempty"`
	FactOffset         int64          `json:"fact_offset"`
	FactBlock          int            `json:"fact_block"`
	Records            int64          `json:"records"`
	BytesBefore        int64          `json:"bytes_before"`
	RepairReturn       int64          `json:"repair_return,omitempty"`
	RepairPhase        string         `json:"repair_phase,omitempty"`
	Legacy             *factMigration `json:"legacy,omitempty"`
}

type sourceMigrationRun struct {
	old           *factStore
	shadow        *sourceStore
	legacy        *factMigrationReader
	ids           []string
	inputVerified bool
	decodedFacts  int64 // migration cost; no event payloads are retained here
}

type sourceCutoverSeal struct {
	Version      int      `json:"version"`
	Input        string   `json:"input"`
	Hash         string   `json:"sha256"`
	Size         int64    `json:"size"`
	Records      int64    `json:"records"`
	Retiring     string   `json:"retiring,omitempty"`
	Retired      []string `json:"retired,omitempty"`
	ProgressHash string   `json:"progress_sha256,omitempty"`
}

func sourceFileSeal(ctx context.Context, path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			return "", 0, err
		}
		n, e := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
			total += int64(n)
		}
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", 0, e
		}
	}
	after, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || before.ModTime() != after.ModTime() || total != before.Size() {
		return "", 0, ErrStale
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

func validateSourceMigrationCounts(ctx context.Context, run *sourceMigrationRun, state sourceMigration) error {
	if run.legacy != nil {
		return validateDirectLegacyCounts(ctx, run, state)
	}
	for _, check := range []struct{ old, new string }{{"SELECT count(*) FROM sources", "SELECT count(*) FROM sources"}, {"SELECT (SELECT count(*) FROM metadata)+(SELECT count(*) FROM meta)+(SELECT count(*) FROM sources)", "SELECT count(*) FROM metadata"}} {
		var old, new int64
		if err := run.old.db.QueryRowContext(ctx, check.old).Scan(&old); err != nil {
			return err
		}
		if err := run.shadow.db.QueryRowContext(ctx, check.new).Scan(&new); err != nil {
			return err
		}
		if old != new {
			return errors.New("migration cardinality differs; originals preserved")
		}
	}
	var events int64
	if err := run.old.db.QueryRowContext(ctx, "SELECT count(*) FROM events").Scan(&events); err != nil {
		return err
	}
	if events != state.Records {
		return errors.New("migration canonical event count differs; originals preserved")
	}
	actual, err := countSourceMigrationFacts(ctx, run.shadow)
	if err != nil {
		return err
	}
	if actual != events {
		return errors.New("migration target canonical event count differs; originals preserved")
	}
	var orphan bool
	if err := run.shadow.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM exceptions e JOIN sources s ON s.rowid=e.source WHERE s.missing=0 AND NOT EXISTS(SELECT 1 FROM ranges r WHERE r.source=e.source AND r.start<=e.offset AND r.end>e.end_offset))").Scan(&orphan); err != nil {
		return err
	}
	if orphan {
		return errors.New("migration has unverified exceptions; originals preserved")
	}
	return nil
}

func countSourceMigrationFacts(ctx context.Context, f *sourceStore) (int64, error) {
	var incomplete bool
	if err := f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sources WHERE missing=0 AND complete=0)").Scan(&incomplete); err != nil {
		return 0, err
	}
	if incomplete {
		return 0, errors.New("migration target source is incomplete; originals preserved")
	}
	rows, err := f.db.QueryContext(ctx, "SELECT r.start,r.end,r.body,s.missing FROM ranges r JOIN sources s ON s.rowid=r.source")
	if err != nil {
		return 0, err
	}
	var total int64
	for rows.Next() {
		var start, end int64
		var body []byte
		var missing bool
		if err = rows.Scan(&start, &end, &body, &missing); err != nil {
			break
		}
		var raw []byte
		raw, err = decodeSourceValue(body, 3*maxRecordBytes)
		if err != nil {
			break
		}
		var value sourceRange
		if err = json.Unmarshal(raw, &value); err != nil {
			break
		}
		if missing || value.Facts < 0 || value.Chunk.Start.Offset != start || value.Chunk.End.Offset != end || end <= start {
			err = errors.New("migration target range differs; originals preserved")
			break
		}
		total += int64(value.Facts)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return 0, err
	}
	rows, err = f.db.QueryContext(ctx, "SELECT e.offset,e.block,e.end_offset,e.end_block,e.records,e.body FROM exceptions e JOIN sources s ON s.rowid=e.source WHERE s.missing=1")
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var first, last sourcePosition
		var count int
		var body []byte
		if err = rows.Scan(&first.Offset, &first.Block, &last.Offset, &last.Block, &count, &body); err != nil {
			return 0, err
		}
		facts, _, e := decodeSourceExceptionBlock(body, first, last, count)
		if e != nil {
			return 0, e
		}
		for _, fact := range facts {
			if fact.Event == nil {
				return 0, errors.New("migration target stored fact differs; originals preserved")
			}
			total++
		}
	}
	return total, rows.Err()
}

// Inspect lengths before loading opaque values. A batch is at most 8 MiB;
// one larger value may be handled alone, bounded by the evidence limit.
func sourceMigrationBatch(ctx context.Context, db *sql.DB, query string, args ...any) (int, uint64, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	count, size := 0, int64(0)
	for rows.Next() {
		var n int64
		if err = rows.Scan(&n); err != nil {
			return 0, 0, err
		}
		if n < 0 || n > maxSourceRangeLogicalBytes {
			return 0, 0, errors.New("opaque recovery value exceeds bounded migration budget; originals preserved")
		}
		if count > 0 && size+n > 8*1024*1024 {
			break
		}
		count++
		size += n
	}
	return count, uint64(size)*2 + 256*1024, rows.Err()
}

func sourceMigrationState(ctx context.Context, f *sourceStore) (sourceMigration, bool, error) {
	var raw string
	err := f.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='source_migration'").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return sourceMigration{}, false, nil
	}
	var st sourceMigration
	if err == nil {
		err = json.Unmarshal([]byte(raw), &st)
	}
	return st, err == nil, err
}
func saveSourceMigration(ctx context.Context, f *sourceStore, st sourceMigration) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return f.write(ctx, uint64(2*len(raw)+128*1024), func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, "INSERT INTO meta VALUES('source_migration',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", string(raw))
		return e
	})
}

func migrationRecoveryPath(input, kind, key string) []byte {
	hash := sha256.Sum256([]byte(input))
	return metadataPath([][]byte{[]byte("source-recovery"), []byte(hex.EncodeToString(hash[:])), []byte(kind), []byte(key)})
}

// Recovery records are opaque evidence, not current-format control state.
// Never overwrite a colliding original metadata record or interpret unknown keys.
func preserveMigrationMetadata(ctx context.Context, tx *sql.Tx, path []byte, sequence string, body []byte, bucket int) error {
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata VALUES(?,?,?,?) ON CONFLICT(path) DO NOTHING", path, sequence, body, bucket); err != nil {
		return err
	}
	var gotSequence string
	var gotBody []byte
	var gotBucket int
	if err := tx.QueryRowContext(ctx, "SELECT sequence,body,bucket FROM metadata WHERE path=?", path).Scan(&gotSequence, &gotBody, &gotBucket); err != nil {
		return err
	}
	if gotSequence != sequence || gotBucket != bucket || !bytes.Equal(gotBody, body) || (gotBody == nil) != (body == nil) {
		return errors.New("migration metadata collision; originals preserved")
	}
	return nil
}

func migrateSourceControl(ctx context.Context, run *sourceMigrationRun, state *sourceMigration) error {
	limit, additional, err := sourceMigrationBatch(ctx, run.old.db, "SELECT 4*length(CAST(key AS BLOB))+length(CAST(value AS BLOB))+512 FROM meta WHERE (?=0 OR key>?) ORDER BY key LIMIT 64", state.ControlSet, state.Control)
	if err != nil {
		return err
	}
	rows, err := run.old.db.QueryContext(ctx, "SELECT key,value FROM meta WHERE (?=0 OR key>?) ORDER BY key LIMIT ?", state.ControlSet, state.Control, limit)
	if err != nil {
		return err
	}
	type control struct{ key, value string }
	var items []control
	for rows.Next() {
		var i control
		if err = rows.Scan(&i.key, &i.value); err != nil {
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
		state.Phase = "metadata"
		return nil
	}
	if err = run.shadow.write(ctx, additional, func(tx *sql.Tx) error {
		for _, i := range items {
			if err := preserveMigrationMetadata(ctx, tx, migrationRecoveryPath(state.Input, "meta", i.key), "0", []byte(i.value), 0); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, i := range items {
		var value []byte
		if err = run.shadow.db.QueryRowContext(ctx, "SELECT body FROM metadata WHERE path=?", migrationRecoveryPath(state.Input, "meta", i.key)).Scan(&value); err != nil {
			return err
		}
		if !bytes.Equal(value, []byte(i.value)) {
			return errors.New("migration control evidence differs; originals preserved")
		}
	}
	state.Control = items[len(items)-1].key
	state.ControlSet = true
	return nil
}

func ensureMigrationSource(ctx context.Context, run *sourceMigrationRun, state sourceMigration, row int64, id, generation, agent string, active, missing int, original []byte, cp sourceCheckpoint, count int, offset int64, block int) error {
	// Allocate the exact original row before importing any range. Otherwise a
	// gapped original row sequence would be renumbered by automatic allocation.
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	frame, err := encodeSourceValue(raw)
	if err != nil {
		return err
	}
	path := migrationRecoveryPath(state.Input, "checkpoint", fmt.Sprint(row))
	err = run.shadow.write(ctx, uint64(256*1024+2*(len(original)+len(frame)+3*len(path)+len(id)+len(generation)+len(agent)+512)), func(tx *sql.Tx) error {
		if err := preserveMigrationMetadata(ctx, tx, path, "0", original, 0); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO sources(rowid,id,generation,active,agent,mtime,missing,checkpoint,search_count,search_offset,search_block) VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(rowid) DO NOTHING", row, id, generation, active, agent, cp.Mtime, missing, frame, count, offset, block); err != nil {
			return err
		}
		var gotID, gotGeneration string
		if err := tx.QueryRowContext(ctx, "SELECT id,generation FROM sources WHERE rowid=?", row).Scan(&gotID, &gotGeneration); err != nil {
			return err
		}
		if gotID != id || gotGeneration != generation {
			return errors.New("migration source identity collision; originals preserved")
		}
		return nil
	})
	if err != nil {
		return err
	}
	var saved []byte
	if err = run.shadow.db.QueryRowContext(ctx, "SELECT body FROM metadata WHERE path=?", path).Scan(&saved); err != nil {
		return err
	}
	if !bytes.Equal(saved, original) {
		return errors.New("migration checkpoint evidence differs; originals preserved")
	}
	return nil
}

func enterStoredMigration(ctx context.Context, run *sourceMigrationRun, state *sourceMigration, row int64) error {
	if state.Mode == "stored" {
		return nil
	}
	if state.Anchor.Offset > 0 {
		var copied int64
		var err error
		copied, err = run.inputFactCount(ctx, row, state.Anchor.Offset)
		if err != nil {
			return err
		}
		state.Records -= copied
	}
	state.Mode = "stored"
	state.FactOffset, state.FactBlock = -1, -1
	return nil
}

// A source can change at any replay boundary, including the durable write's
// final guard and its independent readback. All those boundaries retain the
// same original facts and persist the same bounded recovery transition.
func changedMigrationSource(ctx context.Context, run *sourceMigrationRun, state *sourceMigration, row int64, st *sourceState, cause error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !errors.Is(cause, ErrStale) && !os.IsNotExist(cause) {
		return fmt.Errorf("migration source %d at offset %d: %w", row, state.Anchor.Offset, cause)
	}
	// ErrStale also describes invalid persisted range proofs. Do not erase
	// such evidence as a source change when the authorized file is unchanged.
	info, err := os.Stat(st.Path)
	changed := os.IsNotExist(err)
	if err != nil && !changed {
		return err
	}
	if err == nil {
		changed = !os.SameFile(st.Info, info) || st.Info.Size() != info.Size() || !st.Info.ModTime().Equal(info.ModTime())
		if !changed {
			file, before, openErr := openReplaySource(st)
			if openErr == nil {
				openErr = finishSourceOperation(ctx, st, file, before)
				file.Close()
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			changed = errors.Is(openErr, ErrStale) || os.IsNotExist(openErr)
			if openErr != nil && !changed {
				return openErr
			}
		}
	}
	if !changed {
		return fmt.Errorf("migration source %d at offset %d: %w", row, state.Anchor.Offset, cause)
	}
	if err := enterStoredMigration(ctx, run, state, row); err != nil {
		return err
	}
	if err := saveSourceMigration(ctx, run.shadow, *state); err != nil {
		return err
	}
	return errStorageMigration
}

func sqliteVersion(ctx context.Context, path string) (int, error) {
	u := url.URL{Scheme: "file", Path: path}
	u.RawQuery = "mode=ro&_pragma=busy_timeout(200)"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return 0, err
	}
	defer db.Close()
	var v int
	err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v)
	return v, err
}

// The application authorization inventory, rather than stored paths, chooses
// which local sources may reconstruct facts. Only one bounded shadow is made.
func (s *Service) migrateSourceStore(ctx context.Context) error {
	if err := s.upgradeUnsealedSourceShadow(ctx); err != nil {
		return err
	}

	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		if _, e := os.Stat(s.path + ".source-legacy"); e == nil {
			return s.resumeSourceCutover(ctx)
		}
		if _, e := os.Stat(filepath.Join(filepath.Dir(s.path), "index.bbolt")); e == nil {
			return s.migrateDirectLegacy(ctx)
		}
		return nil
	} else if err != nil {
		return err
	}
	v, err := sqliteVersion(ctx, s.path)
	if err != nil {
		return err
	}
	if v == sourceStoreVersion || v == 2 {
		if err := s.finishSourceCutover(ctx); err != nil {
			return err
		}
		if v == 2 {
			return s.upgradeSourceBlocks(ctx, s.path)
		}
		return s.resumeSourcePacking(ctx)
	}
	if v != factStoreVersion {
		return errors.New("unrecognized trajectory format; original preserved")
	}
	if s.sourceMigration == nil {
		identity, size, err := migrationFileIdentity(s.path)
		if err != nil {
			return err
		}
		if err = s.capacityCheck(s.path, 256*1024*1024); err != nil {
			return err
		}
		u := url.URL{Scheme: "file", Path: s.path}
		u.RawQuery = "mode=ro&_pragma=busy_timeout(200)&_pragma=cache_size(-4096)"
		db, err := sql.Open("sqlite", u.String())
		if err != nil {
			return err
		}
		db.SetMaxOpenConns(1)
		shadow, err := openMigrationSourceStore(ctx, s.path+".source-migrating")
		if err != nil {
			db.Close()
			return err
		}
		s.sourceMigration = &sourceMigrationRun{old: &factStore{db: db, path: s.path}, shadow: shadow}
		state, has, err := sourceMigrationState(ctx, shadow)
		if err != nil {
			return err
		}
		if !has {
			state = sourceMigration{Version: sourceMigrationVersion, Input: identity, Phase: "control", BytesBefore: size}
			err = saveSourceMigration(ctx, shadow, state)
		}
		if err != nil {
			return err
		}
		if state.Version != sourceMigrationVersion {
			return errors.New("migration input changed; source and shadow preserved")
		}
	}
	run := s.sourceMigration
	state, _, err := sourceMigrationState(ctx, run.shadow)
	if err != nil {
		return err
	}
	if !run.inputVerified {
		if state.Phase == "ready" {
			if _, e := os.Stat(s.path + ".source-ready.json"); e == nil {
				if e = checkSourceCutoverSeal(ctx, s.path, s.path+".source-migrating", state); e != nil {
					return e
				}
			} else if os.IsNotExist(e) {
				// No old seal proves control/metadata/stored facts. Binding a
				// physical identity cannot substitute for their full recheck.
				if e = restartUnsealedSourceMigration(ctx, run.shadow, &state); e != nil {
					return e
				}
			} else {
				return e
			}
		}
		if err = s.bindMigrationInput(ctx, run, &state); err != nil {
			return err
		}
		run.inputVerified = true
	}
	if err = checkMigrationInput(ctx, s.path, state, false); err != nil {
		return err
	}
	if !state.IdentitiesComplete && state.RepairReturn == 0 {
		return s.upgradeMigrationIdentities(ctx, run, &state)
	}
	if state.Phase == "ready" {
		if _, err = os.Stat(s.path + ".source-ready.json"); os.IsNotExist(err) {
			// Both restarted and current owners recover cancellation between the
			// ready row and external seal by repeating bounded verification.
			if err = restartUnsealedSourceMigration(ctx, run.shadow, &state); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			return s.installSourceMigration(ctx, state)
		}
	}
	quantum := 25 * time.Millisecond
	if s.storageOffline {
		quantum = time.Second
	}
	if state.Phase == "control" {
		if err = migrateSourceControl(ctx, run, &state); err != nil {
			return err
		}
		if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
			return err
		}
		return errStorageMigration
	}
	if state.Phase == "metadata" {
		limit, additional, err := sourceMigrationBatch(ctx, run.old.db, "SELECT 4*length(path)+length(CAST(sequence AS BLOB))+COALESCE(length(body),0)+512 FROM metadata WHERE (?=0 OR path>?) ORDER BY path LIMIT 64", state.MetadataSet, state.Metadata)
		if err != nil {
			return err
		}
		rows, err := run.old.db.QueryContext(ctx, "SELECT path,sequence,body,bucket FROM metadata WHERE (?=0 OR path>?) ORDER BY path LIMIT ?", state.MetadataSet, state.Metadata, limit)
		if err != nil {
			return err
		}
		type item struct {
			path, body []byte
			sequence   string
			bucket     int
		}
		var items []item
		for rows.Next() {
			var i item
			if err = rows.Scan(&i.path, &i.sequence, &i.body, &i.bucket); err != nil {
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
			state.Phase = "sources"
		} else {
			err = run.shadow.write(ctx, additional, func(tx *sql.Tx) error {
				for _, i := range items {
					if e := preserveMigrationMetadata(ctx, tx, i.path, i.sequence, i.body, i.bucket); e != nil {
						return e
					}
				}
				return nil
			})
			if err != nil {
				return err
			}
			for _, i := range items {
				var seq string
				var body []byte
				var bucket int
				err = run.shadow.db.QueryRowContext(ctx, "SELECT sequence,body,bucket FROM metadata WHERE path=?", i.path).Scan(&seq, &body, &bucket)
				if err != nil {
					return err
				}
				if seq != i.sequence || bucket != i.bucket || !bytes.Equal(body, i.body) || (body == nil) != (i.body == nil) {
					return errors.New("migration metadata differs; originals preserved")
				}
			}
			state.Metadata = items[len(items)-1].path
			state.MetadataSet = true
		}
		if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
			return err
		}
		return errStorageMigration
	}
	return s.migrateSourceFacts(ctx, run, state, quantum)
}

func (s *Service) migrateSourceFacts(ctx context.Context, run *sourceMigrationRun, state sourceMigration, quantum time.Duration) error {
	set := s.provider(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
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
	// Discovery belongs to preparation, not the batch scheduling quantum. The
	// caller's context remains authoritative throughout both. Permit one bounded
	// verified batch even when the soft quantum expires before its first step.
	deadline := time.Now().Add(quantum)
	for first := true; first || time.Now().Before(deadline); first = false {
		var row int64
		var id, generation, agent string
		var active, missing, count, block int
		var offset int64
		var body []byte
		input, err := run.inputSource(ctx, max(int64(1), state.Source))
		row, id, generation, agent = input.row, input.id, input.generation, input.agent
		active, missing, body, count, offset, block = input.active, input.missing, input.body, input.count, input.offset, input.block
		if errors.Is(err, sql.ErrNoRows) {
			if err = s.validateMigrationSources(ctx, run.shadow); err != nil {
				if e := repairMigrationSource(ctx, run, &state, err); e != nil {
					return e
				}
				return errStorageMigration
			}
			if err = validateSourceMigrationCounts(ctx, run, state); err != nil {
				return err
			}
			if run.legacy != nil {
				if state.RepairPhase == "ready" || state.RepairPhase == "legacy-search" {
					state.Phase = "legacy-search"
				} else {
					state.Phase = "legacy-verify"
					state.Legacy.After = nil
					state.Legacy.Hash = nil
					state.Legacy.Records = 0
				}
				state.RepairPhase = ""
				if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
					return err
				}
				return errStorageMigration
			}
			state.Phase = "ready"
			state.RepairPhase = ""
			if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
				return err
			}
			if err = writeSourceCutoverSeal(ctx, s.path, run, state); err != nil {
				return err
			}
			return errStorageMigration
		}
		if err != nil {
			return err
		}
		raw, err := decodeStored(body)
		if err != nil {
			return err
		}
		var cp sourceCheckpoint
		if err = json.Unmarshal(raw, &cp); err != nil {
			return err
		}
		if cp.Generation != generation {
			return ErrStale
		}
		src, ok := allowed[id]
		if state.Source != row {
			state.Source = row
			state.Anchor = replayAnchor{}
			state.Mode = ""
			state.FactOffset, state.FactBlock = -1, -1
		}
		if err = ensureMigrationSource(ctx, run, state, row, id, generation, agent, active, missing, body, cp, count, offset, block); err != nil {
			return err
		}
		if state.Mode == "stored" || !ok || active == 0 || missing != 0 || cp.Missing || cp.Version != projectionVersion {
			if err = enterStoredMigration(ctx, run, &state, row); err != nil {
				return err
			}
			done, err := migrateStoredSource(ctx, run, &state, row, id, agent, active, missing, cp, count, offset, block)
			if err != nil {
				return err
			}
			if done {
				state.Source = row + 1
				if state.RepairReturn != 0 {
					state.Source, state.RepairReturn = state.RepairReturn, 0
				}
				state.Anchor = replayAnchor{}
				state.Mode = ""
				state.FactOffset, state.FactBlock = -1, -1
			}
			if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
				return err
			}
			continue
		}
		if saved, has, e := run.shadow.checkpoint(ctx, id); e != nil {
			return e
		} else if has && saved.Generation == generation {
			cp = saved
		}
		info, err := os.Stat(src.Path)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
			if e := enterStoredMigration(ctx, run, &state, row); e != nil {
				return e
			}
			if e := saveSourceMigration(ctx, run.shadow, state); e != nil {
				return e
			}
			return errStorageMigration
		}
		if legacyFileIdentity(cp.Identity, info) {
			bound, e := run.shadow.upgradeFileIdentity(ctx, src, cp, info, false, 8*1024*1024, deadline)
			if errors.Is(e, errStorageMigration) {
				return e
			}
			if e != nil {
				if errors.Is(e, ErrStale) && !checkpointLegacyAnchorsMatch(src, cp, info) {
					if e = enterStoredMigration(ctx, run, &state, row); e != nil {
						return e
					}
					if e = saveSourceMigration(ctx, run.shadow, state); e != nil {
						return e
					}
					return errStorageMigration
				}
				return e
			}
			cp = bound
		}
		st := &sourceState{Source: src, ID: id, Generation: generation, Info: info, checkpoint: cp}
		if state.Anchor.Offset < cp.Offset {
			chunk, err := nextReplayChunk(ctx, st, state.Anchor)
			if err != nil {
				return changedMigrationSource(ctx, run, &state, row, st, err)
			}
			facts, err := run.inputRange(ctx, row, id, chunk.Start.Offset, chunk.End.Offset)
			if err != nil {
				return err
			}
			if err = run.shadow.importRange(ctx, st, chunk, &facts); err != nil {
				return changedMigrationSource(ctx, run, &state, row, st, err)
			}
			persisted, err := run.shadow.sourceRanges(ctx, st, chunk.Start.Offset-1, 1)
			if err != nil || len(persisted) != 1 {
				if err != nil {
					return err
				}
				return ErrStale
			}
			got, err := run.shadow.readRange(ctx, st, persisted[0])
			if err != nil {
				return changedMigrationSource(ctx, run, &state, row, st, err)
			}
			// Independently re-read canonical facts after the bounded durable write.
			original, err := run.inputRange(ctx, row, id, chunk.Start.Offset, chunk.End.Offset)
			if err != nil {
				return err
			}
			if len(original) != len(got) {
				return errors.New("migration fact count differs; original preserved")
			}
			for i, old := range original {
				a, _ := json.Marshal(old.event)
				b, _ := json.Marshal(got[i].event)
				if old.offset != got[i].offset || old.block != got[i].block || old.size != got[i].size || !bytes.Equal(a, b) {
					return errors.New("complete migration facts differ; original and shadow preserved")
				}
			}
			state.Records += int64(len(facts))
			state.Anchor = chunk.End
			if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
				return err
			}
		} else {
			if err = run.shadow.completeSource(ctx, st, count, offset, block); err != nil {
				return changedMigrationSource(ctx, run, &state, row, st, err)
			}
			state.Source = row + 1
			state.Anchor = replayAnchor{}
			state.Mode = ""
			state.FactOffset, state.FactBlock = -1, -1
			if err = saveSourceMigration(ctx, run.shadow, state); err != nil {
				return err
			}
		}
	}
	return errStorageMigration
}

func legacyRangeFacts(ctx context.Context, f *factStore, row int64, id string, start, end int64) ([]sourceFact, error) {
	refs, err := f.references(ctx, "SELECT "+factRefColumns+" FROM events d JOIN sources s ON s.rowid=d.source WHERE s.rowid=? AND d.offset>=? AND d.offset<? ORDER BY d.offset,d.block", row, start, end)
	if err != nil {
		return nil, err
	}
	facts := []sourceFact{}
	size := 0
	var cache factBlockReadCache
	for _, ref := range refs {
		e, err := f.readEventRow(ctx, id, ref.offset, ref.block, true, &cache, ref.row)
		if err != nil {
			return nil, err
		}
		size += ref.size
		if size > 128*1024*1024 {
			return nil, errors.New("migration range exceeds bounded evidence budget; preserved")
		}
		facts = append(facts, sourceFact{ref.offset, ref.block, e, ref.size})
	}
	return facts, nil
}

func (s *Service) installSourceMigration(ctx context.Context, st sourceMigration) error {
	if st.Legacy != nil {
		return s.installDirectLegacy(ctx, st)
	}
	err := checkMigrationInput(ctx, s.path, st, true)
	if err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// A source change cannot grant authority to modify an already sealed target.
	if err = checkSourceCutoverSeal(ctx, s.path, s.path+".source-migrating", st); err != nil {
		return err
	}
	if s.sourceMigration != nil {
		if err = s.validateMigrationSources(ctx, s.sourceMigration.shadow); err != nil {
			if e := repairMigrationSource(ctx, s.sourceMigration, &st, err); e != nil {
				return e
			}
			if e := os.Remove(s.path + ".source-ready.json"); e != nil && !os.IsNotExist(e) {
				return e
			}
			return errStorageMigration
		}
	}
	if err = checkSourceCutoverSeal(ctx, s.path, s.path+".source-migrating", st); err != nil {
		return err
	}
	if s.sourceMigration != nil {
		if err = s.sourceMigration.closeInput(); err != nil {
			return err
		}
		if err = s.sourceMigration.shadow.db.Close(); err != nil {
			return err
		}
		s.sourceMigration = nil
	}
	if _, err = os.Stat(s.path + ".source-legacy"); err == nil {
		return errors.New("existing migration original must be reconciled; preserved")
	}
	if err = os.Rename(s.path, s.path+".source-legacy"); err != nil {
		return err
	}
	if err = syncStorageDirectory(s.path); err != nil {
		return err
	}
	return s.resumeSourceCutover(ctx)
}
func (s *Service) resumeSourceCutover(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(s.path+".source-migrating", s.path); err != nil {
		return err
	}
	if err := syncStorageDirectory(s.path); err != nil {
		return err
	}
	return s.finishSourceCutover(ctx)
}
func (s *Service) finishSourceCutover(ctx context.Context) error {
	backup := s.path + ".source-legacy"
	if _, err := os.Stat(backup); os.IsNotExist(err) {
		if _, err = os.Stat(s.path + ".source-ready.json"); os.IsNotExist(err) {
			return nil
		} else if err != nil {
			return err
		}
		f, err := openMigrationSourceStore(ctx, s.path)
		if err != nil {
			return err
		}
		state, has, err := sourceMigrationState(ctx, f)
		f.db.Close()
		if err != nil {
			return err
		}
		if !has || state.Phase != "ready" {
			return errors.New("unreconciled cutover seal; originals preserved")
		}
		if s.expectedInputSHA256 != "" && s.expectedInputSHA256 != state.InputSHA256 {
			return errors.New("cutover input SHA differs; originals preserved")
		}
		if state.Legacy != nil {
			return s.finishDirectLegacy(ctx, state)
		}
		if err = checkSourceCutoverSeal(ctx, s.path, s.path, state); err != nil {
			return err
		}
		seal, err := readSourceSeal(s.path)
		if err != nil {
			return err
		}
		if seal.Retiring != "source-legacy" && !slices.Contains(seal.Retired, "source-legacy") {
			return errors.New("migration original disappeared without retirement intent; preserved")
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = os.Remove(s.path + ".source-ready.json"); err != nil {
			return err
		}
		return syncStorageDirectory(s.path)
	} else if err != nil {
		return err
	}
	f, err := openMigrationSourceStore(ctx, s.path)
	if err != nil {
		return err
	}
	defer f.db.Close()
	st, has, err := sourceMigrationState(ctx, f)
	if err != nil {
		return err
	}
	if !has || st.Phase != "ready" {
		return errors.New("new store is not verified; migration original preserved")
	}
	if s.expectedInputSHA256 != "" && s.expectedInputSHA256 != st.InputSHA256 {
		return errors.New("cutover input SHA differs; originals preserved")
	}
	if err = checkSourceCutoverSeal(ctx, s.path, s.path, st); err != nil {
		return err
	}
	if err = checkMigrationInput(ctx, backup, st, true); err != nil {
		return err
	}
	var integrity string
	if err = f.db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("new source store integrity: %s; original preserved", integrity)
	}
	if err = s.validateMigrationSources(ctx, f); err != nil {
		var invalidated *migrationSourceInvalidated
		if errors.As(err, &invalidated) {
			f.db.Close()
			return s.restoreSourceMigration(ctx, st)
		}
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	seal, err := readSourceSeal(s.path)
	if err != nil {
		return err
	}
	seal.Retiring = "source-legacy"
	if err = saveSourceSeal(s.path, seal); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = os.Remove(backup); err != nil {
		return err
	}
	if err = syncStorageDirectory(s.path); err != nil {
		return err
	}
	seal.Retiring = ""
	seal.Retired = append(seal.Retired, "source-legacy")
	if err = saveSourceSeal(s.path, seal); err != nil {
		return err
	}
	if err = os.Remove(s.path + ".source-ready.json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	return syncStorageDirectory(s.path)
}

func writeSourceCutoverSeal(ctx context.Context, path string, run *sourceMigrationRun, state sourceMigration) (result error) {
	if err := closeSourceShadow(run); err != nil {
		return err
	}
	// A cancelled seal operation must remain retryable in this process. Opening
	// this existing small store performs no migration or evidence writes.
	defer func() {
		var err error
		run.shadow, err = openSourceStore(context.WithoutCancel(ctx), path+".source-migrating")
		if result == nil {
			result = err
		}
	}()
	hash, size, err := sourceFileSeal(ctx, path+".source-migrating")
	if err != nil {
		return err
	}
	seal := sourceCutoverSeal{Version: sourceMigrationVersion, Input: state.Input, Hash: hash, Size: size, Records: state.Records}
	if state.Legacy != nil {
		seal.ProgressHash = legacyProgressDigest(run.legacy.progress)
		if previous, e := readSourceSeal(path); e == nil && previous.Input == state.Input {
			if previous.ProgressHash != "" && previous.ProgressHash != seal.ProgressHash {
				return errors.New("retired search recovery evidence differs; originals preserved")
			}
			seal.Retired, seal.Retiring = previous.Retired, previous.Retiring
		}
	}
	return saveSourceSeal(path, seal)
}

func saveSourceSeal(path string, seal sourceCutoverSeal) error {
	body, err := json.Marshal(seal)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path+".source-ready.json.tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	if e := file.Close(); err == nil {
		err = e
	}
	if err != nil {
		return err
	}
	if err = os.Rename(path+".source-ready.json.tmp", path+".source-ready.json"); err != nil {
		return err
	}
	if err = syncStorageDirectory(path); err != nil {
		return err
	}
	return nil
}
func closeSourceShadow(run *sourceMigrationRun) error {
	if run.shadow == nil {
		return nil
	}
	err := run.shadow.db.Close()
	run.shadow = nil
	return err
}
func checkSourceCutoverSeal(ctx context.Context, path, target string, state sourceMigration) error {
	seal, err := readSourceSeal(path)
	if err != nil {
		return err
	}
	if seal.Version != sourceMigrationVersion || seal.Input != state.Input || seal.Records != state.Records || len(seal.Hash) != 64 {
		return errors.New("cutover seal identity differs; original preserved")
	}
	hash, size, err := sourceFileSeal(ctx, target)
	if err != nil {
		return err
	}
	if hash != seal.Hash || size != seal.Size {
		return errors.New("cutover target changed; original preserved")
	}
	return nil
}

func readSourceSeal(path string) (sourceCutoverSeal, error) {
	var seal sourceCutoverSeal
	file, err := os.Open(path + ".source-ready.json")
	if err != nil {
		return seal, err
	}
	body, err := io.ReadAll(io.LimitReader(file, 4097))
	file.Close()
	if err != nil {
		return seal, err
	}
	if len(body) > 4096 {
		return seal, errors.New("invalid cutover seal; original preserved")
	}
	if err = json.Unmarshal(body, &seal); err != nil {
		return seal, err
	}
	return seal, nil
}

// Missing, retired or semantically unreproducible sources keep complete facts.
// These bounded exceptions are inaccessible until current source evidence can
// authorize a new generation; no source/metadata/recovery evidence is deleted.
func migrateStoredSource(ctx context.Context, run *sourceMigrationRun, state *sourceMigration, row int64, id, agent string, active, missing int, cp sourceCheckpoint, count int, offset int64, block int) (bool, error) {
	if state.FactOffset < 0 {
		done, err := run.shadow.clearMigrationSource(ctx, row)
		if err != nil || !done {
			return false, err
		}
	}
	facts, err := run.inputBatch(ctx, row, id, state.FactOffset, state.FactBlock, 64)
	if err != nil {
		return false, err
	}
	additional := uint64(256 * 1024)
	for _, fact := range facts {
		raw, err := json.Marshal(fact.event)
		if err != nil {
			return false, err
		}
		additional += 2 * uint64(len(raw))
		if additional > 128*1024*1024 {
			return false, errors.New("unreproducible facts exceed bounded migration capacity; preserved")
		}
	}
	raw, err := json.Marshal(cp)
	if err != nil {
		return false, err
	}
	body, err := encodeSourceValue(raw)
	if err != nil {
		return false, err
	}
	err = run.shadow.write(ctx, additional, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "UPDATE sources SET missing=1,complete=0,checkpoint=?,search_count=?,search_offset=?,search_block=? WHERE rowid=? AND id=? AND generation=?", body, count, offset, block, row, id, cp.Generation)
		if err != nil {
			return err
		}
		saved := make([]sourceException, 0, len(facts))
		for _, fact := range facts {
			event := fact.event
			saved = append(saved, sourceException{fact.offset, fact.block, &event})
		}
		return replaceSourceExceptions(tx, row, saved)
	})
	if err != nil {
		return false, err
	}
	original, err := run.inputBatch(ctx, row, id, state.FactOffset, state.FactBlock, 64)
	if err != nil {
		return false, err
	}
	if len(original) != len(facts) {
		return false, ErrStale
	}
	for i, fact := range original {
		saved, err := exceptionAt(ctx, run.shadow.db, row, fact.offset, fact.block)
		if err != nil {
			return false, err
		}
		a, _ := json.Marshal(fact.event)
		b, _ := json.Marshal(saved.Event)
		if saved.Offset != facts[i].offset || saved.Block != facts[i].block || !bytes.Equal(a, b) {
			return false, errors.New("unreproducible facts differ; original preserved")
		}
	}
	if len(facts) > 0 {
		last := facts[len(facts)-1]
		state.FactOffset, state.FactBlock = last.offset, last.block
		state.Records += int64(len(facts))
		return false, nil
	}
	expected, err := run.inputFactCount(ctx, row, -1)
	var actual int64
	if err != nil {
		return false, err
	}
	if err = run.shadow.db.QueryRowContext(ctx, "SELECT coalesce(sum(records),0) FROM exceptions WHERE source=?", row).Scan(&actual); err != nil {
		return false, err
	}
	if expected != actual {
		return false, errors.New("unreproducible fact cardinality differs; original preserved")
	}
	return true, nil
}
