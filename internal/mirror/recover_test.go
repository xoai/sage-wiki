package mirror

import (
	"context"
	"testing"
	"time"
)

// Witness tests for Gate-3 F-077/F-078: crash windows between rotation
// step 1 (tail commit) / step 4 (state commit) and step 5 (local commit).

// TestRecover_TailCommitCrash_NoDuplicateSeq (F-077): simulate the exact
// crash — the tail segment is committed REMOTELY (rotation step 1's
// commitSegment) but the local bookkeeping save never happened (step 5
// lost). Same generation on both sides; stale seq/offset. Before the
// reconciliation fix, the next pass re-sealed the tail into a duplicate
// seq and wedged the mirror permanently ("wal seq gap: 000002 after
// 000002").
func TestRecover_TailCommitCrash_NoDuplicateSeq(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t) // seal seg 1

	// row-2 in WAL, unsealed. Commit the tail REMOTELY (as rotation step 1
	// would) while local stays stale — the crash.
	f.dbWrite(t, "row-2")
	st := f.remoteState(t)
	seg, err := SealWALSegment(f.m.walPath(), f.m.local.WALOffset)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.commitSegment(context.Background(), st, seg, 2); err != nil {
		t.Fatal(err)
	}
	// Sanity: remote now has wal [1,2], local says seq 1.
	st = f.remoteState(t)
	if len(st.DB.WAL) != 2 {
		t.Fatalf("setup: remote wal = %d", len(st.DB.WAL))
	}

	// Recovery pass: must reconcile, not duplicate.
	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	st = f.remoteState(t)
	seqs := map[int]bool{}
	for _, seg := range st.DB.WAL {
		_, seq, err := ParseWALSegmentKey(seg.Key)
		if err != nil {
			t.Fatal(err)
		}
		if seqs[seq] {
			t.Fatalf("duplicate seq %d in committed wal list: %+v", seq, st.DB.WAL)
		}
		seqs[seq] = true
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("committed state invalid after recovery: %v", err)
	}
	rep, err := f.m.Verify(context.Background())
	if err != nil || !rep.Valid {
		t.Fatalf("verify after recovery: %+v %v", rep, err)
	}
	// And the NEXT pass works (the wedge was permanent before the fix).
	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatalf("post-recovery pass: %v", err)
	}
}

// TestRecover_Step4Crash_NoPoisonedChain (F-078): crash AFTER rotation
// step 4 (gen N+1 committed) but BEFORE step 5 (local commit). Local file
// says gen N with old salt/offset; the on-disk WAL still holds pre-rotation
// content. The recovery pass must NOT seal that stale WAL into the new
// generation (which produced a headerless first segment and silent
// restore loss before the fix).
func TestRecover_Step4Crash_NoPoisonedChain(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t)

	// Rotate for real, then simulate the step-4 crash: roll local state
	// back to pre-rotation (gen 1, old salt/offset/seq).
	f.dbWrite(t, "row-2")
	f.dbClose() // fold before sealing → forced rotation
	staleLocal := *f.m.local
	ageLocalRotationFile(t, f.dir, -2*time.Hour)
	if res := f.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}
	// Remote is gen 2; local says gen 1 (stale salt/offset/seq, WAL on disk
	// may still carry pre-restart content).
	if err := SaveLocalState(localStatePath(f.dir), &staleLocal); err != nil {
		t.Fatal(err)
	}

	// Recovery pass + a new write, then a follow-up pass to seal it.
	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	f.dbWrite(t, "row-3")
	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatalf("post-recovery pass: %v", err)
	}

	st := f.remoteState(t)
	if st.Generation != 2 {
		t.Fatalf("generation = %d", st.Generation)
	}
	// Hydrate must restore row-3 (gen-2 WAL chain is clean).
	_, cfg := setupFakeMirror(t, f.fake)
	dst := t.TempDir()
	rep, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{})
	if err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if !rep.Valid {
		t.Fatalf("report = %+v", rep)
	}
	assertRowPresent(t, dst, "row-3")
	rep2, err := f.m.Verify(context.Background())
	if err != nil || !rep2.Valid {
		t.Fatalf("verify: %+v %v", rep2, err)
	}
}
