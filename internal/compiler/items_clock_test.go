package compiler

import (
	"testing"
	"time"
)

func TestCompileItemStore_ClockInjection(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	fixed := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)
	store := NewCompileItemStore(db, func() time.Time { return fixed })

	item := CompileItem{
		SourcePath:  "raw/clock.md",
		Hash:        "sha256:clock",
		FileType:    "article",
		SizeBytes:   10,
		Tier:        3,
		TierDefault: 3,
		SourceType:  "compiler",
	}
	if err := store.Upsert(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := store.GetByPath("raw/clock.md")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	want := "2023-11-14 22:13:20" // SQLite datetime('now')-compatible format
	if got.CreatedAt != want {
		t.Errorf("CreatedAt = %q, want %q (injected clock)", got.CreatedAt, want)
	}
	if got.UpdatedAt != want {
		t.Errorf("UpdatedAt = %q, want %q (injected clock)", got.UpdatedAt, want)
	}

	later := fixed.Add(time.Hour)
	store2 := NewCompileItemStore(db, func() time.Time { return later })
	if err := store2.MarkPass("raw/clock.md", "indexed"); err != nil {
		t.Fatalf("mark pass: %v", err)
	}
	got2, err := store.GetByPath("raw/clock.md")
	if err != nil {
		t.Fatalf("get 2: %v", err)
	}
	if got2.UpdatedAt != "2023-11-14 23:13:20" {
		t.Errorf("after MarkPass, UpdatedAt = %q, want %q", got2.UpdatedAt, "2023-11-14 23:13:20")
	}
	if got2.CreatedAt != want {
		t.Errorf("after MarkPass, CreatedAt = %q, want unchanged %q", got2.CreatedAt, want)
	}
}
