package mirror

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openWALDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS t (id INTEGER PRIMARY KEY, v TEXT)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestSnapshot_WithOpenWriter: the online backup API produces a consistent
// snapshot alongside a live writer holding an uncommitted write txn.
func TestSnapshot_WithOpenWriter(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wiki.db")
	db := openWALDB(t, dbPath)
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('committed')"); err != nil {
		t.Fatal(err)
	}
	// Open writer: begin an uncommitted write transaction and hold it.
	writer, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	tx, err := writer.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO t (v) VALUES ('uncommitted')"); err != nil {
		t.Fatal(err)
	}

	b, err := snapshotDatabase(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("snapshotDatabase with open writer: %v", err)
	}
	// Snapshot must contain committed content and NOT the uncommitted row.
	snapPath := filepath.Join(dir, "snap.db")
	writeFile(t, snapPath, b)
	snap, err := sql.Open("sqlite", snapPath)
	if err != nil {
		t.Fatal(err)
	}
	defer snap.Close()
	var n int
	if err := snap.QueryRow("SELECT COUNT(*) FROM t WHERE v='committed'").Scan(&n); err != nil || n != 1 {
		t.Fatalf("committed row: n=%d err=%v", n, err)
	}
	if err := snap.QueryRow("SELECT COUNT(*) FROM t WHERE v='uncommitted'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("uncommitted row leaked: n=%d err=%v", n, err)
	}
}

// TestSnapshotFallback_VacuumFailsDefers: with the backup API forced off
// and VACUUM INTO failing every attempt (unwritable destination dir), the
// policy exhausts its retries and returns a DeferredError — with the
// counter incremented BEFORE return and persisted.
func TestSnapshotFallback_VacuumFailsDefers(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wiki.db")
	db := openWALDB(t, dbPath)
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('x')"); err != nil {
		t.Fatal(err)
	}

	localPath := filepath.Join(dir, "mirror-local.json")
	local := &LocalState{}
	_, err := snapshotForRotation(context.Background(), dbPath, snapOptions{
		forceFallback: true,
		busyTimeout:   time.Millisecond,
		maxRetries:    2,
		vacuumDestDir: "/nonexistent-mirror-test-dir",
		local:         local,
		localPath:     localPath,
	})
	var de *DeferredError
	if !errors.As(err, &de) {
		t.Fatalf("err = %v, want DeferredError", err)
	}
	if !strings.Contains(err.Error(), "after 2 attempt(s)") {
		t.Fatalf("retry policy not exercised: %v", err)
	}
	if local.ConsecutiveDefers != 1 {
		t.Fatalf("ConsecutiveDefers = %d, want 1 (incremented before return)", local.ConsecutiveDefers)
	}
	loaded, lerr := LoadLocalState(localPath)
	if lerr != nil || loaded.ConsecutiveDefers != 1 {
		t.Fatalf("persisted defers = %d, %v", loaded.ConsecutiveDefers, lerr)
	}
}

// TestSnapshotFallback_VacuumSucceeds: forced fallback with NO writer →
// VACUUM INTO produces a valid db.
func TestSnapshotFallback_VacuumSucceeds(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "wiki.db")
	db := openWALDB(t, dbPath)
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('via-vacuum')"); err != nil {
		t.Fatal(err)
	}
	b, err := snapshotForRotation(context.Background(), dbPath, snapOptions{
		forceFallback: true,
		busyTimeout:   50 * time.Millisecond,
		maxRetries:    1,
	})
	if err != nil {
		t.Fatalf("snapshotForRotation: %v", err)
	}
	snapPath := filepath.Join(dir, "snap.db")
	writeFile(t, snapPath, b)
	snap, _ := sql.Open("sqlite", snapPath)
	defer snap.Close()
	var v string
	if err := snap.QueryRow("SELECT v FROM t").Scan(&v); err != nil || v != "via-vacuum" {
		t.Fatalf("vacuum snapshot content = %q, %v", v, err)
	}
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
