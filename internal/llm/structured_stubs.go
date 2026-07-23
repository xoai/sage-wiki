package llm

import (
	"encoding/json"
	"net/http"
)

// Structured-output stubs (P2-4 T2): keep the interface total while the
// real implementations land (T3 anthropic, T4 openai, T5 gemini).
// Wrappers (openai-compatible/qwen/ollama via nonBatchProvider) return
// ok == false UNCONDITIONALLY — even wrapping a raw openai provider
// (design D2: byte-identical fallback for openai-compatible).

func (p *nonBatchProvider) FormatStructuredRequest(messages []Message, schema JSONSchema, opts CallOpts) (func() (*http.Request, error), bool, error) {
	return nil, false, nil // pinned: wrappers always fall back (design D2)
}

func (p *nonBatchProvider) ParseStructuredResponse(body []byte) (json.RawMessage, error) {
	return nil, ErrStructuredUnsupported
}
