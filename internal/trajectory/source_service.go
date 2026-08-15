package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

func (f *sourceStore) checkpoints(ctx context.Context) (map[string]sourceCheckpoint, error) {
	rows, err := f.db.QueryContext(ctx, "SELECT id,generation,checkpoint,missing FROM sources WHERE active=1")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sourceCheckpoint{}
	for rows.Next() {
		var id, generation string
		var body []byte
		var missing bool
		if err = rows.Scan(&id, &generation, &body, &missing); err != nil {
			return nil, err
		}
		raw, err := decodeSourceValue(body, maxRecordBytes)
		if err != nil {
			return nil, err
		}
		var c sourceCheckpoint
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		if generation != c.Generation {
			return nil, ErrStale
		}
		if missing {
			c.Missing = true
		}
		out[id] = c
	}
	return out, rows.Err()
}

// Removal denies access without discarding the last identity/recovery evidence.
func (f *sourceStore) prune(ctx context.Context, allowed map[string]bool, checkpoints map[string]sourceCheckpoint) error {
	if checkpoints == nil {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var removed []string
	for id, c := range checkpoints {
		if !allowed[id] && !c.Missing {
			removed = append(removed, id)
		}
	}
	if len(removed) == 0 {
		return nil
	}
	return f.write(ctx, uint64(len(removed))*4096+128*1024, func(tx *sql.Tx) error {
		for _, id := range removed {
			c := checkpoints[id]
			c.Missing = true
			gap(&c.Coverage, "source_missing")
			raw, err := json.Marshal(c)
			if err != nil {
				return err
			}
			body, err := encodeSourceValue(raw)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "UPDATE sources SET missing=1,checkpoint=? WHERE id=? AND active=1", body, id); err != nil {
				return err
			}
		}
		return nil
	})
}

func (f *sourceStore) maintenancePending(ctx context.Context) (bool, error) {
	var pending bool
	err := f.db.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM sources WHERE active=1 AND missing=0 AND (complete=0 OR verified_mtime<>mtime))").Scan(&pending)
	return pending, err
}

func (s *Service) openSearch() error { return s.openSearchContext(context.Background()) }
func (s *Service) openSearchContext(ctx context.Context) error {
	if err := s.openIndexContext(ctx); err != nil {
		return err
	}
	if s.search == nil {
		s.search = &searchIndex{db: s.store.db, path: s.store.path}
	}
	return s.store.db.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key='revision'").Scan(&s.search.revision)
}
func (s *Service) closeSearch() error { s.search = nil; return nil }
func (s *Service) resetSearch() error { return s.closeSearch() }

func (f *sourceStore) advanceReadiness(ctx context.Context, st *sourceState, p sourceReadiness) (sourceReadiness, error) {
	if !p.complete || p.generation != st.Generation {
		return p, errStorageMigration
	}
	if p.count >= st.checkpoint.EventCount {
		return p, nil
	}
	facts, err := f.readinessFacts(ctx, st, p)
	if err != nil {
		return p, err
	}
	if len(facts) == 0 {
		return p, ErrStale
	}
	bytes := 0
	for i, fact := range facts {
		if i >= searchBatchEvents || bytes > 0 && bytes+fact.size > searchBatchBytes {
			break
		}
		bytes += fact.size
		p.count++
		p.offset, p.block = fact.offset, fact.block
		if fact.event.EntityCoverage != nil && !fact.event.EntityCoverage.Complete {
			p.entityGaps++
		}
	}
	if p.count > st.checkpoint.EventCount {
		return p, ErrStale
	}
	if err = ctx.Err(); err != nil {
		return p, err
	}
	// The read quantum bounds verification. Once verified, preserve this one
	// bounded frontier even if its deadline expires during the durable commit.
	commitCtx := context.WithoutCancel(ctx)
	err = f.write(commitCtx, 128*1024, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(commitCtx, "UPDATE sources SET search_count=?,search_offset=?,search_block=?,search_entity_gaps=? WHERE id=? AND generation=? AND active=1 AND missing=0", p.count, p.offset, p.block, p.entityGaps, st.ID, st.Generation)
		return err
	})
	return p, err
}

func (s *Service) syncSearch(ctx context.Context, states []*sourceState, cov *snapshot.TrajectoryCoverage) error {
	return s.syncSearchScope(ctx, states, cov, true, 1)
}
func (s *Service) syncSearchScope(ctx context.Context, states []*sourceState, cov *snapshot.TrajectoryCoverage, fullScope bool, maxBatches int) error {
	if err := s.openSearchContext(ctx); err != nil {
		return err
	}
	ready, err := s.store.readiness(ctx)
	if err != nil {
		return err
	}
	ordered := append([]*sourceState(nil), states...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })
	start := sort.Search(len(ordered), func(i int) bool { return ordered[i].ID > s.searchAfter })
	if maxBatches <= 0 {
		maxBatches = 1
	}
	budget, cancel := context.WithTimeout(ctx, 25*time.Millisecond)
	defer cancel()
	batches := 0
	for n := range ordered {
		st := ordered[(start+n)%len(ordered)]
		p := ready[st.ID]
		if batches >= maxBatches || budget.Err() != nil {
			break
		}
		if p.generation != st.Generation || !p.complete {
			continue
		}
		if p.count < st.checkpoint.EventCount {
			p, err = s.store.advanceReadiness(ctx, st, p)
			if err != nil && !(errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil) {
				return err
			}
			if err == nil {
				ready[st.ID] = p
			}
			batches++
		} else if p.verifiedSize != st.Info.Size() || p.verifiedMtime != st.Info.ModTime().UnixNano() {
			_, err = s.store.auditSource(budget, st)
			if err != nil && !(errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil) {
				return err
			}
			batches++
		}
		s.searchAfter = st.ID
	}
	if cov.Index != nil {
		cov.Index.SearchableEvents, cov.Index.SearchableSources = 0, 0
	}
	for _, st := range states {
		p := ready[st.ID]
		if p.generation != st.Generation || !p.complete || p.count < st.checkpoint.EventCount {
			gap(cov, "search_index_pending")
			gap(cov, "index_pending")
		}
		if cov.Index != nil && p.generation == st.Generation && p.complete {
			cov.Index.SearchableEvents += p.count
			if p.count == st.checkpoint.EventCount && st.checkpoint.Offset == st.Info.Size() {
				cov.Index.SearchableSources++
			}
		}
	}
	return s.store.db.QueryRowContext(ctx, "SELECT CAST(value AS INTEGER) FROM meta WHERE key='revision'").Scan(&s.search.revision)
}

func (s *Service) querySearch(ctx context.Context, q snapshot.TrajectorySelector, states []*sourceState, cov snapshot.TrajectoryCoverage) (snapshot.TrajectoryQueryResult, error) {
	out, err := s.store.query(ctx, q, states, cov)
	out.WatchCursor = s.watchCursorLocked(q)
	return out, err
}
