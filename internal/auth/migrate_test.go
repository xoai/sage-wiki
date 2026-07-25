package auth

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestMigrateCopiesAllFileCreds(t *testing.T) {
	forceFileBackend(t)
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	s.writeFileOnly(t, fullCred())
	direct := &Credential{Provider: "anthropic", AccessToken: "sk-ant-x", RefreshToken: "", ExpiresAt: 0, Source: "env"}
	s.writeFileOnly(t, direct)

	res, err := MigrateToKeychain(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 2 {
		t.Fatalf("moved = %v, want 2 providers", res.Moved)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed = %v", res.Failed)
	}
	// File intact (point-in-time backup).
	got, err := s.Get("openai")
	if err != nil || got.AccessToken != "sk-live-token-abc" {
		t.Errorf("file copy altered: %v %v", got, err)
	}
	// Second run: existing keychain entries are SKIPPED — never
	// overwritten (a refresh after migration must not be reverted to the
	// frozen file copy).
	res2, err := MigrateToKeychain(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Moved) != 0 {
		t.Errorf("second migrate moved %v — must not overwrite keychain entries", res2.Moved)
	}
	if len(res2.Skipped) != 2 {
		t.Errorf("second migrate skipped = %v, want 2", res2.Skipped)
	}
}

func TestMigratePerProviderFailureTolerated(t *testing.T) {
	forceFileBackend(t)
	kr := &failingKeyring{failOn: "anthropic"}
	s := newTestStore(t, kr, "keychain")
	s.writeFileOnly(t, fullCred())
	s.writeFileOnly(t, &Credential{Provider: "anthropic", AccessToken: "sk-ant-x", RefreshToken: "", ExpiresAt: 0, Source: "env"})

	res, err := MigrateToKeychain(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Moved) != 1 || res.Moved[0] != "openai" {
		t.Errorf("moved = %v — openai should still migrate despite anthropic failing", res.Moved)
	}
	if len(res.Failed) != 1 || res.Failed[0].Name != "anthropic" {
		t.Errorf("failed = %v", res.Failed)
	}
}

type failingKeyring struct {
	failOn string
	inner  *recordingKeyring
}

func (f *failingKeyring) Get(service, user string) (string, error) {
	f.init()
	return f.inner.Get(service, user)
}
func (f *failingKeyring) Set(service, user, value string) error {
	if user == f.failOn {
		return errInjected
	}
	f.init()
	return f.inner.Set(service, user, value)
}
func (f *failingKeyring) Delete(service, user string) error {
	f.init()
	return f.inner.Delete(service, user)
}

func (f *failingKeyring) init() {
	if f.inner == nil {
		f.inner = newRecordingKeyring()
	}
}

type errInjectedT struct{ s string }

func (e errInjectedT) Error() string { return e.s }

var errInjected = errInjectedT{"injected keyring failure"}

func TestMigrateHeadlessFailsNonZero(t *testing.T) {
	forceFileBackend(t)
	s := newTestStore(t, nil, "file")
	_, err := MigrateToKeychain(s)
	if err == nil {
		t.Fatal("headless migrate must fail")
	}
	if !strings.Contains(err.Error(), "no OS keychain available") {
		t.Errorf("error = %v", err)
	}
}

func TestNoticeShownOnce(t *testing.T) {
	forceFileBackend(t)
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	s.writeFileOnly(t, fullCred())

	// First construction prints (and writes the flag).
	s.maybeKeychainNotice()
	sf, err := s.read()
	if err != nil {
		t.Fatal(err)
	}
	if !sf.KeychainNoticeShown {
		t.Error("flag not written after first notice")
	}
	// Second call: flag set → no print (verified by no lock/write activity —
	// the function returns before any write).
	s.maybeKeychainNotice()
	sf2, _ := s.read()
	if !sf2.KeychainNoticeShown {
		t.Error("flag lost")
	}
}

// TestNoticePrintsOnce captures stderr and pins single-print behavior.
func TestNoticePrintsOnce(t *testing.T) {
	forceFileBackend(t)
	kr := newRecordingKeyring()
	s := newTestStore(t, kr, "keychain")
	s.writeFileOnly(t, fullCred())

	out1 := captureStderr(t, func() { s.maybeKeychainNotice() })
	out2 := captureStderr(t, func() { s.maybeKeychainNotice() })
	if !strings.Contains(out1, "auth migrate") {
		t.Errorf("first call should print the notice, got %q", out1)
	}
	if out2 != "" {
		t.Errorf("second call printed again: %q", out2)
	}
	if strings.Contains(out1, "sk-live-token-abc") {
		t.Error("notice leaks token material")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = orig
	data, _ := io.ReadAll(r)
	r.Close()
	return string(data)
}
