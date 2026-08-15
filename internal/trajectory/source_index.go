package trajectory

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"strconv"
	"time"

	"agentload/internal/snapshot"
	fastjson "github.com/goccy/go-json"
)

type sourceGroup struct {
	value  sourceRange
	filter *sourceFilter
	hash   hash.Hash
}

func newSourceGroup(start replayAnchor) sourceGroup {
	return sourceGroup{value: sourceRange{Chunk: replayChunk{Version: projectionVersion, Start: start, End: start}}, filter: sizedSourceFilter(replayChunkBytes), hash: sha256.New()}
}

func finishSourceGroup(group *sourceGroup) error {
	c := &group.value.Chunk
	c.Hash = hex.EncodeToString(group.hash.Sum(nil))
	c.HashState = nil
	if c.End.Offset-c.Start.Offset < replayChunkBytes {
		var err error
		c.HashState, err = group.hash.(encoding.BinaryMarshaler).MarshalBinary()
		return err
	}
	return nil
}

func (f *sourceStore) sourceTail(ctx context.Context, st *sourceState) (*sourceGroup, error) {
	if st.checkpoint.Offset == 0 {
		return nil, nil
	}
	var start int64
	err := f.db.QueryRowContext(ctx, "SELECT r.start FROM ranges r JOIN sources s ON s.rowid=r.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND r.end=? ORDER BY r.start DESC LIMIT 1", st.ID, st.Generation, st.checkpoint.Offset).Scan(&start)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStale
	}
	if err != nil {
		return nil, err
	}
	ranges, err := f.sourceRanges(ctx, st, start-1, 1)
	if err != nil {
		return nil, err
	}
	if len(ranges) != 1 || ranges[0].value.Chunk.Start.Offset != start || ranges[0].value.Chunk.End.Offset != st.checkpoint.Offset {
		return nil, ErrStale
	}
	r := ranges[0]
	c := r.value.Chunk
	if c.End.Offset-c.Start.Offset >= replayChunkBytes {
		return nil, nil
	}
	if c.End.Line != st.checkpoint.Line || c.End.WorkingDirectory != st.checkpoint.WorkingDirectory {
		return nil, ErrStale
	}
	h := sha256.New()
	if len(c.HashState) == 0 || len(c.HashState) > 256 {
		return nil, ErrStale
	}
	if err = h.(encoding.BinaryUnmarshaler).UnmarshalBinary(c.HashState); err != nil {
		return nil, err
	}
	if hex.EncodeToString(h.Sum(nil)) != c.Hash {
		return nil, ErrStale
	}
	return &sourceGroup{value: r.value, filter: r.filter, hash: h}, nil
}

func checkpointSourceMatch(file *os.File, info os.FileInfo, c sourceCheckpoint) bool {
	identity, err := persistentFileIdentity(file, info)
	if err != nil || c.Missing || c.Generation == "" || c.Identity != identity || info.Size() < c.Size {
		return false
	}
	if info.Size() == c.Size && info.ModTime().UnixNano() != c.Mtime {
		return false
	}
	prefix := make([]byte, len(c.Prefix))
	n, _ := file.ReadAt(prefix, 0)
	if n != len(prefix) || !bytes.Equal(prefix, c.Prefix) {
		return false
	}
	if c.AnchorLength > 0 {
		anchor := make([]byte, c.AnchorLength)
		n, _ = file.ReadAt(anchor, c.AnchorOffset)
		if n != len(anchor) || digest(anchor) != c.AnchorDigest {
			return false
		}
	}
	return true
}

func persistentSourceCoverage(c snapshot.TrajectoryCoverage) snapshot.TrajectoryCoverage {
	out := c
	out.Gaps = []string{}
	out.Complete = true
	for _, g := range c.Gaps {
		if g != "partial_record" && g != "scan_cancelled" && g != "scan_size_limit" && g != "source_changed_during_index" {
			gap(&out, g)
		}
	}
	return out
}

