package mirror

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
)

// metaRef records one invariant-(c) expectation: an object a retained
// generation's sealed map claims must exist.
type metaRef struct {
	gen    int
	path   string
	key    string
	sha256 string
}

// VerifyMode checks the consistency invariant (spec.md §Data model):
// (a) every object referenced by mirror-state exists with matching sha256,
// (b) every retained rotated generation has a valid meta.json (snapshot +
// WAL), and (c) every non-tombstoned entry in those generations' sealed
// object maps exists with matching sha256 — a retained generation is FULLY
// restorable, not just db-restorable. Full re-download + re-hash by
// default; fast is HEAD-only existence. Orphans are advisory, never
// violations.
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
	// metaRefs are invariant-(c) references from retained generations' sealed
	// object maps — a SLICE: every (gen, path, key, sha) tuple is checked
	// (two generations' divergent shas for one key both get verified, never
	// collapsed by map iteration — F-019b).
	var metaRefs []metaRef

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
	// and every RETAINED generation < live must have a valid meta.json
	// whose referenced objects check out. Retention is RULE-BASED, not
	// physical: prune-eligible generations (gen ≤ live − retain_generations)
	// are exempt — a kill mid-prune leaves one partially deleted BY DESIGN,
	// and that is not corruption (the invariant covers only what the format
	// promises to keep).
	genKeys, err := m.client.ListObjects(ctx, m.cfg.Bucket, prefix+"db/")
	if err != nil {
		return rep, fmt.Errorf("mirror verify: list db/: %w", err)
	}
	// Retention: prefer the STATE's recorded retain (it reflects the
	// shipper's actual config; local config may differ on this workspace).
	// A state retain < 1 is treated as ABSENT (F-023): 0 is the omitempty
	// zero, and a negative (hand-edit/corruption) must never disable ALL
	// rotated-generation checks silently.
	retain := st.RetainGenerations
	if retain < 1 {
		retain = m.cfg.RetainGenerations
	}
	rotated := map[int]bool{}
	for _, k := range genKeys {
		if gen, ok := parseGenerationDirKey(k); ok && gen < st.Generation &&
			gen > st.Generation-retain {
			rotated[gen] = true
		}
	}
	gens := make([]int, 0, len(rotated))
	for gen := range rotated {
		gens = append(gens, gen)
	}
	sort.Ints(gens) // deterministic violation/report order (SPEC-04 D1)
	for _, gen := range gens {
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
			continue // an invalid meta contributes no references (no cascade of secondary violations)
		}
		collect(meta.Snapshot, meta.SnapshotSHA256, meta.WAL, nil, nil)
		// Invariant (c): the generation's sealed object maps are checked by
		// the same sha rule as live objects (tombstones skipped — a retained
		// generation must be FULLY restorable, not just db-restorable).
		for path, ref := range meta.Objects {
			if ref.Deleted {
				continue
			}
			metaRefs = append(metaRefs, metaRef{gen: gen, path: path, key: ref.Key, sha256: ref.SHA256})
		}
		for name, ref := range meta.Vectors {
			if ref.Deleted {
				continue
			}
			metaRefs = append(metaRefs, metaRef{gen: gen, path: name, key: ref.Key, sha256: ref.SHA256})
		}
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

	// Invariant (c): meta-map references — existence + (unless fast) full
	// re-hash, violations naming generation + path. Sorted by (gen, key)
	// for deterministic violation order.
	sort.Slice(metaRefs, func(i, j int) bool {
		if metaRefs[i].gen != metaRefs[j].gen {
			return metaRefs[i].gen < metaRefs[j].gen
		}
		if metaRefs[i].key != metaRefs[j].key {
			return metaRefs[i].key < metaRefs[j].key
		}
		return metaRefs[i].path < metaRefs[j].path
	})
	for _, ref := range metaRefs {
		key := ref.key
		exists, err := m.client.HeadObject(ctx, m.cfg.Bucket, key)
		if err != nil {
			return rep, fmt.Errorf("mirror verify: head %s: %w", key, err)
		}
		if !exists {
			fail("generation %d: meta-map object %s (%s) missing", ref.gen, ref.path, key)
			continue
		}
		rep.Checked++
		if fast || ref.sha256 == "" {
			continue
		}
		body, err := m.client.GetObject(ctx, m.cfg.Bucket, key)
		if err != nil {
			return rep, fmt.Errorf("mirror verify: download %s: %w", key, err)
		}
		if got := sha256HexBytes(body); got != ref.sha256 {
			fail("generation %d: meta-map object %s (%s): sha256 mismatch: map says %s, object hashes to %s", ref.gen, ref.path, key, ref.sha256, got)
		}
	}

	// metaRef keys referenced for the orphan union.
	metaRefKeys := map[string]bool{}
	for _, ref := range metaRefs {
		metaRefKeys[ref.key] = true
	}

	// Orphan advisory: anything under our prefixes not referenced by live
	// state OR any retained generation's sealed map (F-013: a meta-only
	// object is a FORMAT MEMBER, never an orphan).
	for _, listPrefix := range []string{prefix + "objects/", prefix + "vectors/", prefix + "db/"} {
		orphans, err := m.client.ListObjects(ctx, m.cfg.Bucket, listPrefix)
		if err != nil {
			return rep, fmt.Errorf("mirror verify: list %s: %w", listPrefix, err)
		}
		for _, k := range orphans {
			if _, ok := referenced[k]; ok {
				continue
			}
			if metaRefKeys[k] {
				continue // referenced by a retained generation's sealed map
			}
			if gen, ok := parseGenerationDirKey(k); ok && gen <= st.Generation {
				// Chain members/debris of existing generations (benign:
				// crash-window residue self-heals). FUTURE-gen keys (gen >
				// live — abandoned mid-rotation garbage nothing references)
				// still flag (F-023).
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
