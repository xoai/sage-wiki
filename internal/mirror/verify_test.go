package mirror

import (
	"context"
	"path/filepath"
	"strconv"
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

// retain-in-state witnesses (follow-up item 3): verify prefers the STATE's
// retain_generations over the verifier's local config.

func TestVerify_RetainFromState_LargerWins(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	// Add rotated gens 1 and 2 with metas; live is 3. State retain = 3 →
	// gen 1 retained (3-1=2 > 3-3=0... precisely: gen > live-retain = 0).
	addRotatedGen(t, fake, 1)
	addRotatedGen(t, fake, 2)
	// gen-1's meta deleted: a VIOLATION iff the state's retain=3 is in
	// force (gen 1 retained). Under the local cfg=1 both gens are exempt,
	// so the old intact version passed identically either way — vacuous
	// (independent review issue 2). This version distinguishes.
	delete(fake.objects, GenerationMetaKey("ws/", 1))
	st.RetainGenerations = 3
	m := openVerifyMirror(t, fake, st)
	m.cfg.RetainGenerations = 1 // local would exempt everything — state must win
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("state retain=3 must retain (and flag) gen 1 — local cfg did NOT win")
	}
}

func TestVerify_RetainFromState_SmallerWins(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2) // meta present; live=3; state retain=1 → gen 2 prune-eligible (exempt)
	// Remove gen-2's meta to make the point: with retain=1 gen 2 is exempt
	// from the invariant even with a MISSING meta; with local cfg=5 it
	// would be retained and flagged.
	delete(fake.objects, GenerationMetaKey("ws/", 2))
	st.RetainGenerations = 1
	m := openVerifyMirror(t, fake, st)
	m.cfg.RetainGenerations = 5
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Valid {
		t.Fatalf("state retain=1 must exempt gen 2 (local cfg does NOT resurrect): %+v", rep.Violations)
	}
}

func TestVerify_RetainFallback_LocalConfig(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2) // live=3; local retain=2 → gen 2 retained → checked
	delete(fake.objects, "ws/db/generation-2/snapshot.db.zst")
	// st.RetainGenerations stays 0 (absent) → fallback to local (2).
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("missing gen-2 snapshot within local retain must fail")
	}
}

func TestVerify_RetainFallback_LocalUnsetDefaultsTo2(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2) // live=3
	// gen-2's snapshot deleted: a VIOLATION at retain=2 (retained), exempt
	// only at retain=0 — this witness distinguishes 0 from the normalized
	// default 2 (F-026: the old version passed identically under both).
	delete(fake.objects, "ws/db/generation-2/snapshot.db.zst")
	// local config UNSET at Open (normalized to 2 by Open's normalize).
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("missing gen-2 snapshot must violate at the normalized default retain=2")
	}
}

// addRotatedGen plants a valid rotated generation (meta + snapshot).
func addRotatedGen(t *testing.T, fake *fakeS3, gen int) {
	t.Helper()
	sha := sha256HexBytes([]byte("gen-snap"))
	snapKey := SnapshotKey("ws/", gen)
	meta := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: gen,
		CreatedAt: time.Now().UTC(), SealedAt: time.Now().UTC(),
		Snapshot: snapKey, SnapshotSHA256: sha,
		WAL: []WALSegmentRef{},
	}
	mb, _ := MarshalMeta(meta)
	fake.objects[GenerationMetaKey("ws/", gen)] = mb
	fake.objects[snapKey] = []byte("gen-snap")
}

// TestVerify_NegativeStateRetain_FallsBack (F-023 witness): a negative
// retain in mirror-state is treated as ABSENT — the rotated-generation
// checks still run under local fallback, never silently disabled.
func TestVerify_NegativeStateRetain_FallsBack(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2)
	delete(fake.objects, "ws/db/generation-2/snapshot.db.zst") // violation at retain=2
	st.RetainGenerations = -1
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("negative state retain must NOT disable rotated checks (fallback applies)")
	}
}

