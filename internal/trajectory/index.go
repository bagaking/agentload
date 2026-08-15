package trajectory

import (
	"agentload/internal/snapshot"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

var sourceBucket = []byte("sources-v1")
var eventBucket = []byte("events")
var pairBucket = []byte("pairs")
var metaKey = []byte("meta")
var checkpointBucket = []byte("checkpoints-v1")
var checkpointLayoutKey = []byte("_layout")
var checkpointMigrationKey = []byte("_after")

const projectionVersion = 7

type sourceCheckpoint struct {
	Version          int                         `json:"version"`
	Missing          bool                        `json:"missing,omitempty"`
	EventCount       int                         `json:"event_count"`
	SessionTitle     string                      `json:"session_title,omitempty"`
	SessionNativeID  string                      `json:"session_native_id,omitempty"`
	NativeIDPresent  bool                        `json:"native_id_present,omitempty"`
	LastAction       string                      `json:"last_action,omitempty"`
	LastEvent        *time.Time                  `json:"last_event,omitempty"`
	Tools            []string                    `json:"tools,omitempty"`
	WorkingDirectory string                      `json:"working_directory,omitempty"`
	Generation       string                      `json:"generation"`
	Identity         string                      `json:"identity"`
	PendingIdentity  string                      `json:"pending_identity,omitempty"`
	Prefix           []byte                      `json:"prefix"`
	Size             int64                       `json:"size"`
	Mtime            int64                       `json:"mtime"`
	Offset           int64                       `json:"offset"`
	Line             int                         `json:"line"`
	AnchorOffset     int64                       `json:"anchor_offset"`
	AnchorLength     int                         `json:"anchor_length"`
	AnchorDigest     string                      `json:"anchor_digest"`
	Coverage         snapshot.TrajectoryCoverage `json:"coverage"`
}
type indexedPair struct {
	Calls   []string `json:"calls"`
	Results []string `json:"results"`
}

// NewPersistent uses a private, rebuildable index. It opens only on a content
// request, after the application has checked its explicit access preference.
func NewPersistent(provider Provider, path string) *Service {
	s := New(provider)
	if filepath.Base(path) == "index.bbolt" {
		path = filepath.Join(filepath.Dir(path), "trajectory.sqlite")
	}
	s.path = path
	return s
}
func (s *Service) openIndex() error { return s.openIndexContext(context.Background()) }
func (s *Service) openIndexContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.store != nil {
		return nil
	}
	if s.path == "" {
		root, err := os.MkdirTemp("", "agentload-trajectory-")
		if err != nil {
			return err
		}
		s.path = filepath.Join(root, "trajectory.sqlite")
		s.temporary = true
	}
	if err := s.migrateSourceStore(ctx); err != nil {
		return err
	}
	store, err := openSourceStore(ctx, s.path)
	if err != nil {
		return err
	}
	s.store = store
	s.checkpointsReady = true
	s.storageReady = true
	return nil
}
func (s *Service) migrateCheckpoints(ctx context.Context) error { return ctx.Err() }
func (s *Service) closeStores() error {
	var result error
	if s.sourceMigration != nil {
		result = s.sourceMigration.closeInput()
		if s.sourceMigration.shadow != nil {
			if err := s.sourceMigration.shadow.db.Close(); err != nil {
				result = err
			}
		}
		s.sourceMigration = nil
	}
	s.closeSearch()
	if s.store != nil {
		if err := s.store.db.Close(); err != nil {
			result = err
		}
		s.store = nil
	}
	if s.annotationDB != nil {
		if err := s.annotationDB.Close(); err != nil {
			result = err
		}
		s.annotationDB = nil
	}
	s.checkpointsReady = false
	s.storageReady = false
	return result
}
func (s *Service) Close() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.resetWatchLocked("watch_closed")
	if err := s.closeStores(); err != nil {
		return err
	}
	if s.temporary && s.path != "" {
		if err := os.RemoveAll(filepath.Dir(s.path)); err != nil {
			return err
		}
		s.path = ""
	}
	return nil
}
func (s *Service) Reset() error {
	s.opMu.Lock()
	defer s.opMu.Unlock()
	s.resetWatchLocked("content_access_revoked")
	if err := s.closeStores(); err != nil {
		return err
	}
	// Revocation clears live readers/cursors. Lossless exceptions and recovery
	// state cannot be reconstructed after their original source disappears.
	return nil
}

