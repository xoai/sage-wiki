package mirror

import (
	"context"
	"testing"
	"time"
)

// Witness: F-093 — generation-mismatch reconcile must not seal a
// pre-restart incarnation into the new generation's chain, and frames
// written AFTER the crashed rotation's checkpoint must remain restorable.
func TestRecover_GenMismatch_SingleSaltChain(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t)
	f.dbWrite(t, "row-2")
	f.dbClose()
	ageLocalRotationFile(t, f.dir, -2*time.Hour)
	if res := f.pass(t); !res.Rotated {
		t.Fatal("setup: rotation did not fire")
	}

	// Simulate the step-4 crash: roll local back to pre-rotation (gen 1,
	// old salt/offset/seq) while remote is gen 2.
	staleLocal := *f.m.local
	staleLocal.Generation = 1
	staleLocal.LastSegmentSeq = 1
	if err := SaveLocalState(localStatePath(f.dir), &staleLocal); err != nil {
		t.Fatal(err)
	}
	// A write lands BEFORE the recovery pass (serve-restart-first-tick
	// shape) — its frames belong to the new generation.
	f.dbWrite(t, "row-3")

	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	// Any further seals, then hydrate: row-3 must be restorable.
	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatalf("post-recovery pass: %v", err)
	}
	st := f.remoteState(t)
	if len(st.DB.WAL) == 0 {
		t.Fatal("row-3's frames never sealed into the chain")
	}
	_, cfg := setupFakeMirror(t, f.fake)
	dst := t.TempDir()
	if _, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	assertRowPresent(t, dst, "row-3")
	rep, _ := f.m.Verify(context.Background())
	if !rep.Valid {
		t.Fatalf("verify: %+v", rep.Violations)
	}
}

// Witness: F-094 — frames appended between the tail-commit crash and the
// recovery pass must seal (the realign must not skip them).
func TestRecover_RealignDoesNotSkipLiveFrames(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t)

	// Commit the tail REMOTELY (crash after rotation step 1's commitSegment,
	// local bookkeeping lost).
	f.dbWrite(t, "row-2")
	st := f.remoteState(t)
	seg, err := SealWALSegment(f.m.walPath(), f.m.local.WALOffset)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.commitSegment(context.Background(), st, seg, 2); err != nil {
		t.Fatal(err)
	}
	// Frames land BETWEEN the crash and recovery (same incarnation).
	f.dbWrite(t, "row-3")

	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatalf("recovery pass: %v", err)
	}
	st = f.remoteState(t)
	if len(st.DB.WAL) < 3 {
		t.Fatalf("row-3's frames never sealed: wal=%d", len(st.DB.WAL))
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("chain invalid: %v", err)
	}
	_, cfg := setupFakeMirror(t, f.fake)
	dst := t.TempDir()
	if _, err := Hydrate(context.Background(), cfg, dst, HydrateOpts{}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	assertRowPresent(t, dst, "row-3")
}

var _ = time.Now

// TestChainTailLength_LocalAheadOfRemote (F-099/PB-1): local bookkeeping
// ahead of the remote chain must error loudly, never panic.
func TestChainTailLength_LocalAheadOfRemote(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	st := f.remoteState(t)
	// Simulate split-brain/bucket rollback: local seq beyond the chain.
	f.m.local.LastSegmentSeq = len(st.DB.WAL) + 5
	if _, err := f.m.chainTailLength(context.Background(), st, f.m.local.LastSegmentSeq); err == nil {
		t.Fatal("local-ahead-of-remote must return a loud error, not a panic")
	}
}

// TestSnapshot_AfterTailCrash_NoWedge (F-114): mirror snapshot after a
// step-1-crash window must reconcile, not wedge the remote with a
// duplicate seq.
func TestSnapshot_AfterTailCrash_NoWedge(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	f.dbWrite(t, "row-1")
	f.pass(t)
	// Commit the tail remotely (crash: local bookkeeping lost).
	f.dbWrite(t, "row-2")
	st := f.remoteState(t)
	seg, err := SealWALSegment(f.m.walPath(), f.m.local.WALOffset)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.m.commitSegment(context.Background(), st, seg, 2); err != nil {
		t.Fatal(err)
	}
	// Snapshot MUST reconcile, not duplicate seq 2.
	if _, err := f.m.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	st = f.remoteState(t)
	if err := st.Validate(); err != nil {
		t.Fatalf("remote wedged after snapshot: %v", err)
	}
	rep, _ := f.m.Verify(context.Background())
	if !rep.Valid {
		t.Fatalf("verify: %+v", rep.Violations)
	}
}
