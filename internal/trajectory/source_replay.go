package trajectory

import (
	"agentload/internal/snapshot"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Anchors describe state before a complete physical line. They do not contain
// event bodies, entity JSON or an inference about model-input membership.
type replayAnchor struct {
	Offset           int64  `json:"offset"`
	Line             int    `json:"line"` // complete physical lines before Offset
	WorkingDirectory string `json:"cwd,omitempty"`
}

const replayChunkBytes = 128 * 1024

type replayChunk struct {
	Version int          `json:"version"`
	Start   replayAnchor `json:"start"`
	End     replayAnchor `json:"end"`
	Hash    string       `json:"hash"` // SHA256 of the exact complete-record range
	Events  int          `json:"events"`
	// Only an unfinished tail needs the standard SHA-256 marshaled state.
	// This permits bounded online append without decoding its old events again.
	HashState []byte `json:"hash_state,omitempty"`
}

// Any canonical difference is an exception, not just an identity override.
// Absent/retired sources and decoder changes must likewise retain their facts
// or pause migration. The exception uses the existing checksummed codec and
// preserves the entire public DTO; it is never silently replaced by replay.
func encodeReplayException(expected, regenerated snapshot.TrajectoryEvent) ([]byte, error) {
	original, err := json.Marshal(expected)
	if err != nil {
		return nil, err
	}
	replayed, err := json.Marshal(regenerated)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(original, replayed) {
		return nil, nil
	}
	return encodeStored(original)
}

func decodeReplayException(value []byte) (snapshot.TrajectoryEvent, error) {
	var event snapshot.TrajectoryEvent
	raw, err := decodeStored(value)
	if err == nil {
		err = json.Unmarshal(raw, &event)
	}
	return event, err
}

func openReplaySource(st *sourceState) (*os.File, os.FileInfo, error) {
	if st.Decoder == nil || st.ID != sourceID(st.Source) || st.Generation == "" || st.Generation != st.checkpoint.Generation || st.checkpoint.Version != projectionVersion {
		return nil, nil, ErrStale
	}
	f, err := os.Open(st.Path)
	if err != nil {
		return nil, nil, ErrStale
	}
	info, err := f.Stat()
	identity := ""
	if err == nil {
		identity, err = persistentFileIdentity(f, info)
	}
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(st.Info, info) || identity != st.checkpoint.Identity || info.Size() < st.checkpoint.Offset {
		_ = f.Close()
		return nil, nil, ErrStale
	}
	// An unchanged-size rewrite cannot reuse a candidate index built for the
	// old body. Appends may retain the committed prefix; verify its bounded
	// identity anchors before trusting any negative candidate decision.
	if info.Size() < st.checkpoint.Size || info.Size() == st.checkpoint.Size && info.ModTime().UnixNano() != st.checkpoint.Mtime {
		_ = f.Close()
		return nil, nil, ErrStale
	}
	prefix := make([]byte, len(st.checkpoint.Prefix))
	n, _ := f.ReadAt(prefix, 0)
	if n != len(prefix) || !bytes.Equal(prefix, st.checkpoint.Prefix) {
		_ = f.Close()
		return nil, nil, ErrStale
	}
	if st.checkpoint.AnchorLength > 0 {
		anchor := make([]byte, st.checkpoint.AnchorLength)
		n, _ = f.ReadAt(anchor, st.checkpoint.AnchorOffset)
		if n != len(anchor) || digest(anchor) != st.checkpoint.AnchorDigest {
			_ = f.Close()
			return nil, nil, ErrStale
		}
	}
	return f, info, nil
}

// An append BEFORE a read is permitted: only the pinned committed prefix is
// replayed. Any write DURING this read, including append, requires retry. The
// final pathname check prevents an old descriptor surviving a file replacement
// from publishing an apparently current event or migration checkpoint.
func finishReplaySource(st *sourceState, f *os.File, before os.FileInfo) error {
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return ErrStale
	}
	current, err := os.Stat(st.Path)
	if err != nil || !os.SameFile(after, current) || current.Size() != before.Size() || !current.ModTime().Equal(before.ModTime()) {
		return ErrStale
	}
	return nil
}

