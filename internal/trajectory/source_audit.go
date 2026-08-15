package trajectory

import (
	"agentload/internal/historyfile"
	"context"
	"time"
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
	end := int64(0)
	readBytes := int64(0)
	deadline := time.Now().Add(25 * time.Millisecond)
	for visited := 0; visited < 64; visited++ {
		if visited > 0 && (readBytes >= 4*1024*1024 || !time.Now().Before(deadline)) {
			break
		}
		// Read one range at a time so a large decoder state cannot multiply
		// resident memory by the batch limit. Only the cursor shares a commit.
		ranges, err := f.sourceRanges(ctx, st, after, 1)
		if err != nil {
			return true, err
		}
		if len(ranges) == 0 {
			if visited == 0 && st.checkpoint.Offset != 0 {
				return true, ErrStale
			}
			break
		}
		r := ranges[0].value.Chunk
		if visited > 0 && readBytes+r.End.Offset-r.Start.Offset > 4*1024*1024 {
			break
		}
		if err = verifyReplayBytes(ctx, file, r); err != nil {
			return true, err
		}
		after, end = r.Start.Offset, r.End.Offset
		readBytes += r.End.Offset - r.Start.Offset
		if end == st.checkpoint.Offset {
			break
		}
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
