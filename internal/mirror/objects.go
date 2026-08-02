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
			// Resurrect-with-identical-content: un-tombstone the existing ref
			// (keep sha + key, NO re-PUT) — a fresh PUT under encryption
			// writes new-nonce ciphertext (new shipped sha) under the same
			// content key, silently invalidating every historical sealed map
			// that names the old sha (F-019).
			if ref, ok := st.Objects[ch.Path]; ok && ref.Deleted && committedContentSHA(ref) == ch.SHA256 {
				// Re-read before un-tombstoning (N-3): the diff token is
				// from BEFORE this pass's network time — the file may have
				// changed underfoot (same guard as the normal upsert path).
				b, rerr := os.ReadFile(filepath.Join(m.dir, filepath.FromSlash(ch.Path)))
				if rerr != nil {
					// Vanished between diff and guard — DEFER like the sibling
					// upsert path (un-tombstoning a gone file restores a
					// just-deleted doc at next hydrate).
					res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s vanished mid-pass: %v", ch.Path, rerr))
					continue
				}
				if sha256HexBytes(b) != ch.SHA256 {
					res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s changed mid-pass, deferred", ch.Path))
					continue
				}
				ref.Deleted = false
				st.Objects[ch.Path] = ref
				res.ObjectsResurrected++
				continue
			}
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
			shippedSHA, err := m.putObjectShasum(ctx, key, b)
			if err != nil {
				return fmt.Errorf("ship: PUT %s: %w", ch.Path, err)
			}
			st.Objects[ch.Path] = m.objectRef(key, shippedSHA, ch.SHA256)
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
		// Resurrect-with-identical-content for vectors (F-019's vector half):
		// un-tombstone instead of re-PUT — a fresh PUT under encryption
		// writes new-nonce ciphertext (new shipped sha) and invalidates
		// every historical sealed map naming the old sha.
		if ref, ok := st.Vectors[ch.Path]; ok && ref.Deleted && committedContentSHA(ref) == ch.SHA256 {
			b, rerr := os.ReadFile(filepath.Join(m.dir, ".sage", ch.Path))
			if rerr != nil {
				res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s vanished mid-pass: %v", ch.Path, rerr))
				return nil
			}
			if sha256HexBytes(b) != ch.SHA256 {
				res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s changed mid-pass, deferred", ch.Path))
				return nil
			}
			ref.Deleted = false
			st.Vectors[ch.Path] = ref
			res.ObjectsResurrected++
			return nil
		}
		b, err := os.ReadFile(filepath.Join(m.dir, ".sage", ch.Path))
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s vanished mid-pass: %v", ch.Path, err))
			return nil
		}
		// Underfoot re-hash (F-021, CRITICAL): the docs upsert has this
		// guard; without it a torn read commits {Key: <shaX>, SHA256: <shaY>}
		// which FAILS validateObjectRef on every subsequent pass and
		// permanently wedges ship + all hydrates (unencrypted default).
		if got := sha256HexBytes(b); got != ch.SHA256 {
			res.Warnings = append(res.Warnings, fmt.Sprintf("ship: %s changed mid-pass, deferred", ch.Path))
			return nil
		}
		key := VectorObjectKey(prefix, ch.SHA256)
		shippedSHA, err := m.putObjectShasum(ctx, key, b)
		if err != nil {
			return fmt.Errorf("ship: PUT vector %s: %w", ch.Path, err)
		}
		st.Vectors[ch.Path] = m.objectRef(key, shippedSHA, ch.SHA256)
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

// putObjectShasum ships through the single byte-path seam and returns the
// sha of the SHIPPED bytes (ciphertext when encrypted).
func (m *Mirror) putObjectShasum(ctx context.Context, key string, b []byte) (string, error) {
	shipped, err := m.shipBytes(b)
	if err != nil {
		return "", err
	}
	if err := m.client.PutObject(ctx, m.cfg.Bucket, key, shipped); err != nil {
		return "", err
	}
	return sha256HexBytes(shipped), nil
}

// objectRef builds the committed reference: shipped-bytes sha for integrity,
// content sha recorded only under encryption (diff dedupe + key addressing).
func (m *Mirror) objectRef(key, shippedSHA, contentSHA string) ObjectRef {
	ref := ObjectRef{Key: key, SHA256: shippedSHA}
	if m.encKey != nil {
		ref.ContentSHA256 = contentSHA
	}
	return ref
}
