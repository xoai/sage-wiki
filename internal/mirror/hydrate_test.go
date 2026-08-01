package mirror

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// hydrateFixture builds a mirrored workspace then wipes it for restore.
type hydrateFixture struct {
	src  *shipFixture
	cfg  Config
	fake *fakeS3
}

func newHydrateFixture(t *testing.T) *hydrateFixture {
	t.Helper()
	f := newShipFixture(t)
	defer f.dbClose()
	// Content: docs, db rows, vectors.
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	writeWS(t, f.dir, "raw/paper.pdf", "PDF")
	writeWS(t, f.dir, ".sage/vectors.idx", "SWVI-1")
	f.dbWrite(t, "row-1")
	f.dbClose()
	// Defeat the rotation debounce (post-enable window) so row-1 ships in a
	// forced rotation this pass rather than deferring.
	ageLocalRotationFile(t, f.dir, -2*time.Hour)
	f.pass(t)
	_, cfg := setupFakeMirror(t, f.fake)
	return &hydrateFixture{src: f, cfg: cfg, fake: f.fake}
}

func treeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		b, _ := os.ReadFile(path)
		out[filepath.ToSlash(rel)] = string(b)
		return nil
	})
	return out
}

func TestHydrate_RoundTrip(t *testing.T) {
	h := newHydrateFixture(t)
	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{})
	if err != nil {
		t.Fatalf("Hydrate: %v", err)
	}
	// Tree compare for shipped files.
	files := treeFiles(t, dst)
	if files["wiki/concepts/Foo.md"] != "# Foo" {
		t.Fatalf("Foo.md = %q", files["wiki/concepts/Foo.md"])
	}
	if files["raw/paper.pdf"] != "PDF" {
		t.Fatal("raw/ not restored")
	}
	if files[".sage/vectors.idx"] != "SWVI-1" {
		t.Fatal("vectors not restored")
	}
	// DB restorable: row-1 present (sealed segment or snapshot).
	db, err := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v string
	if err := db.QueryRow("SELECT v FROM t WHERE v='row-1'").Scan(&v); err != nil {
		t.Fatalf("restored db missing row-1: %v", err)
	}
	if rep.Generation != 2 {
		t.Fatalf("Generation = %d, want 2 (forced rotation shipped row-1)", rep.Generation)
	}
}

func TestHydrate_NonEmptyDir(t *testing.T) {
	h := newHydrateFixture(t)
	dst := t.TempDir()
	os.WriteFile(filepath.Join(dst, "existing"), []byte("x"), 0o644)
	if _, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{}); err == nil {
		t.Fatal("hydrate into non-empty dir must fail (no merge semantics)")
	}
}

func TestHydrate_SHAMismatchAborts_NamesObject(t *testing.T) {
	h := newHydrateFixture(t)
	// Corrupt the committed doc object.
	st := h.src.remoteState(t)
	for _, ref := range st.Objects {
		h.fake.objects[ref.Key] = []byte("CORRUPTED")
	}
	dst := filepath.Join(t.TempDir(), "restored")
	_, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{})
	if err == nil {
		t.Fatal("sha mismatch must abort hydrate")
	}
	for key := range st.Objects {
		if strings.Contains(err.Error(), st.Objects[key].Key) {
			return
		}
	}
	t.Fatalf("error should name the corrupted object: %v", err)
}

// TestHydrate_CorruptVectorsWarns: vectors are rebuildable — a corrupt SWVI
// object warns and continues (spec §Edge cases per-class rule).
func TestHydrate_CorruptVectorsWarns(t *testing.T) {
	h := newHydrateFixture(t)
	st := h.src.remoteState(t)
	for _, ref := range st.Vectors {
		h.fake.objects[ref.Key] = []byte("CORRUPTED")
	}
	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{})
	if err != nil {
		t.Fatalf("corrupt vectors must not abort: %v", err)
	}
	if len(rep.Advisories) == 0 {
		t.Fatal("corrupt vector should produce an advisory")
	}
	if _, err := os.Stat(filepath.Join(dst, ".sage", "wiki.db")); err != nil {
		t.Fatal("db not restored")
	}
}

func TestHydrate_TombstonedFileAbsent(t *testing.T) {
	h := newHydrateFixture(t)
	deleteWS(t, h.src.dir, "wiki/concepts/Foo.md")
	h.src.pass(t)
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "wiki/concepts/Foo.md")); !os.IsNotExist(err) {
		t.Fatal("tombstoned file restored — should stay absent")
	}
}

// TestHydrate_PITR table test: two-generation timeline anchored on the
// actual committed timestamps (fabricated absolute times are flaky).
func TestHydrate_PITR(t *testing.T) {
	h := newHydrateFixture(t) // row-1 shipped in generation 2
	gen2Created := h.src.remoteState(t).DB.CreatedAt

	// Force generation 3 with row-gen2 (spaced 2s on the injected clock so
	// second-precision created_at boundaries stay distinct).
	h.src.now = gen2Created.Add(2 * time.Second)
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation to gen 3 did not fire")
	}
	gen3Created := h.src.remoteState(t).DB.CreatedAt

	// A gen-3 segment (row-gen2-seg), sealed AFTER gen3Created.
	h.src.now = gen3Created.Add(time.Second)
	h.src.dbWrite(t, "row-gen2-seg")
	h.src.pass(t)

	cases := []struct {
		name    string
		at      time.Time
		want    []string
		wantNot []string
	}{
		{"newest", time.Time{}, []string{"row-1", "row-gen2", "row-gen2-seg"}, nil},
		{"before gen3", gen3Created.Add(-time.Millisecond), []string{"row-1"}, []string{"row-gen2", "row-gen2-seg"}},
		{"at gen3 start", gen3Created.Add(time.Millisecond), []string{"row-1", "row-gen2"}, []string{"row-gen2-seg"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "restored")
			if _, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{At: tc.at}); err != nil {
				t.Fatalf("Hydrate: %v", err)
			}
			db, err := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			for _, want := range tc.want {
				var n int
				if err := db.QueryRow("SELECT COUNT(*) FROM t WHERE v=?", want).Scan(&n); err != nil || n != 1 {
					t.Fatalf("want %q present: n=%d err=%v", want, n, err)
				}
			}
			for _, not := range tc.wantNot {
				var n int
				if err := db.QueryRow("SELECT COUNT(*) FROM t WHERE v=?", not).Scan(&n); err != nil || n != 0 {
					t.Fatalf("want %q absent: n=%d err=%v", not, n, err)
				}
			}
		})
	}
	_ = gen2Created
}

func TestHydrate_GenerationFlag(t *testing.T) {
	h := newHydrateFixture(t)
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	h.src.now = h.src.now.Add(2 * time.Minute)
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{Generation: 2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Generation != 2 {
		t.Fatalf("Generation = %d, want 2", rep.Generation)
	}
	db, _ := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
	defer db.Close()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM t WHERE v='row-gen2'").Scan(&n)
	if n != 0 {
		t.Fatal("gen-3 row leaked into --generation 2 restore")
	}
	var m int
	db.QueryRow("SELECT COUNT(*) FROM t WHERE v='row-1'").Scan(&m)
	if m != 1 {
		t.Fatal("row-1 missing from --generation 2 restore")
	}
}

func TestHydrate_PITRBeforeFirstGeneration(t *testing.T) {
	h := newHydrateFixture(t)
	dst := filepath.Join(t.TempDir(), "restored")
	_, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{At: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)})
	if err == nil {
		t.Fatal("PITR before first generation must error naming the oldest point")
	}
}
