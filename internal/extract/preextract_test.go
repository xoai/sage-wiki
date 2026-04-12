package extract

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTryPreExtracted_Found(t *testing.T) {
	// 创建临时目录模拟 .pre-extracted/
	dir := t.TempDir()
	preDir := filepath.Join(dir, ".pre-extracted", "files", "inbox")
	os.MkdirAll(preDir, 0755)

	content := "---\npre_extracted: true\nconfidence: high\nengine: markitdown\n---\n# Test content"
	os.WriteFile(filepath.Join(preDir, "test.pdf.md"), []byte(content), 0644)

	sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc == nil {
		t.Fatal("expected SourceContent, got nil")
	}
	if sc.PreExtracted != true {
		t.Error("expected PreExtracted=true")
	}
	if sc.Confidence != "high" {
		t.Errorf("expected confidence=high, got %s", sc.Confidence)
	}
}

func TestTryPreExtracted_NotFound(t *testing.T) {
	dir := t.TempDir()
	sc, err := TryPreExtracted(dir, "raw/inbox/missing.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc != nil {
		t.Error("expected nil for missing pre-extracted file")
	}
}

func TestTryPreExtracted_LowConfidence(t *testing.T) {
	dir := t.TempDir()
	preDir := filepath.Join(dir, ".pre-extracted", "files", "inbox")
	os.MkdirAll(preDir, 0755)

	content := "---\npre_extracted: true\nconfidence: low\nengine: markitdown\n---\n# Low quality"
	os.WriteFile(filepath.Join(preDir, "test.pdf.md"), []byte(content), 0644)

	sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// low confidence 应返回 nil，让 Go 引擎处理
	if sc != nil {
		t.Error("expected nil for low confidence")
	}
}

func TestTryPreExtracted_CorruptedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	preDir := filepath.Join(dir, ".pre-extracted", "files", "inbox")
	os.MkdirAll(preDir, 0755)

	content := "not yaml frontmatter\n# Just content"
	os.WriteFile(filepath.Join(preDir, "test.pdf.md"), []byte(content), 0644)

	sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc != nil {
		t.Error("expected nil for corrupted frontmatter")
	}
}

func TestTryPreExtracted_NoPreExtractedDir(t *testing.T) {
	dir := t.TempDir()
	// 不创建 .pre-extracted/ 目录
	sc, err := TryPreExtracted(dir, "raw/inbox/test.pdf")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sc != nil {
		t.Error("expected nil when .pre-extracted/ doesn't exist")
	}
}
