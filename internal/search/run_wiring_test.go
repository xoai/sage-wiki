package search

import (
	"database/sql"
	"testing"

	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// wiringFixture builds two EQUAL-RELEVANCE docs: doc1 is a rank-1 BM25-only
// hit, doc2 a rank-1 vector-only hit — identical RRF contributions, so both
// normalize to 1.0 and the soft boost is a genuine tie-breaker (min-max
// normalization stretches UNEQUAL ranks to large gaps a 3% boost can never
// cross; that is by design, matching the reference formula).
func wiringFixture(t *testing.T) (Deps, *memory.Store) {
	t.Helper()
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "keyword alpha topic", Tags: []string{"go", "search"}})
	ms.Add(memory.Entry{ID: "doc2", Content: "Vulpes vulpes exhibits adaptability", Tags: []string{"python"}})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "keyword alpha topic"},
	})
	indexTestChunks(t, db, cs, "doc2", []memory.ChunkEntry{
		{ChunkID: "doc2:c0", ChunkIndex: 0, Content: "Vulpes vulpes exhibits adaptability"},
	})
	if err := db.WriteTx(func(tx *sql.Tx) error {
		return vs.UpsertChunk(tx, "doc2:c0", "doc2", []float32{1, 0, 0})
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	// Doc vector too, so doc2 accumulates two vector lists exactly as doc1
	// accumulates two bm25 lists (chunk + doc granularity).
	if err := vs.Upsert("doc2", []float32{1, 0, 0}); err != nil {
		t.Fatalf("upsert doc vector: %v", err)
	}
	// Equal weights make the two docs a true tie: 2×0.5/61 each.
	return Deps{
		Mem: ms, Chunks: cs, Vec: vs,
		Embedder:   fixedEmbedder{v: []float32{1, 0, 0}},
		BM25Weight: 0.5, VectorWeight: 0.5,
	}, ms
}

// T2.1b: FilterTags is a HARD AND filter — non-matching docs are excluded,
// however well they score.
func TestRunFilterTagsHardExclusion(t *testing.T) {
	deps, _ := wiringFixture(t)

	resp, err := Run(deps, Request{Query: "keyword topic", Limit: 5, FilterTags: []string{"go", "search"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].DocID != "doc1" {
		t.Fatalf("hard filter failed: %+v", resp.Results)
	}
}

// T2.1b: Tags is a SOFT boost — non-matching docs remain, matching docs
// gain up to 3%/tag (cap 15%) and outrank equal-relevance siblings.
func TestRunSoftBoostVsHardFilterDistinction(t *testing.T) {
	deps, _ := wiringFixture(t)

	// Soft: both docs stay; the python-tagged one rises.
	resp, err := Run(deps, Request{Query: "keyword topic", Limit: 5, Tags: []string{"python"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("soft boost must not exclude: %+v", resp.Results)
	}
	if resp.Results[0].DocID != "doc2" {
		t.Errorf("soft-boosted doc2 should rank first, got %s", resp.Results[0].DocID)
	}

	// Hard: only the python-tagged doc survives.
	hard, err := Run(deps, Request{Query: "keyword topic", Limit: 5, FilterTags: []string{"python"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hard.Results) != 1 || hard.Results[0].DocID != "doc2" {
		t.Errorf("hard filter distinction broken: %+v", hard.Results)
	}
}

// T2.1b: an injected trust predicate excludes docs through the facade.
func TestRunTrustPredicateExclusion(t *testing.T) {
	deps, _ := wiringFixture(t)
	deps.IncludeDoc = func(docID string) bool { return docID != "doc2" }

	resp, err := Run(deps, Request{Query: "keyword topic", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Results {
		if r.DocID == "doc2" {
			t.Fatalf("trust predicate did not exclude doc2: %+v", resp.Results)
		}
	}
	if len(resp.Results) != 1 {
		t.Errorf("expected doc1 only, got %+v", resp.Results)
	}
}

// T2.1b: results carry per-channel ranks (the T6.3 attribution source) —
// each doc's ranks reflect exactly the legs that found it.
func TestRunEmitsChannelRanks(t *testing.T) {
	deps, _ := wiringFixture(t)

	resp, err := Run(deps, Request{Query: "keyword topic", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	byDoc := map[string]SearchResult{}
	for _, r := range resp.Results {
		byDoc[r.DocID] = r
	}
	d1, ok1 := byDoc["doc1"]
	d2, ok2 := byDoc["doc2"]
	if !ok1 || !ok2 {
		t.Fatalf("expected both docs, got %+v", resp.Results)
	}
	if d1.BM25Rank <= 0 || d1.VectorRank != 0 {
		t.Errorf("doc1 (bm25-only) ranks = bm25:%d vec:%d, want bm25>0 vec:0", d1.BM25Rank, d1.VectorRank)
	}
	if d2.VectorRank <= 0 || d2.BM25Rank != 0 {
		t.Errorf("doc2 (vector-only) ranks = bm25:%d vec:%d, want vec>0 bm25:0", d2.BM25Rank, d2.VectorRank)
	}
}