func statIdentity(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return fmt.Sprintf("%d:%d", st.Dev, st.Ino)
	}
	return fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
}
func eventKey(offset int64, block int) []byte {
	k := make([]byte, 12)
	binary.BigEndian.PutUint64(k, uint64(offset))
	binary.BigEndian.PutUint32(k[8:], uint32(block))
	return k
}
func decodeIndexed(value []byte) (snapshot.TrajectoryEvent, error) {
	var e snapshot.TrajectoryEvent
	if len(value) == 0 {
		return e, ErrNotFound
	}
	if !currentEventEncoding(value) {
		return e, fmt.Errorf("index record invalid: unsupported physical event encoding")
	}
	e, err := decodeEventStored(value)
	if err != nil {
		return e, fmt.Errorf("index record invalid: %w", err)
	}
	return e, nil
}

func putEvent(bucket *bolt.Bucket, key []byte, e snapshot.TrajectoryEvent) error {
	value, err := encodeEventStored(e)
	if err != nil {
		return err
	}
	// Keys follow immutable source offsets and append in order. Dense leaf
	// splits avoid restoring half-empty event pages after archive compaction.
	// Mutable checkpoints and call-pair lookup buckets keep their own policy.
	bucket.FillPercent = 1
	return bucket.Put(key, value)
}
func putJSON(bucket *bolt.Bucket, key []byte, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put(key, b)
}

// cachedSource avoids rewriting unchanged archives on each search. Appends
// expose the committed prefix; replacements never reuse a previous generation.
func (s *Service) cachedSource(src Source) (*sourceState, bool) {
	return s.cachedSourceWithVolumes(src, nil)
}

func (s *Service) cachedSourceWithVolumes(src Source, volumes map[uint64]string) (*sourceState, bool) {
	id := sourceID(src)
	if previous, ok := s.checkpointSnapshot[id]; ok && !previous.Missing && previous.Version == projectionVersion && previous.Generation != "" {
		info, err := os.Stat(src.Path)
		if err == nil && info.Mode().IsRegular() {
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				prefix := volumes[uint64(stat.Dev)]
				if prefix != "" && previous.Identity == prefix+strconv.FormatUint(uint64(stat.Ino), 10) && info.Size() == previous.Size && info.ModTime().UnixNano() == previous.Mtime {
					return &sourceState{Source: src, ID: id, Generation: previous.Generation, Info: info, owner: s, checkpoint: previous}, previous.Offset < info.Size()
				}
			}
		}
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return nil, true
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, true
	}
	identity, err := persistentFileIdentity(f, info)
	if err != nil {
		return nil, true
	}
	var previous sourceCheckpoint
	if s.checkpointSnapshot != nil {
		var ok bool
		previous, ok = s.checkpointSnapshot[id]
		if !ok {
			err = ErrNotFound
		}
	} else {
		var ok bool
		previous, ok, err = s.store.checkpoint(context.Background(), id)
		if !ok && err == nil {
			err = ErrNotFound
		}
	}

	if err != nil || previous.Missing || previous.Version != projectionVersion || previous.Generation == "" || previous.Identity != identity || info.Size() < previous.Size {
		return nil, true
	}
	unchanged := info.Size() == previous.Size && info.ModTime().UnixNano() == previous.Mtime
	if !unchanged {
		if info.Size() == previous.Size {
			return nil, true
		}
		prefix := make([]byte, len(previous.Prefix))
		n, _ := f.ReadAt(prefix, 0)
		if n != len(prefix) || !bytes.Equal(prefix, previous.Prefix) {
			return nil, true
		}
		if previous.AnchorLength > 0 {
			anchor := make([]byte, previous.AnchorLength)
			n, _ = f.ReadAt(anchor, previous.AnchorOffset)
			if n != len(anchor) || digest(anchor) != previous.AnchorDigest {
				return nil, true
			}
		}
	}
	st := &sourceState{Source: src, ID: id, Generation: previous.Generation, Info: info, owner: s, checkpoint: previous}
	return st, !unchanged || previous.Offset < info.Size()
}

