package main

import (
	"agentload/internal/snapshot"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTrajectoryIndexAppAccessAndRootWithdrawal(t *testing.T) {
	app, _, instance := trajectoryTestApp(t)
	defer app.trajectory.Close()
	path := filepath.Join(app.trajectoryAccess.root, "trajectory.sqlite")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled app opened content index")
	}
	var out, stderr bytes.Buffer
	if code := runTrajectoryCLI([]string{"access", "on", "--instance-file", instance}, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	// Access/root withdrawal asserts prepared evidence. Cold preparation is
	// deliberately bounded and may truthfully return index_pending.
	prepareTrajectoryAppFixture(t, app)
	q, err := app.trajectory.Query(context.Background(), snapshot.TrajectorySelector{Collection: "events"})
	if err != nil || len(q.Events) == 0 {
		t.Fatal(q, err)
	}
	old := q.Events[0].ID
	if _, err := os.Stat(path); err != nil {
		t.Fatal("content index absent", err)
	}
	for _, name := range []string{"index.bbolt", "search.sqlite"} {
		if _, err := os.Stat(filepath.Join(app.trajectoryAccess.root, name)); !os.IsNotExist(err) {
			t.Fatal("fresh app created a second content store", name, err)
		}
	}
	app.cfg.CodexRoots = nil
	withdrawn, err := app.trajectory.Query(context.Background(), snapshot.TrajectorySelector{})
	if err != nil || len(withdrawn.Sessions) != 0 {
		t.Fatal("removed root still queryable", withdrawn, err)
	}
	if _, err := app.trajectory.Get(context.Background(), snapshot.TrajectoryGetParams{ID: old}); err == nil {
		t.Fatal("removed source read cached content")
	}
	preserved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	stderr.Reset()
	if code := runTrajectoryCLI([]string{"access", "off", "--instance-file", instance}, &out, &stderr); code != 0 {
		t.Fatal(stderr.String())
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(preserved, retained) {
		t.Fatal("revocation altered useful index/recovery state", err)
	}
	credentials, err := readTrajectoryInstance(instance)
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"traj.query", "traj.get"} {
		var params any = snapshot.TrajectorySelector{}
		if method == "traj.get" {
			params = snapshot.TrajectoryGetParams{ID: old, View: "raw"}
		}
		if data, err := trajectoryRPC(context.Background(), credentials, method, params); err == nil || len(data) != 0 {
			t.Fatal("revoked access exposed retained evidence", method, err)
		}
	}
	unchanged, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(retained, unchanged) {
		t.Fatal("denied reads changed retained recovery state", err)
	}
}
