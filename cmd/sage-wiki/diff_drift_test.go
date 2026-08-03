package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// TestRunDiff_NoFalseTemplateDriftWithOverride pins the Gate-1 finding:
// a workspace whose prompts/ override was COMPILED with the override must
// not show a perpetual 'templates' drift annotation on every doc — the
// diff path must hash the same effective templates compile used.
func TestRunDiff_NoFalseTemplateDriftWithOverride(t *testing.T) {
	dir := writeCompileableWorkspace(t)

	// Override BEFORE the compile: keys are stored WITH the override's hash.
	os.MkdirAll(filepath.Join(dir, "prompts"), 0755)
	if err := os.WriteFile(filepath.Join(dir, "prompts", "summarize-article.md"),
		[]byte("Summarize {{.SourcePath}} — test override body, long enough to render."), 0644); err != nil {
		t.Fatal(err)
	}
	compileWorkspaceForTest(t, dir)

	// Reset the package registry to pristine so the test proves runDiff
	// (re)loads overrides itself rather than inheriting compile's state.
	resetPromptsForTest(t)

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	out := captureStdout(t, func() {
		if err := runDiff(diffCmd, nil); err != nil {
			t.Fatalf("runDiff: %v", err)
		}
	})
	t.Logf("runDiff output:\n%s", out)
	if strings.Contains(out, "drift: templates") {
		t.Errorf("false 'templates' drift with a compiled-in override:\n%s", out)
	}

	// Positive control (anti-vacuity): remove the override and reset the
	// registry — the compiled-in override hash now mismatches, and the
	// annotation MUST surface 'drift: templates'.
	os.Remove(filepath.Join(dir, "prompts", "summarize-article.md"))
	resetPromptsForTest(t)
	out2 := captureStdout(t, func() {
		if err := runDiff(diffCmd, nil); err != nil {
			t.Fatalf("runDiff 2: %v", err)
		}
	})
	t.Logf("runDiff after override removal:\n%s", out2)
	if !strings.Contains(out2, "drift: templates") {
		t.Errorf("positive control failed: override removal should surface 'drift: templates':\n%s", out2)
	}

	// Leave the package registry pristine for the next test in the binary.
	resetPromptsForTest(t)
}

func resetPromptsForTest(t *testing.T) {
	t.Helper()
	prompts.ResetDefaultRegistryForTest()
}

// TestExplainJSON_RealShape is the non-vacuous replacement for the deleted
// JSONShape test (Gate-3 major): marshal the real explanation type and
// require every spec'd field present.
func TestExplainJSON_RealShape(t *testing.T) {
	dir := writeCompileableWorkspace(t)
	compileWorkspaceForTest(t, dir)

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()
	if err := compileCmd.Flags().Set("explain", "raw/a.md"); err != nil {
		t.Fatal(err)
	}
	defer compileCmd.Flags().Set("explain", "")

	out := captureStdout(t, func() {
		if err := runCompile(compileCmd, nil); err != nil {
			t.Fatalf("runCompile --explain: %v", err)
		}
	})
	for _, f := range []string{
		`"path"`, `"source_hash"`, `"pipeline"`, `"templates"`, `"models"`,
		`"config_hash"`, `"embed"`, `"key"`, `"stored_key"`,
		`"stored_parts"`, `"current_parts"`, `"verdict"`,
	} {
		if !strings.Contains(out, f) {
			t.Errorf("--explain --json output missing %s:\n%s", f, out)
		}
	}
	t.Logf("full --explain --json output (%d bytes): %q", len(out), out)
	if !strings.Contains(out, `"verdict": "skip: unchanged"`) {
		t.Errorf("verdict in --json output: %s", out)
	}
}

