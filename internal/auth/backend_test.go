package auth

import (
	"encoding/json"
	"errors"
	"strings"
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
	// Direct construction: bypasses NewStore's maybeKeychainNotice, which
	// would probe the REAL keyring in tests (500ms cost + prompt risk +
	// unwanted notice output).
	return &Store{path: t.TempDir() + "/auth.json", backend: backend, kr: kr}
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
	before := s.readFileBytes(t)
	refreshed := fullCred()
	refreshed.AccessToken = "sk-NEW-rotated"
	if err := s.refreshAndStore("openai", refreshed); err != nil {
		t.Fatal(err)
	}
	if kr.sets != 2 {
		t.Errorf("keyring sets = %d, want 2 (initial + refresh)", kr.sets)
	}
	after := s.readFileBytes(t)
	if string(before) != string(after) {
		t.Error("refresh wrote to the FILE on the keychain backend — must route through backend Put")
	}
}

func TestCredentialLocation(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	cred := fullCred()

	// File only.
	s.writeFileOnly(t, cred)
	if got := s.CredentialLocation("openai"); got != "file" {
		t.Errorf("file-only location = %q", got)
	}

	// Both, same content.
	if err := s.Put("openai", cred); err != nil {
		t.Fatal(err)
	}
	if got := s.CredentialLocation("openai"); got != "keychain+file" {
		t.Errorf("both location = %q", got)
	}

	// Both, divergent.
	stale := fullCred()
	stale.AccessToken = "sk-OLD-stale"
	sf, _ := s.read()
	sf.Credentials["openai"] = stale
	unlock, _ := lockFile(s.path)
	s.write(sf)
	unlock()
	if got := s.CredentialLocation("openai"); got != "keychain+file (copies differ)" {
		t.Errorf("divergent location = %q", got)
	}

	// Keychain only.
	if err := s.deleteWithBackend("openai"); err != nil {
		t.Fatal(err)
	}
	if err := kr.Set(keyringService, "openai", mustJSON(t, cred)); err != nil {
		t.Fatal(err)
	}
	if got := s.CredentialLocation("openai"); got != "keychain" {
		t.Errorf("keychain-only location = %q", got)
	}

	// Absent.
	if got := s.CredentialLocation("gemini"); got != "" {
		t.Errorf("absent location = %q", got)
	}
}

func mustJSON(t *testing.T, c *Credential) string {
	t.Helper()
	c2 := *c
	c2.Provider = ""
	data, err := json.Marshal(&c2)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestStatusOutputNoFullSecrets pins the secrets discipline (spec §7.8):
// `auth status` output must NEVER contain the FULL access or refresh
// token strings (the last-4 display via cred.String() is allowed).
func TestStatusOutputNoFullSecrets(t *testing.T) {
	cred := fullCred()
	out := FormatStatusLine("openai", cred, "keychain+file")
	if strings.Contains(out, cred.AccessToken) {
		t.Errorf("status line leaks the FULL access token: %s", out)
	}
	if strings.Contains(out, cred.RefreshToken) {
		t.Errorf("status line leaks the FULL refresh token: %s", out)
	}
	if !strings.Contains(out, cred.String()) {
		t.Errorf("status line should include the redacted display: %s", out)
	}
}

// TestListKeychainMergedUnion pins the spec §3 List row: file ∪ keychain
// over auth.Providers, keychain winning on conflict.
func TestListKeychainMergedUnion(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")

	fileCred := fullCred()
	fileCred.AccessToken = "sk-FILE-stale"
	s.writeFileOnly(t, fileCred)
	s.writeFileOnly(t, &Credential{Provider: "gemini", AccessToken: "gem-file", Source: "file"})

	kcCred := fullCred()
	kcCred.AccessToken = "sk-KEYCHAIN-fresh"
	if err := s.Put("openai", kcCred); err != nil {
		t.Fatal(err)
	}

	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if got := list["openai"].AccessToken; got != "sk-KEYCHAIN-fresh" {
		t.Errorf("keychain must win on conflict, got %q", got)
	}
	if got := list["gemini"].AccessToken; got != "gem-file" {
		t.Errorf("file-only credential missing from merged list: %q", got)
	}
}

// TestTOSOnKeychainBackend pins spec §7.9: TOS methods work on the FILE
// regardless of the credential backend.
func TestTOSOnKeychainBackend(t *testing.T) {
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	if s.IsTOSAcknowledged() {
		t.Error("TOS acknowledged on fresh store")
	}
	if err := s.AcknowledgeTOS(); err != nil {
		t.Fatal(err)
	}
	if !s.IsTOSAcknowledged() {
		t.Error("TOS not persisted on keychain backend")
	}
	if kr.sets != 0 {
		t.Error("TOS touched the keychain (must stay file-based)")
	}
}
