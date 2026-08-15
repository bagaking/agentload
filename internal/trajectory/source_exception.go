package trajectory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	fastjson "github.com/goccy/go-json"
)

const sourceExceptionBlockBytes = 16 * 1024
const sourceExceptionBlockRecords = 64

// Public events are materialized at the boundary. Equal parent references are
// factored using the existing codec, then one source-local block is compressed.
// LogicalBytes measures the public event, not its factored physical body.
type storedSourceException struct {
	Offset       int64           `json:"offset"`
	Block        int             `json:"block"`
	LogicalBytes int             `json:"logical_bytes"`
	Event        json.RawMessage `json:"event"`
}

type sourceExceptionBlock struct {
	first, last sourcePosition
	records     int
	body        []byte
}

func exceptionPositionLess(a, b sourcePosition) bool {
	return a.Offset < b.Offset || a.Offset == b.Offset && a.Block < b.Block
}

func encodeSourceExceptionBlocks(facts []sourceException) ([]sourceExceptionBlock, error) {
	var blocks []sourceExceptionBlock
	records := make([]storedSourceException, 0, sourceExceptionBlockRecords)
	bytes, logical := 2, 0
	flush := func() error {
		if len(records) == 0 {
			return nil
		}
		raw, err := json.Marshal(records)
		if err != nil {
			return err
		}
		body, err := encodeSourceValueBounded(raw, maxSourceRangeLogicalBytes)
		if err != nil {
			return err
		}
		first, last := records[0], records[len(records)-1]
		blocks = append(blocks, sourceExceptionBlock{sourcePosition{first.Offset, first.Block}, sourcePosition{last.Offset, last.Block}, len(records), body})
		records = records[:0]
		bytes, logical = 2, 0
		return nil
	}
	var previous sourcePosition
	for i, e := range facts {
		position := sourcePosition{e.Offset, e.Block}
		if e.Offset < 0 || e.Block < 0 || i > 0 && !exceptionPositionLess(previous, position) {
			return nil, ErrStale
		}
		previous = position
		r := storedSourceException{Offset: e.Offset, Block: e.Block, Event: json.RawMessage("null")}
		var err error
		if e.Event != nil {
			r.Event, r.LogicalBytes, err = marshalEventStored(*e.Event)
			if err != nil {
				return nil, err
			}
		}
		raw, err := json.Marshal(r)
		if err != nil {
			return nil, err
		}
		if len(records) > 0 && (len(records) == sourceExceptionBlockRecords || bytes+len(raw)+1 > sourceExceptionBlockBytes || logical+r.LogicalBytes > maxSourceRangeLogicalBytes) {
			if err = flush(); err != nil {
				return nil, err
			}
		}
		records = append(records, r)
		bytes += len(raw) + 1
		logical += r.LogicalBytes
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return blocks, nil
}

func decodeSourceExceptionBlock(body []byte, first, last sourcePosition, count int) ([]sourceException, int, error) {
	if count < 1 || count > sourceExceptionBlockRecords {
		return nil, 0, ErrStale
	}
	raw, err := decodeSourceValue(body, maxSourceRangeLogicalBytes)
	if err != nil {
		return nil, 0, err
	}
	var records []storedSourceException
	if err = fastjson.Unmarshal(raw, &records); err != nil {
		return nil, 0, err
	}
	if len(records) != count {
		return nil, 0, ErrStale
	}
	facts := make([]sourceException, 0, len(records))
	logical := 0
	var previous sourcePosition
	for i, r := range records {
		position := sourcePosition{r.Offset, r.Block}
		if r.Offset < 0 || r.Block < 0 || i > 0 && !exceptionPositionLess(previous, position) || r.LogicalBytes < 0 || r.LogicalBytes > maxStoredValue {
			return nil, 0, ErrStale
		}
		previous = position
		fact := sourceException{Offset: r.Offset, Block: r.Block}
		if !bytes.Equal(r.Event, []byte("null")) {
			event, e := decodeEventRaw(r.Event)
			if e != nil {
				return nil, 0, e
			}
			expanded, e := json.Marshal(event)
			if e != nil {
				return nil, 0, e
			}
			if len(expanded) != r.LogicalBytes {
				return nil, 0, ErrStale
			}
			fact.Event = &event
		} else if r.LogicalBytes != 0 {
			return nil, 0, ErrStale
		}
		logical += r.LogicalBytes
		if logical > maxSourceRangeLogicalBytes {
			return nil, 0, errors.New("exception block exceeds expanded evidence budget; preserved")
		}
		facts = append(facts, fact)
	}
	if (sourcePosition{facts[0].Offset, facts[0].Block}) != first || previous != last {
		return nil, 0, ErrStale
	}
	return facts, logical, nil
}

func putSourceExceptionBlocks(tx *sql.Tx, source int64, blocks []sourceExceptionBlock) error {
	for _, b := range blocks {
		if _, err := tx.Exec("INSERT INTO exceptions(source,offset,block,body,end_offset,end_block,records) VALUES(?,?,?,?,?,?,?)", source, b.first.Offset, b.first.Block, b.body, b.last.Offset, b.last.Block, b.records); err != nil {
			return err
		}
	}
	return nil
}

func putSourceExceptions(tx *sql.Tx, source int64, facts []sourceException) error {
	ordered := append([]sourceException(nil), facts...)
	sort.Slice(ordered, func(i, j int) bool {
		return exceptionPositionLess(sourcePosition{ordered[i].Offset, ordered[i].Block}, sourcePosition{ordered[j].Offset, ordered[j].Block})
	})
	blocks, err := encodeSourceExceptionBlocks(ordered)
	if err != nil {
		return err
	}
	return putSourceExceptionBlocks(tx, source, blocks)
}
func putSourceException(tx *sql.Tx, source int64, e sourceException) error {
	return putSourceExceptions(tx, source, []sourceException{e})
}

type sourceExceptionReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func exceptionAt(ctx context.Context, db sourceExceptionReader, source, offset int64, block int) (sourceException, error) {
	var body []byte
	var first, last sourcePosition
	var count int
	err := db.QueryRowContext(ctx, "SELECT offset,block,end_offset,end_block,records,body FROM exceptions WHERE source=? AND (offset<? OR (offset=? AND block<=?)) ORDER BY offset DESC,block DESC LIMIT 1", source, offset, offset, block).Scan(&first.Offset, &first.Block, &last.Offset, &last.Block, &count, &body)
	if err != nil {
		return sourceException{}, err
	}
	facts, _, err := decodeSourceExceptionBlock(body, first, last, count)
	if err != nil {
		return sourceException{}, err
	}
	wanted := sourcePosition{offset, block}
	i := sort.Search(len(facts), func(i int) bool {
		return !exceptionPositionLess(sourcePosition{facts[i].Offset, facts[i].Block}, wanted)
	})
	if i == len(facts) || facts[i].Offset != offset || facts[i].Block != block {
		return sourceException{}, sql.ErrNoRows
	}
	return facts[i], nil
}

// Migration batches replay the same first and last logical slots after a
// crash. Discard only blocks wholly contained in that batch; an overlap would
// indicate broken progress and must preserve the original facts.
func replaceSourceExceptions(tx *sql.Tx, source int64, facts []sourceException) error {
	if len(facts) == 0 {
		return nil
	}
	first, last := facts[0], facts[len(facts)-1]
	var crosses bool
	err := tx.QueryRow("SELECT EXISTS(SELECT 1 FROM exceptions WHERE source=? AND (offset<? OR (offset=? AND block<=?)) AND (end_offset>? OR (end_offset=? AND end_block>=?)) AND ((offset<? OR (offset=? AND block<?)) OR (end_offset>? OR (end_offset=? AND end_block>?))))", source, last.Offset, last.Offset, last.Block, first.Offset, first.Offset, first.Block, first.Offset, first.Offset, first.Block, last.Offset, last.Offset, last.Block).Scan(&crosses)
	if err != nil {
		return err
	}
	if crosses {
		return ErrStale
	}
	if _, err = tx.Exec("DELETE FROM exceptions WHERE source=? AND (offset>? OR (offset=? AND block>=?)) AND (end_offset<? OR (end_offset=? AND end_block<=?))", source, first.Offset, first.Offset, first.Block, last.Offset, last.Offset, last.Block); err != nil {
		return err
	}
	return putSourceExceptions(tx, source, facts)
}
