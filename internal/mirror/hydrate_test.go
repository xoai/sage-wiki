package mirror

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
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
	defer h.src.dbClose() // Windows: every dbWrite must Close before TempDir cleanup
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

// assertRowPresent checks a row exists in the restored workspace's db.
func assertRowPresent(t *testing.T, dst, row string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM t WHERE v=?", row).Scan(&n); err != nil || n != 1 {
		t.Fatalf("row %q: n=%d err=%v", row, n, err)
	}
}

// TestHydrate_PathTraversalRejected (F-084): a poisoned mirror-state must
// never write outside the restore dir.
func TestHydrate_PathTraversalRejected(t *testing.T) {
	h := newHydrateFixture(t)
	st := h.src.remoteState(t)
	st.Objects["../escape.md"] = ObjectRef{
		Key:    "ws/objects/docs/ab/" + sha256HexBytes([]byte("escape")),
		SHA256: sha256HexBytes([]byte("escape")),
	}
	sb, _ := MarshalState(st)
	h.fake.objects[StateKey("ws/")] = sb
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{}); err == nil {
		t.Fatal("path traversal must be rejected loudly")
	}
	if _, err := os.Stat(filepath.Join(dst, "..", "escape.md")); !os.IsNotExist(err) {
		t.Fatal("traversal wrote outside dst")
	}
}

// TestHydrate_PartialResumeSelectionMismatch (F-107 witness): resuming a
// --partial restore with a different selection is refused.
func TestHydrate_PartialResumeSelectionMismatch(t *testing.T) {
	h := newHydrateFixture(t)
	dst := filepath.Join(t.TempDir(), "restored")
	h.cfg.Encryption = EncryptionConfig{}
	// First partial run: force an abort after db by corrupting one doc.
	st := h.src.remoteState(t)
	var docKey string
	for _, ref := range st.Objects {
		docKey = ref.Key
		break
	}
	orig := h.fake.objects[docKey]
	h.fake.objects[docKey] = []byte("CORRUPTED")
	_, cfg := setupFakeMirror(t, h.fake)
	if _, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{Partial: true}); err == nil {
		t.Fatal("setup: corrupted doc should abort")
	}
	h.fake.objects[docKey] = orig
	// Resume with a DIFFERENT selection (--generation 1 vs newest-at-first-run).
	// Force a rotation so newest changed since the first run.
	h.src.dbWrite(t, "row-x")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	if _, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{Partial: true, Generation: 1}); err == nil {
		t.Fatal("resume with a different selection must be refused")
	}
}

