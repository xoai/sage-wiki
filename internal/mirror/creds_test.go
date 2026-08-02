package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveCredentials_FromEnv(t *testing.T) {
	t.Setenv("TEST_AK", "ak-from-env")
	t.Setenv("TEST_SK", "sk-from-env")
	creds, err := ResolveCredentials("TEST_AK", "TEST_SK", "TEST_AK_TOKEN", "")
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if creds.AccessKey != "ak-from-env" || creds.SecretKey != "sk-from-env" {
		t.Fatalf("creds = %+v", creds)
	}
}

func TestResolveCredentials_FromFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte(`{"access_key":"ak-file","secret_key":"sk-file"}`), 0o600)
	creds, err := ResolveCredentials("UNSET_AK", "UNSET_SK", "UNSET_AK_TOKEN", f)
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if creds.AccessKey != "ak-file" || creds.SecretKey != "sk-file" {
		t.Fatalf("creds = %+v", creds)
	}
}

// TestResolveCredentials_EnvWins: env names take precedence when both
// resolve (spec.md §Config precedence).
func TestResolveCredentials_EnvWins(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte(`{"access_key":"ak-file","secret_key":"sk-file"}`), 0o600)
	t.Setenv("TEST_AK", "ak-env")
	t.Setenv("TEST_SK", "sk-env")
	creds, err := ResolveCredentials("TEST_AK", "TEST_SK", "TEST_AK_TOKEN", f)
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if creds.AccessKey != "ak-env" {
		t.Fatalf("env should win over file: %+v", creds)
	}
}

func TestResolveCredentials_Missing(t *testing.T) {
	_, err := ResolveCredentials("UNSET_AK", "UNSET_SK", "UNSET_AK_TOKEN", "")
	if err == nil {
		t.Fatal("missing creds should be a hard error")
	}
	if !strings.Contains(err.Error(), "UNSET_AK") {
		t.Fatalf("error should name the env var: %v", err)
	}
}

func TestResolveCredentials_PartialEnv(t *testing.T) {
	t.Setenv("TEST_AK", "ak-only")
	_, err := ResolveCredentials("TEST_AK", "UNSET_SK", "TEST_AK_TOKEN", "")
	if err == nil {
		t.Fatal("partial env should error naming the missing half")
	}
}

func TestResolveCredentials_BadFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte(`{"wrong":"shape"}`), 0o600)
	if _, err := ResolveCredentials("UNSET_AK", "UNSET_SK", "UNSET_AK_TOKEN", f); err == nil {
		t.Fatal("bad credentials file should error")
	}
}

func TestLoadEncryptionKey(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mirror.key")
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	os.WriteFile(f, key, 0o600)
	got, err := LoadEncryptionKey(f)
	if err != nil {
		t.Fatalf("LoadEncryptionKey: %v", err)
	}
	if len(got) != 32 || got[5] != 5 {
		t.Fatalf("key = %x", got)
	}
}

func TestLoadEncryptionKey_WrongLength(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mirror.key")
	os.WriteFile(f, make([]byte, 16), 0o600)
	if _, err := LoadEncryptionKey(f); err == nil {
		t.Fatal("16-byte keyfile should error (need 32)")
	}
}

func TestLoadEncryptionKey_Missing(t *testing.T) {
	if _, err := LoadEncryptionKey(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("missing keyfile should error")
	}
}

func TestResolveCredentials_SessionToken(t *testing.T) {
	t.Setenv("TEST_AK", "ak")
	t.Setenv("TEST_SK", "sk")
	t.Setenv("TEST_ST", "session-abc")
	creds, err := ResolveCredentials("TEST_AK", "TEST_SK", "TEST_ST", "")
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if creds.SessionToken != "session-abc" {
		t.Fatalf("SessionToken = %q", creds.SessionToken)
	}
}

func TestResolveCredentials_EmptyTokenIsAbsent(t *testing.T) {
	t.Setenv("TEST_AK", "ak")
	t.Setenv("TEST_SK", "sk")
	t.Setenv("TEST_ST", "")
	creds, err := ResolveCredentials("TEST_AK", "TEST_SK", "TEST_ST", "")
	if err != nil {
		t.Fatal(err)
	}
	if creds.SessionToken != "" {
		t.Fatalf("empty env token must read as absent, got %q", creds.SessionToken)
	}
}

// TestResolveCredentials_CrossSourceRejected: env keys + file token is a
// LOUD error (signing without a token yields opaque 403s).
func TestResolveCredentials_CrossSourceRejected(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte(`{"access_key":"ak-file","secret_key":"sk-file","session_token":"st-file"}`), 0o600)
	t.Setenv("TEST_AK", "ak-env")
	t.Setenv("TEST_SK", "sk-env")
	if _, err := ResolveCredentials("TEST_AK", "TEST_SK", "TEST_ST_MISSING", f); err == nil {
		t.Fatal("env keys + file token must be a loud error")
	}
}

func TestResolveCredentials_TokenFromFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte(`{"access_key":"ak-file","secret_key":"sk-file","session_token":"st-file"}`), 0o600)
	creds, err := ResolveCredentials("UNSET_AK", "UNSET_SK", "UNSET_ST", f)
	if err != nil {
		t.Fatal(err)
	}
	if creds.SessionToken != "st-file" {
		t.Fatalf("SessionToken = %q", creds.SessionToken)
	}
}

// TestResolveCredentials_MalformedFileUnderEnvKeys (F-024 witness): env
// keys + an UNREADABLE credentials file errors loudly naming the file.
func TestResolveCredentials_MalformedFileUnderEnvKeys(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte("{not json"), 0o600)
	t.Setenv("TEST_AK", "ak-env")
	t.Setenv("TEST_SK", "sk-env")
	_, err := ResolveCredentials("TEST_AK", "TEST_SK", "TEST_ST", f)
	if err == nil {
		t.Fatal("malformed file under env keys must error loudly")
	}
	if !strings.Contains(err.Error(), "creds.json") {
		t.Fatalf("error must name the file: %v", err)
	}
}

// TestResolveCredentials_ReverseCrossSourceRejected (F-025 witness): file
// keys + env token set is a loud symmetric error.
func TestResolveCredentials_ReverseCrossSourceRejected(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte(`{"access_key":"ak-file","secret_key":"sk-file"}`), 0o600)
	t.Setenv("TEST_ST", "token-from-env")
	if _, err := ResolveCredentials("UNSET_AK", "UNSET_SK", "TEST_ST", f); err == nil {
		t.Fatal("file keys + env token must be a loud error")
	}
}
