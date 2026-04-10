package linter

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// --- Completeness Pass ---

type CompletenessPass struct{}

func (p *CompletenessPass) Name() string       { return "completeness" }
func (p *CompletenessPass) CanAutoFix() bool    { return false }
func (p *CompletenessPass) Fix(_ *LintContext, _ []Finding) error { return nil }

func (p *CompletenessPass) Run(ctx *LintContext) ([]Finding, error) {
	var findings []Finding

	// Scan articles for [[wikilinks]] that point to non-existent files
	conceptsDir := filepath.Join(ctx.ProjectDir, ctx.OutputDir, "concepts")
	entries, err := os.ReadDir(conceptsDir)
	if err != nil {
		return nil, nil // no concepts dir yet
	}

	linkRe := regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	existingFiles := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".md")
		existingFiles[name] = true
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(conceptsDir, e.Name()))
		if err != nil {
			continue
		}

		matches := linkRe.FindAllStringSubmatch(string(data), -1)
		for _, m := range matches {
			linkTarget := m[1]
			if !existingFiles[linkTarget] {
				findings = append(findings, Finding{
					Pass:     "completeness",
					Severity: SevWarning,
					Path:     filepath.Join(ctx.OutputDir, "concepts", e.Name()),
					Message:  fmt.Sprintf("broken [[%s]] — no article exists", linkTarget),
				})
			}
		}
	}

	return findings, nil
}

// --- Style Pass ---

type StylePass struct{}

func (p *StylePass) Name() string       { return "style" }
func (p *StylePass) CanAutoFix() bool    { return true }

func (p *StylePass) Run(ctx *LintContext) ([]Finding, error) {
	var findings []Finding

	conceptsDir := filepath.Join(ctx.ProjectDir, ctx.OutputDir, "concepts")
	entries, _ := os.ReadDir(conceptsDir)

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(conceptsDir, e.Name()))
		if err != nil {
			continue
		}

		content := string(data)

		// Check for YAML frontmatter
		if !strings.HasPrefix(content, "---") {
			findings = append(findings, Finding{
				Pass:     "style",
				Severity: SevWarning,
				Path:     filepath.Join(ctx.OutputDir, "concepts", e.Name()),
				Message:  "missing YAML frontmatter",
				Fix:      "add frontmatter",
			})
		}

		// Check for concept field in frontmatter
		if strings.HasPrefix(content, "---") && !strings.Contains(content[:min(500, len(content))], "concept:") {
			findings = append(findings, Finding{
				Pass:     "style",
				Severity: SevInfo,
				Path:     filepath.Join(ctx.OutputDir, "concepts", e.Name()),
				Message:  "frontmatter missing 'concept' field",
			})
		}
	}

	return findings, nil
}

func (p *StylePass) Fix(ctx *LintContext, findings []Finding) error {
	// Auto-fix: add missing frontmatter
	for _, f := range findings {
		if f.Fix == "add frontmatter" {
			path := filepath.Join(ctx.ProjectDir, f.Path)
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			name := strings.TrimSuffix(filepath.Base(f.Path), ".md")
			fm := fmt.Sprintf("---\nconcept: %s\nconfidence: low\n---\n\n", name)
			os.WriteFile(path, []byte(fm+string(data)), 0644)
		}
	}
	return nil
}

// --- Orphans Pass ---

type OrphansPass struct{}

func (p *OrphansPass) Name() string       { return "orphans" }
func (p *OrphansPass) CanAutoFix() bool    { return false }
func (p *OrphansPass) Fix(_ *LintContext, _ []Finding) error { return nil }

func (p *OrphansPass) Run(ctx *LintContext) ([]Finding, error) {
	var findings []Finding

	if ctx.DB == nil {
		return nil, nil
	}

	ontStore := ontology.NewStore(ctx.DB, ctx.ValidRelations)

	entities, err := ontStore.ListEntities("")
	if err != nil {
		return nil, err
	}

	for _, e := range entities {
		if e.Type == "source" {
			continue // sources don't need backlinks
		}

		rels, err := ontStore.GetRelations(e.ID, ontology.Both, "")
		if err != nil {
			continue
		}

		if len(rels) == 0 {
			findings = append(findings, Finding{
				Pass:     "orphans",
				Severity: SevInfo,
				Path:     e.ArticlePath,
				Message:  fmt.Sprintf("orphan entity %q — no relations", e.Name),
			})
		}
	}

	return findings, nil
}

// --- Consistency Pass ---

type ConsistencyPass struct{}

func (p *ConsistencyPass) Name() string       { return "consistency" }
func (p *ConsistencyPass) CanAutoFix() bool    { return false }
func (p *ConsistencyPass) Fix(_ *LintContext, _ []Finding) error { return nil }

func (p *ConsistencyPass) Run(ctx *LintContext) ([]Finding, error) {
	var findings []Finding

	if ctx.DB == nil {
		return nil, nil
	}

	// Find contradicts edges
	rows, err := ctx.DB.ReadDB().Query(
		"SELECT source_id, target_id FROM relations WHERE relation='contradicts'",
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var src, tgt string
		rows.Scan(&src, &tgt)
		findings = append(findings, Finding{
			Pass:     "consistency",
			Severity: SevWarning,
			Message:  fmt.Sprintf("contradiction: %s contradicts %s", src, tgt),
		})
	}

	return findings, nil
}

// --- Connections Pass ---

type ConnectionsPass struct{}

func (p *ConnectionsPass) Name() string       { return "connections" }
func (p *ConnectionsPass) CanAutoFix() bool    { return false }
func (p *ConnectionsPass) Fix(_ *LintContext, _ []Finding) error { return nil }

