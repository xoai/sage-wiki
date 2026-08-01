package serve

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTokenBucketAdmitsThen429(t *testing.T) {
	tb := NewTokenBucket(0.001, 2) // 2 burst, ~0 refill
	h := tb.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	codes := []int{}
	for i := 0; i < 4; i++ {
		r := httptest.NewRequest("GET", "/search", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		codes = append(codes, w.Code)
	}
	if codes[0] != 200 || codes[1] != 200 || codes[2] != 429 || codes[3] != 429 {
		t.Errorf("codes = %v, want [200 200 429 429]", codes)
	}
	// Different IP gets its own bucket.
	r := httptest.NewRequest("GET", "/search", nil)
	r.RemoteAddr = "10.0.0.2:1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("second IP should be admitted: %d", w.Code)
	}
}
