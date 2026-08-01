package mirror

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func (f *shipFixture) writeAndFold(t *testing.T, v string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(f.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (v) VALUES (?)", v); err != nil {
		t.Fatal(err)
	}
	db.Close()
}

func TestPrune_KeepsNewestRetained(t *testing.T) {
	f := newShipFixture(t)
	// Force 3 rotations (debounce defeated via time travel).
	for i, v := range []string{"a", "b", "c"} {
		f.writeAndFold(t, v)
		f.now = f.now.Add(2 * time.Minute)
		res := f.pass(t)
		if !res.Rotated {
			t.Fatalf("rotation %d did not fire: %+v", i, res)
		}
	}
	st := f.remoteState(t)
	if st.Generation != 4 {
		t.Fatalf("generation = %d, want 4", st.Generation)
	}
	// retain_generations=2: gens 4,3 alive; 1,2 pruned.
	for _, k := range f.fake.keys() {
		if strings.Contains(k, "db/generation-1/") || strings.Contains(k, "db/generation-2/") {
			t.Fatalf("pruned generation still present: %s", k)
		}
	}
	// Gen-3 meta survives (PITR depth).
	if _, ok := f.fake.get(GenerationMetaKey("ws/", 3)); !ok {
		t.Fatal("retained generation meta pruned wrongly")
	}
	// Mirror still valid.
	rep, err := f.m.Verify(context.Background())
	if err != nil || !rep.Valid {
		t.Fatalf("verify after prune: %+v %v", rep, err)
	}
}

func TestPrune_FailureIsAdvisory(t *testing.T) {
	f := newShipFixture(t)
	// First rotation (nothing to prune yet).
	f.writeAndFold(t, "a")
	f.now = f.now.Add(2 * time.Minute)
	if res := f.pass(t); !res.Rotated {
		t.Fatal("rotation 1 should fire")
	}
	// Inject delete failure, then rotate again (gen 1 becomes pruneable).
	f.m.pruneDelete = func(ctx context.Context, bucket, key string) error {
		return context.DeadlineExceeded
	}
	f.writeAndFold(t, "b")
	f.now = f.now.Add(2 * time.Minute)
	res := f.pass(t)
	if !res.Rotated {
		t.Fatal("rotation 2 should fire")
	}
	if len(res.Warnings) == 0 {
		t.Fatal("prune failure should surface as a warning, not an error")
	}
	// Commit pointer unaffected.
	st := f.remoteState(t)
	if st.Generation != 3 {
		t.Fatalf("state corrupted by prune failure: gen %d", st.Generation)
	}
}
