package embed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

// EmbedOverride holds optional overrides from the embed config block.
type EmbedOverride struct {
	Provider   string
	Model      string
	Dimensions int
	APIKey     string
	BaseURL    string
}

// NewFromConfig creates an Embedder from the project config, using embed
// overrides when present and falling back to auto-detection from api config.
func NewFromConfig(cfg *config.Config) Embedder {
	var ov *EmbedOverride
	if cfg.Embed != nil {
		ov = &EmbedOverride{
			Provider:   cfg.Embed.Provider,
			Model:      cfg.Embed.Model,
			Dimensions: cfg.Embed.Dimensions,
			APIKey:     cfg.Embed.APIKey,
			BaseURL:    cfg.Embed.BaseURL,
		}
	}
	return NewCascade(cfg.API.Provider, cfg.API.APIKey, cfg.API.BaseURL, ov)
}

// NewCascade auto-detects the best available embedding provider.
// Tier 0: Explicit embed config override (if model + credentials provided).
// Tier 1: Provider embedding API (if available).
// Tier 2: Ollama local (if running).
// Returns nil if no embedding provider is available.
func NewCascade(provider string, apiKey string, baseURL string, override *EmbedOverride) Embedder {
	// Tier 0: Explicit embed config — user specified model/credentials
	if override != nil && override.Model != "" {
		p := override.Provider
		if p == "" {
			p = provider
		}
		key := override.APIKey
		if key == "" {
			key = apiKey
		}
		url := override.BaseURL
		if url == "" {
			url = baseURL
		}
		if key != "" {
			dims := override.Dimensions
			if dims == 0 {
				dims = defaultDimensions[override.Model]
			}
			// dims may be 0 for unknown models — will auto-detect from first response
			embedder := &APIEmbedder{
				provider: p,
				model:    override.Model,
				apiKey:   key,
				baseURL:  url,
				dims:     dims,
				client:   newEmbedHTTPClient(),
			}
			if dims > 0 {
				log.Info("embedding provider detected", "tier", 0, "provider", p, "model", override.Model, "dims", dims)
			} else {
				log.Info("embedding provider detected", "tier", 0, "provider", p, "model", override.Model, "dims", "auto-detect")
			}
			return embedder
		}
	}

	// Tier 1: Provider embedding API
	if model, ok := defaultModels[provider]; ok && apiKey != "" {
		dims := defaultDimensions[model]
		embedder := &APIEmbedder{
			provider: provider,
			model:    model,
			apiKey:   apiKey,
			baseURL:  baseURL,
			dims:     dims,
			client:   newEmbedHTTPClient(),
		}
		log.Info("embedding provider detected", "tier", 1, "provider", provider, "model", model, "dims", dims)
		return embedder
	}

	// Tier 2: Ollama local
	if ollamaAvailable() {
		log.Info("embedding provider detected", "tier", 2, "provider", "ollama", "model", "nomic-embed-text", "dims", 768)
		return &OllamaEmbedder{
			model:  "nomic-embed-text",
			dims:   768,
			client: newEmbedHTTPClient(),
		}
	}

	log.Warn("no embedding provider available — vector search disabled. Install Ollama or configure an embedding-capable provider.")
	return nil
}

// sharedEmbedTransport is a process-wide HTTP transport for embedding API
// calls. It overrides http.DefaultTransport's MaxIdleConnsPerHost=2 — which
// causes TCP/TLS churn when Pass-3 write.go and Tier-1 index.go fire many
// concurrent Embed() calls at a single embedding endpoint. Mirrors the fix in
// internal/llm/client.go:sharedTransport (PER-116).
var sharedEmbedTransport http.RoundTripper = func() http.RoundTripper {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 512
	tr.MaxIdleConnsPerHost = 256
	tr.MaxConnsPerHost = 0
	tr.IdleConnTimeout = 90 * time.Second
	return tr
}()

// newEmbedHTTPClient returns a stdlib http.Client that uses sharedEmbedTransport.
func newEmbedHTTPClient() http.Client {
	return http.Client{
		Transport: sharedEmbedTransport,
		Timeout:   120 * time.Second,
	}
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

// maxEmbedChars is the per-request input cap (in runes) for OpenAI-compatible
// embedding endpoints. 5000 runes ≈ 4K tokens, leaving headroom for 8K-token
// limits common to GLM embedding-3 / bge-m3 / Qwen text-embedding-v3.
// Longer texts are split and mean-pooled to produce a single document vector.
const maxEmbedChars = 5000

func (e *APIEmbedder) Embed(text string) ([]float32, error) {
	if e.provider == "gemini" {
		return e.embedGemini(text)
	}
	runes := []rune(text)
	if len(runes) <= maxEmbedChars {
		return e.embedOpenAI(text)
	}
	return e.embedOpenAILong(runes)
}

// embedOpenAILong splits overly-long input into rune-aligned chunks, embeds
// each chunk, and mean-pools the resulting vectors so downstream storage still
// receives a single fixed-dimension embedding per document.
func (e *APIEmbedder) embedOpenAILong(runes []rune) ([]float32, error) {
	var pooled []float32
	chunks := 0
	for i := 0; i < len(runes); i += maxEmbedChars {
		end := i + maxEmbedChars
		if end > len(runes) {
			end = len(runes)
		}
		vec, err := e.embedOpenAI(string(runes[i:end]))
		if err != nil {
			return nil, fmt.Errorf("embed: chunk %d/%d: %w", chunks+1, (len(runes)+maxEmbedChars-1)/maxEmbedChars, err)
		}
		if pooled == nil {
			pooled = make([]float32, len(vec))
		} else if len(vec) != len(pooled) {
			return nil, fmt.Errorf("embed: inconsistent dimensions across chunks: %d vs %d", len(vec), len(pooled))
		}
		for j, v := range vec {
			pooled[j] += v
		}
		chunks++
	}
	if chunks == 0 {
		return nil, fmt.Errorf("embed: no chunks produced from input")
	}
	inv := 1.0 / float32(chunks)
	for j := range pooled {
		pooled[j] *= inv
	}
	log.Info("embed: mean-pooled long input", "model", e.model, "chunks", chunks, "chars", len(runes))
	return pooled, nil
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
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

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

	embedding := result.Data[0].Embedding

	// Auto-detect dimensions from first response
	if e.dims == 0 {
		e.dims = len(embedding)
		log.Info("auto-detected embedding dimensions", "model", e.model, "dims", e.dims)
	}

	return embedding, nil
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

// OllamaEmbedder uses a local Ollama instance.
type OllamaEmbedder struct {
	model  string
	dims   int
	client http.Client
}

func (e *OllamaEmbedder) Name() string    { return fmt.Sprintf("ollama/%s", e.model) }
func (e *OllamaEmbedder) Dimensions() int { return e.dims }

func (e *OllamaEmbedder) Embed(text string) ([]float32, error) {
	body, _ := json.Marshal(map[string]any{
		"model":  e.model,
		"prompt": text,
	})

	resp, err := e.client.Post("http://localhost:11434/api/embeddings", "application/json", bytes.NewReader(body))
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

// ollamaAvailable probes localhost:11434 for a running Ollama instance.
func ollamaAvailable() bool {
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://localhost:11434/api/tags")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
