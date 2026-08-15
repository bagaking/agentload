package trajectory

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func oldDictionarylessFrame(t *testing.T, raw []byte, kind byte, logicalSize int) []byte {
	t.Helper()
	var body bytes.Buffer
	w, err := zlib.NewWriterLevel(&body, zlib.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(raw)
	if err = w.Close(); err != nil {
		t.Fatal(err)
	}
	header := 8
	if kind == 2 {
		header = 12
	}
	frame := make([]byte, header+body.Len())
	copy(frame, []byte{0, 'A', 'L', kind})
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(raw)))
	if kind == 2 {
		binary.BigEndian.PutUint32(frame[8:12], uint32(logicalSize))
	}
	copy(frame[header:], body.Bytes())
	return frame
}

func TestTrajectoryDictionaryVersionAndDictionaryIDAreImmutable(t *testing.T) {
	hash := sha256.Sum256([]byte(storageDictionaryV1))
	if hex.EncodeToString(hash[:]) != "d2463b04a197fb2233119cf9ce81885e5b2d47e456c10092551c84e8ddb59717" {
		t.Fatal("persisted dictionary changed; requires a new codec migration")
	}
	raw := []byte(strings.Repeat(`{"role":"assistant","kind":"tool_call","text":"中文词"}`, 25))
	frame, err := encodeStored(raw)
	if err != nil || frame[3] != 4 || storedLogicalSize(frame) != len(raw) {
		t.Fatal("dictionary text frame or budget invalid", err)
	}
	decoded, err := decodeStored(frame)
	if err != nil || !bytes.Equal(decoded, raw) {
		t.Fatal("dictionary byte roundtrip failed", err)
	}
	// The zlib stream must refuse a different dictionary even if its body and
	// declared allocation are otherwise well formed.
	r, err := zlib.NewReaderDict(bytes.NewReader(frame[8:]), []byte("another dictionary"))
	if r != nil {
		_ = r.Close()
	}
	if !errors.Is(err, zlib.ErrDictionary) {
		t.Fatal("wrong dictionary accepted", err)
	}
}

func TestTrajectoryDictionaryReaderReuseSurvivesCorruptAndPriorFrames(t *testing.T) {
	raw := []byte(strings.Repeat(`{"kind":"tool_call","text":"research 中文词"}`, 30))
	current, err := encodeStored(raw)
	if err != nil {
		t.Fatal(err)
	}
	prior := oldDictionarylessFrame(t, raw, 1, len(raw))
	corrupt := append([]byte(nil), current...)
	corrupt[len(corrupt)-1] ^= 1
	for i := 0; i < 12; i++ {
		if _, err := decodeStored(corrupt); err == nil {
			t.Fatal("damaged checksum accepted")
		}
		for _, frame := range [][]byte{prior, current} {
			got, err := decodeStored(frame)
			if err != nil || !bytes.Equal(got, raw) {
				t.Fatal("reader reuse retained the wrong dictionary or error", err)
			}
		}
	}
}