// AC-5 (invariant c): a retained generation's meta-map object that is
// missing or corrupted is a violation naming generation + key; --fast
// passes on a flipped byte (existence holds).
func TestVerify_MetaMapCorruptionFails(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2)
	// Give gen 2 a meta MAP with one object.
	sha := sha256HexBytes([]byte("doc"))
	meta := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 2,
		CreatedAt: time.Now().UTC(), SealedAt: time.Now().UTC(),
		Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha256HexBytes([]byte("gen-snap")),
		WAL: []WALSegmentRef{},
		Objects: map[string]ObjectRef{
			"wiki/a.md": {Key: "ws/objects/docs/" + sha[:2] + "/" + sha, SHA256: sha},
		},
	}
	mb, _ := MarshalMeta(meta)
	fake.objects[GenerationMetaKey("ws/", 2)] = mb
	fake.objects["ws/objects/docs/"+sha[:2]+"/"+sha] = []byte("doc")

	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Valid {
		t.Fatalf("intact meta map must pass: %+v", rep.Violations)
	}

	// Flip one byte in the meta-mapped object.
	fake.objects["ws/objects/docs/"+sha[:2]+"/"+sha] = []byte("Xoc")
	rep, err = m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("corrupted meta-map object must fail full verify")
	}
	found := false
	for _, v := range rep.Violations {
		if strings.Contains(v, "generation 2") && strings.Contains(v, "wiki/a.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("violation must name generation + key: %v", rep.Violations)
	}
	// --fast is existence-only → passes.
	fast, err := m.VerifyFast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !fast.Valid {
		t.Fatalf("--fast must pass on flipped byte (existence holds): %+v", fast.Violations)
	}
}

func TestVerify_MetaMapTombstoneSkipped(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2)
	sha := sha256HexBytes([]byte("doc"))
	meta := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 2,
		CreatedAt: time.Now().UTC(), SealedAt: time.Now().UTC(),
		Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha256HexBytes([]byte("gen-snap")),
		WAL: []WALSegmentRef{},
		Objects: map[string]ObjectRef{
			"wiki/gone.md": {Key: "ws/objects/docs/" + sha[:2] + "/" + sha, SHA256: sha, Deleted: true},
		},
	}
	mb, _ := MarshalMeta(meta)
	fake.objects[GenerationMetaKey("ws/", 2)] = mb
	// NO object present — tombstone must not require one.
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Valid {
		t.Fatalf("tombstone in meta map must not violate: %+v", rep.Violations)
	}
}

// F-013 witness: an object referenced ONLY by a retained generation's
// sealed map is NOT an orphan (it is a format member); a genuinely
// unreferenced object IS still flagged.
func TestVerify_MetaOnlyObjectNotOrphaned(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2)
	sha := sha256HexBytes([]byte("doc"))
	meta := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 2,
		CreatedAt: time.Now().UTC(), SealedAt: time.Now().UTC(),
		Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha256HexBytes([]byte("gen-snap")),
		WAL: []WALSegmentRef{},
		Objects: map[string]ObjectRef{
			"wiki/meta-only.md": {Key: "ws/objects/docs/" + sha[:2] + "/" + sha, SHA256: sha},
		},
	}
	mb, _ := MarshalMeta(meta)
	fake.objects[GenerationMetaKey("ws/", 2)] = mb
	// The meta-only object exists in the bucket.
	fake.objects["ws/objects/docs/"+sha[:2]+"/"+sha] = []byte("doc")
	// Plus a genuine orphan.
	orphanSHA := sha256HexBytes([]byte("orphan"))
	fake.objects["ws/objects/docs/"+orphanSHA[:2]+"/"+orphanSHA] = []byte("orphan")

	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rep.Advisories {
		if strings.Contains(a, "meta-only") {
			t.Fatalf("meta-map object must NOT be flagged orphan: %q", a)
		}
	}
	found := false
	for _, a := range rep.Advisories {
		if strings.Contains(a, orphanSHA[:16]) {
			found = true
		}
	}
	if !found {
		t.Fatalf("genuine orphan must still be advisory: %v", rep.Advisories)
	}
}

// Live --at with ZERO excluded segments: the live note fires anyway.
func TestHydrate_LiveAtZeroSegmentOvershoot_NoteFires(t *testing.T) {
	h := newHydrateFixture(t)
	// --at in the future relative to everything sealed: zero excluded
	// segments, live gen selected.
	future := time.Now().Add(time.Hour).UTC()
	dst := filepath.Join(t.TempDir(), "restored")
	rep, err := Hydrate(context.Background(), h.cfg, dst, HydrateOpts{At: future})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.Overshoot, "objects are at newest (live map)") {
		t.Fatalf("live --at note must fire with zero excluded segments: %q", rep.Overshoot)
	}
}

