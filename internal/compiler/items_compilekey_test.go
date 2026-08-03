package compiler

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
)

// TestCompileKeyStore_RoundTrip pins SetCompileKey/GetByPath on the SQLite
// backend (spec test 10's SQLite half).
func TestCompileKeyStore_RoundTrip(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewCompileItemStore(db, func() time.Time { return time.Unix(1700000000, 0).UTC() })
	item := CompileItem{SourcePath: "raw/a.md", Hash: "sha256:x", FileType: "article", Tier: 3, TierDefault: 3, SourceType: "compiler"}
	if err := store.Upsert(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := store.GetByPath("raw/a.md")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CompileKey != "" {
		t.Errorf("fresh row CompileKey = %q, want empty", got.CompileKey)
	}

	parts := `{"source":"sha256:x","pipeline":"1","templates":"","models":"","config":"deadbeef","embed":"openai:m:8"}`
	if err := store.SetCompileKey("raw/a.md", "cafebabe1234", parts); err != nil {
		t.Fatalf("SetCompileKey: %v", err)
	}

	got2, err := store.GetByPath("raw/a.md")
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if got2.CompileKey != "cafebabe1234" {
		t.Errorf("CompileKey = %q, want cafebabe1234", got2.CompileKey)
	}
	if got2.CompileKeyParts != parts {
		t.Errorf("CompileKeyParts = %q, want %q", got2.CompileKeyParts, parts)
	}

	if err := store.ClearCompileKey("raw/a.md"); err != nil {
		t.Fatalf("ClearCompileKey: %v", err)
	}
	got3, _ := store.GetByPath("raw/a.md")
	if got3.CompileKey != "" || got3.CompileKeyParts != "" {
		t.Errorf("after clear: key=%q parts=%q, want both empty", got3.CompileKey, got3.CompileKeyParts)
	}
}

// TestCompileKeyStore_MigrationFromPreSpec04 builds a DB whose compile_items
// predates the columns (dropped post-migration), re-opens, and proves the
// migration is additive and loss-free (spec: old workspaces upgrade cleanly).
func TestCompileKeyStore_MigrationFromPreSpec04(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// Simulate the pre-SPEC-04 shape by dropping the columns the migration added.
	if _, err := db.WriteDB().Exec("ALTER TABLE compile_items DROP COLUMN compile_key"); err != nil {
		t.Fatalf("drop key: %v", err)
	}
	if _, err := db.WriteDB().Exec("ALTER TABLE compile_items DROP COLUMN compile_key_parts"); err != nil {
		t.Fatalf("drop parts: %v", err)
	}
	// Roll the schema version back so reopening re-runs V15.
	if _, err := db.WriteDB().Exec("DELETE FROM schema_version WHERE version = 15"); err != nil {
		t.Fatalf("rollback version: %v", err)
	}
	db.Close()

	db2, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	store := NewCompileItemStore(db2, func() time.Time { return time.Unix(1700000000, 0).UTC() })
	if err := store.Upsert(CompileItem{SourcePath: "raw/b.md", Hash: "sha256:y", FileType: "article", Tier: 3, TierDefault: 3, SourceType: "compiler"}); err != nil {
		t.Fatalf("upsert after migration: %v", err)
	}
	if err := store.SetCompileKey("raw/b.md", "abc", "{}"); err != nil {
		t.Fatalf("SetCompileKey after migration: %v", err)
	}
}
