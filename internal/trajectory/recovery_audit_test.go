package trajectory

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestTrajectoryRecoveryAuditStaysInSourceQueue(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request(strings.Repeat("prefix ", 24000))+request("middle")+request("last"))
	var ranges int
	if err := s.store.db.QueryRow("SELECT count(*) FROM ranges").Scan(&ranges); err != nil || ranges < 2 {
		t.Fatalf("multiple audit units: %d %v", ranges, err)
	}
	if _, err := s.store.db.Exec("UPDATE sources SET verified_size=-1,verified_mtime=-1,audit_size=-1,audit_mtime=-1,audit_after=-1"); err != nil {
		t.Fatal(err)
	}
	more, err := s.PrepareSource(context.Background(), st.Source, func() bool { return true })
	if err != nil || !more {
		t.Fatal("unfinished raw audit left the source queue", more, err)
	}
	ready, err := s.store.readinessScope(context.Background(), st.ID)
	if err != nil || ready[st.ID].verifiedMtime != -1 {
		t.Fatal("partial audit blessed the source", ready, err)
	}
	for n := 0; n < ranges && more; n++ {
		more, err = s.PrepareSource(context.Background(), st.Source, func() bool { return true })
		if err != nil {
			t.Fatal(err)
		}
	}
	if more {
		t.Fatal("source audit failed to drain without catalog scans")
	}
	ready, err = s.store.readinessScope(context.Background(), st.ID)
	if err != nil || ready[st.ID].verifiedSize != st.Info.Size() || ready[st.ID].verifiedMtime != st.Info.ModTime().UnixNano() {
		t.Fatal("completed source audit missing seal", ready, err)
	}
}

func TestTrajectoryRecoveryCatalogDoesNotRetryUnauthorizedAudit(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, request("retained evidence"))
	if _, err := s.store.db.Exec("UPDATE sources SET verified_size=-1,verified_mtime=-1"); err != nil {
		t.Fatal(err)
	}
	// Incomplete discovery cannot prove deletion. The retained source is not
	// authorized in this inventory and must not drive a perpetual catalog retry.
	set := SourceSet{Coverage: coverage("incomplete discovery")}
	set.Coverage.Complete = false
	more, err := s.PrepareCatalog(context.Background(), set, func() bool { return true })
	if err != nil || more {
		t.Fatal("unrelated unverified source spun catalog maintenance", more, err)
	}
	cp, ok, err := s.store.checkpoint(context.Background(), st.ID)
	if err != nil || !ok || cp.Missing || cp.EventCount != 1 {
		t.Fatal("idle maintenance discarded recovery evidence", cp, err)
	}
	ready, err := s.store.readinessScope(context.Background(), st.ID)
	if err != nil || ready[st.ID].verifiedMtime != -1 {
		t.Fatal("catalog falsely sealed unauthorized bytes", ready, err)
	}
	if slices.Contains(cp.Coverage.Gaps, "source_missing") {
		t.Fatal("incomplete discovery invented removal")
	}
}
