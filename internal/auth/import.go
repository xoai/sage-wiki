package auth

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func ImportFromCLI(providerName string, store *Store) error {
	cfg, ok := Providers[providerName]
	if !ok {
		return fmt.Errorf("auth: unknown provider %q", providerName)
	}
	if cfg.ImportPath == "" {
		return fmt.Errorf("auth: provider %q does not support import", providerName)
	}

	// Claude Code stores credentials in the macOS Keychain (no flat file), so
	// users export the access token via CLAUDE_CODE_OAUTH_TOKEN. When set, use
	// it directly and skip the file. The token has no refresh token or known
	// expiry: the auth transport uses it as-is (it does not attempt a refresh
	// without a refresh token) until the provider rejects it, at which point the
	// user re-exports a fresh token.
	if providerName == "anthropic" {
		if tok := strings.TrimSpace(os.Getenv("CLAUDE_CODE_OAUTH_TOKEN")); tok != "" {
			return store.Put(providerName, &Credential{AccessToken: tok, Source: "import"})
		}
	}

	importPath := expandPath(cfg.ImportPath, providerName)

	data, err := os.ReadFile(importPath)
	if err != nil {
		return fmt.Errorf("auth: could not read %s: %w\nMake sure the CLI tool is installed and authenticated", importPath, err)
	}

	var cred *Credential
	switch providerName {
	case "openai":
		cred, err = parseCodexAuth(data)
	case "anthropic":
		cred, err = parseClaudeAuth(data)
	case "github-copilot":
		cred, err = parseCopilotAuth(data)
	case "gemini":
		cred, err = parseGeminiAuth(data)
	default:
		return fmt.Errorf("auth: no import parser for %q", providerName)
	}
	if err != nil {
		return err
	}

	if cred.AccessToken == "" {
		return fmt.Errorf("auth: imported credentials have no access token — the CLI tool may not be authenticated")
	}

	cred.Source = "import"
	return store.Put(providerName, cred)
}

func expandPath(path string, provider string) string {
	switch provider {
	case "openai":
		if v := os.Getenv("CODEX_HOME"); v != "" {
			return filepath.Join(v, "auth.json")
		}
	case "github-copilot":
		if v := os.Getenv("COPILOT_HOME"); v != "" {
			return filepath.Join(v, "settings.json")
		}
	}

	if strings.HasPrefix(path, "~/") {
		// $HOME wins on every platform (unix already works this way via
		// os.UserHomeDir; on Windows UserHomeDir reads USERPROFILE and
		// ignores HOME — honoring HOME first keeps the tilde contract
		// uniform, e.g. for git-bash users and tests).
		if home := os.Getenv("HOME"); home != "" {
			return filepath.Join(home, path[2:])
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func parseCodexAuth(data []byte) (*Credential, error) {
	var f struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("auth: parse codex auth.json: %w", err)
	}
	return &Credential{
		AccessToken:  f.Tokens.AccessToken,
		RefreshToken: f.Tokens.RefreshToken,
	}, nil
}

func parseClaudeAuth(data []byte) (*Credential, error) {
	// Claude Code nests OAuth credentials under "claudeAiOauth" and stores
	// expiresAt as a numeric millisecond epoch. Older/other shapes may store
	// the fields flat at the top level with an RFC3339 expiresAt string.
	// Support both; prefer the nested shape when it carries a token.
	var f struct {
		ClaudeAiOauth *struct {
			AccessToken  string          `json:"accessToken"`
			RefreshToken string          `json:"refreshToken"`
			ExpiresAt    json.RawMessage `json:"expiresAt"`
		} `json:"claudeAiOauth"`
		AccessToken  string          `json:"accessToken"`
		RefreshToken string          `json:"refreshToken"`
		ExpiresAt    json.RawMessage `json:"expiresAt"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("auth: parse claude .credentials.json: %w", err)
	}

	accessToken, refreshToken, rawExpiry := f.AccessToken, f.RefreshToken, f.ExpiresAt
	if f.ClaudeAiOauth != nil && f.ClaudeAiOauth.AccessToken != "" {
		accessToken = f.ClaudeAiOauth.AccessToken
		refreshToken = f.ClaudeAiOauth.RefreshToken
		rawExpiry = f.ClaudeAiOauth.ExpiresAt
	}

	return &Credential{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    parseClaudeExpiry(rawExpiry),
	}, nil
}

// parseClaudeExpiry accepts either a numeric millisecond epoch (current Claude
// Code format) or an RFC3339 string (legacy), returning Unix seconds (0 if
// absent or unparseable). A quoted RFC3339 string is not a valid JSON number,
// so the numeric unmarshal fails cleanly and we fall through to time.Parse.
func parseClaudeExpiry(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var ms int64
	if err := json.Unmarshal(raw, &ms); err == nil {
		if ms > 0 {
			return ms / 1000 // ms → seconds
		}
		return 0
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t.Unix()
		}
	}
	return 0
}

func parseCopilotAuth(data []byte) (*Credential, error) {
	var f struct {
		OAuthToken string `json:"oauth_token"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("auth: parse copilot settings.json: %w", err)
	}
	return &Credential{
		AccessToken:  f.OAuthToken,
		RefreshToken: f.OAuthToken,
	}, nil
}

func parseGeminiAuth(data []byte) (*Credential, error) {
	var f struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiryDate   int64  `json:"expiry_date"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("auth: parse gemini oauth_creds.json: %w", err)
	}
	var expiresAt int64
	if f.ExpiryDate > 0 {
		expiresAt = f.ExpiryDate / 1000 // ms → seconds
	}
	return &Credential{
		AccessToken:  f.AccessToken,
		RefreshToken: f.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}
