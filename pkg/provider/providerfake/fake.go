// Package providerfake is a deterministic, offline Provider for tests and
// examples (SPEC-01 AC-B2). It performs no network I/O.
package providerfake

import (
	"context"
	"hash/fnv"
	"strings"
	"sync"

	"github.com/xoai/sage-wiki/pkg/provider"
)

// Fake is a scriptable provider. Completions are matched by substring of
// the last user message; embeddings are deterministic content hashes.
type Fake struct {
	mu        sync.Mutex
	Responses map[string]string // substring → content (longest key wins)
	Default   string            // content when nothing matches
	Model     string            // reported model id (default "fake-model")
	Dims      int               // embedding dimensions (default 8)
	Calls     []provider.CompleteRequest
}

// New returns a Fake with the given default response content.
func New(defaultContent string) *Fake {
	return &Fake{Responses: map[string]string{}, Default: defaultContent}
}

func (f *Fake) model() string {
	if f.Model != "" {
		return f.Model
	}
	return "fake-model"
}

// Complete records the call and returns the scripted response. Usage is a
// deterministic function of the request (4 chars/token heuristic).
func (f *Fake) Complete(_ context.Context, req provider.CompleteRequest) (*provider.CompleteResponse, error) {
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	f.mu.Unlock()

	last := ""
	for _, m := range req.Messages {
		if m.Role == "user" {
			last = m.Content
		}
	}
	content := f.Default
	best := 0
	for key, resp := range f.Responses {
		if strings.Contains(last, key) && len(key) > best {
			best = len(key)
			content = resp
		}
	}
	input := 0
	for _, m := range req.Messages {
		input += len(m.Content) / 4
	}
	return &provider.CompleteResponse{
		Content: content,
		Model:   f.model(),
		Usage:   provider.Usage{InputTokens: input, OutputTokens: len(content) / 4},
	}, nil
}

// Embed returns deterministic per-text vectors derived from the content
// hash — no network, stable across runs.
func (f *Fake) Embed(_ context.Context, texts []string) ([][]float32, error) {
	dims := f.Dims
	if dims <= 0 {
		dims = 8
	}
	out := make([][]float32, len(texts))
	for i, text := range texts {
		vec := make([]float32, dims)
		for d := 0; d < dims; d++ {
			h := fnv.New32a()
			h.Write([]byte(text))
			h.Write([]byte{byte(d)})
			vec[d] = float32(h.Sum32()%1000) / 1000.0
		}
		out[i] = vec
	}
	return out, nil
}

// Models lists the single fake model (no pricing — unknown).
func (f *Fake) Models(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: f.model(), Family: "fake"}}, nil
}

// Called reports whether any recorded request's last user message contains
// the substring — the example/test assertion seam.
func (f *Fake) Called(substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.Calls {
		for _, m := range c.Messages {
			if m.Role == "user" && strings.Contains(m.Content, substr) {
				return true
			}
		}
	}
	return false
}

// SortedCalls returns the recorded requests in order (copy).
func (f *Fake) SortedCalls() []provider.CompleteRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]provider.CompleteRequest, len(f.Calls))
	copy(out, f.Calls)
	return out
}
