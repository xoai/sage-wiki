package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/xoai/sage-wiki/internal/metrics"
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
	Model     string
	MaxTokens int
	// Temperature (SPEC-04 D2): nil omits the field (provider server-side
	// default); non-nil sends it explicitly — compile passes always send 0
	// unless compiler.temperature overrides.
	Temperature *float64
	// RawFallback (P2-4, spec §4 amendment): StructuredCompletion's fallback
	// returns raw completion text for the site's own parser instead of the
	// shared fence-strip parse.
	RawFallback bool
}

// Usage holds detailed token usage breakdown.
type Usage struct {
	InputTokens      int
	OutputTokens     int
	CachedTokens     int // tokens served from cache (reduced cost)
	CacheWriteTokens int // tokens written to cache (e.g. anthropic cache_creation; premium rate)
}

// Response holds the LLM response.
type Response struct {
	Content    string
	Model      string
	TokensUsed int
	Usage      Usage // detailed breakdown

	// FinishReason indicates why the model stopped generating.
	// Common values: "stop" (natural end), "length" (hit max_tokens),
	// "content_filter", "tool_calls". Empty if the provider didn't surface it.
	FinishReason string

	// Reasoning holds the model's chain-of-thought text when a reasoning model
	// returns it in a separate field (e.g., DeepSeek, Qwen via OpenAI-compatible
	// APIs). NOT used as fallback content — surfaced only for diagnostics so
	// callers can report how many tokens reasoning consumed.
	Reasoning string
}

// EmptyContentDetails returns a human-readable diagnostic string when an LLM
// response has empty Content. Returns "" if Content is non-empty.
//
// The message includes finish_reason and the size of any reasoning output
// (so users can tell when a reasoning model exhausted max_tokens on
// chain-of-thought) along with actionable hints.
func (r *Response) EmptyContentDetails() string {
	if r == nil || r.Content != "" {
		return ""
	}
	parts := []string{"LLM returned empty content"}
	if r.FinishReason != "" {
		parts = append(parts, fmt.Sprintf("finish_reason=%s", r.FinishReason))
	}
	if r.Reasoning != "" {
		// rune count is a rough proxy for token count without pulling tiktoken
		parts = append(parts, fmt.Sprintf("reasoning consumed %d chars", len([]rune(r.Reasoning))))
	}
	if r.Usage.OutputTokens > 0 {
		parts = append(parts, fmt.Sprintf("output_tokens=%d", r.Usage.OutputTokens))
	}
	msg := strings.Join(parts, ", ")
	if r.FinishReason == "length" || r.Reasoning != "" {
		msg += ". This usually means a reasoning model exhausted its token budget on chain-of-thought. " +
			"Try raising compiler.summary_max_tokens (or article_max_tokens), switch the pass to a " +
			"non-reasoning model, or skip reasoning via extra_params for a model that supports it " +
			"(provider-specific: `enable_thinking: false`, `reasoning_effort: low`, or Anthropic `thinking: { type: disabled }`)."
	}
	return msg
}

// Client is a provider-agnostic LLM client.
type Client struct {
	provider      Provider
	injected      *injectedCompletion
	providerName  string  // the configured provider string (registry identity; provider.Name() is the wire adapter, not the config kind)
	priceOverride float64 // compiler.token_price_per_million — keeps tracker-less ledger pricing consistent with tracked paths
	priceTable    string  // compiler.price_table — the workspace overlay on tracker-less paths
	registryOnce  sync.Once
	registry      *Registry
	registryErr   error
	limiter       *rateLimiter
	client        http.Client
	tracker       *CostTracker  // optional cost tracking
	recorder      UsageRecorder // optional usage-event ledger
	pass          string        // current compiler pass name (for tracking)
	tier          int           // compile tier for usage events (TierNotCompileScoped when unset)
	cacheID       string        // active cache ID (empty = no caching)
	// callTimeout bounds each provider call (SPEC-08 provider_timeout).
	// The per-call deadline is min(remaining ctx deadline, callTimeout) —
	// a shorter caller deadline always wins. Zero disables the extra
	// bound (the http.Client transport timeout still applies).
	callTimeout time.Duration
}

