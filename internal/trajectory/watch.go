package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const WatchHistoryLimit = 256
const WatchResponseBytes = 64 * 1024
const WatchCursorTTL = 10 * time.Minute

var ErrAccess = errors.New("trajectory content access disabled")
var ErrClosed = errors.New("trajectory service closed")

type watchSource struct {
	generation string
	version    int
	offset     int64
	count      int
}
type watchState struct {
	mu               sync.Mutex
	changed          chan struct{}
	dirty            uint64
	secret           [32]byte
	epoch            string
	next             uint64
	entries          []snapshot.TrajectoryChange
	sources          map[string]watchSource
	initialized      bool
	closed           bool
	revision         string
	evidenceRevision uint64
	coverage         snapshot.TrajectoryCoverage
	resetReason      string
	now              func() time.Time
}

func newWatchState() *watchState {
	w := &watchState{changed: make(chan struct{}), sources: map[string]watchSource{}, now: time.Now}
	w.rotateLocked("watch_instance_changed")
	return w
}
func (w *watchState) wakeLocked() {
	close(w.changed)
	w.changed = make(chan struct{})
}
func (w *watchState) rotateLocked(reason string) {
	if _, err := rand.Read(w.secret[:]); err != nil {
		panic("cannot create trajectory watch cursor identity")
	}
	w.epoch = digest(w.secret[:])
	w.next = 0
	w.entries = nil
	w.resetReason = reason
	w.wakeLocked()
}

// NotifyEvidence is only a wake hint. The registry's existing watcher calls it
// outside its index lock; it never reads or indexes transcript content.
func (s *Service) NotifyEvidence() {
	w := s.watch
	w.mu.Lock()
	w.dirty++
	w.wakeLocked()
	w.mu.Unlock()
}

// The caller holds opMu. Closing/revoking wakes all idle requests and discards
// every cached source reference and cursor; no per-subscriber resource remains.
func (s *Service) resetWatchLocked(reason string) {
	w := s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rotateLocked(reason)
	w.sources = map[string]watchSource{}
	w.initialized = false
	w.revision = ""
	w.coverage = coverage("trajectory changes")
	if reason == "watch_closed" {
		w.closed = true
	}
}

// sourceRevision is shared by query and watch. Checkpoint progress and coverage
// matter even when a partially indexed source has unchanged size and mtime.
func sourceRevision(states []*sourceState) string {
	parts := make([]string, 0, len(states))
	for _, st := range states {
		cov, _ := json.Marshal(st.checkpoint.Coverage)
		parts = append(parts, fmt.Sprintf("%s:%s:%d:%d:%d:%d:%d:%s", st.ID, st.Generation, st.checkpoint.Version, st.checkpoint.Size, st.checkpoint.Mtime, st.checkpoint.Offset, st.checkpoint.EventCount, cov))
	}
	sort.Strings(parts)
	return digest([]byte(strings.Join(parts, "|")))
}

func copyWatchCoverage(c snapshot.TrajectoryCoverage) snapshot.TrajectoryCoverage {
	c.Gaps = append([]string{}, c.Gaps...)
	if c.Index != nil {
		index := *c.Index
		c.Index = &index
	}
	return c
}
func accessDisabled(c snapshot.TrajectoryCoverage) bool {
	for _, g := range c.Gaps {
		if g == "content_access_disabled" {
			return true
		}
	}
	return false
}

// Readiness changes affect query coverage, not continuity of observed source
// events. Keep all other gaps and preserve the actual public coverage unchanged.
func watchObservationCoverage(c snapshot.TrajectoryCoverage) snapshot.TrajectoryCoverage {
	c = copyWatchCoverage(c)
	c.Index = nil
	gaps := c.Gaps[:0]
	removed := false
	for _, g := range c.Gaps {
		if g == "index_pending" || g == "search_index_pending" {
			removed = true
			continue
		}
		gaps = append(gaps, g)
	}
	c.Gaps = gaps
	if removed && len(gaps) == 0 {
		c.Complete = true
	}
	return c
}

