package trajectory

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/url"
)

// Connection-local settings must survive retirement after a failed commit.
func searchDatabaseDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(200)")
	q.Add("_pragma", "temp_store(MEMORY)")
	u.RawQuery = q.Encode()
	return u.String()
}

// These SQLite handles have one connection and one lifecycle owner (Service's
// opMu, or offline maintenance). SQLite leaves a transaction active when COMMIT
// returns BUSY; database/sql has already marked the Tx done, so its deferred
// Rollback cannot release that transaction. Retire the owning connection on a
// failed commit, which rolls it back without replaying or acknowledging writes.
func commitSearchTransaction(tx *sql.Tx, db *sql.DB) error {
	err := tx.Commit()
	if err == nil {
		return nil
	}
	conn, closeErr := db.Conn(context.Background())
	if closeErr == nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		_ = conn.Close()
	}
	return err
}

// A transaction created by Conn.BeginTx still pins its connection after Commit.
// Retire that connection directly, rather than waiting on the one-slot DB pool.
func commitSearchConnectionTransaction(tx *sql.Tx, conn *sql.Conn) error {
	err := tx.Commit()
	if err != nil {
		_ = conn.Raw(func(any) error { return driver.ErrBadConn })
	}
	return err
}
