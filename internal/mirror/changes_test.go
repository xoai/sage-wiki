package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeWS(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func shaOf(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

func committedRef(content string) ObjectRef {
	sha := shaOf(content)
	return ObjectRef{Key: "ws/objects/docs/" + sha[:2] + "/" + sha, SHA256: sha}
}

// populatedWorkspace builds the full ship set + every excluded file.
func populatedWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// Ship set.
	writeWS(t, dir, "wiki/concepts/Foo.md", "# Foo")
	writeWS(t, dir, "wiki/summaries/a.md", "summary")
	writeWS(t, dir, "raw/paper.pdf", "PDF-BYTES")
	writeWS(t, dir, "prompts/write_article.txt", "prompt")
	writeWS(t, dir, ".manifest.json", `{"sources":[]}`)
	writeWS(t, dir, ".sage/manifest.json", `{"concepts":[]}`)
	writeWS(t, dir, ".sage/pack-state.yaml", "packs: []")
	writeWS(t, dir, ".sage/vectors.idx", "SWVI-1")
	writeWS(t, dir, ".sage/vectors-chunks.idx", "SWVI-2")
	// Exclusions — config.yaml FIRST (F-001: it can carry api.api_key).
	writeWS(t, dir, "config.yaml", "api:\n  api_key: sk-SECRET\n")
	writeWS(t, dir, ".sage/engine.lock", "1234")
	writeWS(t, dir, ".sage/batch-state.json.tmp", "{}")
	writeWS(t, dir, ".sage/jobs.jsonl", "{}")
	writeWS(t, dir, ".sage/batch-state.json", "{}")
	writeWS(t, dir, ".sage/compile-state.json", "{}")
	writeWS(t, dir, ".sage/usage.jsonl", "{}")
	writeWS(t, dir, ".sage/lintlog/x.log", "log")
	writeWS(t, dir, ".sage/pack-snapshots/snap/p.md", "snap")
	writeWS(t, dir, ".sage/mirror-local.json", "{}")
	writeWS(t, dir, ".sage/mirror-ship.lock", "1")
	writeWS(t, dir, ".sage/hydrate-state.json", "{}")
	writeWS(t, dir, ".sage/wiki.db", "DB")
	writeWS(t, dir, ".sage/wiki.db-wal", "WAL")
	return dir
}

func TestChanges_NoChange(t *testing.T) {
	dir := populatedWorkspace(t)
	committed := map[string]ObjectRef{
		"wiki/concepts/Foo.md":      committedRef("# Foo"),
		"wiki/summaries/a.md":       committedRef("summary"),
		"raw/paper.pdf":             committedRef("PDF-BYTES"),
		"prompts/write_article.txt": committedRef("prompt"),
		".manifest.json":            committedRef(`{"sources":[]}`),
		".sage/manifest.json":       committedRef(`{"concepts":[]}`),
		".sage/pack-state.yaml":     committedRef("packs: []"),
	}
	vectors := map[string]ObjectRef{
		"vectors.idx":        committedRef("SWVI-1"),
		"vectors-chunks.idx": committedRef("SWVI-2"),
	}
	src := NewDiffChangeSource(dir)
	changes, _, err := src.Changes(context.Background(), ChangeToken{Committed: committed, CommittedVectors: vectors})
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("no-change expected, got %+v", changes)
	}
}

