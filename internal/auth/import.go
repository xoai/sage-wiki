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
	var f struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    string `json:"expiresAt"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("auth: parse claude .credentials.json: %w", err)
	}
	var expiresAt int64
	if f.ExpiresAt != "" {
		if t, err := time.Parse(time.RFC3339, f.ExpiresAt); err == nil {
			expiresAt = t.Unix()
		}
	}
	return &Credential{
		AccessToken:  f.AccessToken,
		RefreshToken: f.RefreshToken,
		ExpiresAt:    expiresAt,
	}, nil
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