// F-019b witness: two retained generations with DIVERGENT shas for one key
// are BOTH checked deterministically — the mismatching generation is named
// (a key-collapsed map would check one nondeterministically or neither).
// The fixture is STRUCTURALLY VALID (ContentSHA256 satisfies content-
// addressed validation) so only invariant (c) can attribute the violation.
func TestVerify_MetaRefsDivergentShasBothChecked(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2)
	shaA := sha256HexBytes([]byte("doc-A"))
	shaB := sha256HexBytes([]byte("doc-B"))
	key := "ws/objects/docs/" + shaA[:2] + "/" + shaA
	metaA := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 2,
		CreatedAt: time.Now().UTC(), SealedAt: time.Now().UTC(),
		Snapshot: "ws/db/generation-2/snapshot.db.zst", SnapshotSHA256: sha256HexBytes([]byte("gen-snap")),
		WAL:     []WALSegmentRef{},
		Objects: map[string]ObjectRef{"wiki/a.md": {Key: key, SHA256: shaA, ContentSHA256: shaA}},
	}
	mb, _ := MarshalMeta(metaA)
	fake.objects[GenerationMetaKey("ws/", 2)] = mb
	fake.objects[key] = []byte("doc-A")

	metaB := &GenerationMeta{
		FormatVersion: FormatVersion, Generation: 1,
		CreatedAt: time.Now().Add(-time.Hour).UTC(), SealedAt: time.Now().UTC(),
		Snapshot: "ws/db/generation-1/snapshot.db.zst", SnapshotSHA256: sha256HexBytes([]byte("gen-snap")),
		WAL:     []WALSegmentRef{},
		Objects: map[string]ObjectRef{"wiki/a.md": {Key: key, SHA256: shaB, ContentSHA256: shaA}},
	}
	mb2, _ := MarshalMeta(metaB)
	fake.objects[GenerationMetaKey("ws/", 1)] = mb2
	fake.objects["ws/db/generation-1/snapshot.db.zst"] = []byte("gen-snap")

	m := openVerifyMirror(t, fake, st)
	m.cfg.RetainGenerations = 5 // both gens retained
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Valid {
		t.Fatal("divergent-sha generation must be flagged")
	}
	found := false
	for _, v := range rep.Violations {
		if strings.Contains(v, "generation 1") && strings.Contains(v, "wiki/a.md") {
			found = true
		}
	}
	if !found {
		t.Fatalf("violation must name generation 1 (the mismatching gen): %v", rep.Violations)
	}
	for _, v := range rep.Violations {
		if strings.Contains(v, "generation 2") && strings.Contains(v, "wiki/a.md") {
			t.Fatalf("matching generation wrongly flagged: %v", rep.Violations)
		}
	}
}

// N-1 regression pin: if invariant (c) collapsed metaRefs by key, this
// assertion would FLIP between runs — call Verify twice and require the
// SAME attribution both times (deterministic slice order).
func TestVerify_MetaRefsDivergentShasDeterministicAttribution(t *testing.T) {
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
	r1, _ := m.Verify(context.Background())
	r2, _ := m.Verify(context.Background())
	v1 := strings.Join(r1.Violations, "|")
	v2 := strings.Join(r2.Violations, "|")
	if v1 != v2 {
		t.Fatalf("attribution must be deterministic:\nrun1: %s\nrun2: %s", v1, v2)
	}
	if !strings.Contains(v1, "generation 1") || strings.Contains(v1, "generation 2: meta-map") {
		t.Fatalf("attribution must name gen 1 only for the meta-map mismatch: %s", v1)
	}
}

// F-023 witness: an abandoned FUTURE-generation dir (crash mid-rotation)
// still flags as orphan; existing-gen debris is skipped.
func TestVerify_OrphanExclusionBoundary(t *testing.T) {
	fake := newFakeS3()
	st := verifyFixture(t, fake)
	addRotatedGen(t, fake, 2)
	// Future-gen debris (gen = live+1): abandoned mid-rotation snapshot.
	fake.objects["ws/db/generation-4/snapshot.db.zst"] = []byte("abandoned")
	// Existing-gen stray key (debris, benign).
	fake.objects["ws/db/generation-1/wal/000099.zst"] = []byte("debris")
	m := openVerifyMirror(t, fake, st)
	rep, err := m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	future := false
	for _, a := range rep.Advisories {
		if strings.Contains(a, "generation-4") {
			future = true
		}
	}
	if !future {
		t.Fatalf("future-gen abandoned snapshot must flag as orphan: %v", rep.Advisories)
	}
	for _, a := range rep.Advisories {
		if strings.Contains(a, "000099") {
			t.Fatalf("existing-gen debris should not flag: %v", rep.Advisories)
		}
	}
}
