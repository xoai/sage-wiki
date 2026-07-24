package auth

// Backend-aware credential storage (P2-6): the Store keeps its
// constructor signature and picks a backend per construction via
// probeKeyring (D1: probe once per process, 500ms timeout, never writes).
// Method semantics per spec §3.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zalando/go-keyring"
)

// backendForStore selects the backend for a Store (test seam: tests set
// s.backend/s.kr directly and never hit probeKeyring).
func (s *Store) backendForStore() (string, keyringAPI) {
	if s.backend != "" {
		return s.backend, s.kr
	}
	return probeKeyring(goKeyring{}), goKeyring{}
}

// Backend reports the active backend ("keychain" | "file") for
// `auth status` (design D5).
func (s *Store) Backend() string {
	b, _ := s.backendForStore()
	return b
}

func (s *Store) keychainGet(provider string) (*Credential, error) {
	_, kr := s.backendForStore()
	raw, err := kr.Get(keyringService, provider)
	if err != nil {
		return nil, err
	}
	var cred Credential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return nil, fmt.Errorf("auth: decode keychain credential for %q: %w", provider, err)
	}
	cred.Provider = provider // json:"-" — rehydrated from the entry key (D2)
	return &cred, nil
}

func (s *Store) keychainPut(provider string, cred *Credential) error {
	_, kr := s.backendForStore()
	cred.Provider = "" // never marshal Provider (rehydrated from the entry key)
	data, err := json.Marshal(cred)
	cred.Provider = provider
	if err != nil {
		return err
	}
	return kr.Set(keyringService, provider, string(data))
}

// fileGet is today's file behavior, verbatim.
func (s *Store) fileGet(provider string) (*Credential, error) {
	sf, err := s.read()
	if err != nil {
		return nil, err
	}
	cred, ok := sf.Credentials[provider]
	if !ok {
		return nil, fmt.Errorf("auth: no credentials for provider %q", provider)
	}
	return cred, nil
}

// getWithBackend implements the spec §3 Get row: keychain → on
// keyring.ErrNotFound → file (the file's own error is final); other
// keyring errors FAIL (no silent downgrade).
func (s *Store) getWithBackend(provider string) (*Credential, error) {
	backend, _ := s.backendForStore()
	if backend != "keychain" {
		return s.fileGet(provider)
	}
	cred, err := s.keychainGet(provider)
	if err == nil {
		return cred, nil
	}
	if err == keyring.ErrNotFound {
		return s.fileGet(provider)
	}
	return nil, fmt.Errorf("auth: keychain read for %q: %w", provider, err)
}

// putWithBackend implements the spec §3 Put row: active backend ONLY —
// the file backup stays frozen at migration time (D4 Put policy).
func (s *Store) putWithBackend(provider string, cred *Credential) error {
	backend, _ := s.backendForStore()
	if backend == "keychain" {
		return s.keychainPut(provider, cred)
	}
	unlock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	defer unlock()

	sf, err := s.read()
	if err != nil {
		return err
	}

	cred.Provider = provider
	sf.Credentials[provider] = cred
	return s.write(sf)
}

// deleteWithBackend implements the spec §3 Delete row: BOTH backends
// (no logout resurrection), ignoring keyring.ErrNotFound for
// never-migrated providers.
func (s *Store) deleteWithBackend(provider string) error {
	backend, _ := s.backendForStore()

	// File entry (always attempted — post-migration the file holds a copy).
	unlock, err := lockFile(s.path)
	if err != nil {
		return err
	}
	sf, err := s.read()
	if err != nil {
		unlock()
		return err
	}
	delete(sf.Credentials, provider)
	werr := s.write(sf)
	unlock()
	if werr != nil {
		return werr
	}

	if backend == "keychain" {
		_, kr := s.backendForStore()
		if err := kr.Delete(keyringService, provider); err != nil && err != keyring.ErrNotFound {
			return fmt.Errorf("auth: keychain delete for %q: %w", provider, err)
		}
	}
	return nil
}

// listWithBackend implements the spec §3 List row: merged union over
// auth.Providers keys, keychain winning on conflict (it is the active
// backend). File backend: file verbatim.
func (s *Store) listWithBackend() (map[string]*Credential, error) {
	sf, err := s.read()
	if err != nil {
		return nil, err
	}
	backend, _ := s.backendForStore()
	if backend != "keychain" {
		return sf.Credentials, nil
	}
	merged := map[string]*Credential{}
	for k, v := range sf.Credentials {
		merged[k] = v
	}
	for name := range Providers {
		if cred, err := s.keychainGet(name); err == nil {
			merged[name] = cred
		}
	}
	return merged, nil
}

// refreshAndStore is the backend-aware write path for rotated tokens
// (spec §3 RefreshAndGet row). RefreshAndGet already holds the file
// lock, so the file path here writes WITHOUT re-acquiring it (the old
// code's direct write under the same lock — byte-identical); the
// keychain path takes no file lock at all.
func (s *Store) refreshAndStore(providerName string, refreshed *Credential) error {
	backend, _ := s.backendForStore()
	if backend == "keychain" {
		return s.keychainPut(providerName, refreshed)
	}
	sf, err := s.read()
	if err != nil {
		return err
	}
	refreshed.Provider = providerName
	sf.Credentials[providerName] = refreshed
	return s.write(sf)
}

// --- test helpers (backend_test.go) ---

func (s *Store) writeFileOnly(t fataler, cred *Credential) {
	unlock, err := lockFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	sf, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	cred.Provider = cred.Provider
	sf.Credentials[cred.Provider] = cred
	if err := s.write(sf); err != nil {
		t.Fatal(err)
	}
}

func (s *Store) readFileBytes(t fataler) []byte {
	sf, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(sf)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

type fataler interface {
	Fatal(args ...any)
}

var _ = strings.TrimSpace
