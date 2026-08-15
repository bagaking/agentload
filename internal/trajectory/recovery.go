package trajectory

import (
	"context"
	"errors"
	"slices"
	"time"
)

// PrepareCatalog applies current authorization and drains bounded deletion
// work even when a removed root has no source left in the recovery queue.
func (s *Service) PrepareCatalog(ctx context.Context, set SourceSet, enabled func() bool) (bool, error) {
	if err := s.lockOperation(ctx); err != nil {
		return false, err
	}
	defer s.opMu.Unlock()
	if !enabled() {
		return false, nil
	}
	if err := s.storageCheck(s.path); err != nil {
		return false, err
	}
	if err := s.openIndexContext(ctx); err != nil {
		if errors.Is(err, errStorageMigration) {
			return true, nil
		}
		return true, err
	}
	if !s.checkpointsReady {
		chunk, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		err := s.migrateCheckpoints(chunk)
		cancel()
		if err != nil || !s.checkpointsReady {
			return true, err
		}
	}
	_, cov := s.collectSetBudget(ctx, set, 0)
	if slices.Contains(cov.Gaps, "search_index_unavailable") || slices.Contains(cov.Gaps, "index_prune_failed") {
		return true, errors.New("archive catalog maintenance incomplete")
	}
	if s.search == nil {
		return slices.Contains(cov.Gaps, "index_storage_pending"), nil
	}
	return s.store.maintenancePending(ctx)
}

// PrepareSource is the same projection as Query's preparation, in a bounded
// background chunk. The application supplies its authorized adapter catalog
// and checks access again under the lifecycle lock. No second log parser exists.
// pending means actionable work: a half-written final record waits for a file
// notification, rather than spinning until the writer finishes it.
func (s *Service) PrepareSource(ctx context.Context, src Source, enabled func() bool) (pending bool, err error) {
	if err := s.lockOperation(ctx); err != nil {
		return false, err
	}
	defer s.opMu.Unlock()
	if !enabled() || src.Decoder == nil {
		return false, nil
	}
	if err = s.storageCheck(s.path); err != nil {
		return false, err
	}
	if err = s.openIndexContext(ctx); err != nil {
		if errors.Is(err, errStorageMigration) {
			return true, nil
		}
		return false, err
	}
	if !s.checkpointsReady {
		chunk, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
		err = s.migrateCheckpoints(chunk)
		cancel()
		if err != nil || !s.checkpointsReady {
			return err == nil, err
		}
	}
	st, needed := s.cachedSource(src)
	if needed {
		previousGeneration, previousOffset := "", int64(-1)
		if st != nil {
			previousGeneration, previousOffset = st.Generation, st.checkpoint.Offset
		}
		st, err = s.indexSourceQuantum(ctx, src, 256, 2*1024*1024, time.Now().Add(25*time.Millisecond))
		if err != nil {
			return false, err
		}
		if st.Generation != previousGeneration || st.checkpoint.Offset != previousOffset {
			s.NotifyEvidence()
		}
	}
	cov := coverage("background archive recovery")
	if err = s.syncSearchScope(ctx, []*sourceState{st}, &cov, false, 1); err != nil {
		if errors.Is(err, errStorageMigration) {
			return true, nil
		}
		return false, err
	}
	for _, g := range cov.Gaps {
		if g == "search_index_pending" {
			pending = true
		}
	}
	if st.checkpoint.Offset < st.Info.Size() {
		partial := false
		for _, g := range st.checkpoint.Coverage.Gaps {
			if g == "partial_record" {
				partial = true
			}
		}
		pending = pending || !partial
	}
	return pending, nil
}
