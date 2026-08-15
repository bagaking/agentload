package trajectory

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestTrajectoryRecoveryAuditStaysInSourceQueue(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, strings.Repeat(request(strings.Repeat("prefix ", 40000)), 20)+request("middle")+request("last"))
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

func TestTrajectoryRecoveryAuditRejectsPreviouslyReadRangeChangeBeforeSeal(t *testing.T) {
	s, st := replayFixture(t, "codex", CodexDecoder{}, strings.Repeat(request(strings.Repeat("prefix ", 40000)), 20)+request("middle")+request("last"))
	var ranges int
	if err := s.store.db.QueryRow("SELECT count(*) FROM ranges").Scan(&ranges); err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.db.Exec("UPDATE sources SET verified_size=-1,verified_mtime=-1,audit_size=-1,audit_mtime=-1,audit_after=-1"); err != nil {
		t.Fatal(err)
	}
	more, err := s.PrepareSource(context.Background(), st.Source, func() bool { return true })
	if err != nil || !more {
		t.Fatal("expected unfinished audit", more, err)
	}
	// Damage the range that the prior unit already checked. Later units must
	// not seal the source merely because their own bytes and chain are valid.
	if _, err = s.store.db.Exec("UPDATE ranges SET body=X'00' WHERE start=(SELECT min(start) FROM ranges)"); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < ranges && more && err == nil; n++ {
		more, err = s.PrepareSource(context.Background(), st.Source, func() bool { return true })
	}
	if err == nil {
		t.Fatal("corrupted prior range was accepted at audit seal", more)
	}
	ready, readErr := s.store.readinessScope(context.Background(), st.ID)
	if readErr != nil || ready[st.ID].verifiedMtime != -1 {
		t.Fatal("failed audit published source trust", ready, readErr)
	}
}
