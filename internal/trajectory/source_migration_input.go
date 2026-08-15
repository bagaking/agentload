package trajectory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	bolt "go.etcd.io/bbolt"
)

// These readers exist only during a physical-format migration. Public service
// consumers use sourceStore exclusively; neither old DB grants authorization.
type migrationSource struct {
	row                           int64
	id, generation, agent         string
	active, missing, count, block int
	offset                        int64
	body                          []byte
}

func directLegacyIDs(r *factMigrationReader) ([]string, error) {
	ids := map[string]bool{}
	err := r.canonical.View(func(tx *bolt.Tx) error {
		if sources := tx.Bucket(sourceBucket); sources != nil {
			if err := sources.ForEach(func(k, v []byte) error {
				if v == nil {
					ids[string(k)] = true
				}
				return nil
			}); err != nil {
				return err
			}
		}
		if cps := tx.Bucket(checkpointBucket); cps != nil {
			return cps.ForEach(func(k, v []byte) error {
				if len(k) > 0 && k[0] != '_' && v != nil {
					ids[string(k)] = true
				}
				return nil
			})
		}
		return nil
	})
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, err
}

func (r *sourceMigrationRun) inputSource(ctx context.Context, row int64) (migrationSource, error) {
	v := migrationSource{}
	if r.legacy == nil {
		err := r.old.db.QueryRowContext(ctx, "SELECT s.rowid,s.id,s.generation,a.value,s.active,s.missing,s.checkpoint,s.search_count,s.search_offset,s.search_block FROM sources s JOIN symbols a ON a.id=s.agent WHERE s.rowid>=? ORDER BY s.rowid LIMIT 1", row).Scan(&v.row, &v.id, &v.generation, &v.agent, &v.active, &v.missing, &v.body, &v.count, &v.offset, &v.block)
		return v, err
	}
	if err := ctx.Err(); err != nil {
		return v, err
	}
	if row < 1 || row > int64(len(r.ids)) {
		return v, sql.ErrNoRows
	}
	v.row, v.id, v.active, v.offset, v.block = row, r.ids[row-1], 1, -1, -1
	err := r.legacy.canonical.View(func(tx *bolt.Tx) error {
		cp, err := r.legacy.checkpoint(tx, []byte(v.id))
		if err != nil {
			return err
		}
		if cp.Missing {
			v.missing = 1
		}
		v.generation = cp.Generation
		if b := tx.Bucket(checkpointBucket); b != nil {
			v.body = bytes.Clone(b.Get([]byte(v.id)))
		}
		if v.body == nil {
			if b := tx.Bucket(sourceBucket).Bucket([]byte(v.id)); b != nil {
				v.body = bytes.Clone(b.Get(metaKey))
			}
		}
		p := r.legacy.progress[v.id]
		v.agent = p.agent
		if r.legacy.eligible[v.id] {
			v.count, v.offset, v.block = p.count, p.offset, p.block
		}
		return nil
	})
	return v, err
}

func (r *sourceMigrationRun) inputRange(ctx context.Context, row int64, id string, start, end int64) ([]sourceFact, error) {
	if r.legacy == nil {
		return legacyRangeFacts(ctx, r.old, row, id, start, end)
	}
	return r.legacyFacts(ctx, id, start, end, -1, -1, 0)
}

func (r *sourceMigrationRun) inputBatch(ctx context.Context, row int64, id string, offset int64, block, limit int) ([]sourceFact, error) {
	if r.legacy != nil {
		return r.legacyFacts(ctx, id, 0, -1, offset, block, limit)
	}
	refs, err := r.old.references(ctx, "SELECT "+factRefColumns+" FROM events d JOIN sources s ON s.rowid=d.source WHERE s.rowid=? AND (d.offset,d.block)>(?,?) ORDER BY d.offset,d.block LIMIT ?", row, offset, block, limit)
	if err != nil {
		return nil, err
	}
	result := []sourceFact{}
	var cache factBlockReadCache
	size := 0
	for _, ref := range refs {
		e, err := r.old.readEventRow(ctx, id, ref.offset, ref.block, true, &cache, ref.row)
		if err != nil {
			return nil, err
		}
		size += ref.size
		if size > maxSourceRangeLogicalBytes {
			return nil, errors.New("migration batch exceeds bounded evidence budget; originals preserved")
		}
		result = append(result, sourceFact{ref.offset, ref.block, e, ref.size})
	}
	return result, nil
}

