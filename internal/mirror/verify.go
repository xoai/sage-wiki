package mirror

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
)

// VerifyMode checks the consistency invariant (spec.md §Data model):
// (a) every object referenced by mirror-state exists with matching sha256,
// (b) every retained rotated generation has a valid meta.json whose objects
// likewise exist and match. Full re-download + re-hash by default; fast is
// HEAD-only existence. Orphans are advisory, never violations.
func (o *mirrorOps) VerifyMode(ctx context.Context, fast bool) (Report, error) {
	m := o.m
	prefix := NormalizePrefix(m.cfg.Prefix)
	rep := Report{Valid: true}

	fail := func(format string, args ...any) {
		rep.Valid = false
		rep.Violations = append(rep.Violations, fmt.Sprintf(format, args...))
	}

	sb, err := m.client.GetObject(ctx, m.cfg.Bucket, StateKey(prefix))
	if err != nil {
		if errors.Is(err, s3.ErrNotFound) {
			fail("mirror-state.json missing — mirror not enabled or never committed")
			return rep, nil
		}
		return rep, fmt.Errorf("mirror verify: read state: %w", err)
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		fail("mirror-state.json unparseable: %v", err)
		return rep, nil
	}
	if err := st.Validate(); err != nil {
		fail("mirror-state.json invalid: %v", err)
	}
	rep.Generation = st.Generation

	referenced := map[string]string{} // key → expected sha ("" = skip hash)

	collect := func(snapshot, snapSHA string, wal []WALSegmentRef, objects, vectors map[string]ObjectRef) {
		referenced[snapshot] = snapSHA
		for _, seg := range wal {
			referenced[seg.Key] = seg.SHA256
		}
		for _, ref := range objects {
			if !ref.Deleted {
				referenced[ref.Key] = ref.SHA256
			}
		}
		for _, ref := range vectors {
			if !ref.Deleted {
				referenced[ref.Key] = ref.SHA256
			}
		}
	}
	collect(st.DB.Snapshot, st.DB.SnapshotSHA256, st.DB.WAL, st.Objects, st.Vectors)

	// Rotated generations (b): LIST db/ — a generation dir is discovered
	// from ANY key under it (a missing meta.json is itself the violation),
	// and every generation < live must have a valid meta.json whose
	// referenced objects check out.
	genKeys, err := m.client.ListObjects(ctx, m.cfg.Bucket, prefix+"db/")
	if err != nil {
		return rep, fmt.Errorf("mirror verify: list db/: %w", err)
	}
	rotated := map[int]bool{}
	for _, k := range genKeys {
		if gen, ok := parseGenerationDirKey(k); ok && gen < st.Generation {
			rotated[gen] = true
		}
	}
	for gen := range rotated {
		mb, err := m.client.GetObject(ctx, m.cfg.Bucket, GenerationMetaKey(prefix, gen))
		if err != nil {
			if errors.Is(err, s3.ErrNotFound) {
				fail("generation %d: meta.json missing (rotated generation unrestorable)", gen)
				continue
			}
			return rep, fmt.Errorf("mirror verify: read meta gen %d: %w", gen, err)
		}
		meta, err := UnmarshalMeta(mb)
		if err != nil {
			fail("generation %d: meta.json unparseable: %v", gen, err)
			continue
		}
		if err := meta.Validate(); err != nil {
			fail("generation %d: meta.json invalid: %v", gen, err)
		}
		collect(meta.Snapshot, meta.SnapshotSHA256, meta.WAL, nil, nil)
	}

	// Existence (and, unless fast, full re-hash) for every referenced object.
	keys := make([]string, 0, len(referenced))
	for k := range referenced {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic violation order
	for _, key := range keys {
		wantSHA := referenced[key]
		exists, err := m.client.HeadObject(ctx, m.cfg.Bucket, key)
		if err != nil {
			return rep, fmt.Errorf("mirror verify: head %s: %w", key, err)
		}
		if !exists {
			fail("%s: referenced by committed state but missing", key)
			continue
		}
		rep.Checked++
		if fast || wantSHA == "" {
			continue
		}
		body, err := m.client.GetObject(ctx, m.cfg.Bucket, key)
		if err != nil {
			return rep, fmt.Errorf("mirror verify: download %s: %w", key, err)
		}
		if got := sha256HexBytes(body); got != wantSHA {
			fail("%s: sha256 mismatch: state says %s, object hashes to %s", key, wantSHA, got)
		}
	}

	// Orphan advisory: anything under our prefixes not referenced.
	for _, listPrefix := range []string{prefix + "objects/", prefix + "vectors/", prefix + "db/"} {
		orphans, err := m.client.ListObjects(ctx, m.cfg.Bucket, listPrefix)
		if err != nil {
			return rep, fmt.Errorf("mirror verify: list %s: %w", listPrefix, err)
		}
		for _, k := range orphans {
			if _, ok := referenced[k]; ok {
				continue
			}
			if _, err := ParseGenerationMetaKey(k); err == nil {
				continue // meta.json is a format member, never an orphan
			}
			rep.Advisories = append(rep.Advisories, "unreferenced object (orphan): "+k)
		}
	}
	sort.Strings(rep.Advisories)
	return rep, nil
}
