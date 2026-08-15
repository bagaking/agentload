package historyfile

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRuntimeOwnerReleasedAfterProcessExit(t *testing.T) {
	if os.Getenv("AGENTLOAD_OWNER_TEST_HELPER") == "1" {
		owner, err := TryAcquire(os.Getenv("AGENTLOAD_OWNER_TEST_PATH"))
		if err != nil {
			os.Exit(2)
		}
		defer owner.Release()
		fmt.Println("ready")
		_, _ = os.Stdin.Read(make([]byte, 1))
		return
	}
	path := filepath.Join(t.TempDir(), "history.owner")
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeOwnerReleasedAfterProcessExit$")
	cmd.Env = append(os.Environ(), "AGENTLOAD_OWNER_TEST_HELPER=1", "AGENTLOAD_OWNER_TEST_PATH="+path)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("owner process did not start: %q %v", line, err)
	}
	if owner, err := TryAcquire(path); !errors.Is(err, ErrLocked) {
		owner.Release()
		t.Fatal("second runtime was allowed to own the same history", err)
	}
	// Separate test histories remain independently usable.
	other, err := TryAcquire(path + ".other")
	if err != nil {
		t.Fatal(err)
	}
	other.Release()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	owner, err := TryAcquire(path)
	if err != nil {
		t.Fatal("crashed owner left a stale lock", err)
	}
	owner.Release()
	owner, err = TryAcquire(path)
	if err != nil {
		t.Fatal("normal shutdown did not release ownership", err)
	}
	owner.Release()
}
