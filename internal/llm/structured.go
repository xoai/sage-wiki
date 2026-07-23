package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
)

// doWithRetry executes buildReq with the direct path's retry discipline —
// THE single transport the client uses (P2-4 extraction): rate limiter,
// ctx-annotated request, 429 first-discrimination + typed RateLimitError,
// cancellable backoff with retry counting, StatusError on non-retryable.
// buildReq is called per attempt (fresh body — Do consumes it).
// Behavior is byte-identical to the pre-extraction chatCompletionDirect.
func (c *Client) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error)) ([]byte, error) {
	var lastErr error
	var lastStatusCode int

	const maxAttempts = 4
	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Abort before doing more work if already cancelled.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Wait for rate limiter
		c.limiter.wait()

		req, err := buildReq()
		if err != nil {
			return nil, fmt.Errorf("llm: format request: %w", err)
		}
		req = req.WithContext(ctx) // cancellation reaches the in-flight call

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("llm: request failed: %w", err)
		}

		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			return body, nil
		}

		if isRetryable(resp.StatusCode) {
			delay := backoffDelay(attempt)
			if resp.StatusCode == 429 {
				metrics.CounterNamed("llm_rate_limited_total").Inc() // first discrimination (P2-2)
			}
			log.Warn("retryable error, retrying", "status", resp.StatusCode, "attempt", attempt+1, "delay", delay)
			if attempt+1 < maxAttempts {
				// Cancellable backoff: a cancel during the sleep returns promptly.
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
				metrics.CounterNamed("llm_retries_total").Inc() // a retry actually ran (P2-2)
			} // final attempt: no sleep, no counter (no retry follows)
			lastStatusCode = resp.StatusCode
			lastErr = fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
			continue
		}

		return nil, &StatusError{Code: resp.StatusCode, Body: string(body)}
	}

	// If the final failure was a 429, return a typed RateLimitError
	// so BackpressureController can detect it and adjust concurrency.
	if lastStatusCode == 429 {
		return nil, &RateLimitError{
			StatusCode: 429,
			Body:       lastErr.Error(),
		}
	}

	return nil, fmt.Errorf("llm: max retries exceeded: %w", lastErr)
}

// StructuredCompletion issues messages with the provider's native JSON
// constraint and returns the parsed, schema-validated payload (spec §3).
// Providers without a mechanism take the plain-completion + fence-strip
// fallback, byte-identical to today. Call sites never branch.
//
// opts.RawFallback (CallOpts field, spec §4 amendment): return the raw
// plain-completion text in rawText instead of the shared fence-strip
// parse — for the two sites whose legacy parsers have no bracket-hunt
// tolerance (tools_write.go, grounding.go).
func (c *Client) StructuredCompletion(ctx context.Context, messages []Message, schema JSONSchema, opts CallOpts) (payload json.RawMessage, rawText string, err error) {
	buildReq, ok, err := c.provider.FormatStructuredRequest(messages, schema, opts)
	if err != nil {
		return nil, "", fmt.Errorf("llm: format structured request: %w", err)
	}
	if !ok {
		// Fallback: plain completion + the caller's chosen parsing path.
		resp, err := c.ChatCompletionCtx(ctx, messages, opts)
		if err != nil {
			return nil, "", err
		}
		if opts.RawFallback {
			return nil, resp.Content, nil
		}
		payload, err := ParseJSONFromText(resp.Content)
		if err != nil {
			return nil, "", err
		}
		if err := ValidateJSON(schema.Schema, payload); err != nil {
			return nil, "", err
		}
		return payload, "", nil
	}

	// Native path: prebuilt constrained request through the shared transport
	// (deliberately NON-cached, design D7).
	body, err := c.doWithRetry(ctx, buildReq)
	if err != nil {
		// OpenAI degrade: 400 mentioning the constraint field → ONE
		// json_object retry (envelope root for arrays; spec §3).
		var se *StatusError
		if errors.As(err, &se) && se.Code == 400 && mentionsConstraintField(se.Body) {
			degraded := schema
			degraded.Degraded = true
			if dreq, dok, derr := c.provider.FormatStructuredRequest(messages, degraded, opts); derr == nil && dok {
				body, err = c.doWithRetry(ctx, dreq)
			}
		}
		if err != nil {
			return nil, "", err
		}
	}

	// Usage/trackUsage fires exactly as on any completion. Anthropic's
	// forced tool_use legitimately returns empty text Content — the
	// empty-content guard is skipped only when the structured parse
	// succeeds (client-side carve-out, spec §3).
	if resp, rerr := c.provider.ParseResponse(body); rerr == nil {
		c.trackUsage(resp.Model, resp.Usage)
	}

	payload, err = c.provider.ParseStructuredResponse(body)
	if err != nil {
		return nil, "", fmt.Errorf("llm: parse structured response: %w", err)
	}

	// Validate against the envelope (arrays) or canonical schema (objects).
	target := schema.Schema
	if schema.IsArray {
		target = schema.Envelope()
	}
	if err := ValidateJSON(target, payload); err != nil {
		return nil, "", err // content-invalid: constraint worked, shape wrong — NO fallback
	}

	// Unwrap the envelope; both paths return the bare payload.
	if schema.IsArray {
		var env struct {
			Items json.RawMessage `json:"items"`
		}
		if err := json.Unmarshal(payload, &env); err != nil {
			return nil, "", fmt.Errorf("structured: envelope unwrap: %w", err)
		}
		return env.Items, "", nil
	}
	return payload, "", nil
}

func mentionsConstraintField(body string) bool {
	return contains(body, "response_format") || contains(body, "json_schema")
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
