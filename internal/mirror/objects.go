package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// shipObjects syncs the ship set to content-addressed bucket objects
// (spec.md §Object sync): upserts PUT at content keys (dedup by sha via the
// diff), deletes recorded as tombstones in the committed state — never a
// physical delete (bucket versioning honored). st is mutated in memory; the
// caller's state commit carries the updates LAST.
func (m *Mirror) shipObjects(ctx context.Context, st *State, changes []Change, res *shipResult) error {
	prefix := NormalizePrefix(m.cfg.Prefix)
	for _, ch := range changes {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ch.Vector {
			if err := m.syncVector(ctx, st, prefix, ch, res); err != nil {
				return err
			}
			continue
		}
		switch ch.Kind {
		case ChangeUpsert:
			b, err := os.ReadFile(filepath.Join(m.dir, filepath.FromSlash(ch.Path)))
			if err != nil {
				// Vanished between diff and read — next pass's diff settles it.
				res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s vanished mid-pass: %v", ch.Path, err))
				continue
			}
			if got := sha256HexBytes(b); got != ch.SHA256 {
				// Changed underfoot — skip this pass; the diff re-fires.
				res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s changed mid-pass, deferred", ch.Path))
				continue
			}
			key := DocObjectKey(prefix, ch.SHA256)
			if err := m.putObject(ctx, key, b); err != nil {
				return fmt.Errorf("ship: PUT %s: %w", ch.Path, err)
			}
			st.Objects[ch.Path] = ObjectRef{Key: key, SHA256: ch.SHA256}
			res.ObjectsShipped++
		case ChangeDelete:
			ref, ok := st.Objects[ch.Path]
			if !ok {
				continue // never shipped — nothing to tombstone
			}
			ref.Deleted = true
			st.Objects[ch.Path] = ref
			res.ObjectsTombstoned++
		}
	}
	return nil
}

func (m *Mirror) syncVector(ctx context.Context, st *State, prefix string, ch Change, res *shipResult) error {
	switch ch.Kind {
	case ChangeUpsert:
		b, err := os.ReadFile(filepath.Join(m.dir, ".sage", ch.Path))
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s vanished mid-pass: %v", ch.Path, err))
			return nil
		}
		key := VectorObjectKey(prefix, ch.SHA256)
		if err := m.putObject(ctx, key, b); err != nil {
			return fmt.Errorf("ship: PUT vector %s: %w", ch.Path, err)
		}
		st.Vectors[ch.Path] = ObjectRef{Key: key, SHA256: ch.SHA256}
		res.ObjectsShipped++
	case ChangeDelete:
		ref, ok := st.Vectors[ch.Path]
		if !ok {
			return nil
		}
		ref.Deleted = true
		st.Vectors[ch.Path] = ref
		res.ObjectsTombstoned++
	}
	return nil
}

// putObject is the single byte-path seam (Task 21 encryption hooks here).
func (m *Mirror) putObject(ctx context.Context, key string, b []byte) error {
	return m.client.PutObject(ctx, m.cfg.Bucket, key, b)
}
