package auth

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

type authTransport struct {
	base     http.RoundTripper
	store    *Store
	provider string

	mu   sync.RWMutex
	cred *Credential
}

func NewAuthTransport(base http.RoundTripper, store *Store, provider string) http.RoundTripper {
	cred, _ := store.Get(provider)
	return &authTransport{
		base:     base,
		store:    store,
		provider: provider,
		cred:     cred,
	}
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cred := t.cachedCred()
	if cred == nil || cred.ExpiresWithin(5*time.Minute) {
		var err error
		cred, err = t.refreshCred()
		if err != nil {
			return nil, fmt.Errorf("auth: token refresh failed for %s: %w — "+
				"run `sage-wiki auth login --provider %s` to re-authenticate",
				t.provider, err, t.provider)
		}
	}

	r2 := req.Clone(req.Context())

	r2.Header.Del("x-api-key")
	r2.Header.Del("Authorization")

	if q := r2.URL.Query(); q.Has("key") {
		q.Del("key")
		r2.URL.RawQuery = q.Encode()
	}

	r2.Header.Set("Authorization", "Bearer "+cred.AccessToken)
	for k, v := range cred.ExtraHeaders() {
		r2.Header.Set(k, v)
	}

	return t.base.RoundTrip(r2)
}

func (t *authTransport) cachedCred() *Credential {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.cred
}

func (t *authTransport) refreshCred() (*Credential, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.cred != nil && !t.cred.ExpiresWithin(5*time.Minute) {
		return t.cred, nil
	}

	cred, err := t.store.RefreshAndGet(t.provider)
	if err != nil {
		return nil, err
	}
	t.cred = cred
	return cred, nil
}
