package postgres

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/xoai/sage-wiki/internal/store"
)

// TestMigrationV3EvidenceColumns covers the P3-1 Postgres leg: the six added
// relations columns, a pre-v3 row reading back zero-valued, and — the reason
// currentSchemaVersion has to move with the migration — a reader-mode open
// succeeding afterwards.
func TestMigrationV3EvidenceColumns(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("migv3_%d", time.Now().UnixNano())
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

	// Simulate a pre-v3 database: drop the six columns and roll the version
	// back, then let Open migrate forward. ADD COLUMN IF NOT EXISTS is
	// re-runnable, but the version row is what the runner keys off.
	for _, col := range []string{"evidence", "confidence", "source_doc", "valid_from", "valid_to", "invalidated_by"} {
		if _, err := raw.Exec("ALTER TABLE relations DROP COLUMN IF EXISTS " + col); err != nil {
			t.Fatalf("drop %s: %v", col, err)
		}
	}
	if _, err := raw.Exec("DELETE FROM schema_version WHERE version >= 3"); err != nil {
		t.Fatalf("roll back version: %v", err)
	}

	// A legacy row, written the pre-v3 way.
	if _, err := raw.Exec(`INSERT INTO entities (id, type, name) VALUES ('e1','concept','One'), ('e2','concept','Two')
		ON CONFLICT (id) DO NOTHING`); err != nil {
		t.Fatalf("seed entities: %v", err)
	}
	if _, err := raw.Exec(`INSERT INTO relations (id, source_id, target_id, relation, created_at)
		VALUES ('legacy','e1','e2','implements', now())
		ON CONFLICT (source_id, target_id, relation) DO NOTHING`); err != nil {
		t.Fatalf("seed relation: %v", err)
	}

	b, err := Open(testDSN, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("open (migrates to v3): %v", err)
	}
	defer b.Close()

	for _, col := range []string{"evidence", "confidence", "source_doc", "valid_from", "valid_to", "invalidated_by"} {
		var n int
		if err := raw.QueryRow(`SELECT COUNT(*) FROM information_schema.columns
			WHERE table_name='relations' AND column_name=$1`, col).Scan(&n); err != nil || n != 1 {
			t.Errorf("relations.%s missing after v3 (n=%d, err=%v)", col, n, err)
		}
	}

	var version int
	if err := raw.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want currentSchemaVersion (%d)", version, currentSchemaVersion)
	}

	// The legacy row survives and reads back zero-valued through the store.
	rels, err := b.Ontology().RelationsByType("implements")
	if err != nil {
		t.Fatalf("RelationsByType: %v", err)
	}
	var legacy *store.Relation
	for i := range rels {
		if rels[i].ID == "legacy" {
			legacy = &rels[i]
		}
	}
	if legacy == nil {
		t.Fatal("legacy relation lost across the migration")
	}
	if legacy.Evidence != "" || legacy.Confidence != 0 || legacy.SourceDoc != "" ||
		legacy.ValidFrom != "" || legacy.ValidTo != "" || legacy.InvalidatedBy != "" {
		t.Errorf("pre-v3 row read back non-zero: %+v", *legacy)
	}

	// Reader-mode open must still succeed. verifySchemaVersion compares against
	// currentSchemaVersion, so a migration that bumps the data without bumping
	// the constant locks every reader out.
	rb, err := Open(testDSN, store.OpenOptions{Mode: store.ModeReader, VectorDimension: 3})
	if err != nil {
		t.Fatalf("reader-mode open after v3: %v", err)
	}
	rb.Close()
}
