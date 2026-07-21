package storetest

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/store"
)

// BackendFactory creates a fresh, empty backend per call (writer mode).
type BackendFactory func(t *testing.T) store.Backend

// RunConformance exercises every store.Backend interface against a backend
// (spec §8). Backend-neutral: assertions are contract properties, never
// representation details (BLOB encodings, corrupt-blob guards, and golden
// error text stay per-backend unit tests).
func RunConformance(t *testing.T, newBackend BackendFactory) {
	t.Helper()
	t.Run("entries", EntriesConformance(newBackend))
	t.Run("chunks", ChunksConformance(newBackend))
	t.Run("vectors", VectorsConformance(newBackend))
	t.Run("ontology", OntologyConformance(newBackend))
	t.Run("trust", TrustConformance(newBackend))
	t.Run("compile_items", CompileItemsConformance(newBackend))
	t.Run("output_index", OutputIndexConformance(newBackend))
	t.Run("learnings", LearningsConformance(newBackend))
	t.Run("timestamps", TimestampsConformance(newBackend))
	t.Run("cjk_parity", CJKConformance(newBackend))
	t.Run("write_serialization", WriteSerializationConformance(newBackend))
}

func EntriesConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		es := b.Entries()

		e := store.Entry{ID: "src:a.md", Content: "zebra quill amber", Tags: []string{"t1"}, ArticlePath: "wiki/a.md"}
		if err := es.Add(e); err != nil {
			t.Fatalf("Add: %v", err)
		}
		got, err := es.Get("src:a.md")
		if err != nil || got == nil {
			t.Fatalf("Get: %v %v", got, err)
		}
		if got.Content != e.Content || got.ArticlePath != e.ArticlePath || len(got.Tags) != 1 {
			t.Errorf("round trip mismatch: %+v", got)
		}

		// Absent entry: (nil, nil) contract.
		miss, err := es.Get("src:nope.md")
		if err != nil || miss != nil {
			t.Errorf("Get absent = %v, %v; want nil, nil", miss, err)
		}

		// Search finds by term; top-k contains the right doc.
		if err := es.Add(store.Entry{ID: "src:b.md", Content: "zebra striped"}); err != nil {
			t.Fatal(err)
		}
		hits, err := es.Search("zebra", nil, 10)
		if err != nil || len(hits) != 2 {
			t.Fatalf("Search: %v %v", hits, err)
		}
		hitIDs := map[string]bool{}
		for _, h := range hits {
			hitIDs[h.ID] = true
		}
		if !hitIDs["src:a.md"] || !hitIDs["src:b.md"] {
			t.Errorf("Search missing docs: %v", hitIDs)
		}

		// Tag filter applied (AND semantics: ALL tags must be present).
		filtered, err := es.Search("zebra", []string{"t1"}, 10)
		if err != nil || len(filtered) != 1 || filtered[0].ID != "src:a.md" {
			t.Errorf("tag filter: %+v %v", filtered, err)
		}
		if filtered, err := es.Search("zebra", []string{"t1", "t2"}, 10); err != nil || len(filtered) != 0 {
			t.Errorf("AND tag filter: %+v %v — both tags required", filtered, err)
		}

		// ListAll full population.
		all, err := es.ListAll()
		if err != nil || len(all) != 2 {
			t.Fatalf("ListAll: %v %v", all, err)
		}


		// Stopword fallback (spec §5 pinned mitigation): stopword-only
		// queries must not error and must return docs containing the words.
		es.Add(store.Entry{ID: "src:stop.md", Content: "the and of"})
		stopHits, err := es.Search("the and", nil, 10)
		if err != nil {
			t.Fatalf("stopword query errored: %v", err)
		}
		foundStop := false
		for _, h := range stopHits {
			if h.ID == "src:stop.md" {
				foundStop = true
			}
		}
		if !foundStop {
			t.Errorf("stopword fallback missing doc: %+v", stopHits)
		}

		// Hyphenated query must not error (tsquery operator-char guard).
		if _, err := es.Search("well-known", nil, 10); err != nil {
			t.Errorf("hyphenated query errored: %v", err)
		}

		// Update + Delete + Count.
		e.Content = "zebra revised"
		if err := es.Update(e); err != nil {
			t.Fatal(err)
		}
		got, _ = es.Get("src:a.md")
		if got.Content != "zebra revised" {
			t.Errorf("after Update: %q", got.Content)
		}
		if err := es.Delete("src:b.md"); err != nil {
			t.Fatal(err)
		}
		if err := es.Delete("src:stop.md"); err != nil {
			t.Fatal(err)
		}
		n, _ := es.Count()
		if n != 1 {
			t.Errorf("Count = %d, want 1", n)
		}
	}
}

func ChunksConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		cs := b.Chunks()

		// NeedsBackfill outcomes (spec §8): fresh DB no entries → false;
		// entries>0 ∧ chunks==0 → true; populated → false.
		if cs.NeedsBackfill(b.Entries()) {
			t.Error("fresh DB with no entries: NeedsBackfill true, want false")
		}
		b.Entries().Add(store.Entry{ID: "src:d1", Content: "doc one body"})
		if !cs.NeedsBackfill(b.Entries()) {
			t.Error("entries>0, chunks==0: NeedsBackfill false, want true")
		}

		// tx-scoped index + search.
		err := b.WriteTx(func(tx *sql.Tx) error {
			return cs.IndexChunks(tx, "src:d1", []store.ChunkEntry{
				{ChunkID: "c1", ChunkIndex: 0, Heading: "Intro", Content: "zebra intro body", StartOffset: 0, EndOffset: 10},
				{ChunkID: "c2", ChunkIndex: 1, Heading: "Body", Content: "quill body text", StartOffset: 11, EndOffset: 20},
			})
		})
		if err != nil {
			t.Fatalf("IndexChunks: %v", err)
		}
		if cs.NeedsBackfill(b.Entries()) {
			t.Error("populated: NeedsBackfill true, want false")
		}

		hits, err := cs.SearchChunks("zebra", 10)
		if err != nil || len(hits) != 1 || hits[0].ChunkID != "c1" {
			t.Errorf("SearchChunks: %+v %v", hits, err)
		}

		// Multi-query RRF: both queries' docs present.
		mq, err := cs.SearchChunksMultiQuery([]string{"zebra", "quill"}, 10)
		if err != nil || len(mq) != 2 {
			t.Errorf("MultiQuery: %+v %v", mq, err)
		}

		// ListAll.
		all, err := cs.ListAll()
		if err != nil || len(all) != 2 || all[0].DocID != "src:d1" {
			t.Errorf("ListAll: %+v %v", all, err)
		}

		// tx-scoped delete.
		n, _ := cs.Count()
		if n != 2 {
			t.Fatalf("Count = %d, want 2", n)
		}
		if err := b.WriteTx(func(tx *sql.Tx) error { return cs.DeleteDocChunks(tx, "src:d1") }); err != nil {
			t.Fatal(err)
		}
		n, _ = cs.Count()
		if n != 0 {
			t.Errorf("Count after delete = %d, want 0", n)
		}
	}
}

func VectorsConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		vs := b.Vectors()

		// Get absent: (nil, nil) contract (spec §3).
		v, err := vs.Get("missing")
		if err != nil || v != nil {
			t.Errorf("Get absent = %v, %v; want nil, nil", v, err)
		}

		if err := vs.Upsert("d1", []float32{1, 0, 0}); err != nil {
			t.Fatal(err)
		}
		if err := vs.Upsert("d2", []float32{0, 1, 0}); err != nil {
			t.Fatal(err)
		}
		got, err := vs.Get("d1")
		if err != nil || len(got) != 3 || got[0] != 1 {
			t.Errorf("Get: %v %v", got, err)
		}

		// Cosine search: d1 must outrank d2 for a d1-like query.
		hits, err := vs.Search([]float32{0.9, 0.1, 0}, 2)
		if err != nil || len(hits) != 2 {
			t.Fatalf("Search: %v %v", hits, err)
		}
		if hits[0].ID != "d1" {
			t.Errorf("Search top = %q, want d1 (cosine ordering)", hits[0].ID)
		}

		// Chunk vectors via caller-tx + invalidation contract.
		if err := b.WriteTx(func(tx *sql.Tx) error {
			return vs.UpsertChunk(tx, "c1", "d1", []float32{1, 0, 0})
		}); err != nil {
			t.Fatal(err)
		}
		vs.InvalidateChunkCache() // mandatory post-commit (interface contract)
		ch, err := vs.SearchChunks([]float32{1, 0, 0}, 5)
		if err != nil || len(ch) != 1 || ch[0].ChunkID != "c1" {
			t.Errorf("SearchChunks: %+v %v", ch, err)
		}

		// SearchChunksFiltered: docID filter applied.
		filt, err := vs.SearchChunksFiltered([]float32{1, 0, 0}, []string{"d2"}, 5)
		if err != nil || len(filt) != 0 {
			t.Errorf("Filtered(d2): %+v %v — filter not applied", filt, err)
		}

		// HasChunkVectors / DeleteDocChunkVectors.
		has, _ := vs.HasChunkVectors("d1")
		if !has {
			t.Error("HasChunkVectors false after upsert")
		}
		if err := vs.DeleteDocChunkVectors("d1"); err != nil {
			t.Fatal(err)
		}
		vs.InvalidateChunkCache()
		has, _ = vs.HasChunkVectors("d1")
		if has {
			t.Error("HasChunkVectors true after delete")
		}

		// Dimensions + Count + Delete.
		if d, _ := vs.Dimensions(); d != 3 {
			t.Errorf("Dimensions = %d, want 3", d)
		}
		if err := vs.Delete("d2"); err != nil {
			t.Fatal(err)
		}
		if n, _ := vs.Count(); n != 1 {
			t.Errorf("Count = %d, want 1", n)
		}
	}
}

func OntologyConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		os := b.Ontology()

		os.AddEntity(store.Entity{ID: "e1", Type: "concept", Name: "One"})
		os.AddEntity(store.Entity{ID: "e2", Type: "concept", Name: "Two"})
		os.AddEntity(store.Entity{ID: "e3", Type: "concept", Name: "Three"})
		e, err := os.GetEntity("e1")
		if err != nil || e == nil || e.Name != "One" {
			t.Fatalf("GetEntity: %v %v", e, err)
		}
		if n, _ := os.EntityCount("concept"); n != 3 {
			t.Errorf("EntityCount = %d, want 3", n)
		}

		os.AddRelation(store.Relation{ID: "r1", SourceID: "e1", TargetID: "e2", Relation: "implements"})
		os.AddRelation(store.Relation{ID: "r2", SourceID: "e2", TargetID: "e3", Relation: "contradicts"})
		if n, _ := os.RelationCount(); n != 2 {
			t.Errorf("RelationCount = %d, want 2", n)
		}

		rels, err := os.RelationsByType("contradicts")
		if err != nil || len(rels) != 1 || rels[0].SourceID != "e2" {
			t.Errorf("RelationsByType: %+v %v", rels, err)
		}
		all, err := os.AllRelations()
		if err != nil || len(all) != 2 {
			t.Errorf("AllRelations: %+v %v", all, err)
		}
		counts, err := os.EntityConnectionCounts()
		if err != nil {
			t.Fatal(err)
		}
		if counts["e1"] < 1 || counts["e2"] < 1 {
			t.Errorf("connection counts missing: %+v", counts)
		}

		// Traverse outbound from e1 reaches e2.
		tr, err := os.Traverse("e1", store.TraverseOpts{Direction: store.Outbound, MaxDepth: 1})
		if err != nil || len(tr) != 1 || tr[0].ID != "e2" {
			t.Errorf("Traverse: %+v %v", tr, err)
		}

		// GetRelations outbound filter.
		gr, err := os.GetRelations("e1", store.Outbound, "")
		if err != nil || len(gr) != 1 {
			t.Errorf("GetRelations: %+v %v", gr, err)
		}

		// Delete cascades relations.
		if err := os.DeleteEntity("e2"); err != nil {
			t.Fatal(err)
		}
		if n, _ := os.RelationCount(); n != 0 {
			t.Errorf("after cascade: RelationCount = %d, want 0", n)
		}
	}
}

func TrustConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		ts := b.Trust()

		o := &store.PendingOutput{
			ID: "t1.md", Question: "Q?", QuestionHash: "h1",
			Answer: "A.", AnswerHash: "ah1", State: store.StatePending,
			Confirmations: 1, FilePath: "p",
			// Local-time RFC3339, matching hooks.go writers — consistent-offset
			// comparison. (Mixed UTC/local offsets trigger the documented
			// latent sqlite lexicographic quirk, spec §5 — not asserted here.)
			CreatedAt: time.Now(),
		}
		if err := ts.InsertPending(o); err != nil {
			t.Fatalf("InsertPending: %v", err)
		}
		got, err := ts.Get("t1.md")
		if err != nil || got == nil || got.Question != "Q?" {
			t.Fatalf("Get: %v %v", got, err)
		}
		if ts.IsConfirmed("t1.md") {
			t.Error("IsConfirmed true for pending output")
		}
		// Id-only semantics: a confirmed output whose file_path merely
		// CONTAINS the doc id must not confirm a different id.
		conf := &store.PendingOutput{
			ID: "other.md", Question: "Q2?", QuestionHash: "h2",
			Answer: "A2.", AnswerHash: "ah2", State: store.StateConfirmed,
			Confirmations: 1, FilePath: "outputs/t1.md", CreatedAt: time.Now(),
		}
		if err := ts.InsertPending(conf); err != nil {
			t.Fatal(err)
		}
		if ts.IsConfirmed("t1.md") {
			t.Error("IsConfirmed matched by file_path substring — must be id-only")
		}
		ts.Delete("other.md")

		// Consensus tx methods: store a question vector, find it similar.
		err = b.WriteTx(func(tx *sql.Tx) error {
			return ts.EmbedAndStoreQuestion(tx, "h1", []float32{1, 0, 0})
		})
		if err != nil {
			t.Fatalf("EmbedAndStoreQuestion: %v", err)
		}
		var match *store.SimilarQuestion
		err = b.WriteTx(func(tx *sql.Tx) error {
			var err error
			match, err = ts.FindSimilarQuestion(tx, []float32{0.99, 0.01, 0}, 0.9)
			return err
		})
		if err != nil || match == nil {
			t.Fatalf("FindSimilarQuestion: %v %v", match, err)
		}
		if match.Output.ID != "t1.md" || match.Score < 0.9 {
			t.Errorf("match = %+v", match)
		}

		// State transitions.
		if err := ts.Promote("t1.md"); err != nil {
			t.Fatal(err)
		}
		if !ts.IsConfirmed("t1.md") {
			t.Error("IsConfirmed false after Promote")
		}
		if err := ts.Demote("t1.md"); err != nil {
			t.Fatal(err)
		}
		if ts.IsConfirmed("t1.md") {
			t.Error("IsConfirmed true after Demote")
		}

		// Confirmations round trip.
		if err := ts.RecordConfirmation("t1.md", `["c1"]`, "ah1"); err != nil {
			t.Fatal(err)
		}
		confs, err := ts.GetConfirmations("t1.md")
		if err != nil || len(confs) != 1 {
			t.Errorf("GetConfirmations: %+v %v", confs, err)
		}

		// ListOlderThan: far-future cutoff includes, past cutoff excludes.
		older, err := ts.ListOlderThan(time.Now().Add(time.Hour))
		if err != nil || len(older) != 1 {
			t.Errorf("ListOlderThan(+1h): %+v %v", older, err)
		}
		older, _ = ts.ListOlderThan(time.Now().Add(-time.Hour))
		if len(older) != 0 {
			t.Errorf("ListOlderThan(-1h): %+v, want empty", older)
		}

		if err := ts.Delete("t1.md"); err != nil {
			t.Fatal(err)
		}
		if got, _ := ts.Get("t1.md"); got != nil {
			t.Error("Get after Delete non-nil")
		}
	}
}

func CompileItemsConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		is := b.CompileItems()

		item := store.CompileItem{SourcePath: "a.md", Hash: "h1", FileType: "md", Tier: 3}
		if err := is.Upsert(item); err != nil {
			t.Fatalf("Upsert: %v", err)
		}
		got, err := is.GetByPath("a.md")
		if err != nil || got == nil || got.Hash != "h1" {
			t.Fatalf("GetByPath: %v %v", got, err)
		}

		// Sticky pass flags: MarkPass persists, later Upsert does not clear
		// (same hash); hash CHANGE resets flags (re-process).
		if err := is.MarkPass("a.md", "indexed"); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("a.md")
		if !got.PassIndexed {
			t.Error("PassIndexed false after MarkPass")
		}
		if err := is.Upsert(store.CompileItem{SourcePath: "a.md", Hash: "h1", FileType: "md", Tier: 3, CompileID: "c-1"}); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("a.md")
		if !got.PassIndexed {
			t.Error("same-hash Upsert cleared sticky flag")
		}
		if got.CompileID != "c-1" {
			t.Errorf("CompileID not round-tripped: %q", got.CompileID)
		}
		if err := is.Upsert(store.CompileItem{SourcePath: "a.md", Hash: "h2", FileType: "md", Tier: 2}); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("a.md")
		if got.Hash != "h2" {
			t.Error("Upsert did not update hash")
		}
		if got.PassIndexed {
			t.Error("hash-change Upsert kept pass flag — must reset")
		}
		if got.Tier != 2 {
			t.Errorf("re-Upsert did not update tier: %d", got.Tier)
		}

		// SetTier: promoted_at on promotion, demoted_at on demotion,
		// error on missing source.
		if err := is.SetTier("a.md", 3, "promote"); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("a.md")
		if got.Tier != 3 || got.PromotedAt == "" {
			t.Errorf("promotion: tier=%d promoted_at=%q", got.Tier, got.PromotedAt)
		}
		if err := is.SetTier("a.md", 1, "demote"); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("a.md")
		if got.DemotedAt == "" {
			t.Error("demotion left demoted_at empty")
		}
		if err := is.SetTier("ghost.md", 1, "x"); err == nil {
			t.Error("SetTier on missing source: expected error")
		}
		// Restore tier 3 state for downstream fixtures.
		is.SetTier("a.md", 3, "restore")

		// ListPending by tier + pass flag.
		is.Upsert(store.CompileItem{SourcePath: "b.md", Tier: 3})
		pend, err := is.ListPending(1)
		if err != nil || len(pend) == 0 {
			t.Errorf("ListPending: %+v %v", pend, err)
		}

		// IncrementQueryHits batched.
		if err := is.IncrementQueryHits([]string{"a.md", "b.md"}); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("a.md")
		if got.QueryHitCount != 1 {
			t.Errorf("QueryHitCount = %d, want 1", got.QueryHitCount)
		}
		if got.LastQueriedAt == "" {
			t.Error("LastQueriedAt empty after hit")
		}

		// Stats: tiers + error count.
		is.MarkError("b.md", errors.New("boom"))
		stats, err := is.Stats()
		if err != nil {
			t.Fatal(err)
		}
		if stats.TotalSources != 2 || stats.ByTier[3] != 2 {
			t.Errorf("Stats: %+v", stats)
		}
		if stats.WithErrors != 1 {
			t.Errorf("WithErrors = %d, want 1", stats.WithErrors)
		}

		// Quality scores.
		is.SetQualityScore("a.md", 0.3)
		low, err := is.ListBelowQualityScore(0.5)
		if err != nil || len(low) != 1 || low[0].SourcePath != "a.md" {
			t.Errorf("ListBelowQualityScore: %+v %v", low, err)
		}

		// Demotion branches (spec §5): hit-recent item is NOT stale even if old.
		farPast := time.Now().AddDate(0, 0, -365).UTC().Format(time.RFC3339)
		stale, err := is.ListDemotionCandidates(farPast)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range stale {
			if p == "a.md" {
				t.Error("a.md (recently queried) listed as demotion candidate")
			}
		}

		// Promotion candidates: only tiers 0/1 with hits qualify
		// (compiler/items.go:368-377 parity).
		is.Upsert(store.CompileItem{SourcePath: "t2.md", Tier: 2})
		is.IncrementQueryHits([]string{"t2.md"})
		promo, err := is.ListPromotionCandidates(1)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range promo {
			if p == "t2.md" {
				t.Error("tier-2 item listed as promotion candidate — only tiers 0/1 qualify")
			}
		}
		is.DeleteByPaths([]string{"t2.md"})

		// DeleteByPaths.
		if err := is.DeleteByPaths([]string{"a.md", "b.md"}); err != nil {
			t.Fatal(err)
		}
		if n, _ := is.Count(); n != 0 {
			t.Errorf("Count = %d after DeleteByPaths", n)
		}
	}
}

func OutputIndexConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		oi := b.OutputIndex()

		if _, ok, _ := oi.Get("x.md"); ok {
			t.Error("Get on empty index ok=true")
		}
		if err := oi.Set("x.md", "hash1"); err != nil {
			t.Fatal(err)
		}
		h, ok, err := oi.Get("x.md")
		if err != nil || !ok || h != "hash1" {
			t.Errorf("Get: %q %v %v", h, ok, err)
		}

		// tx helpers write atomically within a caller tx.
		if err := b.WriteTx(func(tx *sql.Tx) error { return oi.SetTx(tx, "y.md", "hash2") }); err != nil {
			t.Fatal(err)
		}
		all, err := oi.All()
		if err != nil || len(all) != 2 {
			t.Errorf("All: %+v %v", all, err)
		}
		if err := b.WriteTx(func(tx *sql.Tx) error { return oi.DeleteTx(tx, "y.md") }); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := oi.Get("y.md"); ok {
			t.Error("y.md present after DeleteTx")
		}

		// Backfill bulk-inserts.
		if err := oi.Backfill(map[string][]byte{"z.md": []byte("content-z")}); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := oi.Get("z.md"); !ok {
			t.Error("z.md missing after Backfill")
		}
	}
}

func LearningsConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		ls := b.Learnings()

		l := store.Learning{Type: "gotcha", Content: "redis eviction is LRU here", Tags: "redis,cache", SourcePass: "cli"}
		if err := ls.Store(l); err != nil {
			t.Fatalf("Store: %v", err)
		}
		// Dedup: same content → no second row, server-side LearningID.
		if err := ls.Store(l); err != nil {
			t.Fatal(err)
		}
		all, err := ls.List()
		if err != nil || len(all) != 1 {
			t.Fatalf("List: %+v %v (dedup broken?)", all, err)
		}
		if all[0].ID != store.LearningID(l.Content) {
			t.Errorf("ID = %q, want store.LearningID(content)", all[0].ID)
		}
		if all[0].CreatedAt == "" {
			t.Error("CreatedAt not populated")
		}

		rec, err := ls.Recall("redis", 10)
		if err != nil || len(rec) != 1 {
			t.Errorf("Recall: %+v %v", rec, err)
		}
		rec, _ = ls.Recall("nonexistent-domain", 10)
		if len(rec) != 0 {
			t.Errorf("Recall miss: %+v", rec)
		}

		// Prune keeps fresh entries.
		n, err := ls.Prune()
		if err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("Prune removed %d fresh entries", n)
		}
	}
}

func TimestampsConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		is := b.CompileItems()

		is.Upsert(store.CompileItem{SourcePath: "t.md", Tier: 3})
		got, err := is.GetByPath("t.md")
		if err != nil || got == nil {
			t.Fatal(err)
		}
		// datetime('now') family: "2006-01-02 15:04:05" UTC shape (spec §5).
		created, err := time.ParseInLocation("2006-01-02 15:04:05", got.CreatedAt, time.UTC)
		if err != nil {
			t.Errorf("CreatedAt %q not in datetime('now') format: %v", got.CreatedAt, err)
		}
		if time.Since(created) > time.Minute {
			t.Errorf("CreatedAt %q not ~now", got.CreatedAt)
		}

		// RFC3339 family: IncrementQueryHits writes RFC3339 (spec §5).
		is.IncrementQueryHits([]string{"t.md"})
		got, _ = is.GetByPath("t.md")
		if _, err := time.Parse(time.RFC3339, got.LastQueriedAt); err != nil {
			t.Errorf("LastQueriedAt %q not RFC3339: %v", got.LastQueriedAt, err)
		}
	}
}

func CJKConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		es := b.Entries()
		es.Add(store.Entry{ID: "src:jp.md", Content: "これはテストです 知識ベース"})
		es.Add(store.Entry{ID: "src:en.md", Content: "english only document"})

		// Parity of behavior, not recall quality (design D5): whatever the
		// backend's tokenizer does with CJK, searching a CJK string and an
		// English term must not error and must return a subset of docs.
		hits, err := es.Search("テスト", nil, 10)
		if err != nil {
			t.Fatalf("CJK search errored: %v", err)
		}
		for _, h := range hits {
			if h.ID != "src:jp.md" && h.ID != "src:en.md" {
				t.Errorf("unexpected doc: %q", h.ID)
			}
		}
		hits, err = es.Search("english", nil, 10)
		if err != nil || len(hits) != 1 || hits[0].ID != "src:en.md" {
			t.Errorf("english search: %+v %v", hits, err)
		}
	}
}

func WriteSerializationConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)

		// WriteTx runs fn and commits.
		if err := b.WriteTx(func(tx *sql.Tx) error { return nil }); err != nil {
			t.Fatalf("WriteTx: %v", err)
		}

		// BeginWrite: mutex released on Commit — a following WriteTx proceeds.
		tx, err := b.BeginWrite()
		if err != nil {
			t.Fatalf("BeginWrite: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		done := make(chan error, 1)
		go func() { done <- b.WriteTx(func(tx *sql.Tx) error { return nil }) }()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("WriteTx after BeginWrite commit: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("WriteTx blocked after BeginWrite commit — mutex leaked")
		}

		// Rollback path also releases.
		tx2, err := b.BeginWrite()
		if err != nil {
			t.Fatalf("BeginWrite 2: %v", err)
		}
		tx2.Rollback()
		tx3, err := b.BeginWrite()
		if err != nil {
			t.Fatalf("BeginWrite 3 after rollback: %v", err)
		}
		tx3.Rollback()
	}
}
