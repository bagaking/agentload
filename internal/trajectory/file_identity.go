package trajectory

import (
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func validPersistentIdentity(identity string) bool {
	parts := strings.Split(identity, ":")
	if len(parts) != 3 || parts[0] != "volume-v1" || len(parts[1]) != 32 || parts[1] != strings.ToLower(parts[1]) {
		return false
	}
	volume, err := hex.DecodeString(parts[1])
	ino, inoErr := strconv.ParseUint(parts[2], 10, 64)
	return err == nil && string(volume) != string(make([]byte, 16)) && inoErr == nil && ino != 0
}

func validateCheckpointIdentity(cp sourceCheckpoint) error {
	_, legacy := parseLegacyFileIdentity(cp.Identity)
	if !legacy && !validPersistentIdentity(cp.Identity) {
		return errors.New("source checkpoint identity is invalid; evidence preserved")
	}
	if cp.Version != projectionVersion || cp.Generation == "" || cp.Offset < 0 || cp.Size < cp.Offset || cp.Line < 0 || cp.EventCount < 0 || len(cp.Prefix) > 256 || cp.AnchorOffset < 0 || cp.AnchorLength < 0 || int64(cp.AnchorLength) > cp.Offset-cp.AnchorOffset {
		return errors.New("source checkpoint boundary is invalid; evidence preserved")
	}
	return nil
}

func parseLegacyFileIdentity(identity string) (uint64, bool) {
	parts := strings.Split(identity, ":")
	if len(parts) != 2 {
		return 0, false
	}
	_, err := strconv.ParseInt(parts[0], 10, 64)
	ino, err2 := strconv.ParseUint(parts[1], 10, 64)
	return ino, err == nil && err2 == nil
}

func legacyFileIdentity(identity string, info os.FileInfo) bool {
	ino, valid := parseLegacyFileIdentity(identity)
	st, ok := info.Sys().(*syscall.Stat_t)
	return valid && ok && ino == uint64(st.Ino)
}

func persistentPathIdentity(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	identity, err := persistentFileIdentity(f, info)
	if err != nil {
		return "", 0, err
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(info, current) || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
		return "", 0, ErrStale
	}
	return identity + ":" + strconv.FormatInt(info.Size(), 10) + ":" + strconv.FormatInt(info.ModTime().UnixNano(), 10), info.Size(), nil
}
