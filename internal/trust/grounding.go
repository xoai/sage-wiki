package trust

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/xoai/sage-wiki/internal/llm"
)

type Claim struct {
	Text string `json:"text"`
}

type EntailmentScore float64

const (
	ScoreGrounded   EntailmentScore = 1.0
	ScoreInferred   EntailmentScore = 0.5
	ScoreUngrounded EntailmentScore = 0.0
)

func ExtractClaims(answer string, client *llm.Client, model string) ([]Claim, error) {
	prompt := `Extract the distinct factual claims from this answer. Return a JSON array of objects with a "text" field for each claim. Only include verifiable factual statements, not opinions or hedging language. If there are no factual claims, return an empty array.

Answer:
` + answer + `

Respond with ONLY valid JSON, no markdown fencing.`

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: 1024, Temperature: 0.01})
	if err != nil {
		return nil, fmt.Errorf("trust: extract claims: %w", err)
	}

	content := strings.TrimSpace(resp.Content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	content = strings.TrimSpace(content)

	var claims []Claim
	if err := json.Unmarshal([]byte(content), &claims); err != nil {
		return nil, fmt.Errorf("trust: parse claims JSON: %w (raw: %s)", err, content)
	}
	return claims, nil
}

func CheckEntailment(claim string, passage string, client *llm.Client, model string) (EntailmentScore, error) {
	prompt := fmt.Sprintf(`Given this source passage and a claim, determine if the passage supports the claim.

Source passage:
%s

Claim:
%s

Respond with exactly one word:
- "grounded" if the passage directly states or clearly supports the claim
- "inferred" if the passage partially supports the claim or it can be reasonably inferred
- "ungrounded" if the passage does not support the claim or contradicts it`, passage, claim)

	resp, err := client.ChatCompletion([]llm.Message{
		{Role: "user", Content: prompt},
	}, llm.CallOpts{Model: model, MaxTokens: 16, Temperature: 0.01})
	if err != nil {
		return ScoreUngrounded, fmt.Errorf("trust: check entailment: %w", err)
	}

	verdict := strings.ToLower(strings.TrimSpace(resp.Content))
	switch {
	case strings.Contains(verdict, "grounded") && !strings.Contains(verdict, "ungrounded"):
		return ScoreGrounded, nil
	case strings.Contains(verdict, "inferred"):
		return ScoreInferred, nil
	default:
		return ScoreUngrounded, nil
	}
}

func ComputeGroundingScore(answer string, sources []string, client *llm.Client, model string) (float64, error) {
	claims, err := ExtractClaims(answer, client, model)
	if err != nil {
		return 0, err
	}
	if len(claims) == 0 {
		return 1.0, nil
	}

	allPassages := strings.Join(sources, "\n\n---\n\n")

	var total float64
	for _, claim := range claims {
		score, err := CheckEntailment(claim.Text, allPassages, client, model)
		if err != nil {
			return 0, err
		}
		total += float64(score)
	}

	return total / float64(len(claims)), nil
}
