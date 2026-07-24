package auth

// Backend-aware credential storage (P2-6): the Store keeps its
// constructor signature and picks a backend per construction via
// probeKeyring (D1: probe once per process, 500ms timeout, never writes).
// Method semantics per spec §3.

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

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
	// Copy-then-marshal (i1: mutating the caller's Credential is a data
	// race under concurrent Store use and visible to concurrent readers).
	snapshot := *cred
	snapshot.Provider = "" // never marshal Provider (rehydrated from the entry key)
	data, err := json.Marshal(&snapshot)
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
	if errors.Is(err, keyring.ErrNotFound) {
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
	var errs []error

	// File entry (always attempted — post-migration the file holds a copy).
	unlock, err := lockFile(s.path)
	if err != nil {
		errs = append(errs, err)
	} else {
		sf, rerr := s.read()
		if rerr != nil {
			errs = append(errs, rerr)
		} else {
			delete(sf.Credentials, provider)
			if werr := s.write(sf); werr != nil {
				errs = append(errs, werr)
			}
		}
		unlock()
	}

	// Keychain entry (i1: attempted even when the file half fails — a
	// surviving keychain copy would resurrect the credential on Get).
	if backend == "keychain" {
		_, kr := s.backendForStore()
		if err := kr.Delete(keyringService, provider); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, fmt.Errorf("auth: keychain delete for %q: %w", provider, err))
		}
	}
	return errors.Join(errs...)
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

func timeUnixRFC3339(sec int64) string {
	return time.Unix(sec, 0).Format(time.RFC3339)
}

// FormatStatusLine is THE per-credential status line for `auth status`
// (spec §5/§7.8 — the command and the secret-scan test share exactly
// this path, so the test covers the real output, not a mirror).
func FormatStatusLine(name string, cred *Credential, location string) string {
	expiry := "unknown"
	if cred.ExpiresAt != 0 {
		expiry = timeUnixRFC3339(cred.ExpiresAt)
	}
	loc := ""
	if location != "" {
		loc = "  [" + location + "]"
	}
	return fmt.Sprintf("  %-16s  token: %s  source: %-6s  status: %-17s  expires: %s%s",
		name, cred.String(), cred.Source, cred.Status(), expiry, loc)
}

// CredentialLocation reports where a credential physically lives for
// `auth status` (design D5): "keychain", "file", "keychain+file", or ""
// (not stored). When both copies exist and DIFFER, appends
// " (copies differ)" — no direction claimed (no timestamps exist).
func (s *Store) CredentialLocation(provider string) string {
	backend, _ := s.backendForStore()

	inFile := false
	sf, err := s.read()
	var fileCred *Credential
	if err == nil {
		fileCred, inFile = sf.Credentials[provider]
	}

	inKeychain := false
	var kcCred *Credential
	if backend == "keychain" {
		if c, err := s.keychainGet(provider); err == nil {
			inKeychain, kcCred = true, c
		}
	}

	switch {
	case inKeychain && inFile:
		if kcCred.AccessToken != fileCred.AccessToken || kcCred.RefreshToken != fileCred.RefreshToken {
			return "keychain+file (copies differ)"
		}
		return "keychain+file"
	case inKeychain:
		return "keychain"
	case inFile:
		return "file"
	}
	return ""
}
