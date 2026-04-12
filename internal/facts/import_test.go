package facts

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

func setupImportTest(t *testing.T) (string, *storage.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}

	// 创建 .pre-extracted 结构
	preDir := filepath.Join(dir, ".pre-extracted", "files", "inbox")
	os.MkdirAll(preDir, 0755)

	// 写 extract-meta.yaml
	metaYAML := `schema_version: "1.0"
extractor: file-extract
extractor_version: "0.1.0"
extracted_at: "2026-04-12T14:30:00"
`
	os.WriteFile(filepath.Join(dir, ".pre-extracted", "extract-meta.yaml"), []byte(metaYAML), 0644)

	return dir, db, func() {
		db.Close()
		os.RemoveAll(dir)
	}
}

func writeNumbersYAML(t *testing.T, dir, relPath, content string) {
	t.Helper()
	fullPath := filepath.Join(dir, ".pre-extracted", "files", relPath)
	os.MkdirAll(filepath.Dir(fullPath), 0755)
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", relPath, err)
	}
}

func TestImportBasic(t *testing.T) {
	dir, db, cleanup := setupImportTest(t)
	defer cleanup()

	numbersYAML := `numbers:
  - value: "5.2亿元"
    numeric: 520000000
    number_type: monetary
    entity: "希烽光电"
    entity_type: company
    period: "2024"
    period_type: actual
    semantic_label: "营业收入"
    source_file: "投资建议书.pdf"
    source_location: "P5"
    context_type: paragraph
    exact_quote: "2024年营业收入5.2亿元"
    extraction_method: ai_enrichment
`
	writeNumbersYAML(t, dir, "inbox/投资建议书.pdf.numbers.yaml", numbersYAML)

	store := NewStore(db)
	report, err := ImportDir(store, dir, nil)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.Added != 1 {
		t.Errorf("expected 1 added, got %d", report.Added)
	}
	if report.Errors != 0 {
		t.Errorf("expected 0 errors, got %d", report.Errors)
	}

	results, _ := store.Query(QueryOpts{Entity: "希烽光电"})
	if len(results) != 1 {
		t.Fatalf("expected 1 fact, got %d", len(results))
	}
	if results[0].Numeric != 520000000 {
		t.Errorf("expected numeric=520000000, got %f", results[0].Numeric)
	}
}

func TestImportUpsertNoDuplicate(t *testing.T) {
	dir, db, cleanup := setupImportTest(t)
	defer cleanup()

	numbersYAML := `numbers:
  - value: "100"
    numeric: 100
    entity: "A公司"
    semantic_label: "营收"
    exact_quote: "营收100"
`
	writeNumbersYAML(t, dir, "inbox/test.pdf.numbers.yaml", numbersYAML)

	store := NewStore(db)

	// 导入两次
	r1, _ := ImportDir(store, dir, nil)
	r2, _ := ImportDir(store, dir, nil)

	if r1.Added != 1 {
		t.Errorf("first import: expected 1 added, got %d", r1.Added)
	}
	// 第二次应全部跳过（upsert）
	if r2.Added != 0 {
		t.Errorf("second import: expected 0 added (dedup), got %d", r2.Added)
	}
	if r2.Skipped != 1 {
		t.Errorf("second import: expected 1 skipped, got %d", r2.Skipped)
	}
}

func TestImportCorruptedYAML(t *testing.T) {
	dir, db, cleanup := setupImportTest(t)
	defer cleanup()

	writeNumbersYAML(t, dir, "inbox/bad.pdf.numbers.yaml", "not: [valid yaml: {{{")

	store := NewStore(db)
	report, err := ImportDir(store, dir, nil)
	if err != nil {
		t.Fatalf("import should not fail entirely: %v", err)
	}
	if report.Errors != 1 {
		t.Errorf("expected 1 error for corrupt yaml, got %d", report.Errors)
	}
}

func TestImportBadSchemaVersion(t *testing.T) {
	dir, db, cleanup := setupImportTest(t)
	defer cleanup()

	// 覆写 extract-meta.yaml 为不兼容版本
	metaYAML := `schema_version: "99.0"
extractor: future-tool
`
	os.WriteFile(filepath.Join(dir, ".pre-extracted", "extract-meta.yaml"), []byte(metaYAML), 0644)

	numbersYAML := `numbers:
  - value: "100"
    numeric: 100
    entity: "A"
    semantic_label: "x"
    exact_quote: "x100"
`
	writeNumbersYAML(t, dir, "inbox/test.pdf.numbers.yaml", numbersYAML)

	store := NewStore(db)
	_, err := ImportDir(store, dir, nil)
	if err == nil {
		t.Error("expected error for incompatible schema version")
	}
}

func TestImportWithAliases(t *testing.T) {
	dir, db, cleanup := setupImportTest(t)
	defer cleanup()

	numbersYAML := `numbers:
  - value: "200"
    numeric: 200
    entity: "XiFeng Optics"
    semantic_label: "Net Profit"
    exact_quote: "net profit 200"
`
	writeNumbersYAML(t, dir, "inbox/en.pdf.numbers.yaml", numbersYAML)

	aliases := &Aliases{
		EntityAliases: map[string][]string{
			"希烽光电": {"XiFeng Optics", "希烽", "XiFeng"},
		},
		LabelAliases: map[string][]string{
			"净利润": {"Net Profit", "Net Income"},
		},
	}

	store := NewStore(db)
	report, err := ImportDir(store, dir, aliases)
	if err != nil {
		t.Fatalf("import with aliases: %v", err)
	}
	if report.Added != 1 {
		t.Errorf("expected 1 added, got %d", report.Added)
	}

	results, _ := store.Query(QueryOpts{Entity: "希烽光电"})
	if len(results) != 1 {
		t.Fatalf("expected 1 fact with canonical entity, got %d", len(results))
	}
	if results[0].SemanticLabel != "净利润" {
		t.Errorf("expected label=净利润, got %s", results[0].SemanticLabel)
	}
}
