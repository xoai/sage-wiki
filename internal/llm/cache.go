package llm

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
)

func readBody(resp *http.Response) ([]byte, error) {
	return io.ReadAll(resp.Body)
}

// CachingProvider extends Provider with prompt caching support.
// Providers that don't support caching simply don't implement this interface —
// ChatCompletionCached falls back to regular ChatCompletion transparently.
type CachingProvider interface {
	// SetupCache prepares caching for a compile session.
	// Returns a cache ID (for Gemini) or empty string (for Anthropic/OpenAI).
	SetupCache(systemPrompt string, model string) (cacheID string, err error)

	// FormatCachedRequest creates a request using the cached context.
	FormatCachedRequest(cacheID string, messages []Message, opts CallOpts) (*http.Request, error)

	// TeardownCache cleans up (deletes Gemini cache, no-op for others).
	TeardownCache(cacheID string) error
}

// ChatCompletionCached sends a request using prompt caching if supported.
// Delegates to ChatCompletionCachedCtx with a background context.
func (c *Client) ChatCompletionCached(cacheID string, messages []Message, opts CallOpts) (*Response, error) {
	return c.ChatCompletionCachedCtx(context.Background(), cacheID, messages, opts)
}

// ChatCompletionCachedCtx is the cached path with a cancellation context threaded
// to the in-flight request. It falls back to chatCompletionDirect (bypassing the
// cacheID check to avoid recursion) on a cache miss/failure — EXCEPT on context
// cancellation, where it returns the ctx error promptly rather than retrying via
// the direct path. This path is the DEFAULT for anthropic/gemini compiles (prompt
// caching is on by default), so its cancellation matters as much as the direct one.
func (c *Client) ChatCompletionCachedCtx(ctx context.Context, cacheID string, messages []Message, opts CallOpts) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cp, ok := c.provider.(CachingProvider)
	if !ok {
		// Provider doesn't support caching — use direct path
		return c.chatCompletionDirect(ctx, messages, opts)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	c.limiter.wait()

	req, err := cp.FormatCachedRequest(cacheID, messages, opts)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx) // cancellation reaches the in-flight call

	resp, err := c.client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr // cancelled — return promptly, don't fall back
		}
		// Fall back to direct path on cache failure
		log.Warn("cached request failed, falling back", "error", err)
		return c.chatCompletionDirect(ctx, messages, opts)
	}

	body, err := readBody(resp)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("llm: read cached response body: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		result, err := c.provider.ParseResponse(body)
		if err != nil {
			if IsTruncatedBodyErr(err) {
				// Truncated cached 200 (issue #114): the direct path
				// retries with validation; deterministic parse errors
				// still fail fast below.
				log.Warn("truncated cached response, falling back to direct", "error", err)
				return c.chatCompletionDirect(ctx, messages, opts)
			}
			return nil, err
		}
		c.trackUsage(ctx, result.Model, result.Usage)
		return result, nil
	}

	// On error, fall back to direct path (no cacheID check)
	if resp.StatusCode == 429 {
		metrics.CounterNamed("llm_rate_limited_total").Inc() // cached-path discrimination (P2-2)
		log.Warn("rate limited on cached request, retrying direct")
		return c.chatCompletionDirect(ctx, messages, opts)
	}

	log.Warn("cached request error, falling back", "status", resp.StatusCode)
	return c.chatCompletionDirect(ctx, messages, opts)
}

// SetupCache creates a cache session if the provider supports it.
// Stores the cacheID so subsequent ChatCompletion calls auto-use caching.
func (c *Client) SetupCache(systemPrompt string, model string) (string, error) {
	cp, ok := c.provider.(CachingProvider)
	if !ok {
		return "", nil
	}
	cacheID, err := cp.SetupCache(systemPrompt, model)
	if err != nil {
		log.Warn("cache setup failed, continuing without cache", "error", err)
		return "", nil
	}
	c.cacheID = cacheID
	if cacheID != "" {
		log.Info("prompt cache active", "cacheID", cacheID)
	}
	return cacheID, nil
}

// TeardownCache cleans up the active cache session.
func (c *Client) TeardownCache(cacheID string) {
	c.cacheID = ""
	if cacheID == "" {
		return
	}
	cp, ok := c.provider.(CachingProvider)
	if !ok {
		return
	}
	if err := cp.TeardownCache(cacheID); err != nil {
		log.Warn("cache teardown failed", "cacheID", cacheID, "error", err)
	}
}
