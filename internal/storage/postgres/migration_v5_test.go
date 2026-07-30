package postgres

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// TestMigrationV5DerivedRelations covers the Postgres leg of decision-035's
// schema half.
//
// The column TYPES are the point, not a detail. Postgres `relations.created_at`
// is TIMESTAMPTZ and `confidence` is DOUBLE PRECISION, so derived_relations must
// match — an earlier design that used TEXT/REAL here would have failed at the
// first read that scanned a derived row, and (because this runner has no
// transaction) would have left the database half-migrated and unopenable.
func TestMigrationV5DerivedRelations(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("migv5_%d", time.Now().UnixNano())
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

	// Simulate a pre-v5 database, then let Open migrate forward.
	if _, err := raw.Exec("DROP TABLE IF EXISTS derived_relations"); err != nil {
		t.Fatalf("drop derived_relations: %v", err)
	}
	if _, err := raw.Exec("DELETE FROM schema_version WHERE version >= 5"); err != nil {
		t.Fatalf("roll back version: %v", err)
	}

	b, err := Open(testDSN, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("open (migrates to v5): %v", err)
	}

	for _, c := range []struct{ col, typ string }{
		{"alias_id", "text"},
		{"id", "text"},
		{"source_id", "text"},
		{"target_id", "text"},
		{"relation", "text"},
		{"created_at", "timestamp with time zone"},
		{"evidence", "text"},
		{"confidence", "double precision"},
		{"source_doc", "text"},
		{"valid_from", "text"},
		{"valid_to", "text"},
		{"invalidated_by", "text"},
	} {
		var got string
		if err := raw.QueryRow(`SELECT data_type FROM information_schema.columns
			WHERE table_name='derived_relations' AND column_name=$1`, c.col).Scan(&got); err != nil {
			t.Errorf("derived_relations.%s missing after v5: %v", c.col, err)
			continue
		}
		if got != c.typ {
			t.Errorf("derived_relations.%s is %q, want %q — it must match the relations column it mirrors",
				c.col, got, c.typ)
		}
	}

	for _, idx := range []string{"idx_derived_source", "idx_derived_target", "idx_derived_alias"} {
		var n int
		if err := raw.QueryRow(
			`SELECT COUNT(*) FROM pg_indexes WHERE tablename='derived_relations' AND indexname=$1`,
			idx).Scan(&n); err != nil || n != 1 {
			t.Errorf("index %s missing after v5 (n=%d, err=%v)", idx, n, err)
		}
	}

	var version int
	if err := raw.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != currentSchemaVersion {
		t.Errorf("schema_version = %d, want currentSchemaVersion (%d)", version, currentSchemaVersion)
	}

	// Two aliases may derive the same edge; the same alias twice may not.
	if _, err := raw.Exec(`INSERT INTO entities (id,type,name) VALUES ('C','concept','C'),('Q','concept','Q')
	                       ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	ins := `INSERT INTO derived_relations (alias_id,id,source_id,target_id,relation)
	        VALUES ($1,$2,'C','Q','extends')`
	if _, err := raw.Exec(ins, "al1", "alias:1111111111111111"); err != nil {
		t.Fatalf("first alias: %v", err)
	}
	if _, err := raw.Exec(ins, "al2", "alias:1111111111111111"); err != nil {
		t.Fatalf("second alias deriving the same edge must be permitted: %v", err)
	}
	if _, err := raw.Exec(ins, "al1", "alias:1111111111111111"); err == nil {
		t.Error("the same alias asserting one edge twice must conflict on the primary key")
	}

	// A pruned entity takes its derived edges with it.
	if _, err := raw.Exec(`DELETE FROM entities WHERE id='Q'`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM derived_relations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("derived rows after deleting the target entity = %d, want 0 (ON DELETE CASCADE)", n)
	}

	// Close the writer before reopening: this backend takes a vault lock.
	b.Close()

	// A reader-mode open must succeed after the migration; a stale
	// currentSchemaVersion is exactly what this catches.
	rb, err := Open(testDSN, store.OpenOptions{Mode: store.ModeReader, VectorDimension: 3})
	if err != nil {
		t.Fatalf("reader open after v5: %v", err)
	}
	rb.Close()

	// And v5 must be re-runnable: this runner has no transaction, so a failure
	// part-way through re-runs the whole slice from an un-bumped version.
	if _, err := raw.Exec("DELETE FROM schema_version WHERE version = 5"); err != nil {
		t.Fatal(err)
	}
	b2, err := Open(testDSN, store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 3})
	if err != nil {
		t.Fatalf("v5 is not re-runnable after a partial apply: %v", err)
	}
	b2.Close()
}
