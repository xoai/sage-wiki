package llm

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"github.com/xoai/sage-wiki/internal/log"
)

// litellmProvider targets a LiteLLM proxy (https://docs.litellm.ai/docs/simple_proxy).
// Chat and streaming go through the proxy's OpenAI-compatible
// /chat/completions, like openai-compatible. What the generic path cannot
// do is read the proxy's per-alias metadata from /model/info, which drives:
//
//   - pricing: per-alias token prices become PriceSourceProviderAPI registry
//     entries, so proxy aliases are priced instead of "unknown";
//   - structured output: json_schema is sent only for aliases the proxy
//     reports as supports_response_schema (LiteLLM translates it per backend);
//     every other alias keeps the plain completion fallback;
//   - embeddings: the first mode=embedding alias is the Tier 1 embedder.
//
// Requests also carry drop_params: true, so the proxy drops parameters an
// alias's backend rejects (compile passes always send temperature 0, which
// reasoning models such as gpt-5 or o-series refuse). extra_params can set
// drop_params: false to get the backend's error instead.
//
// Like nonBatchProvider it holds the openai provider in a named field so the
// OpenAI Files/Batches methods are not promoted: the proxy's batch API needs
// per-deployment file routing that sage-wiki's batch path does not model.
type litellmProvider struct {
	inner *openaiProvider
}

// DefaultLiteLLMBaseURL is the proxy's default listen address.
const DefaultLiteLLMBaseURL = "http://localhost:4000"

func newLiteLLMProvider(apiKey string, baseURL string) *litellmProvider {
	if baseURL == "" {
		baseURL = DefaultLiteLLMBaseURL
	}
	p := &litellmProvider{inner: newOpenAIProvider(apiKey, strings.TrimSuffix(baseURL, "/"))}
	p.setExtraParams(nil)
	return p
}

func (p *litellmProvider) Name() string { return "litellm" }

// SupportsVision is true: the proxy forwards OpenAI image_url content parts
// and returns a backend error for aliases whose model cannot take images.
func (p *litellmProvider) SupportsVision() bool { return true }

// setExtraParams layers caller extra_params over the drop_params default.
func (p *litellmProvider) setExtraParams(m map[string]interface{}) {
	merged := map[string]interface{}{"drop_params": true}
	for k, v := range m {
		merged[k] = v
	}
	p.inner.setExtraParams(merged)
}

func (p *litellmProvider) FormatRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return p.inner.FormatRequest(messages, opts)
}

func (p *litellmProvider) ParseResponse(body []byte) (*Response, error) {
	return p.inner.ParseResponse(body)
}

func (p *litellmProvider) FormatStreamRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return p.inner.FormatStreamRequest(messages, opts)
}

func (p *litellmProvider) ParseStreamChunk(data []byte) (string, bool) {
	return p.inner.ParseStreamChunk(data)
}

// FormatStructuredRequest sends OpenAI json_schema only when the proxy says
// the alias supports it. Unknown aliases, or a proxy whose /model/info is
// unreachable, return ok == false: the same fallback openai-compatible uses.
func (p *litellmProvider) FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error) {
	m, ok := p.lookupModel(opts.Model)
	if !ok || !m.SupportsResponseSchema {
		return nil, false, nil
	}
	return p.inner.FormatStructuredRequest(messages, schema, opts)
}

func (p *litellmProvider) ParseStructuredResponse(body []byte) (json.RawMessage, error) {
	return p.inner.ParseStructuredResponse(body)
}

func (p *litellmProvider) lookupModel(alias string) (LiteLLMModel, bool) {
	models, err := LiteLLMModels(p.inner.baseURL, p.inner.apiKey)
	if err != nil {
		return LiteLLMModel{}, false
	}
	for _, m := range models {
		if m.Name == alias {
			return m, true
		}
	}
	return LiteLLMModel{}, false
}

// endpoint reports the connection the registry needs to load this proxy's
// prices (see providerAPIPrices).
func (p *litellmProvider) endpoint() Endpoint {
	return Endpoint{Provider: "litellm", BaseURL: p.inner.baseURL, APIKey: p.inner.apiKey}
}