// sharedTransport is the HTTP transport reused by every llm.Client.
// It overrides Go's http.DefaultTransport (which caps MaxIdleConnsPerHost at 2,
// forcing TCP/TLS churn on ~every concurrent call) and sets MaxConnsPerHost=0
// (unlimited). This matters when the compiler fires cfg.Compiler.MaxParallel
// concurrent requests at a single vLLM/Ollama/OpenAI host.
//
// Kept as a package-level var so embedding connection pool is shared across
// Clients in the same process (matches Go's DefaultTransport ergonomics).
var sharedTransport http.RoundTripper = func() http.RoundTripper {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 512
	tr.MaxIdleConnsPerHost = 256
	tr.MaxConnsPerHost = 0 // unlimited (do not throttle concurrent dials)
	tr.IdleConnTimeout = 90 * time.Second
	return tr
}()

// NewClient creates a new LLM client for the given provider.
// extraParams (if provided) are merged into every request body — use for
// provider-specific parameters like Qwen's enable_thinking or DeepSeek's
// reasoning_effort.
func NewClient(providerName string, apiKey string, baseURL string, rateLimit int, extraParams ...map[string]interface{}) (*Client, error) {
	p, err := newProvider(providerName, apiKey, baseURL)
	if err != nil {
		return nil, err
	}

	// rateLimit == 0 (omitted) → use provider default (may be 0 for self-hosted).
	// rateLimit  < 0          → explicit opt-out, disable client-side rate limiting.
	if rateLimit == 0 {
		rateLimit = defaultRateLimit(providerName)
	}
	if forceRateLimitForTest != nil {
		rateLimit = *forceRateLimitForTest
	}

	var extra map[string]interface{}
	if len(extraParams) > 0 && extraParams[0] != nil {
		extra = extraParams[0]
	}

	// Wire extra params into any provider that accepts them — the raw openai
	// provider, the nonBatchProvider wrappers (openai-compatible/qwen/ollama,
	// which forward to their inner provider), and anthropic. Providers that
	// can't take them (e.g. gemini) are warned about so the setting isn't
	// silently dropped.
	if len(extra) > 0 {
		if s, ok := p.(extraParamsSetter); ok {
			s.setExtraParams(extra)
		} else {
			log.Warn("extra_params set but provider does not support them — ignored", "provider", providerName)
		}
	}

	return &Client{
		provider:     p,
		providerName: providerName,
		limiter:      newRateLimiter(rateLimit),
		tier:         TierNotCompileScoped,
		client: http.Client{
			Transport: sharedTransport,
			Timeout:   120 * time.Second,
		},
		callTimeout: 120 * time.Second,
	}, nil
}

func (c *Client) SetTransport(t http.RoundTripper) {
	c.client.Transport = t
}

// SetCallTimeout bounds each provider call (SPEC-08 provider_timeout).
// Shorter caller context deadlines always win (context nesting takes the
// minimum). Zero or negative disables the extra bound.
func (c *Client) SetCallTimeout(d time.Duration) {
	if d > 0 {
		c.callTimeout = d
	}
}

// ChatCompletion sends a chat completion request with retry on rate limits.
// If a cache is active (via SetupCache), automatically uses the cached path.
// It delegates to ChatCompletionCtx with a background context (no cancellation);
// callers that need Ctrl-C / deadline propagation use ChatCompletionCtx.
func (c *Client) ChatCompletion(messages []Message, opts CallOpts) (*Response, error) {
	return c.ChatCompletionCtx(context.Background(), messages, opts)
}

// ChatCompletionCtx is ChatCompletion with a cancellation context: the context
// reaches the in-flight HTTP request and the retry backoff, so a cancel or
// deadline returns promptly instead of blocking on the LLM call. A nil ctx is
// treated as context.Background().
func (c *Client) ChatCompletionCtx(ctx context.Context, messages []Message, opts CallOpts) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	var resp *Response
	var err error
	if c.cacheID != "" {
		resp, err = c.ChatCompletionCachedCtx(ctx, c.cacheID, messages, opts)
	} else {
		resp, err = c.chatCompletionDirect(ctx, messages, opts)
	}
	if err != nil {
		return nil, err
	}
	resp.Content = stripThinkTags(resp.Content)
	return resp, nil
}

