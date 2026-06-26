package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/wiki"
	"gopkg.in/yaml.v3"
)

func TestWalkSourceDir_SymlinkedDirectory(t *testing.T) {
	dir := t.TempDir()

	// Real source directory outside the project
	realDir := filepath.Join(dir, "realsrc")
	os.MkdirAll(realDir, 0755)
	os.WriteFile(filepath.Join(realDir, "page1.md"), []byte("# Page 1"), 0644)
	os.WriteFile(filepath.Join(realDir, "page2.md"), []byte("# Page 2"), 0644)

	// Symlink inside a project
	projectDir := filepath.Join(dir, "project")
	os.MkdirAll(projectDir, 0755)
	linkPath := filepath.Join(projectDir, "raw")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var paths []string
	err := WalkSourceDir(linkPath, func(absPath, relPath string, _ os.DirEntry) error {
		paths = append(paths, relPath)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkSourceDir: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(paths), paths)
	}
}

func TestWalkSourceDir_PlainDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file.md"), []byte("hello"), 0644)

	var paths []string
	err := WalkSourceDir(dir, func(absPath, relPath string, _ os.DirEntry) error {
		paths = append(paths, relPath)
		return nil
	})
	if err != nil {
		t.Fatalf("WalkSourceDir: %v", err)
	}
	if len(paths) != 1 {
		t.Fatalf("expected 1 file, got %d", len(paths))
	}
	if paths[0] != "file.md" {
		t.Errorf("expected file.md, got %s", paths[0])
	}
}

