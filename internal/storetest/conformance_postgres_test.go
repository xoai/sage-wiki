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
// Each subtest gets an isolated DATABASE cloned from the DSN's database as
// template (a schema-per-subtest approach breaks pgvector's type lookup:
// the extension lives in public and search_path isolation hides it).
// Vector columns are created at dimension 3 to match the suite's fixtures.
func TestConformancePostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset — postgres conformance leg skipped")
	}
	RunConformance(t, func(t *testing.T) store.Backend {
		t.Helper()
		dbName := fmt.Sprintf("conf_%d", time.Now().UnixNano())
		// Bootstrap from the maintenance DB — a boot attached to the template
		// triggers 55006 for every concurrent cloner, itself included.
		boot, err := sql.Open("pgx", swapDBName(dsn, "postgres"))
		if err != nil {
			t.Fatalf("bootstrap connect: %v", err)
		}
		// Retry through SQLSTATE 55006: go test runs packages in parallel and
		// another cloner may hold the template briefly (P3-7 added one).
		created := false
		for attempt := 0; attempt < 10 && !created; attempt++ {
			if _, err := boot.Exec("CREATE DATABASE " + dbName + " TEMPLATE " + templateDBName(dsn)); err == nil {
				created = true
			} else if !strings.Contains(err.Error(), "55006") {
				boot.Close()
				t.Fatalf("create test database: %v", err)
			} else {
				time.Sleep(time.Duration(100*(attempt+1)) * time.Millisecond)
			}
		}
		boot.Close()
		if !created {
			t.Fatal("create test database: template busy after 10 retries")
		}

		b, err := postgres.Open(swapDBName(dsn, dbName), store.OpenOptions{
			Mode:            store.ModeWriter,
			VectorDimension: 3,
		})
		if err != nil {
			t.Fatalf("postgres open: %v", err)
		}
		t.Cleanup(func() {
			b.Close()
			clean, err := sql.Open("pgx", swapDBName(dsn, "postgres"))
			if err == nil {
				clean.Exec("DROP DATABASE " + dbName)
				clean.Close()
			}
		})
		return b
	})
}

// templateDBName extracts the database name from the DSN path (query params
// stripped first — socket paths in params contain slashes).
func templateDBName(dsn string) string {
	path := dsn
	if j := strings.Index(path, "?"); j >= 0 {
		path = path[:j]
	}
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return "postgres"
}

// swapDBName replaces the database name in the DSN path.
func swapDBName(dsn, name string) string {
	suffix := ""
	path := dsn
	if j := strings.Index(path, "?"); j >= 0 {
		suffix = path[j:]
		path = path[:j]
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i+1] + name + suffix
	}
	return dsn
}
