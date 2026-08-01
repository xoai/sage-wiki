package mirror

import (
	"context"
	"fmt"
)

// pruneGenerations deletes generations older than the retain_generations
// newest AFTER a new generation commits (spec.md §Rationale: client-side
// prune is format housekeeping; bucket lifecycle policies stay a non-goal).
// Failures are advisory warnings — the commit pointer is never at risk.
func (m *Mirror) pruneGenerations(ctx context.Context, liveGen int) []string {
	prefix := NormalizePrefix(m.cfg.Prefix)
	floor := liveGen - m.cfg.RetainGenerations + 1
	if floor <= 1 {
		return nil // nothing old enough to prune
	}
	keys, err := m.client.ListObjects(ctx, m.cfg.Bucket, prefix+"db/")
	if err != nil {
		return []string{fmt.Sprintf("prune: list db/: %v", err)}
	}
	var warnings []string
	for _, key := range keys {
		gen, ok := parseGenerationDirKey(key)
		if !ok || gen >= floor {
			continue
		}
		if err := m.pruneDelete(ctx, m.cfg.Bucket, key); err != nil {
			warnings = append(warnings, fmt.Sprintf("prune: delete %s: %v", key, err))
		}
	}
	return warnings
}

// pruneDeleteDefault deletes via the client (swappable via m.pruneDelete).
func (m *Mirror) pruneDeleteDefault(ctx context.Context, bucket, key string) error {
	return m.client.DeleteObject(ctx, bucket, key)
}