// AC-1 (PITR objects): --generation restores the PRE-rotation doc set;
// newest restores the NEW set — proving the object selection is in force.
func TestHydrate_GenerationRestoresSealedObjectMap(t *testing.T) {
	h := newHydrateFixture(t)
	// Gen 1 has Foo. Rotate to gen 2, then MUTATE the doc set.
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	writeWS(t, h.src.dir, "wiki/concepts/New.md", "new doc after seal")
	writeWS(t, h.src.dir, "wiki/concepts/Foo.md", "# Foo CHANGED")
	h.src.pass(t)

	// Newest: sees the NEW set.
	dstNew := filepath.Join(t.TempDir(), "new")
	if _, err := Hydrate(context.Background(), h.cfg, dstNew, HydrateOpts{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dstNew, "wiki/concepts/New.md")); err != nil {
		t.Fatal("newest restore must see the new doc")
	}
	b, _ := os.ReadFile(filepath.Join(dstNew, "wiki/concepts/Foo.md"))
	if string(b) != "# Foo CHANGED" {
		t.Fatal("newest restore must see the changed doc")
	}

	// --generation 2 (the retained rotated gen): sees the SEALED map.
	dstOld := filepath.Join(t.TempDir(), "old")
	rep, err := Hydrate(context.Background(), h.cfg, dstOld, HydrateOpts{Generation: 2})
	if err != nil {
		t.Fatalf("hydrate --generation 2: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dstOld, "wiki/concepts/New.md")); !os.IsNotExist(err) {
		t.Fatal("--generation 2 must NOT see the post-seal doc")
	}
	b, _ = os.ReadFile(filepath.Join(dstOld, "wiki/concepts/Foo.md"))
	if string(b) != "# Foo" {
		t.Fatalf("--generation 2 must see the SEALED Foo content, got %q", b)
	}
	_ = rep
}

// AC-2: --at into a rotated generation: docs created in a LATER generation
// are absent; overshoot names both skews.
func TestHydrate_PITR_RotatedObjectsAndDualSkew(t *testing.T) {
	h := newHydrateFixture(t)
	gen2Created := h.src.remoteState(t).DB.CreatedAt
	// Seal a segment in gen 2's lifetime at T0+1s (opens a created→sealed window).
	h.src.now = gen2Created.Add(time.Second)
	h.src.dbWrite(t, "row-gen2-seg")
	h.src.pass(t)
	// Rotate to gen 3 at T0+2s (meta gen 2: created=T0, sealed=T0+1s).
	h.src.now = gen2Created.Add(2 * time.Second)
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	writeWS(t, h.src.dir, "wiki/concepts/Later.md", "later")
	h.src.pass(t)

	// --at T0+0.5s: inside gen 2's window → segment overshoot (the T0+1s
	// segment) AND object skew (docs at gen 2's seal, newer than T).
	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{At: gen2Created.Add(500 * time.Millisecond)})
	if err != nil {
		t.Fatalf("hydrate --at: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "wiki/concepts/Later.md")); !os.IsNotExist(err) {
		t.Fatal("later-generation doc must be absent in PITR restore")
	}
	if !strings.Contains(rep.Overshoot, "objects at generation 2's seal") {
		t.Fatalf("overshoot must name the object skew exactly (pre-feature 'sealed' must not satisfy): %q", rep.Overshoot)
	}
	if !strings.Contains(rep.Overshoot, "segment(s)") {
		t.Fatalf("overshoot must name excluded segments: %q", rep.Overshoot)
	}
	// row-gen2-seg (sealed after T) must NOT be in the restored db.
	db, err := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM t WHERE v='row-gen2-seg'").Scan(&n)
	if n != 0 {
		t.Fatal("post-T segment replayed into the restored db")
	}
}

// AC-3: old mirror (meta without maps) → fallback to live maps + canonical advisory.
func TestHydrate_OldMirrorFallbackWarning(t *testing.T) {
	h := newHydrateFixture(t) // gen 1 already rotated (meta exists)
	// Rewrite gen-1's meta WITHOUT maps but with its REAL snapshot (a
	// pre-feature mirror — meta written before the objects field existed).
	mb0, ok := h.fake.get(GenerationMetaKey("ws/", 1))
	if !ok {
		t.Fatal("setup: gen-1 meta missing")
	}
	meta0, err := UnmarshalMeta(mb0)
	if err != nil {
		t.Fatal(err)
	}
	meta0.Objects = nil
	meta0.Vectors = nil
	mb, _ := MarshalMeta(meta0)
	h.fake.objects[GenerationMetaKey("ws/", 1)] = mb

	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{Generation: 1})
	if err != nil {
		t.Fatalf("hydrate on old mirror: %v", err)
	}
	found := false
	for _, a := range rep.Advisories {
		if strings.Contains(a, "note: generation has no object map; docs restored at newest") {
			found = true
		}
	}
	if !found {
		t.Fatalf("canonical fallback advisory missing: %v", rep.Advisories)
	}
	if _, err := os.Stat(filepath.Join(dst, "wiki/concepts/Foo.md")); err != nil {
		t.Fatal("fallback must restore docs from live state")
	}
}

// AC-4: tombstone in the sealed map stays absent; a file deleted in a LATER
// generation's lifetime IS restored (the per-generation bound).
// F-020 (empty-seal witness): a generation sealed with an EMPTY doc set
// (objects: {} vectors: {}) restores ZERO docs and does NOT fire the
// fallback advisory — a len()==0 or omitempty regression would silently
// fall back to live docs.
func TestHydrate_EmptySealedSetRestoresEmpty(t *testing.T) {
	h := newHydrateFixture(t)
	// Write a stripped-to-{} meta for gen 1 (present, empty).
	mb0, ok := h.fake.get(GenerationMetaKey("ws/", 1))
	if !ok {
		t.Fatal("setup: gen-1 meta missing")
	}
	meta0, err := UnmarshalMeta(mb0)
	if err != nil {
		t.Fatal(err)
	}
	meta0.Objects = map[string]ObjectRef{}
	meta0.Vectors = map[string]ObjectRef{}
	mb, _ := MarshalMeta(meta0)
	h.fake.objects[GenerationMetaKey("ws/", 1)] = mb

	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{Generation: 1})
	if err != nil {
		t.Fatalf("hydrate --generation 1: %v", err)
	}
	for _, a := range rep.Advisories {
		if strings.Contains(a, "no object map") {
			t.Fatalf("empty-seal must NOT fire the fallback advisory: %v", rep.Advisories)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dst, "wiki", "concepts"))
	if err == nil && len(entries) > 0 {
		t.Fatalf("empty sealed set must restore zero docs, found %d", len(entries))
	}
}

