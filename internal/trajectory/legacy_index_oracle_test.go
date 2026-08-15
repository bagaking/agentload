package trajectory

import (
	"agentload/internal/historyfile"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Frozen v1 writer is a migration/test oracle, never a live Service backend.
func (store *factStore) legacyIndexQuantum(ctx context.Context, src Source, maxRecords int, maxBytes int64, deadline time.Time) (*sourceState, error) {
	if err := historyfile.CheckStorageBudget(store.path); err != nil {
		return nil, err
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, ErrNotFound
	}
	id := sourceID(src)
	st := &sourceState{Source: src, ID: id, Info: info}
	err = store.write(context.WithoutCancel(ctx), func(w *factWriter) error {
		previous, ok, readErr := store.checkpointIn(w.tx, id)
		rebuilt := readErr != nil
		if !ok {
			previous = sourceCheckpoint{}
		}
		if readErr != nil {
			var generation string
			_ = w.tx.QueryRow("SELECT generation FROM sources WHERE id=? AND active=1", id).Scan(&generation)
			previous = sourceCheckpoint{Generation: generation}
		}
		current := previous
		same := !previous.Missing && previous.Generation != "" && previous.Identity == statIdentity(info) && info.Size() >= previous.Size
		if same {
			prefix := make([]byte, len(previous.Prefix))
			n, _ := f.ReadAt(prefix, 0)
			same = n == len(prefix) && bytes.Equal(prefix, previous.Prefix)
			if same && info.Size() == previous.Size && info.ModTime().UnixNano() != previous.Mtime {
				same = false
			}
			if same && previous.AnchorLength > 0 {
				anchor := make([]byte, previous.AnchorLength)
				n, _ := f.ReadAt(anchor, previous.AnchorOffset)
				same = n == len(anchor) && digest(anchor) == previous.AnchorDigest
			}
		}
		reindex := same && previous.Version != projectionVersion
		if !same || reindex {
			prefix := make([]byte, min(256, int(info.Size())))
			_, _ = f.ReadAt(prefix, 0)
			generation := digest([]byte(statIdentity(info) + ":" + string(prefix) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10) + ":" + previous.Generation))
			if reindex {
				generation = previous.Generation
				prefix = previous.Prefix
			}
			current = sourceCheckpoint{Version: projectionVersion, Identity: statIdentity(info), Prefix: prefix, Generation: generation, Coverage: coverage("source:" + id)}
			if rebuilt {
				gap(&current.Coverage, "index_checkpoint_rebuilt")
			}
			if _, err := w.tx.Exec("UPDATE sources SET active=0 WHERE id=? AND active=1", id); err != nil {
				return err
			}
		}
		st.Generation = current.Generation
		current.Size = info.Size()
		current.Mtime = info.ModTime().UnixNano()
		source, err := w.source(id, src.Agent, current)
		if err != nil {
			return err
		}
		// Transient gaps are retried; malformed/unsupported complete records stay.
		persistent := current.Coverage
		persistent.Gaps = nil
		persistent.Complete = true
		for _, g := range current.Coverage.Gaps {
			if g != "partial_record" && g != "scan_cancelled" && g != "scan_size_limit" && g != "source_changed_during_index" {
				gap(&persistent, g)
			}
		}
		persistent.Gaps = append([]string{}, persistent.Gaps...)
		current.Coverage = persistent
		if current.Offset < info.Size() {
			if _, err := f.Seek(current.Offset, io.SeekStart); err != nil {
				return err
			}
			reader := bufio.NewReaderSize(f, 64*1024)
			scanned := int64(0)
			records := 0
			for current.Offset < info.Size() {
				if ctx.Err() != nil {
					gap(&current.Coverage, "scan_cancelled")
					break
				}
				if scanned >= maxBytes || (maxRecords > 0 && records >= maxRecords) {
					gap(&current.Coverage, "scan_size_limit")
					break
				}
				// A background time slice yields after a complete record. Source
				// setup may itself consume the slice; it must not starve a source
				// forever. Caller cancellation remains a separate hard boundary.
				if records > 0 && !deadline.IsZero() && !time.Now().Before(deadline) {
					gap(&current.Coverage, "scan_size_limit")
					break
				}
				body, length, oversized, readErr := boundedLine(ctx, reader)
				if readErr != nil {
					if ctx.Err() != nil {
						gap(&current.Coverage, "scan_cancelled")
						break
					}
					if length > 0 {
						gap(&current.Coverage, "partial_record")
					}
					if readErr != io.EOF {
						gap(&current.Coverage, "read_error")
					}
					break
				}
				offset := current.Offset
				current.Line++
				records++
				scanned += int64(length)
				current.Offset += int64(length)
				if oversized {
					gap(&current.Coverage, fmt.Sprintf("record_size_limit:L%d", current.Line))
					continue
				}
				current.AnchorOffset = offset
				current.AnchorLength = len(body)
				current.AnchorDigest = digest(body)
				decoded, omissions := shapeRecord(st, current.Line, offset, body, &current.WorkingDirectory)
				for _, omission := range omissions {
					gap(&current.Coverage, omission)
				}
				for _, e := range decoded {
					if err := func() error { _, err := w.event(source, e, false); return err }(); err != nil {
						return err
					}
					current.EventCount++
					updateSessionPreview(&current, e)

				}
			}
		}
		after, err := f.Stat()
		if err != nil {
			return err
		}
		if after.Size() != info.Size() || after.ModTime() != info.ModTime() {
			gap(&current.Coverage, "source_changed_during_index")
		}
		st.checkpoint = current
		_, err = w.source(id, src.Agent, current)
		return err
	})
	if err != nil {
		return nil, err
	}
	return st, nil
}

var legacyFixtureOracles sync.Map

func legacyOracleFor(t *testing.T, st *sourceState) *factStore {
	t.Helper()
	key := st.Path + ":" + st.Generation
	if prior, ok := legacyFixtureOracles.Load(key); ok {
		f := prior.(*factStore)
		cp, _, err := f.checkpoint(context.Background(), st.ID)
		if err != nil {
			t.Fatal(err)
		}
		if cp.Offset < st.checkpoint.Offset {
			_, err = f.legacyIndexQuantum(context.Background(), st.Source, 0, st.checkpoint.Offset-cp.Offset, time.Time{})
			if err != nil {
				t.Fatal(err)
			}
		}
		return f
	}
	f, err := openFactStore(filepath.Join(t.TempDir(), "oracle.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { legacyFixtureOracles.Delete(key); _ = f.db.Close() })
	seed := st.checkpoint
	// The frozen writer receives the public generation as input, while its
	// private physical identity retains the old mount-number representation.
	seed.Identity = statIdentity(st.Info)
	seed.Offset, seed.Line, seed.EventCount = 0, 0, 0
	seed.WorkingDirectory = ""
	seed.SessionTitle, seed.SessionNativeID, seed.LastAction = "", "", ""
	seed.NativeIDPresent = false
	seed.LastEvent = nil
	seed.Tools = nil
	seed.AnchorOffset, seed.AnchorLength, seed.AnchorDigest = 0, 0, ""
	seed.Coverage = coverage("source:" + st.ID)
	if err = f.write(context.Background(), func(w *factWriter) error { _, err := w.source(st.ID, st.Agent, seed); return err }); err != nil {
		t.Fatal(err)
	}
	old, err := f.legacyIndexQuantum(context.Background(), st.Source, 0, max(int64(1), st.checkpoint.Offset), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if old.Generation != st.Generation {
		t.Fatal("oracle identity changed")
	}
	legacyFixtureOracles.Store(key, f)
	return f
}
