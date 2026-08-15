package trajectory

import (
	"agentload/internal/snapshot"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"modernc.org/sqlite"
)

func TestTrajectorySearchCommitBusyRecovery(t *testing.T) {
	s, path := fixture(t, request("old needle"))
	before := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	oldID := before.Sessions[0].MatchedIDs[0]
	reader, err := sql.Open("sqlite", s.search.path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	readTx, err := reader.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer readTx.Rollback()
	var count int
	if err = readTx.QueryRow("SELECT search_count FROM sources WHERE active=1 AND missing=0").Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = os.WriteFile(path, []byte(request("old needle")+request("new needle")), 0600); err != nil {
		t.Fatal(err)
	}
	src := s.provider(context.Background()).Sources[0]
	_, err = s.PrepareSource(context.Background(), src, func() bool { return true })
	var busy *sqlite.Error
	if !errors.As(err, &busy) || busy.Code()&255 != 5 {
		t.Fatal("reader did not force a failed commit", err)
	}
	if err = readTx.Rollback(); err != nil {
		t.Fatal(err)
	}
	after := searchTestPreparedQuery(t, s, snapshot.TrajectorySelector{Text: "needle"})
	if len(after.Sessions) != 1 || after.Sessions[0].MatchedCount != 2 || after.Sessions[0].MatchedIDs[0] != oldID {
		t.Fatal("failed commit poisoned connection, lost identity or duplicated evidence", after)
	}
	if err = s.search.db.QueryRow("SELECT search_count FROM sources WHERE active=1 AND missing=0").Scan(&count); err != nil || count != 2 {
		t.Fatal("recovery acknowledged uncommitted or duplicated rows", count, err)
	}
}
