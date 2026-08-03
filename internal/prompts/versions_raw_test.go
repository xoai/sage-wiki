package prompts

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// TestEffectiveTemplateHashes_RawBytes pins the raw-bytes contract: the hash
// matches sha256 of the override FILE (not the parse tree), and a
// comment-only edit drifts.
func TestEffectiveTemplateHashes_RawBytes(t *testing.T) {
	dir := t.TempDir()
	body := []byte("{{.ConceptName}} — body with a comment-free first line\n")
	if err := os.WriteFile(filepath.Join(dir, "write-article.md"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	r := NewRegistry()
	if err := r.LoadFromDir(dir); err != nil {
		t.Fatal(err)
	}
	hashes, err := EffectiveTemplateHashes(r)
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(body)
	if got := hashes["write_article"]; got != hex.EncodeToString(want[:])[:16] {
		t.Errorf("override hash = %q, want sha256(raw file) = %q", got, hex.EncodeToString(want[:])[:16])
	}

	// Comment-only edit drifts.
	body2 := append([]byte("{{/* a steering comment */}}\n"), body...)
	if err := os.WriteFile(filepath.Join(dir, "write-article.md"), body2, 0o644); err != nil {
		t.Fatal(err)
	}
	hashes2, _ := EffectiveTemplateHashes(r)
	if hashes2["write_article"] == hashes["write_article"] {
		t.Error("comment-only edit did not drift the hash")
	}

	// sha256sum reproducibility: embedded templates hash the embedded bytes.
	embedded, _ := templateFS.ReadFile("templates/summarize_article.txt")
	wantE := sha256.Sum256(embedded)
	if got := hashes["summarize_article"]; got != hex.EncodeToString(wantE[:])[:16] {
		t.Errorf("embedded hash = %q, want sha256(embedded) = %q", got, hex.EncodeToString(wantE[:])[:16])
	}
}
