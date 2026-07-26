package pack

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/xoai/sage-wiki/internal/config"
)

// A pack apply must not silently reset a user's ontology.resolve block.
// MergeOntology returns a fresh literal and mergeOntologyIntoMap replaces the
// whole `ontology:` node, so any field the literal omits is erased — the same
// defect that was fixed for triples.
func TestMergeOntologyPreservesResolve(t *testing.T) {
	base := config.OntologyConfig{
		EntityTypes: []config.EntityTypeConfig{{Name: "pattern"}},
		Resolve: config.ResolveConfig{
			Enabled:            true,
			Model:              "claude-haiku-4-5-20251001",
			MaxTokens:          8192,
			MaxBlockSize:       40,
			AutoApplyThreshold: 0.92,
			MaxTokenDF:         0.03,
			MinTokenDFFloor:    15,
			UseEmbeddings:      true,
			EmbedThreshold:     0.88,
			MaxEmbedCandidates: 250,
		},
	}
	overlay := config.OntologyConfig{
		RelationTypes: []config.RelationConfig{{Name: "refines", Synonyms: []string{"refines"}}},
	}

	got := MergeOntology(base, overlay)

	if got.Resolve != base.Resolve {
		t.Errorf("resolve config lost or altered by a pack apply:\n got  %+v\n want %+v",
			got.Resolve, base.Resolve)
	}
	if len(got.RelationTypes) == 0 {
		t.Error("overlay relation types dropped")
	}
}

// The converse: a user who never configured resolve must not get a zeroed block
// written into their config.yaml. That is what `yaml:"resolve,omitempty"` on a
// struct VALUE buys — yaml.v3 elides a zero struct, unlike encoding/json.
func TestMergeOntologyDoesNotMaterializeEmptyResolve(t *testing.T) {
	got := MergeOntology(config.OntologyConfig{}, config.OntologyConfig{
		RelationTypes: []config.RelationConfig{{Name: "refines"}},
	})

	out, err := yaml.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "resolve:") {
		t.Errorf("a zero resolve block was materialized into the config:\n%s", out)
	}
}
