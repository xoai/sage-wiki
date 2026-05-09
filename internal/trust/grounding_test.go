package trust

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/llm"
)

func mockLLMServer(t *testing.T, handler func(body string) string) (*llm.Client, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		response := handler(string(b))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{"message": map[string]string{"role": "assistant", "content": response}},
			},
			"usage": map[string]int{"prompt_tokens": 10, "completion_tokens": 5},
		})
	}))
	client, err := llm.NewClient("openai", "test-key", srv.URL+"/v1", -1)
	if err != nil {
		t.Fatal(err)
	}
	return client, srv.Close
}

func TestExtractClaims(t *testing.T) {
	client, cleanup := mockLLMServer(t, func(body string) string {
		return `[{"text": "Go was created by Google"}, {"text": "Go was released in 2009"}]`
	})
	defer cleanup()

	claims, err := ExtractClaims("Go was created by Google and released in 2009.", client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 2 {
		t.Fatalf("expected 2 claims, got %d", len(claims))
	}
	if claims[0].Text != "Go was created by Google" {
		t.Errorf("claim[0] = %q", claims[0].Text)
	}
}

func TestExtractClaimsEmpty(t *testing.T) {
	client, cleanup := mockLLMServer(t, func(body string) string {
		return `[]`
	})
	defer cleanup()

	claims, err := ExtractClaims("I think maybe something.", client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if len(claims) != 0 {
		t.Errorf("expected 0 claims, got %d", len(claims))
	}
}

func TestCheckEntailmentGrounded(t *testing.T) {
	client, cleanup := mockLLMServer(t, func(body string) string {
		return "grounded"
	})
	defer cleanup()

	score, err := CheckEntailment("Go was created by Google", "Go is a programming language created by Google.", client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if score != ScoreGrounded {
		t.Errorf("score = %v, want grounded (1.0)", score)
	}
}

func TestCheckEntailmentInferred(t *testing.T) {
	client, cleanup := mockLLMServer(t, func(body string) string {
		return "inferred"
	})
	defer cleanup()

	score, err := CheckEntailment("Go is popular", "Go is used by many companies.", client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if score != ScoreInferred {
		t.Errorf("score = %v, want inferred (0.5)", score)
	}
}

func TestCheckEntailmentUngrounded(t *testing.T) {
	client, cleanup := mockLLMServer(t, func(body string) string {
		return "ungrounded"
	})
	defer cleanup()

	score, err := CheckEntailment("Go was created by Microsoft", "Go is a programming language created by Google.", client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if score != ScoreUngrounded {
		t.Errorf("score = %v, want ungrounded (0.0)", score)
	}
}

func TestComputeGroundingScore(t *testing.T) {
	callCount := 0
	client, cleanup := mockLLMServer(t, func(body string) string {
		callCount++
		if callCount == 1 {
			return `[{"text": "claim A"}, {"text": "claim B"}]`
		}
		if strings.Contains(body, "claim A") {
			return "grounded"
		}
		return "ungrounded"
	})
	defer cleanup()

	score, err := ComputeGroundingScore("answer text", []string{"source passage"}, client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	// 1 grounded (1.0) + 1 ungrounded (0.0) = 0.5
	if score != 0.5 {
		t.Errorf("score = %v, want 0.5", score)
	}
}

func TestComputeGroundingScoreNoClaims(t *testing.T) {
	client, cleanup := mockLLMServer(t, func(body string) string {
		return `[]`
	})
	defer cleanup()

	score, err := ComputeGroundingScore("no factual claims here", []string{"source"}, client, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	if score != 1.0 {
		t.Errorf("score = %v, want 1.0 for no claims", score)
	}
}
