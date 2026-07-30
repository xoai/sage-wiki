package postgres

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// TestMigrationV4AliasTable covers the P3-3 Postgres leg: the entity_aliases
// table, its three indexes, an upgrade from a v3 database, and — the reason
// currentSchemaVersion has to move with the migration — a reader-mode open
// succeeding afterwards.
func TestMigrationV4AliasTable(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("migv4_%d", time.Now().UnixNano())
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
	testDSN := swapDB(dsn, dbName)

	raw, err := sql.Open("pgx", testDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()

	// Create the schema first: these tests clone the base database, which is
	// empty on a fresh CI Postgres. Simulating a downgrade against a database
	// that was never migrated fails on the first ALTER/DELETE. (This passed
	// locally for whoever ran it against a long-lived dev database that had
	// accumulated the schema from earlier runs — the base having schema was
	// an accident of local state, never a guarantee.)
	seed, err := Open(testDSN, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("seed schema: %v", err)
	}
	seed.Close()

	// Simulate a pre-v4 database: drop the table and roll the version back,
	// then let Open migrate forward.
	if _, err := raw.Exec("DROP TABLE IF EXISTS entity_aliases"); err != nil {
		t.Fatalf("drop entity_aliases: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM schema_version WHERE version >= 4"); err != nil {
		t.Fatalf("roll back version: %v", err)
	}

	b, err := Open(testDSN, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("open (migrates to v4): %v", err)
	}
	defer b.Close()

	for _, col := range []string{
		"alias", "canonical_id", "entity_type", "status", "confidence",
		"reason", "source", "created_at", "decided_at", "decided_by",
	} {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name='entity_aliases' AND column_name=$1`, col).Scan(&n); err != nil || n != 1 {
			t.Errorf("entity_aliases.%s missing after v4 (n=%d, err=%v)", col, n, err)
		}
	}

	for _, idx := range []string{
		"idx_entity_aliases_active",
		"idx_entity_aliases_canonical",
		"idx_entity_aliases_status",
	} {
		var n int
		if err := raw.QueryRow(
			`SELECT COUNT(*) FROM pg_indexes WHERE tablename='entity_aliases' AND indexname=$1`,
			idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after v4 (n=%d, err=%v)", idx, n, err)
		}
	}

	var version int
	if err := raw.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want currentSchemaVersion (%d)", version, currentSchemaVersion)
	}

	// The partial unique index enforces one live decision per alias while
	// letting rejections accumulate — the property the whole rejection design
	// depends on, verified here on the real backend rather than by inspection.
	ont := b.Ontology()
	mk := func(alias, canonical string, st store.AliasStatus) store.EntityAlias {
		return store.EntityAlias{
			Alias: alias, CanonicalID: canonical, EntityType: "concept",
			Status: st, Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
		}
	}
	if err := ont.PutAlias(mk("a", "c1", store.AliasApplied)); err != nil {
		t.Fatalf("first active row: %v", err)
	}
	if err := ont.PutAlias(mk("a", "c2", store.AliasPending)); err == nil {
		t.Error("a second ACTIVE row for one alias was accepted on postgres")
	}
	for _, c := range []string{"c3", "c4"} {
		if err := ont.PutAlias(mk("a", c, store.AliasRejected)); err != nil {
			t.Errorf("rejected row %s must coexist with an active one: %v", c, err)
		}
	}

	// Timestamps are TEXT on both backends and must round-trip byte-identically
	// — this is what binding raw strings rather than nullRFC protects.
	got, err := ont.GetActiveAlias("a")
	if err != nil || got == nil {
		t.Fatalf("GetActiveAlias: %+v %v", got, err)
	}
	if got.CreatedAt != "2026-07-26T00:00:00Z" {
		t.Errorf("CreatedAt = %q, want the byte-identical RFC3339 string", got.CreatedAt)
	}

	// A reader-mode open must succeed after the migration; a stale
	// currentSchemaVersion is exactly what this catches.
	rb, err := Open(testDSN, store.OpenOptions{Mode: store.ModeReader, VectorDimension: 3})
	if err != nil {
		t.Fatalf("reader open after v4: %v", err)
	}
	rb.Close()
}
