package auth

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestGenerateVerifier(t *testing.T) {
	v, err := generateVerifier()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) < 43 {
		t.Errorf("verifier too short: %d chars", len(v))
	}
	// Must be base64url without padding
	if _, err := base64.RawURLEncoding.DecodeString(v); err != nil {
		t.Errorf("verifier is not valid base64url: %v", err)
	}

	v2, _ := generateVerifier()
	if v == v2 {
		t.Error("two verifiers should not be identical")
	}
}

func TestGenerateChallenge(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	challenge := generateChallenge(verifier)

	// Verify it's SHA256 of the verifier, base64url-encoded
	h := sha256.Sum256([]byte(verifier))
	expected := base64.RawURLEncoding.EncodeToString(h[:])
	if challenge != expected {
		t.Errorf("challenge = %q, want %q", challenge, expected)
	}

	// Must be base64url without padding
	if _, err := base64.RawURLEncoding.DecodeString(challenge); err != nil {
		t.Errorf("challenge is not valid base64url: %v", err)
	}
}

func TestGenerateState(t *testing.T) {
	s, err := generateState()
	if err != nil {
		t.Fatal(err)
	}
	if len(s) != 32 { // 16 bytes hex-encoded
		t.Errorf("state length = %d, want 32", len(s))
	}

	s2, _ := generateState()
	if s == s2 {
		t.Error("two states should not be identical")
	}
}

func TestCallbackServer(t *testing.T) {
	result := make(chan callbackResult, 1)
	port, shutdown, err := startCallbackServer(0, "/callback", result)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()

	// Simulate browser redirect to callback
	callbackURL := fmt.Sprintf("http://localhost:%d/callback?code=test-auth-code&state=test-state", port)
	resp, err := http.Get(callbackURL)
	if err != nil {
		t.Fatalf("callback request failed: %v", err)
	}
	resp.Body.Close()

	select {
	case r := <-result:
		if r.code != "test-auth-code" {
			t.Errorf("code = %q, want %q", r.code, "test-auth-code")
		}
		if r.state != "test-state" {
			t.Errorf("state = %q, want %q", r.state, "test-state")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for callback result")
	}
}

func TestCallbackServerWrongPath(t *testing.T) {
	result := make(chan callbackResult, 1)
	port, shutdown, err := startCallbackServer(0, "/callback", result)
	if err != nil {
		t.Fatal(err)
	}
	defer shutdown()

	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/wrong-path?code=x&state=y", port))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}

	select {
	case <-result:
		t.Error("should not receive result on wrong path")
	case <-time.After(100 * time.Millisecond):
		// expected
	}
}

func TestExchangeCodeForTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}

		r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("grant_type = %q", r.Form.Get("grant_type"))
		}
		if r.Form.Get("code") != "auth-code-123" {
			t.Errorf("code = %q", r.Form.Get("code"))
		}
		if r.Form.Get("code_verifier") != "my-verifier" {
			t.Errorf("code_verifier = %q", r.Form.Get("code_verifier"))
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "at-new-token",
			"refresh_token": "rt-new-token",
			"expires_in":    3600,
		})
	}))
	defer server.Close()

	tok, err := exchangeCodeForTokens(server.URL, "auth-code-123", "my-verifier", "client-id", "http://localhost:1234/callback")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "at-new-token" {
		t.Errorf("AccessToken = %q", tok.AccessToken)
	}
	if tok.RefreshToken != "rt-new-token" {
		t.Errorf("RefreshToken = %q", tok.RefreshToken)
	}
	if tok.ExpiresAt <= time.Now().Unix() {
		t.Error("ExpiresAt should be in the future")
	}
}

func TestExtractAccountID(t *testing.T) {
	// Build a mock JWT with the OpenAI claim
	payload := map[string]interface{}{
		"sub": "user-123",
		"https://api.openai.com/auth.chatgpt_account_id": "acct-abc-456",
	}
	payloadJSON, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	// JWT format: header.payload.signature
	fakeJWT := "eyJhbGciOiJSUzI1NiJ9." + payloadB64 + ".fake-signature"

	accountID := extractAccountID(fakeJWT, "https://api.openai.com/auth.chatgpt_account_id")
	if accountID != "acct-abc-456" {
		t.Errorf("accountID = %q, want %q", accountID, "acct-abc-456")
	}
}

func TestExtractAccountIDMissingClaim(t *testing.T) {
	payload := map[string]interface{}{"sub": "user-123"}
	payloadJSON, _ := json.Marshal(payload)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	fakeJWT := "eyJhbGciOiJSUzI1NiJ9." + payloadB64 + ".fake-signature"

	accountID := extractAccountID(fakeJWT, "https://api.openai.com/auth.chatgpt_account_id")
	if accountID != "" {
		t.Errorf("expected empty accountID, got %q", accountID)
	}
}

