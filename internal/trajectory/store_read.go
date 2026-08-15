package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type factRef struct {
	row                        int64
	source, generation, digest string
	offset                     int64
	block, size                int
	override                   sql.NullString
}

func (r factRef) id() string {
	if r.override.Valid {
		return r.override.String
	}
	return sourceEventID(snapshot.TrajectorySourceRef{ID: r.source, Generation: r.generation, Offset: r.offset, Block: r.block, Digest: r.digest})
}

const factRefColumns = "d.rowid,s.id,s.generation,d.offset,d.block,d.digest,d.id_override,d.logical_size"

func (f *factStore) references(ctx context.Context, query string, args ...any) ([]factRef, error) {
	rows, err := f.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := []factRef{}
	for rows.Next() {
		var r factRef
		if err = rows.Scan(&r.row, &r.source, &r.generation, &r.offset, &r.block, &r.digest, &r.override, &r.size); err != nil {
			return nil, err
		}
		refs = append(refs, r)
	}
	return refs, rows.Err()
}
func (f *factStore) sourceReferences(ctx context.Context, st *sourceState, condition, order string, limit int, args ...any) ([]factRef, error) {
	values := []any{st.ID, st.Generation}
	values = append(values, args...)
	values = append(values, limit)
	return f.references(ctx, "SELECT "+factRefColumns+" FROM events d JOIN sources s ON s.rowid=d.source WHERE s.id=? AND s.generation=? AND s.active=1 AND s.missing=0 AND "+condition+" ORDER BY d.offset "+order+",d.block "+order+" LIMIT ?", values...)
}
func (f *factStore) walk(ctx context.Context, st *sourceState, visit func(snapshot.TrajectoryEvent, int) error) error {
	var offset int64 = -1
	block := -1
	for {
		refs, err := f.sourceReferences(ctx, st, "(d.offset,d.block)>(?,?)", "ASC", 128, offset, block)
		if err != nil {
			return err
		}
		if len(refs) == 0 {
			return nil
		}
		for _, r := range refs {
			if err = ctx.Err(); err != nil {
				return err
			}
			e, err := f.event(ctx, st.ID, r.offset, r.block)
			if err != nil {
				return err
			}
			if e.ID != r.id() || e.Source.Generation != st.Generation {
				return ErrStale
			}
			if err = visit(e, r.size); err != nil {
				return err
			}
			offset = r.offset
			block = r.block
		}
	}
}
func (f *factStore) pair(ctx context.Context, st *sourceState, call string) (indexedPair, error) {
	var result indexedPair
	var calls, results int
	var pointers [4]sql.NullInt64
	err := f.db.QueryRowContext(ctx, `SELECT p.call_count,p.result_count,p.call1,p.call2,p.result1,p.result2 FROM pairs p JOIN sources s ON s.rowid=p.source JOIN symbols c ON c.id=p.call WHERE s.id=? AND s.generation=? AND s.active=1 AND c.value=?`, st.ID, st.Generation, call).Scan(&calls, &results, &pointers[0], &pointers[1], &pointers[2], &pointers[3])
	if errors.Is(err, sql.ErrNoRows) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	for i, p := range pointers {
		count := calls
		if i >= 2 {
			count = results
		}
		if i%2 >= count {
			continue
		}
		if !p.Valid {
			return result, ErrStale
		}
		refs, err := f.references(ctx, "SELECT "+factRefColumns+" FROM events d JOIN sources s ON s.rowid=d.source WHERE d.rowid=? AND s.id=? AND s.generation=? AND s.active=1", p.Int64, st.ID, st.Generation)
		if err != nil {
			return result, err
		}
		if len(refs) != 1 {
			return result, ErrStale
		}
		if i < 2 {
			result.Calls = append(result.Calls, refs[0].id())
		} else {
			result.Results = append(result.Results, refs[0].id())
		}
	}
	return result, nil
}
func (f *factStore) window(ctx context.Context, st *sourceState, p snapshot.TrajectoryGetParams, offset int64, block int) ([]snapshot.TrajectoryEvent, string, string, error) {
	var focus factRef
	if p.ID[0] == 's' {
		refs, err := f.sourceReferences(ctx, st, "1=1", "DESC", 1)
		if err != nil {
			return nil, "", "", err
		}
		if len(refs) == 0 {
			return nil, "", "", ErrNotFound
		}
		focus = refs[0]
	} else {
		refs, err := f.sourceReferences(ctx, st, "d.offset=? AND d.block=?", "ASC", 1, offset, block)
		if err != nil {
			return nil, "", "", err
		}
		if len(refs) != 1 || refs[0].id() != p.ID {
			return nil, "", "", ErrStale
		}
		focus = refs[0]
	}
	n := p.Around
	if p.ID[0] == 's' {
		n *= 2
	}
	left, err := f.sourceReferences(ctx, st, "(d.offset,d.block)<(?,?)", "DESC", n+1, focus.offset, focus.block)
	if err != nil {
		return nil, "", "", err
	}
	before := ""
	if len(left) > n {
		before = left[n].id()
		left = left[:n]
	}
	for i, j := 0, len(left)-1; i < j; i, j = i+1, j-1 {
		left[i], left[j] = left[j], left[i]
	}
	refs := append(left, focus)
	after := ""
	if p.ID[0] != 's' {
		right, err := f.sourceReferences(ctx, st, "(d.offset,d.block)>(?,?)", "ASC", p.Around+1, focus.offset, focus.block)
		if err != nil {
			return nil, "", "", err
		}
		if len(right) > p.Around {
			after = right[p.Around].id()
			right = right[:p.Around]
		}
		refs = append(refs, right...)
	}
	events := []snapshot.TrajectoryEvent{}
	for _, r := range refs {
		e, err := f.event(ctx, st.ID, r.offset, r.block)
		if err != nil {
			return nil, "", "", err
		}
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

func (f *factStore) prune(ctx context.Context, allowed map[string]bool) error {
	rows, err := f.db.QueryContext(ctx, "SELECT id,checkpoint FROM sources WHERE active=1 AND missing=0")
	if err != nil {
		return err
	}
	type item struct {
		id         string
		checkpoint []byte
	}
	var remove []item
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.checkpoint); err != nil {
			rows.Close()
			return err
		}
		if !allowed[i.id] {
			remove = append(remove, i)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(remove) == 0 {
		return nil
	}
	return f.write(ctx, func(w *factWriter) error {
		for _, i := range remove {
			raw, err := decodeStored(i.checkpoint)
			if err != nil {
				return err
			}
			var previous sourceCheckpoint
			if err = json.Unmarshal(raw, &previous); err != nil {
				return err
			}
			c := sourceCheckpoint{Version: 1, Missing: true, Generation: previous.Generation, Coverage: coverage("source:" + i.id)}
			gap(&c.Coverage, "source_missing")
			body, err := json.Marshal(c)
			if err != nil {
				return err
			}
			body, err = encodeStored(body)
			if err != nil {
				return err
			}
			// The immutable retired row retains its generation and recovery evidence;
			// the current tombstone denies content immediately. Physical deletion is
			// bounded background work and never an authorization decision.
			if _, err = w.tx.Exec("UPDATE sources SET missing=1,checkpoint=? WHERE id=? AND active=1", body, i.id); err != nil {
				return err
			}
		}
		return nil
	})
}
func (f *factStore) reclaimSmall(ctx context.Context) (bool, error) {
	var n int
	if err := f.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT rowid FROM events WHERE source IN (SELECT rowid FROM sources WHERE active=0 OR missing=1) LIMIT 65)").Scan(&n); err != nil {
		return false, err
	}
	if n > 64 {
		return true, nil
	}
	return f.reclaim(ctx)
}

