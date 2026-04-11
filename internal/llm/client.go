package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
)

// Message represents a chat message.
type Message struct {
	Role        string `json:"role"` // system, user, assistant
	Content     string `json:"content"`
	ImageBase64 string `json:"-"` // base64 image data (vision messages only)
	ImageMime   string `json:"-"` // e.g. "image/png"
}

// CallOpts configures an LLM call.
type CallOpts struct {
	Model      string
	MaxTokens  int
	Temperature float64
}

// Usage holds detailed token usage breakdown.
type Usage struct {
	InputTokens  int
	OutputTokens int
	CachedTokens int // tokens served from cache (reduced cost)
}

// Response holds the LLM response.
type Response struct {
	Content    string
	Model      string
	TokensUsed int
	Usage      Usage // detailed breakdown
}

// Client is a provider-agnostic LLM client.
type Client struct {
	provider Provider
	limiter  *rateLimiter
	client   http.Client
	tracker  *CostTracker // optional cost tracking
	pass     string       // current compiler pass name (for tracking)
	cacheID  string       // active cache ID (empty = no caching)
}

// NewClient creates a new LLM client for the given provider.
func NewClient(providerName string, apiKey string, baseURL string, rateLimit int) (*Client, error) {
	p, err := newProvider(providerName, apiKey, baseURL)
	if err != nil {
		return nil, err
	}

	if rateLimit <= 0 {
		rateLimit = defaultRateLimit(providerName)
	}

	return &Client{
		provider: p,
		limiter:  newRateLimiter(rateLimit),
		client:   http.Client{Timeout: 120 * time.Second},
	}, nil
}

// ChatCompletion sends a chat completion request with retry on rate limits.
// If a cache is active (via SetupCache), automatically uses the cached path.
func (c *Client) ChatCompletion(messages []Message, opts CallOpts) (*Response, error) {
	var resp *Response
	var err error
	if c.cacheID != "" {
		resp, err = c.ChatCompletionCached(c.cacheID, messages, opts)
	} else {
		resp, err = c.chatCompletionDirect(messages, opts)
	}
	if err != nil {
		return nil, err
	}
	resp.Content = stripThinkTags(resp.Content)
	return resp, nil
}

