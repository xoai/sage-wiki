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
	// Current Claude Code format: credentials nested under "claudeAiOauth"
	// with expiresAt as a numeric millisecond epoch.
	data := []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-test","refreshToken":"rt-claude","expiresAt":1771398000000,"scopes":["user:inference"],"subscriptionType":"max"},"trustedDeviceToken":"tdt","organizationUuid":"org-1"}`)
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
	if cred.ExpiresAt != 1771398000 {
		t.Errorf("ExpiresAt = %d, want 1771398000 (ms→s)", cred.ExpiresAt)
	}
}

func TestParseClaudeAuthLegacyFlat(t *testing.T) {
	// Legacy/flat shape with an RFC3339 expiresAt string must still parse.
	data := []byte(`{"accessToken":"sk-ant-oat01-flat","refreshToken":"rt-flat","expiresAt":"2027-02-18T07:00:00Z"}`)
	cred, err := parseClaudeAuth(data)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken != "sk-ant-oat01-flat" {
		t.Errorf("AccessToken = %q", cred.AccessToken)
	}
	if cred.RefreshToken != "rt-flat" {
		t.Errorf("RefreshToken = %q", cred.RefreshToken)
	}
	if cred.ExpiresAt == 0 {
		t.Error("ExpiresAt should be set from RFC3339 timestamp")
	}
}

func TestImportFromCLINestedClaude(t *testing.T) {
	forceFileBackend(t)
	// Reproduces the VPS bug: a real Claude Code credentials file (nested) must
	// import successfully via `auth import --provider anthropic`.
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	home := filepath.Join(dir, "home")
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0700)
	creds := []byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-imported","refreshToken":"rt-imported","expiresAt":1771398000000},"organizationUuid":"org-1"}`)
	os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), creds, 0600)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "") // ensure the file path, not the env override

	if err := ImportFromCLI("anthropic", store); err != nil {
		t.Fatalf("ImportFromCLI(anthropic): %v", err)
	}

	cred, err := store.Get("anthropic")
	if err != nil {
		t.Fatalf("Get after import: %v", err)
	}
	if cred.AccessToken != "sk-ant-oat01-imported" {
		t.Errorf("AccessToken = %q", cred.AccessToken)
	}
	if cred.Source != "import" {
		t.Errorf("Source = %q, want %q", cred.Source, "import")
	}
}

func TestImportFromCLIClaudeOAuthTokenEnv(t *testing.T) {
	forceFileBackend(t)
	// macOS path: Claude Code keeps creds in the Keychain (no file), so the user
	// exports CLAUDE_CODE_OAUTH_TOKEN. Import must use it with NO file present.
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	t.Setenv("HOME", filepath.Join(dir, "home")) // empty home → no ~/.claude file
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "  sk-ant-oat01-from-env  ")

	if err := ImportFromCLI("anthropic", store); err != nil {
		t.Fatalf("ImportFromCLI(anthropic) with env token: %v", err)
	}

	cred, err := store.Get("anthropic")
	if err != nil {
		t.Fatalf("Get after import: %v", err)
	}
	if cred.AccessToken != "sk-ant-oat01-from-env" {
		t.Errorf("AccessToken = %q, want trimmed env token", cred.AccessToken)
	}
	if cred.RefreshToken != "" {
		t.Errorf("RefreshToken = %q, want empty (env token has none)", cred.RefreshToken)
	}
	if cred.ExpiresAt != 0 {
		t.Errorf("ExpiresAt = %d, want 0 (unmanaged)", cred.ExpiresAt)
	}
	if cred.Source != "import" {
		t.Errorf("Source = %q, want import", cred.Source)
	}
}

func TestImportFromCLIClaudeOAuthTokenEnvPrecedence(t *testing.T) {
	forceFileBackend(t)
	// When both the env var and a credentials file exist, the env var wins
	// (documented override).
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	home := filepath.Join(dir, "home")
	claudeDir := filepath.Join(home, ".claude")
	os.MkdirAll(claudeDir, 0700)
	os.WriteFile(filepath.Join(claudeDir, ".credentials.json"),
		[]byte(`{"claudeAiOauth":{"accessToken":"sk-ant-oat01-from-file","refreshToken":"rt","expiresAt":1771398000000}}`), 0600)
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "sk-ant-oat01-from-env")

	if err := ImportFromCLI("anthropic", store); err != nil {
		t.Fatalf("ImportFromCLI(anthropic): %v", err)
	}

	cred, _ := store.Get("anthropic")
	if cred.AccessToken != "sk-ant-oat01-from-env" {
		t.Errorf("AccessToken = %q, want env token to win over file", cred.AccessToken)
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
	forceFileBackend(t)
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
	forceFileBackend(t)
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	t.Setenv("CODEX_HOME", filepath.Join(dir, "nonexistent"))

	err := ImportFromCLI("openai", store)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestImportFromCLIEmptyToken(t *testing.T) {
	forceFileBackend(t)
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
	want := filepath.Join("/custom/codex", "auth.json")
	if got != want {
		t.Errorf("expandPath = %q, want %q", got, want)
	}
}

func TestExpandPathTilde(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	got := expandPath("~/.codex/auth.json", "openai")
	// Expected derives from the implemented precedence: $HOME when set,
	// else os.UserHomeDir (identical on unix, divergent on Windows).
	home := os.Getenv("HOME")
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	expected := filepath.Join(home, ".codex/auth.json")
	if got != expected {
		t.Errorf("expandPath = %q, want %q", got, expected)
	}
}