func updateSessionPreview(c *sourceCheckpoint, e snapshot.TrajectoryEvent) {
	if e.Kind == "session" && e.NativeID != "" {
		c.NativeIDPresent = true
		if len(e.NativeID) <= 256 {
			c.SessionNativeID = e.NativeID
		} else {
			c.SessionNativeID = ""
			gap(&c.Coverage, "native_id_omitted_for_size")
		}
	}
	if e.Timestamp != nil {
		c.LastEvent = e.Timestamp
	}
	if c.SessionTitle == "" && e.Role == "user" && e.Text != "" && !strings.HasPrefix(e.Text, "# AGENTS.md instructions") {
		c.SessionTitle = shorten(e.Text, 160)
	}
	if e.Tool == nil || e.Tool.Name == "" {
		return
	}
	if e.Kind == "tool_call" {
		c.LastAction = shorten(e.Tool.Name, 160)
	}
	for _, tool := range c.Tools {
		if tool == e.Tool.Name {
			return
		}
	}
	if len(e.Tool.Name) > 256 || len(c.Tools) >= 16 {
		gap(&c.Coverage, "session_tool_preview_limit")
		return
	}
	c.Tools = append(c.Tools, e.Tool.Name)
}

func (s *Service) indexSource(ctx context.Context, src Source) (*sourceState, error) {
	return s.indexSourceBatch(ctx, src, 0, maxScanBytes)
}

func (s *Service) indexSourceBatch(ctx context.Context, src Source, maxRecords int, maxBytes int64) (*sourceState, error) {
	return s.indexSourceQuantum(ctx, src, maxRecords, maxBytes, time.Time{})
}

func (s *Service) indexSourceQuantum(ctx context.Context, src Source, maxRecords int, maxBytes int64, deadline time.Time) (*sourceState, error) {
	if err := s.storageCheck(s.path); err != nil {
		return nil, err
	}
	st, err := s.store.indexSource(ctx, src, maxRecords, maxBytes, deadline)
	if st != nil {
		st.owner = s
	}
	return st, err
}

// ReadSlice bounds allocations even for a huge physical line. A partial line
// never advances the durable complete-record checkpoint.
func boundedLine(ctx context.Context, r *bufio.Reader) ([]byte, int, bool, error) {
	return boundedLineTo(ctx, r, nil)
}

func boundedLineTo(ctx context.Context, r *bufio.Reader, raw io.Writer) ([]byte, int, bool, error) {
	var body []byte
	length := 0
	oversized := false
	for {
		if ctx.Err() != nil {
			return body, length, oversized, ctx.Err()
		}
		part, err := r.ReadSlice('\n')
		if raw != nil {
			if _, writeErr := raw.Write(part); writeErr != nil {
				return body, length, oversized, writeErr
			}
		}
		length += len(part)
		if length > maxRecordBytes {
			oversized = true
			body = nil
		} else if !oversized {
			body = append(body, part...)
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return body, length, oversized, err
	}
}

// Consumers stage callback products and discard them unless the complete
// walk, including its final source observation, succeeds.
func scan(ctx context.Context, st *sourceState, visit func(snapshot.TrajectoryEvent) bool) (snapshot.TrajectoryCoverage, error) {
	cov := st.checkpoint.Coverage
	err := st.owner.store.walk(ctx, st, func(e snapshot.TrajectoryEvent, _ int) error {
		if !visit(e) {
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		gap(&cov, "index_read_incomplete")
		return cov, err
	}
	return cov, nil
}
func (s *Service) pruneSources(ctx context.Context, allowed map[string]bool) error {
	// Only allowed sources may be indexed after this operation's snapshot was
	// read. Absent sources remain unchanged under opMu until pruning completes.
	return s.store.prune(ctx, allowed, s.checkpointSnapshot)
}
func (s *Service) indexedWindow(ctx context.Context, st *sourceState, p snapshot.TrajectoryGetParams, offset int64, block int) ([]snapshot.TrajectoryEvent, string, string, error) {
	return s.store.window(ctx, st, p, offset, block)
}