// chatCompletionDirect sends a request without checking cacheID.
// Used by ChatCompletion and as the fallback path for ChatCompletionCached.
func (c *Client) chatCompletionDirect(messages []Message, opts CallOpts) (*Response, error) {
	var lastErr error

	for attempt := 0; attempt < 4; attempt++ {
		// Wait for rate limiter
		c.limiter.wait()

		req, err := c.provider.FormatRequest(messages, opts)
		if err != nil {
			return nil, fmt.Errorf("llm: format request: %w", err)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("llm: request failed: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			result, err := c.provider.ParseResponse(body)
			if err != nil {
				return nil, fmt.Errorf("llm: parse response: %w", err)
			}
			c.trackUsage(result.Model, result.Usage)
			return result, nil
		}

		if isRetryable(resp.StatusCode) {
			delay := backoffDelay(attempt)
			log.Warn("retryable error, retrying", "status", resp.StatusCode, "attempt", attempt+1, "delay", delay)
			time.Sleep(delay)
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
			continue
		}

		return nil, fmt.Errorf("llm: API returned %d: %s", resp.StatusCode, string(body))
	}

	return nil, fmt.Errorf("llm: max retries exceeded: %w", lastErr)
}

// SupportsVision returns whether the provider supports image inputs.
func (c *Client) SupportsVision() bool {
	return c.provider.SupportsVision()
}

// ChatCompletionWithImage sends a chat completion with an inline base64 image.
// The image is embedded in a Message with ImageBase64/ImageMime fields set.
// Each provider adapter handles the multimodal format in FormatRequest.
func (c *Client) ChatCompletionWithImage(messages []Message, prompt string, imageBase64 string, mimeType string, opts CallOpts) (*Response, error) {
	visionMsg := Message{
		Role:        "user",
		Content:     prompt,
		ImageBase64: imageBase64,
		ImageMime:   mimeType,
	}
	return c.ChatCompletion(append(messages, visionMsg), opts)
}

// ProviderName returns the provider name.
func (c *Client) ProviderName() string {
	return c.provider.Name()
}

// SetTracker attaches a cost tracker. All subsequent calls are tracked.
func (c *Client) SetTracker(tracker *CostTracker) {
	c.tracker = tracker
}

// SetPass sets the current compiler pass name for cost tracking.
func (c *Client) SetPass(pass string) {
	c.pass = pass
}

// trackUsage records token usage if a tracker is attached.
func (c *Client) trackUsage(model string, usage Usage) {
	if c.tracker != nil {
		c.tracker.Track(c.pass, model, usage, false)
	}
}

// Provider defines the interface for LLM provider adapters.
type Provider interface {
	Name() string
	FormatRequest(messages []Message, opts CallOpts) (*http.Request, error)
	ParseResponse(body []byte) (*Response, error)
	SupportsVision() bool
}

func newProvider(name string, apiKey string, baseURL string) (Provider, error) {
	switch name {
	case "openai", "openai-compatible":
		return newOpenAIProvider(apiKey, baseURL), nil
	case "anthropic":
		return newAnthropicProvider(apiKey, baseURL), nil
	case "gemini":
		return newGeminiProvider(apiKey, baseURL), nil
	case "ollama":
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return newOpenAIProvider("", baseURL+"/v1"), nil
	default:
		return nil, fmt.Errorf("llm: unsupported provider %q", name)
	}
}

func defaultRateLimit(provider string) int {
	switch provider {
	case "anthropic":
		return 50
	case "openai":
		return 60
	case "gemini":
		return 60
	default:
		return 30
	}
}

// isRetryable returns true for HTTP status codes that warrant automatic retry.
// Covers rate limits (429) and transient server errors (500, 502, 503).
func isRetryable(statusCode int) bool {
	return statusCode == 429 || statusCode == 500 || statusCode == 502 || statusCode == 503
}

// backoffDelay returns exponential backoff with jitter, capped at 60s.
func backoffDelay(attempt int) time.Duration {
	base := math.Pow(2, float64(attempt)) // 1, 2, 4, 8
	jitter := rand.Float64() * base
	delay := base + jitter
	if delay > 60 {
		delay = 60
	}
	return time.Duration(delay * float64(time.Second))
}

// rateLimiter implements a simple token bucket rate limiter.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration
	lastCall time.Time
}

func newRateLimiter(requestsPerMinute int) *rateLimiter {
	interval := time.Minute / time.Duration(requestsPerMinute)
	return &rateLimiter{interval: interval}
}

func (r *rateLimiter) wait() {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(r.lastCall)
	if elapsed < r.interval {
		time.Sleep(r.interval - elapsed)
	}
	r.lastCall = time.Now()
}

// stripThinkTags removes <think>...</think> blocks from LLM responses.
// Some models (e.g. MiniMax) include reasoning traces that should not appear in output.
// When the model puts ALL content inside think tags (common with reasoning models
// under tight token budgets), falls back to extracting the think content rather
// than returning empty.
var thinkTagRe = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)
var thinkContentRe = regexp.MustCompile(`(?s)<think>(.*?)</think>`)

func stripThinkTags(s string) string {
	stripped := strings.TrimSpace(thinkTagRe.ReplaceAllString(s, ""))
	if stripped != "" {
		return stripped
	}
	// Fallback: extract content from inside first think block
	if m := thinkContentRe.FindStringSubmatch(s); len(m) > 1 {
		return strings.TrimSpace(m[1])
	}
	return stripped
}

// jsonBody creates a JSON request body. Panics on marshal failure
// since we only marshal known map structures.
func jsonBody(v any) *bytes.Buffer {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("llm: failed to marshal request body: %v", err))
	}
	return bytes.NewBuffer(data)
}
