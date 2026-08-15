package trajectory

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type migrationPairKey struct{ source, call string }
type migrationPairProof struct {
	f       *sourceStore
	changes map[migrationPairKey]*indexedPair
}

func (p *migrationPairProof) get(ctx context.Context, key migrationPairKey) (indexedPair, error) {
	if value, ok := p.changes[key]; ok {
		if value == nil {
			return indexedPair{}, nil
		}
		return *value, nil
	}
	var value indexedPair
	var body []byte
	err := p.f.db.QueryRowContext(ctx, "SELECT body FROM migration_pairs WHERE source=? AND call=?", key.source, key.call).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err == nil {
		err = json.Unmarshal(body, &value)
	}
	return value, err
}

// Accumulate independently reconstructed target witnesses during the single
// canonical traversal. A batch contains at most 64 entries and commits these
// bounded references and its cursor in one transaction.
func (p *migrationPairProof) event(ctx context.Context, e snapshot.TrajectoryEvent) error {
	if e.Tool == nil || e.Tool.CallID == "" || e.Kind != "tool_call" && e.Kind != "tool_result" {
		return nil
	}
	key := migrationPairKey{e.Source.ID, e.Tool.CallID}
	value, err := p.get(ctx, key)
	if err != nil {
		return err
	}
	if e.Kind == "tool_call" && len(value.Calls) < 2 {
		value.Calls = append(value.Calls, e.ID)
	}
	if e.Kind == "tool_result" && len(value.Results) < 2 {
		value.Results = append(value.Results, e.ID)
	}
	p.changes[key] = &value
	return nil
}

func (p *migrationPairProof) verify(ctx context.Context, entry storageEntry) error {
	key := migrationPairKey{string(entry.path[1]), string(entry.path[3])}
	actual, err := p.get(ctx, key)
	if err != nil {
		return err
	}
	var expected indexedPair
	if err = json.Unmarshal(entry.value, &expected); err != nil {
		return err
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(expected)
	if !bytes.Equal(a, b) {
		return errors.New("dual migration pairing ledger differs; originals preserved")
	}
	p.changes[key] = nil
	return nil
}

func (p *migrationPairProof) commit(ctx context.Context, tx *sql.Tx) error {
	for key, value := range p.changes {
		if value == nil {
			if _, err := tx.ExecContext(ctx, "DELETE FROM migration_pairs WHERE source=? AND call=?", key.source, key.call); err != nil {
				return err
			}
		} else {
			body, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, "INSERT INTO migration_pairs VALUES(?,?,?) ON CONFLICT(source,call) DO UPDATE SET body=excluded.body", key.source, key.call, body); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *migrationPairProof) previousSourcesEmpty(ctx context.Context, next string) error {
	rows, err := p.f.db.QueryContext(ctx, "SELECT source,call FROM migration_pairs WHERE source<>? LIMIT 65", next)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key migrationPairKey
		if err = rows.Scan(&key.source, &key.call); err != nil {
			return err
		}
		value, ok := p.changes[key]
		if !ok || value != nil {
			return errors.New("previous source has unmatched canonical pairs; originals preserved")
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for key, value := range p.changes {
		if key.source != next && value != nil {
			return errors.New("previous source has unmatched canonical pairs; originals preserved")
		}
	}
	return nil
}