func finishSourceOperation(ctx context.Context, st *sourceState, f *os.File, before os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return finishReplaySource(st, f, before)
}

// Verify all raw bytes before using a negative candidate without a whole-source
// seal. This proves only this pinned range, never the rest of the source.
func verifyReplayBytes(ctx context.Context, f *os.File, c replayChunk) error {
	if c.Start.Offset < 0 || c.End.Offset <= c.Start.Offset || len(c.Hash) != sha256.Size*2 {
		return ErrStale
	}
	h := sha256.New()
	buf := make([]byte, 64*1024)
	for offset := c.Start.Offset; offset < c.End.Offset; {
		if err := ctx.Err(); err != nil {
			return err
		}
		n := int(min(int64(len(buf)), c.End.Offset-offset))
		read, err := f.ReadAt(buf[:n], offset)
		if err != nil && err != io.EOF {
			return err
		}
		if read != n {
			return ErrStale
		}
		_, _ = h.Write(buf[:read])
		offset += int64(read)
	}
	if hex.EncodeToString(h.Sum(nil)) != c.Hash {
		return ErrStale
	}
	return ctx.Err()
}

// nextReplayChunk freezes at most one bounded range of the committed prefix.
// Append-only bytes after checkpoint.Offset never enter this physical upgrade.
// A single large line is its own bounded unit; oversized records retain their
// existing omission rather than becoming parseable after a storage change.
func nextReplayChunk(ctx context.Context, st *sourceState, start replayAnchor) (replayChunk, error) {
	c := replayChunk{Version: projectionVersion, Start: start, End: start}
	if err := ctx.Err(); err != nil {
		return c, err
	}
	if start.Offset < 0 || start.Offset >= st.checkpoint.Offset || start.Line < 0 {
		return c, ErrInvalid
	}
	f, before, err := openReplaySource(st)
	if err != nil {
		return c, err
	}
	defer f.Close()
	if _, err = f.Seek(start.Offset, io.SeekStart); err != nil {
		return c, err
	}
	r := bufio.NewReaderSize(io.LimitReader(f, st.checkpoint.Offset-start.Offset), 64*1024)
	h := sha256.New()
	for c.End.Offset < st.checkpoint.Offset && c.End.Offset-start.Offset < replayChunkBytes {
		if err = ctx.Err(); err != nil {
			return c, err
		}
		// ReadSlice bounds memory even when an omitted line exceeds 1 MiB.
		var body []byte
		length := 0
		for {
			if err = ctx.Err(); err != nil {
				return c, err
			}
			part, readErr := r.ReadSlice('\n')
			_, _ = h.Write(part)
			length += len(part)
			if length <= maxRecordBytes {
				body = append(body, part...)
			} else {
				body = nil
			}
			if readErr == bufio.ErrBufferFull {
				continue
			}
			if readErr != nil {
				return c, ErrStale // committed prefixes end after a full newline
			}
			break
		}
		c.End.Line++
		if length <= maxRecordBytes {
			events, _ := shapeRecord(st, c.End.Line, c.End.Offset, body, &c.End.WorkingDirectory)
			c.Events += len(events)
		}
		c.End.Offset += int64(length)
	}
	if err = ctx.Err(); err != nil {
		return c, err
	}
	if err = finishReplaySource(st, f, before); err != nil {
		return c, err
	}
	c.Hash = hex.EncodeToString(h.Sum(nil))
	if c.End.Offset-c.Start.Offset < replayChunkBytes {
		c.HashState, err = h.(encoding.BinaryMarshaler).MarshalBinary()
		if err != nil {
			return c, err
		}
	}
	return c, nil
}

// replaySourceChunk pins current evidence and verifies every raw byte before
// exposing regenerated facts. Ordinary ranges use a bounded immutable snapshot;
// oversized omitted ranges are streamed. Both passes verify the full digest.
// Consumers must commit/publish visitor results only after this call succeeds.
func replaySourceChunk(ctx context.Context, st *sourceState, c replayChunk, visit func(snapshot.TrajectoryEvent, int) error) (snapshot.TrajectoryCoverage, error) {
	if err := ctx.Err(); err != nil {
		return coverage("source:" + st.ID), err
	}
	f, before, err := openReplaySource(st)
	if err != nil {
		return coverage("source:" + st.ID), err
	}
	defer f.Close()
	cov, err := replaySourceChunkFrom(ctx, st, c, f, visit)
	if err != nil {
		return cov, err
	}
	return cov, finishSourceOperation(ctx, st, f, before)
}

