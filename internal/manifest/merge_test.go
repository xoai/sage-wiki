package manifest

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// buildBase returns the manifest as the compile saw it at Load time.
func buildBase() *Manifest {
	m := New()
	m.AddSource("raw/paperA.md", "sha256:a", "paper", 100) // pending; compile will compile it
	m.AddSource("raw/paperB.md", "sha256:b", "paper", 100) // pending; compile will remove it
	m.AddSource("raw/shared.md", "sha256:s", "paper", 100)
	m.MarkCompiled("raw/shared.md", "wiki/summaries/shared.md", []string{"existing"})
	m.AddConcept("existing", "wiki/concepts/existing.md", []string{"raw/shared.md"})
	m.AddConcept("orphanCandidate", "wiki/concepts/orphan.md", []string{"raw/paperB.md"})
	return m
}

// TestMergeThreeWay is the D3 core: apply the compile's changes (ours-base) onto
// a fresh reload that a concurrent writer already mutated (theirs), with the
// compile authoritative on same-key conflicts. It must:
//   - keep the concurrent writer's disjoint additions (no clobber),
//   - win same-key conflicts for the compile,
//   - carry the compile's dedup-merge into an EXISTING concept,
//   - apply the compile's source removal, and
//   - NOT resurrect a concept the compile removed (the P1-1 orphan class).
func TestMergeThreeWay(t *testing.T) {
	base := buildBase()

	// ours = base + the compile's mutations.
	ours := base.Clone()
	ours.MarkCompiled("raw/paperA.md", "wiki/summaries/paperA.md", []string{"newConcept"})
	ours.AddConcept("newConcept", "wiki/concepts/new.md", []string{"raw/paperA.md"})
	// dedup-merge: the compile folded paperA into the EXISTING concept.
	existing := ours.Concepts["existing"]
	existing.Sources = append(existing.Sources, "raw/paperA.md")
	ours.Concepts["existing"] = existing
	// removal: the compile removed paperB and its now-orphaned concept.
	ours.RemoveSource("raw/paperB.md")
	delete(ours.Concepts, "orphanCandidate")

	// theirs = base + a concurrent writer that also touched the same-key concept.
	theirs := base.Clone()
	theirs.AddSource("raw/writer.md", "sha256:w", "article", 50)
	theirs.AddConcept("writerConcept", "wiki/concepts/writer.md", []string{"raw/writer.md"})
	conflict := theirs.Concepts["existing"]
	conflict.Sources = append(conflict.Sources, "raw/writerExtra.md") // theirs's competing edit
	theirs.Concepts["existing"] = conflict

	merged := mergeInto(theirs, base, ours)

	// Compile's source compile wins (same-key change).
	if s := merged.Sources["raw/paperA.md"]; s.Status != "compiled" {
		t.Errorf("paperA: expected compiled (ours wins), got %q", s.Status)
	}
	// Compile's removal applied.
	if _, ok := merged.Sources["raw/paperB.md"]; ok {
		t.Error("paperB should be removed by the compile")
	}
	// Untouched-by-ours source stays.
	if s := merged.Sources["raw/shared.md"]; s.Status != "compiled" {
		t.Errorf("shared: expected compiled (unchanged), got %q", s.Status)
	}
	// Concurrent writer's disjoint source survives.
	if _, ok := merged.Sources["raw/writer.md"]; !ok {
		t.Error("concurrent writer's source was lost")
	}

	// Same-key concept conflict: the compile wins, carrying its dedup-merge.
	wantExisting := []string{"raw/shared.md", "raw/paperA.md"}
	if got := merged.Concepts["existing"].Sources; !reflect.DeepEqual(got, wantExisting) {
		t.Errorf("existing concept: expected ours %v, got %v", wantExisting, got)
	}
	// Compile's new concept present.
	if _, ok := merged.Concepts["newConcept"]; !ok {
		t.Error("compile's new concept was lost")
	}
	// Concurrent writer's concept survives.
	if _, ok := merged.Concepts["writerConcept"]; !ok {
		t.Error("concurrent writer's concept was lost")
	}
	// The removed concept must NOT be resurrected from theirs.
	if _, ok := merged.Concepts["orphanCandidate"]; ok {
		t.Error("orphaned concept resurrected — the P1-1 orphan class regressed")
	}

	if merged.SourceCount() != 3 {
		t.Errorf("expected 3 sources (paperA, shared, writer), got %d", merged.SourceCount())
	}
	if merged.ConceptCount() != 3 {
		t.Errorf("expected 3 concepts (existing, newConcept, writerConcept), got %d", merged.ConceptCount())
	}
}

// TestMergeSaveMergesConcurrentWriter drives the real entry point: the on-disk
// manifest already carries a concurrent writer's addition (theirs); MergeSave
// must reload it, merge the compile's delta on top, and persist both.
func TestMergeSaveMergesConcurrentWriter(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".manifest.json")

	base := buildBase()
	if err := base.Save(path); err != nil {
		t.Fatalf("seed base: %v", err)
	}

	// A concurrent writer lands on disk after the compile's Load.
	if err := Mutate(context.Background(), path, func(m *Manifest) error {
		m.AddSource("raw/writer.md", "sha256:w", "article", 50)
		return nil
	}); err != nil {
		t.Fatalf("concurrent writer: %v", err)
	}

	// The compile's in-memory copy (ours) advanced from base.
	ours := base.Clone()
	ours.MarkCompiled("raw/paperA.md", "wiki/summaries/paperA.md", []string{"newConcept"})
	ours.AddConcept("newConcept", "wiki/concepts/new.md", []string{"raw/paperA.md"})

	if err := MergeSave(context.Background(), path, base, ours); err != nil {
		t.Fatalf("MergeSave: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if _, ok := got.Sources["raw/writer.md"]; !ok {
		t.Error("concurrent writer's source lost through MergeSave")
	}
	if s := got.Sources["raw/paperA.md"]; s.Status != "compiled" {
		t.Errorf("compile's mark lost through MergeSave: status=%q", s.Status)
	}
	if _, ok := got.Concepts["newConcept"]; !ok {
		t.Error("compile's concept lost through MergeSave")
	}
}
