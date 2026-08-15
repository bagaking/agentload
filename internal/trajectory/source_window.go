package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"errors"
	"io"
	"math"
)

func (f *sourceStore) rangeStarts(ctx context.Context, st *sourceState, after int64, descending bool, limit int) ([]int64, error) {
	comparison, order := ">", "ASC"
	if descending {
		comparison, order = "<", "DESC"
	}
	rows, err := f.db.QueryContext(ctx, "SELECT r.start FROM ranges r JOIN sources s ON s.rowid=r.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND r.start"+comparison+"? ORDER BY r.start "+order+" LIMIT ?", st.ID, st.Generation, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	starts := []int64{}
	for rows.Next() {
		var start int64
		if err = rows.Scan(&start); err != nil {
			return nil, err
		}
		starts = append(starts, start)
	}
	return starts, rows.Err()
}

// take locates a bounded neighborhood by physical coordinates. A large
// historical session is not reconstructed merely to open its last few steps.
func (f *sourceStore) take(ctx context.Context, st *sourceState, offset int64, block int, descending, inclusive bool, limit int) (facts []sourceFact, result error) {
	facts = []sourceFact{}
	if limit < 1 || limit > WatchHistoryLimit+1 {
		return facts, ErrInvalid
	}
	source, before, err := openReplaySource(st)
	if err != nil {
		return nil, err
	}
	defer source.Close()
	defer func() {
		if e := finishSourceOperation(ctx, st, source, before); e != nil {
			facts = nil
			result = e
		}
	}()
	if err = f.validateRanges(ctx, st); err != nil {
		return nil, err
	}
	var start int64
	err = f.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(r.start),0) FROM ranges r JOIN sources s ON s.rowid=r.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND r.start<=?", st.ID, st.Generation, offset).Scan(&start)
	if err != nil {
		return nil, err
	}
	after := start - 1
	logical := 0
	if descending {
		after = start + 1
	}
	for {
		starts, err := f.rangeStarts(ctx, st, after, descending, 16)
		if err != nil {
			return nil, err
		}
		if len(starts) == 0 {
			return facts, nil
		}
		for _, start := range starts {
			after = start
			ranges, err := f.sourceRanges(ctx, st, start-1, 1)
			if err != nil {
				return nil, err
			}
			if len(ranges) != 1 || ranges[0].value.Chunk.Start.Offset != start {
				return nil, ErrStale
			}
			part, err := f.readRange(ctx, st, ranges[0])
			if err != nil {
				return nil, err
			}
			for n := range part {
				i := n
				if descending {
					i = len(part) - 1 - n
				}
				fact := part[i]
				eligible := physicalLess(offset, block, fact.offset, fact.block)
				if descending {
					eligible = physicalLess(fact.offset, fact.block, offset, block)
				}
				if inclusive && fact.offset == offset && fact.block == block {
					eligible = true
				}
				if !eligible {
					continue
				}
				logical += fact.size
				if logical > maxSourceRangeLogicalBytes {
					return nil, errors.New("source neighborhood exceeds bounded read budget")
				}
				facts = append(facts, fact)
				if len(facts) == limit {
					return facts, nil
				}
			}
		}
	}
}

func (f *sourceStore) pair(ctx context.Context, st *sourceState, call string) (indexedPair, error) {
	result := indexedPair{}
	if call == "" || len(call) > 512 {
		return result, nil
	}
	err := f.walkSourceCandidates(ctx, st, func(filter *sourceFilter) bool { return filter.maybeCall(call) }, nil, func(fact sourceFact) error {
		e := fact.event
		if e.Tool == nil || e.Tool.CallID != call {
			return nil
		}
		if e.Kind == "tool_call" && len(result.Calls) < 2 {
			result.Calls = append(result.Calls, e.ID)
		}
		if e.Kind == "tool_result" && len(result.Results) < 2 {
			result.Results = append(result.Results, e.ID)
		}
		if len(result.Calls) == 2 && len(result.Results) == 2 {
			return io.EOF
		}
		return nil
	})
	if errors.Is(err, io.EOF) {
		err = nil
	}
	if err != nil {
		return indexedPair{}, err
	}
	return result, nil
}

func (f *sourceStore) window(ctx context.Context, st *sourceState, p snapshot.TrajectoryGetParams, offset int64, block int) (events []snapshot.TrajectoryEvent, beforeID, afterID string, result error) {
	source, beforeInfo, err := openReplaySource(st)
	if err != nil {
		return nil, "", "", err
	}
	defer source.Close()
	defer func() {
		if e := finishSourceOperation(ctx, st, source, beforeInfo); e != nil {
			events = nil
			beforeID = ""
			afterID = ""
			result = e
		}
	}()
	if p.ID == "" || p.Around < 0 || p.Around > 20 {
		return nil, "", "", ErrInvalid
	}
	session := p.ID[0] == 's'
	var focus []sourceFact
	if session {
		focus, err = f.take(ctx, st, math.MaxInt64, math.MaxInt, true, true, 1)
	} else {
		focus, err = f.take(ctx, st, offset, block, false, true, 1)
	}
	if err != nil {
		return nil, "", "", err
	}
	if len(focus) == 0 {
		return nil, "", "", ErrNotFound
	}
	if !session && (focus[0].offset != offset || focus[0].block != block || focus[0].event.ID != p.ID) {
		return nil, "", "", ErrStale
	}
	n := p.Around
	if session {
		n *= 2
	}
	left, err := f.take(ctx, st, focus[0].offset, focus[0].block, true, false, n+1)
	if err != nil {
		return nil, "", "", err
	}
	before, after := "", ""
	if len(left) > n {
		before = left[n].event.ID
		left = left[:n]
	}
	for i, j := 0, len(left)-1; i < j; i, j = i+1, j-1 {
		left[i], left[j] = left[j], left[i]
	}
	facts := append(left, focus[0])
	if !session {
		right, err := f.take(ctx, st, focus[0].offset, focus[0].block, false, false, p.Around+1)
		if err != nil {
			return nil, "", "", err
		}
		if len(right) > p.Around {
			after = right[p.Around].event.ID
			right = right[:p.Around]
		}
		facts = append(facts, right...)
	}
	events = []snapshot.TrajectoryEvent{}
	for _, fact := range facts {
		e := fact.event
		if _, err = readRecord(st, e.Source); err != nil {
			return nil, "", "", err
		}
		if e.Tool != nil && e.Tool.CallID != "" {
			pair, err := f.pair(ctx, st, e.Tool.CallID)
			if err != nil {
				return nil, "", "", err
			}
			if len(pair.Calls) == 1 && len(pair.Results) == 1 {
				if e.Kind == "tool_call" {
					e.PairID = pair.Results[0]
				} else if e.Kind == "tool_result" {
					e.PairID = pair.Calls[0]
				}
			} else if len(pair.Calls) > 1 || len(pair.Results) > 1 {
				e.Omissions = append(e.Omissions, "ambiguous_call_id")
			}
		}
		events = append(events, e)
	}
	return events, before, after, nil
}
