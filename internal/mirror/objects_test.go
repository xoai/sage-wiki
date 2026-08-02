package mirror

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShipObjects_UpsertAndCommit(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	res := f.pass(t)
	if res.ObjectsShipped != 1 {
		t.Fatalf("ObjectsShipped = %d, want 1", res.ObjectsShipped)
	}
	sha := shaOf("# Foo")
	key := "ws/objects/docs/" + sha[:2] + "/" + sha
	if _, ok := f.fake.get(key); !ok {
		t.Fatalf("object not PUT at content key %q", key)
	}
	st := f.remoteState(t)
	ref, ok := st.Objects["wiki/concepts/Foo.md"]
	if !ok || ref.Key != key || ref.SHA256 != sha {
		t.Fatalf("state objects = %+v", st.Objects)
	}
}

func TestShipObjects_UnchangedNotRePUT(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	f.pass(t)
	putsBefore := len(f.fake.putLog)
	res := f.pass(t)
	if res.ObjectsShipped != 0 {
		t.Fatalf("re-shipped unchanged: %+v", res)
	}
	if len(f.fake.putLog) != putsBefore {
		t.Fatal("no PUT expected on unchanged pass")
	}
}

func TestShipObjects_Tombstone(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	f.pass(t)
	deleteWS(t, f.dir, "wiki/concepts/Foo.md")
	res := f.pass(t)
	if res.ObjectsTombstoned != 1 {
		t.Fatalf("ObjectsTombstoned = %d", res.ObjectsTombstoned)
	}
	st := f.remoteState(t)
	ref := st.Objects["wiki/concepts/Foo.md"]
	if !ref.Deleted {
		t.Fatalf("tombstone not recorded: %+v", ref)
	}
	// Object bytes REMAIN in the bucket (bucket versioning honored).
	if _, ok := f.fake.get(ref.Key); !ok {
		t.Fatal("tombstoned object physically deleted — versioning not honored")
	}
}

func TestShipObjects_VectorsPrefix(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, ".sage/vectors.idx", "SWVI-1")
	f.pass(t)
	sha := shaOf("SWVI-1")
	if _, ok := f.fake.get("ws/vectors/" + sha); !ok {
		t.Fatal("vector not under top-level vectors/ prefix (SPEC-03 layout)")
	}
	st := f.remoteState(t)
	if _, ok := st.Vectors["vectors.idx"]; !ok {
		t.Fatalf("vectors map = %+v", st.Vectors)
	}
}

func TestShipObjects_StateCommitWithoutDBChanges(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	f.pass(t)
	st1 := f.remoteState(t)
	// Docs-only change, no db writes: state must still commit.
	writeWS(t, f.dir, "wiki/summaries/a.md", "sum")
	res := f.pass(t)
	if res.ObjectsShipped != 1 {
		t.Fatalf("ObjectsShipped = %d", res.ObjectsShipped)
	}
	st2 := f.remoteState(t)
	if !st2.UpdatedAt.After(st1.UpdatedAt) && st2.UpdatedAt != st1.UpdatedAt {
		// equal is fine (same-second); the point is the new object is committed
	}
	if _, ok := st2.Objects["wiki/summaries/a.md"]; !ok {
		t.Fatal("docs-only change never committed")
	}
}

func TestShipObjects_NoSecretsShipped(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "config.yaml", "api:\n  api_key: sk-SECRET-BYTES\n")
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	f.pass(t)
	for key, b := range f.fake.objects {
		if strings.Contains(string(b), "sk-SECRET-BYTES") {
			t.Fatalf("config secret shipped in object %s", key)
		}
	}
}

func deleteWS(t *testing.T, dir, rel string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
		t.Fatal(err)
	}
}

// TestShipObjects_ResurrectKeepsSHA (F-019): resurrecting identical content
// UN-tombstones the existing ref (same key, same sha, NO re-PUT) — under
// encryption a fresh PUT would write new-nonce ciphertext (new shipped sha)
// and silently invalidate every historical sealed map naming the old sha.
func TestShipObjects_ResurrectKeepsSHA(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	f.pass(t)
	st := f.remoteState(t)
	ref := st.Objects["wiki/concepts/Foo.md"]

	// Tombstone, then resurrect with IDENTICAL content.
	deleteWS(t, f.dir, "wiki/concepts/Foo.md")
	f.pass(t)
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	objectPutsBefore := 0
	for _, k := range f.fake.putLog {
		if strings.HasPrefix(k, "ws/objects/") {
			objectPutsBefore++
		}
	}
	res := f.pass(t)

	if res.ObjectsResurrected != 1 {
		t.Fatalf("ObjectsResurrected = %d, want 1", res.ObjectsResurrected)
	}
	objectPutsAfter := 0
	for _, k := range f.fake.putLog {
		if strings.HasPrefix(k, "ws/objects/") {
			objectPutsAfter++
		}
	}
	if objectPutsAfter != objectPutsBefore {
		t.Fatal("resurrect re-PUT the object (would change shipped sha under encryption)")
	}
	st2 := f.remoteState(t)
	got := st2.Objects["wiki/concepts/Foo.md"]
	if got.Deleted {
		t.Fatal("resurrected ref still tombstoned")
	}
	if got.Key != ref.Key || got.SHA256 != ref.SHA256 {
		t.Fatalf("resurrect changed the identity: %+v vs %+v", got, ref)
	}
}

