package storage

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// buildV7Fixture creates a V7 DB by executing the real migrationV1..V7
// consts (mirroring the runner's per-migration tx + disableFK for V4) plus
// schema_version rows 1-7 and representative data — no hand-copied schema
// (drift risk). It opens the file with a raw sql.DB because storage.Open
// would run ALL migrations including V8; the later storage.Open then runs
// ONLY V8.
func buildV7Fixture(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "v7.db")

	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}

	if _, err := raw.Exec("CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	consts := []string{migrationV1, migrationV2, migrationV3, migrationV4, migrationV5, migrationV6, migrationV7}
	for i, sqlText := range consts {
		if i == 3 { // V4 needs FK off, per the runner
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

	// Representative data: one entry + one chunk row whose content carries
	// 2- and 3-char-prefix-searchable terms.
	if _, err := raw.Exec(
		"INSERT INTO entries (id, content, tags, article_path) VALUES ('concept:attention', 'attention mechanism transformers', 'concept', 'wiki/concepts/attention.md')"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(
		"INSERT INTO chunks_fts (chunk_id, heading, content) VALUES ('concept:attention:0', 'Attention', 'attention mechanism transformers')"); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	return reopened
}

func TestMigrationV8_Upgrade(t *testing.T) {
	db := buildV7Fixture(t)
	defer db.Close()

	// Version reaches 8 via the schema_version TABLE (not user_version).
	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 15 {
		t.Errorf("MAX(version) = %d, want 15", version)
	}

	// Content preserved through the recreate+copy.
	var content string
	if err := db.ReadDB().QueryRow("SELECT content FROM entries WHERE id='concept:attention'").Scan(&content); err != nil {
		t.Fatalf("entry content lost in V8 rebuild: %v", err)
	}
	if !strings.Contains(content, "attention mechanism") {
		t.Errorf("entry content = %q", content)
	}
	if err := db.ReadDB().QueryRow("SELECT content FROM chunks_fts WHERE chunk_id='concept:attention:0'").Scan(&content); err != nil {
		t.Fatalf("chunk content lost in V8 rebuild: %v", err)
	}

	// Recreated tables carry prefix='2 3', the porter tokenizer, and
	// chunk_id UNINDEXED (the BM25-pollution guard).
	for _, table := range []string{"entries", "chunks_fts"} {
		var sql string
		if err := db.ReadDB().QueryRow("SELECT sql FROM sqlite_master WHERE name=?", table).Scan(&sql); err != nil {
			t.Fatalf("sqlite_master for %s: %v", table, err)
		}
		if !strings.Contains(sql, "prefix='2 3'") {
			t.Errorf("%s missing prefix='2 3': %s", table, sql)
		}
		if !strings.Contains(sql, "porter unicode61") {
			t.Errorf("%s lost porter tokenizer: %s", table, sql)
		}
	}
	var chunkSQL string
	if err := db.ReadDB().QueryRow("SELECT sql FROM sqlite_master WHERE name='chunks_fts'").Scan(&chunkSQL); err != nil {
		t.Fatalf("sqlite_master for chunks_fts: %v", err)
	}
	if !strings.Contains(chunkSQL, "chunk_id UNINDEXED") {
		t.Errorf("chunks_fts lost chunk_id UNINDEXED: %s", chunkSQL)
	}

	// Prefix queries return expected hits on BOTH tables (2- AND 3-char).
	var n int
	// 2-char AND 3-char prefixes (spec test 9 — both must be index-backed).
	for _, q := range []string{"at*", "me*", "tr*", "att*", "mec*", "tra*"} {
		if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM entries WHERE entries MATCH ?", q).Scan(&n); err != nil {
			t.Fatalf("entries prefix query %q: %v", q, err)
		}
		if n != 1 {
			t.Errorf("entries MATCH %q = %d, want 1", q, n)
		}
		if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM chunks_fts WHERE chunks_fts MATCH ?", q).Scan(&n); err != nil {
			t.Fatalf("chunks_fts prefix query %q: %v", q, err)
		}
		if n != 1 {
			t.Errorf("chunks_fts MATCH %q = %d, want 1", q, n)
		}
	}
}

func TestMigrationV8_FreshDB(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(filepath.Join(dir, "fresh.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	var version int
	if err := db.ReadDB().QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 15 {
		t.Errorf("fresh DB MAX(version) = %d, want 15", version)
	}
	var sql string
	if err := db.ReadDB().QueryRow("SELECT sql FROM sqlite_master WHERE name='entries'").Scan(&sql); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sql, "prefix='2 3'") {
		t.Errorf("fresh entries table missing prefix='2 3': %s", sql)
	}
}