func (f *factStore) maintenancePending(ctx context.Context) (bool, error) {
	var pending bool
	err := f.db.QueryRowContext(ctx, `SELECT
 EXISTS(SELECT 1 FROM sources WHERE active=0)
 OR EXISTS(SELECT 1 FROM events WHERE source IN(SELECT rowid FROM sources WHERE missing=1))
 OR EXISTS(SELECT 1 FROM pairs WHERE source IN(SELECT rowid FROM sources WHERE missing=1))
 OR EXISTS(SELECT 1 FROM symbols WHERE refs=0 AND id<>0 AND NOT EXISTS(SELECT 1 FROM sources WHERE agent=symbols.id) AND NOT EXISTS(SELECT 1 FROM pairs WHERE call=symbols.id))`).Scan(&pending)
	return pending, err
}
func (f *factStore) reclaim(ctx context.Context) (bool, error) {
	pending, err := f.maintenancePending(ctx)
	if err != nil || !pending {
		return pending, err
	}
	rows, err := f.db.QueryContext(ctx, `SELECT d.rowid,d.logical_size,fb.rowid,length(fb.body),cb.rowid,length(cb.body)
 FROM events d JOIN event_facts ef ON ef.rowid=d.rowid JOIN blocks fb ON fb.rowid=ef.block LEFT JOIN contents c ON c.rowid=d.rowid LEFT JOIN blocks cb ON cb.rowid=c.block
 WHERE d.source IN(SELECT rowid FROM sources WHERE active=0 OR missing=1) LIMIT 64`)
	if err != nil {
		return false, err
	}
	var ids []int64
	logical, allocated := int64(0), int64(0)
	seen := map[int64]bool{}
	for rows.Next() {
		var id, size, fact, bytes int64
		var content, contentBytes sql.NullInt64
		if err = rows.Scan(&id, &size, &fact, &bytes, &content, &contentBytes); err != nil {
			break
		}
		added := int64(0)
		if !seen[fact] {
			added += bytes
		}
		if content.Valid && !seen[content.Int64] {
			added += contentBytes.Int64
		}
		if len(ids) > 0 && (logical+size > 256<<10 || allocated+added > 256<<10) {
			break
		}
		ids = append(ids, id)
		logical += size
		allocated += added
		seen[fact] = true
		if content.Valid {
			seen[content.Int64] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return false, err
	}
	err = f.write(ctx, func(w *factWriter) error {
		for _, id := range ids {
			if _, err := w.tx.Exec("DELETE FROM events WHERE rowid=?", id); err != nil {
				return err
			}
		}
		if _, err := w.tx.Exec("DELETE FROM pairs WHERE (source,call) IN(SELECT p.source,p.call FROM pairs p JOIN sources s ON s.rowid=p.source WHERE (s.active=0 OR s.missing=1) AND NOT EXISTS(SELECT 1 FROM events WHERE source=s.rowid) LIMIT 64)"); err != nil {
			return err
		}
		if _, err := w.tx.Exec("DELETE FROM sources WHERE rowid IN(SELECT rowid FROM sources WHERE active=0 AND NOT EXISTS(SELECT 1 FROM events WHERE source=sources.rowid) AND NOT EXISTS(SELECT 1 FROM pairs WHERE source=sources.rowid) LIMIT 64)"); err != nil {
			return err
		}
		_, err := w.tx.Exec("DELETE FROM symbols WHERE id IN(SELECT id FROM symbols WHERE refs=0 AND id<>0 AND NOT EXISTS(SELECT 1 FROM sources WHERE agent=symbols.id) AND NOT EXISTS(SELECT 1 FROM pairs WHERE call=symbols.id) LIMIT 256)")
		return err
	})
	if err != nil {
		return false, err
	}
	return f.maintenancePending(ctx)
}
