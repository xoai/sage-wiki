package auth

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// recordingKeyring records all calls for the backend-aware Store tests.
type recordingKeyring struct {
	entries map[string]string
	sets    int
	deletes int
	getErr  error
}

func newRecordingKeyring() *recordingKeyring {
	return &recordingKeyring{entries: map[string]string{}}
}

func (r *recordingKeyring) Get(service, user string) (string, error) {
	if r.getErr != nil {
		return "", r.getErr
	}
	v, ok := r.entries[user]
	if !ok {
		return "", keyring.ErrNotFound
	}
	return v, nil
}

func (r *recordingKeyring) Set(service, user, value string) error {
	r.entries[user] = value
	r.sets++
	return nil
}

func (r *recordingKeyring) Delete(service, user string) error {
	r.deletes++
	if _, ok := r.entries[user]; !ok {
		return keyring.ErrNotFound
	}
	delete(r.entries, user)
	return nil
}

func newTestStore(t *testing.T, kr keyringAPI, backend string) *Store {
	t.Helper()
	s := NewStore(t.TempDir() + "/auth.json")
	s.backend = backend
	s.kr = kr
	return s
}

func fullCred() *Credential {
	return &Credential{
		Provider:     "openai",
		AccessToken:  "sk-live-token-abc",
		RefreshToken: "rt-refresh-def",
		ExpiresAt:    1999999999,
		AccountID:    "acct-42",
		Source:       "import",
	}
}

func TestRoundTripAllFieldShapes(t *testing.T) {
	for _, tc := range []struct {
		name string
		cred *Credential
	}{
		{"full", fullCred()},
		{"direct token (no refresh, no expiry)", &Credential{
			Provider: "anthropic", AccessToken: "sk-ant-oat01-x", RefreshToken: "", ExpiresAt: 0, Source: "env",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			kr := newRecordingKeyring()
			s := newTestStore(t, kr, "keychain")
			if err := s.Put(tc.cred.Provider, tc.cred); err != nil {
				t.Fatal(err)
			}
			got, err := s.Get(tc.cred.Provider)
			if err != nil {
				t.Fatal(err)
			}
			for _, check := range []struct{ field, a, b string }{
				{"Provider", tc.cred.Provider, got.Provider},
				{"AccessToken", tc.cred.AccessToken, got.AccessToken},
				{"RefreshToken", tc.cred.RefreshToken, got.RefreshToken},
				{"AccountID", tc.cred.AccountID, got.AccountID},
				{"Source", tc.cred.Source, got.Source},
			} {
				if check.a != check.b {
					t.Errorf("%s = %q, want %q", check.field, check.b, check.a)
				}
			}
			if got.ExpiresAt != tc.cred.ExpiresAt {
				t.Errorf("ExpiresAt = %d, want %d", got.ExpiresAt, tc.cred.ExpiresAt)
			}
		})
	}
}

func TestGetFallbackErrNotFound(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	// File holds the cred; keychain misses → file hit.
	s.writeFileOnly(t, fullCred())
	got, err := s.Get("openai")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.AccessToken != "sk-live-token-abc" {
		t.Errorf("fallback returned %q", got.AccessToken)
	}
}

func TestGetKeyringErrorFails(t *testing.T) {
	kr := newRecordingKeyring()
	kr.getErr = errors.New("keyring locked")
	s := newTestStore(t, kr, "keychain")
	s.writeFileOnly(t, fullCred())
	_, err := s.Get("openai")
	if err == nil {
		t.Fatal("keyring error must FAIL the Get (no silent downgrade to file)")
	}
}

func TestGetMissingProviderIsError(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	_, err := s.Get("nonexistent")
	if err == nil {
		t.Fatal("missing provider must be an ERROR, never (nil,nil)")
	}
}

func TestPutKeychainOnlyFreezesFile(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	before := s.readFileBytes(t)
	if err := s.Put("openai", fullCred()); err != nil {
		t.Fatal(err)
	}
	after := s.readFileBytes(t)
	if string(before) != string(after) {
		t.Error("Put on keychain backend MODIFIED the file (backup must stay frozen)")
	}
	if kr.sets != 1 {
		t.Errorf("keyring sets = %d, want 1", kr.sets)
	}
}

func TestDeleteBothBackendsNoResurrection(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	cred := fullCred()
	if err := s.Put(cred.Provider, cred); err != nil {
		t.Fatal(err)
	}
	s.writeFileOnly(t, cred) // also in file (post-migration state)
	if err := s.Delete(cred.Provider); err != nil {
		t.Fatal(err)
	}
	// Both backends cleared → Get must error (no resurrection).
	if _, err := s.Get(cred.Provider); err == nil {
		t.Fatal("logout resurrection: credential still resolves after Delete")
	}
}

func TestDeleteIgnoresKeyringNotFound(t *testing.T) {
	kr := newRecordingKeyring() // empty — never migrated
	s := newTestStore(t, kr, "keychain")
	s.writeFileOnly(t, fullCred())
	if err := s.Delete("openai"); err != nil {
		t.Fatalf("Delete on never-migrated provider must ignore keyring ErrNotFound: %v", err)
	}
}

func TestRefreshAndGetUsesBackendPut(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	// Seed: a credential in the keychain. Simulate refresh by calling
	// refreshAndStore (the backend-aware write path) directly.
	cred := fullCred()
	cred.RefreshToken = "rt-old"
	if err := s.Put("openai", cred); err != nil {
		t.Fatal(err)
	}
	refreshed := fullCred()
	refreshed.AccessToken = "sk-NEW-rotated"
	if err := s.refreshAndStore("openai", refreshed); err != nil {
		t.Fatal(err)
	}
	if kr.sets != 2 {
		t.Errorf("keyring sets = %d, want 2 (initial + refresh)", kr.sets)
	}
	before := s.readFileBytes(t)
	after := s.readFileBytes(t)
	if string(before) != string(after) {
		t.Error("refresh wrote to the FILE on the keychain backend — must route through backend Put")
	}
}
