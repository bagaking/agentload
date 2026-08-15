package trajectory

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ATTR_VOL_UUID is persistent across remounts. Device numbers and fsid are
// observations of a mount, and must not identify a durable checkpoint.
func persistentFileIdentity(file *os.File, info os.FileInfo) (string, error) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", errors.New("persistent file identity unavailable")
	}
	attrs := unix.Attrlist{Bitmapcount: 5, Volattr: unix.ATTR_VOL_INFO | unix.ATTR_VOL_UUID}
	var body [20]byte // native uint32 length followed by uuid_t
	_, _, errno := syscall.Syscall6(unix.SYS_FGETATTRLIST, file.Fd(), uintptr(unsafe.Pointer(&attrs)), uintptr(unsafe.Pointer(&body[0])), uintptr(len(body)), 0, 0)
	runtime.KeepAlive(file)
	if errno != 0 {
		return "", fmt.Errorf("persistent volume UUID: %w", errno)
	}
	if binary.NativeEndian.Uint32(body[:4]) != uint32(len(body)) || body == [20]byte{20, 0, 0, 0} {
		return "", errors.New("persistent volume UUID unavailable")
	}
	return fmt.Sprintf("volume-v1:%s:%d", hex.EncodeToString(body[4:]), st.Ino), nil
}
