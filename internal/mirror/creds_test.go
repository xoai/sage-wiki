package mirror

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/mirror/s3"
)

func TestResolveCredentials_FromEnv(t *testing.T) {
	t.Setenv("TEST_AK", "ak-from-env")
	t.Setenv("TEST_SK", "sk-from-env")
	creds, err := ResolveCredentials("TEST_AK", "TEST_SK", "")
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
	creds, err := ResolveCredentials("UNSET_AK", "UNSET_SK", f)
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
	creds, err := ResolveCredentials("TEST_AK", "TEST_SK", f)
	if err != nil {
		t.Fatalf("ResolveCredentials: %v", err)
	}
	if creds.AccessKey != "ak-env" {
		t.Fatalf("env should win over file: %+v", creds)
	}
}

func TestResolveCredentials_Missing(t *testing.T) {
	_, err := ResolveCredentials("UNSET_AK", "UNSET_SK", "")
	if err == nil {
		t.Fatal("missing creds should be a hard error")
	}
	if !strings.Contains(err.Error(), "UNSET_AK") {
		t.Fatalf("error should name the env var: %v", err)
	}
}

func TestResolveCredentials_PartialEnv(t *testing.T) {
	t.Setenv("TEST_AK", "ak-only")
	_, err := ResolveCredentials("TEST_AK", "UNSET_SK", "")
	if err == nil {
		t.Fatal("partial env should error naming the missing half")
	}
}

func TestResolveCredentials_BadFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "creds.json")
	os.WriteFile(f, []byte(`{"wrong":"shape"}`), 0o600)
	if _, err := ResolveCredentials("UNSET_AK", "UNSET_SK", f); err == nil {
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

var _ = s3.Credentials{} // type anchor: ResolveCredentials returns s3.Credentials
