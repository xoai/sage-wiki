package llm

import (
	"fmt"
	"io"
	"net/http"

	"github.com/xoai/sage-wiki/internal/log"
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
// Falls back to chatCompletionDirect (bypasses cacheID check) to avoid infinite recursion.
func (c *Client) ChatCompletionCached(cacheID string, messages []Message, opts CallOpts) (*Response, error) {
	cp, ok := c.provider.(CachingProvider)
	if !ok {
		// Provider doesn't support caching — use direct path
		return c.chatCompletionDirect(messages, opts)
	}

	c.limiter.wait()

	req, err := cp.FormatCachedRequest(cacheID, messages, opts)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		// Fall back to direct path on cache failure
		log.Warn("cached request failed, falling back", "error", err)
		return c.chatCompletionDirect(messages, opts)
	}

	body, err := readBody(resp)
	resp.Body.Close()
	if err != nil {
		return nil, fmt.Errorf("llm: read cached response body: %w", err)
	}

	if resp.StatusCode == http.StatusOK {
		result, err := c.provider.ParseResponse(body)
		if err != nil {
			return nil, err
		}
		c.trackUsage(result.Model, result.Usage)
		return result, nil
	}

	// On error, fall back to direct path (no cacheID check)
	if resp.StatusCode == 429 {
		log.Warn("rate limited on cached request, retrying direct")
		return c.chatCompletionDirect(messages, opts)
	}

	log.Warn("cached request error, falling back", "status", resp.StatusCode)
	return c.chatCompletionDirect(messages, opts)
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