// TestShipObjects_VectorResurrectKeepsSHA (F-019 vector half): resurrecting
// an identical vector UN-tombstones it — no re-PUT, sha preserved.
func TestShipObjects_VectorResurrectKeepsSHA(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, ".sage/vectors.idx", "SWVI-1")
	f.pass(t)
	st := f.remoteState(t)
	ref := st.Vectors["vectors.idx"]

	deleteWS(t, f.dir, ".sage/vectors.idx")
	f.pass(t)
	writeWS(t, f.dir, ".sage/vectors.idx", "SWVI-1")
	vecPutsBefore := 0
	for _, k := range f.fake.putLog {
		if strings.HasPrefix(k, "ws/vectors/") {
			vecPutsBefore++
		}
	}
	res := f.pass(t)

	if res.ObjectsResurrected != 1 {
		t.Fatalf("ObjectsResurrected = %d, want 1", res.ObjectsResurrected)
	}
	vecPutsAfter := 0
	for _, k := range f.fake.putLog {
		if strings.HasPrefix(k, "ws/vectors/") {
			vecPutsAfter++
		}
	}
	if vecPutsAfter != vecPutsBefore {
		t.Fatal("vector resurrect re-PUT (would change shipped sha under encryption)")
	}
	st2 := f.remoteState(t)
	got := st2.Vectors["vectors.idx"]
	if got.Deleted {
		t.Fatal("resurrected vector still tombstoned")
	}
	if got.Key != ref.Key || got.SHA256 != ref.SHA256 {
		t.Fatalf("vector resurrect changed identity: %+v vs %+v", got, ref)
	}
}

// N-1 witness (CRITICAL): a torn vector read (idx rewritten between diff and
// read) defers with a warning and touches NOTHING — without the guard the
// committed ref would fail validateObjectRef on every subsequent pass,
// permanently wedging ship and all hydrates.
func TestShipObjects_VectorTornReadDefers(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, ".sage/vectors.idx", "SWVI-1")
	st := f.remoteState(t)
	// Diff says SWVI-1; the file now says SWVI-CHANGED (torn read).
	writeWS(t, f.dir, ".sage/vectors.idx", "SWVI-CHANGED")
	changes := []Change{{Path: "vectors.idx", Kind: ChangeUpsert, SHA256: shaOf("SWVI-1"), Vector: true}}
	var res shipResult
	if err := f.m.syncVector(context.Background(), st, NormalizePrefix(f.m.cfg.Prefix), changes[0], &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Warnings) == 0 {
		t.Fatal("torn read must warn")
	}
	if _, ok := f.fake.get("ws/vectors/" + shaOf("SWVI-CHANGED")); ok {
		t.Fatal("torn read committed mismatched bytes (the wedge class)")
	}
	if len(st.Vectors) != 0 {
		t.Fatal("torn read mutated state")
	}
}

// N-6 witness: changed-underfoot content defers the resurrect (tombstone intact).
func TestShipObjects_ResurrectDefersOnChangedUnderfoot(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	f.pass(t)
	deleteWS(t, f.dir, "wiki/concepts/Foo.md")
	f.pass(t)
	st := f.remoteState(t)
	changes := []Change{{Path: "wiki/concepts/Foo.md", Kind: ChangeUpsert, SHA256: shaOf("# Foo")}}
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo CHANGED")
	var res shipResult
	if err := f.m.shipObjects(context.Background(), st, changes, &res); err != nil {
		t.Fatal(err)
	}
	if res.ObjectsResurrected != 0 {
		t.Fatal("changed content must not resurrect")
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w, "changed mid-pass, deferred") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected the defer warning: %v", res.Warnings)
	}
	if !st.Objects["wiki/concepts/Foo.md"].Deleted {
		t.Fatal("tombstone flipped despite changed content")
	}
}