func TestChanges_TouchedFile(t *testing.T) {
	dir := populatedWorkspace(t)
	writeWS(t, dir, "wiki/concepts/Foo.md", "# Foo v2")
	src := NewDiffChangeSource(dir)
	changes, _, err := src.Changes(context.Background(), ChangeToken{
		Committed: map[string]ObjectRef{"wiki/concepts/Foo.md": committedRef("# Foo")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) == 0 {
		t.Fatal("touch not detected")
	}
	var found *Change
	for i := range changes {
		if changes[i].Path == "wiki/concepts/Foo.md" {
			found = &changes[i]
		}
	}
	if found == nil || found.Kind != ChangeUpsert || found.SHA256 != shaOf("# Foo v2") {
		t.Fatalf("change = %+v", found)
	}
}

func TestChanges_DeletedFile_Tombstone(t *testing.T) {
	dir := populatedWorkspace(t)
	os.Remove(filepath.Join(dir, "wiki/concepts/Foo.md"))
	src := NewDiffChangeSource(dir)
	changes, _, err := src.Changes(context.Background(), ChangeToken{
		Committed: map[string]ObjectRef{"wiki/concepts/Foo.md": committedRef("# Foo")},
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range changes {
		if c.Path == "wiki/concepts/Foo.md" && c.Kind == ChangeDelete {
			found = true
		}
	}
	if !found {
		t.Fatalf("tombstone not emitted: %+v", changes)
	}
}

func TestChanges_NewFile(t *testing.T) {
	dir := populatedWorkspace(t)
	writeWS(t, dir, "wiki/concepts/New.md", "new")
	src := NewDiffChangeSource(dir)
	changes, _, err := src.Changes(context.Background(), ChangeToken{Committed: map[string]ObjectRef{}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range changes {
		if c.Path == "wiki/concepts/New.md" && c.Kind == ChangeUpsert {
			found = true
		}
	}
	if !found {
		t.Fatalf("new file not detected: %+v", changes)
	}
}

// TestChanges_Exclusions: NOTHING in the exclusion list ever produces a
// Change — and config.yaml's secret bytes never enter a Change hash.
func TestChanges_Exclusions(t *testing.T) {
	dir := populatedWorkspace(t)
	src := NewDiffChangeSource(dir)
	changes, _, err := src.Changes(context.Background(), ChangeToken{Committed: map[string]ObjectRef{}})
	if err != nil {
		t.Fatal(err)
	}
	excludedPrefixes := []string{
		"config.yaml", ".sage/engine.lock", ".sage/jobs.jsonl",
		".sage/batch-state.json", ".sage/compile-state.json", ".sage/usage.jsonl",
		".sage/lintlog/", ".sage/pack-snapshots/", ".sage/mirror-local.json",
		".sage/mirror-ship.lock", ".sage/hydrate-state.json", ".sage/wiki.db",
	}
	for _, c := range changes {
		for _, ex := range excludedPrefixes {
			if strings.HasPrefix(c.Path, ex) || strings.HasSuffix(c.Path, ".tmp") {
				t.Fatalf("excluded path produced a change: %s", c.Path)
			}
		}
		if strings.Contains(c.SHA256, shaOf("sk-SECRET")[:16]) {
			t.Fatal("config.yaml secret bytes influenced a change")
		}
	}
}

func TestChanges_ResurrectAfterTombstone(t *testing.T) {
	dir := populatedWorkspace(t)
	writeWS(t, dir, "wiki/concepts/Foo.md", "# Foo")
	src := NewDiffChangeSource(dir)
	tombstoned := committedRef("# Foo")
	tombstoned.Deleted = true
	changes, _, _ := src.Changes(context.Background(), ChangeToken{
		Committed: map[string]ObjectRef{"wiki/concepts/Foo.md": tombstoned},
	})
	found := false
	for _, c := range changes {
		if c.Path == "wiki/concepts/Foo.md" && c.Kind == ChangeUpsert {
			found = true
		}
	}
	if !found {
		t.Fatal("file resurrecting a tombstone must upsert")
	}
}

func TestChanges_Vectors(t *testing.T) {
	dir := populatedWorkspace(t)
	writeWS(t, dir, ".sage/vectors.idx", "SWVI-CHANGED")
	src := NewDiffChangeSource(dir)
	changes, _, _ := src.Changes(context.Background(), ChangeToken{
		CommittedVectors: map[string]ObjectRef{"vectors.idx": committedRef("SWVI-1")},
	})
	found := false
	for _, c := range changes {
		if c.Path == "vectors.idx" && c.Kind == ChangeUpsert {
			found = true
		}
	}
	if !found {
		t.Fatalf("vector change not detected: %+v", changes)
	}
}

func TestChanges_MissingDirsOK(t *testing.T) {
	dir := t.TempDir() // empty workspace: no wiki/, raw/, prompts/
	src := NewDiffChangeSource(dir)
	if _, _, err := src.Changes(context.Background(), ChangeToken{}); err != nil {
		t.Fatalf("missing ship-set dirs should be fine: %v", err)
	}
}
