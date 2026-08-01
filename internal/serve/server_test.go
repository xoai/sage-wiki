package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"crypto/subtle"

	"github.com/xoai/sage-wiki/internal/wiki"
)

func testServer(t *testing.T, tokens []string) *Server {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gpt-4o-mini"); err != nil {
		t.Fatal(err)
	}
	deps, err := AssembleDeps(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deps.Close)
	cfg := Config{
		Workspace: dir,
		Tokens:    tokens,
		ReadyFn:   func() bool { return true },
		RateLimit: nil,
	}
	srv, err := New(deps, nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func TestHealthzReadyz(t *testing.T) {
	srv := testServer(t, nil)
	h := srv.Handler()

	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("healthz = %d", w.Code)
	}

	srv.cfg.ReadyFn = func() bool { return false }
	r = httptest.NewRequest("GET", "/readyz", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("readyz before ready = %d, want 503", w.Code)
	}
	srv.cfg.ReadyFn = func() bool { return true }
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("readyz after ready = %d, want 200", w.Code)
	}
}

func TestAuthValidInvalidMissing(t *testing.T) {
	srv := testServer(t, []string{"s3cret"})
	h := srv.Handler()

	for _, tc := range []struct {
		name   string
		header string
		want   int
	}{
		{"valid", "Bearer s3cret", 200},
		{"invalid", "Bearer wrong", 401},
		{"missing", "", 401},
	} {
		r := httptest.NewRequest("GET", "/jobs", nil)
		if tc.header != "" {
			r.Header.Set("Authorization", tc.header)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, w.Code, tc.want)
		}
		if tc.want == 401 && strings.Contains(w.Body.String(), "s3cret") {
			t.Errorf("token leaked in error body: %s", w.Body.String())
		}
	}

	// Open paths bypass auth.
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Errorf("healthz must not require a token: %d", w.Code)
	}
}

func TestLoadTokensPrecedenceAndPerms(t *testing.T) {
	dir := t.TempDir()
	f := dir + "/tokens.txt"
	if err := writeTestFile(f, "# comment\n\nfile-token-1\nfile-token-2\n"); err != nil {
		t.Fatal(err)
	}
	toks, err := LoadTokens("", f, "env-token", "cfg-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(toks) != 2 || toks[0] != "file-token-1" || toks[1] != "file-token-2" {
		t.Errorf("file tokens = %v", toks)
	}
	toks, _ = LoadTokens("flag-token", f, "env-token", "cfg-token")
	if len(toks) != 1 || toks[0] != "flag-token" {
		t.Errorf("flag must beat file: %v", toks)
	}
	toks, _ = LoadTokens("", "", "env-token", "cfg-token")
	if len(toks) != 1 || toks[0] != "env-token" {
		t.Errorf("env fallback: %v", toks)
	}
}

func TestCheckRefusal(t *testing.T) {
	if err := CheckRefusal("0.0.0.0:8484", nil, false); err == nil || !strings.Contains(err.Error(), "--insecure-no-auth") {
		t.Errorf("non-loopback refusal must name --insecure-no-auth: %v", err)
	}
	if err := CheckRefusal("0.0.0.0:8484", nil, true); err != nil {
		t.Errorf("insecure override must allow: %v", err)
	}
	if err := CheckRefusal("0.0.0.0:8484", []string{"t"}, false); err != nil {
		t.Errorf("token present must allow: %v", err)
	}
	if err := CheckRefusal("127.0.0.1:8484", nil, false); err != nil {
		t.Errorf("loopback without token must be allowed: %v", err)
	}
}

func TestConstantTimeComparesAllCandidates(t *testing.T) {
	// Structural property: the presented digest is compared against EVERY
	// configured digest (no early-exit on length mismatch — digests are
	// uniform 32-byte SHA-256).
	a, b := tokenDigest("alpha"), tokenDigest("beta")
	if len(a) != len(b) || len(a) != 32 {
		t.Fatal("digests must be uniform length")
	}
}

func TestCompileJobFlow(t *testing.T) {
	srv := testServer(t, nil)
	// Stub the exec to avoid a real compile.
	srv.queue = NewQueue(srv.ledger, 1, func(ctx context.Context, j *Job) (json.RawMessage, error) {
		return json.RawMessage(`{"added":0}`), nil
	}, clockSeq(time.Millisecond))
	h := srv.Handler()

	body := bytes.NewReader([]byte(`{"model":"gpt-4o-mini","max_docs":1}`))
	r := httptest.NewRequest("POST", "/compile", body)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusAccepted {
		t.Fatalf("compile = %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["job_id"] == "" {
		t.Fatal("no job_id")
	}

	r = httptest.NewRequest("GET", "/jobs/"+resp["job_id"], nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("job get = %d", w.Code)
	}
	var j Job
	json.Unmarshal(w.Body.Bytes(), &j)
	if j.Kind != "compile" || j.Request.Model != "gpt-4o-mini" {
		t.Errorf("job = %+v", j)
	}

	r = httptest.NewRequest("GET", "/jobs/nope", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 {
		t.Errorf("missing job = %d, want 404", w.Code)
	}
}

func TestDocNegativeSrcID(t *testing.T) {
	srv := testServer(t, nil)
	h := srv.Handler()
	r := httptest.NewRequest("GET", "/docs/src:raw/foo.md", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "not an article id") {
		t.Errorf("src: doc id = %d %s", w.Code, w.Body.String())
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// TestAnyTokenMatchComparesEveryCandidate pins Q-5's structural property:
// the comparator runs once per candidate even when the first matches or
// the presented digest matches nothing.
func TestAnyTokenMatchComparesEveryCandidate(t *testing.T) {
	digests := [][]byte{tokenDigest("a"), tokenDigest("b"), tokenDigest("c")}
	calls := 0
	counting := func(a, b []byte) int {
		calls++
		return subtle.ConstantTimeCompare(a, b)
	}
	if !anyTokenMatch(tokenDigest("a"), digests, counting) {
		t.Fatal("first candidate should match")
	}
	if calls != len(digests) {
		t.Errorf("early exit leaked: %d compares for %d digests", calls, len(digests))
	}
	calls = 0
	if anyTokenMatch(tokenDigest("zzz"), digests, counting) {
		t.Fatal("no candidate should match")
	}
	if calls != len(digests) {
		t.Errorf("miss path early exit: %d compares for %d digests", calls, len(digests))
	}
	calls = 0
	if anyTokenMatch(tokenDigest(""), digests, counting) {
		t.Fatal("empty presented must not match")
	}
	if calls != len(digests) {
		t.Errorf("empty presented early exit: %d compares", calls)
	}
}
