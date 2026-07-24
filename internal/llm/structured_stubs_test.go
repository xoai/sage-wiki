package llm

import (
	"testing"
)

// TestWrappersAlwaysFallback pins design D2: openai-compatible / qwen /
// ollama (nonBatchProvider, even wrapping a raw openai provider) return
// ok == false unconditionally, and their ParseStructuredResponse is
// ErrStructuredUnsupported — byte-identical fallback for those backends.
func TestWrappersAlwaysFallback(t *testing.T) {
	inner := newOpenAIProvider("k", "https://api.test")
	for _, p := range []Provider{
		&nonBatchProvider{inner: inner},
		&nonBatchProvider{inner: inner},
		&nonBatchProvider{inner: inner},
	} {
		_, ok, err := p.FormatStructuredRequest(nil, testSchema(), CallOpts{})
		if err != nil {
			t.Errorf("%s: %v", p.Name(), err)
		}
		if ok {
			t.Errorf("%s: wrapper must return ok==false even wrapping raw openai", p.Name())
		}
		if _, err := p.ParseStructuredResponse([]byte(`{}`)); err != ErrStructuredUnsupported {
			t.Errorf("%s: ParseStructuredResponse = %v", p.Name(), err)
		}
	}
}
