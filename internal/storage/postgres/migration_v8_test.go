package postgres

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/xoai/sage-wiki/internal/store"
)

// V8 (graph communities) migration: tables created with the twin DDL, schema
// version advanced. Env-gated like every Postgres migration test.
func TestMigrationV8Communities(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("migv8_%d", time.Now().UnixNano())
	boot, err := sql.Open("pgx", swapDB(dsn, "postgres"))
	if err != nil {
		t.Fatalf("bootstrap connect: %v", err)
	}
	createClone(t, boot, dbName, dsnDB(dsn))
	boot.Close()
	t.Cleanup(func() {
		c, err := sql.Open("pgx", swapDB(dsn, "postgres"))
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

	for _, table := range []string{"communities", "community_members"} {
		var name string
		err := b.(*backend).pool.QueryRow(
			`SELECT table_name FROM information_schema.tables WHERE table_name = $1`, table).Scan(&name)
		if err != nil || name != table {
			t.Errorf("table %s missing after migration: %v", table, err)
		}
	}
	var version int
	if err := b.(*backend).pool.QueryRow(`SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema version = %d, want %d", version, currentSchemaVersion)
	}
}