// The caller owns a descriptor already pinned by openReplaySource and must
// finishSourceOperation before publishing any result. Reusing it across ranges
// avoids re-opening, re-statting and re-reading the same identity anchors.
func replaySourceChunkFrom(ctx context.Context, st *sourceState, c replayChunk, f *os.File, visit func(snapshot.TrajectoryEvent, int) error) (snapshot.TrajectoryCoverage, error) {
	cov := coverage("source:" + st.ID)
	if err := ctx.Err(); err != nil {
		return cov, err
	}
	if c.Version != projectionVersion || c.Start.Offset < 0 || c.End.Offset <= c.Start.Offset || c.End.Offset > st.checkpoint.Offset || c.Start.Line < 0 || c.End.Line <= c.Start.Line || c.Events < 0 || len(c.Hash) != sha256.Size*2 {
		return cov, ErrStale
	}
	length := c.End.Offset - c.Start.Offset
	h := sha256.New()
	rangeReader := io.NewSectionReader(f, c.Start.Offset, length)
	var decodeReader io.Reader
	if length <= replayChunkBytes+maxRecordBytes {
		body := make([]byte, int(length))
		if _, err := io.ReadFull(rangeReader, body); err != nil {
			return cov, err
		}
		_, _ = h.Write(body)
		decodeReader = bytes.NewReader(body)
	} else {
		buf := make([]byte, 64*1024)
		for {
			if err := ctx.Err(); err != nil {
				return cov, err
			}
			n, readErr := rangeReader.Read(buf)
			_, _ = h.Write(buf[:n])
			if readErr == io.EOF {
				break
			}
			if readErr != nil {
				return cov, readErr
			}
		}
		decodeReader = io.NewSectionReader(f, c.Start.Offset, length)
	}
	if hex.EncodeToString(h.Sum(nil)) != c.Hash {
		return cov, ErrStale
	}
	h.Reset()
	reader := bufio.NewReaderSize(io.TeeReader(decodeReader, h), 64*1024)
	pos, events := c.Start, 0
	for pos.Offset < c.End.Offset {
		body, n, oversized, readErr := boundedLine(ctx, reader)
		if readErr != nil {
			if ctx.Err() != nil {
				return cov, ctx.Err()
			}
			return cov, ErrStale
		}
		pos.Line++
		if oversized {
			gap(&cov, fmt.Sprintf("record_size_limit:L%d", pos.Line))
		} else {
			decoded, omissions := shapeRecord(st, pos.Line, pos.Offset, body, &pos.WorkingDirectory)
			for _, omission := range omissions {
				gap(&cov, omission)
			}
			for _, e := range decoded {
				// Existing facts normalize JSON (including RawMessage escaping).
				logical, marshalErr := json.Marshal(e)
				if marshalErr != nil {
					return cov, marshalErr
				}
				if len(logical) > maxStoredValue {
					return cov, fmt.Errorf("trajectory event exceeds storage bound")
				}
				var canonical snapshot.TrajectoryEvent
				if err := json.Unmarshal(logical, &canonical); err != nil {
					return cov, err
				}
				// Encoding may replace an invalid UTF-8 fragment with an escape;
				// its decoded canonical rune has a different encoded byte length.
				logical, marshalErr = json.Marshal(canonical)
				if marshalErr != nil {
					return cov, marshalErr
				}
				if len(logical) > maxStoredValue {
					return cov, fmt.Errorf("trajectory event exceeds storage bound")
				}
				if err := visit(canonical, len(logical)); err != nil {
					return cov, err
				}
				events++
			}
		}
		pos.Offset += int64(n)
	}
	if err := ctx.Err(); err != nil {
		return cov, err
	}
	if pos != c.End || events != c.Events {
		return cov, ErrStale
	}
	if hex.EncodeToString(h.Sum(nil)) != c.Hash {
		return cov, ErrStale
	}
	return cov, nil
}
