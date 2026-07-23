package web

import (
	"net/http/httptest"
	"testing"

	"github.com/xoai/sage-wiki/internal/metrics"
)

// Endpoint matrix per spec §7.4:
// flag true → 200 + series; flag off → 404 (no token) / 401 (token set);
// token set → 401 without bearer (loopback included); no token → 200.

func TestMetricsEndpointFlagOn(t *testing.T) {
	metrics.ResetForTest()
	s := setupTestProject(t)
	s.cfg.Serve.Metrics = true
	metrics.CounterNamed("test_series_total").Inc()

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackReq("GET", "/metrics"))
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body := rec.Body.String(); !containsStr(body, "test_series_total") {
		t.Errorf("series missing: %.200s", body)
	}
}

func TestMetricsEndpointFlagOff(t *testing.T) {
	s := setupTestProject(t)
	s.cfg.Serve.Metrics = false
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackReq("GET", "/metrics"))
	if rec.Code != 404 {
		t.Errorf("flag off, no token: status = %d, want 404", rec.Code)
	}
}

func TestMetricsEndpointTokenGated(t *testing.T) {
	metrics.ResetForTest()
	s := setupTestProject(t)
	s.cfg.Serve.Metrics = true
	s.SetAuth("secret-token", nil)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackReq("GET", "/metrics"))
	if rec.Code != 401 {
		t.Errorf("token set, no bearer: status = %d, want 401", rec.Code)
	}

	req := loopbackReq("GET", "/metrics")
	req.Header.Set("Authorization", "Bearer secret-token")
	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, req)
	if rec2.Code != 200 {
		t.Errorf("token set, with bearer: status = %d, want 200", rec2.Code)
	}
}

func TestMetricsEndpointFlagOffWithToken(t *testing.T) {
	s := setupTestProject(t)
	s.cfg.Serve.Metrics = false
	s.SetAuth("secret-token", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackReq("GET", "/metrics"))
	// Gate evaluates before routing: 401 when token set, not 404.
	if rec.Code != 401 {
		t.Errorf("flag off + token set: status = %d, want 401", rec.Code)
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
