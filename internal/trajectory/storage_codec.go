package trajectory

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

// Each value is independently readable: cold history needs no whole-archive
// inflate and a slice still seeks directly to its event. zlib supplies a
// checksum; the frame bounds allocation before decoding damaged content.
const maxStoredValue = 64 * 1024 * 1024

var valueMagic = []byte{0, 'A', 'L', 4}
var valueWriters = sync.Pool{New: func() any {
	w, _ := zlib.NewWriterLevelDict(io.Discard, zlib.DefaultCompression, storageDictionary)
	return w
}}

type storedValueReader struct {
	input  bytes.Reader
	reader io.ReadCloser
}

var valueReaders = sync.Pool{New: func() any { return new(storedValueReader) }}

func encodeStored(raw []byte) ([]byte, error) { return encodeStoredBounded(raw, maxStoredValue) }
func encodeStoredBounded(raw []byte, max int) ([]byte, error) {
	if len(raw) > max {
		return nil, errors.New("trajectory value exceeds storage bound")
	}
	// JSON is the current small-value encoding. Larger incompressible values
	// also stay verbatim. Text columns escape raw data in a type-0 frame.
	if len(raw) < 128 {
		return raw, nil
	}
	var out bytes.Buffer
	out.Write(valueMagic)
	_ = binary.Write(&out, binary.BigEndian, uint32(len(raw)))
	w := valueWriters.Get().(*zlib.Writer)
	w.Reset(&out)
	_, err := w.Write(raw)
	if closeErr := w.Close(); err == nil {
		err = closeErr
	}
	valueWriters.Put(w)
	if err != nil {
		return nil, err
	}
	if out.Len() >= len(raw) {
		return raw, nil
	}
	return out.Bytes(), nil
}

func decodeStored(value []byte) ([]byte, error) { return decodeStoredBounded(value, maxStoredValue) }
func decodeStoredBounded(value []byte, max int) ([]byte, error) {
	if len(value) > max+12 {
		return nil, errors.New("trajectory value exceeds storage bound")
	}
	if len(value) == 0 || value[0] != 0 {
		if len(value) > max {
			return nil, errors.New("trajectory value exceeds storage bound")
		}
		return value, nil
	}
	if len(value) < 8 || !bytes.Equal(value[1:3], valueMagic[1:3]) || value[3] > 5 {
		return nil, errors.New("invalid trajectory storage frame")
	}
	n := int(binary.BigEndian.Uint32(value[4:8]))
	if n > max {
		return nil, errors.New("invalid trajectory storage length")
	}
	start := 8
	if compactEventEncoding(value) {
		if len(value) < 12 || binary.BigEndian.Uint32(value[8:12]) > maxStoredValue {
			return nil, errors.New("invalid trajectory event length")
		}
		start = 12
	}
	if value[3] == 0 || value[3] == 3 {
		if len(value)-start != n {
			return nil, errors.New("trajectory storage length mismatch")
		}
		return value[start:], nil
	}
	var dictionary []byte
	if value[3] == 4 || value[3] == 5 {
		dictionary = storageDictionary
	}
	pooled, err := openStoredReader(value[start:], dictionary)
	if err != nil {
		return nil, err
	}
	defer releaseStoredReader(pooled)
	raw, err := io.ReadAll(io.LimitReader(pooled.reader, int64(n)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) != n {
		return nil, errors.New("trajectory storage length mismatch")
	}
	return raw, nil
}

func openStoredReader(body, dictionary []byte) (*storedValueReader, error) {
	pooled := valueReaders.Get().(*storedValueReader)
	pooled.input.Reset(body)
	var err error
	if pooled.reader == nil {
		pooled.reader, err = zlib.NewReaderDict(&pooled.input, dictionary)
	} else {
		err = pooled.reader.(zlib.Resetter).Reset(&pooled.input, dictionary)
	}
	if err != nil {
		releaseStoredReader(pooled)
		return nil, err
	}
	return pooled, nil
}

func releaseStoredReader(pooled *storedValueReader) {
	if pooled.reader != nil {
		_ = pooled.reader.Close()
	}
	pooled.input.Reset(nil)
	valueReaders.Put(pooled)
}

func encodeTextStored(text string) ([]byte, error) {
	return encodeTextStoredBounded(text, maxStoredValue)
}
func encodeTextStoredBounded(text string, max int) ([]byte, error) {
	raw := []byte(text)
	encoded, err := encodeStoredBounded(raw, max)
	if err != nil {
		return nil, err
	}
	if len(encoded) < len(raw) {
		return encoded, nil
	}
	framed := make([]byte, 8+len(raw))
	copy(framed, valueMagic)
	framed[3] = 0
	binary.BigEndian.PutUint32(framed[4:8], uint32(len(raw)))
	copy(framed[8:], raw)
	return framed, nil
}

func storedLogicalSize(value []byte) int {
	if len(value) >= 8 && value[0] == 0 && bytes.Equal(value[1:3], valueMagic[1:3]) {
		if compactEventEncoding(value) && len(value) >= 12 {
			return int(binary.BigEndian.Uint32(value[8:12]))
		}
		return int(binary.BigEndian.Uint32(value[4:8]))
	}
	return len(value)
}
