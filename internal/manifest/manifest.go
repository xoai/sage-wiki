package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Manifest tracks sources, concepts, and their relationships.
type Manifest struct {
	Version  int                 `json:"version"`
	Sources  map[string]Source   `json:"sources"`
	Concepts map[string]Concept  `json:"concepts"`
	EmbedModel string            `json:"embed_model,omitempty"`
	EmbedDim   int               `json:"embed_dim,omitempty"`
}

// Source represents a tracked source file.
type Source struct {
	Hash             string   `json:"hash"`
	Type             string   `json:"type"`
	SizeBytes        int64    `json:"size_bytes"`
	AddedAt          string   `json:"added_at"`
	CompiledAt       string   `json:"compiled_at,omitempty"`
	SummaryPath      string   `json:"summary_path,omitempty"`
	ConceptsProduced []string `json:"concepts_produced,omitempty"`
	ChunkCount       int      `json:"chunk_count,omitempty"`
	Status           string   `json:"status"` // pending, compiled, error
}

// Concept represents a tracked concept.
type Concept struct {
	ArticlePath  string   `json:"article_path"`
	Sources      []string `json:"sources"`
	LastCompiled string   `json:"last_compiled"`
}

// New creates an empty manifest.
func New() *Manifest {
	return &Manifest{
		Version:  2,
		Sources:  make(map[string]Source),
		Concepts: make(map[string]Concept),
	}
}

// Load reads a manifest from disk.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return New(), nil
		}
		return nil, fmt.Errorf("manifest.Load: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest.Load: %w", err)
	}

	if m.Sources == nil {
		m.Sources = make(map[string]Source)
	}
	if m.Concepts == nil {
		m.Concepts = make(map[string]Concept)
	}

	return &m, nil
}

// Save writes the manifest to disk.
func (m *Manifest) Save(path string) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("manifest.Save: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// AddSource registers a new source file.
func (m *Manifest) AddSource(path string, hash string, typ string, size int64) {
	m.Sources[path] = Source{
		Hash:      hash,
		Type:      typ,
		SizeBytes: size,
		AddedAt:   time.Now().UTC().Format(time.RFC3339),
		Status:    "pending",
	}
}

// MarkCompiled marks a source as compiled.
func (m *Manifest) MarkCompiled(path string, summaryPath string, concepts []string) {
	if s, ok := m.Sources[path]; ok {
		s.CompiledAt = time.Now().UTC().Format(time.RFC3339)
		s.SummaryPath = summaryPath
		s.ConceptsProduced = concepts
		s.Status = "compiled"
		m.Sources[path] = s
	}
}

// RemoveSource removes a source entry.
func (m *Manifest) RemoveSource(path string) {
	delete(m.Sources, path)
}

// AddConcept registers a concept.
func (m *Manifest) AddConcept(name string, articlePath string, sources []string) {
	m.Concepts[name] = Concept{
		ArticlePath:  articlePath,
		Sources:      sources,
		LastCompiled: time.Now().UTC().Format(time.RFC3339),
	}
}

// PendingSources returns sources with status "pending".
func (m *Manifest) PendingSources() map[string]Source {
	pending := make(map[string]Source)
	for path, s := range m.Sources {
		if s.Status == "pending" {
			pending[path] = s
		}
	}
	return pending
}

// ArticlesFromSource returns concept names whose Sources list contains the given path.
func (m *Manifest) ArticlesFromSource(sourcePath string) []string {
	var names []string
	for name, c := range m.Concepts {
		for _, s := range c.Sources {
			if s == sourcePath {
				names = append(names, name)
				break
			}
		}
	}
	return names
}

// SourcesForArticle returns the source paths for a given concept name.
func (m *Manifest) SourcesForArticle(conceptName string) []string {
	if c, ok := m.Concepts[conceptName]; ok {
		return c.Sources
	}
	return nil
}

// SourceCount returns the total number of tracked sources.
func (m *Manifest) SourceCount() int {
	return len(m.Sources)
}

// ConceptCount returns the total number of tracked concepts.
func (m *Manifest) ConceptCount() int {
	return len(m.Concepts)
}
