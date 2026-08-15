package main

import (
	"agentload/internal/snapshot"
	"agentload/internal/trajectory"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func trajectoryWatchFixture(t *testing.T) (*trayApp, trajectoryInstance, string, string) {
	t.Helper()
	app, server, instancePath := trajectoryTestApp(t)
	if err := app.trajectoryAccess.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.trajectory.Close(); app.observer.evidenceIndex.stopIndex() })
	sources := app.trajectorySources(context.Background())
	if len(sources.Sources) != 1 {
		t.Fatalf("fixture sources: %+v", sources)
	}
	// A resumed cursor should observe the later append only. Foreground Query
	// deliberately prepares a short quantum, which is not a fixture barrier.
	prepareTrajectoryAppFixture(t, app)
	ctx, cancel := context.WithCancel(context.Background())
	app.startArchiveRecovery(ctx)
	t.Cleanup(func() {
		cancel()
		<-app.archiveDone
	})
	return app, trajectoryInstance{Endpoint: server.URL, Token: app.trajectoryAccess.token}, instancePath, sources.Sources[0].Path
}

func trajectoryWatchRecord(t *testing.T) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "type": "response_item", "payload": map[string]any{
		"type": "function_call", "id": "native-watch-call", "call_id": "watch-call", "name": "exec_command", "arguments": `{"cmd":"watch private sentinel"}`,
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(data) + "\n"
}

func trajectoryWatchNotifyAppend(t *testing.T, app *trayApp, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(trajectoryWatchRecord(t))
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("append: %v %v", err, closeErr)
	}
	// Exercise the existing registry watcher publication path, including its
	// subscription callback. No trajectory-specific scanner is invoked here.
	app.observer.evidenceIndex.recordWatchBatch(evidenceWatchBatch{Complete: true, Paths: []string{path}})
}

func TestTrajectoryWatchRegistryCallbackAndRPC(t *testing.T) {
	app, instance, _, path := trajectoryWatchFixture(t)
	q := snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"}
	baseline, err := app.trajectory.Query(context.Background(), q)
	if err != nil || baseline.WatchCursor == "" {
		t.Fatalf("query baseline: %+v %v", baseline, err)
	}
	type receipt struct {
		body json.RawMessage
		err  error
	}
	done := make(chan receipt, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() {
		body, err := trajectoryRPC(ctx, instance, "traj.watch", snapshot.TrajectoryWatchParams{Selector: q, Cursor: baseline.WatchCursor, TimeoutMS: 1500})
		done <- receipt{body, err}
	}()
	time.Sleep(30 * time.Millisecond)
	trajectoryWatchNotifyAppend(t, app, path)
	select {
	case result := <-done:
		var watched snapshot.TrajectoryWatchResult
		if result.err != nil || json.Unmarshal(result.body, &watched) != nil || len(watched.Changes) != 1 || watched.ResetRequired {
			t.Fatalf("registry callback/RPC lost change: %s %v", result.body, result.err)
		}
		query, err := app.trajectory.Query(context.Background(), q)
		if err != nil || len(query.Events) != 2 || watched.Changes[0].EventID != query.Events[1].ID {
			t.Fatalf("RPC notification differs from shared query: %+v %+v %v", watched, query, err)
		}
		if len(result.body) > trajectory.WatchResponseBytes || strings.Contains(string(result.body), "watch private sentinel") {
			t.Fatal("RPC watch exposed body or exceeded bound")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("existing watcher callback did not wake RPC long poll")
	}
}

func TestTrajectoryCLIWatchNDJSONUsesSharedCursor(t *testing.T) {
	app, instance, instancePath, path := trajectoryWatchFixture(t)
	q := snapshot.TrajectorySelector{Collection: "events", Tool: "exec_command"}
	baseline, err := app.trajectory.Query(context.Background(), q)
	if err != nil || baseline.WatchCursor == "" {
		t.Fatalf("query baseline: %+v %v", baseline, err)
	}
	trajectoryWatchNotifyAppend(t, app, path)
	var stdout, stderr bytes.Buffer
	args := []string{"watch", "events", "--tool", "exec_command", "--cursor", baseline.WatchCursor, "--once", "--timeout-ms", "1000", "--format", "ndjson", "--instance-file", instancePath}
	if code := runTrajectoryCLIContext(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatalf("CLI watch failed: %d %s", code, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	var cli snapshot.TrajectoryWatchResult
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &cli) != nil || len(cli.Changes) != 1 {
		t.Fatalf("NDJSON did not retain bounded domain batch: %s", stdout.String())
	}
	body, err := trajectoryRPC(context.Background(), instance, "traj.watch", snapshot.TrajectoryWatchParams{Selector: q, Cursor: baseline.WatchCursor, TimeoutMS: 10})
	var rpc snapshot.TrajectoryWatchResult
	if err != nil || json.Unmarshal(body, &rpc) != nil || len(rpc.Changes) != 1 || rpc.Changes[0] != cli.Changes[0] {
		t.Fatalf("CLI replay has different evidence identity: %+v %s %v", cli, body, err)
	}
	stdout.Reset()
	args[5] = cli.Cursor
	args[8] = "10"
	if code := runTrajectoryCLIContext(context.Background(), args, &stdout, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	var continuation snapshot.TrajectoryWatchResult
	if json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &continuation) != nil || len(continuation.Changes) != 0 || continuation.ResetRequired {
		t.Fatalf("CLI repeated consumed event: %s", stdout.String())
	}
}

type trajectoryWatchOutput struct {
	mu    sync.Mutex
	data  bytes.Buffer
	first chan struct{}
	once  sync.Once
}

func (w *trajectoryWatchOutput) Write(b []byte) (int, error) {
	w.mu.Lock()
	n, err := w.data.Write(b)
	w.mu.Unlock()
	w.once.Do(func() { close(w.first) })
	return n, err
}

func TestTrajectoryCLIWatchCancellationKeepsOneInstance(t *testing.T) {
	_, _, instancePath, _ := trajectoryWatchFixture(t)
	before, err := os.ReadFile(instancePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	output := &trajectoryWatchOutput{first: make(chan struct{})}
	var stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- runTrajectoryCLIContext(ctx, []string{"watch", "events", "--timeout-ms", "4000", "--format", "ndjson", "--instance-file", instancePath}, output, &stderr)
	}()
	select {
	case <-output.first:
	case <-time.After(time.Second):
		t.Fatal("CLI did not expose baseline/reset contract")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("cancelled CLI retained its long poll")
	}
	after, err := os.ReadFile(instancePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("watch started or replaced the running local instance")
	}
}

func TestTrajectoryWatchRPCRevocationCancelsPendingRead(t *testing.T) {
	app, instance, _, _ := trajectoryWatchFixture(t)
	q := snapshot.TrajectorySelector{Collection: "events"}
	baseline, err := app.trajectory.Query(context.Background(), q)
	if err != nil || baseline.WatchCursor == "" {
		t.Fatalf("query baseline: %+v %v", baseline, err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := trajectoryRPC(context.Background(), instance, "traj.watch", snapshot.TrajectoryWatchParams{Selector: q, Cursor: baseline.WatchCursor, TimeoutMS: 4000})
		done <- err
	}()
	time.Sleep(30 * time.Millisecond)
	if _, err := trajectoryHTTP(context.Background(), instance, "/api/trajectory/access", map[string]bool{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("pending watch succeeded after access revocation")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("access revocation did not wake RPC long poll")
	}
}
