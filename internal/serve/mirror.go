package serve

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/mirror"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

// MirrorShipper runs continuous mirroring in-process (spec.md §Components:
// ticker at ship_interval, scheduled rotations from the last commit of any
// kind, graceful drain within drain_timeout).
type MirrorShipper struct {
	m              *mirror.Mirror
	shipInterval   time.Duration
	drainTimeout   time.Duration
	stop           chan struct{}
	done           chan struct{}
	drainAbandoned bool // observable by tests: drain exceeded budget

	rotating atomic.Bool    // F-087: rotation in flight — sealing ticks never wait on it
	rotWG    sync.WaitGroup // F-098: Stop awaits in-flight rotations within budget

	lastShip atomic.Int64 // unix seconds of the last successful ship pass (SPEC-07 lag gauge)
}

// NewMirrorShipper builds the in-process shipper (nil-safe usage patterns:
// callers gate on cfg.Mirror.Enabled).
func NewMirrorShipper(m *mirror.Mirror, cfg mirror.Config) *MirrorShipper {
	return &MirrorShipper{
		m:            m,
		shipInterval: cfg.ShipInterval,
		drainTimeout: cfg.DrainTimeout,
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
}

// Start launches the ship loop; Stop drains it.
func (s *MirrorShipper) Start(ctx context.Context) {
	go func() {
		defer close(s.done)
		tick := time.NewTicker(s.shipInterval)
		defer tick.Stop()
		s.lastShip.Store(time.Now().Unix()) // baseline: the shipper just started
		s.runShipLoop(ctx, tick.C)
	}()
}

func (s *MirrorShipper) runShipLoop(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		case <-ticks:
			if err := s.m.Ship(ctx, pkmirror.ChangeBatch{}); err != nil {
				slog.Warn("mirror ship pass failed (retrying next tick)", "err", err)
				// SPEC-07: lag grows until a pass succeeds.
				metrics.GaugeNamed("mirror_ship_lag_seconds").Set(time.Now().Unix() - s.lastShip.Load())
				continue
			}
			s.lastShip.Store(time.Now().Unix())
			metrics.GaugeNamed("mirror_ship_lag_seconds").Set(0)
			// Scheduled rotation on its OWN goroutine (F-087): a busy-
			// writer rotation (~20s of retries) must not starve the
			// segment-sealing cadence that RPO depends on. One in flight
			// at a time.
			if s.m.ScheduledRotationDue() && s.rotating.CompareAndSwap(false, true) {
				s.rotWG.Add(1)
				go func() {
					defer s.rotWG.Done()
					defer s.rotating.Store(false)
					if _, err := s.m.Snapshot(ctx); err != nil {
						slog.Warn("mirror scheduled rotation failed", "err", err)
					}
				}()
			}
		}
	}
}

// Stop signals the loop, waits for the ticker AND any in-flight rotation
// (F-098: a healthy system never reports drainAbandoned just because the
// shipper's own rotation holds the ship-mutex), then runs the FINAL ship
// pass within drain_timeout.
func (s *MirrorShipper) Stop() {
	close(s.stop)
	<-s.done
	// ONE shared drain ctx (item 4): the rotation wait, the final Ship,
	// and Quiesce all share it — total drain ≈ drainTimeout. If the wait
	// consumes the budget, Ship is abandoned loudly and Quiesce skipped.
	drainCtx, cancel := context.WithTimeout(context.Background(), s.drainTimeout)
	defer cancel()
	rotDone := make(chan struct{})
	go func() {
		s.rotWG.Wait()
		close(rotDone)
	}()
	select {
	case <-rotDone:
	case <-drainCtx.Done():
		slog.Warn("mirror drain: rotation still in flight at budget — proceeding to final pass")
	}
	if err := s.m.Ship(drainCtx, pkmirror.ChangeBatch{}); err != nil {
		s.drainAbandoned = true
		slog.Warn("mirror drain: final ship pass abandoned (local state correct; next run re-ships)", "err", err)
		return
	}
	// Quiesce (F-102): fold the just-sealed frames and refresh the hash
	// reference, so the NEXT process's first pass classifies this stop's
	// close-fold as benign (b) rather than a spurious (a) rotation.
	// Failure is LOUD — a skipped quiesce reverts to data-safe (a)
	// rotations, never to lost content.
	if err := s.m.Quiesce(drainCtx); err != nil {
		slog.Warn("mirror drain: quiesce failed (serve-stop folds will rotate conservatively)", "err", err)
	}
}
