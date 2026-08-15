package trajectory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"time"
)

// Old mount-number checkpoints can be rebound only after every already
// committed raw range has been verified on this durable file. The pending
// binding pins bounded progress across restarts and never grants read access.
func (f *sourceStore) upgradeFileIdentity(ctx context.Context, src Source, cp sourceCheckpoint, info os.FileInfo, preserveOriginal bool, maxBytes int64, deadline time.Time) (sourceCheckpoint, error) {
	file, err := os.Open(src.Path)
	if err != nil {
		return cp, err
	}
	defer file.Close()
	before, err := file.Stat()
	if err != nil || !os.SameFile(info, before) {
		return cp, ErrStale
	}
	identity, err := persistentFileIdentity(file, before)
	if err != nil {
		return cp, err
	}
	if cp.Identity == identity {
		return cp, nil
	}
	if !legacyFileIdentity(cp.Identity, before) {
		return cp, ErrStale
	}
	observed := cp
	observed.Identity = identity
	if !checkpointSourceMatch(file, before, observed) {
		return cp, ErrStale
	}
	st := &sourceState{Source: src, ID: sourceID(src), Generation: cp.Generation, Info: before, checkpoint: observed}
	var size, mtime, after int64
	var complete bool
	err = f.db.QueryRowContext(ctx, "SELECT audit_size,audit_mtime,audit_after,complete FROM sources WHERE id=? AND generation=? AND active=1 AND missing=0", st.ID, st.Generation).Scan(&size, &mtime, &after, &complete)
	if err != nil {
		return cp, err
	}
	if cp.PendingIdentity != identity || size != before.Size() || mtime != before.ModTime().UnixNano() {
		cp.PendingIdentity = identity
		if err = f.writeIdentityCheckpoint(ctx, st, file, before, cp, -1, false, preserveOriginal); err != nil {
			return cp, err
		}
		after = -1
		st.checkpoint.PendingIdentity = cp.PendingIdentity
	}
	ranges, err := f.sourceRanges(ctx, st, after, 64)
	if err != nil {
		return cp, err
	}
	scanned, visited := int64(0), 0
	for _, r := range ranges {
		if visited > 0 && (scanned >= maxBytes || !deadline.IsZero() && !time.Now().Before(deadline)) {
			break
		}
		if err = verifyReplayBytes(ctx, file, r.value.Chunk); err != nil {
			return cp, err
		}
		after = r.value.Chunk.Start.Offset
		scanned += r.value.Chunk.End.Offset - r.value.Chunk.Start.Offset
		visited++
	}
	if len(ranges) == 64 || visited < len(ranges) {
		if err = f.writeIdentityCheckpoint(ctx, st, file, before, cp, after, false, preserveOriginal); err != nil {
			return cp, err
		}
		return cp, errStorageMigration
	}
	if complete {
		if err = f.checkRangeSequence(ctx, st); err != nil {
			return cp, err
		}
	}
	cp.Identity, cp.PendingIdentity = identity, ""
	if err = f.writeIdentityCheckpoint(ctx, st, file, before, cp, after, complete, preserveOriginal); err != nil {
		return cp, err
	}
	return cp, nil
}

func (f *sourceStore) writeIdentityCheckpoint(ctx context.Context, st *sourceState, file *os.File, before os.FileInfo, cp sourceCheckpoint, after int64, sealed, preserveOriginal bool) error {
	raw, err := json.Marshal(cp)
	if err != nil {
		return err
	}
	body, err := encodeSourceValue(raw)
	if err != nil {
		return err
	}
	var original []byte
	path := metadataPath([][]byte{[]byte("source-identity-v1"), []byte(st.ID), []byte(st.Generation)})
	if preserveOriginal && st.checkpoint.PendingIdentity == "" {
		if err = f.db.QueryRowContext(ctx, "SELECT checkpoint FROM sources WHERE id=? AND generation=? AND active=1", st.ID, st.Generation).Scan(&original); err != nil {
			return err
		}
	} else if preserveOriginal {
		// A pending online conversion must already own its opaque original.
		var saved []byte
		if err = f.db.QueryRowContext(ctx, "SELECT body FROM metadata WHERE path=?", path).Scan(&saved); err != nil {
			return err
		}
	}
	err = f.write(ctx, uint64(256*1024+2*(len(body)+len(original)+3*len(path))), func(tx *sql.Tx) error {
		if original != nil {
			if err := preserveMigrationMetadata(ctx, tx, path, "0", original, 0); err != nil {
				return err
			}
		}
		verifiedSize, verifiedMtime := int64(-1), int64(-1)
		if sealed {
			verifiedSize, verifiedMtime = before.Size(), before.ModTime().UnixNano()
		}
		res, err := tx.ExecContext(ctx, "UPDATE sources SET checkpoint=?,audit_size=?,audit_mtime=?,audit_after=?,verified_size=?,verified_mtime=? WHERE id=? AND generation=? AND active=1 AND missing=0", body, before.Size(), before.ModTime().UnixNano(), after, verifiedSize, verifiedMtime, st.ID, st.Generation)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrStale
		}
		return finishSourceOperation(ctx, st, file, before)
	})
	if err != nil {
		return err
	}
	if original != nil {
		var saved []byte
		if err = f.db.QueryRowContext(ctx, "SELECT body FROM metadata WHERE path=?", path).Scan(&saved); err != nil {
			return err
		}
		if !bytes.Equal(saved, original) {
			return errors.New("identity original checkpoint differs; preserved")
		}
	}
	return nil
}
