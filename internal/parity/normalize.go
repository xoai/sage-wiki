package parity

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/manifest"
)

// goldenEpoch pins corpus mtimes (SPEC-09 discovery: sourcedate falls
// back to file mtime).
var goldenEpoch = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// goldenNow is the search-side clock: far enough in the future that the
// recency bonus underflows to exactly +0 for every document
// (0.05·2^(-ageDays/14) = 0). Generated articles carry per-build
// wall-clock mtimes, so any near-now epoch lets recency drift scores
// between builds; a far-future epoch makes scores deterministic by
// construction. Golden queries assert doc+rank+exact-score on this basis.
var goldenNow = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC)

// GoldenEpoch returns the mtime epoch.
func GoldenEpoch() time.Time { return goldenEpoch }

// GoldenNow returns the search clock (far-future recency suppressor).
func GoldenNow() time.Time { return goldenNow }

var rfc3339Re = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(Z|[+-]\d{2}:\d{2})`)

// Normalize applies the ONE timestamp normalization (shared by the golden
// writer and checker): RFC3339 sentinels in frontmatter blocks, `## `
// heading lines elsewhere (CHANGELOG), and manifest timestamp zeroing.
// Rules mirror internal/compiler/characterization_test.go verbatim.
func Normalize(data []byte) []byte {
	if bytes.HasPrefix(data, []byte("---\n")) {
		end := bytes.Index(data[4:], []byte("\n---"))
		if end != -1 {
			fm := rfc3339Re.ReplaceAll(data[:4+end], []byte("<TS>"))
			return append(fm, data[4+end:]...)
		}
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "## ") && rfc3339Re.MatchString(line) {
			lines[i] = "## <TS>"
		}
	}
	return []byte(strings.Join(lines, "\n"))
}

// HashNormalized hashes a file after Normalize.
func HashNormalized(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(Normalize(data))
	return hex.EncodeToString(h[:]), nil
}

// NormalizeManifestJSON loads .manifest.json, zeroes every timestamp
// field, and re-marshals deterministically.
func NormalizeManifestJSON(path string) ([]byte, error) {
	mf, err := manifest.Load(path)
	if err != nil {
		return nil, err
	}
	mf.CreatedAt = ""
	for p, src := range mf.Sources {
		src.AddedAt = ""
		src.CompiledAt = ""
		mf.Sources[p] = src
	}
	for name, c := range mf.Concepts {
		c.LastCompiled = ""
		mf.Concepts[name] = c
	}
	return json.Marshal(mf)
}

// BuildWorkspace materializes a corpus into a compiled workspace:
// copy → pin mtimes → write config → full compile (SPEC-09 §2.4).
func BuildWorkspace(corpusDir, wsDir, replayURL, goldenConfig string) error {
	return BuildWorkspaceAuth(corpusDir, wsDir, replayURL, goldenConfig, "sk-replay", "gpt-4o-mini")
}

// BuildWorkspaceAuth is BuildWorkspace with an explicit API key + model —
// the real-vendor record path (make record-fixtures ORIGIN=... KEY=... MODEL=...).
func BuildWorkspaceAuth(corpusDir, wsDir, replayURL, goldenConfig, apiKey, model string) error {
	return buildWorkspaceOpts(corpusDir, wsDir, replayURL, goldenConfig, apiKey, model, compiler.CompileOpts{})
}

// buildWorkspaceOpts is BuildWorkspaceAuth with explicit compile options —
// the SPEC-07 event golden injects JobID + a capture sink here.
func buildWorkspaceOpts(corpusDir, wsDir, replayURL, goldenConfig, apiKey, model string, opts compiler.CompileOpts) error {
	if err := copyTree(corpusDir, wsDir); err != nil {
		return fmt.Errorf("copy corpus: %w", err)
	}
	// Pin mtimes (source dates must not depend on checkout time).
	err := filepath.Walk(wsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		return os.Chtimes(path, goldenEpoch, goldenEpoch)
	})
	if err != nil {
		return fmt.Errorf("pin mtimes: %w", err)
	}

	configBody := fmt.Sprintf(`version: 1
project: parity
sources:
  - path: raw
    type: auto
    watch: false
output: wiki
api:
  provider: openai
  api_key: %q
  base_url: %s
models:
  summarize: %q
  extract: %q
  write: %q
compiler:
  auto_commit: false
  default_tier: 3
  mode: standard
  max_parallel: 1
ontology:
  triples:
    enabled: true
  relations:
    - name: implements
      functional: true
%s`, apiKey, replayURL, model, model, model, goldenConfig)
	if err := os.WriteFile(filepath.Join(wsDir, "config.yaml"), []byte(configBody), 0o644); err != nil {
		return err
	}

	// Pin the compile clock (SOURCE_DATE_EPOCH) so timestamps in compiled
	// artifacts — and everything indexed from them — are build-stable.
	if err := os.Setenv("SOURCE_DATE_EPOCH", fmt.Sprintf("%d", goldenEpoch.Unix())); err != nil {
		return err
	}
	defer os.Unsetenv("SOURCE_DATE_EPOCH")
	_, err = compiler.Compile(wsDir, opts)
	if err != nil {
		return fmt.Errorf("compile: %w", err)
	}
	return nil
}

// copyTree copies src into dst (creating dst), regular files only.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." || info.IsDir() {
			return nil
		}
		// The corpus's own README is documentation, not a source.
		if rel == "README.md" {
			return nil
		}
		target := filepath.Join(dst, "raw", rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// treeHashes returns relpath → normalized hash for every file under root.
func treeHashes(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		h, err := HashNormalized(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = h
		return nil
	})
	return out, err
}

// sortedKeys is a small helper for deterministic JSON dumps.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
