package historyfile

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestMaintenanceCapacityRejectsUnrepresentablePeak(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-yet-created", "index")
	if err := CheckStorageCapacity(path, ^uint64(0)); !errors.Is(err, ErrStorageBudget) {
		t.Fatal("peak capacity not rejected on destination filesystem", err)
	}
}
