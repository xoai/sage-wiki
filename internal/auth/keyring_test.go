package auth

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

type mockKeyring struct {
	getFn func(service, user string) (string, error)
	calls *int32
}

func (m mockKeyring) Get(service, user string) (string, error) {
	atomic.AddInt32(m.calls, 1)
	return m.getFn(service, user)
}
func (m mockKeyring) Set(service, user, value string) error { return nil }
func (m mockKeyring) Delete(service, user string) error     { return nil }

func resetProbeCache(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		probeOnce = sync.Once{}
		probeResult = ""
	})
}

func TestProbeKeyringOutcomes(t *testing.T) {
	cases := []struct {
		name string
		get  func(service, user string) (string, error)
		want string
	}{
		{"success", func(s, u string) (string, error) { return "x", nil }, "keychain"},
		{"not found", func(s, u string) (string, error) { return "", keyring.ErrNotFound }, "keychain"},
		{"other error", func(s, u string) (string, error) { return "", errors.New("no dbus") }, "file"},
	}
	resetProbeCache(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := probeKeyringForTest(mockKeyring{getFn: tc.get, calls: new(int32)}); got != tc.want {
				t.Errorf("probe = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestProbeTimeout(t *testing.T) {
	resetProbeCache(t)
	// A locked keyring (blocking read) maps to file backend within ~500ms
	// and emits NOTHING (spec §6: silent by design).
	start := time.Now()
	got := probeKeyringForTest(mockKeyring{
		getFn: func(s, u string) (string, error) {
			time.Sleep(5 * time.Second)
			return "", nil
		},
		calls: new(int32),
	})
	if got != "file" {
		t.Errorf("timeout probe = %q, want file", got)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Errorf("probe took %v — timeout not enforced", d)
	}
}

func TestProbeCachedPerProcess(t *testing.T) {
	resetProbeCache(t)
	var calls int32
	kr := mockKeyring{
		getFn: func(s, u string) (string, error) { return "", keyring.ErrNotFound },
		calls: &calls,
	}
	probeOnce = sync.Once{}
	probeResult = ""
	probeKeyring(kr)
	probeKeyring(kr)
	if calls != 1 {
		t.Errorf("probe ran %d times across two constructions, want 1 (sync.Once)", calls)
	}
}
