package compiler

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
)

// #128 T3: the gate suppresses source-less concepts entirely.
func TestFilterLowEvidence(t *testing.T) {
	concepts := []ExtractedConcept{
		{Name: "real", Sources: []string{"raw/a.md"}},
		{Name: "rap", Sources: nil},
		{Name: "wsp", Sources: []string{}},
	}
	kept, skipped := filterLowEvidence(concepts, 1)
	if len(kept) != 1 || kept[0].Name != "real" {
		t.Errorf("kept = %+v, want [real]", kept)
	}
	if len(skipped) != 2 {
		t.Errorf("skipped = %+v, want [rap wsp]", skipped)
	}
}

func TestFilterLowEvidenceDisabled(t *testing.T) {
	concepts := []ExtractedConcept{{Name: "rap", Sources: nil}}
	kept, skipped := filterLowEvidence(concepts, 0)
	if len(kept) != 1 || len(skipped) != 0 {
		t.Errorf("disabled gate must keep everything: kept=%+v skipped=%+v", kept, skipped)
	}
}

func TestFilterLowEvidenceStricterThreshold(t *testing.T) {
	concepts := []ExtractedConcept{
		{Name: "one", Sources: []string{"raw/a.md"}},
		{Name: "two", Sources: []string{"raw/a.md", "raw/b.md"}},
	}
	kept, skipped := filterLowEvidence(concepts, 2)
	if len(kept) != 1 || kept[0].Name != "two" || len(skipped) != 1 {
		t.Errorf("min=2: kept=%+v skipped=%+v", kept, skipped)
	}
}

// The full chain: dedup fold → gate → manifest, mirroring the call order at
// the three sites (gate runs BEFORE AddConcept).
func TestGateBeforeAddConcept(t *testing.T) {
	concepts := deduplicateConcepts([]ExtractedConcept{
		{Name: "remedial-action-plan", Aliases: []string{"rap"}, Sources: []string{"raw/a.md"}},
		{Name: "rap", Sources: nil}, // folds into remedial-action-plan
		{Name: "wsp", Sources: nil}, // stays, gets gated
	}, nil)
	kept, _ := filterLowEvidence(concepts, 1)
	m := manifest.New()
	for _, c := range kept {
		m.AddConcept(c.Name, "wiki/concepts/"+c.Name+".md", c.Sources, c.Aliases...)
	}
	if _, ok := m.Concepts["wsp"]; ok {
		t.Error("source-less concept reached the manifest")
	}
	if _, ok := m.Concepts["remedial-action-plan"]; !ok {
		t.Error("canonical concept missing from manifest")
	}
	if _, ok := m.Concepts["rap"]; ok {
		t.Error("acronym must not stand alone in the manifest")
	}
}
