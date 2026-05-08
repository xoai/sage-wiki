package auth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCodexAuth(t *testing.T) {
	data := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"at-codex","refresh_token":"rt-codex","id_token":"id-tok"},"last_refresh":1234}`)
	cred, err := parseCodexAuth(data)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "at-codex" {
		t.Errorf("AccessToken = %q", cred.AccessToken)
	}
	if cred.RefreshToken != "rt-codex" {
		t.Errorf("RefreshToken = %q", cred.RefreshToken)
	}
}

func TestParseClaudeAuth(t *testing.T) {
	data := []byte(`{"accessToken":"sk-ant-oat01-test","refreshToken":"rt-claude","expiresAt":"2027-02-18T07:00:00.000Z"}`)
	cred, err := parseClaudeAuth(data)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "sk-ant-oat01-test" {
		t.Errorf("AccessToken = %q", cred.AccessToken)
	}
	if cred.RefreshToken != "rt-claude" {
		t.Errorf("RefreshToken = %q", cred.RefreshToken)
	}
	if cred.ExpiresAt == 0 {
		t.Error("ExpiresAt should be set from ISO timestamp")
	}
}

func TestParseCopilotAuth(t *testing.T) {
	data := []byte(`{"oauth_token":"gho_test123","editor":"vscode"}`)
	cred, err := parseCopilotAuth(data)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "gho_test123" {
		t.Errorf("AccessToken = %q", cred.AccessToken)
	}
}

func TestParseGeminiAuth(t *testing.T) {
	data := []byte(`{"access_token":"ya29.test","refresh_token":"1//rt-gemini","token_type":"Bearer","expiry_date":1717862400000}`)
	cred, err := parseGeminiAuth(data)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "ya29.test" {
		t.Errorf("AccessToken = %q", cred.AccessToken)
	}
	if cred.RefreshToken != "1//rt-gemini" {
		t.Errorf("RefreshToken = %q", cred.RefreshToken)
	}
	if cred.ExpiresAt != 1717862400 {
		t.Errorf("ExpiresAt = %d, want 1717862400 (ms→s conversion)", cred.ExpiresAt)
	}
}

func TestImportFromCLI(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	// Create a fixture Codex auth file
	codexDir := filepath.Join(dir, ".codex")
	os.MkdirAll(codexDir, 0700)
	codexAuth := []byte(`{"tokens":{"access_token":"at-imported","refresh_token":"rt-imported"}}`)
	os.WriteFile(filepath.Join(codexDir, "auth.json"), codexAuth, 0600)

	// Override CODEX_HOME
	t.Setenv("CODEX_HOME", codexDir)

	if err := ImportFromCLI("openai", store); err != nil {
		t.Fatalf("ImportFromCLI: %v", err)
	}

	cred, err := store.Get("openai")
	if err != nil {
		t.Fatalf("Get after import: %v", err)
	}
	if cred.AccessToken != "at-imported" {
		t.Errorf("AccessToken = %q", cred.AccessToken)
	}
	if cred.Source != "import" {
		t.Errorf("Source = %q, want %q", cred.Source, "import")
	}
}

func TestImportFromCLIMissingFile(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	t.Setenv("CODEX_HOME", filepath.Join(dir, "nonexistent"))

	err := ImportFromCLI("openai", store)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestImportFromCLIEmptyToken(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	codexDir := filepath.Join(dir, ".codex")
	os.MkdirAll(codexDir, 0700)
	os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"tokens":{"access_token":"","refresh_token":""}}`), 0600)

	t.Setenv("CODEX_HOME", codexDir)

	err := ImportFromCLI("openai", store)
	if err == nil {
		t.Error("expected error for empty token")
	}
}

func TestExpandPathWithEnvOverride(t *testing.T) {
	t.Setenv("CODEX_HOME", "/custom/codex")
	got := expandPath("~/.codex/auth.json", "openai")
	if got != "/custom/codex/auth.json" {
		t.Errorf("expandPath = %q, want /custom/codex/auth.json", got)
	}
}

func TestExpandPathTilde(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	got := expandPath("~/.codex/auth.json", "openai")
	home, _ := os.UserHomeDir()
	expected := filepath.Join(home, ".codex/auth.json")
	if got != expected {
		t.Errorf("expandPath = %q, want %q", got, expected)
	}
}