// AC-4 second half: a file deleted in a LATER generation's lifetime IS
// restored from the pre-delete generation's sealed map.
func TestHydrate_LaterDeleteRestoredFromEarlierGen(t *testing.T) {
	h := newHydrateFixture(t) // gen 2 live; Foo shipped in gen 1's map
	// Rotate to gen 3 (gen 2's map = current, Foo present).
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	// Delete Foo in gen 3's lifetime (live map tombstones it).
	deleteWS(t, h.src.dir, "wiki/concepts/Foo.md")
	h.src.pass(t)

	// --generation 2 (sealed BEFORE the delete): Foo must come back with
	// pre-delete content. Pre-feature code (live tombstoned map) fails this.
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{Generation: 2}); err != nil {
		t.Fatalf("hydrate --generation 2: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "wiki/concepts/Foo.md"))
	if err != nil {
		t.Fatalf("file deleted in a LATER generation must be restored from the pre-delete gen: %v", err)
	}
	if string(b) != "# Foo" {
		t.Fatalf("restored content = %q, want pre-delete %q", b, "# Foo")
	}
}

func TestHydrate_MetaTombstoneAndLaterDeleteBound(t *testing.T) {
	h := newHydrateFixture(t)
	deleteWS(t, h.src.dir, "wiki/concepts/Foo.md")
	h.src.pass(t) // tombstone committed (Foo deleted in gen 1's lifetime)
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	// Gen 1's sealed map carries Foo as a TOMBSTONE.
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{Generation: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "wiki/concepts/Foo.md")); !os.IsNotExist(err) {
		t.Fatal("tombstoned file in the sealed map must stay absent")
	}
}