func (p *ConnectionsPass) Run(ctx *LintContext) ([]Finding, error) {
	var findings []Finding

	if ctx.DB == nil {
		return nil, nil
	}

	vecStore := vectors.NewStore(ctx.DB)
	ontStore := ontology.NewStore(ctx.DB, ctx.ValidRelations)

	// Get all concept entities with vectors
	concepts, err := ontStore.ListEntities("concept")
	if err != nil || len(concepts) < 2 {
		return nil, nil
	}

	// For each concept with a vector, find similar concepts via cosine similarity
	const similarityThreshold = 0.7
	for _, a := range concepts {
		// Get this concept's vector
		aID := "concept:" + a.ID
		aResults, err := vecStore.Search(nil, 0)
		_ = aResults
		// We need the raw vector — read from DB directly
		var aVec []byte
		var aDims int
		err = ctx.DB.ReadDB().QueryRow("SELECT embedding, dimensions FROM vec_entries WHERE id=?", aID).Scan(&aVec, &aDims)
		if err != nil {
			continue // no vector for this concept
		}

		// Search for similar vectors
		aFloat := decodeFloat32s(aVec)
		results, err := vecStore.Search(aFloat, 10)
		if err != nil {
			continue
		}

		for _, r := range results {
			// Skip self and non-concept entries
			if r.ID == aID || r.Score < similarityThreshold {
				continue
			}

			// Extract concept ID from vector ID
			bConceptID := r.ID
			if len(bConceptID) > 8 && bConceptID[:8] == "concept:" {
				bConceptID = bConceptID[8:]
			}

			// Check if ontology edge exists
			rels, _ := ontStore.GetRelations(a.ID, ontology.Both, "")
			hasEdge := false
			for _, rel := range rels {
				if rel.TargetID == bConceptID || rel.SourceID == bConceptID {
					hasEdge = true
					break
				}
			}

			if !hasEdge {
				findings = append(findings, Finding{
					Pass:     "connections",
					Severity: SevInfo,
					Message:  fmt.Sprintf("high similarity (%.2f) between %q and %q but no ontology edge", r.Score, a.Name, bConceptID),
				})
			}
		}
	}

	return findings, nil
}

// decodeFloat32s converts a BLOB to float32 slice (duplicated from vectors package to avoid circular import).
func decodeFloat32s(buf []byte) []float32 {
	v := make([]float32, len(buf)/4)
	for i := range v {
		bits := uint32(buf[i*4]) | uint32(buf[i*4+1])<<8 | uint32(buf[i*4+2])<<16 | uint32(buf[i*4+3])<<24
		v[i] = math.Float32frombits(bits)
	}
	return v
}

// --- Impute Pass ---

type ImputePass struct{}

func (p *ImputePass) Name() string       { return "impute" }
func (p *ImputePass) CanAutoFix() bool    { return false }
func (p *ImputePass) Fix(_ *LintContext, _ []Finding) error { return nil }

func (p *ImputePass) Run(ctx *LintContext) ([]Finding, error) {
	var findings []Finding

	conceptsDir := filepath.Join(ctx.ProjectDir, ctx.OutputDir, "concepts")
	entries, _ := os.ReadDir(conceptsDir)

	todoRe := regexp.MustCompile(`(?i)\[TODO\]|\[UNKNOWN\]|\[TBD\]`)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, _ := os.ReadFile(filepath.Join(conceptsDir, e.Name()))
		content := string(data)

		matches := todoRe.FindAllString(content, -1)
		if len(matches) > 0 {
			findings = append(findings, Finding{
				Pass:     "impute",
				Severity: SevWarning,
				Path:     filepath.Join(ctx.OutputDir, "concepts", e.Name()),
				Message:  fmt.Sprintf("contains %d placeholder(s): %s", len(matches), strings.Join(matches, ", ")),
			})
		}

		// Check for thin sections (< 50 chars after heading)
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "## ") && i+1 < len(lines) {
				section := ""
				for j := i + 1; j < len(lines) && !strings.HasPrefix(lines[j], "## "); j++ {
					section += lines[j]
				}
				if len(strings.TrimSpace(section)) < 50 {
					findings = append(findings, Finding{
						Pass:     "impute",
						Severity: SevInfo,
						Path:     filepath.Join(ctx.OutputDir, "concepts", e.Name()),
						Message:  fmt.Sprintf("thin section: %s (< 50 chars)", line),
					})
				}
			}
		}
	}

	return findings, nil
}

// --- Staleness Pass ---

type StalenessPass struct{}

func (p *StalenessPass) Name() string       { return "staleness" }
func (p *StalenessPass) CanAutoFix() bool    { return false }
func (p *StalenessPass) Fix(_ *LintContext, _ []Finding) error { return nil }

func (p *StalenessPass) Run(ctx *LintContext) ([]Finding, error) {
	var findings []Finding

	// Check article modification dates
	conceptsDir := filepath.Join(ctx.ProjectDir, ctx.OutputDir, "concepts")
	entries, _ := os.ReadDir(conceptsDir)

	threshold := 90 * 24 * time.Hour // 90 days

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		age := time.Since(info.ModTime())
		if age > threshold {
			findings = append(findings, Finding{
				Pass:     "staleness",
				Severity: SevInfo,
				Path:     filepath.Join(ctx.OutputDir, "concepts", e.Name()),
				Message:  fmt.Sprintf("article is %d days old", int(age.Hours()/24)),
			})
		}
	}

	return findings, nil
}
