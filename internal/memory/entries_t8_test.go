package memory

import (
	"database/sql"
	"testing"
)

func TestListAllEntries(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)
	s.Add(Entry{ID: "src:a.md", Content: "alpha body", Tags: []string{"x"}, ArticlePath: "wiki/a.md"})
	s.Add(Entry{ID: "src:b.md", Content: "beta body"})

	all, err := s.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll len = %d, want 2", len(all))
	}
	byID := map[string]Entry{}
	for _, e := range all {
		byID[e.ID] = e
	}
	if byID["src:a.md"].Content != "alpha body" || byID["src:a.md"].ArticlePath != "wiki/a.md" {
		t.Errorf("entry a mismatch: %+v", byID["src:a.md"])
	}
	if len(byID["src:a.md"].Tags) != 1 || byID["src:a.md"].Tags[0] != "x" {
		t.Errorf("entry a tags mismatch: %+v", byID["src:a.md"].Tags)
	}
}

func TestCountUncompiled(t *testing.T) {
	db := openTestDB(t)
	s := NewStore(db)
	s.Add(Entry{ID: "src:a.md", Content: "unique zebra token"})
	s.Add(Entry{ID: "src:b.md", Content: "unique zebra token"})
	// Seed compile_items with raw SQL (importing compiler here would close a
	// test import cycle: compiler imports memory).
	db.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO compile_items (source_path, tier) VALUES ('a.md', 1)"); err != nil {
			return err
		}
		_, err := tx.Exec("INSERT INTO compile_items (source_path, tier) VALUES ('b.md', 3)")
		return err
	})

	// tier<3 uncompiled: only a.md (tier 1) counts; b.md is tier 3.
	n, err := s.CountUncompiled("zebra")
	if err != nil {
		t.Fatalf("CountUncompiled: %v", err)
	}
	if n != 1 {
		t.Errorf("CountUncompiled(zebra) = %d, want 1", n)
	}

	// Empty/garbage query → 0 (sanitize guard, mcp:301 parity).
	if n, _ := s.CountUncompiled(""); n != 0 {
		t.Errorf("CountUncompiled(empty) = %d, want 0", n)
	}
	if n, _ = s.CountUncompiled("no-such-token-xyz"); n != 0 {
		t.Errorf("CountUncompiled(miss) = %d, want 0", n)
	}
}

func TestListAllChunks(t *testing.T) {
	db := openTestDB(t)
	cs := NewChunkStore(db)
	db.WriteTx(func(tx *sql.Tx) error {
		return cs.IndexChunks(tx, "doc1", []ChunkEntry{
			{ChunkID: "c1", ChunkIndex: 0, Heading: "H1", Content: "body one", StartOffset: 0, EndOffset: 8},
			{ChunkID: "c2", ChunkIndex: 1, Heading: "H2", Content: "body two", StartOffset: 9, EndOffset: 17},
		})
	})

	all, err := cs.ListAll()
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListAll len = %d, want 2", len(all))
	}
	if all[0].ChunkID != "c1" || all[0].DocID != "doc1" || all[0].Content != "body one" || all[0].Heading != "H1" {
		t.Errorf("chunk 0 mismatch: %+v", all[0])
	}
}
