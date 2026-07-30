package wiki

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/storedial"
)

// P3-7 T4: the reconcile path works on the Postgres backend — opened via the
// same storedial.Open option literal reconcileStartup uses (the option
// wiring is the actual failure surface on PG), driven end-to-end, with no
// stray SQLite file. Env-gated like every Postgres test (TEST_DATABASE_URL).

func pgReconcileDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL unset — postgres reconcile leg skipped")
	}
	return dsn
}

func TestReconcileBackendPostgres(t *testing.T) {
	dsn := pgReconcileDSN(t)
	dbName := fmt.Sprintf("p37reconcile_%d", time.Now().UnixNano())
	// Bootstrap from the maintenance DB, never the template: a boot attached
	// to the template IS the "other user" that triggers 55006 for everyone
	// (and held-open boots during retries livelock all concurrent cloners).
	boot, err := sql.Open("pgx", swapDBForTest(dsn, "postgres"))
	if err != nil {
		t.Fatalf("bootstrap connect: %v", err)
	}
	// CREATE DATABASE TEMPLATE fails with SQLSTATE 55006 when another test
	// package holds a connection to the template — a real hazard now that
	// multiple packages clone concurrently (go test runs packages in
	// parallel, including in CI). Retry with backoff; the window is short.
	created := false
	for attempt := 0; attempt < 10; attempt++ {
		if _, err := boot.Exec("CREATE DATABASE " + dbName + " TEMPLATE " + pgTemplateDB(t, dsn)); err == nil {
			created = true
			break
		} else if !strings.Contains(err.Error(), "55006") {
			boot.Close()
			t.Fatalf("create test database: %v", err)
		}
		time.Sleep(time.Duration(100*(attempt+1)) * time.Millisecond)
	}
	boot.Close()
	if !created {
		t.Fatal("create test database: template busy after 10 retries")
	}
	t.Cleanup(func() {
		c, err := sql.Open("pgx", swapDBForTest(dsn, "postgres"))
		if err == nil {
			c.Exec("DROP DATABASE " + dbName)
			c.Close()
		}
	})

	dir := t.TempDir()
	if err := InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	// Configure the vault for Postgres.
	cfgBytes, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfgBytes = append(cfgBytes, []byte(fmt.Sprintf(`
storage:
  backend: postgres
  dsn: %s
  vector_dimension: 3
`, swapDBForTest(dsn, dbName)))...)
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), cfgBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	// Drift: manifest expects an article no store has.
	content := "# Gamma\n\nContent about gamma."
	rel := filepath.Join(cfg.Output, "concepts", "gamma.md")
	abs := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	m := manifest.New()
	m.AddConcept("gamma", rel, []string{"raw/g.md"})
	if err := m.Save(filepath.Join(dir, ".manifest.json")); err != nil {
		t.Fatal(err)
	}

	// The startup wiring itself: ONE shared option literal
	// (storedial.OpenWithConfig) — drift here breaks reconcileStartup and
	// this test together, which is the point (Gate-8).
	backend, err := storedial.OpenWithConfig(cfg, dir, store.ModeWriter)
	if err != nil {
		t.Fatalf("storedial.OpenWithConfig (the startup literal): %v", err)
	}
	defer backend.Close()

	res, err := ReconcileBackend(ctxForTest(), dir, cfg, backend, nil)
	if err != nil {
		t.Fatalf("ReconcileBackend on PG: %v", err)
	}
	if res.Reindexed != 1 {
		t.Errorf("Reindexed = %d, want 1 (PG leg)", res.Reindexed)
	}

	// The entry landed on POSTGRES — and NOT in the SQLite file InitGreenfield
	// bootstraps (that file's existence is the P2-1 skip-listed init behavior,
	// explicitly out of P3-7 scope; what matters is reconcile writes nothing
	// to it).
	got, err := backend.Entries().Get("concept:gamma")
	if err != nil || got == nil {
		t.Errorf("gamma not indexed into the PG FTS: %v %v", got, err)
	}
	sqliteDB, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err == nil {
		defer sqliteDB.Close()
		var n int
		_ = sqliteDB.QueryRow(`SELECT COUNT(*) FROM entries WHERE id = 'concept:gamma'`).Scan(&n)
		if n != 0 {
			t.Error("reconcile wrote to the SQLite file on a Postgres vault")
		}
	}
}

// --- test-local DSN helpers (mirrors internal/storage/postgres/migration_v2_test.go) ---

func pgTemplateDB(t *testing.T, dsn string) string {
	t.Helper()
	path := dsn
	if j := strings.Index(path, "?"); j >= 0 {
		path = path[:j]
	}
	if i := strings.LastIndex(path, "/"); i >= 0 && i+1 < len(path) {
		return path[i+1:]
	}
	return "postgres"
}

func swapDBForTest(dsn, dbName string) string {
	suffix, path := "", dsn
	if j := strings.Index(path, "?"); j >= 0 {
		suffix, path = path[j:], path[:j]
	}
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[:i+1] + dbName + suffix
	}
	return path + "/" + dbName + suffix
}

func ctxForTest() context.Context { return context.Background() }
