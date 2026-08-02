package compiler

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
	"gopkg.in/yaml.v3"
)

// TierManager resolves effective tiers for source files and manages
// promotion/demotion based on usage signals.
type TierManager struct {
	cfg   *config.CompilerConfig
	items store.CompileItemStore
}

// NewTierManager creates a new TierManager.
func NewTierManager(cfg *config.CompilerConfig, items store.CompileItemStore) *TierManager {
	return &TierManager{cfg: cfg, items: items}
}

// ResolveTier returns the effective tier for a source path.
// Priority: frontmatter > .wikitier > config tier_defaults > default_tier.
//
// Pass nil for frontmatter when content hasn't been read yet — .wikitier
// and config defaults are used. Frontmatter overrides take effect on the
// next pass that reads the file.
func (tm *TierManager) ResolveTier(path string, projectDir string, frontmatter map[string]interface{}) int {
	// 1. Frontmatter override
	if frontmatter != nil {
		if v, ok := frontmatter["tier"]; ok {
			if tier, ok := toInt(v); ok && tier >= 0 && tier <= 3 {
				return tier
			}
		}
	}

	// 2. .wikitier file — walk up from source directory to projectDir.
	// Most-specific (closest to file) wins.
	absDir := filepath.Dir(filepath.Join(projectDir, path))
	absProject := filepath.Clean(projectDir)
	for dir := absDir; ; dir = filepath.Dir(dir) {
		if tier, ok := tm.resolveWikiTier(dir, path); ok {
			return tier
		}
		// Stop when we've checked the project root
		if dir == absProject || dir == filepath.Dir(dir) {
			break
		}
	}

	// 3. Config tier_defaults by file extension
	return tm.ConfigDefault(path)
}

// ConfigDefault returns the default tier from config for a file path.
// Uses the file extension to look up tier_defaults, falls back to default_tier.
func (tm *TierManager) ConfigDefault(path string) int {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	if tm.cfg.TierDefaults != nil {
		if tier, ok := tm.cfg.TierDefaults[ext]; ok {
			return tier
		}
	}
	if tm.cfg.DefaultTier > 0 {
		return tm.cfg.DefaultTier
	}
	return 1
}

// CheckPromotions evaluates Tier 0-1 sources against promotion signals.
// Returns source paths that should be promoted to Tier 3.
// Filtering is done in SQL to avoid loading all low-tier items at scale.
func (tm *TierManager) CheckPromotions() ([]string, error) {
	if !tm.cfg.AutoPromoteEnabled() {
		return nil, nil
	}

	signals := tm.cfg.PromoteSignals
	hitThreshold := signals.QueryHitCount
	if hitThreshold <= 0 {
		hitThreshold = 3
	}

	return tm.items.ListPromotionCandidates(hitThreshold)
}

// CheckDemotions evaluates Tier 3 sources against demotion signals.
// Returns source paths that should be demoted to Tier 1.
// Filtering is done in SQL to avoid loading all Tier 3 items at scale.
func (tm *TierManager) CheckDemotions() ([]string, error) {
	if !tm.cfg.AutoDemoteEnabled() {
		return nil, nil
	}

	staleDays := tm.cfg.DemoteSignals.StaleDays
	if staleDays <= 0 {
		staleDays = 90
	}

	// The threshold MUST come from the same clock that stamps created_at
	// (SPEC-04 D4): with SOURCE_DATE_EPOCH pinned, created_at is the epoch —
	// comparing it against wall-clock-now would demote every row as stale.
	staleThreshold := config.NowUTC().AddDate(0, 0, -staleDays).Format(time.RFC3339)
	return tm.items.ListDemotionCandidates(staleThreshold)
}

// RecordQueryHit increments hit count for sources matching a search query.
func (tm *TierManager) RecordQueryHit(sourcePaths []string) error {
	return tm.items.IncrementQueryHits(sourcePaths)
}

// resolveWikiTier checks for a .wikitier file in the given directory
// and matches the source path against its patterns.
func (tm *TierManager) resolveWikiTier(dir string, relPath string) (int, bool) {
	wikiTierPath := filepath.Join(dir, ".wikitier")
	data, err := os.ReadFile(wikiTierPath)
	if err != nil {
		return 0, false
	}

	var overrides map[string]int
	if err := yaml.Unmarshal(data, &overrides); err != nil {
		log.Warn(".wikitier parse error", "path", wikiTierPath, "error", err)
		return 0, false
	}

	baseName := filepath.Base(relPath)

	// Pass 1: exact filename match (highest priority)
	for pattern, tier := range overrides {
		if tier < 0 || tier > 3 {
			continue
		}
		// Exact match: no glob metacharacters
		if !strings.ContainsAny(pattern, "*?[") && pattern == baseName {
			return tier, true
		}
	}

	// Pass 2: glob pattern match
	for pattern, tier := range overrides {
		if tier < 0 || tier > 3 {
			continue
		}
		if !strings.ContainsAny(pattern, "*?[") {
			continue // already handled in pass 1
		}
		matched, err := filepath.Match(pattern, baseName)
		if err != nil {
			log.Warn(".wikitier invalid pattern", "pattern", pattern, "error", err)
			continue
		}
		if matched {
			return tier, true
		}
	}

	return 0, false
}

// toInt converts an interface value to int (handles float64 from JSON, int from YAML).
func toInt(v interface{}) (int, bool) {
	switch val := v.(type) {
	case int:
		return val, true
	case float64:
		return int(val), true
	case int64:
		return int(val), true
	}
	return 0, false
}
