package serve

import (
	"context"
	"log/slog"
	"time"

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
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.stop:
				return
			case <-tick.C:
				if err := s.m.Ship(ctx, pkmirror.ChangeBatch{}); err != nil {
					slog.Warn("mirror ship pass failed (retrying next tick)", "err", err)
					continue
				}
				// Scheduled rotation: measured from the last commit of any
				// kind (fold-forced rotations reset it too).
				if s.m.ScheduledRotationDue() {
					if _, err := s.m.Snapshot(ctx); err != nil {
						slog.Warn("mirror scheduled rotation failed", "err", err)
					}
				}
			}
		}
	}()
}

// Stop signals the loop, runs the FINAL ship pass within drain_timeout,
// and reports whether the drain completed in budget (abandonment is loud).
func (s *MirrorShipper) Stop() {
	close(s.stop)
	<-s.done
	ctx, cancel := context.WithTimeout(context.Background(), s.drainTimeout)
	defer cancel()
	if err := s.m.Ship(ctx, pkmirror.ChangeBatch{}); err != nil {
		s.drainAbandoned = true
		slog.Warn("mirror drain: final ship pass abandoned (local state correct; next run re-ships)", "err", err)
	}
}
