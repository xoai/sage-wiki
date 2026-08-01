package mirror

import (
	"context"
	"database/sql"
	"fmt"

	"modernc.org/sqlite"
)

// backupConn matches the modernc.org/sqlite driver connection's online
// backup surface exactly (Go interface method sets require identical
// signatures, so we name the concrete *sqlite.Backup). Pinned minimum
// driver version: v1.48.1 (probe test TestBackupProbe; see spec.md
// Recommendation Rationale). If a driver bump removes NewBackup, the
// assertion fails loudly and the VACUUM INTO fallback (Task 11) becomes
// primary.
type backupConn interface {
	NewBackup(dstURI string) (*sqlite.Backup, error)
}

// snapshotViaBackupAPI copies the database behind db to dstPath using the
// SQLite online backup API, safe alongside a live writer.
func snapshotViaBackupAPI(db *sql.DB, dstPath string) error {
	conn, err := db.Conn(context.Background())
	if err != nil {
		return fmt.Errorf("snapshot: acquire conn: %w", err)
	}
	defer conn.Close()
	return conn.Raw(func(driverConn any) error {
		bc, ok := driverConn.(backupConn)
		if !ok {
			return fmt.Errorf("snapshot: driver conn %T does not expose NewBackup (need modernc.org/sqlite >= v1.48.1)", driverConn)
		}
		backup, err := bc.NewBackup(dstPath)
		if err != nil {
			return fmt.Errorf("snapshot: NewBackup: %w", err)
		}
		defer backup.Finish()
		// Step(-1) copies all remaining pages in one call; the returned
		// bool is "more pages remain" (true) vs "finished" (false) per
		// modernc.org/sqlite backup.go — a loop here would busy-spin on
		// SQLITE_BUSY, so one full-copy call is the correct shape.
		if _, err := backup.Step(-1); err != nil {
			return fmt.Errorf("snapshot: backup step: %w", err)
		}
		return nil
	})
}
