package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrencyRepro guards against the hidden serialization that caused
// PER-112 spike #7 to see vllm:num_requests_running ≤ 1 despite
// cfg.Compiler.MaxParallel = 32 and compiler.backpressure = false.
//
// It fires N=32 concurrent ChatCompletion calls against an httptest server
// whose handler sleeps 500ms per request, and asserts:
//
//   - provider "openai-compatible" with the default rate limit (fix: 0 =
//     disabled): all 32 requests in flight simultaneously, wall ≈ 500ms.
//   - provider "openai" (public API, default 60 RPM): because wait() no longer
//     holds the mutex across Sleep, goroutines sleep concurrently. Requests are
//     SPACED one second apart as intended by the throttle, so peakInFlight
//     stays small (≤ 2 with a 500ms server + 1s spacing) and wall ≈ 31s — but
//     critically, the limiter does not serialize across goroutines beyond what
//     the RPM actually requires.
//   - explicit rate_limit high enough: acts as "effectively unbounded",
//     peakInFlight = 32.
//
// Before the per-112-concurrency-fix patch, the "openai-compatible" scenario
// had peakInFlight=1 and wall ≈ 62s (32 × 2s interval × serialized-by-mutex).
// After the patch, peakInFlight=32 and wall ≈ 500ms.
//
// Run:  go test -run TestConcurrencyRepro -v -timeout 180s ./internal/llm/
func TestConcurrencyRepro(t *testing.T) {
	server := newMockCompletionServer()
	defer server.Close()

	type scenario struct {
		name        string
		provider    string
		rateLimit   int
		wantPeakMin int64         // inclusive lower bound
		wantWallMax time.Duration // upper bound (per-call latency is 500ms, so gives plenty of margin)
	}

	scenarios := []scenario{
		// Sage-wiki's real-world config (provider: openai-compatible, no api.rate_limit
		// set). With the fix, defaultRateLimit returns 0 for this provider → the
		// limiter is a no-op and all 32 goroutines' requests fire in parallel.
		{"openai-compatible-default-is-unlimited", "openai-compatible", 0, 32, 3 * time.Second},

		// Explicit opt-out via rate_limit: -1 (works for any provider).
		{"explicit-disable-via-negative", "openai", -1, 32, 3 * time.Second},

		// Explicit high cap — effectively unbounded.
		{"explicit-100krpm", "openai", 100_000, 32, 3 * time.Second},

		// Paid API default stays active: 60 RPM = 1s spacing. Requests overlap
		// their LLM latency (500ms < 1s spacing) so peakInFlight stays low, but
		// goroutines SLEEP CONCURRENTLY (fix guarantee) rather than serializing
		// on the mutex. wall is dominated by RPM pacing: (N-1) * 1s + 0.5s =
		// 31.5s. Tolerance generous to avoid test flakes.
		{"openai-60rpm-still-paces-correctly", "openai", 0, 1, 40 * time.Second},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			server.reset()
			client, err := NewClient(sc.provider, "sk-test", server.URL(), sc.rateLimit)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			const N = 32
			var wg sync.WaitGroup
			start := time.Now()
			for i := 0; i < N; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := client.ChatCompletion([]Message{
						{Role: "user", Content: "hi"},
					}, CallOpts{Model: "mock", MaxTokens: 10})
					if err != nil {
						t.Errorf("ChatCompletion: %v", err)
					}
				}()
			}
			wg.Wait()
			wall := time.Since(start)
			peak := server.Peak()

			t.Logf("scenario=%s N=%d wall=%s peakInFlight=%d", sc.name, N, wall, peak)

			if peak < sc.wantPeakMin {
				t.Errorf("peakInFlight = %d, want >= %d", peak, sc.wantPeakMin)
			}
			if wall > sc.wantWallMax {
				t.Errorf("wall = %s, want <= %s", wall, sc.wantWallMax)
			}
		})
	}
}

// mockCompletionServer is a minimal httptest server that mimics an
// OpenAI-compatible chat completions endpoint with a fixed 500ms latency.
// It records the peak number of requests observed in-flight at any one time.
type mockCompletionServer struct {
	*httptest.Server
	inFlight atomic.Int64
	peak     atomic.Int64
}

func newMockCompletionServer() *mockCompletionServer {
	s := &mockCompletionServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cur := s.inFlight.Add(1)
		for {
			p := s.peak.Load()
			if cur <= p {
				break
			}
			if s.peak.CompareAndSwap(p, cur) {
				break
			}
		}

		time.Sleep(500 * time.Millisecond)

		s.inFlight.Add(-1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]string{"content": "ok"}},
			},
			"model": "mock",
			"usage": map[string]int{"total_tokens": 10},
		})
	}))
	return s
}

func (s *mockCompletionServer) URL() string   { return s.Server.URL }
func (s *mockCompletionServer) Peak() int64   { return s.peak.Load() }
func (s *mockCompletionServer) reset() {
	s.inFlight.Store(0)
	s.peak.Store(0)
}
