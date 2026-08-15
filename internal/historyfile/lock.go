package historyfile

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Lock serializes all readers that may rewrite and all appenders for one
// history path. The separate lock file remains stable when the data file is
// atomically replaced.
type Lock struct {
	file *os.File
}

var ErrLocked = errors.New("history is already owned by another Agent Load instance")

func Acquire(path string) (*Lock, error) {
	return acquire(path, unix.LOCK_EX)
}

// TryAcquire reserves a runtime owner without waiting behind another process.
// The OS releases the lock on exit, including crashes; the file is not a PID
// receipt and must stay at a stable inode while other processes check it.
func TryAcquire(path string) (*Lock, error) {
	return acquire(path, unix.LOCK_EX|unix.LOCK_NB)
}

func acquire(path string, flags int) (*Lock, error) {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), flags); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrLocked
		}
		return nil, err
	}
	return &Lock{file: file}, nil
}

func (l *Lock) Release() {
	if l == nil || l.file == nil {
		return
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}
