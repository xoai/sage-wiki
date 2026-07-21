package storetest

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/xoai/sage-wiki/internal/storage/postgres"
	"github.com/xoai/sage-wiki/internal/store"
)

// TestConformancePostgres runs the conformance suite against the postgres
// backend — gated on TEST_DATABASE_URL (merge precondition per the
// 2026-07-21 waiver; the database needs CREATE EXTENSION vector once).
// Each subtest gets an isolated schema (search_path) so fixtures never
// collide across sections; vector columns are created at dimension 3 to
// match the suite's small fixtures.
func TestConformancePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset — postgres conformance leg skipped")
	}
	RunConformance(t, func(t *testing.T) store.Backend {
		t.Helper()
		schema := fmt.Sprintf("conf_%d", time.Now().UnixNano())
		boot, err := sql.Open("pgx", dsn)
		if err != nil {
			t.Fatalf("bootstrap connect: %v", err)
		}
		if _, err := boot.Exec("CREATE SCHEMA " + schema); err != nil {
			boot.Close()
			t.Fatalf("create schema: %v", err)
		}
		boot.Close()

		sep := "?"
		if strings.Contains(dsn, "?") {
			sep = "&"
		}
		b, err := postgres.Open(dsn+sep+"search_path="+schema, store.OpenOptions{
			Mode:            store.ModeWriter,
			VectorDimension: 3,
		})
		if err != nil {
			t.Fatalf("postgres open: %v", err)
		}
		t.Cleanup(func() {
			b.Close()
			clean, err := sql.Open("pgx", dsn)
			if err == nil {
				clean.Exec("DROP SCHEMA " + schema + " CASCADE")
				clean.Close()
			}
		})
		return b
	})
}
