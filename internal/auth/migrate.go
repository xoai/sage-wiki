package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/zalando/go-keyring"
)

// Migration support (P2-6 spec §4): the explicit, only path copying file
// credentials into the OS keychain — plus the one-time first-run notice.

// MigrateResult reports a migration pass.
type MigrateResult struct {
	Moved   []string
	Skipped []string
	Failed  []FailedProvider
}

// FailedProvider is one provider whose keychain write failed.
type FailedProvider struct {
	Name string
	Err  error
}

// MigrateToKeychain copies every file credential into the keyring,
// enumerating the closed provider set (auth.Providers keys — go-keyring
// has no List API). The file is left intact (point-in-time backup).
// Per-provider failures are reported, never abort.
func MigrateToKeychain(s *Store) (*MigrateResult, error) {
	backend, kr := s.backendForStore()
	if backend != "keychain" {
		return nil, fmt.Errorf("no OS keychain available")
	}
	sf, err := s.read()
	if err != nil {
		return nil, err
	}
	res := &MigrateResult{}
	for name := range Providers {
		cred, ok := sf.Credentials[name]
		if !ok {
			continue
		}
		// Never overwrite an existing keychain entry (i1 CRITICAL: a
		// re-run after refresh would revert rotated tokens to the frozen
		// file copy). Skip and report instead.
		if _, err := kr.Get(keyringService, name); err == nil {
			res.Skipped = append(res.Skipped, name)
			continue
		} else if !errors.Is(err, keyring.ErrNotFound) {
			// A locked keyring read is NOT "absent" — writing here could
			// overwrite an existing entry (the revert-rotated-token hole).
			res.Failed = append(res.Failed, FailedProvider{name, err})
			continue
		}
		snapshot := *cred // copy-then-marshal (i1: no mutation of the caller's Credential)
		snapshot.Provider = ""
		data, err := json.Marshal(&snapshot)
		if err != nil {
			res.Failed = append(res.Failed, FailedProvider{name, err})
			continue
		}
		if err := kr.Set(keyringService, name, string(data)); err != nil {
			res.Failed = append(res.Failed, FailedProvider{name, err})
			continue
		}
		res.Moved = append(res.Moved, name)
	}
	return res, nil
}

// storeFile gains the persisted notice-suppression flag (spec §4: older
// binaries tolerate the new field — the read path is plain
// json.Unmarshal with no DisallowUnknownFields).

// maybeKeychainNotice prints the first-run notice at most once (persisted
// via KeychainNoticeShown in the file store). Never blocks, never prints
// secrets.
func (s *Store) maybeKeychainNotice() {
	backend, _ := s.backendForStore()
	if backend != "keychain" {
		return
	}
	sf, err := s.read()
	if err != nil || sf.KeychainNoticeShown || len(sf.Credentials) == 0 {
		return
	}
	// Lock + flag-write BEFORE printing (i2: two concurrent NewStore
	// calls must not both print — the flag is the single-print guard).
	unlock, err := lockFile(s.path)
	if err != nil {
		return
	}
	defer unlock()
	sf, err = s.read()
	if err != nil || sf.KeychainNoticeShown {
		return
	}
	sf.KeychainNoticeShown = true
	if err := s.write(sf); err != nil {
		return
	}
	fmt.Fprintln(os.Stderr, "credentials found in file backup — run `sage-wiki auth migrate` to move them into the OS keychain (file fallback unchanged)")
}
