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
	t.Run("aliases", AliasConformance(newBackend))
	t.Run("derived_union", DerivedUnionConformance(newBackend))
	t.Run("unlink", UnlinkConformance(newBackend))
	t.Run("trust", TrustConformance(newBackend))
	t.Run("compile_items", CompileItemsConformance(newBackend))
	t.Run("compile_items_queue", CompileItemsQueueConformance(newBackend))
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

		// CountUncompiled: entries join compile_items tier<3 (seeded via
		// WriteTx raw SQL to avoid an import cycle).
		if err := b.WriteTx(func(tx *sql.Tx) error {
			_, err := tx.Exec("INSERT INTO compile_items (source_path, tier) VALUES ('a.md', 1)")
			return err
		}); err != nil {
			t.Fatalf("seed compile_items: %v", err)
		}
		if n, err := es.CountUncompiled("zebra"); err != nil || n != 1 {
			t.Errorf("CountUncompiled = %d, %v; want 1 (only a.md is tier<3)", n, err)
		}
		if n, _ := es.CountUncompiled(""); n != 0 {
			t.Errorf("CountUncompiled(empty) = %d, want 0", n)
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

		// --- P3-1: evidenced relations (runs last; the cascade above left e1
		// and e3 in place with no relations, so these counts stand alone).

		// P3-1: evidence and provenance must round-trip identically on both
		// backends. Postgres stores confidence as DOUBLE PRECISION and the
		// three temporal columns as TEXT (not TIMESTAMPTZ) precisely so this
		// comparison is byte-for-byte rather than format-dependent.
		evidenced := store.Relation{
			ID: "r3", SourceID: "e1", TargetID: "e3", Relation: "extends",
			Evidence: "e1 extends e3", Confidence: 0.75, SourceDoc: "raw/doc.md",
			ValidFrom: "2026-01-15", ValidTo: "2026-06-01", InvalidatedBy: "r9",
		}
		if err := os.AddRelation(evidenced); err != nil {
			t.Fatalf("AddRelation evidenced: %v", err)
		}
		got, err := os.RelationsByType("extends")
		if err != nil || len(got) != 1 {
			t.Fatalf("RelationsByType(extends): %+v %v", got, err)
		}
		if g := got[0]; g.Evidence != evidenced.Evidence || g.Confidence != evidenced.Confidence ||
			g.SourceDoc != evidenced.SourceDoc || g.ValidFrom != evidenced.ValidFrom ||
			g.ValidTo != evidenced.ValidTo || g.InvalidatedBy != evidenced.InvalidatedBy {
			t.Errorf("evidenced relation did not round-trip: %+v", g)
		}

		// A caller that sets none of the new fields reads back zero-valued —
		// the pre-P3-1 shape, which every existing caller still uses.
		if err := os.AddRelation(store.Relation{
			ID: "r4", SourceID: "e3", TargetID: "e1", Relation: "cites",
		}); err != nil {
			t.Fatalf("AddRelation legacy: %v", err)
		}
		legacy, err := os.RelationsByType("cites")
		if err != nil || len(legacy) != 1 {
			t.Fatalf("RelationsByType(cites): %+v %v", legacy, err)
		}
		if l := legacy[0]; l.Evidence != "" || l.Confidence != 0 || l.SourceDoc != "" ||
			l.ValidFrom != "" || l.ValidTo != "" || l.InvalidatedBy != "" {
			t.Errorf("legacy relation read back non-zero: %+v", l)
		}

		// The upsert rule: an existing edge is updated only on strictly
		// higher confidence, and created_at survives. Zero-confidence
		// re-assertion — what every pre-P3-1 caller does — must be a no-op.
		bumped := evidenced
		bumped.Evidence = "e1 extends e3, restated"
		bumped.Confidence = 0.9
		bumped.CreatedAt = "2030-01-01T00:00:00Z"
		if err := os.AddRelation(bumped); err != nil {
			t.Fatalf("AddRelation bumped: %v", err)
		}
		after, err := os.RelationsByType("extends")
		if err != nil || len(after) != 1 {
			t.Fatalf("RelationsByType after bump: %+v %v", after, err)
		}
		if after[0].Evidence != bumped.Evidence || after[0].Confidence != 0.9 {
			t.Errorf("higher confidence did not win: %+v", after[0])
		}
		if after[0].CreatedAt == "2030-01-01T00:00:00Z" {
			t.Error("created_at was overwritten; the earliest assertion's timestamp must survive")
		}
		if err := os.AddRelation(store.Relation{
			ID: "r3", SourceID: "e1", TargetID: "e3", Relation: "extends",
		}); err != nil {
			t.Fatalf("AddRelation zero-confidence: %v", err)
		}
		after, _ = os.RelationsByType("extends")
		if len(after) != 1 || after[0].Evidence != bumped.Evidence {
			t.Errorf("zero-confidence re-assertion erased evidence: %+v", after)
		}

		// AddEntity: empty fields never clobber; type is always writable.
		if err := os.AddEntity(store.Entity{
			ID: "e4", Type: "technique", Name: "Four",
			Definition: "kept", ArticlePath: "wiki/four.md",
		}); err != nil {
			t.Fatalf("AddEntity e4: %v", err)
		}
		if err := os.AddEntity(store.Entity{ID: "e4", Type: "concept", Name: "Four"}); err != nil {
			t.Fatalf("AddEntity e4 re-add: %v", err)
		}
		e4, err := os.GetEntity("e4")
		if err != nil || e4 == nil {
			t.Fatalf("GetEntity e4: %v %v", e4, err)
		}
		if e4.Definition != "kept" || e4.ArticlePath != "wiki/four.md" {
			t.Errorf("empty fields clobbered stored values: %+v", e4)
		}
		if e4.Type != "concept" {
			t.Errorf("type = %q, want %q — type is written unconditionally", e4.Type, "concept")
		}

		// E3: a caller that supplies CreatedAt but not UpdatedAt must not end up
		// with a NULL updated_at. Postgres coupled the two defaults, so
		// nullRFC("") bound NULL and the unconditional SET wrote it over a
		// stored timestamp; sqlite defaulted them independently.
		if err := os.AddEntity(store.Entity{
			ID: "e5", Type: "concept", Name: "Five",
			CreatedAt: "2026-01-01T00:00:00Z", // UpdatedAt deliberately empty
		}); err != nil {
			t.Fatalf("AddEntity e5: %v", err)
		}
		e5, err := os.GetEntity("e5")
		if err != nil || e5 == nil {
			t.Fatalf("GetEntity e5: %v %v", e5, err)
		}
		if e5.UpdatedAt == "" {
			t.Error("updated_at empty when only CreatedAt was supplied (E3)")
		}

		// D11: `WHERE source_id=? OR target_id=?` with `AND relation=?`
		// appended parses as `source_id=? OR (target_id=? AND relation=?)`.
		// SQLite had the unparenthesized form; Postgres did not. The cascade
		// above already destroyed the `implements` edge, so e1 now has an
		// outbound `extends` (r3) and an inbound `cites` (r4) — a Both query
		// filtered to `cites` must return exactly one.
		filtered, err := os.GetRelations("e1", store.Both, "cites")
		if err != nil {
			t.Fatalf("GetRelations(Both, cites): %v", err)
		}
		if len(filtered) != 1 || filtered[0].Relation != "cites" {
			t.Errorf("Both+filter returned %d relations, want exactly the cites edge: %+v", len(filtered), filtered)
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

// CompileItemsQueueConformance covers the durable-queue surface (P2-3,
// spec C2): claim fencing, lease expiry, heartbeat, release outcomes,
// attempt-budget semantics, reset, and the Upsert hash-change revival.
func CompileItemsQueueConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		is := b.CompileItems()

		// Fixture: three pending tier-1 items (indexed done, embed owed).
		for _, p := range []string{"q1.md", "q2.md", "q3.md"} {
			if err := is.Upsert(store.CompileItem{SourcePath: p, Hash: "h1", Tier: 1, PassIndexed: true}); err != nil {
				t.Fatal(err)
			}
		}

		// Claim: basics + limit. Claims don't burn the attempt budget.
		claimed, err := is.Claim(1, "w1", time.Hour, 2)
		if err != nil {
			t.Fatalf("Claim: %v", err)
		}
		if len(claimed) != 2 {
			t.Fatalf("Claim limit: got %d items, want 2", len(claimed))
		}
		for _, it := range claimed {
			if it.Status != "leased" || it.LeaseOwner != "w1" || it.Attempts != 0 {
				t.Errorf("claimed item %+v: want status=leased owner=w1 attempts=0", it)
			}
			if it.LeaseUntil == "" || it.HeartbeatAt == "" {
				t.Errorf("claimed item %s missing lease timestamps", it.SourcePath)
			}
		}

		// Fencing: w2 cannot claim w1's live leases; the unclaimed third
		// item goes to w2.
		claimed2, err := is.Claim(1, "w2", time.Hour, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(claimed2) != 1 || claimed2[0].SourcePath != "q3.md" {
			t.Errorf("w2 claimed %+v, want only [q3.md]", claimed2)
		}

		// Heartbeat extends the lease (TTL 2h vs the claimed 1h — always
		// distinguishable without sleeping).
		first, _ := is.GetByPath("q1.md")
		if err := is.Heartbeat("w1", []string{"q1.md"}, 2*time.Hour); err != nil {
			t.Fatalf("Heartbeat: %v", err)
		}
		refreshed, _ := is.GetByPath("q1.md")
		if refreshed.LeaseUntil == first.LeaseUntil {
			t.Errorf("Heartbeat did not extend lease_until (%q)", first.LeaseUntil)
		}

		// Release(retry): pending again, lease cleared, attempt budget
		// burned (attempts=1).
		if err := is.Release("q3.md", "w2", store.ReleaseRetry); err != nil {
			t.Fatal(err)
		}
		got, _ := is.GetByPath("q3.md")
		if got.Status != "pending" || got.LeaseOwner != "" || got.LeaseUntil != "" || got.Attempts != 1 {
			t.Errorf("Release(retry): %+v, want pending, cleared lease, attempts=1", got)
		}

		// Release(done) with owed passes → pending (progress, budget
		// reset); complete the pass → done.
		if err := is.Release("q1.md", "w1", store.ReleaseDone); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("q1.md")
		if got.Status != "pending" || got.Attempts != 0 {
			t.Errorf("Release(done) with owed passes: %+v, want pending attempts=0", got)
		}
		if _, err := is.Claim(1, "w1", time.Hour, 10); err != nil {
			t.Fatal(err)
		}
		if err := is.MarkPass("q1.md", "embedded"); err != nil {
			t.Fatal(err)
		}
		if err := is.Release("q1.md", "w1", store.ReleaseDone); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("q1.md")
		if got.Status != "done" {
			t.Errorf("Release(done) tier-complete: status=%q, want done", got.Status)
		}

		// Release(failed): dead-lettered, excluded from future claims.
		if err := is.Release("q2.md", "w1", store.ReleaseFailed); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("q2.md")
		if got.Status != "failed" {
			t.Errorf("Release(failed): status=%q, want failed", got.Status)
		}
		remaining, err := is.Claim(1, "w9", time.Hour, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range remaining {
			if it.SourcePath == "q2.md" {
				t.Error("dead-lettered item re-claimed")
			}
		}

		// RequeueExpired: only expired leases return to pending. Dedicated
		// item so earlier claims don't interfere: q4 is claimed with an
		// already-expired TTL.
		if err := is.Upsert(store.CompileItem{SourcePath: "q4.md", Hash: "h1", Tier: 1, PassIndexed: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := is.Claim(1, "w3", -time.Hour, 10); err != nil {
			t.Fatal(err)
		}
		n, err := is.RequeueExpired(time.Now().UTC())
		if err != nil {
			t.Fatalf("RequeueExpired: %v", err)
		}
		if n != 1 {
			t.Errorf("RequeueExpired = %d, want 1", n)
		}
		got, _ = is.GetByPath("q4.md")
		if got.Status != "pending" || got.LeaseOwner != "" {
			t.Errorf("requeued item: %+v, want pending with cleared lease", got)
		}

		// ResetFailed: dead letters rejoin the queue with a fresh budget.
		if n, err := is.ResetFailed(); err != nil || n != 1 {
			t.Errorf("ResetFailed = %d, %v; want 1", n, err)
		}
		got, _ = is.GetByPath("q2.md")
		if got.Status != "pending" || got.Attempts != 0 {
			t.Errorf("ResetFailed: %+v, want pending attempts=0", got)
		}

		// Upsert hash-change revival: a failed item re-upserted with new
		// content resets to pending with a fresh budget; same-hash upsert
		// preserves queue state.
		if _, err := is.Claim(1, "w1", time.Hour, 10); err != nil {
			t.Fatal(err)
		}
		if err := is.Release("q2.md", "w1", store.ReleaseFailed); err != nil {
			t.Fatal(err)
		}
		if err := is.Upsert(store.CompileItem{SourcePath: "q2.md", Hash: "h2", Tier: 1}); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("q2.md")
		if got.Status != "pending" || got.Attempts != 0 || got.LeaseOwner != "" {
			t.Errorf("hash-change Upsert: %+v, want pending attempts=0 lease cleared", got)
		}
		if _, err := is.Claim(1, "w1", time.Hour, 10); err != nil {
			t.Fatal(err)
		}
		if err := is.Upsert(store.CompileItem{SourcePath: "q2.md", Hash: "h2", Tier: 1}); err != nil {
			t.Fatal(err)
		}
		got, _ = is.GetByPath("q2.md")
		if got.Status != "leased" {
			t.Errorf("same-hash Upsert touched queue state: %+v", got)
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

// AliasConformance exercises entity resolution (P3-3) on both backends.
//
// Registered in RunConformance above — an unregistered sub-test compiles and
// silently never runs, which would leave the two-backend claim unproven while
// looking covered.
func AliasConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		os := b.Ontology()

		mk := func(alias, canonical string, status store.AliasStatus) store.EntityAlias {
			return store.EntityAlias{
				Alias: alias, CanonicalID: canonical, EntityType: "concept",
				Status: status, Confidence: 0.9, Reason: "same referent",
				Source: "llm", CreatedAt: "2026-07-26T00:00:00Z",
			}
		}

		// Round-trip, including the audit fields.
		if err := os.PutAlias(mk("edwin", "buzz", store.AliasApplied)); err != nil {
			t.Fatalf("PutAlias: %v", err)
		}
		got, err := os.GetActiveAlias("edwin")
		if err != nil || got == nil {
			t.Fatalf("GetActiveAlias: %+v %v", got, err)
		}
		if got.CanonicalID != "buzz" || got.Status != store.AliasApplied ||
			got.Confidence != 0.9 || got.Source != "llm" {
			t.Errorf("alias round-trip lost fields: %+v", got)
		}
		// Timestamps are TEXT on both backends and must come back byte-identical
		// — this is what the raw-string binding (not nullRFC) exists to protect.
		if got.CreatedAt != "2026-07-26T00:00:00Z" {
			t.Errorf("CreatedAt = %q, want byte-identical RFC3339 across backends", got.CreatedAt)
		}

		if missing, err := os.GetActiveAlias("nobody"); err != nil || missing != nil {
			t.Errorf("GetActiveAlias(absent) = %+v, %v; want nil, nil", missing, err)
		}

		// Rejections are symmetric and are never overwritten by an auto-apply.
		if err := os.PutAlias(mk("musician", "astronaut", store.AliasRejected)); err != nil {
			t.Fatalf("PutAlias rejected: %v", err)
		}
		for _, pair := range [][2]string{{"musician", "astronaut"}, {"astronaut", "musician"}} {
			rejected, err := os.IsRejected(pair[0], pair[1])
			if err != nil {
				t.Fatalf("IsRejected(%s,%s): %v", pair[0], pair[1], err)
			}
			if !rejected {
				t.Errorf("IsRejected(%s,%s) = false; rejection must hold in both directions",
					pair[0], pair[1])
			}
		}
		if err := os.PutAlias(mk("musician", "astronaut", store.AliasApplied)); err != nil {
			t.Fatalf("PutAlias over rejected must be a no-op, not an error: %v", err)
		}
		if act, _ := os.GetActiveAlias("musician"); act != nil {
			t.Errorf("a rejected row was flipped to active: %+v", act)
		}

		// The sweep re-puts applied rows every compile; origin fields must survive.
		resweep := mk("edwin", "buzz", store.AliasApplied)
		resweep.CreatedAt = "2027-01-01T00:00:00Z"
		resweep.Source = "manual"
		if err := os.PutAlias(resweep); err != nil {
			t.Fatalf("PutAlias resweep: %v", err)
		}
		after, _ := os.GetActiveAlias("edwin")
		if after == nil || after.CreatedAt != "2026-07-26T00:00:00Z" || after.Source != "llm" {
			t.Errorf("sweep rewrote the audit origin: %+v", after)
		}

		// Listing is status-filtered and deterministically ordered.
		if err := os.PutAlias(mk("aa", "buzz", store.AliasApplied)); err != nil {
			t.Fatal(err)
		}
		applied, err := os.ListAliases(store.AliasApplied)
		if err != nil {
			t.Fatalf("ListAliases: %v", err)
		}
		if len(applied) != 2 {
			t.Fatalf("applied rows = %d, want 2", len(applied))
		}
		if applied[0].Alias != "aa" || applied[1].Alias != "edwin" {
			t.Errorf("ListAliases order = [%s %s], want sorted [aa edwin]",
				applied[0].Alias, applied[1].Alias)
		}

		// Status transition clears the active row.
		if err := os.SetAliasStatus("aa", "buzz", store.AliasRejected, "user"); err != nil {
			t.Fatalf("SetAliasStatus: %v", err)
		}
		if act, _ := os.GetActiveAlias("aa"); act != nil {
			t.Errorf("row still active after rejection: %+v", act)
		}

		// CanonicalID follows applied chains, not pending ones.
		if err := os.PutAlias(mk("buzz", "aldrin", store.AliasApplied)); err != nil {
			t.Fatal(err)
		}
		if id, err := os.CanonicalID("edwin"); err != nil || id != "aldrin" {
			t.Errorf("CanonicalID(edwin) = %q, %v; want aldrin (edwin->buzz->aldrin)", id, err)
		}
		if err := os.PutAlias(mk("pend", "target", store.AliasPending)); err != nil {
			t.Fatal(err)
		}
		if id, err := os.CanonicalID("pend"); err != nil || id != "pend" {
			t.Errorf("CanonicalID(pend) = %q, %v; a pending row must not be followed", id, err)
		}
		if id, err := os.CanonicalID("unknown"); err != nil || id != "unknown" {
			t.Errorf("CanonicalID(unknown) = %q, %v; want the input id", id, err)
		}

		// --- LinkAlias: non-destructive edge union, both backends ---
		lb := new(t)
		lo := lb.Ontology()
		for _, id := range []string{"edwin", "buzz", "apollo"} {
			if err := lo.AddEntity(store.Entity{ID: id, Type: "concept", Name: id}); err != nil {
				t.Fatalf("AddEntity %s: %v", id, err)
			}
		}
		// The canonical asserts an edge itself, at LOWER confidence than the
		// alias's competing assertion of the same (target, relation).
		if err := lo.AddRelation(store.Relation{ID: "own", SourceID: "buzz", TargetID: "apollo",
			Relation: "extends", Evidence: "buzz extends apollo", Confidence: 0.6,
			SourceDoc: "raw/buzz.md"}); err != nil {
			t.Fatal(err)
		}
		if err := lo.AddRelation(store.Relation{ID: "aliased", SourceID: "edwin", TargetID: "apollo",
			Relation: "extends", Evidence: "edwin extends apollo", Confidence: 0.9,
			SourceDoc: "raw/edwin.md"}); err != nil {
			t.Fatal(err)
		}
		// An alias<->canonical edge: cannot be copied without becoming a
		// self-loop, and must be RETAINED rather than deleted.
		if err := lo.AddRelation(store.Relation{ID: "loop", SourceID: "edwin", TargetID: "buzz",
			Relation: "contradicts", Confidence: 0.4}); err != nil {
			t.Fatal(err)
		}
		beforeLink, err := lo.RelationCount()
		if err != nil {
			t.Fatal(err)
		}
		beforeLink++ // the gemini edge added just below

		// A third entity the canonical does NOT already point at, so the insert
		// branch actually executes. Without it Copied is 0 on both backends and
		// the Postgres INSERT path — nullRFC binding, RowsAffected on a real
		// insert, the alias: id — is never exercised.
		if err := lo.AddEntity(store.Entity{ID: "gemini", Type: "concept", Name: "gemini"}); err != nil {
			t.Fatal(err)
		}
		if err := lo.AddRelation(store.Relation{ID: "only-alias", SourceID: "edwin", TargetID: "gemini",
			Relation: "extends", Evidence: "edwin extends gemini", Confidence: 0.55,
			SourceDoc: "raw/edwin.md"}); err != nil {
			t.Fatal(err)
		}

		res, err := lo.LinkAlias(mk("edwin", "buzz", store.AliasApplied))
		if err != nil {
			t.Fatalf("LinkAlias: %v", err)
		}
		if res.Copied != 1 {
			t.Errorf("Copied = %d, want 1 (the edge only the alias had)", res.Copied)
		}
		// The copy landed with the alias's provenance and a derived id.
		copied, err := lo.GetRelations("buzz", store.Outbound, "extends")
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, r := range copied {
			if r.TargetID == "gemini" {
				found = true
				if r.Evidence != "edwin extends gemini" || r.SourceDoc != "raw/edwin.md" {
					t.Errorf("copy lost provenance: %+v", r)
				}
				if len(r.ID) != len("alias:")+16 {
					t.Errorf("copied id = %q, want alias: + 16 hex chars", r.ID)
				}
			}
		}
		if !found {
			t.Errorf("the copied edge is missing from the canonical: %+v", copied)
		}
		if res.Skipped != 1 {
			t.Errorf("Skipped = %d, want 1 (the canonical already asserted that edge)", res.Skipped)
		}
		if res.SelfLoops != 1 {
			t.Errorf("SelfLoops = %d, want 1", res.SelfLoops)
		}

		// The canonical's OWN evidence survives. A copy must never overwrite a
		// native assertion — the confidence-guarded upsert is sound only when
		// both sides assert the same edge, which a copy does not.
		canon, err := lo.GetRelations("buzz", store.Outbound, "extends")
		if err != nil {
			t.Fatalf("GetRelations: %v", err)
		}
		var own *store.Relation
		for i := range canon {
			if canon[i].TargetID == "apollo" {
				own = &canon[i]
			}
		}
		if own == nil {
			t.Fatalf("the canonical's own edge disappeared: %+v", canon)
		}
		if own.Evidence != "buzz extends apollo" || own.Confidence != 0.6 ||
			own.SourceDoc != "raw/buzz.md" {
			t.Errorf("canonical's own edge was overwritten by the copy: %+v", own)
		}

		// Nothing was deleted: the alias keeps every edge it had.
		afterLink, err := lo.RelationCount()
		if err != nil {
			t.Fatal(err)
		}
		// One copy ADDED (the gemini edge); nothing removed.
		if afterLink != beforeLink+1 {
			t.Errorf("relation count %d -> %d, want +1 (one copy added, nothing removed)",
				beforeLink, afterLink)
		}
		aliasEdges, err := lo.GetRelations("edwin", store.Both, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(aliasEdges) != 3 {
			t.Errorf("alias edges = %d, want 3 retained (linking deletes nothing)", len(aliasEdges))
		}

		// Idempotent: the sweep re-runs this on every compile.
		again, err := lo.LinkAlias(mk("edwin", "buzz", store.AliasApplied))
		if err != nil {
			t.Fatalf("LinkAlias re-run: %v", err)
		}
		if again.Copied != 0 {
			t.Errorf("re-link Copied = %d, want 0", again.Copied)
		}

		// Missing endpoints are typed facts, not errors, and write no alias row.
		miss, err := lo.LinkAlias(mk("ghost", "buzz", store.AliasApplied))
		if err != nil || !miss.AliasMissing {
			t.Errorf("missing alias: %+v %v; want AliasMissing, no error", miss, err)
		}
		if act, _ := lo.GetActiveAlias("ghost"); act != nil {
			t.Errorf("alias row written for a missing alias: %+v", act)
		}
		miss, err = lo.LinkAlias(mk("apollo", "ghost", store.AliasApplied))
		if err != nil || !miss.CanonicalMissing {
			t.Errorf("missing canonical: %+v %v; want CanonicalMissing, no error", miss, err)
		}

		// The partial unique index is the real enforcement of one live decision
		// per alias, and it must behave identically on both backends.
		ib := new(t)
		io := ib.Ontology()
		if err := io.PutAlias(mk("dup", "c1", store.AliasApplied)); err != nil {
			t.Fatalf("first active row: %v", err)
		}
		if err := io.PutAlias(mk("dup", "c2", store.AliasPending)); err == nil {
			t.Error("a SECOND active row for one alias was accepted; the partial unique index is not enforcing")
		}
		// Rejections are exempt and may accumulate — that is why the key is the
		// pair and the index is partial.
		for _, c := range []string{"c3", "c4"} {
			if err := io.PutAlias(mk("dup", c, store.AliasRejected)); err != nil {
				t.Errorf("rejected row %s must be allowed alongside an active one: %v", c, err)
			}
		}

		// CanonicalID must terminate on a cycle rather than loop.
		cb := new(t)
		co := cb.Ontology()
		if err := co.PutAlias(mk("x", "y", store.AliasApplied)); err != nil {
			t.Fatal(err)
		}
		if err := co.PutAlias(mk("y", "x", store.AliasApplied)); err != nil {
			t.Fatal(err)
		}
		done := make(chan string, 1)
		go func() { id, _ := co.CanonicalID("x"); done <- id }()
		select {
		case id := <-done:
			if id != "x" {
				t.Errorf("CanonicalID on a cycle = %q, want the input id", id)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("CanonicalID looped forever on a cycle")
		}
	}
}

// DerivedUnionConformance proves both backends agree on decision-035's union
// semantics. It could not exist before M3: until LinkAlias wrote derived rows,
// the interface had no way to create one, so each backend could only be checked
// against itself with raw SQL.
func DerivedUnionConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		os := b.Ontology()
		mk := func(id string) {
			t.Helper()
			if err := os.AddEntity(store.Entity{ID: id, Type: "concept", Name: id}); err != nil {
				t.Fatal(err)
			}
		}
		for _, id := range []string{"canon", "alias", "x", "y"} {
			mk(id)
		}
		// The alias owns an edge the canonical does not.
		if err := os.AddRelation(store.Relation{
			ID: "r1", SourceID: "alias", TargetID: "x", Relation: "extends", Evidence: "ALIAS-EDGE",
		}); err != nil {
			t.Fatal(err)
		}
		// ...and both assert one to y, so the canonical's own must win.
		if err := os.AddRelation(store.Relation{
			ID: "r2", SourceID: "alias", TargetID: "y", Relation: "extends", Evidence: "ALIAS",
		}); err != nil {
			t.Fatal(err)
		}
		if err := os.AddRelation(store.Relation{
			ID: "r3", SourceID: "canon", TargetID: "y", Relation: "extends", Evidence: "CANONICAL",
		}); err != nil {
			t.Fatal(err)
		}

		res, err := os.LinkAlias(store.EntityAlias{
			Alias: "alias", CanonicalID: "canon", EntityType: "concept",
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("LinkAlias: %v", err)
		}
		if res.Copied != 1 {
			t.Errorf("Copied = %d, want 1 (only alias->x is new)", res.Copied)
		}
		if res.Skipped != 1 {
			t.Errorf("Skipped = %d, want 1 (canon->y already asserted natively)", res.Skipped)
		}

		// The canonical now sees the alias's edge, through the union.
		rels, err := os.GetRelations("canon", store.Outbound, "")
		if err != nil {
			t.Fatal(err)
		}
		byTarget := map[string]string{}
		for _, r := range rels {
			byTarget[r.TargetID] = r.Evidence
		}
		if byTarget["x"] != "ALIAS-EDGE" {
			t.Errorf("canon->x evidence = %q, want ALIAS-EDGE (derived)", byTarget["x"])
		}
		if byTarget["y"] != "CANONICAL" {
			t.Errorf("canon->y evidence = %q, want CANONICAL — a derived row must never displace an original", byTarget["y"])
		}
		if len(rels) != 2 {
			t.Errorf("canon outbound = %d, want 2: %+v", len(rels), rels)
		}

		// The alias keeps its own edges — this links, it does not collapse.
		aliasRels, err := os.GetRelations("alias", store.Outbound, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(aliasRels) != 2 {
			t.Errorf("alias outbound = %d, want 2 (its own rows survive)", len(aliasRels))
		}

		// Every whole-graph edge read must agree. ListRelations is here because
		// it was unioned on Postgres and NOT on SQLite, and nothing caught it:
		// the two backends answered `ontology list --type relations`
		// differently. A method named in the design as needing the union but
		// absent from this test is exactly how that ships.
		listed, err := os.ListRelations("", -1)
		if err != nil {
			t.Fatal(err)
		}

		// RelationCount and AllRelations must agree — both are whole-graph edge
		// reads, and disagreeing was a real defect found while implementing M3.
		n, err := os.RelationCount()
		if err != nil {
			t.Fatal(err)
		}
		all, err := os.AllRelations()
		if err != nil {
			t.Fatal(err)
		}
		if n != len(all) {
			t.Errorf("RelationCount() = %d but len(AllRelations()) = %d — both are whole-graph edge reads", n, len(all))
		}
		if len(listed) != len(all) {
			t.Errorf("ListRelations() = %d but AllRelations() = %d — both must union derived edges",
				len(listed), len(all))
		}
	}
}

// UnlinkConformance is decision-035's headline: a link that can be taken back.
// Three cycles failed to deliver this, so the assertions are deliberately about
// the END STATE of the graph, not about what the API returned.
func UnlinkConformance(new BackendFactory) func(*testing.T) {
	return func(t *testing.T) {
		b := new(t)
		os := b.Ontology()
		for _, id := range []string{"canon", "alias", "x"} {
			if err := os.AddEntity(store.Entity{ID: id, Type: "concept", Name: id}); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.AddRelation(store.Relation{
			ID: "r1", SourceID: "alias", TargetID: "x", Relation: "extends",
		}); err != nil {
			t.Fatal(err)
		}

		before, err := os.AllRelations()
		if err != nil {
			t.Fatal(err)
		}

		link := store.EntityAlias{
			Alias: "alias", CanonicalID: "canon", EntityType: "concept",
			Status: store.AliasApplied, Source: "llm", CreatedAt: "2026-07-27T00:00:00Z",
		}
		if _, err := os.LinkAlias(link); err != nil {
			t.Fatal(err)
		}
		mid, err := os.GetRelations("canon", store.Outbound, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(mid) != 1 {
			t.Fatalf("canon should see the derived edge, got %+v", mid)
		}

		if err := os.UnlinkAlias("alias", "canon"); err != nil {
			t.Fatalf("UnlinkAlias: %v", err)
		}

		// The graph must be indistinguishable from before the link.
		after, err := os.AllRelations()
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Errorf("relations after unlink = %d, want %d (pre-link): %+v", len(after), len(before), after)
		}
		post, err := os.GetRelations("canon", store.Outbound, "")
		if err != nil {
			t.Fatal(err)
		}
		if len(post) != 0 {
			t.Errorf("canon still sees %d edges after unlink: %+v", len(post), post)
		}

		// And the pair must be rejected, or the next compile re-applies it. A
		// delete alone is a pause, not an undo.
		rejected, err := os.IsRejected("alias", "canon")
		if err != nil {
			t.Fatal(err)
		}
		if !rejected {
			t.Error("unlink left the pair un-rejected — the next compile would re-apply it")
		}
		if active, err := os.GetActiveAlias("alias"); err != nil {
			t.Fatal(err)
		} else if active != nil {
			t.Errorf("alias still has an active row after unlink: %+v", active)
		}
	}
}
