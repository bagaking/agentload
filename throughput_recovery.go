package main

import (
	"agentload/internal/historyfile"
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	bolt "go.etcd.io/bbolt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"syscall"
	"time"
)

// Only native usage state is persisted here: no prompt, tool body, or cleaned
// trajectory content. This projection shares the registry inventory and native
// Usage decoder with live collection, and remains independent of content access.
type throughputRecovery struct {
	db       *bolt.DB
	store    *throughputHistoryStore
	adapters *codingAgentRegistry
	dirty    bool
}
type throughputReplayCheckpoint struct {
	Identity     string            `json:"identity"`
	PrefixLength int               `json:"prefix_length"`
	PrefixDigest [sha256.Size]byte `json:"prefix_digest"`
	Offset       int64             `json:"offset"`
	Size         int64             `json:"size"`
	Mtime        int64             `json:"mtime"`
	Anchor       [sha256.Size]byte `json:"anchor"`
	HasAnchor    bool              `json:"has_anchor"`
	Initialized  bool              `json:"initialized"`
	Total        int64             `json:"total"`
	At           time.Time         `json:"at"`
	Project      string            `json:"project"`
	Gaps         int               `json:"gaps"`
}

var replaySources = []byte("usage-sources-v2")
var replayMeta = []byte("meta")
var replayMinutes = []byte("minutes")
var replayMessages = []byte("messages")

func openThroughputRecovery(history string, store *throughputHistoryStore, adapters *codingAgentRegistry) (*throughputRecovery, error) {
	root := resolveHistoryFile(history) + ".throughput-index"
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(root, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "usage.bbolt")
	db, err := bolt.Open(path, 0600, &bolt.Options{Timeout: 200 * time.Millisecond})
	if err != nil {
		return nil, err
	}
	// The initial derived format retained prefix bytes for file identity.
	// Rebuild into a fresh file so those bytes also disappear from free pages.
	// Native sources remain the authority; history facts merge idempotently.
	legacy := false
	err = db.View(func(tx *bolt.Tx) error {
		legacy = tx.Bucket([]byte("usage-sources-v1")) != nil
		return nil
	})
	if err == nil && legacy {
		tmp := path + ".rebuild"
		if err = os.Remove(tmp); err != nil && !errors.Is(err, os.ErrNotExist) {
			db.Close()
			return nil, err
		}
		fresh, freshErr := bolt.Open(tmp, 0600, nil)
		if freshErr != nil {
			db.Close()
			return nil, freshErr
		}
		freshErr = fresh.Update(func(tx *bolt.Tx) error { _, e := tx.CreateBucket(replaySources); return e })
		closeErr := fresh.Close()
		if freshErr == nil {
			freshErr = closeErr
		}
		if freshErr != nil {
			db.Close()
			return nil, freshErr
		}
		if err = db.Close(); err != nil {
			return nil, err
		}
		if err = os.Rename(tmp, path); err != nil {
			return nil, err
		}
		db, err = bolt.Open(path, 0600, &bolt.Options{Timeout: 200 * time.Millisecond})
		if err != nil {
			return nil, err
		}
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	err = db.Update(func(tx *bolt.Tx) error { _, err := tx.CreateBucketIfNotExists(replaySources); return err })
	if err != nil {
		db.Close()
		return nil, err
	}
	return &throughputRecovery{db: db, store: store, adapters: adapters}, nil
}
func replayFileIdentity(info os.FileInfo) string {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Dev), 10) + ":" + strconv.FormatUint(st.Ino, 10)
	}
	return info.Name() + ":" + info.Mode().String()
}
func replayJSON(bucket *bolt.Bucket, key []byte, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return bucket.Put(key, b)
}

