package mirror

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestConcurrency_ServePlusCLIPass: an in-process shipper ticking (the serve
// shape) while CLI passes and mirror snapshot fire concurrently — verify
// stays valid (single-leader via ship-mutex).
func TestConcurrency_ServePlusCLIPass(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()

	// "Serve" shipper: a second Mirror ticking passes against the same
	// workspace + bucket.
	_, cfg := setupFakeMirror(t, f.fake)
	ticker, err := Open(f.dir, cfg, NewDiffChangeSource(f.dir))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var tickerWG sync.WaitGroup
	tickerWG.Add(1)
	go func() {
		defer tickerWG.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_, _ = ticker.shipPass(ctx)
			time.Sleep(5 * time.Millisecond)
		}
	}()

	// Concurrent CLI passes + forced snapshots interleaved with writes.
	for i := 0; i < 10; i++ {
		writeWS(t, f.dir, "wiki/concepts/Foo.md", "# Foo "+string(rune('a'+i)))
		if _, err := f.m.shipPass(context.Background()); err != nil {
			t.Fatalf("CLI pass %d: %v", i, err)
		}
		if i%3 == 0 {
			if _, err := f.m.Snapshot(context.Background()); err != nil {
				t.Fatalf("snapshot %d: %v", i, err)
			}
		}
	}
	cancel()
	tickerWG.Wait()

	rep, err := f.m.Verify(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Valid {
		t.Fatalf("verify invalid after concurrent shipping: %+v", rep.Violations)
	}
}

// TestConcurrency_TwoCLICommands: two Mirrors (two CLI processes) ship the
// same workspace concurrently — both processes' mutations end up shipped.
func TestConcurrency_TwoCLICommands(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	_, cfg := setupFakeMirror(t, f.fake)
	m2, err := Open(f.dir, cfg, NewDiffChangeSource(f.dir))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			writeWS(t, f.dir, "wiki/from-m1.md", "m1")
			if _, err := f.m.shipPass(context.Background()); err != nil {
				errs <- err
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 5; i++ {
			writeWS(t, f.dir, "wiki/from-m2.md", "m2")
			if _, err := m2.shipPass(context.Background()); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	select {
	case err := <-errs:
		t.Fatalf("concurrent pass: %v", err)
	default:
	}

	// Final pass settles any mid-pass races; both files must be committed.
	if _, err := f.m.shipPass(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := f.remoteState(t)
	if _, ok := st.Objects["wiki/from-m1.md"]; !ok {
		t.Fatal("m1's mutation never shipped")
	}
	if _, ok := st.Objects["wiki/from-m2.md"]; !ok {
		t.Fatal("m2's mutation never shipped")
	}
	rep, _ := f.m.Verify(context.Background())
	if !rep.Valid {
		t.Fatalf("verify invalid: %+v", rep.Violations)
	}
}

// TestConcurrency_SlowCycleContention: the winner's cycle is stalled past
// ship_lock_timeout by a blocked bucket — the loser times out (warn+skip
// semantics: ErrShipLocked), and a subsequent pass ships normally.
func TestConcurrency_SlowCycleContention(t *testing.T) {
	f := newShipFixture(t)
	defer f.dbClose()
	_, cfg := setupFakeMirror(t, f.fake)

	// Winner: blocked transport holds the mutex.
	f.fake.mu.Lock() // hold the fake's lock — every handler call blocks
	winner, err := Open(f.dir, cfg, NewDiffChangeSource(f.dir))
	if err != nil {
		f.fake.mu.Unlock()
		t.Fatal(err)
	}
	var winnerWG sync.WaitGroup
	winnerWG.Add(1)
	go func() {
		defer winnerWG.Done()
		writeWS(t, winner.dir, "wiki/winner.md", "w")
		_, _ = winner.shipPass(context.Background()) // blocks inside the fake
	}()
	// Let the winner acquire the ship-mutex and enter the blocked PUT.
	time.Sleep(100 * time.Millisecond)

	// Loser: short ship_lock_timeout → must time out fast.
	cfg.ShipLockTimeout = 50 * time.Millisecond
	loser, err := Open(f.dir, cfg, NewDiffChangeSource(f.dir))
	if err != nil {
		f.fake.mu.Unlock()
		t.Fatal(err)
	}
	start := time.Now()
	writeWS(t, loser.dir, "wiki/loser.md", "l")
	_, err = loser.shipPass(context.Background())
	if err == nil {
		t.Fatal("loser should hit ErrShipLocked under contention")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("loser blocked unboundedly: %v", time.Since(start))
	}

	// Unblock: winner completes; a later pass ships the loser's change.
	f.fake.mu.Unlock()
	winnerWG.Wait()
	if _, err := loser.shipPass(context.Background()); err != nil {
		t.Fatalf("post-contention pass: %v", err)
	}
	st := loser.remoteStateForTest(t)
	if _, ok := st.Objects["wiki/loser.md"]; !ok {
		t.Fatal("loser's mutation never shipped after contention")
	}
}

func (m *Mirror) remoteStateForTest(t *testing.T) *State {
	t.Helper()
	st, err := m.remoteState(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return st
}
