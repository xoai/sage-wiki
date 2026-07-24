package postgres

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/xoai/sage-wiki/internal/store"
)

// migrationTestDSN returns TEST_DATABASE_URL or skips (the codebase's
// standard pg-leg gating, mirroring storetest/conformance_postgres_test.go).
func migrationTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset — postgres migration leg skipped")
	}
	return dsn
}

func swapDB(dsn, dbName string) string {
	parts := strings.Split(dsn, "/")
	last := parts[len(parts)-1]
	if idx := strings.Index(last, "?"); idx >= 0 {
		parts[len(parts)-1] = dbName + last[idx:]
	} else {
		parts[len(parts)-1] = dbName
	}
	return strings.Join(parts, "/")
}

func dsnDB(dsn string) string {
	parts := strings.Split(dsn, "/")
	last := parts[len(parts)-1]
	if idx := strings.Index(last, "?"); idx >= 0 {
		return last[:idx]
	}
	return last
}

// TestMigrationV2QueueColumns proves the V2 migration (queue columns +
// per-tier backfill, mirroring sqlite TestMigrationV9) applies cleanly on a
// fresh database and backfills correctly.
func TestMigrationV2QueueColumns(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("migv2_%d", time.Now().UnixNano())
	boot, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("bootstrap connect: %v", err)
	}
	if _, err := boot.Exec("CREATE DATABASE " + dbName + " TEMPLATE " + dsnDB(dsn)); err != nil {
		boot.Close()
		t.Fatalf("create test database: %v", err)
	}
	boot.Close()
	t.Cleanup(func() {
		c, err := sql.Open("pgx", dsn)
		if err == nil {
			c.Exec("DROP DATABASE " + dbName)
			c.Close()
		}
	})

	b, err := Open(swapDB(dsn, dbName), store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.Close()

	raw, err := sql.Open("pgx", swapDB(dsn, dbName))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// Columns exist.
	for _, col := range []string{"status", "lease_owner", "lease_until", "heartbeat_at", "attempts"} {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name='compile_items' AND column_name=$1`, col).Scan(&n); err != nil || n != 1 {
			t.Errorf("column %s missing (n=%d, err=%v)", col, n, err)
		}
	}
	// Index exists.
	var idx int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM pg_indexes WHERE tablename='compile_items' AND indexname='idx_ci_claim'`).Scan(&idx); err != nil || idx != 1 {
		t.Errorf("idx_ci_claim missing (n=%d, err=%v)", idx, err)
	}
	// Backfill predicate (insert then re-run the V2 backfill UPDATE).
	if _, err := raw.Exec(`INSERT INTO compile_items (source_path, tier, pass_indexed, pass_embedded) VALUES
		('t1-done', 1, 1, 1), ('t1-pending', 1, 1, 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE compile_items SET status = 'done' WHERE
		(tier = 0 AND pass_indexed = 1) OR
		(tier = 1 AND pass_indexed = 1 AND pass_embedded = 1) OR
		(tier = 2 AND pass_indexed = 1 AND pass_embedded = 1 AND pass_parsed = 1) OR
		(tier = 3 AND pass_indexed = 1 AND pass_embedded = 1
			AND pass_summarized = 1 AND pass_extracted = 1 AND pass_written = 1)`); err != nil {
		t.Fatal(err)
	}
	var done, pending int
	raw.QueryRow("SELECT COUNT(*) FROM compile_items WHERE status='done'").Scan(&done)
	raw.QueryRow("SELECT COUNT(*) FROM compile_items WHERE status='pending'").Scan(&pending)
	if done != 1 || pending != 1 {
		t.Errorf("backfill: done=%d pending=%d, want 1/1", done, pending)
	}
}
