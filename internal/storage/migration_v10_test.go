package storage

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// buildV9Fixture creates a V9 DB from the real migrationV1..V9 consts
// (mirroring migration_v9_test.go's runner-mirroring approach) plus legacy
// entities and relations written through the pre-V10 five-column INSERT.
func buildV9Fixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v9.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	consts := []string{
		migrationV1, migrationV2, migrationV3, migrationV4, migrationV5,
		migrationV6, migrationV7, migrationV8, migrationV9,
	}
	for i, sqlText := range consts {
		if i == 3 { // V4 rebuilds entities; the runner disables FKs for it.
			if _, err := raw.Exec("PRAGMA foreign_keys = OFF"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := raw.Exec(sqlText); err != nil {
			t.Fatalf("fixture migration v%d: %v", i+1, err)
		}
		if _, err := raw.Exec("INSERT INTO schema_version (version) VALUES (?)", i+1); err != nil {
			t.Fatal(err)
		}
		if i == 3 {
			if _, err := raw.Exec("PRAGMA foreign_keys = ON"); err != nil {
				t.Fatal(err)
			}
		}
	}

	// Legacy rows, written the pre-V10 way: five relation columns, no evidence.
	for _, e := range []struct{ id, typ, name string }{
		{"alpha", "concept", "Alpha"},
		{"beta", "technique", "Beta"},
	} {
		if _, err := raw.Exec(
			`INSERT INTO entities (id, type, name, definition, article_path, created_at, updated_at)
			 VALUES (?, ?, ?, '', '', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z')`,
			e.id, e.typ, e.name,
		); err != nil {
			t.Fatalf("fixture entity %s: %v", e.id, err)
		}
	}
	if _, err := raw.Exec(
		`INSERT INTO relations (id, source_id, target_id, relation, created_at)
		 VALUES ('alpha-extends-beta', 'alpha', 'beta', 'extends', '2026-01-02T00:00:00Z')`,
	); err != nil {
		t.Fatalf("fixture relation: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("open (migrates to current): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestMigrationV10AddsEvidenceColumns(t *testing.T) {
	db := buildV9Fixture(t)

	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 15 {
		t.Fatalf("schema version = %d, want 15", version)
	}

	rows, err := db.ReadDB().Query("PRAGMA table_info(relations)")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	for _, col := range []string{"evidence", "confidence", "source_doc", "valid_from", "valid_to", "invalidated_by"} {
		if !have[col] {
			t.Errorf("relations.%s missing after V10", col)
		}
	}
}

func TestMigrationV10PreservesLegacyRows(t *testing.T) {
	db := buildV9Fixture(t)

	var id, src, tgt, rel, created string
	var evidence, sourceDoc, validFrom, validTo, invalidatedBy string
	var confidence float64
	err := db.ReadDB().QueryRow(
		`SELECT id, source_id, target_id, relation, COALESCE(created_at,''),
		        COALESCE(evidence,''), COALESCE(confidence,0), COALESCE(source_doc,''),
		        COALESCE(valid_from,''), COALESCE(valid_to,''), COALESCE(invalidated_by,'')
		 FROM relations WHERE id='alpha-extends-beta'`,
	).Scan(&id, &src, &tgt, &rel, &created, &evidence, &confidence, &sourceDoc, &validFrom, &validTo, &invalidatedBy)
	if err != nil {
		t.Fatalf("read legacy relation: %v", err)
	}

	if src != "alpha" || tgt != "beta" || rel != "extends" {
		t.Errorf("legacy relation altered: %s -[%s]-> %s", src, rel, tgt)
	}
	if created != "2026-01-02T00:00:00Z" {
		t.Errorf("created_at = %q, want the fixture value preserved", created)
	}
	// The whole point of nullable/defaulted columns: an old row reads back with
	// zero values, not an error and not a spurious default.
	if evidence != "" || confidence != 0 || sourceDoc != "" {
		t.Errorf("legacy row got non-zero evidence fields: evidence=%q confidence=%v source_doc=%q",
			evidence, confidence, sourceDoc)
	}
	if validFrom != "" || validTo != "" || invalidatedBy != "" {
		t.Errorf("legacy row got non-zero temporal fields: %q %q %q", validFrom, validTo, invalidatedBy)
	}

	var entityCount int
	if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM entities").Scan(&entityCount); err != nil {
		t.Fatalf("count entities: %v", err)
	}
	if entityCount != 2 {
		t.Errorf("entities = %d, want 2 preserved", entityCount)
	}
}