// LiteLLMModel is one alias from a LiteLLM proxy's /model/info.
type LiteLLMModel struct {
	Name       string // model_name: the alias sage-wiki sends as "model"
	Underlying string // litellm_params.model without its provider prefix ("azure/gpt-4.1-mini" → "gpt-4.1-mini")
	Mode       string // "chat", "embedding", "completion", ...

	// Per-token USD prices; nil when the proxy does not know the price.
	InputCostPerToken      *float64
	OutputCostPerToken     *float64
	CacheReadCostPerToken  *float64
	CacheWriteCostPerToken *float64

	SupportsResponseSchema bool
	OutputVectorSize       int // embedding dimensions, 0 when unreported
}

type litellmModelInfoDoc struct {
	Data []struct {
		ModelName     string `json:"model_name"`
		LiteLLMParams struct {
			Model string `json:"model"`
		} `json:"litellm_params"`
		ModelInfo struct {
			Mode                   string   `json:"mode"`
			InputCostPerToken      *float64 `json:"input_cost_per_token"`
			OutputCostPerToken     *float64 `json:"output_cost_per_token"`
			CacheReadCostPerToken  *float64 `json:"cache_read_input_token_cost"`
			CacheWriteCostPerToken *float64 `json:"cache_creation_input_token_cost"`
			SupportsResponseSchema *bool    `json:"supports_response_schema"`
			OutputVectorSize       *int     `json:"output_vector_size"`
		} `json:"model_info"`
	} `json:"data"`
}

// litellmModelCacheTTL bounds how stale proxy metadata may get. Failures are
// cached for the shorter litellmModelFailureTTL so an unreachable proxy does
// not add a request timeout to every client construction.
const (
	litellmModelCacheTTL   = 5 * time.Minute
	litellmModelFailureTTL = time.Minute
)

type litellmModelCacheEntry struct {
	models  []LiteLLMModel
	err     error
	expires time.Time
}

var (
	litellmModelCacheMu sync.Mutex
	// Keyed by base URL + API key: two workspaces see each other's entry only
	// when they talk to the same proxy with the same key.
	litellmModelCache = map[string]litellmModelCacheEntry{}

	litellmHTTPClient = &http.Client{Transport: sharedTransport, Timeout: 10 * time.Second}
)

