package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/xoai/sage-wiki/internal/providerutil"
	publicprovider "github.com/xoai/sage-wiki/pkg/provider"
)

var ErrInjectedNilResponse = errors.New("llm: injected provider returned a nil response")

const InjectedProviderName = "injected"

type injectedCompletion struct {
	provider publicprovider.Provider
}

type injectedProviderAdapter struct{}

func (injectedProviderAdapter) Name() string { return InjectedProviderName }

func (injectedProviderAdapter) FormatRequest([]Message, CallOpts) (*http.Request, error) {
	return nil, errors.New("llm: injected provider request bypassed direct completion")
}

func (injectedProviderAdapter) ParseResponse([]byte) (*Response, error) {
	return nil, errors.New("llm: injected provider response bypassed wire parsing")
}

func (injectedProviderAdapter) SupportsVision() bool { return false }

func (injectedProviderAdapter) FormatStructuredRequest([]Message, JSONSchema, CallOpts) (func() (*http.Request, error), bool, error) {
	return nil, false, nil
}

func (injectedProviderAdapter) ParseStructuredResponse([]byte) (json.RawMessage, error) {
	return nil, errors.New("llm: injected provider has no native structured response")
}

// NewClientWithProvider builds a completion client around the public provider
// surface. The caller-owned provider handles retries and rate limiting.
func NewClientWithProvider(p publicprovider.Provider) (*Client, error) {
	if providerutil.IsNil(p) {
		return nil, errors.New("llm: injected provider is nil")
	}
	return &Client{
		provider:     injectedProviderAdapter{},
		providerName: InjectedProviderName,
		injected:     &injectedCompletion{provider: p},
		limiter:      newRateLimiter(-1),
		tier:         TierNotCompileScoped,
		client:       http.Client{Transport: sharedTransport, Timeout: 120 * time.Second},
		callTimeout:  120 * time.Second,
	}, nil
}

func (c *Client) completeInjected(ctx context.Context, messages []Message, opts CallOpts) (*Response, error) {
	requestMessages := make([]publicprovider.Message, len(messages))
	for i, message := range messages {
		requestMessages[i] = publicprovider.Message{Role: message.Role, Content: message.Content}
	}
	request := publicprovider.CompleteRequest{
		Messages:  requestMessages,
		Model:     opts.Model,
		MaxTokens: opts.MaxTokens,
		Tier:      c.tier,
	}
	callCtx := ctx
	var cancel context.CancelFunc
	if c.callTimeout > 0 {
		callCtx, cancel = context.WithTimeout(ctx, c.callTimeout)
		defer cancel()
	}
	response, err := c.injected.provider.Complete(callCtx, request)
	if err != nil {
		return nil, fmt.Errorf("llm: injected provider completion: %w", err)
	}
	if response == nil {
		return nil, ErrInjectedNilResponse
	}
	model := response.Model
	if model == "" {
		model = opts.Model
	}
	usage := Usage{
		InputTokens:      response.Usage.InputTokens,
		CachedTokens:     response.Usage.CachedTokens,
		CacheWriteTokens: response.Usage.CacheWriteTokens,
		OutputTokens:     response.Usage.OutputTokens,
	}
	c.trackUsage(ctx, model, usage)
	return &Response{
		Content:    response.Content,
		Model:      model,
		TokensUsed: usage.InputTokens + usage.OutputTokens,
		Usage:      usage,
	}, nil
}
