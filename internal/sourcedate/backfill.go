package sourcedate

import (
	"path/filepath"

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
	var candidates []string
	for path := range m.Sources {
		candidates = append(candidates, path, "src:"+path)
	}
	for name := range m.Concepts {
		candidates = append(candidates, "concept:"+name)
	}
	have, err := mem.GetSourceDates(candidates)
	if err != nil {
		return 0, err
	}

	set := 0
	// Pass 1: sources (both identities share one resolution).
	for path, src := range m.Sources {
		_, haveBare := have[path]
		_, haveSrc := have["src:"+path]
		if haveBare && haveSrc {
			continue
		}
		ts := Resolve(filepath.Join(projectDir, path), src.AddedAt)
		if ts <= 0 {
			continue
		}
		for id, present := range map[string]bool{path: haveBare, "src:" + path: haveSrc} {
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
	for name, c := range m.Concepts {
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