// Pass-4 item 1: per-class fallback advisory on the --at path too (mixed
// meta: objects present, vectors nil → "no vector map" advisory).
func TestHydrate_AtPathPerClassFallbackAdvisory(t *testing.T) {
	h := newHydrateFixture(t)
	h.src.m.cfg.RetainGenerations = 4 // keep older gens for this window test
	gen2Created := h.src.remoteState(t).DB.CreatedAt
	// Rotation 1 at T0+2s: gen 2 -> 3.
	h.src.now = gen2Created.Add(2 * time.Second)
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	// Ship a vector in gen 3's lifetime at T0+3s (live vector becomes SWVI-2).
	h.src.now = gen2Created.Add(3 * time.Second)
	writeWS(t, h.src.dir, ".sage/vectors.idx", "SWVI-2")
	if _, err := h.src.m.shipPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Rotation 2 at T0+4s: gen 3 -> 4; meta gen-3 sealed at T0+3s.
	h.src.now = gen2Created.Add(4 * time.Second)
	h.src.dbWrite(t, "row-gen3")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: second rotation did not fire")
	}
	// Mixed meta AFTER the last rotation (prune can't undo it): gen 3 with
	// objects present, vectors nil.
	mb0, ok := h.fake.get(GenerationMetaKey("ws/", 3))
	if !ok {
		t.Fatal("setup: gen-3 meta missing")
	}
	meta0, err := UnmarshalMeta(mb0)
	if err != nil {
		t.Fatal(err)
	}
	meta0.Vectors = nil
	mb, _ := MarshalMeta(meta0)
	h.fake.objects[GenerationMetaKey("ws/", 3)] = mb

	// T = T0+2.5s: inside gen 3 (created T0+2s), before its seal (T0+3s).
	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{At: gen2Created.Add(2500 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, a := range rep.Advisories {
		if strings.Contains(a, "note: generation has no vector map; vectors restored at newest") {
			found = true
		}
	}
	if !found {
		t.Fatalf("--at mixed meta must surface the exact per-class advisory: %v", rep.Advisories)
	}
	// Content assertions (N-4): docs came from gen-3's sealed map, vectors
	// from LIVE state — a drift that swaps sources fails here.
	if _, err := os.Stat(filepath.Join(dst, "wiki", "concepts", "Foo.md")); err != nil {
		t.Fatal("sealed-map doc not restored")
	}
	if _, err := os.Stat(filepath.Join(dst, ".sage", "vectors.idx")); err == nil {
		// Live-state vector restore: content must match the LIVE vector object.
		liveVec := h.src.remoteState(t).Vectors["vectors.idx"]
		b, _ := os.ReadFile(filepath.Join(dst, ".sage", "vectors.idx"))
		if shaOf(string(b)) != liveVec.SHA256 {
			t.Fatalf("vectors restored from wrong source (want live %s)", liveVec.SHA256[:12])
		}
	}
}

// Pass-4 item 2: resurrect guard DEFERS on ReadFile error (vanished mid-pass).
func TestShipObjects_ResurrectDefersOnVanish(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	f.pass(t)
	deleteWS(t, f.dir, "wiki/concepts/Foo.md")
	f.pass(t) // tombstoned
	// Resurrect in the diff set, then VANISH before the guard reads.
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	st := f.remoteState(t)
	changes := []Change{{Path: "wiki/concepts/Foo.md", Kind: ChangeUpsert, SHA256: shaOf("# Foo")}}
	deleteWS(t, f.dir, "wiki/concepts/Foo.md")
	var res shipResult
	if err := f.m.shipObjects(context.Background(), st, changes, &res); err != nil {
		t.Fatal(err)
	}
	if res.ObjectsResurrected != 0 {
		t.Fatalf("vanished file must NOT resurrect: %d", res.ObjectsResurrected)
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "vanished mid-pass") {
			found = true
		}
	}
	if !found {
		t.Fatalf("vanish must warn with the vanish string (not the change string): %v", res.Warnings)
	}
	if !st.Objects["wiki/concepts/Foo.md"].Deleted {
		t.Fatal("vanished file un-tombstoned (should stay tombstoned this pass)")
	}
}

// Pass-4 item 3: rep.Checked counts BOTH tuples of a shared key (deterministic
// slice-vs-map distinguisher — no probabilistic double-run needed).
func TestVerify_MetaRefsCheckedCountsBothTuples(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2)
	shaA := sha256HexBytes([]byte("doc-A"))
	shaB := sha256HexBytes([]byte("doc-B"))
	key := "ws/objects/docs/" + shaA[:2] + "/" + shaA
	mk := func(gen int, sha string, created time.Time) {
		meta := &GenerationMeta{
			FormatVersion: FormatVersion, Generation: gen,
			CreatedAt: created, SealedAt: created,
			Snapshot: "ws/db/generation-" + strconv.Itoa(gen) + "/snapshot.db.zst", SnapshotSHA256: sha256HexBytes([]byte("gen-snap")),
			WAL:     []WALSegmentRef{},
			Objects: map[string]ObjectRef{"wiki/a.md": {Key: key, SHA256: sha, ContentSHA256: shaA}},
		}
		mb, _ := MarshalMeta(meta)
		fake.objects[GenerationMetaKey("ws/", gen)] = mb
		fake.objects["ws/db/generation-"+strconv.Itoa(gen)+"/snapshot.db.zst"] = []byte("gen-snap")
	}
	mk(1, shaB, time.Now().Add(-time.Hour).UTC())
	mk(2, shaA, time.Now().UTC())
	fake.objects[key] = []byte("doc-A")

	m := openVerifyMirror(t, fake, st)
	m.cfg.RetainGenerations = 5
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Base fixture refs (4) + 2 generation snapshots + 2 tuples for the
	// shared key.
	if rep.Checked != 8 {
		t.Fatalf("Checked = %d, want 8 (both tuples of the shared key verified)", rep.Checked)
	}
}

