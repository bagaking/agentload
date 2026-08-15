package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type sourceReadiness struct {
	generation    string
	complete      bool
	count         int
	offset        int64
	block         int
	entityGaps    int
	verifiedSize  int64
	verifiedMtime int64
}

// This narrower error concerns the raw file's read window. SQL range damage
// remains fatal and must never be converted into a successful empty page.
var errQuerySourceChanged = fmt.Errorf("%w: source changed during evidence read", ErrStale)

func querySourceReadError(st *sourceState, file *os.File, before os.FileInfo, err error) error {
	if !errors.Is(err, ErrStale) {
		return err
	}
	after, statErr := file.Stat()
	current, pathErr := os.Stat(st.Path)
	if statErr == nil && pathErr == nil && os.SameFile(before, after) && os.SameFile(after, current) && after.Size() > before.Size() && current.Size() >= after.Size() {
		return errQuerySourceChanged
	}
	return err
}

// Only the caller's current provider-authorized states are eligible. Database
// rows never grant access to a path or extend that current authorization set.
func (f *sourceStore) readiness(ctx context.Context) (map[string]sourceReadiness, error) {
	return f.readinessScope(ctx, "")
}

func (f *sourceStore) readinessScope(ctx context.Context, id string) (map[string]sourceReadiness, error) {
	query := "SELECT id,generation,complete,search_count,search_offset,search_block,search_entity_gaps,verified_size,verified_mtime FROM sources WHERE active=1 AND missing=0"
	var args []any
	if id != "" {
		query += " AND id=?"
		args = append(args, id)
	}
	rows, err := f.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]sourceReadiness{}
	for rows.Next() {
		var id string
		var p sourceReadiness
		if err = rows.Scan(&id, &p.generation, &p.complete, &p.count, &p.offset, &p.block, &p.entityGaps, &p.verifiedSize, &p.verifiedMtime); err != nil {
			return nil, err
		}
		result[id] = p
	}
	return result, rows.Err()
}

func (f *sourceStore) walkSourceCandidates(ctx context.Context, st *sourceState, candidate func(*sourceFilter) bool, ready *sourceReadiness, visit func(sourceFact) error) (result error) {
	return f.walkSourceCandidateRanges(ctx, st, candidate, ready, false, nil, visit)
}