// LiteLLMModels returns the aliases a LiteLLM proxy serves, in the order
// /model/info lists them, cached per proxy and key.
func LiteLLMModels(baseURL, apiKey string) ([]LiteLLMModel, error) {
	if baseURL == "" {
		baseURL = DefaultLiteLLMBaseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")
	key := baseURL + "\x00" + apiKey

	litellmModelCacheMu.Lock()
	defer litellmModelCacheMu.Unlock()
	if e, ok := litellmModelCache[key]; ok && time.Now().Before(e.expires) {
		return e.models, e.err
	}
	models, err := fetchLiteLLMModels(baseURL, apiKey)
	ttl := litellmModelCacheTTL
	if err != nil {
		ttl = litellmModelFailureTTL
		log.Warn("litellm: /model/info unavailable; proxy aliases are unpriced and structured output falls back", "base_url", baseURL, "error", err)
	}
	litellmModelCache[key] = litellmModelCacheEntry{models: models, err: err, expires: time.Now().Add(ttl)}
	return models, err
}

func fetchLiteLLMModels(baseURL, apiKey string) ([]LiteLLMModel, error) {
	req, err := http.NewRequest("GET", baseURL+"/model/info", nil)
	if err != nil {
		return nil, err
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := litellmHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("litellm: model info: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, fmt.Errorf("litellm: model info: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("litellm: model info: HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var doc litellmModelInfoDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("litellm: model info: %w", err)
	}
	models := make([]LiteLLMModel, 0, len(doc.Data))
	for _, d := range doc.Data {
		if d.ModelName == "" {
			continue
		}
		underlying := d.LiteLLMParams.Model
		if i := strings.Index(underlying, "/"); i >= 0 {
			underlying = underlying[i+1:]
		}
		m := LiteLLMModel{
			Name:                   d.ModelName,
			Underlying:             underlying,
			Mode:                   d.ModelInfo.Mode,
			InputCostPerToken:      d.ModelInfo.InputCostPerToken,
			OutputCostPerToken:     d.ModelInfo.OutputCostPerToken,
			CacheReadCostPerToken:  d.ModelInfo.CacheReadCostPerToken,
			CacheWriteCostPerToken: d.ModelInfo.CacheWriteCostPerToken,
		}
		if d.ModelInfo.SupportsResponseSchema != nil {
			m.SupportsResponseSchema = *d.ModelInfo.SupportsResponseSchema
		}
		if d.ModelInfo.OutputVectorSize != nil {
			m.OutputVectorSize = *d.ModelInfo.OutputVectorSize
		}
		models = append(models, m)
	}
	return models, nil
}

// LiteLLMEmbeddingModel returns the first alias the proxy serves in
// embedding mode.
func LiteLLMEmbeddingModel(baseURL, apiKey string) (LiteLLMModel, bool) {
	models, err := LiteLLMModels(baseURL, apiKey)
	if err != nil {
		return LiteLLMModel{}, false
	}
	for _, m := range models {
		if m.Mode == "embedding" {
			return m, true
		}
	}
	return LiteLLMModel{}, false
}

// litellmPrices converts proxy aliases to registry entries keyed
// "litellm:<alias>". The proxy reports the alias as the response model, so
// that key is what usage resolves to. Each price is also registered under the
// underlying model name, for proxies that report the backend model instead,
// but only when every alias routed to that model agrees on the price: a free
// alias and a paid alias over one deployment leave the backend name unknown
// rather than priced wrong. Load-balanced aliases list one entry per
// deployment; the first wins.
func litellmPrices(models []LiteLLMModel, asOf time.Time) map[string]Price {
	perToken := func(v *float64) *decimal.Decimal {
		if v == nil {
			return nil
		}
		d := decimal.NewFromFloat(*v).Mul(million)
		return &d
	}
	out := map[string]Price{}
	byUnderlying := map[string][]Price{}
	var underlyingOrder []string
	for _, m := range models {
		if m.InputCostPerToken == nil && m.OutputCostPerToken == nil {
			continue
		}
		key := "litellm:" + m.Name
		if _, dup := out[key]; dup {
			continue
		}
		price := Price{
			InputPerMTok:       perToken(m.InputCostPerToken),
			CachedInputPerMTok: perToken(m.CacheReadCostPerToken),
			CacheWritePerMTok:  perToken(m.CacheWriteCostPerToken),
			OutputPerMTok:      perToken(m.OutputCostPerToken),
			AsOf:               asOf,
			Source:             PriceSourceProviderAPI,
		}
		out[key] = price
		if m.Underlying != "" && m.Underlying != m.Name {
			if _, seen := byUnderlying[m.Underlying]; !seen {
				underlyingOrder = append(underlyingOrder, m.Underlying)
			}
			byUnderlying[m.Underlying] = append(byUnderlying[m.Underlying], price)
		}
	}
	for _, name := range underlyingOrder {
		key := "litellm:" + name
		if _, taken := out[key]; taken {
			continue
		}
		prices := byUnderlying[name]
		agree := true
		for _, p := range prices[1:] {
			if !samePrice(p, prices[0]) {
				agree = false
				break
			}
		}
		if agree {
			out[key] = prices[0]
		}
	}
	return out
}

func samePrice(a, b Price) bool {
	eq := func(x, y *decimal.Decimal) bool {
		if x == nil || y == nil {
			return x == y
		}
		return x.Equal(*y)
	}
	return eq(a.InputPerMTok, b.InputPerMTok) && eq(a.OutputPerMTok, b.OutputPerMTok) &&
		eq(a.CachedInputPerMTok, b.CachedInputPerMTok) && eq(a.CacheWritePerMTok, b.CacheWritePerMTok)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
