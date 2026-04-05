package wiki

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDoctorUsesConfiguredOllamaBaseURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{
				"models": []map[string]any{{"name": "gemma4:e4b-it-q8_0"}},
			})
		case "/v1/chat/completions":
			json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"content": "ok"}},
				},
				"model": "gemma4:e4b-it-q8_0",
				"usage": map[string]any{"total_tokens": 3},
			})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer server.Close()

	dir := t.TempDir()
	if err := InitGreenfield(dir, "doctor-ollama"); err != nil {
		t.Fatalf("InitGreenfield: %v", err)
	}

	cfg := `project: doctor-ollama
api:
  provider: ollama
  base_url: ` + server.URL + `
models:
  summarize: gemma4:e4b-it-q8_0
`
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	result := RunDoctor(dir)

	var ollamaMessage string
	var connectivityOK bool
	for _, check := range result.Checks {
		if check.Name == "ollama" {
			ollamaMessage = check.Message
		}
		if check.Name == "connectivity" && check.Status == "ok" {
			connectivityOK = true
		}
	}

	if !connectivityOK {
		t.Fatalf("expected connectivity check to succeed, got %+v", result.Checks)
	}
	if !strings.Contains(ollamaMessage, server.URL) {
		t.Fatalf("expected ollama message to mention %q, got %q", server.URL, ollamaMessage)
	}
}