// One quantum advances only complete physical records. Native state, message
// maxima, minute partitions and checkpoint commit in one transaction; retries
// and process restarts cannot count an already committed usage update twice.
func (r *throughputRecovery) prepareSource(ctx context.Context, src trajectory.Source, now time.Time) (pending bool, err error) {
	if err = historyfile.CheckStorageBudget(r.db.Path()); err != nil {
		return false, err
	}
	decoder, supported := r.adapters.usageDecoder(src.Agent)
	if !supported {
		return false, nil
	}
	f, err := os.Open(src.Path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, nil
	}
	key := throughputSessionHash(liveTokenRateSessionKey(src.Agent, src.Path))
	advanced := false
	err = r.db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket(replaySources)
		b, err := root.CreateBucketIfNotExists([]byte(key))
		if err != nil {
			return err
		}
		var checkpoint throughputReplayCheckpoint
		if raw := b.Get(replayMeta); len(raw) > 0 {
			if err = json.Unmarshal(raw, &checkpoint); err != nil {
				checkpoint = throughputReplayCheckpoint{} // source-local rebuild
			}
		}
		same := checkpoint.Identity == replayFileIdentity(info) && info.Size() >= checkpoint.Size && checkpoint.PrefixLength >= 0 && checkpoint.PrefixLength <= 256
		if same {
			prefix := make([]byte, checkpoint.PrefixLength)
			n, _ := f.ReadAt(prefix, 0)
			same = n == len(prefix) && sha256.Sum256(prefix) == checkpoint.PrefixDigest
			if same && info.Size() == checkpoint.Size && info.ModTime().UnixNano() != checkpoint.Mtime {
				same = false
			}
			if same && checkpoint.HasAnchor {
				hash, ok := liveTokenRateBoundaryFingerprint(src.Path, checkpoint.Offset)
				same = ok && hash == checkpoint.Anchor
			}
		}
		if !same {
			advanced = true
			if err = root.DeleteBucket([]byte(key)); err != nil {
				return err
			}
			b, err = root.CreateBucket([]byte(key))
			if err != nil {
				return err
			}
			prefix := make([]byte, min(256, int(info.Size())))
			f.ReadAt(prefix, 0)
			checkpoint = throughputReplayCheckpoint{Identity: replayFileIdentity(info), PrefixLength: len(prefix), PrefixDigest: sha256.Sum256(prefix), Project: liveTokenRateProjectFromSessionKey(liveTokenRateSessionKey(src.Agent, src.Path))}
		}
		if checkpoint.Offset == info.Size() && checkpoint.Size == info.Size() && checkpoint.Mtime == info.ModTime().UnixNano() {
			return nil
		}
		checkpoint.Size, checkpoint.Mtime = info.Size(), info.ModTime().UnixNano()
		minutes, err := b.CreateBucketIfNotExists(replayMinutes)
		if err != nil {
			return err
		}
		messages, err := b.CreateBucketIfNotExists(replayMessages)
		if err != nil {
			return err
		}
		if _, err = f.Seek(checkpoint.Offset, io.SeekStart); err != nil {
			return err
		}
		reader := bufio.NewReaderSize(f, 64*1024)
		scanned := 0
		deadline := time.Now().Add(25 * time.Millisecond)
		for records := 0; checkpoint.Offset < info.Size() && records < 256 && scanned < 2*1024*1024; records++ {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if time.Now().After(deadline) {
				break
			}
			line, length, oversized, readErr := readReplayLine(reader)
			if readErr != nil {
				if !errors.Is(readErr, io.EOF) {
					return readErr
				}
				// A valid single JSON file is a complete record. JSONL tails wait for
				// newline; do not advance into a partially appended record.
				if checkpoint.Offset == 0 && info.Size() <= liveTokenRateBaselineReadLimit && json.Valid(bytes.TrimSpace(line)) {
					readErr = nil
				} else {
					pending = false
					break
				}
			}
			checkpoint.Offset += int64(length)
			advanced = true
			scanned += length
			if oversized {
				checkpoint.Gaps++
				continue
			}
			// Recorded cwd uses the existing project attribution rule, never the day
			// directory of an archive store. It is not inferred from cleaned text.
			if bytes.Contains(line, []byte(`"cwd"`)) {
				cwd := jsonNestedStringField(line, "payload", "cwd")
				if cwd == "" {
					cwd = jsonStringField(line, "cwd")
				}
				if cwd != "" {
					var trace snapshot.SessionTrace
					setTraceProjectPath(&trace, cwd, "transcript_cwd")
					if trace.Project != "" && trace.Project != "unknown" {
						checkpoint.Project = trace.Project
					}
				}
			}
			o, ok := decoder.DecodeUsage(line)
			if !ok {
				continue
			}
			if o.At.IsZero() || o.At.After(now.Add(liveTokenRateFutureSkew)) {
				checkpoint.Gaps++
				if o.Cumulative {
					checkpoint.Initialized = false
				}
				continue
			}
			previous, initialized := int64(0), false
			if o.Cumulative {
				previous, initialized = checkpoint.Total, checkpoint.Initialized
			} else if o.MessageIdentity != "" {
				if value := messages.Get([]byte(o.MessageIdentity)); len(value) == 8 {
					previous, initialized = int64(binary.BigEndian.Uint64(value)), true
				}
			}
			event := outputUsageEvent(o, previous, checkpoint.At, initialized, key)
			if o.Cumulative {
				checkpoint.Total, checkpoint.At, checkpoint.Initialized = o.OutputTokens, o.At, true
			} else if o.MessageIdentity != "" {
				var value [8]byte
				binary.BigEndian.PutUint64(value[:], uint64(max(previous, o.OutputTokens)))
				if err = messages.Put([]byte(o.MessageIdentity), value[:]); err != nil {
					return err
				}
			}
			project := checkpoint.Project
			if project == "" {
				project = liveTokenRateUnassignedProject
			}
			var writeErr error
			partitionOutputUsage(event, now.Add(-historyRetentionWindow), now, throughputMinuteResolution, func(start time.Time, tokens int64) {
				if writeErr != nil {
					return
				}
				end := start.Add(throughputMinuteResolution)
				minuteKey := []byte(end.UTC().Format(time.RFC3339))
				parts := map[string]int64{}
				if v := minutes.Get(minuteKey); len(v) > 0 {
					writeErr = json.Unmarshal(v, &parts)
					if writeErr != nil {
						return
					}
				}
				parts[project] = liveTokenRateSaturatingAdd(parts[project], tokens)
				writeErr = replayJSON(minutes, minuteKey, parts)
			})
			if writeErr != nil {
				return writeErr
			}
		}
		checkpoint.Anchor, checkpoint.HasAnchor = liveTokenRateBoundaryFingerprint(src.Path, checkpoint.Offset)
		// Remove expired minute contributions without decoding historical bodies.
		cutoff := []byte(now.Add(-historyRetentionWindow).UTC().Format(time.RFC3339))
		cursor := minutes.Cursor()
		for k, _ := cursor.First(); k != nil && bytes.Compare(k, cutoff) < 0; k, _ = cursor.Next() {
			if err = cursor.Delete(); err != nil {
				return err
			}
		}
		if err = replayJSON(b, replayMeta, checkpoint); err != nil {
			return err
		}
		// A half-line is not actionable until another file notification/audit.
		if checkpoint.Offset < info.Size() {
			completeEnd, e := liveTokenRateLastCompleteLineOffset(f, checkpoint.Offset, info.Size())
			pending = e == nil && completeEnd > checkpoint.Offset
		}
		return nil
	})
	if err == nil && advanced {
		r.dirty = true
	}
	return pending, err
}
func readReplayLine(reader *bufio.Reader) (body []byte, length int, oversized bool, err error) {
	for {
		part, e := reader.ReadSlice('\n')
		length += len(part)
		if length > liveTokenRateMaxJSONLineBytes {
			body = nil
			oversized = true
		} else if !oversized {
			body = append(body, part...)
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		return body, length, oversized, e
	}
}

// Materialize the ledger after a fair recovery pass. Facts remain lower bounds:
// logs establish positive output, not absence of every unrecorded producer.
func (r *throughputRecovery) publish(now time.Time) error {
	type aggregate struct {
		projects map[string]int64
		sessions map[string]map[string]bool
	}
	sums := map[string]*aggregate{}
	err := r.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(replaySources).ForEach(func(source, value []byte) error {
			if value != nil {
				return nil
			}
			b := tx.Bucket(replaySources).Bucket(source)
			minutes := b.Bucket(replayMinutes)
			if minutes == nil {
				return nil
			}
			return minutes.ForEach(func(at, raw []byte) error {
				parsed, e := time.Parse(time.RFC3339, string(at))
				if e != nil || (parsed.Before(now.Add(-historyRetentionWindow)) || parsed.After(now.Truncate(time.Minute))) {
					return nil
				}
				var parts map[string]int64
				if e = json.Unmarshal(raw, &parts); e != nil {
					return e
				}
				sum := sums[string(at)]
				if sum == nil {
					sum = &aggregate{map[string]int64{}, map[string]map[string]bool{}}
					sums[string(at)] = sum
				}
				for project, tokens := range parts {
					if tokens <= 0 {
						continue
					}
					sum.projects[project] = liveTokenRateSaturatingAdd(sum.projects[project], tokens)
					if sum.sessions[project] == nil {
						sum.sessions[project] = map[string]bool{}
					}
					sum.sessions[project][string(source)] = true
				}
				return nil
			})
		})
	})
	if err != nil {
		return err
	}
	facts := make([]ThroughputMinuteFact, 0, len(sums))
	for at, sum := range sums {
		total := int64(0)
		fact := ThroughputMinuteFact{At: at, State: liveTokenRateStateLive, Coverage: liveTokenRateCoveragePartial, Origin: "session_replay", Projects: []ThroughputMinuteProjectFact{}}
		all := map[string]bool{}
		for project, tokens := range sum.projects {
			total = liveTokenRateSaturatingAdd(total, tokens)
			part := ThroughputMinuteProjectFact{Project: project, OutputTokens: tokens}
			for source := range sum.sessions[project] {
				part.SessionHashes = append(part.SessionHashes, source)
				all[source] = true
			}
			sort.Strings(part.SessionHashes)
			fact.Projects = append(fact.Projects, part)
		}
		if total <= 0 {
			continue
		}
		fact.OutputTokens = &total
		for source := range all {
			fact.SessionHashes = append(fact.SessionHashes, source)
		}
		sort.Strings(fact.SessionHashes)
		sort.Slice(fact.Projects, func(i, j int) bool { return fact.Projects[i].Project < fact.Projects[j].Project })
		facts = append(facts, fact)
	}
	sort.Slice(facts, func(i, j int) bool { return facts[i].At < facts[j].At })
	err = r.store.mergeRecoveredMinutes(facts, now)
	if err == nil {
		r.dirty = false
	}
	return err
}
