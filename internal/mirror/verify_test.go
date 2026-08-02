package mirror

import (
	"context"
	"strings"
	"testing"
	"time"
)

// verifyFixture builds a fake bucket whose mirror-state references real
// objects, all hash-consistent.
func verifyFixture(t *testing.T, fake *fakeS3) *State {
	t.Helper()
	sha := sha256HexBytes([]byte("snapshot-bytes"))
	segSha := sha256HexBytes([]byte("seg-1"))
	docSha := sha256HexBytes([]byte("doc-bytes"))
	vecSha := sha256HexBytes([]byte("vec-bytes"))

	st := fixtureState()
	st.DB.SnapshotSHA256 = sha
	st.DB.WAL = []WALSegmentRef{{Key: "ws/db/generation-3/wal/000001.zst", SHA256: segSha, SealedAt: time.Now().UTC()}}
	st.Objects = map[string]ObjectRef{
		"wiki/a.md": {Key: "ws/objects/docs/" + docSha[:2] + "/" + docSha, SHA256: docSha},
	}
	st.Vectors = map[string]ObjectRef{
		"vectors.idx": {Key: "ws/vectors/" + vecSha, SHA256: vecSha},
	}

	fake.objects[st.DB.Snapshot] = []byte("snapshot-bytes")
	fake.objects["ws/db/generation-3/wal/000001.zst"] = []byte("seg-1")
	fake.objects[st.Objects["wiki/a.md"].Key] = []byte("doc-bytes")
	fake.objects[st.Vectors["vectors.idx"].Key] = []byte("vec-bytes")
	return st
}

func openVerifyMirror(t *testing.T, fake *fakeS3, st *State) *Mirror {
	t.Helper()
	sb, _ := MarshalState(st)
	fake.objects[StateKey("ws/")] = sb
	_, cfg := setupFakeMirror(t, fake)
	m, err := Open(t.TempDir(), cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return m
}

func TestVerify_Valid(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.Valid {
		t.Fatalf("report = %+v", rep)
	}
	if rep.Checked != 4 {
		t.Fatalf("Checked = %d, want 4 (snapshot+seg+doc+vec)", rep.Checked)
	}
}

func TestVerify_MissingObjectFails(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	delete(fake.objects, st.Objects["wiki/a.md"].Key)
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("missing referenced object must fail verify")
	}
	found := false
	for _, v := range rep.Violations {
		if strings.Contains(v, st.Objects["wiki/a.md"].Key) {
			found = true
		}
	}
	if !found {
		t.Fatalf("violation should name the missing key: %v", rep.Violations)
	}
}

func TestVerify_CorruptionFails_NamesObject(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	fake.objects[st.DB.Snapshot] = []byte("Xnapshot-bytes") // one byte flipped
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("corrupted object must fail full verify")
	}
	if !strings.Contains(strings.Join(rep.Violations, " "), st.DB.Snapshot) {
		t.Fatalf("violation should name corrupted key: %v", rep.Violations)
	}
}

// TestVerify_FastPassesCorruption: --fast is HEAD-only — existence holds,
// so it deterministically PASSES on a flipped byte (spec §Test spec).
func TestVerify_FastPassesCorruption(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	fake.objects[st.DB.Snapshot] = []byte("X")
	m := openVerifyMirror(t, fake, st)
	rep, err := m.VerifyFast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Valid {
		t.Fatalf("--fast must pass on existence-only: %+v", rep)
	}
}

func TestVerify_OrphanAdvisory(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	fake.objects["ws/objects/docs/zz/"+strings.Repeat("zz", 32)] = []byte("orphan")
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Valid {
		t.Fatal("orphans are advisory, never violations")
	}
	if len(rep.Advisories) == 0 {
		t.Fatal("orphan should be listed as advisory")
	}
}

func TestVerify_RotatedGenerationChecked(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	// Add a rotated generation 2 with meta.json + snapshot.
	sha := sha256HexBytes([]byte("gen2-snap"))
	meta := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 2,
		CreatedAt: time.Now().UTC(), SealedAt: time.Now().UTC(),
		Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha,
		WAL: []WALSegmentRef{},
	}
	mb, _ := MarshalMeta(meta)
	fake.objects[GenerationMetaKey("ws/", 2)] = mb
	fake.objects["ws/db/generation-2/snapshot.db.zst"] = []byte("gen2-snap")

	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Valid {
		t.Fatalf("valid rotated gen should pass: %+v", rep)
	}
	if rep.Checked != 5 {
		t.Fatalf("Checked = %d, want 5 (4 live + 1 rotated snapshot)", rep.Checked)
	}

	// Corrupt the rotated snapshot → violation.
	fake.objects["ws/db/generation-2/snapshot.db.zst"] = []byte("WRONG")
	rep, _ = m.Verify(context.Background())
	if rep.Valid {
		t.Fatal("corrupt rotated-generation object must fail verify")
	}
}

func TestVerify_RotatedMetaMissing(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	// Generation dir exists with a snapshot but NO meta.json.
	fake.objects["ws/db/generation-2/snapshot.db.zst"] = []byte("x")
	m := openVerifyMirror(t, fake, st)
	rep, _ := m.Verify(context.Background())
	if rep.Valid {
		t.Fatal("missing meta.json for rotated generation must fail")
	}
}

func TestVerify_NoState(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	m, _ := Open(t.TempDir(), cfg, nil)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("no mirror-state → invalid")
	}
}
