package ontology

import "github.com/xoai/sage-wiki/internal/config"

// RelationDef defines a relation type with its keyword synonyms.
type RelationDef struct {
	Name     string
	Synonyms []string
}

// RelationPattern maps keywords to a relation type for extraction.
type RelationPattern struct {
	Keywords []string
	Relation string
}

// BuiltinRelations defines the 8 immutable relation types with default synonyms.
var BuiltinRelations = []RelationDef{
	{Name: RelImplements, Synonyms: []string{"implements", "implementation of", "is an implementation", "实现了", "实现方式"}},
	{Name: RelExtends, Synonyms: []string{"extends", "extension of", "builds on", "builds upon", "扩展了", "基于"}},
	{Name: RelOptimizes, Synonyms: []string{"optimizes", "optimization of", "improves upon", "faster than", "优化了", "改进了", "提升了"}},
	{Name: RelContradicts, Synonyms: []string{"contradicts", "conflicts with", "disagrees with", "challenges", "矛盾", "冲突", "挑战了"}},
	{Name: RelCites, Synonyms: nil},           // created programmatically, not by keyword extraction
	{Name: RelPrerequisiteOf, Synonyms: []string{"prerequisite", "requires knowledge of", "depends on", "built on top of", "前提", "依赖于", "前置条件"}},
	{Name: RelTradesOff, Synonyms: []string{"trade-off", "tradeoff", "trades off", "at the cost of", "取舍", "权衡", "代价是"}},
	{Name: RelDerivedFrom, Synonyms: nil},      // created programmatically by query system
}

// MergedRelations merges user config with built-in defaults.
// For built-in types: config synonyms are appended (deduplicated).
// For new types: a new RelationDef is created.
// Built-in types are always present even if not in config.
func MergedRelations(cfgRelations []config.RelationConfig) []RelationDef {
	// Start with copies of builtins
	result := make([]RelationDef, len(BuiltinRelations))
	for i, b := range BuiltinRelations {
		syns := make([]string, len(b.Synonyms))
		copy(syns, b.Synonyms)
		result[i] = RelationDef{Name: b.Name, Synonyms: syns}
	}

	builtinIdx := make(map[string]int, len(result))
	for i, r := range result {
		builtinIdx[r.Name] = i
	}

	for _, cr := range cfgRelations {
		if idx, ok := builtinIdx[cr.Name]; ok {
			// Extend built-in with additional synonyms (deduplicated)
			existing := make(map[string]bool, len(result[idx].Synonyms))
			for _, s := range result[idx].Synonyms {
				existing[s] = true
			}
			for _, s := range cr.Synonyms {
				if !existing[s] {
					result[idx].Synonyms = append(result[idx].Synonyms, s)
					existing[s] = true
				}
			}
		} else {
			// New custom type
			syns := make([]string, len(cr.Synonyms))
			copy(syns, cr.Synonyms)
			result = append(result, RelationDef{Name: cr.Name, Synonyms: syns})
		}
	}

	return result
}

// ValidRelationNames returns the names from a merged relation list.
func ValidRelationNames(defs []RelationDef) []string {
	names := make([]string, len(defs))
	for i, d := range defs {
		names[i] = d.Name
	}
	return names
}

// RelationPatterns builds extraction patterns, skipping types with empty synonyms.
func RelationPatterns(defs []RelationDef) []RelationPattern {
	var patterns []RelationPattern
	for _, d := range defs {
		if len(d.Synonyms) == 0 {
			continue
		}
		patterns = append(patterns, RelationPattern{
			Keywords: d.Synonyms,
			Relation: d.Name,
		})
	}
	return patterns
}
