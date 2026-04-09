package llm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/log"
)

// StreamCallback is called for each token during streaming.
type StreamCallback func(token string)

// ChatCompletionStream sends a streaming chat completion request.
// The callback is called for each content token. Returns the full response when done.
// The context can be used to cancel the request (e.g. on client disconnect).
func (c *Client) ChatCompletionStream(ctx context.Context, messages []Message, opts CallOpts, cb StreamCallback) (*Response, error) {
	sp, ok := c.provider.(StreamingProvider)
	if !ok {
		// Fallback: non-streaming with single callback at end
		log.Warn("provider does not support streaming, falling back to non-streaming")
		resp, err := c.ChatCompletion(messages, opts)
		if err != nil {
			return nil, err
		}
		cb(resp.Content)
		return resp, nil
	}

	c.limiter.wait()

	req, err := sp.FormatStreamRequest(messages, opts)
	if err != nil {
		return nil, fmt.Errorf("llm: format stream request: %w", err)
	}
	req = req.WithContext(ctx)

	httpClient := http.Client{Timeout: 180 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm: stream request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("llm: API returned %d: %s", resp.StatusCode, string(body))
	}

	// Read SSE events
	var fullContent strings.Builder
	scanner := bufio.NewScanner(resp.Body)

	for scanner.Scan() {
		// Check for context cancellation
		if ctx.Err() != nil {
			return &Response{Content: stripThinkTags(fullContent.String()), Model: opts.Model}, ctx.Err()
		}

		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		token, done := sp.ParseStreamChunk([]byte(data))
		if token != "" {
			fullContent.WriteString(token)
			cb(token)
		}
		if done {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return &Response{Content: stripThinkTags(fullContent.String()), Model: opts.Model}, fmt.Errorf("llm: stream read error: %w", err)
	}

	return &Response{
		Content: stripThinkTags(fullContent.String()),
		Model:   opts.Model,
	}, nil
}

// StreamingProvider extends Provider with streaming capabilities.
type StreamingProvider interface {
	FormatStreamRequest(messages []Message, opts CallOpts) (*http.Request, error)
	ParseStreamChunk(data []byte) (token string, done bool)
}
