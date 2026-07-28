package sourcedate

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/storage"
)

// T3.2 done-criterion: the backfill resolves all three chain tiers, leaves
// a dateless source absent, aggregates concepts as max-of-sources, and is
// idempotent.
func TestBackfill(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mem := memory.NewStore(db)

	// Three date sources + one dateless.
	os.WriteFile(filepath.Join(dir, "dated.md"), []byte("---\ndate: 2024-03-15\n---\nbody\n"), 0644)
	mt := time.Date(2022, 1, 2, 3, 4, 5, 0, time.UTC)
	os.WriteFile(filepath.Join(dir, "plain.md"), []byte("plain body\n"), 0644)
	os.Chtimes(filepath.Join(dir, "plain.md"), mt, mt)
	// gone.md: absent on disk, manifest first-seen only.
	// nothing.md: absent on disk, no first-seen — stays dateless.

	m := manifest.New()
	m.Sources["dated.md"] = manifest.Source{AddedAt: "2020-01-01T00:00:00Z"}
	m.Sources["plain.md"] = manifest.Source{AddedAt: "2020-01-01T00:00:00Z"}
	m.Sources["gone.md"] = manifest.Source{AddedAt: "2021-05-05T00:00:00Z"}
	m.Sources["nothing.md"] = manifest.Source{}
	m.Concepts["topic"] = manifest.Concept{ArticlePath: "wiki/concepts/topic.md", Sources: []string{"dated.md", "plain.md"}}

	for _, id := range []string{"src:dated.md", "src:plain.md", "src:gone.md", "src:nothing.md", "concept:topic"} {
		mem.Add(memory.Entry{ID: id, Content: "x"})
	}

	n, err := Backfill(dir, mem, m)
	if err != nil {
		t.Fatal(err)
	}
	// Three dated sources × two identities (bare summary ID + src:) + one
	// concept = 7; nothing.md stays dateless.
	if n != 7 {
		t.Errorf("backfilled %d, want 7", n)
	}
	// Both identities of a source carry the same date (F-060: the bare
	// path is the Tier-3 summary doc ID — the dominant doc class).
	bare, err := mem.GetSourceDates([]string{"dated.md", "plain.md", "gone.md"})
	if err != nil || len(bare) != 3 {
		t.Fatalf("bare summary IDs not dated: %v %v", bare, err)
	}

	dates, err := mem.GetSourceDates([]string{"src:dated.md", "src:plain.md", "src:gone.md", "src:nothing.md", "concept:topic"})
	if err != nil {
		t.Fatal(err)
	}
	fmWant := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	if dates["src:dated.md"] != fmWant {
		t.Errorf("dated.md = %d, want frontmatter %d", dates["src:dated.md"], fmWant)
	}
	if dates["src:plain.md"] != mt.Unix() {
		t.Errorf("plain.md = %d, want mtime %d", dates["src:plain.md"], mt.Unix())
	}
	if want := time.Date(2021, 5, 5, 0, 0, 0, 0, time.UTC).Unix(); dates["src:gone.md"] != want {
		t.Errorf("gone.md = %d, want first-seen %d", dates["src:gone.md"], want)
	}
	if _, ok := dates["src:nothing.md"]; ok {
		t.Error("nothing.md must stay dateless — absence, never fabricated")
	}
	if dates["concept:topic"] != fmWant {
		t.Errorf("concept = %d, want max-of-sources %d", dates["concept:topic"], fmWant)
	}

	// Idempotent: nothing new on the second run.
	n2, err := Backfill(dir, mem, m)
	if err != nil || n2 != 0 {
		t.Errorf("second run set %d (err %v), want 0", n2, err)
	}
}

// F-071 (Gate-8 QA repro): tier-0/1 corpora index sources WITHOUT
// registering them in the manifest — Backfill must still date their
// entries from the entry IDs, and stay idempotent.
func TestBackfillUnmanifestedTierZeroEntries(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mem := memory.NewStore(db)

	os.WriteFile(filepath.Join(dir, "note.md"), []byte("---\ndate: 2024-03-15\n---\nbody\n"), 0644)
	// The tier-0 shape: src: entry indexed, manifest EMPTY.
	mem.Add(memory.Entry{ID: "src:note.md", Content: "body"})

	m := manifest.New() // sources: {}
	n, err := Backfill(dir, mem, m)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // both identities
		t.Errorf("backfilled %d, want 2", n)
	}
	dates, err := mem.GetSourceDates([]string{"src:note.md", "note.md"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC).Unix()
	if dates["src:note.md"] != want || dates["note.md"] != want {
		t.Errorf("unmanifested dates = %v, want both %d", dates, want)
	}

	n2, err := Backfill(dir, mem, m)
	if err != nil || n2 != 0 {
		t.Errorf("second run set %d (err %v), want 0", n2, err)
	}
}
