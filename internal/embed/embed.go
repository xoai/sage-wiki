package embed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/log"
)

// Embedder generates vector embeddings from text.
type Embedder interface {
	Embed(text string) ([]float32, error)
	Dimensions() int
	Name() string
}

// Default embedding models per provider.
var defaultModels = map[string]string{
	"openai":  "text-embedding-3-small",
	"gemini":  "gemini-embedding-2-preview",
	"voyage":  "voyage-3-lite",
	"mistral": "mistral-embed",
}

// Default dimensions per model.
var defaultDimensions = map[string]int{
	"text-embedding-3-small":     1536,
	"gemini-embedding-2-preview": 768,
	"voyage-3-lite":              1024,
	"mistral-embed":              1024,
	"nomic-embed-text":           768,
}

const defaultOllamaBaseURL = "http://localhost:11434"
const defaultOllamaEmbedModel = "nomic-embed-text"

// NewForConfig creates an embedder using config-driven overrides when present.
func NewForConfig(cfg *config.Config) Embedder {
	if cfg == nil {
		return nil
	}
	return newWithConfig(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, cfg.Embed)
}

// NewCascade auto-detects the best available embedding provider.
// Tier 1: Provider embedding API (if available).
// Tier 2: Ollama local (if running).
// Returns nil if no embedding provider is available.
func NewCascade(provider string, apiKey string, baseURL string) Embedder {
	return newWithConfig(provider, apiKey, baseURL, nil)
}

func newWithConfig(apiProvider string, apiKey string, baseURL string, embedCfg *config.EmbedConfig) Embedder {
	if embedCfg == nil {
		return cascadeEmbedder(apiProvider, apiKey, baseURL)
	}

	provider := strings.TrimSpace(embedCfg.Provider)
	model := strings.TrimSpace(embedCfg.Model)
	dims := embedCfg.Dimensions

	if provider == "" {
		provider = "auto"
	}

	if provider == "auto" && model == "" && dims == 0 {
		return cascadeEmbedder(apiProvider, apiKey, baseURL)
	}

	if provider == "auto" {
		provider = autoEmbedProvider(apiProvider, apiKey, baseURL, model)
		if provider == "" {
			log.Warn("no embedding provider available â€” vector search disabled. Install Ollama or configure an embedding-capable provider.")
			return nil
		}
	}

	return newConfiguredEmbedder(provider, model, dims, apiProvider, apiKey, baseURL)
}

func cascadeEmbedder(provider string, apiKey string, baseURL string) Embedder {
	// Tier 1: Provider embedding API
	if model, ok := defaultModels[provider]; ok && apiKey != "" {
		return newAPIEmbedder(provider, model, defaultDimensions[model], apiKey, baseURL, 1, true)
	}

	// Tier 2: Ollama
	ollamaBaseURL := ResolveOllamaBaseURL(provider, baseURL)
	if ollamaAvailable(ollamaBaseURL) {
		return newOllamaEmbedder(defaultOllamaEmbedModel, defaultDimensions[defaultOllamaEmbedModel], ollamaBaseURL, 2, true)
	}

	log.Warn("no embedding provider available â€” vector search disabled. Install Ollama or configure an embedding-capable provider.")
	return nil
}

func autoEmbedProvider(apiProvider string, apiKey string, baseURL string, model string) string {
	if apiProvider == "openai-compatible" && model != "" && baseURL != "" {
		return apiProvider
	}
	if _, ok := defaultModels[apiProvider]; ok && apiKey != "" {
		return apiProvider
	}
	if ollamaAvailable(ResolveOllamaBaseURL(apiProvider, baseURL)) {
		return "ollama"
	}
	return ""
}

func newConfiguredEmbedder(provider string, model string, dims int, apiProvider string, apiKey string, baseURL string) Embedder {
	switch provider {
	case "ollama":
		if model == "" {
			model = defaultOllamaEmbedModel
		}
		if dims == 0 {
			dims = defaultDimensions[model]
		}

		ollamaBaseURL := defaultOllamaBaseURL
		if apiProvider == "ollama" {
			ollamaBaseURL = ResolveOllamaBaseURL(apiProvider, baseURL)
		}
		if !ollamaAvailable(ollamaBaseURL) {
			log.Warn("configured Ollama embedding provider unavailable", "base_url", ollamaBaseURL)
			return nil
		}
		return newOllamaEmbedder(model, dims, ollamaBaseURL, 0, false)
	case "openai", "gemini", "voyage", "mistral", "openai-compatible":
		if model == "" {
			model = defaultModels[provider]
		}
		if model == "" {
			log.Warn("embedding provider requires a model", "provider", provider)
			return nil
		}
		if dims == 0 {
			dims = defaultDimensions[model]
		}
		if provider == "openai-compatible" && baseURL == "" {
			log.Warn("openai-compatible embedding provider requires api.base_url")
			return nil
		}
		if provider != "openai-compatible" && apiKey == "" {
			log.Warn("embedding provider requires an API key", "provider", provider)
			return nil
		}
		return newAPIEmbedder(provider, model, dims, apiKey, baseURL, 0, false)
	default:
		log.Warn("unsupported embedding provider configured", "provider", provider)
		return nil
	}
}

