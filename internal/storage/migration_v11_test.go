package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// buildV10Fixture creates a V10 DB from the real migrationV1..V10 consts —
// never hand-copied schema SQL, which drifts from the runner silently — then
// opens it through Open so the pending V11 migration actually runs.
func buildV10Fixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v10.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	consts := []string{
		migrationV1, migrationV2, migrationV3, migrationV4, migrationV5,
		migrationV6, migrationV7, migrationV8, migrationV9, migrationV10,
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

func TestMigrationV11CreatesAliasTable(t *testing.T) {
	db := buildV10Fixture(t)

	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != 15 {
		t.Fatalf("schema version = %d, want 15", version)
	}

	rows, err := db.ReadDB().Query("PRAGMA table_info(entity_aliases)")
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
	for _, col := range []string{
		"alias", "canonical_id", "entity_type", "status", "confidence",
		"reason", "source", "created_at", "decided_at", "decided_by",
	} {
		if !have[col] {
			t.Errorf("entity_aliases.%s missing after V11", col)
		}
	}
}

func TestMigrationV11CreatesIndexes(t *testing.T) {
	db := buildV10Fixture(t)

	rows, err := db.ReadDB().Query(
		`SELECT name FROM sqlite_master WHERE type='index' AND tbl_name='entity_aliases'`)
	if err != nil {
		t.Fatalf("query indexes: %v", err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("index rows: %v", err)
	}
	for _, idx := range []string{
		"idx_entity_aliases_active",
		"idx_entity_aliases_canonical",
		"idx_entity_aliases_status",
	} {
		if !have[idx] {
			t.Errorf("index %s missing after V11", idx)
		}
	}
}

// The partial unique index is a hard constraint, not documentation: it is what
// stops a second ACTIVE row for one alias. Spec §2.1 — a violation here is a
// non-target unique violation that ON CONFLICT (alias, canonical_id) does NOT
// absorb, so it aborts the enclosing transaction. The resolution pass guards
// against reaching it; this test pins that the guard is guarding something real.
func TestMigrationV11ActiveIndexRejectsSecondActiveRow(t *testing.T) {
	db := buildV10Fixture(t)

	ins := func(alias, canonical, status string) error {
		return db.WriteTx(func(tx *sql.Tx) error {
			_, err := tx.Exec(
				`INSERT INTO entity_aliases
				   (alias, canonical_id, entity_type, status, source, created_at)
				 VALUES (?, ?, 'concept', ?, 'llm', '2026-07-26T00:00:00Z')`,
				alias, canonical, status)
			return err
		})
	}

	if err := ins("a", "c1", "applied"); err != nil {
		t.Fatalf("first applied row: %v", err)
	}
	// Same alias, different canonical, also active -> rejected by the index.
	err := ins("a", "c2", "pending")
	if err == nil {
		t.Fatal("second ACTIVE row for one alias was accepted; the partial unique index is not enforcing")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Errorf("want a UNIQUE constraint failure, got: %v", err)
	}

	// A rejected row for the same alias is NOT active, so it must be allowed —
	// this is why the key is (alias, canonical_id) and the index is partial:
	// rejections accumulate freely, and rejecting one pair must never block the
	// alias from resolving to something else later.
	if err := ins("a", "c3", "rejected"); err != nil {
		t.Errorf("rejected row for an alias with an active row must be allowed: %v", err)
	}
	if err := ins("a", "c4", "rejected"); err != nil {
		t.Errorf("a second rejected row must be allowed: %v", err)
	}
}

// Pre-V11 data is untouched: V11 creates a table and rebuilds nothing.
// Pre-V11 rows must survive the migration. The data has to be written BEFORE
// Open runs the migration — inserting afterwards only proves that INSERT then
// SELECT works, which is not what this is for.
func TestMigrationV11PreservesExistingData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v10-with-data.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	consts := []string{
		migrationV1, migrationV2, migrationV3, migrationV4, migrationV5,
		migrationV6, migrationV7, migrationV8, migrationV9, migrationV10,
	}
	for i, sqlText := range consts {
		if i == 3 {
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
	// Pre-V11 data, written at V10.
	if _, err := raw.Exec(
		`INSERT INTO entities (id, type, name, definition, article_path, created_at, updated_at)
		 VALUES ('alpha','concept','Alpha','a definition','wiki/alpha.md','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		        ('beta','technique','Beta','','','2026-01-02T00:00:00Z','2026-01-02T00:00:00Z')`); err != nil {
		t.Fatalf("seed entities: %v", err)
	}
	if _, err := raw.Exec(
		`INSERT INTO relations (id, source_id, target_id, relation, created_at, evidence, confidence)
		 VALUES ('a-ext-b','alpha','beta','extends','2026-01-03T00:00:00Z','alpha extends beta',0.7)`); err != nil {
		t.Fatalf("seed relation: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := Open(path) // migrates V10 -> V11
	if err != nil {
		t.Fatalf("open (migrates): %v", err)
	}
	t.Cleanup(func() { db.Close() })

	var id, typ, name, def, ap string
	if err := db.ReadDB().QueryRow(
		`SELECT id, type, name, COALESCE(definition,''), COALESCE(article_path,'')
		 FROM entities WHERE id='alpha'`).Scan(&id, &typ, &name, &def, &ap); err != nil {
		t.Fatalf("read pre-V11 entity: %v", err)
	}
	if typ != "concept" || name != "Alpha" || def != "a definition" || ap != "wiki/alpha.md" {
		t.Errorf("pre-V11 entity altered by the migration: %s/%s/%s/%s", typ, name, def, ap)
	}

	var src, tgt, rel, evidence string
	var conf float64
	if err := db.ReadDB().QueryRow(
		`SELECT source_id, target_id, relation, COALESCE(evidence,''), COALESCE(confidence,0)
		 FROM relations WHERE id='a-ext-b'`).Scan(&src, &tgt, &rel, &evidence, &conf); err != nil {
		t.Fatalf("read pre-V11 relation: %v", err)
	}
	if src != "alpha" || tgt != "beta" || rel != "extends" ||
		evidence != "alpha extends beta" || conf != 0.7 {
		t.Errorf("pre-V11 relation altered: %s -[%s]-> %s ev=%q conf=%v", src, rel, tgt, evidence, conf)
	}

	var n int
	if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM entities").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("entities = %d, want 2 preserved", n)
	}
}
