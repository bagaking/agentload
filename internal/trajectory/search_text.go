package trajectory

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
)

func storedTextMatches(ctx context.Context, value []byte, terms [][]byte, scratch []byte) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if len(value) > maxStoredValue {
		return false, errors.New("trajectory value exceeds storage bound")
	}
	if len(value) == 0 || value[0] != 0 {
		for _, term := range terms {
			if !bytes.Contains(value, term) {
				return false, nil
			}
		}
		return true, nil
	}
	if len(value) < 8 || !bytes.Equal(value[1:3], valueMagic[1:3]) || (value[3] != 0 && value[3] != 1 && value[3] != 4) {
		return false, errors.New("invalid trajectory search frame")
	}
	size := int(binary.BigEndian.Uint32(value[4:8]))
	if size > maxStoredValue {
		return false, errors.New("invalid trajectory storage length")
	}
	if value[3] == 0 {
		if len(value)-8 != size {
			return false, errors.New("trajectory storage length mismatch")
		}
		for _, term := range terms {
			if !bytes.Contains(value[8:], term) {
				return false, nil
			}
		}
		return true, nil
	}
	var dictionary []byte
	if value[3] == 4 {
		dictionary = storageDictionary
	}
	r, err := openStoredReader(value[8:], dictionary)
	if err != nil {
		return false, err
	}
	defer releaseStoredReader(r)
	seen := make([]bool, len(terms))
	overlap := 0
	for _, term := range terms {
		if len(term)-1 > overlap {
			overlap = len(term) - 1
		}
	}
	if overlap >= len(scratch) {
		return false, ErrInvalid
	}
	total, tail := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		n, e := r.reader.Read(scratch[tail:])
		total += n
		if total > size {
			return false, errors.New("trajectory storage length mismatch")
		}
		body := scratch[:tail+n]
		for i, term := range terms {
			if !seen[i] && bytes.Contains(body, term) {
				seen[i] = true
			}
		}
		tail = min(overlap, len(body))
		copy(scratch[:tail], body[len(body)-tail:])
		if e != nil {
			if e != io.EOF {
				return false, e
			}
			break
		}
	}
	if total != size {
		return false, errors.New("trajectory storage length mismatch")
	}
	for _, found := range seen {
		if !found {
			return false, nil
		}
	}
	return true, nil
}