func newAPIEmbedder(provider string, model string, dims int, apiKey string, baseURL string, tier int, detected bool) Embedder {
	if detected {
		log.Info("embedding provider detected", "tier", tier, "provider", provider, "model", model, "dims", dims)
	} else {
		log.Info("embedding provider configured", "provider", provider, "model", model, "dims", dims)
	}

	return &APIEmbedder{
		provider: provider,
		model:    model,
		apiKey:   apiKey,
		baseURL:  baseURL,
		dims:     dims,
	}
}

func newOllamaEmbedder(model string, dims int, baseURL string, tier int, detected bool) Embedder {
	if detected {
		log.Info("embedding provider detected", "tier", tier, "provider", "ollama", "model", model, "dims", dims)
	} else {
		log.Info("embedding provider configured", "provider", "ollama", "model", model, "dims", dims)
	}

	return &OllamaEmbedder{
		model:   model,
		dims:    dims,
		baseURL: baseURL,
	}
}

// ResolveOllamaBaseURL returns the Ollama base URL to use for the current
// provider. We only honor api.base_url when the configured provider is Ollama.
func ResolveOllamaBaseURL(provider string, baseURL string) string {
	if provider == "ollama" && baseURL != "" {
		return strings.TrimRight(baseURL, "/")
	}
	return defaultOllamaBaseURL
}

// OllamaAvailable probes the resolved Ollama endpoint for availability.
func OllamaAvailable(provider string, baseURL string) bool {
	return ollamaAvailable(ResolveOllamaBaseURL(provider, baseURL))
}

// APIEmbedder calls a provider's embedding API.
type APIEmbedder struct {
	provider string
	model    string
	apiKey   string
	baseURL  string
	dims     int
	client   http.Client
}

func (e *APIEmbedder) Name() string    { return fmt.Sprintf("%s/%s", e.provider, e.model) }
func (e *APIEmbedder) Dimensions() int { return e.dims }

func (e *APIEmbedder) Embed(text string) ([]float32, error) {
	if e.provider == "gemini" {
		return e.embedGemini(text)
	}
	return e.embedOpenAI(text)
}

// embedOpenAI uses the OpenAI-compatible /embeddings endpoint.
func (e *APIEmbedder) embedOpenAI(text string) ([]float32, error) {
	url := e.embeddingURL()

	body, _ := json.Marshal(map[string]any{
		"model": e.model,
		"input": text,
	})

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed: API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}

	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embed: empty embedding in response")
	}

	return result.Data[0].Embedding, nil
}

// embedGemini uses the Gemini-native /models/{model}:embedContent endpoint.
func (e *APIEmbedder) embedGemini(text string) ([]float32, error) {
	base := e.baseURL
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	url := fmt.Sprintf("%s/models/%s:embedContent?key=%s", base, e.model, e.apiKey)

	body, _ := json.Marshal(map[string]any{
		"model": "models/" + e.model,
		"content": map[string]any{
			"parts": []map[string]string{
				{"text": text},
			},
		},
	})

	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("embed: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embed: API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Embedding struct {
			Values []float32 `json:"values"`
		} `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("embed: decode response: %w", err)
	}

	if len(result.Embedding.Values) == 0 {
		return nil, fmt.Errorf("embed: empty embedding in response")
	}

	e.dims = len(result.Embedding.Values)
	return result.Embedding.Values, nil
}

func (e *APIEmbedder) embeddingURL() string {
	base := e.baseURL
	if base == "" {
		switch e.provider {
		case "openai":
			base = "https://api.openai.com/v1"
		case "voyage":
			base = "https://api.voyageai.com/v1"
		case "mistral":
			base = "https://api.mistral.ai/v1"
		}
	}
	return base + "/embeddings"
}

// OllamaEmbedder uses an Ollama instance.
type OllamaEmbedder struct {
	model   string
	dims    int
	baseURL string
	client  http.Client
}

func (e *OllamaEmbedder) Name() string    { return fmt.Sprintf("ollama/%s", e.model) }
func (e *OllamaEmbedder) Dimensions() int { return e.dims }

func (e *OllamaEmbedder) Embed(text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  e.model,
		"prompt": text,
	})

	baseURL := e.baseURL
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}

	resp, err := e.client.Post(baseURL+"/api/embeddings", "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("ollama embed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("ollama embed: %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Embedding []float32 `json:"embedding"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("ollama embed: decode: %w", err)
	}

	if len(result.Embedding) == 0 {
		return nil, fmt.Errorf("ollama embed: empty embedding")
	}

	e.dims = len(result.Embedding)
	return result.Embedding, nil
}

// ollamaAvailable probes the given endpoint for a running Ollama instance.
func ollamaAvailable(baseURL string) bool {
	if baseURL == "" {
		baseURL = defaultOllamaBaseURL
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
