package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
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

// Only the caller's current provider-authorized states are eligible. Database
// rows never grant access to a path or extend that current authorization set.
func (f *sourceStore) readiness(ctx context.Context) (map[string]sourceReadiness, error) {
	rows, err := f.db.QueryContext(ctx, "SELECT id,generation,complete,search_count,search_offset,search_block,search_entity_gaps,verified_size,verified_mtime FROM sources WHERE active=1 AND missing=0")
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
	source, before, err := openReplaySource(st)
	if err != nil {
		return err
	}
	defer source.Close()
	defer func() {
		if e := finishSourceOperation(ctx, st, source, before); e != nil {
			result = e
		}
	}()
	if err = f.validateRanges(ctx, st); err != nil {
		return err
	}
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
		summary := cachedSession(st)
		err = f.walkSourceCandidates(ctx, st, func(filter *sourceFilter) bool { return filter.maybe(q) }, &p, func(fact sourceFact) error {
			e := fact.event
			if !matches(e, q) {
				return nil
			}
			if q.Collection == "sessions" {
				summary.MatchedCount++
				if matched >= start && len(out.Sessions) < q.Limit {
					if len(summary.MatchedIDs) < 50 {
						summary.MatchedIDs = append(summary.MatchedIDs, e.ID)
					}
					if summary.MatchedCount == 1 {
						summary.MatchedPreview = matchPreview(e, q)
					}
				} else {
					// For an off-page source only an exact existence witness is
					// needed. readRange completed validation before this callback.
					return io.EOF
				}
			} else {
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
			}
			return nil
		})
		if err != nil && !errors.Is(err, io.EOF) {
			return out, err
		}
		if q.Collection == "sessions" && summary.MatchedCount > 0 {
			if matched >= start && len(out.Sessions) < q.Limit {
				if summary.MatchedCount > len(summary.MatchedIDs) {
					gap(&summary.Coverage, "matched_reference_limit")
				}
				out.Sessions = append(out.Sessions, summary)
			}
			matched++
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
