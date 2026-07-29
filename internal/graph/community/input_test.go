package community

import (
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
)

// P3-5 T2: detection input — one batch entity load, exclusions by class,
// liveness via the shared ontology.LiveAt predicate.

func inputStore(t *testing.T) store.OntologyStore {
	t.Helper()
	db, err := storage.Open(t.TempDir() + "/in.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := ontology.NewStore(db, nil, nil)
	mk := func(id, typ string) {
		t.Helper()
		if err := s.AddEntity(store.Entity{ID: id, Type: typ, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	mk("a", "concept")
	mk("b", "concept")
	mk("c", "concept")
	mk("d", "concept")
	mk("src1", "source")

	add := func(id, from, to, rel, vt string) {
		t.Helper()
		r := store.Relation{ID: id, SourceID: from, TargetID: to, Relation: rel, ValidTo: vt}
		if err := s.AddRelation(r); err != nil {
			t.Fatal(err)
		}
	}
	add("r1", "a", "b", "extends", "")                     // keep
	add("r2", "b", "c", "extends", "2020-01-01T00:00:00Z") // invalidated → drop
	add("r3", "c", "d", "extends", "2099-01-01T00:00:00Z") // future valid_to → still live, keep
	add("r4", "a", "d", "cites", "")                       // cites → drop
	add("r5", "src1", "a", "extends", "")                  // source endpoint → drop
	add("r7", "a", "c", "extends", "")                     // keep
	// NOTE: the missing-endpoint filter is defensive — the relations FK
	// (entities ON DELETE CASCADE) makes a dangling endpoint unwritable.
	return s
}

func TestBuildInputFilters(t *testing.T) {
	nodes, edges, err := BuildInput(inputStore(t))
	if err != nil {
		t.Fatal(err)
	}
	// r1, r3, r7 survive → edges a-b, c-d, a-c; nodes a,b,c,d (no src1).
	if len(edges) != 3 {
		t.Errorf("edges = %v, want 3 (a-b, c-d, a-c)", edges)
	}
	wantNodes := map[string]bool{"a": true, "b": true, "c": true, "d": true}
	if len(nodes) != len(wantNodes) {
		t.Fatalf("nodes = %v", nodes)
	}
	for _, n := range nodes {
		if !wantNodes[n] {
			t.Errorf("unexpected node %q", n)
		}
	}
	for _, e := range edges {
		if e.From == "src1" || e.To == "src1" {
			t.Errorf("filtered edge present: %+v", e)
		}
		if (e.From == "b" && e.To == "c") || (e.From == "a" && e.To == "d") {
			t.Errorf("invalidated/cites edge present: %+v", e)
		}
	}
}

func TestBuildInputIsolatedExcluded(t *testing.T) {
	db, err := storage.Open(t.TempDir() + "/iso.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := ontology.NewStore(db, nil, nil)
	s.AddEntity(store.Entity{ID: "lone", Type: "concept", Name: "lone"})
	s.AddEntity(store.Entity{ID: "x", Type: "concept", Name: "x"})
	s.AddEntity(store.Entity{ID: "y", Type: "concept", Name: "y"})
	s.AddRelation(store.Relation{ID: "r", SourceID: "x", TargetID: "y", Relation: "extends"})

	nodes, _, err := BuildInput(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if n == "lone" {
			t.Error("isolated entities must be excluded from detection input")
		}
	}
}

func TestLiveAtMatchesPredicate(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		vf, vt string
		want   bool
	}{
		{"", "", true},
		{"2020-01-01T00:00:00Z", "", true},
		{"", "2020-01-01T00:00:00Z", false},
		{"", "2099-01-01T00:00:00Z", true},
		{"2099-01-01T00:00:00Z", "", false},
		{"2020-01-01T00:00:00Z", "2021-01-01T00:00:00Z", false},
	}
	for _, c := range cases {
		got := ontology.LiveAt(store.Relation{ValidFrom: c.vf, ValidTo: c.vt}, now)
		if got != c.want {
			t.Errorf("LiveAt(%q,%q) = %v, want %v", c.vf, c.vt, got, c.want)
		}
	}
}
