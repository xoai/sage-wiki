package ontology

import (
	"github.com/xoai/sage-wiki/internal/config"
)

// DefaultRelationTypeDefs returns the built-in relation types matching
// the original hardcoded constants.
func DefaultRelationTypeDefs() []config.RelationTypeDef {
	return []config.RelationTypeDef{
		{Name: RelImplements},
		{Name: RelExtends},
		{Name: RelOptimizes},
		{Name: RelContradicts},
		{Name: RelCites},
		{Name: RelPrerequisiteOf},
		{Name: RelTradesOff},
		{Name: RelDerivedFrom},
	}
}

// ValidRelation checks if a relation string matches any configured type
// (by name or synonym).
func ValidRelation(relation string, configured []config.RelationTypeDef) bool {
	for _, rt := range configured {
		if relation == rt.Name {
			return true
		}
		for _, syn := range rt.Synonyms {
			if relation == syn {
				return true
			}
		}
	}
	return false
}

// NormalizeRelation maps a synonym to its canonical relation name.
// Returns the input unchanged if no match is found.
func NormalizeRelation(relation string, configured []config.RelationTypeDef) string {
	for _, rt := range configured {
		if relation == rt.Name {
			return rt.Name
		}
		for _, syn := range rt.Synonyms {
			if relation == syn {
				return rt.Name
			}
		}
	}
	return relation
}
