package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
)

func TestListBelowQualityScore(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	s := NewCompileItemStore(db, config.NowUTC)

	low := 0.3
	high := 0.9
	s.Upsert(CompileItem{SourcePath: "a.md", Tier: 3})
	s.Upsert(CompileItem{SourcePath: "b.md", Tier: 3})
	s.Upsert(CompileItem{SourcePath: "c.md", Tier: 3})
	s.SetQualityScore("a.md", low)
	s.SetQualityScore("b.md", high)
	// c.md has NULL quality_score — excluded by the predicate.

	rows, err := s.ListBelowQualityScore(0.5)
	if err != nil {
		t.Fatalf("ListBelowQualityScore: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("len = %d, want 1 (%+v)", len(rows), rows)
	}
	if rows[0].SourcePath != "a.md" || rows[0].Score != low {
		t.Errorf("row = %+v, want a.md/0.3", rows[0])
	}
}