// F-022 MAJOR witness (live-map advance): newest selection, live map
// moves between --partial runs → resume refused loudly (fingerprint:
// snapshot key + updated_at), not a silently mixed restore.
func TestHydrate_PartialResumeSelectionDrift_LiveMapAdvance(t *testing.T) {
	h := newHydrateFixture(t)
	dst := filepath.Join(t.TempDir(), "restored")
	// Corrupt a DOC object so the run aborts in the MARKDOWN phase (after
	// db completed and the selection was recorded).
	st := h.src.remoteState(t)
	var docKey string
	for _, ref := range st.Objects {
		docKey = ref.Key
		break
	}
	orig := h.fake.objects[docKey]
	h.fake.objects[docKey] = []byte("CORRUPTED")
	if _, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{Partial: true}); err == nil {
		t.Fatal("setup: corrupted doc should abort in markdown phase")
	}
	// The progress file exists with the selection recorded.
	if _, err := os.Stat(filepath.Join(dst, ".sage", "hydrate-state.json")); err != nil {
		t.Fatalf("setup: progress marker missing: %v", err)
	}
	// Advance the live map between runs (new doc + fix the corruption) —
	// on an ADVANCED clock so the state's updated_at actually moves.
	writeWS(t, h.src.dir, "wiki/concepts/Between.md", "between")
	h.fake.objects[docKey] = orig
	h.src.now = h.src.now.Add(time.Minute)
	if _, err := h.src.m.shipPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Resume: the fingerprint must REFUSE loudly, naming the drift.
	_, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{Partial: true})
	if err == nil {
		t.Fatal("resume after live-map advance must be refused")
	}
	if !strings.Contains(err.Error(), "drift") {
		t.Fatalf("refusal must name the drift: %v", err)
	}
}

// F-024 witness: vecs-only mixed meta — docs fall back to live (advisory
// names it) while vectors restore from the seal; the vector skew is named.
func TestHydrate_VecsOnlyMixedMeta_PairingAndSkew(t *testing.T) {
	h := newHydrateFixture(t)
	h.src.m.cfg.RetainGenerations = 4 // keep older gens for this skew-window test
	gen2Created := h.src.remoteState(t).DB.CreatedAt
	// Rotation 1 at T0+2s: gen 2 -> 3.
	h.src.now = gen2Created.Add(2 * time.Second)
	h.src.dbWrite(t, "row-gen2")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	// Ship a vector in gen 3's lifetime at T0+3s (opens gen 3's skew window:
	// created T0+2s, SealedAt = T0+3s).
	h.src.now = gen2Created.Add(3 * time.Second)
	writeWS(t, h.src.dir, ".sage/vectors.idx", "SWVI-2")
	if _, err := h.src.m.shipPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Rotation 2 at T0+4s: gen 3 -> 4; meta gen-3 sealed at T0+3s.
	h.src.now = gen2Created.Add(4 * time.Second)
	h.src.dbWrite(t, "row-gen3")
	h.src.dbClose()
	ageLocalRotationFile(t, h.src.dir, -2*time.Hour)
	if res := h.src.pass(t); !res.Rotated {
		t.Fatal("setup: second rotation did not fire")
	}
	// Mixed meta AFTER the last rotation (prune can't undo it): gen 3 with
	// objects nil, vectors present.
	mb0, ok := h.fake.get(GenerationMetaKey("ws/", 3))
	if !ok {
		t.Fatal("setup: gen-3 meta missing")
	}
	meta0, err := UnmarshalMeta(mb0)
	if err != nil {
		t.Fatal(err)
	}
	meta0.Objects = nil
	mb, _ := MarshalMeta(meta0)
	h.fake.objects[GenerationMetaKey("ws/", 3)] = mb

	// T = T0+2.5s: inside gen 3 (created T0+2s), before its seal (T0+3s).
	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{At: gen2Created.Add(2500 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	foundDocs, foundVecs := false, false
	for _, a := range rep.Advisories {
		if strings.Contains(a, "no object map") {
			foundDocs = true
		}
	}
	if strings.Contains(rep.Overshoot, "vectors at generation 3's seal") {
		foundVecs = true
	}
	if !foundDocs {
		t.Fatalf("docs fallback advisory missing: %v", rep.Advisories)
	}
	if !foundVecs {
		t.Fatalf("vector skew not named in overshoot: %q", rep.Overshoot)
	}
	// Vectors came from the seal (gen-3 meta map), docs from live.
	if _, err := os.Stat(filepath.Join(dst, ".sage", "vectors.idx")); err != nil {
		t.Fatal("sealed vector not restored from the meta map")
	}
}
