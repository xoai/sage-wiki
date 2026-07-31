package prompts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRegistriesIsolateInOneProcess is the SPEC-01 isolation guarantee: two
// registries with different override dirs render differently in one
// process, and neither perturbs the package default.
func TestRegistriesIsolateInOneProcess(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	write := func(dir, body string) {
		if err := os.WriteFile(filepath.Join(dir, "summarize-article.md"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(dirA, "CUSTOM-A {{.SourcePath}}")
	write(dirB, "CUSTOM-B {{.SourcePath}}")

	ra := NewRegistry()
	rb := NewRegistry()
	if err := ra.LoadFromDir(dirA); err != nil {
		t.Fatal(err)
	}
	if err := rb.LoadFromDir(dirB); err != nil {
		t.Fatal(err)
	}

	outA, err := ra.Render("summarize_article", SummarizeData{SourcePath: "x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	outB, err := rb.Render("summarize_article", SummarizeData{SourcePath: "x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(outA, "CUSTOM-A") {
		t.Errorf("registry A rendered %q", outA)
	}
	if !strings.HasPrefix(outB, "CUSTOM-B") {
		t.Errorf("registry B rendered %q", outB)
	}

	// Package default untouched by the instance loads.
	def, err := Render("summarize_article", SummarizeData{SourcePath: "x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(def, "CUSTOM-") {
		t.Error("package default registry was polluted by instance LoadFromDir")
	}
}