func (f *sourceStore) walkSourceCandidateRanges(ctx context.Context, st *sourceState, candidate func(*sourceFilter) bool, ready *sourceReadiness, witnessOnly bool, nativeSelector *snapshot.TrajectorySelector, visit func(sourceFact) error) (result error) {
	if !witnessOnly || nativeSelector != nil && !nativeTextCandidate(*nativeSelector) {
		nativeSelector = nil
	}
	if err := f.validateRanges(ctx, st); err != nil {
		return err
	}
	source, before, err := openReplaySource(st)
	if err != nil {
		return err
	}
	defer source.Close()
	defer func() {
		if e := finishSourceOperation(ctx, st, source, before); e != nil {
			result = querySourceReadError(st, source, before, e)
		}
	}()
	if ready != nil && (!ready.complete || ready.generation != st.Generation || ready.count < 0 || ready.count > st.checkpoint.EventCount) {
		return ErrStale
	}
	sealed := false
	if candidate != nil {
		p := sourceReadiness{}
		if ready != nil {
			p = *ready
		} else {
			err = f.db.QueryRowContext(ctx, "SELECT verified_size,verified_mtime FROM sources WHERE id=? AND generation=? AND active=1 AND missing=0", st.ID, st.Generation).Scan(&p.verifiedSize, &p.verifiedMtime)
			if err != nil {
				return err
			}
		}
		sealed = p.verifiedSize == before.Size() && p.verifiedMtime == before.ModTime().UnixNano()
	}
	after := int64(-1)
	for {
		ranges, err := f.sourceRanges(ctx, st, after, 16)
		if err != nil {
			return err
		}
		if len(ranges) == 0 {
			return nil
		}
		for _, r := range ranges {
			after = r.value.Chunk.Start.Offset
			if ready != nil && (ready.count == 0 || after > ready.offset) {
				return nil
			}
			if candidate != nil && !candidate(r.filter) {
				if !sealed {
					if err := verifyReplayBytes(ctx, source, r.value.Chunk); err != nil {
						return err
					}
				}
				continue
			}
			if witnessOnly && r.value.Facts == r.value.Chunk.Events && r.value.Chunk.End.Offset-r.value.Chunk.Start.Offset <= replayChunkBytes+maxRecordBytes {
				// The replay reader verifies all bytes of an immutable snapshot
				// before shaping a record. A first exact witness needs no DTOs
				// after it; overrides require the complete canonical merge.
				exceptions, err := f.rangeExceptions(ctx, st, r.value.Chunk.Start.Offset, r.value.Chunk.End.Offset)
				if err != nil {
					return err
				}
				if len(exceptions) == 0 {
					_, err = replaySourceChunkSelected(ctx, st, r.value.Chunk, source, nativeSelector, func(e snapshot.TrajectoryEvent, n int) error {
						if ready != nil && physicalLess(ready.offset, ready.block, e.Source.Offset, e.Source.Block) {
							return io.EOF
						}
						return visit(sourceFact{e.Source.Offset, e.Source.Block, e, n})
					})
					if err != nil {
						return err
					}
					continue
				}
			}
			facts, err := f.readRangeFrom(ctx, st, r, source)
			if err != nil {
				return err
			}
			for _, fact := range facts {
				if err = ctx.Err(); err != nil {
					return err
				}
				if ready != nil && physicalLess(ready.offset, ready.block, fact.offset, fact.block) {
					return nil
				}
				if err = visit(fact); err != nil {
					return err
				}
			}
		}
	}
}

