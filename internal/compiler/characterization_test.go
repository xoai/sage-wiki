package compiler

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/wiki"
)

// compileSnapshot captures everything user-visible a refactor must not
// change. Timestamps are normalized per field (spec D4) — there is no
// clock injection, so they are sentinel-replaced, not frozen.
type compileSnapshot struct {
	Result        compileResultSnapshot
	Manifest      string            // normalized JSON (timestamps zeroed)
	Files         map[string]string // relpath → SHA-256 (frontmatter timestamps sentinel-replaced)
	Items         map[string]string // compile_items path → pass flags
	Counts        map[string]int    // entries, vec_entries, chunks_meta, entities
	ChangelogHash string            // SHA-256 with the timestamp heading sentinel-replaced
}

type compileResultSnapshot struct {
	Added, Modified, Removed                int
	Summarized, Concepts, Articles          int
	Errors, EmbedErrors                     int
	TierIndexed, TierEmbedded, TierCompiled int
}

var rfc3339Re = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(Z|[+-]\d{2}:\d{2})`)

// hashFileNormalized hashes a file with every RFC3339 timestamp inside its
// YAML frontmatter replaced by a fixed sentinel (created_at AND
// compiled_at — buildFrontmatter stamps created_at per article, i1).
// Files without frontmatter (CHANGELOG.md) get their `## <RFC3339>`
// heading lines sentinel-replaced instead.
func hashFileNormalized(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.HasPrefix(content, "---\n") {
		if end := strings.Index(content[4:], "\n---\n"); end >= 0 {
			fm := content[:4+end+5]
			fm = rfc3339Re.ReplaceAllString(fm, "<TS>")
			return fmt.Sprintf("%x", sha256.Sum256([]byte(fm+content[4+end+5:])))
		}
	}
	// No frontmatter: sentinel only heading lines that are bare timestamps
	// (writeChangelog's `## <RFC3339>` entries) — body content is untouched.
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		if strings.HasPrefix(line, "## ") && rfc3339Re.MatchString(line) {
			line = "## <TS>"
		}
		lines = append(lines, line)
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(lines, "\n"))))
}

func captureSnapshot(t *testing.T, dir string, result *CompileResult) compileSnapshot {
	t.Helper()
	snap := compileSnapshot{
		Result: compileResultSnapshot{
			Added: result.Added, Modified: result.Modified, Removed: result.Removed,
			Summarized: result.Summarized, Concepts: result.ConceptsExtracted,
			Articles: result.ArticlesWritten, Errors: result.Errors, EmbedErrors: result.EmbedErrors,
			TierIndexed: result.TierIndexed, TierEmbedded: result.TierEmbedded, TierCompiled: result.TierCompiled,
		},
		Files:  map[string]string{},
		Items:  map[string]string{},
		Counts: map[string]int{},
	}

	// Manifest: parse, zero every timestamp field, re-marshal deterministically.
	mf, err := manifest.Load(filepath.Join(dir, ".manifest.json"))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	mf.CreatedAt = "" // workspace creation timestamp (SPEC-01 format fields)
	for path, src := range mf.Sources {
		src.AddedAt = ""
		src.CompiledAt = ""
		mf.Sources[path] = src
	}
	for name, c := range mf.Concepts {
		c.LastCompiled = ""
		mf.Concepts[name] = c
	}
	mfJSON, err := json.Marshal(mf)
	if err != nil {
		t.Fatal(err)
	}
	snap.Manifest = string(mfJSON)

	// Output files: sorted list + normalized hashes.
	walkRoot := filepath.Join(dir, "wiki")
	if err := filepath.Walk(walkRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		// Normalize separators: file keys differ across OSes (filepath.Rel
		// yields backslashes on Windows) but the pipeline behavior the
		// golden gates is separator-independent.
		snap.Files[filepath.ToSlash(rel)] = hashFileNormalized(t, path)
		return nil
	}); err != nil {
		t.Fatalf("walk output files: %v", err)
	}

	// compile_items: path → pass flags ONLY (no timestamps).
	db := openTestProjectDB(t, dir)
	defer db.Close()
	rows, err := db.ReadDB().Query("SELECT source_path, tier, pass_indexed, pass_embedded, pass_summarized, pass_extracted, pass_written FROM compile_items ORDER BY source_path")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var path string
		var tier int
		var pi, pe, ps, px, pw bool
		if err := rows.Scan(&path, &tier, &pi, &pe, &ps, &px, &pw); err != nil {
			t.Fatal(err)
		}
		snap.Items[filepath.ToSlash(path)] = fmt.Sprintf("t%d/%v%v%v%v%v", tier, pi, pe, ps, px, pw)
	}

	for table, key := range map[string]string{
		"entries": "fts", "vec_entries": "vec", "chunks_meta": "chunks", "entities": "ontology",
	} {
		var n int
		if err := db.ReadDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		snap.Counts[key] = n
	}

	// CHANGELOG: hash with the `## <RFC3339>` heading sentinel-replaced.
	clPath := filepath.Join(dir, "wiki", "CHANGELOG.md")
	if data, err := os.ReadFile(clPath); err == nil {
		snap.ChangelogHash = fmt.Sprintf("%x", sha256.Sum256(rfc3339Re.ReplaceAll(data, []byte("<TS>"))))
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}

	return snap
}

