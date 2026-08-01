package parity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCorpusLint: every group non-empty, files parse, contradiction v2
// carries a later frontmatter date than v1 (the invalidation trigger).
func TestCorpusLint(t *testing.T) {
	root := filepath.Join("..", "..", "testdata", "golden-corpus")
	groups := []string{"notes", "unicode", "text", "code", "contradiction", "alias", "multihop", "adversarial", "dates"}
	total := 0
	for _, g := range groups {
		entries, err := os.ReadDir(filepath.Join(root, g))
		if err != nil {
			t.Fatalf("group %s: %v", g, err)
		}
		if len(entries) == 0 {
			t.Errorf("group %s is empty", g)
		}
		total += len(entries)
	}
	if total < 25 {
		t.Errorf("corpus has %d files, want >= 25", total)
	}

	read := func(p string) string {
		b, err := os.ReadFile(filepath.Join(root, p))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	v1, v2 := read("contradiction/fact-v1.md"), read("contradiction/fact-v2.md")
	if !strings.Contains(v1, "date: 2024-06-01") || !strings.Contains(v2, "date: 2025-03-01") {
		t.Error("contradiction pair must carry ascending frontmatter dates")
	}
	if !strings.Contains(read("adversarial/instruction-lookalike.md"), "Ignore all previous instructions") {
		t.Error("adversarial doc must contain the quoted injection")
	}
	if !strings.Contains(read("alias/kubernetes-k8s.md"), "K8s") {
		t.Error("alias doc must contain the alias")
	}

	// Replay canonicalization sentinel-replaces RFC3339 timestamps, so
	// corpus content must not QUOTE them (two docs differing only in a
	// quoted timestamp would collide to one fixture — accepted collision
	// class, guarded here).
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if rfc3339Re.Match(data) {
			return fmt.Errorf("corpus file %s contains an RFC3339 timestamp (replay-key collision class)", info.Name())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestCorpusSmoke: the REAL corpus compiles through BuildWorkspace
// (content problems surface here, not at regen time).
func TestCorpusSmoke(t *testing.T) {
	if testing.Short() {
		t.Skip("smoke compiles the full corpus")
	}
	root := filepath.Join("..", "..", "testdata", "golden-corpus")
	origin := NewOriginServer()
	defer origin.Close()
	dir := filepath.Join(t.TempDir(), "ws")
	if err := BuildWorkspace(root, dir, origin.URL, ""); err != nil {
		t.Fatalf("corpus smoke build: %v", err)
	}
	hashes, err := treeHashes(filepath.Join(dir, "wiki"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hashes) == 0 {
		t.Fatal("corpus compiled to nothing")
	}
	t.Logf("corpus compiled to %d wiki files", len(hashes))
}