// observeLocked runs at the end of collect under opMu, so query's snapshot and
// its watch cursor share exactly one index boundary. Initial history is a
// baseline; subsequent observation reads only newly indexed event locators.
func (s *Service) observeLocked(states []*sourceState, cov snapshot.TrajectoryCoverage, evidenceRevision uint64) {
	w := s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	cov = copyWatchCoverage(cov)
	for _, st := range states {
		mergeCoverage(&cov, st.checkpoint.Coverage)
	}
	if accessDisabled(cov) {
		if w.initialized || len(w.entries) > 0 {
			w.rotateLocked("content_access_revoked")
		}
		w.sources = map[string]watchSource{}
		w.initialized = false
		w.revision = ""
		w.coverage = cov
		return
	}
	current := make(map[string]watchSource, len(states))
	for _, st := range states {
		current[st.ID] = watchSource{generation: st.Generation, version: st.checkpoint.Version, offset: st.checkpoint.Offset, count: st.checkpoint.EventCount}
	}
	reason := ""
	if w.initialized {
		for id, previous := range w.sources {
			next, exists := current[id]
			if !exists {
				reason = "watch_source_scope_changed"
				break
			}
			if next.generation != previous.generation || next.count < previous.count || next.offset < previous.offset {
				reason = "watch_source_generation_changed"
				break
			}
			if next.version != previous.version {
				reason = "watch_projection_changed"
				break
			}
		}
		// Progress counters change when an append is decoded or search catches up;
		// they do not create an observation gap. Source/generation checks above and
		// the remaining coverage fields own reset decisions.
		beforeCoverage, afterCoverage := watchObservationCoverage(w.coverage), watchObservationCoverage(cov)
		before, _ := json.Marshal(beforeCoverage)
		after, _ := json.Marshal(afterCoverage)
		if reason == "" && !cov.Complete && !bytes.Equal(before, after) {
			reason = "watch_observation_gap"
		}
	}
	if reason != "" {
		w.rotateLocked(reason)
		w.initialized = false
	}
	w.revision = sourceRevision(states)
	w.coverage = cov
	w.evidenceRevision = evidenceRevision
	if w.initialized {
		ordered := append([]*sourceState{}, states...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
		for _, st := range ordered {
			previous := w.sources[st.ID]
			if previous.generation != "" && previous.count == st.checkpoint.EventCount {
				continue
			}
			if err := s.appendWatchEventsLocked(st, previous, w); err != nil {
				gap(&w.coverage, "watch_index_unreadable")
				w.rotateLocked("watch_index_unreadable")
				break
			}
		}
	}
	w.sources = current
	w.initialized = true
}

func (s *Service) appendWatchEventsLocked(st *sourceState, previous watchSource, w *watchState) error {
	count := st.checkpoint.EventCount - previous.count
	if count <= 0 {
		return nil
	}
	changes := make([]snapshot.TrajectoryChange, 0, min(count, WatchHistoryLimit))
	offset := previous.offset
	if count > WatchHistoryLimit {
		offset = st.checkpoint.Offset
	}
	facts, err := s.store.take(context.Background(), st, offset, -1, count > WatchHistoryLimit, false, min(count, WatchHistoryLimit))
	for _, fact := range facts {
		e := fact.event
		ref := e.Source
		ref.NativeType = shorten(ref.NativeType, 120)
		changes = append(changes, snapshot.TrajectoryChange{Kind: "event_added", EventID: e.ID, EventKind: shorten(e.Kind, 80), SessionID: e.SessionID, Agent: shorten(st.Agent, 80), Revision: w.revision, Source: ref})
	}

	if err != nil {
		return err
	}
	if count > WatchHistoryLimit {
		for i, j := 0, len(changes)-1; i < j; i, j = i+1, j-1 {
			changes[i], changes[j] = changes[j], changes[i]
		}
		w.next += uint64(count - len(changes))
	}
	for _, change := range changes {
		w.next++
		change.Sequence = w.next
		w.entries = append(w.entries, change)
		if len(w.entries) > WatchHistoryLimit {
			w.entries = w.entries[1:]
		}
	}
	if len(changes) > 0 {
		w.wakeLocked()
	}
	return nil
}

func normalizeWatchSelector(q snapshot.TrajectorySelector) (snapshot.TrajectorySelector, error) {
	q.Count = false
	if q.Cursor != "" {
		return q, fmt.Errorf("%w: query cursor is not a watch cursor", ErrInvalid)
	}
	if err := ValidateSelector(&q); err != nil {
		return q, err
	}
	if q.Collection != "sessions" && q.Collection != "events" {
		return q, fmt.Errorf("%w: collection cannot be watched", ErrInvalid)
	}
	if q.RelationKind != "" || q.ContextID != "" || q.State != "" {
		return q, fmt.Errorf("%w: selector cannot be watched", ErrInvalid)
	}
	return q, nil
}
func watchSelectorID(q snapshot.TrajectorySelector) string {
	q.Limit, q.Cursor = 0, ""
	q.Text = strings.ToLower(strings.Join(strings.Fields(q.Text), " "))
	q.Tool = strings.ToLower(q.Tool)
	b, _ := json.Marshal(q)
	return digest(b)
}
func (w *watchState) cursorLocked(q snapshot.TrajectorySelector, sequence uint64) string {
	base := fmt.Sprintf("w1.%s.%s.%s.%s", w.epoch, strconv.FormatUint(sequence, 36), watchSelectorID(q), strconv.FormatInt(w.now().Unix(), 36))
	mac := hmac.New(sha256.New, w.secret[:])
	_, _ = mac.Write([]byte(base))
	return base + "." + hex.EncodeToString(mac.Sum(nil))[:24]
}

// Query calls this after consuming the same collect snapshot while holding
// opMu. A query pagination cursor is deliberately excluded from stream scope.
func (s *Service) watchCursorLocked(q snapshot.TrajectorySelector) string {
	q.Cursor = ""
	q, err := normalizeWatchSelector(q)
	if err != nil {
		return ""
	}
	w := s.watch
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || accessDisabled(w.coverage) {
		return ""
	}
	return w.cursorLocked(q, w.next)
}

func (w *watchState) parseCursorLocked(raw string, q snapshot.TrajectorySelector) (uint64, string, error) {
	parts := strings.Split(raw, ".")
	if len(parts) != 6 || parts[0] != "w1" || len(raw) > 256 {
		return 0, "", ErrInvalid
	}
	if parts[3] != watchSelectorID(q) {
		return 0, "", fmt.Errorf("%w: watch selector changed", ErrInvalid)
	}
	if parts[1] != w.epoch {
		return 0, w.resetReason, nil
	}
	mac := hmac.New(sha256.New, w.secret[:])
	_, _ = mac.Write([]byte(strings.Join(parts[:5], ".")))
	expected := hex.EncodeToString(mac.Sum(nil))[:24]
	if !hmac.Equal([]byte(expected), []byte(parts[5])) {
		return 0, "", ErrInvalid
	}
	sequence, err := strconv.ParseUint(parts[2], 36, 64)
	if err != nil || sequence > w.next {
		return 0, "", ErrInvalid
	}
	issued, err := strconv.ParseInt(parts[4], 36, 64)
	if err != nil {
		return 0, "", ErrInvalid
	}
	if age := w.now().Sub(time.Unix(issued, 0)); age > WatchCursorTTL || age < -time.Second {
		return 0, "watch_cursor_expired", nil
	}
	if len(w.entries) > 0 && sequence < w.entries[0].Sequence-1 {
		return 0, "watch_change_window_lost", nil
	}
	return sequence, "", nil
}

// Observe refreshes the authorized provider and shared index; it creates no
// daemon, scanner or polling loop. Registry notifications only wake Watch.
func (s *Service) Observe(ctx context.Context) error {
	if err := s.lockOperation(ctx); err != nil {
		return err
	}
	defer s.opMu.Unlock()
	s.watch.mu.Lock()
	closed := s.watch.closed
	s.watch.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, cov := s.collect(ctx)
	if accessDisabled(cov) {
		return ErrAccess
	}
	return ctx.Err()
}

func (s *Service) watchMatchLocked(states []*sourceState, change snapshot.TrajectoryChange, q snapshot.TrajectorySelector) (bool, error) {
	if q.Agent != "" && q.Agent != change.Agent || q.SessionID != "" && q.SessionID != change.SessionID {
		return false, nil
	}
	var st *sourceState
	for _, candidate := range states {
		if candidate.ID == change.Source.ID && candidate.Generation == change.Source.Generation {
			st = candidate
			break
		}
	}
	if st == nil {
		return false, ErrStale
	}
	e, err := s.store.event(context.Background(), st, change.Source.Offset, change.Source.Block)
	if err != nil {
		return false, err
	}
	if e.ID != change.EventID {
		return false, ErrStale
	}
	return matches(e, q) && MatchEntitySelector(e, q), nil
}

func watchResetResult(w *watchState, q snapshot.TrajectorySelector, reason string) snapshot.TrajectoryWatchResult {
	cov := copyWatchCoverage(w.coverage)
	gap(&cov, reason)
	return snapshot.TrajectoryWatchResult{Changes: []snapshot.TrajectoryChange{}, Cursor: w.cursorLocked(q, w.next), Revision: w.revision, Coverage: cov, ResetRequired: true}
}

// Watch is a bounded long poll. No subscription state belongs to a consumer,
// and reading or replaying a cursor never advances another consumer's position.
func (s *Service) Watch(ctx context.Context, p snapshot.TrajectoryWatchParams) (snapshot.TrajectoryWatchResult, error) {
	empty := snapshot.TrajectoryWatchResult{Changes: []snapshot.TrajectoryChange{}, Coverage: coverage("trajectory changes")}
	q, err := normalizeWatchSelector(p.Selector)
	if err != nil || p.TimeoutMS < 0 || p.TimeoutMS > 5000 {
		return empty, ErrInvalid
	}
	if p.TimeoutMS == 0 {
		p.TimeoutMS = 4000
	}
	deadline := time.Now().Add(time.Duration(p.TimeoutMS) * time.Millisecond)
	for {
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		w := s.watch
		w.mu.Lock()
		dirtyBefore := w.dirty
		closed := w.closed
		w.mu.Unlock()
		if closed {
			return empty, ErrClosed
		}
		if err := s.lockOperation(ctx); err != nil {
			return empty, err
		}
		w.mu.Lock()
		closed = w.closed
		w.mu.Unlock()
		if closed {
			s.opMu.Unlock()
			return empty, ErrClosed
		}
		states, _ := s.collect(ctx)
		if err := ctx.Err(); err != nil {
			s.opMu.Unlock()
			return empty, err
		}
		w.mu.Lock()
		out := snapshot.TrajectoryWatchResult{Changes: []snapshot.TrajectoryChange{}, Revision: w.revision, Coverage: copyWatchCoverage(w.coverage)}
		if w.closed || accessDisabled(w.coverage) {
			err := ErrAccess
			if w.closed {
				err = ErrClosed
			}
			w.mu.Unlock()
			s.opMu.Unlock()
			return empty, err
		}
		sequence, resetReason, cursorErr := uint64(0), "watch_baseline_required", error(nil)
		if p.Cursor != "" {
			sequence, resetReason, cursorErr = w.parseCursorLocked(p.Cursor, q)
		}
		if cursorErr != nil || resetReason != "" {
			if resetReason != "" {
				out = watchResetResult(w, q, resetReason)
			}
			w.mu.Unlock()
			s.opMu.Unlock()
			return out, cursorErr
		}
		for _, change := range w.entries {
			if change.Sequence <= sequence {
				continue
			}
			match, matchErr := s.watchMatchLocked(states, change, q)
			if matchErr != nil {
				out = watchResetResult(w, q, "watch_source_changed")
				break
			}
			if match {
				out.Changes = append(out.Changes, change)
				encoded, _ := json.Marshal(out)
				if len(encoded) > WatchResponseBytes-512 {
					out.Changes = out.Changes[:len(out.Changes)-1]
					out.More = true
					break
				}
			}
			sequence = change.Sequence
			if len(out.Changes) == q.Limit {
				out.More = sequence < w.next
				break
			}
		}
		if !out.ResetRequired {
			out.Cursor = w.cursorLocked(q, sequence)
		}
		wake, dirtyAfter := w.changed, w.dirty
		w.mu.Unlock()
		s.opMu.Unlock()
		if len(out.Changes) > 0 || out.ResetRequired || out.More {
			return out, nil
		}
		p.Cursor = out.Cursor
		if dirtyBefore != dirtyAfter {
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return out, nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return empty, ctx.Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
			// Recheck authorization and scope before an idle response is sent.
		}
	}
}
