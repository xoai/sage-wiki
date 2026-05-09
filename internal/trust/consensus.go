package trust

import (
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
)

func EmbedAndStoreQuestion(tx *sql.Tx, questionHash string, embedding []float32) error {
	blob := encodeFloat32s(embedding)
	_, err := tx.Exec(
		`INSERT OR REPLACE INTO pending_questions_vec (question_hash, embedding, dimensions) VALUES (?, ?, ?)`,
		questionHash, blob, len(embedding))
	return err
}

type SimilarQuestion struct {
	Output *PendingOutput
	Score  float64
}

func FindSimilarQuestion(tx *sql.Tx, questionVec []float32, threshold float64) (*SimilarQuestion, error) {
	rows, err := tx.Query(`SELECT pqv.question_hash, pqv.embedding, pqv.dimensions
		FROM pending_questions_vec pqv
		INNER JOIN pending_outputs po ON po.question_hash = pqv.question_hash
		WHERE po.state IN ('pending', 'confirmed')`)
	if err != nil {
		return nil, fmt.Errorf("trust: query question vecs: %w", err)
	}
	defer rows.Close()

	var bestHash string
	var bestScore float64

	for rows.Next() {
		var qHash string
		var blob []byte
		var dims int
		if err := rows.Scan(&qHash, &blob, &dims); err != nil {
			return nil, err
		}
		vec := decodeFloat32s(blob)
		if len(vec) != len(questionVec) {
			continue
		}
		score := cosineSimilarity(questionVec, vec)
		if score >= threshold && score > bestScore {
			bestScore = score
			bestHash = qHash
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	if bestHash == "" {
		return nil, nil
	}

	row := tx.QueryRow(`SELECT id, question, question_hash, answer, answer_hash,
		state, confirmations, grounding_score, sources_hash, sources_used,
		file_path, created_at, promoted_at, demoted_at
		FROM pending_outputs WHERE question_hash = ? AND state IN ('pending', 'confirmed')
		LIMIT 1`, bestHash)

	o, err := scanOutputFromTx(row)
	if err != nil {
		return nil, err
	}
	return &SimilarQuestion{Output: o, Score: bestScore}, nil
}

func CompareAnswers(answer1, answer2 string, embedder embed.Embedder, client *llm.Client, model string) (bool, error) {
	if embedder != nil {
		vec1, err1 := embedder.Embed(answer1)
		vec2, err2 := embedder.Embed(answer2)
		if err1 == nil && err2 == nil {
			sim := cosineSimilarity(vec1, vec2)
			if sim >= 0.9 {
				return true, nil
			}
			if sim < 0.7 {
				return false, nil
			}
		}
	}

	if client == nil {
		return false, nil
	}

	prompt := fmt.Sprintf(`Compare these two answers and determine if they state the same core facts.

Answer A:
%s

Answer B:
%s

Respond with exactly one word: "agree" if they state the same facts, "disagree" if they contradict each other or state different facts.`, answer1, answer2)

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: 16, Temperature: 0.01})
	if err != nil {
		return false, fmt.Errorf("trust: compare answers: %w", err)
	}

	verdict := strings.ToLower(strings.TrimSpace(resp.Content))
	return strings.Contains(verdict, "agree") && !strings.Contains(verdict, "disagree"), nil
}

func IndependenceScore(confirmations []*Confirmation) float64 {
	if len(confirmations) < 2 {
		return 0
	}

	var totalJaccard float64
	pairs := 0

	for i := 0; i < len(confirmations); i++ {
		setA := parseChunkIDs(confirmations[i].ChunkIDs)
		for j := i + 1; j < len(confirmations); j++ {
			setB := parseChunkIDs(confirmations[j].ChunkIDs)
			totalJaccard += jaccardDistance(setA, setB)
			pairs++
		}
	}

	if pairs == 0 {
		return 0
	}
	return totalJaccard / float64(pairs)
}

func ShouldAutoPromote(confirmations int, independence float64, threshold int) bool {
	if confirmations < threshold {
		return false
	}
	return independence > 0.3 || confirmations >= 2*threshold
}

func parseChunkIDs(jsonStr string) map[string]bool {
	set := map[string]bool{}
	var ids []string
	if err := json.Unmarshal([]byte(jsonStr), &ids); err == nil {
		for _, id := range ids {
			set[id] = true
		}
	}
	return set
}

func jaccardDistance(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 0
	}
	intersection := 0
	union := map[string]bool{}
	for k := range a {
		union[k] = true
		if b[k] {
			intersection++
		}
	}
	for k := range b {
		union[k] = true
	}
	if len(union) == 0 {
		return 0
	}
	return 1.0 - float64(intersection)/float64(len(union))
}

func cosineSimilarity(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}
	denom := math.Sqrt(normA) * math.Sqrt(normB)
	if denom == 0 {
		return 0
	}
	return dot / denom
}

func encodeFloat32s(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

func decodeFloat32s(buf []byte) []float32 {
	n := len(buf) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
	}
	return v
}

func scanOutputFromTx(row *sql.Row) (*PendingOutput, error) {
	return scanOutput(row)
}