// indexSource writes the same shapeRecord facts as replay. Completed events do
// not persist; only the bounded tail filter/hash state and parser checkpoint
// are extended. Appends cannot promote the whole-file verification seal.
func (f *sourceStore) indexSource(ctx context.Context, src Source, maxRecords int, maxBytes int64, deadline time.Time) (*sourceState, error) {
	if src.Decoder == nil || maxRecords < 0 || maxBytes <= 0 {
		return nil, ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(src.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	id := sourceID(src)
	previous, ok, err := f.checkpoint(ctx, id)
	if err != nil {
		return nil, err
	} // preserve damaged state, never guess its facts
	if !ok {
		previous = sourceCheckpoint{}
	}
	if ok && !previous.Missing {
		if err = validateCheckpointIdentity(previous); err != nil {
			return nil, err
		}
	}
	identity, err := persistentFileIdentity(file, info)
	if err != nil {
		return nil, err
	}
	if ok && legacyFileIdentity(previous.Identity, info) && previous.Identity != identity && checkpointLegacyAnchorsMatch(src, previous, info) {
		previous, err = f.upgradeFileIdentity(ctx, src, previous, info, true, maxBytes, deadline)
		if err != nil {
			return nil, err
		}
	}
	same := checkpointSourceMatch(file, info, previous)
	if same && previous.Version != projectionVersion {
		return nil, errors.New("source parser version requires lossless migration; preserved")
	}
	if same {
		var complete bool
		if err = f.db.QueryRowContext(ctx, "SELECT complete FROM sources WHERE id=? AND generation=? AND active=1 AND missing=0", id, previous.Generation).Scan(&complete); err != nil {
			return nil, err
		}
		if !complete {
			return nil, errStorageMigration
		}
	}
	current := previous
	if !same {
		prefix := make([]byte, min(256, int(info.Size())))
		_, _ = file.ReadAt(prefix, 0)
		generation := digest([]byte(identity + ":" + string(prefix) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + previous.Generation))
		current = sourceCheckpoint{Version: projectionVersion, Identity: identity, Prefix: prefix, Generation: generation, Coverage: coverage("source:" + id)}
	}
	st := &sourceState{Source: src, ID: id, Generation: current.Generation, Info: info, checkpoint: current}
	var group sourceGroup
	var tail *sourceGroup
	if same {
		tail, err = f.sourceTail(ctx, st)
		if err != nil {
			return nil, err
		}
	}
	if tail != nil {
		group = *tail
	} else {
		group = newSourceGroup(replayAnchor{current.Offset, current.Line, current.WorkingDirectory})
	}
	groups := []sourceGroup{}
	current.Size = info.Size()
	current.Mtime = info.ModTime().UnixNano()
	current.Coverage = persistentSourceCoverage(current.Coverage)
	if _, err = file.Seek(current.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(io.LimitReader(file, info.Size()-current.Offset), 64*1024)
	scanned, records := int64(0), 0
	newEntityGaps := 0
	lastOffset, lastBlock := int64(-1), -1
	for current.Offset < info.Size() {
		if ctx.Err() != nil {
			gap(&current.Coverage, "scan_cancelled")
			break
		}
		if scanned >= maxBytes || maxRecords > 0 && records >= maxRecords || records > 0 && !deadline.IsZero() && !time.Now().Before(deadline) {
			gap(&current.Coverage, "scan_size_limit")
			break
		}
		priorHash, err := group.hash.(encoding.BinaryMarshaler).MarshalBinary()
		if err != nil {
			return nil, err
		}
		body, length, oversized, readErr := boundedLineTo(ctx, reader, group.hash)
		if readErr != nil {
			if err = group.hash.(encoding.BinaryUnmarshaler).UnmarshalBinary(priorHash); err != nil {
				return nil, err
			}
			if ctx.Err() != nil {
				gap(&current.Coverage, "scan_cancelled")
			} else {
				if length > 0 {
					gap(&current.Coverage, "partial_record")
				}
				if readErr != io.EOF {
					gap(&current.Coverage, "read_error")
				}
			}
			break
		}
		offset := current.Offset
		current.Offset += int64(length)
		current.Line++
		records++
		scanned += int64(length)
		if oversized {
			gap(&current.Coverage, fmt.Sprintf("record_size_limit:L%d", current.Line))
		} else {
			current.AnchorOffset = offset
			current.AnchorLength = len(body)
			current.AnchorDigest = digest(body)
			decoded, omissions := shapeRecord(st, current.Line, offset, body, &current.WorkingDirectory)
			for _, omission := range omissions {
				gap(&current.Coverage, omission)
			}
			for _, e := range decoded {
				raw, err := json.Marshal(e)
				if err != nil || len(raw) > maxStoredValue {
					return nil, errors.New("native event exceeds canonical bound; source preserved")
				}
				var canonical snapshot.TrajectoryEvent
				if err = fastjson.Unmarshal(raw, &canonical); err != nil {
					return nil, err
				}
				raw, err = json.Marshal(canonical)
				if err != nil || len(raw) > maxStoredValue {
					return nil, errors.New("canonical event exceeds source bound; source preserved")
				}
				if err = group.filter.add(context.WithoutCancel(ctx), canonical); err != nil {
					return nil, err
				}
				group.value.Facts++
				group.value.LogicalBytes += len(raw)
				if group.value.LogicalBytes > maxSourceRangeLogicalBytes {
					return nil, errors.New("source group exceeds bounded logical budget; preserved")
				}
				position := sourcePosition{offset, e.Source.Block}
				if group.value.First == nil {
					first := position
					group.value.First = &first
				}
				last := position
				group.value.Last = &last
				if canonical.EntityCoverage != nil && !canonical.EntityCoverage.Complete {
					group.value.EntityIncomplete++
					newEntityGaps++
				}
				group.value.Chunk.Events++
				current.EventCount++
				lastOffset, lastBlock = position.Offset, position.Block
				updateSessionPreview(&current, canonical)
			}
		}
		group.value.Chunk.End = replayAnchor{current.Offset, current.Line, current.WorkingDirectory}
		if group.value.Chunk.End.Offset-group.value.Chunk.Start.Offset >= replayChunkBytes {
			if err = finishSourceGroup(&group); err != nil {
				return nil, err
			}
			groups = append(groups, group)
			group = newSourceGroup(group.value.Chunk.End)
		}
	}
	if group.value.Chunk.End.Offset > group.value.Chunk.Start.Offset && (len(groups) == 0 || group.value.Chunk.End.Offset > groups[len(groups)-1].value.Chunk.End.Offset) {
		if err = finishSourceGroup(&group); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	st.checkpoint = current
	// Cancellation yields only complete physical records. Commit their bounded
	// prefix using the established index owner's cancellation-independent path.
	commitCtx := context.WithoutCancel(ctx)
	err = f.write(commitCtx, 512*1024+uint64(len(groups))*16*1024, func(tx *sql.Tx) error {
		source, err := putSourceCheckpoint(tx, id, src.Agent, current)
		if err != nil {
			return err
		}
		for _, g := range groups {
			if _, err = tx.Exec("DELETE FROM ranges WHERE source=? AND start=?", source, g.value.Chunk.Start.Offset); err != nil {
				return err
			}
			if err = putSourceRange(tx, source, g.value, g.filter); err != nil {
				return err
			}
		}
		// New canonical groups and their filters commit together. A preexisting
		// backlog retains its independent preparation frontier for the common
		// bounded search-preparation owner to advance.
		var ready int
		if err = tx.QueryRow("SELECT search_count FROM sources WHERE rowid=?", source).Scan(&ready); err != nil {
			return err
		}
		if !same || ready == previous.EventCount {
			if lastOffset < 0 && same {
				if err = tx.QueryRow("SELECT search_offset,search_block FROM sources WHERE rowid=?", source).Scan(&lastOffset, &lastBlock); err != nil {
					return err
				}
			}
			if _, err = tx.Exec("UPDATE sources SET search_count=?,search_offset=?,search_block=?,search_entity_gaps=search_entity_gaps+? WHERE rowid=?", current.EventCount, lastOffset, lastBlock, newEntityGaps, source); err != nil {
				return err
			}
		}
		if _, err = tx.Exec("UPDATE sources SET complete=1 WHERE rowid=?", source); err != nil {
			return err
		}
		return finishReplaySource(st, file, info)
	})
	if err != nil {
		return nil, err
	}
	return st, nil
}
