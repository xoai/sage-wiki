package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/xoai/sage-wiki/internal/store"
)

// TestCompileKeyStore_RoundTripPG is spec test 10's postgres half: the
// compile_key/compile_key_parts columns round-trip and ClearCompileKey
// empties them (SPEC-04 F-005: both backends carry the dedup state).
func TestCompileKeyStore_RoundTripPG(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("ckpg_%d", time.Now().UnixNano())
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

	b, err := Open(swapDB(dsn, dbName), store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 8})
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	defer b.Close()

	items := b.CompileItems()
	it := store.CompileItem{
		SourcePath: "raw/pg.md", Hash: "sha256:pg", FileType: "article",
		Tier: 3, TierDefault: 3, SourceType: "compiler",
	}
	if err := items.Upsert(it); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := items.GetByPath("raw/pg.md")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatal("row missing after upsert")
	}
	if got.CompileKey != "" {
		t.Errorf("fresh row CompileKey = %q, want empty", got.CompileKey)
	}

	parts := `{"source":"sha256:pg","pipeline":"1","templates":"","models":"","config":"deadbeef","embed":"openai:m:8"}`
	if err := items.SetCompileKey("raw/pg.md", "cafebabe5678", parts); err != nil {
		t.Fatalf("SetCompileKey: %v", err)
	}
	got2, err := items.GetByPath("raw/pg.md")
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if got2.CompileKey != "cafebabe5678" {
		t.Errorf("CompileKey = %q, want cafebabe5678", got2.CompileKey)
	}
	if got2.CompileKeyParts != parts {
		t.Errorf("CompileKeyParts = %q, want %q", got2.CompileKeyParts, parts)
	}

	if err := items.ClearCompileKey("raw/pg.md"); err != nil {
		t.Fatalf("ClearCompileKey: %v", err)
	}
	got3, _ := items.GetByPath("raw/pg.md")
	if got3.CompileKey != "" || got3.CompileKeyParts != "" {
		t.Errorf("after clear: key=%q parts=%q, want both empty", got3.CompileKey, got3.CompileKeyParts)
	}
}

// TestMigrationV9CompileKeyColumns: a pre-v9 database (clone trimmed to v8)
// gains the columns on open and keeps serving reader opens (the parity rule
// the other migration legs pin).
func TestMigrationV9CompileKeyColumns(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("ckv9_%d", time.Now().UnixNano())
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

	// Trim to v8 state: drop the v9 columns and the version row.
	trim, err := Open(swapDB(dsn, dbName), store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 8})
	if err != nil {
		t.Fatalf("open for trim: %v", err)
	}
	pool := trim.(*backend).pool
	if _, err := pool.ExecContext(context.Background(), "ALTER TABLE compile_items DROP COLUMN IF EXISTS compile_key"); err != nil {
		t.Fatalf("drop key: %v", err)
	}
	if _, err := pool.ExecContext(context.Background(), "ALTER TABLE compile_items DROP COLUMN IF EXISTS compile_key_parts"); err != nil {
		t.Fatalf("drop parts: %v", err)
	}
	if _, err := pool.ExecContext(context.Background(), "DELETE FROM schema_version WHERE version = 9"); err != nil {
		t.Fatalf("rollback version: %v", err)
	}
	trim.Close()

	re, err := Open(swapDB(dsn, dbName), store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 8})
	if err != nil {
		t.Fatalf("reopen after trim: %v", err)
	}
	defer re.Close()
	if err := re.CompileItems().SetCompileKey("raw/v9.md", "k", "{}"); err != nil {
		t.Fatalf("SetCompileKey after re-migration: %v", err)
	}
}

// TestOpenOptionsNow_LandsInTimestamps proves the D4 postgres clock path
// (Gate-2 review): an opener-supplied Now lands in created_at/updated_at.
func TestOpenOptionsNow_LandsInTimestamps(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("cknow_%d", time.Now().UnixNano())
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

	fixed := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	b, err := Open(swapDB(dsn, dbName), store.OpenOptions{
		Mode: store.ModeWriter, VectorDimension: 8,
		Now: func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	defer b.Close()

	items := b.CompileItems()
	if err := items.Upsert(store.CompileItem{SourcePath: "raw/now.md", Hash: "sha256:n", FileType: "article", Tier: 3, TierDefault: 3, SourceType: "compiler"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := items.GetByPath("raw/now.md")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.CreatedAt != "2023-11-14 22:13:20" {
		t.Errorf("CreatedAt = %q, want 2023-11-14 22:13:20 (OpenOptions.Now)", got.CreatedAt)
	}
	if got.UpdatedAt != "2023-11-14 22:13:20" {
		t.Errorf("UpdatedAt = %q, want 2023-11-14 22:13:20 (OpenOptions.Now)", got.UpdatedAt)
	}
}

// TestPostgresStore_ClassifyPrimitives proves the store primitives the
// skip classifier depends on (GetByPath key read, InvalidatePasses flag
// zeroing) work on postgres — the classifier itself is exercised in
// internal/compiler/dedup_skip_test.go against SQLite (same interface).
func TestPostgresStore_ClassifyPrimitives(t *testing.T) {
	dsn := migrationTestDSN(t)
	dbName := fmt.Sprintf("ckcls_%d", time.Now().UnixNano())
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

	b, err := Open(swapDB(dsn, dbName), store.OpenOptions{Mode: store.ModeWriter, VectorDimension: 8})
	if err != nil {
		t.Fatalf("open backend: %v", err)
	}
	defer b.Close()

	items := b.CompileItems()
	it := store.CompileItem{
		SourcePath: "raw/pg.md", Hash: "sha256:pg", FileType: "article",
		Tier: 3, TierDefault: 3, SourceType: "compiler",
		PassIndexed: true, PassEmbedded: true, PassSummarized: true, PassExtracted: true, PassWritten: true,
	}
	if err := items.Upsert(it); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := items.SetCompileKey("raw/pg.md", "k", "{}"); err != nil {
		t.Fatalf("set key: %v", err)
	}
	got, err := items.GetByPath("raw/pg.md")
	if err != nil || got == nil {
		t.Fatalf("get: %v", err)
	}
	if got.CompileKey != "k" {
		t.Errorf("CompileKey = %q, want k — the classifier's R4 read fails without it", got.CompileKey)
	}
	if err := items.InvalidatePasses("raw/pg.md"); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	got2, _ := items.GetByPath("raw/pg.md")
	if got2.PassWritten || got2.PassEmbedded || got2.PassIndexed {
		t.Errorf("flags not zeroed: %+v — R5 drift reset broken on postgres", got2)
	}
}
