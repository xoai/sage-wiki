package manifest

import (
	"context"
	"fmt"
	"reflect"
)

// Clone returns a deep, independent copy of the manifest: the maps and every
// slice field are copied, so mutating the clone never aliases the original. A
// long-running owner (compile) takes a Clone at Load time as the merge base for
// MergeSave (D3) — the base must not drift as the owner mutates its working copy.
func (m *Manifest) Clone() *Manifest {
	c := &Manifest{
		Version:       m.Version,
		EmbedModel:    m.EmbedModel,
		EmbedDim:      m.EmbedDim,
		FormatVersion: m.FormatVersion,
		Engine:        m.Engine,
		CreatedAt:     m.CreatedAt,
		Sources:       make(map[string]Source, len(m.Sources)),
		Concepts:      make(map[string]Concept, len(m.Concepts)),
	}
	for k, s := range m.Sources {
		if s.ConceptsProduced != nil {
			cp := make([]string, len(s.ConceptsProduced))
			copy(cp, s.ConceptsProduced)
			s.ConceptsProduced = cp
		}
		c.Sources[k] = s
	}
	for k, con := range m.Concepts {
		if con.Sources != nil {
			cs := make([]string, len(con.Sources))
			copy(cs, con.Sources)
			con.Sources = cs
		}
		if con.Aliases != nil {
			as := make([]string, len(con.Aliases))
			copy(as, con.Aliases)
			con.Aliases = as
		}
		c.Concepts[k] = con
	}
	return c
}

// MergeSave persists a long-running owner's manifest (compile / resumeBatch /
// on-demand / reextract) without clobbering concurrent short writers (D3). Under
// the exclusive lock it reloads the manifest fresh from disk (theirs — which
// carries any writer that landed during the owner's run), applies the owner's
// changes since Load via a structural three-way merge (ours relative to base,
// owner authoritative on same-key conflicts), and saves atomically.
//
// base is the Clone taken at the owner's Load; ours is its current in-memory
// manifest. The common no-contention case (theirs == base) collapses the merge
// to ours. Cost is O(N) once — one reload, one save — never per mutation.
func MergeSave(ctx context.Context, path string, base, ours *Manifest) error {
	return mergeSaveWithOpts(ctx, path, defaultLockOptions(), base, ours)
}

func mergeSaveWithOpts(ctx context.Context, path string, opts lockOptions, base, ours *Manifest) error {
	lock, err := acquireLockOpts(ctx, path, opts)
	if err != nil {
		return fmt.Errorf("manifest.MergeSave: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	theirs, err := Load(path)
	if err != nil {
		return fmt.Errorf("manifest.MergeSave: reload: %w", err)
	}
	merged := mergeInto(theirs, base, ours)
	if err := merged.Save(path); err != nil {
		return fmt.Errorf("manifest.MergeSave: save: %w", err)
	}
	return nil
}

// mergeInto applies the owner's changes (ours relative to base) onto theirs in
// place and returns it. It is a structural three-way merge over both maps, so it
// captures every mutation the owner made without enumerating them — additions,
// modifications (including dedup-merges into existing concepts), and removals
// (including the concept cleanup that keeps a removed source from orphaning a
// concept). The owner (ours) wins same-key conflicts. Scalar embed fields follow
// the same rule.
func mergeInto(theirs, base, ours *Manifest) *Manifest {
	mergeMap(theirs.Sources, base.Sources, ours.Sources)
	mergeMap(theirs.Concepts, base.Concepts, ours.Concepts)
	if ours.EmbedModel != base.EmbedModel {
		theirs.EmbedModel = ours.EmbedModel
	}
	if ours.EmbedDim != base.EmbedDim {
		theirs.EmbedDim = ours.EmbedDim
	}
	if ours.Version != base.Version {
		theirs.Version = ours.Version
	}
	return theirs
}

// mergeMap applies ours's delta (relative to base) onto theirs:
//   - a key ours added or changed vs base  -> ours's value wins (overwrites theirs)
//   - a key ours removed vs base            -> deleted from theirs
//   - a key ours left equal to base         -> theirs's value kept (theirs's own add/change survives)
func mergeMap[V any](theirs, base, ours map[string]V) {
	for k, ov := range ours {
		bv, inBase := base[k]
		if !inBase || !reflect.DeepEqual(ov, bv) {
			theirs[k] = ov // ours added or modified it — owner authoritative
		}
	}
	for k := range base {
		if _, stillOurs := ours[k]; !stillOurs {
			delete(theirs, k) // ours removed it
		}
	}
}