func (r *sourceMigrationRun) legacyFacts(ctx context.Context, id string, start, end, after int64, afterBlock, limit int) ([]sourceFact, error) {
	result := []sourceFact{}
	err := r.legacy.canonical.View(func(tx *bolt.Tx) error {
		cp, err := r.legacy.checkpoint(tx, []byte(id))
		if err != nil {
			return err
		}
		b := tx.Bucket(sourceBucket).Bucket([]byte(id))
		if b == nil || b.Bucket(eventBucket) == nil {
			return nil
		}
		c := b.Bucket(eventBucket).Cursor()
		k, v := c.Seek(eventKey(start, 0))
		if after >= 0 {
			k, v = c.Seek(eventKey(after, afterBlock))
			if bytes.Equal(k, eventKey(after, afterBlock)) {
				k, v = c.Next()
			}
		}
		size := 0
		for ; k != nil; k, v = c.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(k) != 12 || v == nil {
				return errors.New("unrecognized canonical physical key; originals preserved")
			}
			offset, block := int64(binary.BigEndian.Uint64(k[:8])), int(binary.BigEndian.Uint32(k[8:]))
			if offset < 0 {
				return ErrStale
			}
			if end >= 0 && offset >= end {
				break
			}
			e, err := migrationEvent(v)
			r.decodedFacts++
			if err != nil {
				return err
			}
			if e.Source.ID != id || e.Source.Generation != cp.Generation || e.Source.Offset != offset || e.Source.Block != block {
				return errors.New("canonical physical identity differs; originals preserved")
			}
			raw, err := json.Marshal(e)
			if err != nil {
				return err
			}
			size += len(raw)
			if size > maxSourceRangeLogicalBytes {
				return errors.New("migration range exceeds bounded evidence budget; originals preserved")
			}
			result = append(result, sourceFact{offset, block, e, len(raw)})
			if limit > 0 && len(result) >= limit {
				break
			}
		}
		return nil
	})
	return result, err
}

// until < 0 counts the entire physical source. Prefix counts are used only
// when switching a partially copied source to complete stored exceptions.
func (r *sourceMigrationRun) inputFactCount(ctx context.Context, row, until int64) (int64, error) {
	var count int64
	if r.legacy == nil {
		err := r.old.db.QueryRowContext(ctx, "SELECT count(*) FROM events WHERE source=? AND (?<0 OR offset<?)", row, until, until).Scan(&count)
		return count, err
	}
	if row < 1 || row > int64(len(r.ids)) {
		return 0, ErrStale
	}
	err := r.legacy.canonical.View(func(tx *bolt.Tx) error {
		b := tx.Bucket(sourceBucket).Bucket([]byte(r.ids[row-1]))
		if b == nil || b.Bucket(eventBucket) == nil {
			return nil
		}
		c := b.Bucket(eventBucket).Cursor()
		for k, v := c.First(); k != nil; k, v = c.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(k) != 12 || v == nil {
				return fmt.Errorf("invalid original physical key; originals preserved")
			}
			if until >= 0 && int64(binary.BigEndian.Uint64(k[:8])) >= until {
				break
			}
			count++
		}
		return nil
	})
	return count, err
}

func (r *sourceMigrationRun) closeInput() error {
	if r.legacy != nil {
		r.legacy.close()
		return nil
	}
	if r.old != nil {
		return r.old.db.Close()
	}
	return nil
}
