package mirror

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// TestChanges_UserContentNamedLikeProcessFileShips (F-083): content under
// wiki/raw/prompts named config.yaml or usage.jsonl is CONTENT — only the
// root config.yaml and .sage/* process files are excluded.
func TestChanges_UserContentNamedLikeProcessFileShips(t *testing.T) {
	dir := populatedWorkspace(t)
	writeWS(t, dir, "wiki/concepts/config.yaml", "user doc named config.yaml")
	writeWS(t, dir, "raw/usage.jsonl", "user source named usage.jsonl")
	src := NewDiffChangeSource(dir)
	changes, _, err := src.Changes(context.Background(), ChangeToken{Committed: map[string]ObjectRef{}})
	if err != nil {
		t.Fatal(err)
	}
	var wikiCfg, rawUsage bool
	for _, c := range changes {
		if c.Path == "wiki/concepts/config.yaml" {
			wikiCfg = true
		}
		if c.Path == "raw/usage.jsonl" {
			rawUsage = true
		}
		if c.Path == "config.yaml" {
			t.Fatal("root config.yaml must stay excluded")
		}
	}
	if !wikiCfg || !rawUsage {
		t.Fatalf("user content dropped: wikiCfg=%v rawUsage=%v", wikiCfg, rawUsage)
	}
}

// TestChanges_IdlePassReadsNothing (F-082): a second identical pass hits the
// stat cache for every file — zero file reads beyond stat (hashFileCalls
// does not grow).
func TestChanges_IdlePassReadsNothing(t *testing.T) {
	dir := populatedWorkspace(t)
	// Backdate all files so the racy-clean guard (same-second entries are
	// re-hashed, F-096) doesn't fire — this test isolates the cache-hit path.
	old := time.Now().Add(-10 * time.Second)
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			os.Chtimes(path, old, old)
		}
		return nil
	})
	src := NewDiffChangeSource(dir)
	token := ChangeToken{Committed: map[string]ObjectRef{}, CommittedVectors: map[string]ObjectRef{}}
	if _, _, err := src.Changes(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	before := hashFileCalls.Load()
	// Second pass on a FRESH source (loads cache from disk, like a new process).
	src2 := NewDiffChangeSource(dir)
	if _, _, err := src2.Changes(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if got := hashFileCalls.Load() - before; got != 0 {
		t.Fatalf("idle pass re-hashed %d files (cache miss)", got)
	}
}

// TestChanges_RacyCleanGuard (F-096): a same-size rewrite inside the SAME
// mtime second must still be detected (pinned mtime via Chtimes).
func TestChanges_RacyCleanGuard(t *testing.T) {
	dir := populatedWorkspace(t)
	src := NewDiffChangeSource(dir)
	token := ChangeToken{Committed: map[string]ObjectRef{}}
	if _, _, err := src.Changes(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	// Same-size rewrite, mtime pinned to the cached value's second.
	p := filepath.Join(dir, "wiki/concepts/Foo.md")
	info, _ := os.Stat(p)
	if err := os.WriteFile(p, []byte("# Fxx"), 0o644); err != nil { // same len as "# Foo"
		t.Fatal(err)
	}
	os.Chtimes(p, info.ModTime(), info.ModTime())
	src2 := NewDiffChangeSource(dir)
	changes, _, err := src2.Changes(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range changes {
		if c.Path == "wiki/concepts/Foo.md" {
			found = true
		}
	}
	if !found {
		t.Fatal("same-size same-mtime-second edit hidden by cache (racy-clean)")
	}
}

// TestChanges_VectorPathTraversalRejected (F-095).
func TestChanges_VectorBasenameEnforced(t *testing.T) {
	s := fixtureState()
	s.Vectors["../../escape-vec.idx"] = ObjectRef{
		Key:    "ws/vectors/" + strings.Repeat("ab", 32),
		SHA256: strings.Repeat("ab", 32),
	}
	if err := s.Validate(); err == nil {
		t.Fatal("non-basename vector name must fail validation")
	}
}
