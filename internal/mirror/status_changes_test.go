package mirror

import (
	"context"
	"testing"
	"time"
)

// TestStatus_RealPendingChanges: the diff detector wires through Status —
// a touched file shows pending_changes=1 and lag>0 under an injected clock
// (plan Task 14 fixture test).
func TestStatus_RealPendingChanges(t *testing.T) {
	fake := newFakeS3()
	dir := populatedWorkspace(t)
	commit := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	st := fixtureState()
	st.UpdatedAt = commit
	// Committed maps match the full workspace EXCEPT one file (touched).
	st.Objects = map[string]ObjectRef{
		"wiki/concepts/Foo.md":      committedRef("# Foo OLD"),
		"wiki/summaries/a.md":       committedRef("summary"),
		"raw/paper.pdf":             committedRef("PDF-BYTES"),
		"prompts/write_article.txt": committedRef("prompt"),
		".manifest.json":            committedRef(`{"sources":[]}`),
		".sage/manifest.json":       committedRef(`{"concepts":[]}`),
		".sage/pack-state.yaml":     committedRef("packs: []"),
	}
	st.Vectors = map[string]ObjectRef{
		"vectors.idx":        committedRef("SWVI-1"),
		"vectors-chunks.idx": committedRef("SWVI-2"),
	}
	writeWS(t, dir, "wiki/concepts/Foo.md", "# Foo NEW")

	m := openStatusMirror(t, fake, dir, st)
	m.src = NewDiffChangeSource(dir)
	m.now = func() time.Time { return commit.Add(45 * time.Second) }

	s, err := m.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if s.PendingChanges != 1 {
		t.Fatalf("PendingChanges = %d, want 1 (only the touched file)", s.PendingChanges)
	}
	if s.LagSeconds != 45 {
		t.Fatalf("LagSeconds = %d, want 45", s.LagSeconds)
	}
}
