package trajectory

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
)

// Blocks are immutable once their transaction commits. A point reference
// always names an exact extent. Normal blocks expand to at most 64 KiB; a
// larger single recorded value gets its own bounded block. There is no global
// private training dictionary and no whole-archive decompression.
const factBlockBytes = 64 * 1024

type factBlock struct {
	id  int64
	raw []byte
}

func (w *factWriter) appendBlock(kind int, raw []byte) (int64, int, error) {
	if kind < 0 || kind >= len(w.blocks) || len(raw) > maxStoredValue {
		return 0, 0, errors.New("invalid trajectory block")
	}
	block := w.blocks[kind]
	if block != nil && len(block.raw)+len(raw) > factBlockBytes {
		if err := w.flushBlock(kind); err != nil {
			return 0, 0, err
		}
		block = nil
	}
	if block == nil {
		result, err := w.tx.Exec("INSERT INTO blocks(kind,body) VALUES(?,?)", kind, []byte{})
		if err != nil {
			return 0, 0, err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return 0, 0, err
		}
		block = &factBlock{id: id}
		w.blocks[kind] = block
	}
	if _, err := w.tx.Exec("UPDATE blocks SET refs=refs+1 WHERE rowid=?", block.id); err != nil {
		return 0, 0, err
	}
	offset := len(block.raw)
	block.raw = append(block.raw, raw...)
	return block.id, offset, nil
}
func (w *factWriter) flushBlock(kind int) error {
	block := w.blocks[kind]
	if block == nil {
		return nil
	}
	body, err := encodeTextStored(string(block.raw))
	if err != nil {
		return err
	}
	if _, err = w.tx.Exec("UPDATE blocks SET body=? WHERE rowid=?", body, block.id); err != nil {
		return err
	}
	w.blocks[kind] = nil
	return nil
}
func (w *factWriter) flushBlocks() error {
	for i := range w.blocks {
		if err := w.flushBlock(i); err != nil {
			return err
		}
	}
	return nil
}
func factBlockSlice(ctx context.Context, body []byte, offset, length int) ([]byte, error) {
	return (*factBlockReadCache)(nil).slice(ctx, body, offset, length)
}

// A verification batch owns at most four normal blocks. The current compressed
// bytes are the key, so replacement or corruption cannot reuse old evidence.
// Ordinary point reads use a nil cache; oversized records are never retained.
type factBlockReadCache struct {
	entries [4]struct{ body, raw []byte }
	next    int
}

func (c *factBlockReadCache) slice(ctx context.Context, body []byte, offset, length int) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var raw []byte
	cacheable := c != nil && len(body) >= 8 && len(body) <= factBlockBytes+64 && binary.BigEndian.Uint32(body[4:8]) <= factBlockBytes
	if cacheable {
		for _, entry := range c.entries {
			if entry.body != nil && bytes.Equal(entry.body, body) {
				raw = entry.raw
				break
			}
		}
	}
	if raw == nil {
		var err error
		raw, err = decodeFactBlock(ctx, body)
		if err != nil {
			return nil, err
		}
		if cacheable {
			c.entries[c.next].body = bytes.Clone(body)
			c.entries[c.next].raw = raw
			c.next = (c.next + 1) % len(c.entries)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset > len(raw) || length > len(raw)-offset {
		return nil, errors.New("trajectory block extent mismatch")
	}
	return raw[offset : offset+length], nil
}

func decodeFactBlock(ctx context.Context, body []byte) ([]byte, error) {
	return decodeFactBlockInto(ctx, body, nil)
}

// A reader may reuse its own normal-block buffer after consuming the previous
// extent. Oversized records never replace that bounded reusable buffer.
func decodeFactBlockInto(ctx context.Context, body, buffer []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(body) < 8 || len(body) > maxStoredValue || body[0] != 0 || !bytes.Equal(body[1:3], valueMagic[1:3]) || (body[3] != 0 && body[3] != 4) {
		return nil, errors.New("invalid trajectory block frame")
	}
	size := int(binary.BigEndian.Uint32(body[4:8]))
	if size > maxStoredValue {
		return nil, errors.New("invalid trajectory block length")
	}
	if body[3] == 0 {
		if len(body)-8 != size {
			return nil, errors.New("trajectory block length mismatch")
		}
		return body[8:], nil
	}
	pooled, err := openStoredReader(body[8:], storageDictionary)
	if err != nil {
		return nil, err
	}
	defer releaseStoredReader(pooled)
	if size > factBlockBytes {
		// A declared oversized length is still untrusted. Grow only with bytes
		// actually read, keeping the prior malformed-frame memory boundary.
		var result bytes.Buffer
		result.Grow(factBlockBytes)
		scratch := make([]byte, 32*1024)
		for {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			n, e := pooled.reader.Read(scratch)
			if result.Len()+n > size {
				return nil, errors.New("trajectory block length mismatch")
			}
			result.Write(scratch[:n])
			if e != nil {
				if e != io.EOF {
					return nil, e
				}
				break
			}
		}
		if result.Len() != size {
			return nil, errors.New("trajectory block length mismatch")
		}
		return result.Bytes(), ctx.Err()
	}
	var result []byte
	if size <= factBlockBytes && cap(buffer) >= size {
		result = buffer[:size]
	} else {
		result = make([]byte, size)
	}
	for offset := 0; offset < size; {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		end := min(size, offset+32*1024)
		if _, err = io.ReadFull(pooled.reader, result[offset:end]); err != nil {
			return nil, err
		}
		offset = end
	}
	// Reading through EOF checks the compressed stream's complete checksum,
	// including when the final literal was already found in the first chunk.
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	var extra [1]byte
	n, err := pooled.reader.Read(extra[:])
	if err != io.EOF {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("trajectory block length mismatch")
	}
	if n != 0 {
		return nil, errors.New("trajectory block length mismatch")
	}
	return result, ctx.Err()
}
