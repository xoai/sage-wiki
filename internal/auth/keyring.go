package auth

import (
	"errors"
	"sync"
	"time"

	"github.com/zalando/go-keyring"
)

// keyringAPI is the three go-keyring calls behind an interface so tests
// never touch a real keychain (P2-6 spec §2). Service is always
// keyringService; user is the provider name (go-keyring's two-key shape:
// Get(service, user)).
type keyringAPI interface {
	Get(service, user string) (string, error)
	Set(service, user, value string) error
	Delete(service, user string) error
}

// keyringService prefixes every entry this app owns (design D7: no
// other app's keys are read).
const keyringService = "sage-wiki"

// goKeyring adapts zalando/go-keyring to keyringAPI.
type goKeyring struct{}

func (goKeyring) Get(service, user string) (string, error) { return keyring.Get(service, user) }
func (goKeyring) Set(service, user, value string) error    { return keyring.Set(service, user, value) }
func (goKeyring) Delete(service, user string) error        { return keyring.Delete(service, user) }

// probeKeyring returns "keychain" or "file" (spec §2): a READ of the
// dedicated probe key in a goroutine with a 500ms hard timeout (a locked
// Linux Secret Service collection can block with a dbus unlock prompt
// instead of returning — timeout maps prompt/lock/headless to file).
// The probe NEVER writes. The goroutine left to complete in the
// background holds no state and writes nothing.
//
// The result is computed ONCE per process (sync.Once) — resolve.go
// builds the store twice per client construction and every auth
// subcommand builds its own; without caching the dbus cost/prompt risk
// multiplies per invocation.
var (
	probeOnce   sync.Once
	probeResult string
)

func probeKeyring(kr keyringAPI) string {
	probeOnce.Do(func() {
		probeResult = runProbe(kr)
	})
	return probeResult
}

func runProbe(kr keyringAPI) string {
	type result struct{ err error }
	ch := make(chan result, 1)
	go func() {
		_, err := kr.Get(keyringService, "probe")
		ch <- result{err}
	}()
	select {
	case r := <-ch:
		if r.err == nil || errors.Is(r.err, keyring.ErrNotFound) {
			return "keychain"
		}
		return "file"
	case <-time.After(500 * time.Millisecond):
		return "file" // locked/prompting/headless — silent by design (spec §6)
	}
}

// probeKeyringForTest resets the cached probe (tests only).
func probeKeyringForTest(kr keyringAPI) string {
	probeOnce = sync.Once{}
	probeResult = ""
	return probeKeyring(kr)
}
