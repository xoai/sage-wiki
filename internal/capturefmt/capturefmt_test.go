package capturefmt

import (
	"testing"
)

func TestFrontmatterGolden(t *testing.T) {
	got, err := Frontmatter("cli-capture", "2026-07-31T10:00:00Z", "ml, attention", "from chat", "raw: true")
	if err != nil {
		t.Fatal(err)
	}
	want := "---\nsource: cli-capture\ncaptured_at: 2026-07-31T10:00:00Z\nraw: true\ntags: [ml, attention]\ncontext: \"from chat\"\n---\n\n"
	if got != want {
		t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestFrontmatterMinimal(t *testing.T) {
	got, err := Frontmatter("capture", "2026-07-31T10:00:00Z", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "---\nsource: capture\ncaptured_at: 2026-07-31T10:00:00Z\n---\n\n" {
		t.Errorf("minimal = %q", got)
	}
}

func TestFrontmatterRejectsBreakingTags(t *testing.T) {
	if _, err := Frontmatter("capture", "now", "a]b", ""); err == nil {
		t.Error("tag with ] must be rejected (would break the YAML)")
	}
	if _, err := Frontmatter("capture", "now", "a\nb", ""); err == nil {
		t.Error("tag with newline must be rejected")
	}
	if _, err := Frontmatter("bad\norigin", "now", "", ""); err == nil {
		t.Error("multi-line origin must be rejected")
	}
}
