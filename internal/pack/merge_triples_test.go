package pack

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xoai/sage-wiki/internal/config"
)

// A pack apply must not silently reset a user's ontology.triples block.
// MergeOntology returns a fresh literal, and mergeOntologyIntoMap replaces the
// whole `ontology:` node — so any field the literal omits is erased.
func TestMergeOntologyPreservesTriples(t *testing.T) {
	base := config.OntologyConfig{
		EntityTypes: []config.EntityTypeConfig{{Name: "pattern"}},
		Triples: config.TriplesConfig{
			Enabled:            true,
			Model:              "claude-haiku-4-5-20251001",
			MaxTokens:          8192,
			MaxEntitiesPerDoc:  25,
			MaxRelationsPerDoc: 40,
		},
	}
	overlay := config.OntologyConfig{
		RelationTypes: []config.RelationConfig{{Name: "refines", Synonyms: []string{"refines"}}},
	}

	got := MergeOntology(base, overlay)

	if got.Triples != base.Triples {
		t.Errorf("triples config lost or altered by a pack apply:\n got  %+v\n want %+v", got.Triples, base.Triples)
	}
	// The overlay's own contribution must still land.
	if len(got.RelationTypes) == 0 {
		t.Error("overlay relation types dropped")
	}
}

// The converse guard: a user who never configured triples must not have a
// zeroed block written into their config.yaml by a pack apply. This is what
// `yaml:"triples,omitempty"` on a struct VALUE buys — yaml.v3 elides a zero
// struct, unlike encoding/json.
func TestMergeOntologyDoesNotMaterializeEmptyTriples(t *testing.T) {
	merged := MergeOntology(
		config.OntologyConfig{EntityTypes: []config.EntityTypeConfig{{Name: "pattern"}}},
		config.OntologyConfig{RelationTypes: []config.RelationConfig{{Name: "refines"}}},
	)

	out, err := yaml.Marshal(merged)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "triples") {
		t.Errorf("a zero TriplesConfig was materialized into the config:\n%s", out)
	}
}
