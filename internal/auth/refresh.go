package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func Refresh(providerName string, cred *Credential) (*Credential, error) {
	if cred.RefreshToken == "" {
		return nil, fmt.Errorf("auth: no refresh token for %s — re-authenticate with sage-wiki auth login", providerName)
	}

	cfg, ok := Providers[providerName]
	if !ok {
		return nil, fmt.Errorf("auth: unknown provider %q", providerName)
	}

	switch providerName {
	case "openai", "anthropic":
		return refreshPKCEToken(cfg, cred)
	case "gemini":
		return refreshGeminiToken(cred)
	case "github-copilot":
		return refreshCopilotToken(cred)
	default:
		return nil, fmt.Errorf("auth: no refresh implementation for %q", providerName)
	}
}

func refreshPKCEToken(cfg ProviderConfig, cred *Credential) (*Credential, error) {
	// Same per-provider body encoding as the code exchange: Anthropic's token
	// endpoint requires JSON for refresh too, so a form-encoded refresh would
	// fail (~hours after login) with the same "Invalid request format".
	reqBody, contentType, err := buildTokenRequest(cfg.TokenRequestFormat, map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     cfg.ClientID,
		"refresh_token": cred.RefreshToken,
	})
	if err != nil {
		return nil, err
	}

	resp, err := http.Post(cfg.TokenURL, contentType, reqBody)
	if err != nil {
		return nil, fmt.Errorf("auth: refresh request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("auth: read refresh response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		truncated := string(body)
		if len(truncated) > 500 {
			truncated = truncated[:500] + "..."
		}
		return nil, fmt.Errorf("auth: refresh returned %d: %s", resp.StatusCode, truncated)
	}

	var tok tokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("auth: parse refresh response: %w", err)
	}

	refreshToken := tok.RefreshToken
	if refreshToken == "" {
		refreshToken = cred.RefreshToken
	}

	expiresAt := time.Now().Unix() + int64(tok.ExpiresIn) - 300
	if tok.ExpiresIn == 0 {
		expiresAt = time.Now().Add(1 * time.Hour).Unix()
	}

	return &Credential{
		AccessToken:  tok.AccessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
		AccountID:    cred.AccountID,
		Source:       cred.Source,
	}, nil
}

// Public OAuth client credentials from the Gemini CLI (google-gemini/gemini-cli).
// These are safe to embed — they only work with the user's own refresh token.
func refreshGeminiToken(cred *Credential) (*Credential, error) {
	data := url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {"77185425430.apps.googleusercontent.com"},
		"client_secret": {"OTJgUOQcT7lO7GsGZq2G4IlT"},
		"refresh_token": {cred.RefreshToken},
	}

	resp, err := http.Post("https://oauth2.googleapis.com/token", "application/x-www-form-urlencoded", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("auth: gemini refresh: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: gemini refresh returned %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("auth: parse gemini refresh: %w", err)
	}

	return &Credential{
		AccessToken:  tok.AccessToken,
		RefreshToken: cred.RefreshToken,
		ExpiresAt:    time.Now().Unix() + int64(tok.ExpiresIn) - 300,
		Source:       cred.Source,
	}, nil
}

func refreshCopilotToken(cred *Credential) (*Credential, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/copilot_internal/v2/token", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+cred.RefreshToken)
	req.Header.Set("User-Agent", "GitHubCopilotChat/0.35.0")
	req.Header.Set("Editor-Version", "vscode/1.107.0")

	client := http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("auth: copilot refresh: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("auth: copilot refresh returned %d: %s", resp.StatusCode, string(body))
	}

	var tok struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("auth: parse copilot refresh: %w", err)
	}

	return &Credential{
		AccessToken:  tok.Token,
		RefreshToken: cred.RefreshToken,
		ExpiresAt:    tok.ExpiresAt - 300,
		Source:       cred.Source,
	}, nil
}

func (s *Store) RefreshAndGet(providerName string) (*Credential, error) {
	// P2-6: on the file backend the whole refresh flow holds the flock
	// (as before). On keychain machines the write side never touches the
	// file, but the READ fallback does — and store.write is os.WriteFile
	// (not atomic), so the fallback read takes the lock for the READ ONLY
	// (no flock held across the unbounded keyring write or HTTP refresh).
	backend, _ := s.backendForStore()
	var unlock func()
	if backend != "keychain" {
		var err error
		unlock, err = lockFile(s.path)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}

	var cred *Credential
	var err error
	if backend == "keychain" {
		readUnlock, err := lockFile(s.path)
		if err != nil {
			return nil, err
		}
		cred, err = s.Get(providerName)
		readUnlock()
	} else {
		cred, err = s.Get(providerName)
	}
	if err != nil {
		return nil, err
	}

	refreshed, err := Refresh(providerName, cred)
	if err != nil {
		return nil, err
	}

	// P2-6: rotated tokens go through the backend-aware write path
	// (keychain-only on keychain machines — the file backup stays frozen).
	if err := s.refreshAndStore(providerName, refreshed); err != nil {
		return nil, err
	}

	return refreshed, nil
}
