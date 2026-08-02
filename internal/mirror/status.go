package mirror

import (
	"context"
	"errors"
	"fmt"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
)

// Status reports local + remote mirror state (spec.md §CLI). Remote reads
// only where needed; lag uses the injected clock.
func (o *mirrorOps) Status(ctx context.Context) (Status, error) {
	m := o.m
	prefix := NormalizePrefix(m.cfg.Prefix)

	sb, err := m.client.GetObject(ctx, m.cfg.Bucket, StateKey(prefix))
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			return Status{Enabled: false, PendingRotation: m.local.PendingRotation}, nil
		}
		return Status{}, fmt.Errorf("mirror status: read remote state: %w", err)
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		return Status{}, err
	}

	pending := 0
	if m.src != nil {
		changes, _, err := m.src.Changes(ctx, ChangeToken{
			Committed:        st.Objects,
			CommittedVectors: st.Vectors,
		})
		if err != nil {
			return Status{}, fmt.Errorf("mirror status: detect changes: %w", err)
		}
		pending = len(changes)
	}

	var lag int64
	if pending > 0 {
		lag = int64(m.now().Sub(st.UpdatedAt).Seconds())
		if lag < 0 {
			lag = 0
		}
	}

	deferred := 0
	if m.local.ConsecutiveDefers > m.cfg.MaxConsecutiveDefers {
		deferred = m.local.ConsecutiveDefers
	}

	return Status{
		Enabled:          true,
		RemoteGeneration: st.Generation,
		LastCommit:       st.UpdatedAt,
		PendingChanges:   pending,
		PendingRotation:  m.local.PendingRotation,
		RotationDeferred: deferred,
		LagSeconds:       lag,
		ServeRestartNote: probeServeLock(m.dir),
	}, nil
}
