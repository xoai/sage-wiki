package search

import (
	"context"
	"database/sql"
	"testing"
	"time"

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

	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5, FilterTags: []string{"go", "search"}})
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
	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5, Tags: []string{"python"}})
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
	hard, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5, FilterTags: []string{"python"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(hard.Results) != 1 || hard.Results[0].DocID != "doc2" {
		t.Errorf("hard filter distinction broken: %+v", hard.Results)
	}
}

// F-048: the soft boost changes MEMBERSHIP, not just order — with limit 1,
// the boosted equal-relevance doc must enter the head, which requires the
// over-fetch (without it the pipeline would truncate to 1 before boosting).
func TestRunSoftBoostChangesMembership(t *testing.T) {
	deps, _ := wiringFixture(t)

	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 1, Tags: []string{"python"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].DocID != "doc2" {
		t.Fatalf("boosted doc2 must enter the limit-1 head via over-fetch, got %+v", resp.Results)
	}
}

// F-049: Channels [vector] disables the lexical legs — the bm25-only doc
// must NOT appear (mirror of the vector-off ablation).
func TestRunChannelsExcludeBM25(t *testing.T) {
	deps, _ := wiringFixture(t)

	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5, Channels: []Channel{ChannelVector}})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].DocID != "doc2" {
		t.Fatalf("vector-only channels must return only the vector doc, got %+v", resp.Results)
	}
}

// F-050: a failing tag lookup excludes the doc (hard filters never admit
// what they could not check) without panicking; the failure is logged.
func TestRunFilterTagsLookupFailureStaysClosed(t *testing.T) {
	db := openTestDB(t)
	cs := memory.NewChunkStore(db)
	ms := memory.NewStore(db)
	vs := vectors.NewStore(db)

	ms.Add(memory.Entry{ID: "doc1", Content: "keyword topic", Tags: []string{"go"}})
	indexTestChunks(t, db, cs, "doc1", []memory.ChunkEntry{
		{ChunkID: "doc1:c0", ChunkIndex: 0, Content: "keyword topic"},
	})

	// Force the results first, then break the store for the tag lookups:
	// closing the DB makes mem.Get error during fetchDocEntries... but it
	// would also break the search itself, so instead point the tag lookup
	// at a Store over a closed second handle.
	closedDB := openTestDB(t)
	closedDB.Close()
	brokenMem := memory.NewStore(closedDB)

	deps := Deps{Mem: ms, Chunks: cs, Vec: vs}
	// Swap only the tag-lookup dependency by running the search with the
	// healthy store, then verifying fetchDocEntries against the broken one.
	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5})
	if err != nil || len(resp.Results) == 0 {
		t.Fatalf("baseline search failed: %v %+v", err, resp)
	}
	tags := fetchDocEntries(brokenMem, resp.Results)
	if len(tags) != 0 {
		t.Fatalf("broken store yielded tags: %v", tags)
	}
	// And through Run: a deps whose Mem errors on Get must surface the
	// error (the doc-FTS leg reads through the same closed handle) —
	// never panic, never silently succeed.
	if _, err := Run(context.Background(), Deps{Mem: brokenMem, Chunks: cs, Vec: vs},
		Request{Query: "keyword topic", Limit: 5, FilterTags: []string{"go"}}); err == nil {
		t.Fatal("closed-DB run must propagate the store error, got nil")
	}
}

// T2.1b: an injected trust predicate excludes docs through the facade.
func TestRunTrustPredicateExclusion(t *testing.T) {
	deps, _ := wiringFixture(t)
	deps.IncludeDoc = func(docID string) bool { return docID != "doc2" }

	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5})
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

// V-M3b: two equal-relevance docs order by recency; results carry
// source_date (spec §2.1 Response contract; ADR-039).
func TestRunRecencyBoostAndSourceDates(t *testing.T) {
	deps, ms := wiringFixture(t)

	old := time.Now().AddDate(0, 0, -90).Unix() // decayed to ~0
	recent := time.Now().AddDate(0, 0, -1).Unix()
	if err := ms.SetSourceDate("doc1", old); err != nil {
		t.Fatal(err)
	}
	if err := ms.SetSourceDate("doc2", recent); err != nil {
		t.Fatal(err)
	}

	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 2 {
		t.Fatalf("expected both docs, got %+v", resp.Results)
	}
	if resp.Results[0].DocID != "doc2" {
		t.Errorf("recent doc must outrank equal-relevance old doc, got %s first", resp.Results[0].DocID)
	}
	for _, r := range resp.Results {
		want := old
		if r.DocID == "doc2" {
			want = recent
		}
		if r.SourceDate != want {
			t.Errorf("%s SourceDate = %d, want %d", r.DocID, r.SourceDate, want)
		}
	}
}

// A dateless doc gets no recency contribution and no date in the result.
func TestRunDatelessDocNoRecency(t *testing.T) {
	deps, ms := wiringFixture(t)
	if err := ms.SetSourceDate("doc2", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	// doc1 stays dateless.
	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resp.Results {
		if r.DocID == "doc1" && r.SourceDate != 0 {
			t.Errorf("dateless doc1 carries SourceDate %d", r.SourceDate)
		}
	}
	if resp.Results[0].DocID != "doc2" {
		t.Errorf("dated doc must win the tie, got %s", resp.Results[0].DocID)
	}
}

// T2.1b: results carry per-channel ranks (the T6.3 attribution source) —
// each doc's ranks reflect exactly the legs that found it.
func TestRunEmitsChannelRanks(t *testing.T) {
	deps, _ := wiringFixture(t)

	resp, err := Run(context.Background(), deps, Request{Query: "keyword topic", Limit: 5})
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
