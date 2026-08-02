package mirror

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// shipFixture is a workspace + fake bucket + mirror under test.
type shipFixture struct {
	db   *sql.DB
	dir  string
	fake *fakeS3
	m    *Mirror
	now  time.Time
}

func newShipFixture(t *testing.T) *shipFixture {
	t.Helper()
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	dir := makeWorkspaceWithDB(t)
	m, err := Open(dir, cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	f := &shipFixture{dir: dir, fake: fake, m: m, now: time.Now().UTC()}
	m.now = func() time.Time { return f.now }
	m.src = NewDiffChangeSource(dir)
	// Enable to bootstrap generation 1.
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	return f
}

func (f *shipFixture) dbWrite(t *testing.T, v string) {
	t.Helper()
	if f.db == nil {
		db, err := sql.Open("sqlite", filepath.Join(f.dir, ".sage", "wiki.db"))
		if err != nil {
			t.Fatal(err)
		}
		f.db = db
	}
	if _, err := f.db.Exec("INSERT INTO t (v) VALUES (?)", v); err != nil {
		t.Fatal(err)
	}
}

func (f *shipFixture) dbClose() {
	if f.db != nil {
		f.db.Close()
		f.db = nil
	}
}

func (f *shipFixture) pass(t *testing.T) shipResult {
	t.Helper()
	res, err := f.m.shipPass(context.Background())
	if err != nil {
		t.Fatalf("shipPass: %v", err)
	}
	return res
}

func (f *shipFixture) remoteState(t *testing.T) *State {
	t.Helper()
	sb, ok := f.fake.get(StateKey("ws/"))
	if !ok {
		t.Fatal("no remote state")
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestShip_SealsNewWALFrames(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	res := f.pass(t)
	if res.SealedSegments != 1 {
		t.Fatalf("SealedSegments = %d, want 1", res.SealedSegments)
	}
	st := f.remoteState(t)
	// Writer witness (N1): the segment commit carries retain_generations.
	if st.RetainGenerations != 2 {
		t.Fatalf("segment commit RetainGenerations = %d, want 2", st.RetainGenerations)
	}
	if len(st.DB.WAL) != 1 {
		t.Fatalf("remote wal list = %d", len(st.DB.WAL))
	}
	if _, ok := f.fake.get(st.DB.WAL[0].Key); !ok {
		t.Fatalf("segment %q not PUT", st.DB.WAL[0].Key)
	}
	if f.m.local.LastSegmentSeq != 1 {
		t.Fatalf("LastSegmentSeq = %d", f.m.local.LastSegmentSeq)
	}
	// State committed AFTER the segment (write-then-commit).
	segIdx, stateIdx := -1, -1
	for i, k := range f.fake.putLog {
		if k == st.DB.WAL[0].Key {
			segIdx = i
		}
		if k == StateKey("ws/") && segIdx >= 0 && stateIdx < 0 {
			stateIdx = i
		}
	}
	if stateIdx < segIdx {
		t.Fatal("state committed before its segment")
	}
}

func TestShip_NoNewFrames_NoSegment(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	res := f.pass(t)
	if res.SealedSegments != 0 {
		t.Fatalf("idle pass sealed %d segments", res.SealedSegments)
	}
}

func TestShip_BenignFold_AdoptNoRotation(t *testing.T) {
	f := newShipFixture(t)
	f.dbWrite(t, "row-1")
	f.pass(t)   // seal
	f.dbClose() // close-fold: WAL deleted, db unchanged since seal
	res := f.pass(t)
	if res.Rotated {
		t.Fatal("benign fold (hash unchanged) must not rotate")
	}
	if res.SealedSegments != 0 {
		t.Fatalf("sealed %d on fold pass", res.SealedSegments)
	}
	st := f.remoteState(t)
	if st.Generation != 1 {
		t.Fatalf("generation = %d", st.Generation)
	}
}

func TestShip_FoldWithLoss_ForcesRotation(t *testing.T) {
	f := newShipFixture(t)
	f.dbWrite(t, "row-1")
	f.pass(t)             // seal row-1
	f.dbClose()           // fold row-1 (shipped)
	f.dbWrite(t, "row-2") // new frames in a NEW WAL
	f.dbClose()           // fold row-2 BEFORE sealing — lost from segment chain
	// Fold-forced rotations are debounced (spec): advance past the interval
	// so this pass rotates immediately.
	f.now = f.now.Add(2 * time.Minute)
	res := f.pass(t)
	if !res.Rotated {
		t.Fatal("fold with unshipped loss must force rotation")
	}
	st := f.remoteState(t)
	if st.Generation != 2 {
		t.Fatalf("generation = %d, want 2", st.Generation)
	}
	// Writer witness (N1): the rotation commit carries retain_generations.
	if st.RetainGenerations != 2 {
		t.Fatalf("rotation commit RetainGenerations = %d, want 2", st.RetainGenerations)
	}
	if _, ok := f.fake.get(GenerationMetaKey("ws/", 1)); !ok {
		t.Fatal("gen-1 meta.json not written at rotation")
	}
	// New snapshot restorable: gen-2 snapshot exists.
	if _, ok := f.fake.get(st.DB.Snapshot); !ok {
		t.Fatal("gen-2 snapshot missing")
	}
	// Local bookkeeping committed atomically.
	if f.m.local.Generation != 2 || f.m.local.PendingRotation || f.m.local.ConsecutiveDefers != 0 {
		t.Fatalf("local = %+v", f.m.local)
	}
	if f.m.local.LastRotationAt.IsZero() {
		t.Fatal("LastRotationAt not set")
	}
}

func TestShip_Debounce_PendingRotation(t *testing.T) {
	f := newShipFixture(t)
	f.dbWrite(t, "row-1")
	f.pass(t)
	f.dbClose()
	f.dbWrite(t, "row-2")
	f.dbClose()
	// Fold detected but interval not elapsed → defer, persist pending.
	res := f.pass(t)
	if res.Rotated {
		t.Fatal("rotation should be debounced inside min_rotation_interval")
	}
	if !res.PendingRotation {
		t.Fatal("result should report pending rotation")
	}
	if !f.m.local.PendingRotation {
		t.Fatal("pending_rotation not persisted in local state")
	}
	// Advance past the interval → next pass rotates.
	f.now = f.now.Add(2 * time.Minute)
	res = f.pass(t)
	if !res.Rotated {
		t.Fatal("debounced rotation should fire after interval")
	}
	st := f.remoteState(t)
	if st.Generation != 2 {
		t.Fatalf("generation = %d", st.Generation)
	}
	if f.m.local.PendingRotation {
		t.Fatal("pending_rotation not cleared after rotation")
	}
}

func TestShip_Reconcile_RemoteNewer(t *testing.T) {
	f := newShipFixture(t)
	f.dbWrite(t, "row-1")
	f.pass(t)
	f.dbClose()
	f.dbWrite(t, "row-2")
	f.dbClose()
	f.pass(t) // debounce: pending_rotation=true (inside interval)
	if !f.m.local.PendingRotation {
		t.Fatal("setup: pending_rotation not set")
	}
	// Simulate: the rotation committed remotely (another shipper / crash
	// after step 4) but the local write never happened.
	st := f.remoteState(t)
	st.Generation = 5
	st.DB.Snapshot = SnapshotKey("ws/", 5)
	st.DB.WAL = nil
	sb, _ := MarshalState(st)
	f.fake.objects[StateKey("ws/")] = sb
	// Kill the local db's unshipped data situation is irrelevant — adoption
	// refreshes from current files.
	res := f.pass(t)
	if res.Rotated {
		t.Fatal("reconciliation must not create a spurious rotation")
	}
	if f.m.local.Generation != 5 {
		t.Fatalf("adopted generation = %d, want 5", f.m.local.Generation)
	}
	if f.m.local.PendingRotation {
		t.Fatal("pending_rotation not cleared by reconciliation")
	}
	if f.m.local.LastSegmentSeq != 0 {
		t.Fatalf("seq = %d, want reset to len(remote wal list)=0", f.m.local.LastSegmentSeq)
	}
}

func TestShip_BranchC_FoldOfSealedFrames(t *testing.T) {
	f := newShipFixture(t)
	f.dbWrite(t, "row-1")
	f.pass(t) // sealed
	// Passive checkpoint folds sealed frames into the db (content preserved,
	// hash identical); WAL file persists with same salt.
	db, err := sql.Open("sqlite", filepath.Join(f.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	res := f.pass(t)
	if res.Rotated || res.SealedSegments != 0 {
		t.Fatalf("fold of sealed frames is a no-op: %+v", res)
	}
	if res.HashedDB {
		t.Fatal("pure-idle pass must not read the db (short-circuit)")
	}
	// A subsequent close-fold with no new writes is BENIGN: identity change,
	// hash identical → adopt, no rotation.
	f.dbClose()
	res2 := f.pass(t)
	if res2.Rotated {
		t.Fatal("benign close-fold must not rotate")
	}
}

func TestShip_CombinedWindow_SealAndBranchC(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	// Checkpoint folds NOTHING yet (no seal happened)... build the combined
	// window: seal first, then write more frames, then passive checkpoint.
	f.pass(t)
	f.dbWrite(t, "row-2") // new frames pending
	db, _ := sql.Open("sqlite", filepath.Join(f.dir, ".sage", "wiki.db"))
	db.Exec("PRAGMA wal_checkpoint(PASSIVE)") // folds sealed frames into db
	db.Close()
	res := f.pass(t)
	// Branch (1) seals row-2's frames; branch (c) updates hash bookkeeping.
	if res.SealedSegments != 1 {
		t.Fatalf("combined window: sealed %d, want 1", res.SealedSegments)
	}
	if !res.HashedDB {
		t.Fatal("combined window: hash bookkeeping should update same pass")
	}
	if res.Rotated {
		t.Fatal("combined window must not rotate (no lost frames)")
	}
}

// TestQuiesce_BudgetExceeded (follow-up item 4): a tiny drain budget makes
// Quiesce fail LOUDLY and leave the hash reference untouched.
func TestQuiesce_BudgetExceeded(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t)
	before := f.m.local.LastDBSHA256
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	if err := f.m.Quiesce(ctx); err == nil {
		t.Fatal("expired-budget Quiesce must fail loudly")
	}
	loaded, err := LoadLocalState(localStatePath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastDBSHA256 != before {
		t.Fatal("failed Quiesce must not update the reference")
	}
}

// TestQuiesce_WithinBudget: normal ctx succeeds AND refreshes the reference.
func TestQuiesce_WithinBudget(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := f.m.Quiesce(ctx); err != nil {
		t.Fatalf("Quiesce: %v", err)
	}
	hash, _, err := hashFile(f.m.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadLocalState(localStatePath(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	if loaded.LastDBSHA256 != hash {
		t.Fatal("Quiesce did not refresh the hash reference")
	}
}

// TestHashFileCtx_CancelledCtx pins the CALL PATH: hashFileCtx must route
// through ctxReader (a plain io.Copy replacement would pass the trickle
// test above but fail this one).
func TestHashFileCtx_CancelledCtx(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := hashFileCtx(ctx, f.m.dbPath()); err == nil {
		t.Fatal("hashFileCtx with cancelled ctx must error")
	}
}

// TestHashFileCtx_MidReadCancel (independent review issue 3): cancellation
// DURING the hash aborts it (the ctxReader path, not the fail-fast gate).
// The witness reader cancels the ctx after its first Read — the copy must
// die at the SECOND Read, proving interruption mid-stream.
func TestHashFileCtx_MidReadCancel(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	ctx, cancel := context.WithCancel(context.Background())
	fh, err := os.Open(f.m.dbPath())
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	h := sha256.New()
	reads := 0
	killer := readerFunc(func(p []byte) (int, error) {
		reads++
		if reads > 1 {
			cancel() // cancel AFTER the first successful Read
		}
		// Trickle: one byte per Read so the copy spans MANY reads and the
		// mid-stream cancel is what aborts it (not EOF).
		if len(p) > 1 {
			p = p[:1]
		}
		return fh.Read(p)
	})
	if _, err := io.Copy(h, &ctxReader{ctx: ctx, r: killer}); err == nil {
		t.Fatal("hashFileCtx must abort on mid-stream cancel")
	}
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

// TestShip_RotateSealsObjectMaps (AC-6 writer half): the superseded
// generation's meta.json carries objects/vectors maps EQUAL to the
// committed mirror-state's maps.
func TestShip_RotateSealsObjectMaps(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo")
	writeWS(t, f.dir, "raw/paper.pdf", "PDF")
	writeWS(t, f.dir, ".sage/vectors.idx", "SWVI")
	f.dbWrite(t, "row-1")
	f.dbClose()
	ageLocalRotationFile(t, f.dir, -2*time.Hour)
	if res := f.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	mb, ok := f.fake.get(GenerationMetaKey("ws/", 1))
	if !ok {
		t.Fatal("gen-1 meta.json missing")
	}
	meta, err := UnmarshalMeta(mb)
	if err != nil {
		t.Fatal(err)
	}
	if err := meta.Validate(); err != nil {
		t.Fatalf("sealed meta invalid: %v", err)
	}
	st := f.remoteState(t)
	if len(meta.Objects) != len(st.Objects) {
		t.Fatalf("meta objects %d != state objects %d", len(meta.Objects), len(st.Objects))
	}
	for path, ref := range st.Objects {
		got, ok := meta.Objects[path]
		if !ok || got != ref {
			t.Fatalf("meta objects[%q] = %+v, want %+v", path, got, ref)
		}
	}
	if len(meta.Vectors) != len(st.Vectors) {
		t.Fatalf("meta vectors %d != state vectors %d", len(meta.Vectors), len(st.Vectors))
	}
	for name, ref := range st.Vectors {
		got, ok := meta.Vectors[name]
		if !ok || got != ref {
			t.Fatalf("meta vectors[%q] = %+v, want %+v", name, got, ref)
		}
	}
}

// N-5 witness: a docs-only commit (no WAL growth) advances the sealed
// map's freshness PAST the last segment — SealedAt must be
// max(tail sealed_at, UpdatedAt), i.e. the docs-only commit time, not
// the segment's.
func TestShip_RotateSealedAtCoversDocsOnlyCommits(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t) // segment sealed (tail exists)
	// Docs-only commit at a LATER fake time (no WAL growth).
	writeWS(t, f.dir, "wiki/concepts/Late.md", "late")
	f.now = f.now.Add(3 * time.Second)
	f.pass(t)
	st := f.remoteState(t)
	docsCommit := st.UpdatedAt
	// Rotate.
	f.dbWrite(t, "row-2")
	f.dbClose()
	f.now = f.now.Add(2 * time.Minute)
	if res := f.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	mb, ok := f.fake.get(GenerationMetaKey("ws/", 1))
	if !ok {
		t.Fatal("meta missing")
	}
	meta, err := UnmarshalMeta(mb)
	if err != nil {
		t.Fatal(err)
	}
	if !meta.SealedAt.Equal(docsCommit) {
		t.Fatalf("SealedAt = %v, want the docs-only commit time %v (map freshness, not the segment's)", meta.SealedAt, docsCommit)
	}
}
