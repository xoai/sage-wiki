package sourcedate

import (
	"path/filepath"
	"sort"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/store"
)

// Backfill fills entry_dates for pre-V13 corpora (ADR-039). For every
// manifest source it dates BOTH searchable identities (bare summary-entry
// path and src:-prefixed raw entry — frontmatter > mtime > first-seen);
// concept entries take the max over their sources. Idempotent: the skip
// check consults entry_dates by exactly the IDs Backfill writes (Gate-3
// F-064 — never keyed off FTS IDs, which differ per tier). Dateless
// chains stay absent. Returns how many dates were set.
func Backfill(projectDir string, mem store.EntryStore, m *manifest.Manifest) (int, error) {
	// SPEC-04 D1: sorted candidates — deterministic DB write order.
	var candidates []string
	for _, path := range sortedSourcePaths(m) {
		candidates = append(candidates, path, "src:"+path)
	}
	for _, name := range sortedConceptNames(m) {
		candidates = append(candidates, "concept:"+name)
	}
	have, err := mem.GetSourceDates(candidates)
	if err != nil {
		return 0, err
	}

	set := 0
	// Pass 1: sources (both identities share one resolution).
	for _, path := range sortedSourcePaths(m) {
		src := m.Sources[path]
		_, haveBare := have[path]
		_, haveSrc := have["src:"+path]
		if haveBare && haveSrc {
			continue
		}
		ts := Resolve(filepath.Join(projectDir, path), src.AddedAt)
		if ts <= 0 {
			continue
		}
		// SPEC-04 D1 (Gate-2 review): fixed-order identity iteration — a
		// 2-key inline map literal iterates in random order and entry_dates
		// rowids follow it.
		for _, id := range []string{path, "src:" + path} {
			present := haveBare
			if id != path {
				present = haveSrc
			}
			if present {
				continue
			}
			if err := mem.SetSourceDate(id, ts); err != nil {
				log.Warn("backfill: source date not recorded", "id", id, "error", err)
				continue
			}
			have[id] = ts
			set++
		}
	}
	// Pass 2: concepts (max over contributing sources, populated above).
	for _, name := range sortedConceptNames(m) {
		c := m.Concepts[name]
		id := "concept:" + name
		if _, ok := have[id]; ok {
			continue
		}
		srcIDs := make([]string, len(c.Sources))
		for i, s := range c.Sources {
			srcIDs[i] = "src:" + s
		}
		if ts := Max(have, srcIDs); ts > 0 {
			if err := mem.SetSourceDate(id, ts); err != nil {
				log.Warn("backfill: article source date not recorded", "id", id, "error", err)
				continue
			}
			set++
		}
	}

	// Pass 3: UNMANIFESTED src: entries (Gate-8 F-071). Tier-0/1 indexing
	// never registers sources in the manifest, and a recompile skips
	// already-indexed sources before the date-recording call — so a
	// pre-V13 tier-0/1 vault would stay dateless forever without this
	// pass. Resolve from the entry ID's path (frontmatter > mtime; there
	// is no manifest first-seen for these).
	entries, err := mem.ListAll()
	if err != nil {
		return set, err
	}
	var orphanIDs []string
	for _, e := range entries {
		if len(e.ID) > 4 && e.ID[:4] == "src:" {
			if _, inManifest := m.Sources[e.ID[4:]]; !inManifest {
				orphanIDs = append(orphanIDs, e.ID, e.ID[4:])
			}
		}
	}
	if len(orphanIDs) > 0 {
		orphanHave, err := mem.GetSourceDates(orphanIDs)
		if err != nil {
			return set, err
		}
		for i := 0; i < len(orphanIDs); i += 2 {
			srcID, bareID := orphanIDs[i], orphanIDs[i+1]
			_, hasSrc := orphanHave[srcID]
			_, hasBare := orphanHave[bareID]
			if hasSrc && hasBare {
				continue
			}
			ts := Resolve(filepath.Join(projectDir, bareID), "")
			if ts <= 0 {
				continue
			}
			for id, present := range map[string]bool{srcID: hasSrc, bareID: hasBare} {
				if present {
					continue
				}
				if err := mem.SetSourceDate(id, ts); err != nil {
					log.Warn("backfill: unmanifested source date not recorded", "id", id, "error", err)
					continue
				}
				set++
			}
		}
	}
	return set, nil
}

// BackfillOutputs dates already-promoted Q&A outputs from the trust
// store's creation times (the promote path now stamps new ones; this
// heals pre-existing corpora). Idempotent by the same skip rule.
func BackfillOutputs(mem store.EntryStore, trust store.TrustStore) (int, error) {
	outs, err := trust.ListConfirmed()
	if err != nil {
		return 0, err
	}
	if len(outs) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(outs))
	for _, o := range outs {
		ids = append(ids, "output:"+o.ID)
	}
	have, err := mem.GetSourceDates(ids)
	if err != nil {
		return 0, err
	}
	set := 0
	for _, o := range outs {
		id := "output:" + o.ID
		if _, ok := have[id]; ok {
			continue
		}
		if o.CreatedAt.IsZero() {
			continue
		}
		if err := mem.SetSourceDate(id, o.CreatedAt.Unix()); err != nil {
			log.Warn("backfill: output source date not recorded", "id", id, "error", err)
			continue
		}
		set++
	}
	return set, nil
}

// sortedSourcePaths / sortedConceptNames: SPEC-04 D1 — every map-derived
// slice that feeds write order is sorted.
func sortedSourcePaths(m *manifest.Manifest) []string {
	paths := make([]string, 0, len(m.Sources))
	for p := range m.Sources {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	return paths
}

func sortedConceptNames(m *manifest.Manifest) []string {
	names := make([]string, 0, len(m.Concepts))
	for n := range m.Concepts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
