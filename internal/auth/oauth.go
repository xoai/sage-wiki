package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func generateVerifier() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate verifier: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func generateChallenge(verifier string) string {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:])
}

func generateState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: generate state: %w", err)
	}
	return hex.EncodeToString(b), nil
}

type callbackResult struct {
	code  string
	state string
}

func startCallbackServer(preferredPort int, path string, result chan<- callbackResult) (port int, shutdown func(), err error) {
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		result <- callbackResult{code: code, state: state}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "<html><body><h2>Authorization complete.</h2><p>You can close this tab.</p></body></html>")
	})

	addr := fmt.Sprintf("127.0.0.1:%d", preferredPort)
	listener, err := net.Listen("tcp", addr)
	if err != nil && preferredPort != 0 {
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		return 0, nil, fmt.Errorf("auth: start callback server: %w", err)
	}

	port = listener.Addr().(*net.TCPAddr).Port
	server := &http.Server{Handler: mux}

	go server.Serve(listener)

	return port, func() { server.Close() }, nil
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

func exchangeCodeForTokens(tokenURL, code, verifier, clientID, redirectURI string) (*Credential, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"code_verifier": {verifier},
		"client_id":     {clientID},
		"redirect_uri":  {redirectURI},
	}

	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("auth: token exchange: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("auth: read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		truncated := string(body)
		if len(truncated) > 500 {
			truncated = truncated[:500] + "..."
		}
		return nil, fmt.Errorf("auth: token exchange returned %d: %s", resp.StatusCode, truncated)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("auth: parse token response: %w", err)
	}

	expiresAt := time.Now().Unix() + int64(tok.ExpiresIn) - 300
	if tok.ExpiresIn == 0 {
		expiresAt = time.Now().Add(1 * time.Hour).Unix()
	}

	return &Credential{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		ExpiresAt:    expiresAt,
		Source:       "login",
	}, nil
}

func extractAccountID(jwt string, claim string) string {
	parts := strings.SplitN(jwt, ".", 3)
	if len(parts) < 2 {
		return ""
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	if v, ok := claims[claim]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func buildAuthorizeURL(cfg ProviderConfig, challenge, state string, port int) string {
	u, _ := url.Parse(cfg.AuthorizeURL)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", fmt.Sprintf("http://localhost:%d%s", port, cfg.RedirectPath))
	q.Set("scope", strings.Join(cfg.Scopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	for k, v := range cfg.ExtraAuthParams {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// LoginCallbacks allows the CLI layer to control UI interactions.
type LoginCallbacks struct {
	OnBrowserOpen func(url string)
	OnManualURL   func(authorizeURL string) string // returns pasted redirect URL
	OnSuccess     func(provider string)
}

// LoginPKCE performs the full PKCE OAuth flow for a provider.
func LoginPKCE(providerName string, store *Store, cb LoginCallbacks) error {
	cfg, ok := Providers[providerName]
	if !ok {
		return fmt.Errorf("auth: unknown provider %q", providerName)
	}
	if cfg.FlowType != FlowPKCE {
		return fmt.Errorf("auth: provider %q does not support OAuth login (use import instead)", providerName)
	}

	verifier, err := generateVerifier()
	if err != nil {
		return err
	}
	challenge := generateChallenge(verifier)
	state, err := generateState()
	if err != nil {
		return err
	}

	resultCh := make(chan callbackResult, 1)
	port, shutdown, err := startCallbackServer(cfg.RedirectPort, cfg.RedirectPath, resultCh)
	if err != nil {
		return err
	}
	defer shutdown()

	authorizeURL := buildAuthorizeURL(cfg, challenge, state, port)
	redirectURI := fmt.Sprintf("http://localhost:%d%s", port, cfg.RedirectPath)

	var cbResult callbackResult

	browserErr := openBrowser(authorizeURL)
	if browserErr == nil {
		if cb.OnBrowserOpen != nil {
			cb.OnBrowserOpen(authorizeURL)
		}
		select {
		case cbResult = <-resultCh:
		case <-time.After(120 * time.Second):
			return fmt.Errorf("auth: authorization timed out (120s)")
		}
	} else {
		if cb.OnManualURL == nil {
			return fmt.Errorf("auth: could not open browser and no manual URL handler provided: %w", browserErr)
		}
		pastedURL := cb.OnManualURL(authorizeURL)
		parsed, err := url.Parse(pastedURL)
		if err != nil {
			return fmt.Errorf("auth: invalid pasted URL: %w", err)
		}
		cbResult.code = parsed.Query().Get("code")
		cbResult.state = parsed.Query().Get("state")
	}

	if cbResult.code == "" {
		return fmt.Errorf("auth: no authorization code received")
	}
	if cbResult.state != state {
		return fmt.Errorf("auth: state mismatch (possible CSRF attack)")
	}

	cred, err := exchangeCodeForTokens(cfg.TokenURL, cbResult.code, verifier, cfg.ClientID, redirectURI)
	if err != nil {
		return err
	}

	if cfg.AccountIDClaim != "" {
		cred.AccountID = extractAccountID(cred.AccessToken, cfg.AccountIDClaim)
	}

	if err := store.Put(providerName, cred); err != nil {
		return err
	}

	if cb.OnSuccess != nil {
		cb.OnSuccess(providerName)
	}

	return nil
}
