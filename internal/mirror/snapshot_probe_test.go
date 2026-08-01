package mirror

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// TestBackupProbe pins the SPEC-03 snapshot access path: the modernc driver
// conn exposes NewBackup via interface assertion through sql.Conn.Raw.
// If this test fails to compile or the assertion fails, the VACUUM INTO
// fallback becomes primary (spec.md Recommendation Rationale).
func TestBackupProbe(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.db")

	db, err := sql.Open("sqlite", srcPath)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t (v) VALUES ('hello')"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dstPath := filepath.Join(dir, "snapshot.db")
	if err := snapshotViaBackupAPI(db, dstPath); err != nil {
		t.Fatalf("snapshotViaBackupAPI: %v", err)
	}

	// Verify the snapshot opens and contains the row.
	snap, err := sql.Open("sqlite", dstPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	var v string
	if err := snap.QueryRow("SELECT v FROM t").Scan(&v); err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	if v != "hello" {
		t.Fatalf("snapshot content = %q, want %q", v, "hello")
	}
	if info, err := os.Stat(dstPath); err != nil || info.Size() == 0 {
		t.Fatalf("snapshot file missing or empty: info=%v err=%v", info, err)
	}
}
