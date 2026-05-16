package llm

import "net/http"

// nonBatchProvider wraps openaiProvider for OpenAI-compatible backends that
// implement chat completion (and streaming) but NOT the OpenAI Files/Batches
// API. Used for Ollama, Qwen/DashScope, vLLM, LocalAI, LiteLLM, etc.
//
// The wrapper uses a named field (not embedding) deliberately: embedding
// would promote the underlying openaiProvider's SubmitBatch/PollBatch/
// RetrieveBatch methods, making this type satisfy BatchProvider — which is
// exactly the bug we're fixing (issue #83). Methods are forwarded explicitly
// for Provider and StreamingProvider only.
type nonBatchProvider struct {
	inner *openaiProvider
}

// --- Provider interface ---

func (p *nonBatchProvider) Name() string         { return p.inner.Name() }
func (p *nonBatchProvider) SupportsVision() bool { return p.inner.SupportsVision() }

func (p *nonBatchProvider) FormatRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return p.inner.FormatRequest(messages, opts)
}

func (p *nonBatchProvider) ParseResponse(body []byte) (*Response, error) {
	return p.inner.ParseResponse(body)
}

// --- StreamingProvider interface ---

func (p *nonBatchProvider) FormatStreamRequest(messages []Message, opts CallOpts) (*http.Request, error) {
	return p.inner.FormatStreamRequest(messages, opts)
}

func (p *nonBatchProvider) ParseStreamChunk(data []byte) (string, bool) {
	return p.inner.ParseStreamChunk(data)
}

// Intentionally NOT implemented (would re-enable false BatchProvider claim):
//   - SubmitBatch
//   - PollBatch
//   - RetrieveBatch
