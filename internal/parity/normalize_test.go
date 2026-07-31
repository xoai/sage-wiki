package parity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeFrontmatterAndChangelog(t *testing.T) {
	fm := "---\nsource: raw/a.md\ncompiled_at: 2026-08-01T10:00:00Z\n---\n\n# Body 2026-08-01T10:00:00Z stays\n"
	got := string(Normalize([]byte(fm)))
	if !strings.Contains(got, "compiled_at: <TS>") {
		t.Errorf("frontmatter ts not normalized: %q", got)
	}
	if !strings.Contains(got, "# Body 2026-08-01T10:00:00Z stays") {
		t.Errorf("body ts must NOT be normalized: %q", got)
	}

	ch := "# CHANGELOG\n\n## 2026-08-01T10:00:00Z\n\n- Added: 3\n"
	got2 := string(Normalize([]byte(ch)))
	if !strings.Contains(got2, "## <TS>") {
		t.Errorf("changelog heading not normalized: %q", got2)
	}
}

func TestBuildWorkspaceSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke compiles a workspace")
	}
	corpus := t.TempDir()
	for name, body := range map[string]string{
		"a.md": "# Alpha\n\nAlpha discusses beta systems and gamma rays in detail.\n",
		"b.md": "# Beta\n\nBeta systems handle gamma ray processing pipelines.\n",
	} {
		if err := os.WriteFile(filepath.Join(corpus, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	origin := NewOriginServer()
	defer origin.Close()

	dir1 := filepath.Join(t.TempDir(), "ws1")
	if err := BuildWorkspace(corpus, dir1, origin.URL, ""); err != nil {
		t.Fatalf("BuildWorkspace: %v", err)
	}
	dir2 := filepath.Join(t.TempDir(), "ws2")
	if err := BuildWorkspace(corpus, dir2, origin.URL, ""); err != nil {
		t.Fatalf("BuildWorkspace 2: %v", err)
	}

	h1, err := treeHashes(filepath.Join(dir1, "wiki"))
	if err != nil {
		t.Fatal(err)
	}
	h2, err := treeHashes(filepath.Join(dir2, "wiki"))
	if err != nil {
		t.Fatal(err)
	}
	if len(h1) == 0 {
		t.Fatal("compile produced no wiki files")
	}
	for k, v := range h1 {
		if h2[k] != v {
			t.Errorf("nondeterministic output at %s", k)
		}
	}
}
