package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthTransportInjectsBearer(t *testing.T) {
	var receivedHeaders http.Header

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		w.WriteHeader(200)
	}))
	defer backend.Close()

	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))
	store.Put("openai", &Credential{
		AccessToken:  "at-test-bearer",
		RefreshToken: "rt-test",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		AccountID:    "acct-xyz",
		Source:       "login",
	})

	transport := NewAuthTransport(http.DefaultTransport, store, "openai")
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("GET", backend.URL, nil)
	req.Header.Set("x-api-key", "should-be-stripped")
	req.Header.Set("Authorization", "Bearer old-token")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if got := receivedHeaders.Get("Authorization"); got != "Bearer at-test-bearer" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer at-test-bearer")
	}
	if got := receivedHeaders.Get("x-api-key"); got != "" {
		t.Errorf("x-api-key should be stripped, got %q", got)
	}
	if got := receivedHeaders.Get("ChatGPT-Account-ID"); got != "acct-xyz" {
		t.Errorf("ChatGPT-Account-ID = %q, want %q", got, "acct-xyz")
	}
}

func TestAuthTransportStripsGeminiKeyParam(t *testing.T) {
	var receivedURL string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedURL = r.URL.String()
		w.WriteHeader(200)
	}))
	defer backend.Close()

	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))
	store.Put("gemini", &Credential{
		AccessToken:  "ya29.test-gemini",
		RefreshToken: "rt-gemini",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		Source:       "import",
	})

	transport := NewAuthTransport(http.DefaultTransport, store, "gemini")
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("GET", backend.URL+"/v1/models/gemini-pro:generateContent?key=&alt=sse", nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if receivedURL == "" {
		t.Fatal("no request received")
	}
	if q, _ := http.NewRequest("GET", receivedURL, nil); q.URL.Query().Has("key") {
		t.Errorf("key param should be stripped, got URL: %s", receivedURL)
	}
}

func TestAuthTransportClonesRequest(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer backend.Close()

	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))
	store.Put("anthropic", &Credential{
		AccessToken:  "sk-ant-test",
		RefreshToken: "rt-test",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		Source:       "login",
	})

	transport := NewAuthTransport(http.DefaultTransport, store, "anthropic")
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("GET", backend.URL, nil)
	req.Header.Set("x-api-key", "original-key")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Original request should not be modified
	if got := req.Header.Get("x-api-key"); got != "original-key" {
		t.Errorf("original request was mutated: x-api-key = %q", got)
	}
}

func TestAuthTransportRefreshesExpiredToken(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "at-refreshed",
			"refresh_token": "rt-new",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	origCfg := Providers["openai"]
	Providers["openai"] = ProviderConfig{
		TokenURL: tokenServer.URL,
		ClientID: "test-client",
		FlowType: FlowPKCE,
	}
	defer func() { Providers["openai"] = origCfg }()

	var receivedAuth string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(200)
	}))
	defer backend.Close()

	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))
	store.Put("openai", &Credential{
		AccessToken:  "at-expired",
		RefreshToken: "rt-old",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(),
		Source:       "login",
	})

	transport := NewAuthTransport(http.DefaultTransport, store, "openai")
	client := &http.Client{Transport: transport}

	req, _ := http.NewRequest("GET", backend.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if receivedAuth != "Bearer at-refreshed" {
		t.Errorf("Authorization = %q, want refreshed token", receivedAuth)
	}
}

func TestAuthTransportConcurrentRefresh(t *testing.T) {
	var refreshCount atomic.Int32

	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refreshCount.Add(1)
		time.Sleep(50 * time.Millisecond) // simulate network latency
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "at-concurrent-refreshed",
			"refresh_token": "rt-new",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	origCfg := Providers["openai"]
	Providers["openai"] = ProviderConfig{
		TokenURL: tokenServer.URL,
		ClientID: "test-client",
		FlowType: FlowPKCE,
	}
	defer func() { Providers["openai"] = origCfg }()

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer backend.Close()

	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))
	store.Put("openai", &Credential{
		AccessToken:  "at-expired",
		RefreshToken: "rt-old",
		ExpiresAt:    time.Now().Add(-1 * time.Hour).Unix(),
		Source:       "login",
	})

	transport := NewAuthTransport(http.DefaultTransport, store, "openai")
	client := &http.Client{Transport: transport}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("GET", backend.URL, nil)
			resp, err := client.Do(req)
			if err != nil {
				t.Errorf("request failed: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
	wg.Wait()

	// Double-checked locking should result in exactly 1 refresh
	count := refreshCount.Load()
	if count != 1 {
		t.Errorf("expected 1 refresh call, got %d (double-checked locking may have failed)", count)
	}
}
