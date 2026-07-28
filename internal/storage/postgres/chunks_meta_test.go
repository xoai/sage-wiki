package postgres

import (
	"database/sql"
	"testing"

	"github.com/xoai/sage-wiki/internal/store"
)

// pg twin of the sqlite hydration read (M1 F-034): GetChunksMeta returns
// heading/content for known IDs and omits unknown IDs.
func TestPGGetChunksMeta(t *testing.T) {
	b, _, cleanup := derivedTestBackend(t)
	defer cleanup()

	cs := b.Chunks()
	if err := b.WriteTx(func(tx *sql.Tx) error {
		return cs.IndexChunks(tx, "doc1", []store.ChunkEntry{
			{ChunkID: "doc1:c0", ChunkIndex: 0, Heading: "H0", Content: "alpha content"},
			{ChunkID: "doc1:c1", ChunkIndex: 1, Heading: "H1", Content: "beta content"},
		})
	}); err != nil {
		t.Fatalf("index: %v", err)
	}

	meta, err := cs.GetChunksMeta([]string{"doc1:c1", "doc1:missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 1 {
		t.Fatalf("expected 1 hit, got %d", len(meta))
	}
	c, ok := meta["doc1:c1"]
	if !ok || c.Heading != "H1" || c.Content != "beta content" {
		t.Errorf("unexpected meta: %+v", meta)
	}

	empty, err := cs.GetChunksMeta(nil)
	if err != nil || len(empty) != 0 {
		t.Errorf("nil ids: got %v, %v", empty, err)
	}
}