// IsTruncatedBodyErr reports whether err is a truncation-class parse
// failure on a 200-OK body — transient provider flakiness worth retrying:
// *json.SyntaxError (covers "unexpected end of JSON input" from Unmarshal),
// io.ErrUnexpectedEOF, io.EOF (empty body via a Decoder). Well-formed-but-
// invalid bodies (error-shaped JSON, empty choices, wrong shape) must NOT
// match — those are deterministic and fail fast. Requires providers to wrap
// parse errors with %w (verified: openai.go, anthropic.go, gemini.go).
func IsTruncatedBodyErr(err error) bool {
	var se *json.SyntaxError
	return errors.As(err, &se) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

// chatCompletionDirect sends a request without checking cacheID.
// Used by ChatCompletionCtx and as the fallback path for ChatCompletionCached.
// The ctx bounds the HTTP call and the retry backoff. Body validation rides
// the retry loop via the validate closure (issue #114): a truncated 200 body
// retries; a well-formed-but-invalid one returns (body, verr) and is wrapped
// here, preserving the "llm: parse response" contract.
func (c *Client) chatCompletionDirect(ctx context.Context, messages []Message, opts CallOpts) (*Response, error) {
	if c.injected != nil {
		return c.completeInjected(ctx, messages, opts)
	}
	var result *Response
	validate := func(body []byte) error {
		r, err := c.provider.ParseResponse(body)
		if err != nil {
			return err
		}
		result = r
		return nil
	}
	body, err := c.doWithRetry(ctx, func() (*http.Request, error) {
		return c.provider.FormatRequest(messages, opts)
	}, validate, "parse response")
	if err != nil {
		if body != nil {
			// Non-truncation validation failure (deterministic).
			return nil, fmt.Errorf("llm: parse response: %w", err)
		}
		return nil, err
	}
	c.trackUsage(ctx, result.Model, result.Usage)
	return result, nil
}

// SupportsVision returns whether the provider supports image inputs.
func (c *Client) SupportsVision() bool {
	return c.provider.SupportsVision()
}

// ChatCompletionWithImage sends a chat completion with an inline base64 image.
// The image is embedded in a Message with ImageBase64/ImageMime fields set.
// Each provider adapter handles the multimodal format in FormatRequest.
func (c *Client) ChatCompletionWithImage(messages []Message, prompt string, imageBase64 string, mimeType string, opts CallOpts) (*Response, error) {
	return c.ChatCompletionWithImageCtx(context.Background(), messages, prompt, imageBase64, mimeType, opts)
}

// ChatCompletionWithImageCtx is ChatCompletionWithImage with a cancellation
// context threaded to the underlying call.
func (c *Client) ChatCompletionWithImageCtx(ctx context.Context, messages []Message, prompt string, imageBase64 string, mimeType string, opts CallOpts) (*Response, error) {
	visionMsg := Message{
		Role:        "user",
		Content:     prompt,
		ImageBase64: imageBase64,
		ImageMime:   mimeType,
	}
	return c.ChatCompletionCtx(ctx, append(messages, visionMsg), opts)
}

// ProviderName returns the provider name.
func (c *Client) ProviderName() string {
	return c.provider.Name()
}

// SetTracker attaches a cost tracker. All subsequent calls are tracked.
func (c *Client) SetTracker(tracker *CostTracker) {
	c.tracker = tracker
}

// SetRecorder attaches a usage-event recorder. Every completion fires one
// UsageEvent (SPEC-05 ledger; SPEC-07 consumes the type).
func (c *Client) SetRecorder(recorder UsageRecorder) {
	c.recorder = recorder
}

// SetPriceOverride sets the token_price_per_million override used when
// pricing usage events without an attached tracker (query/expansion paths),
// so the ledger prices those events consistently with compile events.
func (c *Client) SetPriceOverride(override float64) {
	c.priceOverride = override
}

// SetPriceTable points the client at the workspace price table
// (compiler.price_table) for tracker-less pricing — the same load order
// the compile tracker uses (builtin → user file → workspace table).
func (c *Client) SetPriceTable(path string) {
	c.priceTable = path
}

// pricingRegistry loads the registry once per CLIENT (not per process) —
// a long-lived host embedding many workspaces must not leak one
// workspace's prices into another's usage events.
func (c *Client) pricingRegistry() (*Registry, error) {
	c.registryOnce.Do(func() {
		c.registry, c.registryErr = LoadRegistry(c.priceTable)
	})
	return c.registry, c.registryErr
}

// SetTier sets the compile tier recorded on usage events.
//
// c.tier is unsynchronized and buildUsageEvent reads it from request
// goroutines, so (exactly like SetPass) SetTier must be called OUTSIDE a
// fan-out — before it starts and after it joins — never from within one.
func (c *Client) SetTier(tier int) {
	c.tier = tier
}

// RecordBatchUsage fires a usage event for a batch result. Batch usage does
// not flow through trackUsage (results arrive via polling), so the pipeline
// consumption site calls this with the pass it knows.
func (c *Client) RecordBatchUsage(ctx context.Context, pass, model string, usage Usage) {
	if c.recorder == nil {
		return
	}
	ev := c.buildUsageEvent(model, usage)
	ev.Pass = pass
	if c.tracker != nil {
		ev.Cost, ev.PriceSource, ev.Assumptions = c.tracker.PriceUsage(model, usage, true)
	}
	c.recorder.RecordUsage(ctx, ev)
}

// SetPass sets the current compiler pass name for cost tracking.
//
// c.pass is unsynchronized and trackUsage reads it from request goroutines, so
// SetPass must be called OUTSIDE a fan-out — before it starts and after it
// joins — never from within one.
func (c *Client) SetPass(pass string) {
	c.pass = pass
}

// Pass returns the current cost-tracking pass name, so a pass that changes it
// can restore the caller's value on the way out. Without this, a pass that sets
// its own label leaks it into whatever runs next: on the re-extract path
// nothing sets a label afterwards, so every write-pass token would be billed to
// the previous pass.
func (c *Client) Pass() string {
	return c.pass
}

// trackUsage records token usage if a tracker is attached, and fires a
// UsageEvent if a recorder is attached.
func (c *Client) trackUsage(ctx context.Context, model string, usage Usage) {
	if c.tracker != nil {
		c.tracker.Track(c.pass, model, usage, false)
	}
	if c.recorder != nil {
		c.recorder.RecordUsage(ctx, c.buildUsageEvent(model, usage))
	}
}

// buildUsageEvent prices the call against the attached tracker when present,
// else the shared builtin+user-file registry (tracker-less query/expansion
// clients). Unknown price ⇒ Cost nil — never zero, never a guess.
func (c *Client) buildUsageEvent(model string, usage Usage) UsageEvent {
	ev := UsageEvent{
		TS:               time.Now().UTC(),
		Pass:             c.pass,
		Provider:         c.providerName,
		Model:            model,
		Tier:             c.tier,
		InputTokens:      usage.InputTokens,
		CachedTokens:     usage.CachedTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		OutputTokens:     usage.OutputTokens,
	}
	if c.tracker != nil {
		ev.Cost, ev.PriceSource, ev.Assumptions = c.tracker.PriceUsage(model, usage, false)
		return ev
	}
	if r, err := c.pricingRegistry(); err == nil {
		ct := newCostTrackerWithRegistry(c.providerName, c.priceOverride, r)
		ev.Cost, ev.PriceSource, ev.Assumptions = ct.PriceUsage(model, usage, false)
	} else {
		ev.Assumptions = []string{"price registry unavailable: " + err.Error()}
	}
	return ev
}

// Provider defines the interface for LLM provider adapters.
type Provider interface {
	Name() string
	FormatRequest(messages []Message, opts CallOpts) (*http.Request, error)
	ParseResponse(body []byte) (*Response, error)
	SupportsVision() bool
	// FormatStructuredRequest builds the provider's constrained request
	// (P2-4), returned as a builder closure so each retry gets a fresh
	// body. ok == false: no mechanism — the client uses a plain
	// completion + fence-strip fallback. Providers without a mechanism
	// must still implement both methods (stub: ok == false /
	// ErrStructuredUnsupported) so the interface is total.
	FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error)
	// ParseStructuredResponse extracts the constrained payload from the
	// raw response body.
	ParseStructuredResponse(body []byte) (json.RawMessage, error)
}