func TestDiff_DiscoversFilesInSymlinkedSource(t *testing.T) {
	dir := t.TempDir()

	// Init a sage-wiki project
	projectDir := filepath.Join(dir, "wiki")
	if err := wiki.InitGreenfield(projectDir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Create real source dir with files and symlink it in as "raw"
	realDir := filepath.Join(dir, "realsrc")
	os.MkdirAll(realDir, 0755)
	os.WriteFile(filepath.Join(realDir, "a.md"), []byte("# A"), 0644)
	os.WriteFile(filepath.Join(realDir, "b.md"), []byte("# B"), 0644)

	// Remove default raw dir, replace with symlink
	rawPath := filepath.Join(projectDir, "raw")
	os.RemoveAll(rawPath)
	if err := os.Symlink(realDir, rawPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cfg, _ := config.Load(filepath.Join(projectDir, "config.yaml"))
	mf, _ := manifest.Load(filepath.Join(projectDir, ".manifest.json"))

	diff, err := Diff(projectDir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}

	if len(diff.Added) != 2 {
		t.Fatalf("expected 2 added, got %d", len(diff.Added))
	}

	// Manifest paths must use the source name, not the real path
	for _, s := range diff.Added {
		if !strings.HasPrefix(s.Path, "raw/") {
			t.Errorf("manifest path %q should start with raw/", s.Path)
		}
	}

	// Diff should be idempotent — mark as compiled then re-diff
	// diff.Added is built from a map so its order is non-deterministic;
	// use each entry's own .Path/.Hash/.Type/.Size, not [0]/[1] indexing.
	for _, s := range diff.Added {
		mf.AddSource(s.Path, s.Hash, s.Type, s.Size)
		mf.MarkCompiled(s.Path, "wiki/summaries/"+filepath.Base(s.Path), nil)
	}
	mf.Save(filepath.Join(projectDir, ".manifest.json"))

	mf2, _ := manifest.Load(filepath.Join(projectDir, ".manifest.json"))
	diff2, _ := Diff(projectDir, cfg, mf2)
	if len(diff2.Added) != 0 || len(diff2.Modified) != 0 || len(diff2.Removed) != 0 {
		t.Errorf("diff after compile should be empty, got +%d ~%d -%d",
			len(diff2.Added), len(diff2.Modified), len(diff2.Removed))
	}
}

func TestDiff_SymlinkedSourceHonorsConfiguredType(t *testing.T) {
	dir := t.TempDir()

	projectDir := filepath.Join(dir, "wiki")
	if err := wiki.InitGreenfield(projectDir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Symlink raw -> real source dir
	realDir := filepath.Join(dir, "realsrc")
	os.MkdirAll(realDir, 0755)
	os.WriteFile(filepath.Join(realDir, "decision.md"), []byte("# Decision\nWe will use X."), 0644)
	rawPath := filepath.Join(projectDir, "raw")
	os.RemoveAll(rawPath)
	if err := os.Symlink(realDir, rawPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// Write config with type: adr on the raw source
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfgData := map[string]any{
		"version": 1,
		"project": "test",
		"pack":    "generic",
		"sources": []map[string]any{
			{"path": "raw", "type": "adr", "watch": false},
		},
		"output": "wiki",
	}
	rawYAML, _ := yaml.Marshal(cfgData)
	os.WriteFile(cfgPath, rawYAML, 0644)

	cfg, _ := config.Load(cfgPath)
	mf, _ := manifest.Load(filepath.Join(projectDir, ".manifest.json"))

	diff, err := Diff(projectDir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(diff.Added))
	}
	if diff.Added[0].Type != "adr" {
		t.Errorf("expected type=adr, got %q — configured type was dropped on symlinked source", diff.Added[0].Type)
	}
}

func TestDiff_AbsoluteSourcePathProducesRelativeManifestKeys(t *testing.T) {
	dir := t.TempDir()

	projectDir := filepath.Join(dir, "wiki")
	if err := wiki.InitGreenfield(projectDir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Create a source dir outside the project
	externalDir := filepath.Join(dir, "external")
	os.MkdirAll(externalDir, 0755)
	os.WriteFile(filepath.Join(externalDir, "note.md"), []byte("# External Note"), 0644)

	// Write config with an absolute source path
	cfgPath := filepath.Join(projectDir, "config.yaml")
	cfgData := map[string]any{
		"version": 1,
		"project": "test",
		"pack":    "generic",
		"sources": []map[string]any{
			{"path": externalDir, "watch": false},
		},
		"output": "wiki",
	}
	rawYAML, _ := yaml.Marshal(cfgData)
	os.WriteFile(cfgPath, rawYAML, 0644)

	cfg, _ := config.Load(cfgPath)
	mf, _ := manifest.Load(filepath.Join(projectDir, ".manifest.json"))

	diff, err := Diff(projectDir, cfg, mf)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if len(diff.Added) != 1 {
		t.Fatalf("expected 1 added, got %d", len(diff.Added))
	}
	// The manifest path must be relative so it survives across runs.
	// An absolute path here would cause a full re-add on the next compile.
	if filepath.IsAbs(diff.Added[0].Path) {
		t.Errorf("manifest path %q should be project-relative, not absolute", diff.Added[0].Path)
	}
}

func TestWalkSourceDir_BrokenSymlink(t *testing.T) {
	dir := t.TempDir()
	brokenLink := filepath.Join(dir, "broken")
	if err := os.Symlink(filepath.Join(dir, "nonexistent"), brokenLink); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	var count int
	err := WalkSourceDir(brokenLink, func(absPath, relPath string, d os.DirEntry) error {
		count++
		// relPath must never be absolute
		if filepath.IsAbs(relPath) {
			t.Errorf("relPath should be relative, got %q", relPath)
		}
		return nil
	})
	// We expect no crash; broken symlink resolves to nothing, EvalSymlinks
	// falls back to walking the symlink node itself (a single DirEntry).
	if err != nil {
		t.Fatalf("WalkSourceDir should not error on broken symlink: %v", err)
	}
	// The broken symlink node still appears as a DirEntry — it's on the
	// filesystem and IsDir() is false for symlinks.
	if count == 0 {
		t.Error("broken symlink should produce at least one DirEntry")
	}
}

func TestWalkSourceDir_RelPathNeverAbsolute(t *testing.T) {
	// Regression: ensure the filepath.Rel fallback never leaks an absolute
	// path through the callback.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.md"), []byte("x"), 0644)

	err := WalkSourceDir(dir, func(absPath, relPath string, d os.DirEntry) error {
		if filepath.IsAbs(relPath) {
			t.Errorf("relPath must be relative, got %q", relPath)
		}
		if !filepath.IsAbs(absPath) {
			t.Errorf("absPath must be absolute, got %q", absPath)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestWalkSourceDir_NestedSubdirectories(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "a.md"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(dir, "sub", "b.md"), []byte("b"), 0644)

	var relPaths []string
	err := WalkSourceDir(dir, func(absPath, relPath string, d os.DirEntry) error {
		relPaths = append(relPaths, relPath)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(relPaths) != 2 {
		t.Fatalf("expected 2 files, got %d: %v", len(relPaths), relPaths)
	}
	// There's no order guarantee from WalkDir, but we check both exist
	found := make(map[string]bool)
	for _, p := range relPaths {
		found[p] = true
	}
	if !found["a.md"] {
		t.Error("missing a.md")
	}
	if !found[filepath.Join("sub", "b.md")] {
		t.Error("missing sub/b.md")
	}
}

func TestAddRecursive_WatchesResolvedDirectory(t *testing.T) {
	dir := t.TempDir()

	// Real source dir with a subdirectory (to verify recursive walking)
	realDir := filepath.Join(dir, "realsrc")
	os.MkdirAll(filepath.Join(realDir, "sub"), 0755)
	os.WriteFile(filepath.Join(realDir, "a.md"), []byte("a"), 0644)
	os.WriteFile(filepath.Join(realDir, "sub", "b.md"), []byte("b"), 0644)

	// Symlink inside a project directory
	projectDir := filepath.Join(dir, "project")
	os.MkdirAll(projectDir, 0755)
	linkPath := filepath.Join(projectDir, "raw")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	w, err := fsnotify.NewWatcher()
	if err != nil {
		t.Skipf("fsnotify not available: %v", err)
	}
	defer w.Close()

	if err := addRecursive(w, linkPath); err != nil {
		t.Fatalf("addRecursive: %v", err)
	}

	// The real directory (resolved) should be watched, plus subdirs.
	// fsnotify stores watched paths; we check both the root and subdir.
	watched := w.WatchList()
	resolved, _ := filepath.EvalSymlinks(linkPath)

	foundRoot := false
	foundSub := false
	for _, p := range watched {
		if p == resolved {
			foundRoot = true
		}
		if p == filepath.Join(resolved, "sub") {
			foundSub = true
		}
	}
	if !foundRoot {
		t.Errorf("resolved path %q not in watch list: %v", resolved, watched)
	}
	if !foundSub {
		t.Errorf("resolved subpath %q not in watch list: %v", filepath.Join(resolved, "sub"), watched)
	}
}
