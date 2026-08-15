package trajectory

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestTrajectoryInventoryVolumeIdentityGuards(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("original human task"))
	src := st.Source
	src.Info = st.Info
	volumes := inventoryVolumeIdentities(context.Background(), []Source{src})
	if len(volumes) != 1 {
		t.Fatal("current volume identity was not measured", volumes)
	}
	s.checkpointSnapshot = map[string]sourceCheckpoint{st.ID: st.checkpoint}
	current, pending := s.cachedSourceWithVolumes(src, volumes)
	if current == nil || pending || current.Generation != st.Generation || current.checkpoint.EventCount != st.checkpoint.EventCount {
		t.Fatal("unchanged inventory lost committed metadata", current, pending)
	}
	appendFile(t, st.Path, request("new tail"))
	current, pending = s.cachedSourceWithVolumes(src, volumes)
	if current == nil || !pending || current.checkpoint.Offset != st.checkpoint.Offset {
		t.Fatal("metadata optimization advertised an uncommitted append", current, pending)
	}
	if err := os.WriteFile(st.Path, []byte(strings.Repeat("x", int(st.checkpoint.Size))), 0600); err != nil {
		t.Fatal(err)
	}
	if current, pending = s.cachedSourceWithVolumes(src, volumes); current != nil || !pending {
		t.Fatal("same-size rewrite reused inventory metadata", current, pending)
	}
	replacement := st.Path + ".new"
	if err := os.WriteFile(replacement, []byte(request("original human task")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, st.Path); err != nil {
		t.Fatal(err)
	}
	if current, pending = s.cachedSourceWithVolumes(src, volumes); current != nil || !pending {
		t.Fatal("replacement reused a previous inode identity", current, pending)
	}
	if err := os.Remove(st.Path); err != nil {
		t.Fatal(err)
	}
	if current, pending = s.cachedSourceWithVolumes(src, volumes); current != nil || !pending {
		t.Fatal("missing source reused inventory metadata", current, pending)
	}
}
