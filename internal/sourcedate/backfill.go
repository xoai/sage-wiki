package sourcedate

import (
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/manifest"
	"github.com/xoai/sage-wiki/internal/store"
)

// BackfillSourceDates fills entry_dates for pre-V13 corpora (ADR-039):
// src: entries resolve frontmatter > mtime > manifest first-seen;
// concept: entries take the max over their manifest sources' dates.
// Entries that already have a date are left untouched; entries whose
// chain yields nothing stay dateless. Returns how many dates were set.
func Backfill(projectDir string, mem store.EntryStore, m *manifest.Manifest) (int, error) {
	entries, err := mem.ListAll()
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	have, err := mem.GetSourceDates(ids)
	if err != nil {
		return 0, err
	}

	set := 0
	// Pass 1: raw sources.
	for path, src := range m.Sources {
		id := "src:" + path
		if _, ok := have[id]; ok {
			continue
		}
		ts := Resolve(filepath.Join(projectDir, path), src.AddedAt)
		if ts <= 0 {
			continue
		}
		if err := mem.SetSourceDate(id, ts); err != nil {
			log.Warn("backfill: source date not recorded", "id", id, "error", err)
			continue
		}
		have[id] = ts
		set++
	}
	// Pass 2: concepts (max over contributing sources, now that pass 1
	// populated them).
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
