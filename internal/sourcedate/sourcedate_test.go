package sourcedate

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// T3.2 chain: frontmatter date > mtime > manifest first-seen > 0.
func TestResolveSourceDateChain(t *testing.T) {
	dir := t.TempDir()

	// 1. Frontmatter date wins over mtime.
	fm := filepath.Join(dir, "dated.md")
	os.WriteFile(fm, []byte("---\ntitle: x\ndate: 2024-03-15\n---\n\nbody\n"), 0644)
	want := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	if got := Resolve(fm, ""); got != want {
		t.Errorf("frontmatter date = %d, want %d", got, want)
	}

	// RFC3339 frontmatter value.
	fm2 := filepath.Join(dir, "dated2.md")
	os.WriteFile(fm2, []byte("---\ndate: \"2023-07-01T10:30:00Z\"\n---\nbody\n"), 0644)
	want2 := time.Date(2023, 7, 1, 10, 30, 0, 0, time.UTC).Unix()
	if got := Resolve(fm2, ""); got != want2 {
		t.Errorf("rfc3339 frontmatter = %d, want %d", got, want2)
	}

	// 2. No frontmatter → mtime.
	plain := filepath.Join(dir, "plain.md")
	os.WriteFile(plain, []byte("no frontmatter here\n"), 0644)
	mt := time.Date(2022, 1, 2, 3, 4, 5, 0, time.UTC)
	os.Chtimes(plain, mt, mt)
	if got := Resolve(plain, ""); got != mt.Unix() {
		t.Errorf("mtime fallback = %d, want %d", got, mt.Unix())
	}

	// 3. Missing file → manifest first-seen.
	gone := filepath.Join(dir, "gone.md")
	if got := Resolve(gone, "2021-05-05T00:00:00Z"); got != time.Date(2021, 5, 5, 0, 0, 0, 0, time.UTC).Unix() {
		t.Errorf("manifest fallback = %d", got)
	}

	// 4. Nothing → 0 (absence, never fabricated).
	if got := Resolve(gone, ""); got != 0 {
		t.Errorf("dateless = %d, want 0", got)
	}

	// A body mention of `date:` past the frontmatter must not match.
	body := filepath.Join(dir, "body.md")
	os.WriteFile(body, []byte("no frontmatter\ndate: 2020-01-01\n"), 0644)
	os.Chtimes(body, mt, mt)
	if got := Resolve(body, ""); got != mt.Unix() {
		t.Errorf("body date matched without frontmatter block: %d", got)
	}

	// Frontmatter WITHOUT date + body `date:` inside the probe window —
	// the block boundary must stop the match (mtime wins).
	fmBody := filepath.Join(dir, "fmbody.md")
	os.WriteFile(fmBody, []byte("---\ntitle: x\n---\n\ntext\ndate: 2020-01-01\n"), 0644)
	os.Chtimes(fmBody, mt, mt)
	if got := Resolve(fmBody, ""); got != mt.Unix() {
		t.Errorf("body date leaked past the closing ---: %d, want mtime %d", got, mt.Unix())
	}
}

func TestMax(t *testing.T) {
	dates := map[string]int64{"src:a": 100, "src:b": 300, "src:c": 200}
	if got := Max(dates, []string{"src:a", "src:b", "src:c"}); got != 300 {
		t.Errorf("max = %d, want 300", got)
	}
	if got := Max(dates, []string{"src:absent"}); got != 0 {
		t.Errorf("absent-only = %d, want 0", got)
	}
}