// query consumes exact regenerated canonical events. The bounded filter only
// chooses ranges to read; it cannot supply hits, totals or entity conjunctions.
func (f *sourceStore) query(ctx context.Context, q snapshot.TrajectorySelector, states []*sourceState, cov snapshot.TrajectoryCoverage) (snapshot.TrajectoryQueryResult, error) {
	out := snapshot.TrajectoryQueryResult{Sessions: []snapshot.TrajectorySession{}, Events: []snapshot.TrajectoryEvent{}, Coverage: cov, Revision: sourceRevision(states)}
	if err := ValidateSelector(&q); err != nil {
		return out, err
	}
	if q.ContextID != "" || q.ContextScope != "" {
		return out, ErrInvalid
	}
	if q.Collection == "sessions" && q.Limit > 20 {
		q.Limit = 20
	}
	readiness, err := f.readiness(ctx)
	if err != nil {
		return out, err
	}
	var revision uint64
	if err = f.db.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key='revision'").Scan(&revision); err != nil {
		return out, err
	}
	selector := q
	selector.Cursor, selector.Limit = "", 0
	encoded, _ := json.Marshal(selector)
	prefix := "search." + digest([]byte(fmt.Sprintf("%s:source%d:%d", out.Revision, sourceStoreVersion, revision))) + "." + digest(encoded)
	start := 0
	if q.Cursor != "" {
		parts := strings.Split(q.Cursor, ":")
		if len(parts) != 2 || parts[0] != prefix {
			return out, ErrStale
		}
		start, err = strconv.Atoi(parts[1])
		if err != nil || start < 0 {
			return out, ErrInvalid
		}
	}
	ordered := append([]*sourceState(nil), states...)
	sort.Slice(ordered, func(i, j int) bool {
		left, right := ordered[i].Info.ModTime().UnixNano(), ordered[j].Info.ModTime().UnixNano()
		if left != right {
			return left > right
		}
		return ordered[i].ID < ordered[j].ID
	})
	if out.Coverage.Index != nil {
		copy := *out.Coverage.Index
		copy.SearchableEvents, copy.SearchableSources = 0, 0
		out.Coverage.Index = &copy
	}
	for _, st := range states {
		p := readiness[st.ID]
		if st.Generation == "" || p.generation != st.Generation || !p.complete || p.count < 0 || p.count > st.checkpoint.EventCount {
			gap(&out.Coverage, "search_index_pending")
			gap(&out.Coverage, "index_pending")
			continue
		}
		if out.Coverage.Index != nil {
			out.Coverage.Index.SearchableEvents += p.count
			if p.count == st.checkpoint.EventCount && st.checkpoint.Offset == st.Info.Size() {
				out.Coverage.Index.SearchableSources++
			}
		}
		if p.count < st.checkpoint.EventCount {
			gap(&out.Coverage, "search_index_pending")
			gap(&out.Coverage, "index_pending")
		}
	}
	matched := 0
	var pageSources []*sourceState
	pageSessions := []snapshot.TrajectorySession{}
	if q.Collection == "sessions" {
		err = f.visitSessionWitnesses(ctx, q, ordered, readiness, func(st *sourceState, summary snapshot.TrajectorySession) error {
			mergeCoverage(&out.Coverage, summary.Coverage)
			p := readiness[st.ID]
			if p.generation == st.Generation && p.complete && p.count != 0 && p.entityGaps > 0 && (q.Skill != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != "") {
				gap(&out.Coverage, "entity_coverage_incomplete")
			}
			if len(summary.MatchedIDs) > 0 {
				if matched >= start && len(pageSources) < q.Limit {
					pageSessions = append(pageSessions, summary)
					pageSources = append(pageSources, st)
				}
				matched++
			}
			if !q.Count && matched > start+q.Limit {
				return io.EOF
			}
			return nil
		})
		if err != nil {
			return out, err
		}
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if q.Count {
			if err = f.completeSessionPage(ctx, q, pageSources, readiness, pageSessions); err != nil {
				return out, err
			}
			out.MatchedTotal = &matched
		}
		out.Sessions = pageSessions
		return out, boundQueryPage(&out, prefix, start, matched)
	}
	for _, st := range ordered {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if q.Agent != "" && q.Agent != st.Agent || q.SessionID != "" && q.SessionID != sessionID(st) {
			continue
		}
		mergeCoverage(&out.Coverage, st.checkpoint.Coverage)
		p := readiness[st.ID]
		if st.Generation == "" || p.generation != st.Generation || !p.complete || p.count == 0 {
			continue
		}
		if p.entityGaps > 0 && (q.Skill != "" || q.EntityKind != "" || q.EntityID != "" || q.Predicate != "") {
			gap(&out.Coverage, "entity_coverage_incomplete")
		}
		err = f.walkSourceCandidates(ctx, st, func(filter *sourceFilter) bool { return filter.maybe(q) }, &p, func(fact sourceFact) error {
			e := fact.event
			if !matches(e, q) {
				return nil
			}
			if matched >= start && len(out.Events) < q.Limit {
				preview := matchPreview(e, q)
				e = boundedEventPreview(e)
				e.Text = preview
				if e.Tool != nil && len(e.Tool.Arguments) > 512 {
					e.Tool.Arguments = nil
					e.Omissions = append(e.Omissions, "arguments_omitted")
				}
				out.Events = append(out.Events, e)
			}
			matched++
			if !q.Count && matched > start+q.Limit {
				return io.EOF
			}
			return nil
		})
		if err != nil && !errors.Is(err, io.EOF) {
			return out, err
		}
		if !q.Count && matched > start+q.Limit {
			break
		}
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if q.Count {
		out.MatchedTotal = &matched
	}
	return out, boundQueryPage(&out, prefix, start, matched)
}

