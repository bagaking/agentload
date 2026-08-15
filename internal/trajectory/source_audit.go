package trajectory

import (
	"agentload/internal/historyfile"
	"context"
)

// A stable size/mtime pins the entire audit, including across bounded calls.
// Only the last verified physical range publishes the negative-filter seal.
func (f *sourceStore) auditSource(ctx context.Context, st *sourceState) (pending bool, result error) {
	if err := f.validateCheckpoint(ctx, st); err != nil {
		return true, err
	}
	file, before, err := openReplaySource(st)
	if err != nil {
		return true, err
	}
	defer file.Close()
	defer func() {
		if e := finishSourceOperation(ctx, st, file, before); e != nil {
			pending = true
			result = querySourceReadError(st, file, before, e)
		}
	}()
	var size, mtime, after, verifiedSize, verifiedMtime int64
	err = f.db.QueryRowContext(ctx, "SELECT audit_size,audit_mtime,audit_after,verified_size,verified_mtime FROM sources WHERE id=? AND generation=? AND active=1 AND missing=0", st.ID, st.Generation).Scan(&size, &mtime, &after, &verifiedSize, &verifiedMtime)
	if err != nil {
		return true, err
	}
	// A completed audit is idempotent. Do not read past its final cursor and
	// mistake the absence of another range for a changed source.
	if verifiedSize == before.Size() && verifiedMtime == before.ModTime().UnixNano() {
		return false, f.checkRangeSequence(ctx, st)
	}
	if size != before.Size() || mtime != before.ModTime().UnixNano() || after < 0 {
		if err = f.checkRangeSequence(ctx, st); err != nil {
			return true, err
		}
		after = -1
	}
	ranges, err := f.sourceRanges(ctx, st, after, 1)
	if err != nil {
		return true, err
	}
	end := int64(0)
	if len(ranges) > 0 {
		r := ranges[0].value.Chunk
		if err = verifyReplayBytes(ctx, file, r); err != nil {
			return true, err
		}
		after, end = r.Start.Offset, r.End.Offset
	} else if st.checkpoint.Offset != 0 {
		return true, ErrStale
	}
	complete := end == st.checkpoint.Offset
	// Validate the full chain again before publishing trust. A range already
	// read by an earlier unit may have changed between bounded calls.
	if complete {
		if err = f.checkRangeSequence(ctx, st); err != nil {
			return true, err
		}
	}
	if err = finishSourceOperation(ctx, st, file, before); err != nil {
		return true, err
	}
	if err = historyfile.CheckStorageCapacity(f.path, 128*1024); err != nil {
		return true, err
	}
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		return true, err
	}
	defer tx.Rollback()
	if complete {
		_, err = tx.ExecContext(ctx, "UPDATE sources SET audit_size=?,audit_mtime=?,audit_after=?,verified_size=?,verified_mtime=? WHERE id=? AND generation=? AND active=1 AND missing=0", before.Size(), before.ModTime().UnixNano(), after, before.Size(), before.ModTime().UnixNano(), st.ID, st.Generation)
	} else {
		_, err = tx.ExecContext(ctx, "UPDATE sources SET audit_size=?,audit_mtime=?,audit_after=? WHERE id=? AND generation=? AND active=1 AND missing=0", before.Size(), before.ModTime().UnixNano(), after, st.ID, st.Generation)
	}
	if err != nil {
		return true, err
	}
	if err = finishSourceOperation(ctx, st, file, before); err != nil {
		return true, err
	}
	// Auditing changes acceleration trust, never canonical/search facts. It
	// must not invalidate pagination while the user reads an unchanged source.
	err = commitSearchTransaction(tx, f.db)
	return !complete, err
}
