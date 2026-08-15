package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"testing"
)

func TestTrajectoryFactServiceCheckpointStable(t *testing.T) {
	s, _ := fixture(t, request("checkpoint evidence")+call("cp-call")+result("cp-call"))
	first := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "evidence"})
	src := s.provider(context.Background()).Sources[0]
	cp, ok, err := s.store.checkpoint(context.Background(), sourceID(src))
	if err != nil || !ok || !cp.Coverage.Complete {
		t.Fatalf("checkpoint ok=%v error=%v gaps=%v", ok, err, cp.Coverage.Gaps)
	}
	after := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "evidence"})
	if after.Revision != first.Revision || after.Sessions[0].ID != first.Sessions[0].ID {
		t.Fatal("unchanged source identity changed")
	}
}
