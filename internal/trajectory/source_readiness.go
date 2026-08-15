package trajectory

import "context"

// A preparation unit finishes one independently verified range. The caller's
// deadline/cancellation applies throughout; the scheduler's soft quantum only
// decides whether another unit may start. Completed ranges and empty ranges
// are navigation metadata, never newly advertised facts.
func (f *sourceStore) readinessFacts(ctx context.Context, st *sourceState, p sourceReadiness) (facts []sourceFact, result error) {
	if err := f.validateRanges(ctx, st); err != nil {
		return nil, err
	}
	file, before, err := openReplaySource(st)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	defer func() {
		if err := finishSourceOperation(ctx, st, file, before); err != nil {
			facts, result = nil, querySourceReadError(st, file, before, err)
		}
	}()
	var start int64
	if err = f.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(r.start),0) FROM ranges r JOIN sources s ON s.rowid=r.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND r.start<=?", st.ID, st.Generation, p.offset).Scan(&start); err != nil {
		return nil, err
	}
	after := start - 1
	for {
		ranges, err := f.sourceRanges(ctx, st, after, 16)
		if err != nil {
			return nil, err
		}
		if len(ranges) == 0 {
			return nil, ErrStale
		}
		for _, r := range ranges {
			v := r.value
			after = v.Chunk.Start.Offset
			if v.EntityIncomplete < 0 || v.EntityIncomplete > v.Facts || v.LogicalBytes > maxSourceRangeLogicalBytes {
				return nil, ErrStale
			}
			if v.Facts == 0 {
				if v.First != nil || v.Last != nil {
					return nil, ErrStale
				}
				continue
			}
			first, last := v.First, v.Last
			if first == nil || last == nil || first.Offset < v.Chunk.Start.Offset || last.Offset >= v.Chunk.End.Offset || first.Block < 0 || last.Block < 0 || physicalLess(last.Offset, last.Block, first.Offset, first.Block) {
				return nil, ErrStale
			}
			if !physicalLess(p.offset, p.block, last.Offset, last.Block) {
				continue
			}
			part, err := f.readRangeFrom(ctx, st, r, file)
			if err != nil {
				return nil, err
			}
			for _, fact := range part {
				if physicalLess(p.offset, p.block, fact.offset, fact.block) {
					facts = append(facts, fact)
				}
			}
			if len(facts) == 0 {
				return nil, ErrStale
			}
			return facts, nil
		}
	}
}
