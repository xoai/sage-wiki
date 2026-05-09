package trust

import (
	"testing"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/vectors"
)

func TestProcessOutputNewPending(t *testing.T) {
	db := setupTestDB(t)
	projectDir := t.TempDir()

	emb := &mockEmbedder{vec: []float32{0.1, 0.2, 0.3, 0.4}}

	result, err := ProcessOutput(ProcessOutputOpts{
		ProjectDir: projectDir,
		OutputDir:  "wiki",
		Question:   "What is Go?",
		Answer:     "Go is a language.",
		Sources:    []string{"wiki/concepts/go.md"},
		Embedder:   emb,
		Cfg:        config.TrustConfig{},
		DB:         db,
		Stores: IndexStores{
			MemStore: memory.NewStore(db),
			VecStore: vectors.NewStore(db),
			OntStore: ontology.NewStore(db, nil, nil),
		},
		UserNow: "2026-05-09",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "new" {
		t.Errorf("action = %q, want new", result.Action)
	}

	store := NewStore(db)
	got, err := store.Get(result.OutputID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StatePending {
		t.Errorf("state = %q, want pending", got.State)
	}
}

func TestProcessOutputConfirmation(t *testing.T) {
	db := setupTestDB(t)
	projectDir := t.TempDir()

	emb := &mockEmbedder{vec: []float32{0.5, 0.5, 0.0, 0.0}}

	ProcessOutput(ProcessOutputOpts{
		ProjectDir: projectDir,
		OutputDir:  "wiki",
		Question:   "What is Go?",
		Answer:     "Go is a language by Google.",
		Sources:    []string{"wiki/concepts/go.md"},
		Embedder:   emb,
		Cfg:        config.TrustConfig{},
		DB:         db,
		Stores: IndexStores{
			MemStore: memory.NewStore(db),
			VecStore: vectors.NewStore(db),
			OntStore: ontology.NewStore(db, nil, nil),
		},
		UserNow: "2026-05-09",
	})

	result, err := ProcessOutput(ProcessOutputOpts{
		ProjectDir: projectDir,
		OutputDir:  "wiki",
		Question:   "What is Go?",
		Answer:     "Go is a language by Google.",
		Sources:    []string{"wiki/concepts/go.md"},
		ChunksUsed: []string{"chunk1", "chunk2"},
		Embedder:   emb,
		Cfg:        config.TrustConfig{},
		DB:         db,
		Stores: IndexStores{
			MemStore: memory.NewStore(db),
			VecStore: vectors.NewStore(db),
			OntStore: ontology.NewStore(db, nil, nil),
		},
		UserNow: "2026-05-09",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "confirmed" {
		t.Errorf("action = %q, want confirmed", result.Action)
	}
}

func TestProcessOutputConflict(t *testing.T) {
	db := setupTestDB(t)
	projectDir := t.TempDir()

	callIdx := 0
	emb := &mockVaryingEmbedder{
		vecs: [][]float32{
			{0.5, 0.5, 0.0, 0.0},
			{0.5, 0.5, 0.0, 0.0},
			{0.5, 0.5, 0.0, 0.0},
			{1.0, 0.0, 0.0, 0.0},
			{0.0, 0.0, 0.0, 1.0},
		},
		idx: callIdx,
	}

	client, cleanup := mockLLMServer(t, func(body string) string {
		return "disagree"
	})
	defer cleanup()

	ProcessOutput(ProcessOutputOpts{
		ProjectDir: projectDir, OutputDir: "wiki",
		Question: "What is Go?", Answer: "Go is by Google.",
		Embedder: emb, Client: client, Model: "test",
		Cfg: config.TrustConfig{}, DB: db,
		Stores: IndexStores{
			MemStore: memory.NewStore(db), VecStore: vectors.NewStore(db),
			OntStore: ontology.NewStore(db, nil, nil),
		},
		UserNow: "2026-05-09",
	})

	result, err := ProcessOutput(ProcessOutputOpts{
		ProjectDir: projectDir, OutputDir: "wiki",
		Question: "What is Go?", Answer: "Go is by Microsoft.",
		Embedder: emb, Client: client, Model: "test",
		Cfg: config.TrustConfig{}, DB: db,
		Stores: IndexStores{
			MemStore: memory.NewStore(db), VecStore: vectors.NewStore(db),
			OntStore: ontology.NewStore(db, nil, nil),
		},
		UserNow: "2026-05-09",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Action != "conflict" {
		t.Errorf("action = %q, want conflict", result.Action)
	}
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"What is Go?", "what-is-go"},
		{"Hello World!", "hello-world"},
		{"", "output"},
		{"A--B  C", "a-b-c"},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