func TestExtractAccountIDInvalidJWT(t *testing.T) {
	accountID := extractAccountID("not-a-jwt", "claim")
	if accountID != "" {
		t.Errorf("expected empty accountID for invalid JWT, got %q", accountID)
	}
}

func TestLoginPKCEFullFlow(t *testing.T) {
	// Mock token endpoint
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		if r.Form.Get("grant_type") != "authorization_code" {
			t.Errorf("unexpected grant_type: %s", r.Form.Get("grant_type"))
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token":  "at-test-login",
			"refresh_token": "rt-test-login",
			"expires_in":    3600,
		})
	}))
	defer tokenServer.Close()

	// Override provider config to point to mock server
	origCfg := Providers["openai"]
	Providers["openai"] = ProviderConfig{
		AuthorizeURL: origCfg.AuthorizeURL,
		TokenURL:     tokenServer.URL,
		ClientID:     origCfg.ClientID,
		RedirectPort: 0, // will be overridden by callback server
		RedirectPath: "/auth/callback",
		Scopes:       origCfg.Scopes,
		FlowType:     FlowPKCE,
	}
	defer func() { Providers["openai"] = origCfg }()

	dir := t.TempDir()
	store := NewStore(dir + "/auth.json")

	var loginSuccess bool
	err := LoginPKCE("openai", store, LoginCallbacks{
		OnManualURL: func(authorizeURL string) string {
			// Parse the authorize URL to get the state parameter
			u, _ := url.Parse(authorizeURL)
			state := u.Query().Get("state")
			// Return a fake redirect URL with the state and a code
			return fmt.Sprintf("http://localhost:9999/auth/callback?code=manual-code&state=%s", state)
		},
		OnSuccess: func(provider string) {
			loginSuccess = true
		},
	})
	if err != nil {
		t.Fatalf("LoginPKCE: %v", err)
	}

	if !loginSuccess {
		t.Error("OnSuccess was not called")
	}

	cred, err := store.Get("openai")
	if err != nil {
		t.Fatalf("Get after login: %v", err)
	}
	if cred.AccessToken != "at-test-login" {
		t.Errorf("AccessToken = %q, want %q", cred.AccessToken, "at-test-login")
	}
}

func TestLoginPKCEStateMismatch(t *testing.T) {
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"access_token": "tok", "refresh_token": "rt", "expires_in": 3600,
		})
	}))
	defer tokenServer.Close()

	origCfg := Providers["openai"]
	Providers["openai"] = ProviderConfig{
		AuthorizeURL: origCfg.AuthorizeURL,
		TokenURL:     tokenServer.URL,
		ClientID:     origCfg.ClientID,
		RedirectPath: "/auth/callback",
		Scopes:       origCfg.Scopes,
		FlowType:     FlowPKCE,
	}
	defer func() { Providers["openai"] = origCfg }()

	dir := t.TempDir()
	store := NewStore(dir + "/auth.json")

	err := LoginPKCE("openai", store, LoginCallbacks{
		OnManualURL: func(authorizeURL string) string {
			return "http://localhost:9999/callback?code=code&state=wrong-state"
		},
	})
	if err == nil {
		t.Error("expected state mismatch error")
	}
}

func TestLoginPKCEImportOnlyProvider(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir + "/auth.json")
	err := LoginPKCE("gemini", store, LoginCallbacks{})
	if err == nil {
		t.Error("expected error for import-only provider")
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	cfg := Providers["openai"]
	authURL := buildAuthorizeURL(cfg, "test-challenge", "test-state", 1455)

	u, err := url.Parse(authURL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "auth.openai.com" {
		t.Errorf("host = %q", u.Host)
	}
	q := u.Query()
	if q.Get("response_type") != "code" {
		t.Errorf("response_type = %q", q.Get("response_type"))
	}
	if q.Get("client_id") != cfg.ClientID {
		t.Errorf("client_id = %q", q.Get("client_id"))
	}
	if q.Get("code_challenge") != "test-challenge" {
		t.Errorf("code_challenge = %q", q.Get("code_challenge"))
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Errorf("code_challenge_method = %q", q.Get("code_challenge_method"))
	}
	if q.Get("state") != "test-state" {
		t.Errorf("state = %q", q.Get("state"))
	}
	// Check extra params
	if q.Get("codex_cli_simplified_flow") != "true" {
		t.Errorf("codex_cli_simplified_flow = %q", q.Get("codex_cli_simplified_flow"))
	}
}
