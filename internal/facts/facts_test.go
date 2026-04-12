package facts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func setupTestDB(t *testing.T) (*storage.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	return db, func() {
		db.Close()
		os.Remove(dbPath)
	}
}

func TestInsertAndQuery(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)

	f := Fact{
		SourceFile:    "raw/inbox/test.pdf",
		Value:         "5.2亿元",
		Numeric:       5.2,
		NumberType:    "monetary",
		Entity:        "希烽光电",
		EntityType:    "company",
		Period:        "2024",
		PeriodType:    "actual",
		SemanticLabel: "营业收入",
		ExactQuote:    "营业收入 5.2 亿元",
		SchemaVersion: "1.0",
	}

	// 插入
	err := store.Insert(f)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// 查询
	results, err := store.Query(QueryOpts{Entity: "希烽光电"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].Numeric != 5.2 {
		t.Errorf("expected numeric=5.2, got %f", results[0].Numeric)
	}
	if results[0].SemanticLabel != "营业收入" {
		t.Errorf("expected label=营业收入, got %s", results[0].SemanticLabel)
	}
}

func TestUpsertDedup(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)

	f := Fact{
		SourceFile:    "raw/inbox/test.pdf",
		Value:         "5.2亿元",
		Numeric:       5.2,
		Entity:        "希烽光电",
		Period:        "2024",
		SemanticLabel: "营业收入",
		ExactQuote:    "营业收入 5.2 亿元",
		SchemaVersion: "1.0",
	}

	// 插入两次，应只有一条
	if err := store.Insert(f); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if err := store.Insert(f); err != nil {
		t.Fatalf("second insert: %v", err)
	}

	results, err := store.Query(QueryOpts{Entity: "希烽光电"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 (dedup), got %d", len(results))
	}
}

func TestDeleteBySource(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)

	for _, src := range []string{"file1.pdf", "file2.pdf"} {
		err := store.Insert(Fact{
			SourceFile:    src,
			Value:         "100",
			Numeric:       100,
			Entity:        "A公司",
			SemanticLabel: "营收",
			ExactQuote:    "营收100",
			SchemaVersion: "1.0",
		})
		if err != nil {
			t.Fatalf("insert %s: %v", src, err)
		}
	}

	deleted, err := store.DeleteBySource("file1.pdf")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if deleted != 1 {
		t.Errorf("expected 1 deleted, got %d", deleted)
	}

	results, err := store.Query(QueryOpts{})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 remaining, got %d", len(results))
	}
}

func TestQueryByPeriod(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)

	for _, p := range []string{"2023", "2024", "2025E"} {
		err := store.Insert(Fact{
			SourceFile:    "test.pdf",
			Value:         "100",
			Numeric:       100,
			Entity:        "B公司",
			Period:        p,
			SemanticLabel: "营收" + p,
			ExactQuote:    "营收100 " + p,
			SchemaVersion: "1.0",
		})
		if err != nil {
			t.Fatalf("insert %s: %v", p, err)
		}
	}

	results, err := store.Query(QueryOpts{Period: "2024"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 for period 2024, got %d", len(results))
	}
}

func TestQueryByLabel(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)

	for _, label := range []string{"营业收入", "净利润", "毛利率"} {
		err := store.Insert(Fact{
			SourceFile:    "test.pdf",
			Value:         "100",
			Numeric:       100,
			Entity:        "C公司",
			SemanticLabel: label,
			ExactQuote:    label + " 100",
			SchemaVersion: "1.0",
		})
		if err != nil {
			t.Fatalf("insert %s: %v", label, err)
		}
	}

	results, err := store.Query(QueryOpts{Label: "净利润"})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 for label 净利润, got %d", len(results))
	}
}

func TestStats(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewStore(db)

	facts := []Fact{
		{SourceFile: "a.pdf", Value: "1", Numeric: 1, Entity: "X", SemanticLabel: "rev", ExactQuote: "q1", SchemaVersion: "1.0"},
		{SourceFile: "a.pdf", Value: "2", Numeric: 2, Entity: "Y", SemanticLabel: "cost", ExactQuote: "q2", SchemaVersion: "1.0"},
		{SourceFile: "b.pdf", Value: "3", Numeric: 3, Entity: "X", SemanticLabel: "rev2", ExactQuote: "q3", SchemaVersion: "1.0"},
	}
	for _, f := range facts {
		if err := store.Insert(f); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	s, err := store.Stats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if s.TotalFacts != 3 {
		t.Errorf("expected 3 total, got %d", s.TotalFacts)
	}
	if s.UniqueEntities != 2 {
		t.Errorf("expected 2 entities, got %d", s.UniqueEntities)
	}
	if s.UniqueSources != 2 {
		t.Errorf("expected 2 sources, got %d", s.UniqueSources)
	}
}