// writeCompileableWorkspace lays out a one-doc workspace with a stub LLM.
func writeCompileableWorkspace(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["input"] != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"embedding": []float64{0.1, 0.2, 0.3, 0.4}, "index": 0}},
			})
			return
		}
		messages, _ := body["messages"].([]any)
		var all string
		for _, m := range messages {
			if mm, ok := m.(map[string]any); ok {
				c, _ := mm["content"].(string)
				all += c
			}
		}
		var content string
		switch {
		case strings.Contains(all, "concept extraction system"):
			content = `[{"name": "alpha", "aliases": [], "sources": ["raw/a.md"], "type": "concept"}]`
		case strings.Contains(all, "wiki author writing a comprehensive article"):
			content = "# Alpha\n\nDiff drift test article with enough content to pass validation checks.\n\n## See also\n\n[[alpha]]"
		default:
			content = "## Key claims\n\nDiff drift test summary with enough length for the quality gate.\n\n## Concepts\n\nalpha: A concept."
		}
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]string{"content": content}}},
			"model":   "gpt-4o-mini",
			"usage":   map[string]int{"total_tokens": 50},
		})
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	wiki.InitGreenfield(dir, "difftest", "gpt-4o-mini")
	cfg := `version: 1
project: difftest
sources:
  - path: raw
    type: auto
    watch: false
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + srv.URL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 2
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "raw", "a.md"), []byte("# Alpha\n\nDiff drift test content."), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// compileWorkspaceForTest compiles the workspace via the compiler package
// (direct — the CLI's runCompile needs flags the classification doesn't).
func compileWorkspaceForTest(t *testing.T, dir string) {
	t.Helper()
	res, err := compiler.Compile(dir, compiler.CompileOpts{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	t.Logf("compile: %+v", res)
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var key string
	db.QueryRow("SELECT compile_key FROM compile_items WHERE source_path = 'raw/a.md'").Scan(&key)
	t.Logf("stored key after compile: %.16s (len %d)", key, len(key))
}

// captureStdout redirects os.Stdout for fn and returns what was printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()
	fn()
	w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// TestExplain_ForceKeepsResumeAttribution pins pass-3 obs 5: --explain
// --force on an interrupted doc reports 'compile: incomplete (resume)'
// (R0 wins over force), not 'compile: forced'.
func TestExplain_ForceKeepsResumeAttribution(t *testing.T) {
	dir := writeCompileableWorkspace(t)
	compileWorkspaceForTest(t, dir)

	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE compile_items SET pass_written = 0 WHERE source_path = 'raw/a.md'"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	if err := compileCmd.Flags().Set("explain", "raw/a.md"); err != nil {
		t.Fatal(err)
	}
	defer compileCmd.Flags().Set("explain", "")
	if err := compileCmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	defer compileCmd.Flags().Set("force", "false")

	out := captureStdout(t, func() {
		if err := runCompile(compileCmd, nil); err != nil {
			t.Fatalf("runCompile --explain --force: %v", err)
		}
	})
	if !strings.Contains(out, "compile: incomplete (resume)") {
		t.Errorf("--explain --force on interrupted doc should keep resume verdict:\n%s", out)
	}
	if strings.Contains(out, "compile: forced") {
		t.Errorf("--explain --force on interrupted doc must NOT say forced:\n%s", out)
	}
}

// TestExplain_ForceKeepsNewDocAttribution pins the verifier's NEW-1: a
// never-compiled doc under --explain --force keeps 'compile: content (new)'.
func TestExplain_ForceKeepsNewDocAttribution(t *testing.T) {
	dir := writeCompileableWorkspace(t)
	compileWorkspaceForTest(t, dir)

	// Add a brand-new doc (no compile_items row).
	if err := os.WriteFile(filepath.Join(dir, "raw", "new.md"), []byte("# Brand New\n\nNever compiled content."), 0644); err != nil {
		t.Fatal(err)
	}

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	if err := compileCmd.Flags().Set("explain", "raw/new.md"); err != nil {
		t.Fatal(err)
	}
	defer compileCmd.Flags().Set("explain", "")
	if err := compileCmd.Flags().Set("force", "true"); err != nil {
		t.Fatal(err)
	}
	defer compileCmd.Flags().Set("force", "false")

	out := captureStdout(t, func() {
		if err := runCompile(compileCmd, nil); err != nil {
			t.Fatalf("runCompile --explain --force: %v", err)
		}
	})
	if !strings.Contains(out, "compile: content (new)") {
		t.Errorf("--explain --force on a new doc should keep 'compile: content (new)':\n%s", out)
	}
}
