package trust

import (
	"database/sql"
	"testing"
	"time"
)

func TestEmbedAndFindSimilarQuestion(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "What is Go?", QuestionHash: HashQuestion("What is Go?"),
		Answer: "Go is a language.", AnswerHash: HashAnswer("Go is a language."),
		State: StatePending, Confirmations: 1,
		FilePath: "p", CreatedAt: time.Now(),
	})

	vec := []float32{0.1, 0.2, 0.3, 0.4}

	db.WriteTx(func(tx *sql.Tx) error {
		return EmbedAndStoreQuestion(tx, HashQuestion("What is Go?"), vec)
	})

	var found *SimilarQuestion
	db.WriteTx(func(tx *sql.Tx) error {
		var err error
		found, err = FindSimilarQuestion(tx, vec, 0.9)
		return err
	})

	if found == nil {
		t.Fatal("expected to find similar question")
	}
	if found.Output.Question != "What is Go?" {
		t.Errorf("question = %q", found.Output.Question)
	}
	if found.Score < 0.99 {
		t.Errorf("score = %v, expected ~1.0 for identical vector", found.Score)
	}
}

func TestFindSimilarQuestionDissimilar(t *testing.T) {
	db := setupTestDB(t)
	store := NewStore(db)

	store.InsertPending(&PendingOutput{
		ID: "test.md", Question: "What is Go?", QuestionHash: HashQuestion("What is Go?"),
		Answer: "Go is a language.", AnswerHash: HashAnswer("Go is a language."),
		State: StatePending, Confirmations: 1,
		FilePath: "p", CreatedAt: time.Now(),
	})

	db.WriteTx(func(tx *sql.Tx) error {
		return EmbedAndStoreQuestion(tx, HashQuestion("What is Go?"), []float32{1, 0, 0, 0})
	})

	dissimilar := []float32{0, 0, 0, 1}
	var found *SimilarQuestion
	db.WriteTx(func(tx *sql.Tx) error {
		var err error
		found, err = FindSimilarQuestion(tx, dissimilar, 0.8)
		return err
	})

	if found != nil {
		t.Errorf("expected nil for dissimilar question, got score=%v", found.Score)
	}
}

func TestCompareAnswersHighSimilarity(t *testing.T) {
	agree, err := CompareAnswers("Go is a programming language.", "Go is a programming language.", nil, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if agree {
		t.Error("expected false when embedder is nil (can't compare)")
	}
}

func TestCompareAnswersWithMockEmbedder(t *testing.T) {
	emb := &mockEmbedder{vec: []float32{0.9, 0.1, 0.0, 0.0}}
	agree, err := CompareAnswers("answer A", "answer B", emb, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !agree {
		t.Error("expected agree for identical embeddings (sim=1.0)")
	}
}

func TestCompareAnswersDisagreeWithMockEmbedder(t *testing.T) {
	emb := &mockVaryingEmbedder{
		vecs: [][]float32{
			{1, 0, 0, 0},
			{0, 0, 0, 1},
		},
	}
	agree, err := CompareAnswers("answer A", "answer B", emb, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if agree {
		t.Error("expected disagree for orthogonal embeddings")
	}
}

func TestCompareAnswersMarginalTriggersLLM(t *testing.T) {
	emb := &mockVaryingEmbedder{
		vecs: [][]float32{
			{0.8, 0.2, 0.1, 0.0},
			{0.7, 0.3, 0.2, 0.0},
		},
	}
	client, cleanup := mockLLMServer(t, func(body string) string {
		return "agree"
	})
	defer cleanup()

	agree, err := CompareAnswers("answer A", "answer B", emb, client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if !agree {
		t.Error("expected agree from LLM fallback")
	}
}

func TestIndependenceScore(t *testing.T) {
	confs := []*Confirmation{
		{ChunkIDs: `["a","b","c"]`},
		{ChunkIDs: `["d","e","f"]`},
	}
	score := IndependenceScore(confs)
	if score != 1.0 {
		t.Errorf("score = %v, want 1.0 for completely disjoint chunk sets", score)
	}
}

func TestIndependenceScoreOverlapping(t *testing.T) {
	confs := []*Confirmation{
		{ChunkIDs: `["a","b","c"]`},
		{ChunkIDs: `["a","b","c"]`},
	}
	score := IndependenceScore(confs)
	if score != 0.0 {
		t.Errorf("score = %v, want 0.0 for identical chunk sets", score)
	}
}

func TestIndependenceScorePartialOverlap(t *testing.T) {
	confs := []*Confirmation{
		{ChunkIDs: `["a","b"]`},
		{ChunkIDs: `["b","c"]`},
	}
	score := IndependenceScore(confs)
	// intersection=1 (b), union=3 (a,b,c), jaccard_distance = 1 - 1/3 = 0.666...
	if score < 0.66 || score > 0.67 {
		t.Errorf("score = %v, want ~0.667", score)
	}
}

func TestShouldAutoPromote(t *testing.T) {
	if ShouldAutoPromote(1, 0.5, 3) {
		t.Error("should not promote with 1 < 3 confirmations")
	}
	if !ShouldAutoPromote(3, 0.5, 3) {
		t.Error("should promote with 3 confirmations and independence > 0.3")
	}
	if ShouldAutoPromote(3, 0.1, 3) {
		t.Error("should not promote with low independence and exactly threshold confirmations")
	}
	if !ShouldAutoPromote(6, 0.1, 3) {
		t.Error("should promote with 2x threshold confirmations even with low independence")
	}
}

type mockEmbedder struct {
	vec []float32
}

func (m *mockEmbedder) Embed(text string) ([]float32, error) { return m.vec, nil }
func (m *mockEmbedder) Dimensions() int                      { return len(m.vec) }
func (m *mockEmbedder) Name() string                         { return "mock" }

type mockVaryingEmbedder struct {
	vecs [][]float32
	idx  int
}

func (m *mockVaryingEmbedder) Embed(text string) ([]float32, error) {
	v := m.vecs[m.idx%len(m.vecs)]
	m.idx++
	return v, nil
}
func (m *mockVaryingEmbedder) Dimensions() int { return len(m.vecs[0]) }
func (m *mockVaryingEmbedder) Name() string    { return "mock-varying" }
