package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"os"
	"time"
)

// Point reads validate the current authorized catalog, then prepare only the
// requested source. Opening a hit must not scan or backfill unrelated history.
// Full-catalog cache reconciliation remains owned by collection queries.
func (s *Service) collectSource(ctx context.Context, id string) (*sourceState, snapshot.TrajectoryCoverage, error) {
	set := s.provider(ctx)
	cov := coverage("source:" + id)
	mergeCoverage(&cov, set.Coverage)
	if ctx.Err() != nil {
		gap(&cov, "collection_cancelled")
		return nil, cov, ctx.Err()
	}
	var selected *Source
	for i := range set.Sources {
		if set.Sources[i].Decoder != nil && sourceID(set.Sources[i]) == id {
			selected = &set.Sources[i]
			break
		}
	}
	if selected == nil {
		return nil, cov, ErrNotFound
	}
	if err := s.openIndexContext(ctx); err != nil {
		gap(&cov, "index_unavailable")
		return nil, cov, err
	}
	st, needsIndex := s.cachedSource(*selected)
	// Existing references name committed evidence. A verified append retains
	// that prefix; opening it must not first backfill an unrelated new tail.
	if needsIndex && st == nil {
		prepareCtx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()
		var err error
		st, err = s.indexSource(prepareCtx, *selected)
		if err != nil {
			gap(&cov, "source_unreadable:"+id)
			if os.IsNotExist(err) {
				return nil, cov, ErrNotFound
			}
			return nil, cov, err
		}
	}
	if st != nil && (needsIndex || st.checkpoint.Offset < st.Info.Size()) {
		gap(&cov, "index_pending")
	}
	return st, cov, nil
}
