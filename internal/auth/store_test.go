package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreGetPutDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	cred := &Credential{
		AccessToken:  "sk-test-token-123",
		RefreshToken: "rt-test-456",
		ExpiresAt:    time.Now().Add(1 * time.Hour).Unix(),
		Source:       "login",
	}

	if err := store.Put("openai", cred); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := store.Get("openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken != "sk-test-token-123" {
		t.Errorf("AccessToken = %q, want %q", got.AccessToken, "sk-test-token-123")
	}
	if got.Provider != "openai" {
		t.Errorf("Provider = %q, want %q", got.Provider, "openai")
	}
	if got.Source != "login" {
		t.Errorf("Source = %q, want %q", got.Source, "login")
	}

	if err := store.Delete("openai"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, err = store.Get("openai")
	if err == nil {
		t.Error("expected error after Delete, got nil")
	}
}

func TestStoreGetMissing(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	_, err := store.Get("openai")
	if err == nil {
		t.Error("expected error for missing provider, got nil")
	}
}

func TestStoreList(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	store.Put("openai", &Credential{AccessToken: "tok1", Source: "login"})
	store.Put("anthropic", &Credential{AccessToken: "tok2", Source: "import"})

	creds, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("expected 2 credentials, got %d", len(creds))
	}
	if creds["openai"].Provider != "openai" {
		t.Errorf("Provider field not set for openai")
	}
	if creds["anthropic"].Provider != "anthropic" {
		t.Errorf("Provider field not set for anthropic")
	}
}

func TestStoreTOSAcknowledgment(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	if store.IsTOSAcknowledged() {
		t.Error("expected TOS not acknowledged on fresh store")
	}

	if err := store.AcknowledgeTOS(); err != nil {
		t.Fatalf("AcknowledgeTOS: %v", err)
	}

	if !store.IsTOSAcknowledged() {
		t.Error("expected TOS acknowledged after AcknowledgeTOS")
	}

	// TOS should survive credential deletion
	store.Put("openai", &Credential{AccessToken: "tok", Source: "login"})
	store.Delete("openai")

	if !store.IsTOSAcknowledged() {
		t.Error("expected TOS to survive credential deletion")
	}
}

func TestStoreVersionRejection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	bad := storeFile{Version: 99, Credentials: map[string]*Credential{}}
	data, _ := json.Marshal(bad)
	os.WriteFile(path, data, 0600)

	store := NewStore(path)
	_, err := store.Get("openai")
	if err == nil {
		t.Error("expected error for unsupported version")
	}
}

func TestStoreFilePermissions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	store := NewStore(path)
	store.Put("openai", &Credential{AccessToken: "tok", Source: "login"})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("file permissions = %04o, want 0600", mode)
	}
}

func TestStoreCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "deep", "auth.json")

	store := NewStore(path)
	if err := store.Put("openai", &Credential{AccessToken: "tok", Source: "login"}); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := os.Stat(filepath.Dir(path)); err != nil {
		t.Errorf("parent dir not created: %v", err)
	}
}

func TestLockfilePreventsDoubleLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "auth.json")

	unlock, err := lockFile(lockPath)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}

	_, err = lockFile(lockPath)
	if err == nil {
		t.Error("expected error on second lock, got nil")
	}

	unlock()

	unlock2, err := lockFile(lockPath)
	if err != nil {
		t.Fatalf("lock after unlock: %v", err)
	}
	unlock2()
}

func TestLockfileStaleCleansUp(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "auth.json")
	lockFilePath := lockPath + ".lock"

	f, err := os.Create(lockFilePath)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	// Set mtime to past stale threshold
	staleTime := time.Now().Add(-staleThreshold - time.Second)
	os.Chtimes(lockFilePath, staleTime, staleTime)

	unlock, err := lockFile(lockPath)
	if err != nil {
		t.Fatalf("expected stale lock to be cleaned up, got: %v", err)
	}
	unlock()
}

func TestStoreConcurrentPut(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(filepath.Join(dir, "auth.json"))

	var wg sync.WaitGroup
	errs := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			cred := &Credential{
				AccessToken: "tok",
				Source:       "login",
			}
			if err := store.Put("openai", cred); err != nil {
				errs <- err
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	// Some puts may fail due to lock contention — that's expected.
	// At least one should succeed.
	got, err := store.Get("openai")
	if err != nil {
		t.Fatalf("Get after concurrent puts: %v", err)
	}
	if got.AccessToken != "tok" {
		t.Errorf("unexpected token: %q", got.AccessToken)
	}
}