// Keep four independent readers in flight, but consume witnesses in the exact
// session order. An error beyond the verified lookahead is not this page's
// evidence. Cancel and join all readers before returning, including on failure.
func (f *sourceStore) visitSessionWitnesses(ctx context.Context, q snapshot.TrajectorySelector, states []*sourceState, readiness map[string]sourceReadiness, visit func(*sourceState, snapshot.TrajectorySession) error) error {
	eligible := make([]*sourceState, 0, len(states))
	for _, st := range states {
		if (q.Agent == "" || q.Agent == st.Agent) && (q.SessionID == "" || q.SessionID == sessionID(st)) {
			eligible = append(eligible, st)
		}
	}
	type witness struct {
		summary snapshot.TrajectorySession
		err     error
	}
	readCtx, cancel := context.WithCancel(ctx)
	var readers sync.WaitGroup
	defer func() { cancel(); readers.Wait() }()
	const workers = 4
	var jobs [workers]chan int
	var pending [workers]chan witness
	for worker := 0; worker < min(workers, len(eligible)); worker++ {
		jobs[worker] = make(chan int, 1)
		pending[worker] = make(chan witness, 1)
		readers.Add(1)
		go func(worker int) {
			defer readers.Done()
			for {
				select {
				case <-readCtx.Done():
					return
				case i := <-jobs[worker]:
					st, p := eligible[i], readiness[eligible[i].ID]
					result := witness{summary: cachedSession(st)}
					if st.Generation != "" && p.generation == st.Generation && p.complete && p.count != 0 {
						result.err = f.walkSourceCandidateRanges(readCtx, st, func(filter *sourceFilter) bool { return filter.maybe(q) }, &p, !q.Count, &q, func(fact sourceFact) error {
							if !matches(fact.event, q) {
								return nil
							}
							result.summary.MatchedIDs = []string{fact.event.ID}
							result.summary.MatchedPreview = matchPreview(fact.event, q)
							return io.EOF
						})
						if errors.Is(result.err, io.EOF) {
							result.err = nil
						}
					}
					select {
					case <-readCtx.Done():
						return
					case pending[worker] <- result:
					}
				}
			}
		}(worker)
	}
	launch := func(i int) {
		if i < len(eligible) {
			jobs[i%workers] <- i
		}
	}
	for i := 0; i < min(workers, len(eligible)); i++ {
		launch(i)
	}
	for i, st := range eligible {
		var result witness
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result = <-pending[i%workers]:
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if result.err != nil {
			if q.Count || !errors.Is(result.err, errQuerySourceChanged) {
				return result.err
			}
			result.summary.MatchedIDs = []string{}
			result.summary.MatchedPreview = ""
			gap(&result.summary.Coverage, "source_changed_during_query:"+st.ID)
		}
		if err := visit(st, result.summary); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		launch(i + workers)
	}
	return ctx.Err()
}

// Only selected page members need full counts when explicitly requested.
func (f *sourceStore) completeSessionPage(ctx context.Context, q snapshot.TrajectorySelector, states []*sourceState, readiness map[string]sourceReadiness, sessions []snapshot.TrajectorySession) error {
	errs := make([]error, len(states))
	var readers sync.WaitGroup
	const workers = 4
	for worker := 0; worker < min(workers, len(states)); worker++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for i := worker; i < len(states) && ctx.Err() == nil; i += workers {
				st := states[i]
				p := readiness[st.ID]
				summary := cachedSession(st)
				count := 0
				err := f.walkSourceCandidates(ctx, st, func(filter *sourceFilter) bool { return filter.maybe(q) }, &p, func(fact sourceFact) error {
					if !matches(fact.event, q) {
						return nil
					}
					count++
					if len(summary.MatchedIDs) < 50 {
						summary.MatchedIDs = append(summary.MatchedIDs, fact.event.ID)
					}
					if count == 1 {
						summary.MatchedPreview = matchPreview(fact.event, q)
					}
					return nil
				})
				if err == nil && count == 0 {
					err = ErrStale
				}
				errs[i] = err
				if err == nil {
					summary.MatchedCount = &count
					if count > len(summary.MatchedIDs) {
						gap(&summary.Coverage, "matched_reference_limit")
					}
					sessions[i] = summary
				}
			}
		}()
	}
	readers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
