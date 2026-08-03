package trust

import (
	"context"
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

	// P2-4: schema-guaranteed JSON where the provider supports it;
	// RawFallback keeps this site's exact no-bracket-hunt parse tolerance.
	payload, rawText, err := client.StructuredCompletion(context.Background(), []llm.Message{
		{Role: "user", Content: prompt},
	}, ClaimsSchema, llm.CallOpts{Model: model, MaxTokens: 1024, Temperature: llm.Float64(0.01), RawFallback: true})
	if err != nil {
		return nil, fmt.Errorf("trust: extract claims: %w", err)
	}

	content := string(payload)
	if rawText != "" {
		content = strings.TrimSpace(rawText)
		content = strings.TrimPrefix(content, "```json")
		content = strings.TrimPrefix(content, "```")
		content = strings.TrimSuffix(content, "```")
		content = strings.TrimSpace(content)
	}

	var claims []Claim
	if err := json.Unmarshal([]byte(content), &claims); err != nil {
		return nil, fmt.Errorf("trust: parse claims JSON: %w (raw: %s)", err, content)
	}
	return claims, nil
}

// ClaimsSchema is the canonical schema for claim extraction (P2-4).
// minItems: 0 — "no factual claims" is a legitimate answer, not a failure.
var ClaimsSchema = llm.JSONSchema{
	Name:        "claims",
	Description: "factual claims extracted from the answer",
	IsArray:     true,
	Schema: map[string]any{
		"type": "array",
		"items": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"text": map[string]any{"type": "string"},
			},
			"required": []string{"text"},
		},
		"minItems": 0,
	},
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
	}, llm.CallOpts{Model: model, MaxTokens: 16, Temperature: llm.Float64(0.01)})
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
