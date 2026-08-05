package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
)

// backoffDelayFn is the sleep-duration hook for every retry branch
// (tests swap it for instant retries — deterministic and fast).
var backoffDelayFn = backoffDelay

// SetBackoffDelayForTest overrides the retry backoff hook package-wide and
// returns a restore function. For tests only — production code must not
// call this.
func SetBackoffDelayForTest(fn func(attempt int) time.Duration) (restore func()) {
	prev := backoffDelayFn
	backoffDelayFn = fn
	return func() { backoffDelayFn = prev }
}

// doWithRetry executes buildReq with the direct path's retry discipline —
// THE single transport the client uses (P2-4 extraction): rate limiter,
// ctx-annotated request, 429 first-discrimination + typed RateLimitError,
// cancellable backoff with retry counting, StatusError on non-retryable.
// buildReq is called per attempt (fresh body — Do consumes it).
//
// Issue #114 extensions:
//   - A body READ error (dropped mid-body connection) is no longer
//     discarded: it is retried as transport-transient and preserved as
//     lastErr so errors.Is(io.ErrUnexpectedEOF) survives the final wrap.
//   - validate (when non-nil) checks a 200 body INSIDE the loop.
//     Truncation-class failures (IsTruncatedBodyErr) retry with the same
//     backoff discipline; deterministic failures return (body, verr) —
//     body != nil is the discriminant telling the caller to wrap verr and
//     run its usual accounting instead of treating it as transport failure.
//   - Both new branches reset lastStatusCode so a stale 429 can't produce
//     a typed RateLimitError wrapping a non-rate-limit failure, and honor
//     the final-attempt guard (no sleep, no counter when no retry follows).
func (c *Client) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error), validate func([]byte) error, errLabel string) ([]byte, error) {
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
		// SPEC-08 provider_timeout: bound each attempt. context.WithTimeout
		// takes the minimum of the parent deadline and the timeout, so a
		// shorter caller deadline always wins.
		reqCtx := ctx
		var cancelAttempt context.CancelFunc
		if c.callTimeout > 0 {
			reqCtx, cancelAttempt = context.WithTimeout(ctx, c.callTimeout)
		}
		req = req.WithContext(reqCtx) // cancellation reaches the in-flight call

		resp, err := c.client.Do(req)
		if err != nil {
			if cancelAttempt != nil {
				cancelAttempt()
			}
			return nil, fmt.Errorf("llm: request failed: %w", err)
		}

		body, rerr := io.ReadAll(resp.Body)
		resp.Body.Close()
		// SPEC-08: cancelAttempt fires AFTER the body is consumed, so the
		// read error's identity (e.g. io.ErrUnexpectedEOF) survives —
		// cancelling before ReadAll destroys it via Go's HTTP transport
		// close path (issue #114 flake on CI).
		if cancelAttempt != nil {
			cancelAttempt()
		}
		if rerr != nil {
			// Dropped mid-body — transient transport failure. Retry, and
			// preserve the error identity (issue #114 R2: previously
			// laundered into a parse error via the discarded read error).
			delay := backoffDelayFn(attempt)
			log.Warn("response body read failed, retrying", "error", rerr, "attempt", attempt+1, "delay", delay)
			if attempt+1 < maxAttempts {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
				metrics.CounterNamed("llm_retries_total").Inc()
			}
			lastStatusCode = 0
			lastErr = fmt.Errorf("llm: read response body: %w", rerr)
			continue
		}

		if resp.StatusCode == http.StatusOK {
			verr := validate(body)
			if verr == nil {
				return body, nil
			}
			if !IsTruncatedBodyErr(verr) {
				// Deterministic (well-formed-but-invalid): fail fast,
				// body != nil tells the caller this is a validation
				// failure, not a transport failure (issue #114 R4).
				return body, verr
			}
			delay := backoffDelayFn(attempt)
			log.Warn("truncated response body, retrying", "error", verr, "attempt", attempt+1, "delay", delay)
			if attempt+1 < maxAttempts {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(delay):
				}
				metrics.CounterNamed("llm_retries_total").Inc()
			}
			lastStatusCode = 0
			lastErr = fmt.Errorf("%s: %w", errLabel, verr)
			continue
		}

		if isRetryable(resp.StatusCode) {
			delay := backoffDelayFn(attempt)
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
		return c.structuredFallback(ctx, messages, schema, opts)
	}

	// Native path: prebuilt constrained request through the shared transport
	// (deliberately NON-cached, design D7). The validate closure runs
	// ParseStructuredResponse INSIDE the retry loop (issue #114): truncated
	// 200 bodies retry; the captured payload is reused below so a success
	// doesn't parse twice.
	var captured json.RawMessage
	validate := func(body []byte) error {
		p, err := c.provider.ParseStructuredResponse(body)
		if err != nil {
			return err
		}
		captured = p
		return nil
	}
	body, err := c.doWithRetry(ctx, buildReq, validate, "parse structured response")
	if err != nil && body == nil {
		// OpenAI degrade: 400 mentioning the constraint field → ONE
		// json_object retry (envelope root for arrays; spec §3).
		var se *StatusError
		if errors.As(err, &se) && se.Code == 400 && mentionsConstraintField(se.Body) {
			// The json_object degrade is an OpenAI mechanism; providers whose
			// formatter ignores Degraded (anthropic/gemini) must not resend
			// the identical request. Pinned: only openaiProvider degrades.
			if _, isOpenAI := c.provider.(*openaiProvider); isOpenAI {
				degraded := schema
				degraded.Degraded = true
				if dreq, dok, derr := c.provider.FormatStructuredRequest(messages, degraded, opts); derr == nil && dok {
					body, err = c.doWithRetry(ctx, dreq, validate, "parse structured response")
				}
			}
		}
		if err != nil && body == nil {
			// Spec §3: a second 400 (degrade failed) or a 400 without the
			// field mention → plain completion + fence-strip fallback.
			var se *StatusError
			if errors.As(err, &se) && se.Code == 400 {
				return c.structuredFallback(ctx, messages, schema, opts)
			}
			return nil, "", err
		}
	}

	// Usage/trackUsage + the parsed response (for the empty-content hint).
	resp, rerr := c.provider.ParseResponse(body)
	if rerr != nil {
		log.Warn("structured: usage parse failed — tokens untracked", "error", rerr)
	}
	if resp != nil {
		c.trackUsage(ctx, resp.Model, resp.Usage)
	}

	// On a validated success, reuse the payload captured inside the loop.
	// On a (body, verr) deterministic validation failure, surface verr
	// exactly as the pre-#114 code surfaced ParseStructuredResponse's error.
	payload, serr := captured, error(nil)
	if err != nil {
		payload, serr = nil, err
	}
	if serr != nil || len(strings.TrimSpace(string(payload))) == 0 {
		// Anthropic's forced tool_use legitimately returns empty text
		// Content — but then the structured parse succeeded with the tool
		// input as payload, so this hint only fires when the payload is
		// ALSO empty/failed (client-side carve-out, spec §3).
		if resp != nil && strings.TrimSpace(resp.Content) == "" && serr == nil {
			// Anthropic carve-out: forced tool_use has empty text Content
			// but a VALID tool-input payload — handled above, so reaching
			// here with empty payload + empty content is a real failure.
			// When serr != nil, the real parse error wins over the hint.
			return nil, "", fmt.Errorf("llm: structured completion: %s", resp.EmptyContentDetails())
		}
		if serr != nil {
			return nil, "", fmt.Errorf("llm: parse structured response: %w", serr)
		}
		return nil, "", fmt.Errorf("llm: structured completion: empty payload")
	}

	// Validate: envelope first, then the bare array schema (tolerant of
	// providers and test doubles that answer the envelope-shaped request
	// with the bare array — both are schema-validated; no silent pass).
	if schema.IsArray {
		if err := ValidateJSON(schema.Envelope(), payload); err == nil {
			var env struct {
				Items json.RawMessage `json:"items"`
			}
			if uerr := json.Unmarshal(payload, &env); uerr != nil {
				return nil, "", fmt.Errorf("structured: envelope unwrap: %w", uerr)
			}
			return env.Items, "", nil
		}
		if err := ValidateJSON(schema.Schema, payload); err != nil {
			return nil, "", err // content-invalid: constraint worked, shape wrong — NO fallback
		}
		return payload, "", nil
	}
	if err := ValidateJSON(schema.Schema, payload); err != nil {
		return nil, "", err
	}
	return payload, "", nil
}

// structuredFallback is the plain-completion + fence-strip path (used
// when the provider has no mechanism AND when the mechanism is rejected
// with a 400 — spec §3).
func (c *Client) structuredFallback(ctx context.Context, messages []Message, schema JSONSchema, opts CallOpts) (json.RawMessage, string, error) {
	resp, err := c.ChatCompletionCtx(ctx, messages, opts)
	if err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(resp.Content) == "" {
		// Preserve the actionable empty-content hint (finish_reason /
		// reasoning / raise-budget) on the fallback path.
		return nil, "", fmt.Errorf("llm: structured completion: %s", resp.EmptyContentDetails())
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

func mentionsConstraintField(body string) bool {
	return strings.Contains(body, "response_format") || strings.Contains(body, "json_schema")
}