// extraParamsSetter is implemented by providers that merge caller-supplied
// extra_params (e.g. reasoning_effort, enable_thinking, a `thinking` config)
// into the request body. nonBatchProvider forwards to its inner provider.
type extraParamsSetter interface {
	setExtraParams(map[string]interface{})
}

func newProvider(name string, apiKey string, baseURL string) (Provider, error) {
	switch name {
	case "openai":
		return newOpenAIProvider(apiKey, baseURL), nil
	case "openai-compatible":
		return &nonBatchProvider{inner: newOpenAIProvider(apiKey, baseURL)}, nil
	case "qwen":
		if baseURL == "" {
			baseURL = "https://dashscope.aliyuncs.com/compatible-mode/v1"
		}
		return &nonBatchProvider{inner: newOpenAIProvider(apiKey, baseURL)}, nil
	case "anthropic":
		return newAnthropicProvider(apiKey, baseURL), nil
	case "gemini":
		return newGeminiProvider(apiKey, baseURL), nil
	case "ollama":
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return &nonBatchProvider{inner: newOpenAIProvider("", baseURL+"/v1")}, nil
	default:
		return nil, fmt.Errorf("llm: unsupported provider %q", name)
	}
}

// defaultRateLimit returns the default RPM for a provider.
//
//   - Public paid APIs get conservative defaults matching their published limits.
//   - Self-hosted backends (openai-compatible = vLLM/LocalAI/etc., ollama) return
//     0, meaning "no client-side rate limiting": the compiler's BackpressureController
//   - server-side capacity are the real governors, and a 1/sec (or 1/2sec) cap
//     was the hidden reason sage-wiki could not saturate a local GPU endpoint
//     despite cfg.Compiler.MaxParallel >= 8 (PER-116 / per-112-concurrency-fix).
//   - Unknown providers keep the previous conservative 30 RPM default — do not
//     surprise users with unbounded bursts against a new SaaS they wire up.

