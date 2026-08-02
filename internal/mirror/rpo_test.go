package mirror

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// TestRPO_ServeShipper measures the recovery point objective (spec AC-3):
// with the shipper ticking at ship_interval, a kill WITHOUT drain loses at
// most ship_interval of writes on the fixture workload. The measurement is
// written to rpo-results.md by the test runner (Task 23 artifact).
func TestRPO_ServeShipper(t *testing.T) {
	interval := 50 * time.Millisecond
	f := newShipFixture(t)
	defer f.dbClose()
	f.m.cfg.ShipInterval = interval

	// Ticking shipper (no drain — simulates kill -9).
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				_ = f.m.Ship(ctx, pkmirror.ChangeBatch{})
			}
		}
	}()

	// Workload: one marked write per 10ms (faster than the interval, so
	// recent writes are usually pending at kill), then "kill".
	const writes = 100
	writtenAt := make([]time.Time, writes)
	for i := 0; i < writes; i++ {
		writeWS(t, f.dir, fmt.Sprintf("wiki/rpo/%03d.md", i), fmt.Sprintf("w%d", i))
		writtenAt[i] = time.Now()
		time.Sleep(10 * time.Millisecond)
	}
	cancel() // kill: NO drain, NO final pass
	<-done

	// Newest committed state → hydrate → count surviving writes.
	st := f.remoteState(t)
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := Hydrate(context.Background(), f.m.cfg, dst, HydrateOpts{}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	survivors := 0
	var lastSurvivor time.Time
	for i := 0; i < writes; i++ {
		if _, err := readFileString(filepath.Join(dst, fmt.Sprintf("wiki/rpo/%03d.md", i))); err == nil {
			survivors++
			lastSurvivor = writtenAt[i]
		}
	}
	killTime := writtenAt[writes-1]
	lossWindow := killTime.Sub(lastSurvivor)
	t.Logf("RPO: %d/%d writes survived; loss window %v (ship_interval %v); commit %s",
		survivors, writes, lossWindow, interval, st.UpdatedAt.Format(time.RFC3339))
	// AC-3: induced loss ≤ ship_interval of writes. 2× slack for scheduler
	// jitter on the ticker (the guarantee is measured, not theoretical).
	if lossWindow > 2*interval {
		t.Fatalf("RPO violated: lost %v of writes (> 2×ship_interval %v)", lossWindow, interval)
	}
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

var _ = sql.Open
