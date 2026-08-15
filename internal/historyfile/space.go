package historyfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

var ErrStorageBudget = errors.New("local indexing paused: insufficient disk space for safe indexing")

// Bulk derived indexes leave a fixed reserve for the app and other local work.
// Missing destination directories use the nearest existing parent filesystem.
func CheckStorageBudget(path string) error {
	return CheckStorageCapacity(path, 0)
}

// CheckStorageCapacity includes temporary maintenance files in addition to the
// regular reserve. Available space already excludes existing indexes, shadows
// and system swap; callers supply only the additional peak allocation.
func CheckStorageCapacity(path string, additional uint64) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	for {
		var stat unix.Statfs_t
		if err := unix.Statfs(dir, &stat); err == nil {
			available := uint64(stat.Bavail) * uint64(stat.Bsize)
			if additional > ^uint64(0)-(1<<30) || available < additional+(1<<30) {
				return fmt.Errorf("%w: peak additional %d bytes, available %d", ErrStorageBudget, additional, available)
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return os.ErrNotExist
		}
		dir = parent
	}
}