func buildCharacterizationFixture(t *testing.T, serverURL string) string {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gpt-4o-mini")
	cfg := `
version: 1
project: test
sources:
  - path: raw
    type: auto
output: wiki
api:
  provider: openai
  api_key: sk-test
  base_url: ` + serverURL + `
models:
  summarize: gpt-4o-mini
compiler:
  max_parallel: 1
  auto_commit: false
  summary_max_tokens: 500
  default_tier: 3
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"a.md", "b.md", "c.md"} {
		content := "# " + f + "\n\nSelf-attention computes contextual representations of tokens across the sequence."
		if err := os.WriteFile(filepath.Join(dir, "raw", f), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestCharacterization_CompileSnapshotDeterministic is the P1-8 primary
// guardrail: two IDENTICAL fixture builds compiled independently produce
// IDENTICAL snapshots. It must stay green through every refactor commit —
// a snapshot difference means the refactor changed behavior.
func TestCharacterization_CompileSnapshotDeterministic(t *testing.T) {
	fake := newMsgCaptureFake(t)

	run := func() compileSnapshot {
		dir := buildCharacterizationFixture(t, fake.URL)

		// Compile 1: three added sources.
		r1, err := Compile(dir, CompileOpts{})
		if err != nil {
			t.Fatalf("compile 1: %v", err)
		}

		// Compile 2: one modified (a.md) + one removed (c.md).
		if err := os.WriteFile(filepath.Join(dir, "raw", "a.md"),
			[]byte("# a.md\n\nModified: rotary position embeddings encode relative positions."), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(dir, "raw", "c.md")); err != nil {
			t.Fatal(err)
		}
		r2, err := Compile(dir, CompileOpts{})
		if err != nil {
			t.Fatalf("compile 2: %v", err)
		}
		_ = r1

		return captureSnapshot(t, dir, r2)
	}

	snap1 := run()
	snap2 := run()

	a, _ := json.Marshal(snap1)
	b, _ := json.Marshal(snap2)
	if string(a) != string(b) {
		t.Errorf("snapshots differ across identical runs:\n--- run1 ---\n%s\n--- run2 ---\n%s", a, b)
	}

	// D4's actual proof: the snapshot must also equal the GOLDEN baseline
	// captured from pre-refactor code (main before P1-8). Record with:
	//   SAGE_CHARACTERIZATION_RECORD=1 go test -run TestCharacterization ./internal/compiler/
	// (generates testdata/compile_snapshot_golden.json — regenerate ONLY on
	// main, never on a refactor branch).
	goldenPath := filepath.Join("testdata", "compile_snapshot_golden.json")
	if os.Getenv("SAGE_CHARACTERIZATION_RECORD") == "1" {
		// Guard against accidental re-recording (a leaked env var in CI or a
		// refactor-branch shell must not silently rewrite the baseline):
		// the golden is written ONLY when absent, or with an explicit
		// SAGE_CHARACTERIZATION_FORCE=1 overwrite acknowledgement.
		if _, err := os.Stat(goldenPath); err == nil && os.Getenv("SAGE_CHARACTERIZATION_FORCE") != "1" {
			t.Fatalf("golden exists at %s — refusing to overwrite (set SAGE_CHARACTERIZATION_FORCE=1 from pre-refactor main to regenerate)", goldenPath)
		}
		if err := os.MkdirAll("testdata", 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, a, 0644); err != nil {
			t.Fatalf("record golden: %v", err)
		}
		t.Logf("recorded golden snapshot to %s", goldenPath)
		return
	}
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden snapshot (regenerate from main with SAGE_CHARACTERIZATION_RECORD=1): %v", err)
	}
	if string(a) != string(golden) {
		t.Errorf("snapshot differs from PRE-REFACTOR golden baseline:\n--- current ---\n%s\n--- golden ---\n%s", a, golden)
	}
}
