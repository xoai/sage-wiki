package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, srv *httptest.Server, opts ...Option) *Client {
	t.Helper()
	c, err := NewClient(srv.URL, "us-east-1", testCreds, opts...)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	return c
}

func TestPutObject_SignedAndPathed(t *testing.T) {
	var gotAuth, gotPath, gotHash string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotHash = r.Header.Get("X-Amz-Content-Sha256")
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := newTestClient(t, srv, WithPathStyle(true))
	if err := c.PutObject(context.Background(), "bucket", "some/key.txt", []byte("hello")); err != nil {
		t.Fatalf("PutObject: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("unsigned request: %q", gotAuth)
	}
	if gotPath != "/bucket/some/key.txt" {
		t.Fatalf("path-style path = %q", gotPath)
	}
	if gotHash != sha256hex([]byte("hello")) {
		t.Fatalf("payload hash = %q", gotHash)
	}
	if string(gotBody) != "hello" {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestVirtualHostStyle(t *testing.T) {
	// Virtual-host addressing changes the connection target, so assert URL
	// construction via newRequest instead of dialing (bucket.<host> does not
	// resolve in tests).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	c := newTestClient(t, srv) // virtual-host default
	req, err := c.newRequest(context.Background(), http.MethodPut, "bucket", "k", nil, []byte("x"), sha256hex([]byte("x")))
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}
	host := srv.Listener.Addr().String()
	if req.URL.Host != "bucket."+host {
		t.Fatalf("virtual-host = %q, want bucket.%s", req.URL.Host, host)
	}
	if req.URL.Path != "/k" {
		t.Fatalf("path = %q", req.URL.Path)
	}
}

func TestGetObject_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true))
	_, err := c.GetObject(context.Background(), "b", "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestGetObject_Bytes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("payload"))
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true))
	b, err := c.GetObject(context.Background(), "b", "k")
	if err != nil || string(b) != "payload" {
		t.Fatalf("GetObject = %q, %v", b, err)
	}
}

func TestHeadObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/b/exists" {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true))
	ok, err := c.HeadObject(context.Background(), "b", "exists")
	if err != nil || !ok {
		t.Fatalf("Head exists = %v, %v", ok, err)
	}
	ok, err = c.HeadObject(context.Background(), "b", "nope")
	if err != nil || ok {
		t.Fatalf("Head missing = %v, %v", ok, err)
	}
}

func TestRetryOn5xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true), WithBackoff(0)) // no sleep in tests
	if err := c.PutObject(context.Background(), "b", "k", []byte("x")); err != nil {
		t.Fatalf("PutObject after retries: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
}

func TestNoRetryOn4xx(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true))
	err := c.PutObject(context.Background(), "b", "k", []byte("x"))
	if err == nil {
		t.Fatal("expected error on 403")
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1 (no retry on 4xx)", calls.Load())
	}
}

func TestContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := c.PutObject(ctx, "b", "k", []byte("x"))
	if err == nil {
		t.Fatal("expected ctx error")
	}
}

func TestListObjects_Pagination(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("continuation-token") == "" {
			fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>true</IsTruncated><NextContinuationToken>TOK</NextContinuationToken><Contents><Key>a</Key></Contents></ListBucketResult>`)
		} else if r.URL.Query().Get("continuation-token") == "TOK" {
			fmt.Fprint(w, `<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated><Contents><Key>b</Key></Contents></ListBucketResult>`)
		} else {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true))
	keys, err := c.ListObjects(context.Background(), "b", "prefix/")
	if err != nil {
		t.Fatalf("ListObjects: %v", err)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestDeleteObject(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true))
	if err := c.DeleteObject(context.Background(), "b", "k"); err != nil {
		t.Fatalf("DeleteObject: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q", gotMethod)
	}
}

func TestNewClient_InvalidEndpoint(t *testing.T) {
	if _, err := NewClient("://bad", "us-east-1", testCreds); err == nil {
		t.Fatal("expected error for invalid endpoint")
	}
}

// callTimeout formula (follow-up item 5): max(30s, 3×len/256KiB)s, cap 15m.
func TestCallTimeout_Formula(t *testing.T) {
	c := &Client{callTimeoutBase: defaultCallTimeout, callTimeoutCap: maxCallTimeout, rateBytesPerSec: defaultRateBytesPerSec}
	cases := []struct {
		bodyLen int
		want    time.Duration
	}{
		{0, 30 * time.Second},
		{1 << 20, 30 * time.Second},   // 1MiB → floor
		{16 << 20, 192 * time.Second}, // 16MiB → 3×64s
		{1 << 30, 15 * time.Minute},   // 1GiB → capped
	}
	for _, tc := range cases {
		if got := c.callTimeout(tc.bodyLen); got != tc.want {
			t.Fatalf("callTimeout(%d) = %v, want %v", tc.bodyLen, got, tc.want)
		}
	}
	// F-027 regime: tiny injected rate — the early clamp must return the cap,
	// never overflow int64 ns (reviewer repro: 3GiB @ 1B/s).
	c2 := &Client{callTimeoutBase: defaultCallTimeout, callTimeoutCap: maxCallTimeout, rateBytesPerSec: 1}
	if got := c2.callTimeout(3 << 30); got != maxCallTimeout {
		t.Fatalf("callTimeout(3GiB @ 1B/s) = %v, want cap %v", got, maxCallTimeout)
	}
}

// Stall-abort: a server that accepts then hangs is cut at the per-attempt
// cap — wall-clock bounded.
func TestPutObject_StallAbort(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(3 * time.Second) // hang past the attempt timeout
	}))
	defer srv.Close()
	c := newTestClient(t, srv, WithPathStyle(true), WithCallTimeout(200*time.Millisecond), WithBackoff(0))
	start := time.Now()
	err := c.PutObject(context.Background(), "b", "k", []byte("x"))
	if err == nil {
		t.Fatal("stalled server must error")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("stall abort took %v (>2s; want ≈ attempt cap)", el)
	}
}

// Slow-but-completes: payload above the old 30s cap completes under scaling.
func TestPutObject_SlowCompletes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	// Base 30s would survive anyway — shrink it via scale override so the
	// slow server NEEDS the scaled timeout.
	c := newTestClient(t, srv, WithPathStyle(true), WithCallTimeoutScale(1<<20)) // 1MiB/s
	body := make([]byte, 2<<20)                                                  // 2MiB → 3×2s = 6s timeout
	if err := c.PutObject(context.Background(), "b", "k", body); err != nil {
		t.Fatalf("slow PUT should complete under scaled timeout: %v", err)
	}
}