// forceRateLimitForTest, when non-nil, overrides the rate limit of every
// subsequently constructed Client. See SetRateLimitForTest.
var forceRateLimitForTest *int

// SetRateLimitForTest forces every subsequently constructed Client's rate
// limiter to rpm (use -1 to disable), returning a restore function. For
// tests only — production code must not call this.
func SetRateLimitForTest(rpm int) (restore func()) {
	prev := forceRateLimitForTest
	v := rpm
	forceRateLimitForTest = &v
	return func() { forceRateLimitForTest = prev }
}

func defaultRateLimit(provider string) int {
	switch provider {
	case "anthropic":
		return 50
	case "openai":
		return 60
	case "qwen":
		return 60
	case "gemini":
		return 60
	case "openai-compatible", "ollama":
		return 0 // self-hosted: no client-side RPM cap
	default:
		return 30
	}
}

// recordRateLimited is the 429 metrics hook for transport paths without a
// typed branch (batch submit/poll/retrieve, provider variants) — one-line,
// first-discrimination-per-response (P2-2).
func recordRateLimited(statusCode int) {
	if statusCode == 429 {
		metrics.CounterNamed("llm_rate_limited_total").Inc()
	}
}

// RateLimitError is returned when the LLM API returns 429 (Too Many Requests)
// after exhausting all retries. The BackpressureController uses this to
// distinguish rate limits from other errors and adjust concurrency.
type RateLimitError struct {
	StatusCode int
	Body       string
	RetryAfter time.Duration // from Retry-After header, if present
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("llm: rate limited (HTTP %d): %s", e.StatusCode, e.Body)
}

// IsRateLimitError checks whether an error is a rate limit error.
func IsRateLimitError(err error) bool {
	var rle *RateLimitError
	return errors.As(err, &rle)
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

// rateLimiter paces outgoing requests so the Client never exceeds a caller-
// supplied requests-per-minute target. It is an N-goroutine-safe "next available
// slot" limiter:
//
//   - Each goroutine reserves a monotonically advancing wake-up timestamp while
//     holding the mutex, then releases the mutex BEFORE sleeping. This is the
//     key fix for PER-116: the previous implementation held the mutex across
//     time.Sleep(), which serialized ALL concurrent callers to one-per-interval
//     regardless of cfg.Compiler.MaxParallel. With the mutex released, slots are
//     still spaced `interval` apart, but goroutines sleep concurrently — so a
//     burst of N goroutines against a high RPM limit (or a disabled limiter) can
//     all have their requests in flight simultaneously.
//
//   - When requestsPerMinute <= 0 the limiter is disabled (no-op wait). Used by
//     self-hosted backends where a local GPU endpoint is the real governor.
type rateLimiter struct {
	mu       sync.Mutex
	interval time.Duration // 0 = disabled
	nextSlot time.Time     // next allowed call start
}

// newRateLimiter returns a limiter. A non-positive rate means "disabled".
func newRateLimiter(requestsPerMinute int) *rateLimiter {
	if requestsPerMinute <= 0 {
		return &rateLimiter{} // interval=0 → wait() is a no-op
	}
	return &rateLimiter{interval: time.Minute / time.Duration(requestsPerMinute)}
}

// wait blocks just long enough that the calling goroutine's request respects
// the limiter's spacing. The mutex is NOT held across the sleep.
func (r *rateLimiter) wait() {
	if r.interval == 0 {
		return
	}

	r.mu.Lock()
	now := time.Now()
	wakeAt := r.nextSlot
	if wakeAt.Before(now) {
		wakeAt = now
	}
	r.nextSlot = wakeAt.Add(r.interval)
	r.mu.Unlock()

	if d := time.Until(wakeAt); d > 0 {
		time.Sleep(d)
	}
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

// Float64 returns a pointer to v — the CallOpts.Temperature literal helper
// (SPEC-04 D2 made the field a pointer).
func Float64(v float64) *float64 { return &v }
