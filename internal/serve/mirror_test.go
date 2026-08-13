package serve

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xoai/sage-wiki/internal/mirror"
	"github.com/xoai/sage-wiki/pkg/events"
	pkmirror "github.com/xoai/sage-wiki/pkg/mirror"
)

type serveFakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newServeFakeS3() *serveFakeS3 { return &serveFakeS3{objects: map[string][]byte{}} }

func (f *serveFakeS3) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/bk/")
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			b, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "incomplete request body", http.StatusBadRequest)
				return
			}
			f.objects[key] = b
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			if r.URL.Query().Get("list-type") == "2" {
				prefix := r.URL.Query().Get("prefix")
				var sb strings.Builder
				sb.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
				for k := range f.objects {
					if strings.HasPrefix(k, prefix) {
						sb.WriteString("<Contents><Key>" + k + "</Key></Contents>")
					}
				}
				sb.WriteString(`</ListBucketResult>`)
				w.Write([]byte(sb.String()))
				return
			}
			if b, ok := f.objects[key]; ok {
				w.Write(b)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodHead:
			if _, ok := f.objects[key]; ok {
				w.WriteHeader(http.StatusOK)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}
		case http.MethodDelete:
			delete(f.objects, key)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func TestServeFakeS3InterruptedPutPreservesObject(t *testing.T) {
	fake := newServeFakeS3()
	key := "ws/mirror-state.json"
	want := []byte(`{"generation":1}`)
	fake.objects[key] = append([]byte(nil), want...)

	req := httptest.NewRequest(http.MethodPut, "/bk/"+key, nil)
	req.Body = io.NopCloser(io.MultiReader(
		strings.NewReader(`{"generation":`),
		iotest.ErrReader(io.ErrUnexpectedEOF),
	))
	rec := httptest.NewRecorder()
	srv := fake.server()
	t.Cleanup(srv.Close)
	srv.Config.Handler.ServeHTTP(rec, req)

	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("interrupted PUT status = %d, want non-2xx", rec.Code)
	}
	fake.mu.Lock()
	got, ok := fake.objects[key]
	fake.mu.Unlock()
	if !ok || !bytes.Equal(got, want) {
		t.Errorf("object after interrupted PUT = %q, want prior object %q", got, want)
	}
}

func (f *serveFakeS3) hasPrefix(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k := range f.objects {
		if strings.HasPrefix(k, prefix) {
			return true
		}
	}
	return false
}

type shipperFixtureOpts struct {
	interval         time.Duration
	snapshotInterval time.Duration
	drainTimeout     time.Duration
	endpoint         string // override (blocked-bucket tests)
}

func shipperFixture(t *testing.T, opts shipperFixtureOpts) (*serveFakeS3, *MirrorShipper, string) {
	t.Helper()
	fake := newServeFakeS3()
	srv := fake.server()
	t.Cleanup(srv.Close)
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t (v TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	endpoint := srv.URL
	if opts.endpoint != "" {
		endpoint = opts.endpoint
	}
	cfg := mirror.Config{
		Endpoint: endpoint, Bucket: "bk", Prefix: "ws/", Region: "auto",
		ShipInterval: opts.interval, SnapshotInterval: opts.snapshotInterval,
		DrainTimeout: opts.drainTimeout,
	}
	m, err := mirror.Open(dir, cfg, mirror.NewDiffChangeSource(dir))
	if err != nil {
		t.Fatal(err)
	}
	if opts.endpoint == "" {
		if err := m.Enable(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	return fake, NewMirrorShipper(m, cfg), dir
}

func TestShipper_SealsWithinInterval(t *testing.T) {
	fake, shipper, dir := shipperFixture(t, shipperFixtureOpts{
		interval:         20 * time.Millisecond,
		snapshotInterval: time.Hour,
		drainTimeout:     300 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.Start(ctx)
	defer shipper.Stop()

	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755)
	os.WriteFile(filepath.Join(dir, "wiki", "concepts", "Foo.md"), []byte("# Foo"), 0o644)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.hasPrefix("ws/objects/docs/") {
			return // sealed + shipped within cadence
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no object shipped within 2s of a write (ship_interval 20ms)")
}

func TestShipper_StopCommitsFinalPass(t *testing.T) {
	fake, shipper, dir := shipperFixture(t, shipperFixtureOpts{
		interval:         time.Hour, // never ticks
		snapshotInterval: time.Hour,
		drainTimeout:     2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.Start(ctx)

	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755)
	os.WriteFile(filepath.Join(dir, "wiki", "concepts", "Final.md"), []byte("# Final"), 0o644)

	shipper.Stop() // drain must ship the final pass despite the dead ticker
	if !fake.hasPrefix("ws/objects/docs/") {
		t.Fatal("final segment not committed by drain")
	}
}

func TestShipper_DrainBoundedOnBlockedBucket(t *testing.T) {
	_, shipper, _ := shipperFixture(t, shipperFixtureOpts{
		interval:         time.Hour,
		snapshotInterval: time.Hour,
		drainTimeout:     100 * time.Millisecond,
		endpoint:         "http://127.0.0.1:1", // dead
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.Start(ctx)

	start := time.Now()
	shipper.Stop()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("drain unbounded: %v", elapsed)
	}
	if !shipper.drainAbandoned {
		t.Fatal("blocked-bucket drain should record abandonment")
	}
}

func TestShipper_ScheduledRotation(t *testing.T) {
	fake, shipper, _ := shipperFixture(t, shipperFixtureOpts{
		interval:         20 * time.Millisecond,
		snapshotInterval: time.Millisecond, // always due
		drainTimeout:     300 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.Start(ctx)
	defer shipper.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.hasPrefix("ws/db/generation-2/") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("scheduled rotation did not fire")
}

// TestShipper_WALSegmentOnCadenceTick proves the mirror cadence contract: one
// ship tick after a database write produces a WAL segment object.
func TestShipper_WALSegmentOnCadenceTick(t *testing.T) {
	fake, shipper, dir := shipperFixture(t, shipperFixtureOpts{
		interval:         time.Hour,
		snapshotInterval: time.Hour,
		drainTimeout:     2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	loopDone := make(chan struct{})
	go func() {
		defer close(loopDone)
		shipper.runShipLoop(ctx, ticks)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-loopDone:
		case <-time.After(2 * time.Second):
			t.Error("ship loop did not stop after cancellation")
		}
	})

	// db write with the connection held open (WAL persists for sealing).
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('serve-row')"); err != nil {
		t.Fatal(err)
	}

	select {
	case ticks <- time.Now():
	case <-time.After(2 * time.Second):
		t.Fatal("ship loop did not accept cadence tick")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.hasPrefix("ws/db/generation-1/wal/") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("cadence tick did not ship pending WAL segment")
}

// TestShipper_StopQuiesceMakesNextFoldBenign (F-102): after a drained
// serve stops (final pass + Quiesce), the next process's pass classifies
// the stop's close-fold as benign — no spurious rotation, even past the
// debounce window.
func TestShipper_StopQuiesceMakesNextFoldBenign(t *testing.T) {
	fake, shipper, dir := shipperFixture(t, shipperFixtureOpts{
		interval:         20 * time.Millisecond,
		snapshotInterval: time.Hour,
		drainTimeout:     2 * time.Second,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.Start(ctx)
	// PRECONDITION the reviewer's scenario: the row is SEALED before the
	// stop (an unsealed row at stop correctly rotates — not the bug).
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('stop-row')"); err != nil {
		t.Fatal(err)
	}
	// Keep the handle OPEN while the ticker seals (the serve shape: the
	// engine handle is open during writes).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fake.hasPrefix("ws/db/generation-1/wal/") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !fake.hasPrefix("ws/db/generation-1/wal/") {
		t.Fatal("setup: row never sealed before stop")
	}
	// PRODUCTION ORDER: the engine handle is OPEN during the drain
	// (Shutdown closes it after deps.Close) — Quiesce folds and refreshes
	// BEFORE the stop-fold exists.
	shipper.Stop() // final pass (nothing new) + Quiesce (fold + refresh)
	db.Close()     // engine close: no-op fold, WAL deleted

	// Next process: a plain pass (no writes) — explicit NO rotation, NO
	// pending flag, even with the debounce aged away.
	m, err := mirror.Open(dir, shipper.m.Config(), mirror.NewDiffChangeSource(dir))
	if err != nil {
		t.Fatal(err)
	}
	stBefore := mirrorStateForTest(t, fake)
	ageMirrorLocal(t, dir)
	if err := m.Ship(context.Background(), pkmirror.ChangeBatch{}); err != nil {
		t.Fatalf("aged pass: %v", err)
	}
	stAfter := mirrorStateForTest(t, fake)
	if stAfter.Generation != stBefore.Generation {
		t.Fatalf("spurious rotation after drained stop: gen %d → %d", stBefore.Generation, stAfter.Generation)
	}
	rep, err := m.Verify(context.Background())
	if err != nil || !rep.Valid {
		t.Fatalf("verify: %+v %v", rep, err)
	}
	// The sealed row is restorable (the point of the whole exercise).
	dst := filepath.Join(t.TempDir(), "restored")
	if _, err := mirror.Hydrate(context.Background(), shipper.m.Config(), dst, mirror.HydrateOpts{}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	rdb, _ := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
	defer rdb.Close()
	var n int
	rdb.QueryRow("SELECT COUNT(*) FROM t WHERE v='stop-row'").Scan(&n)
	if n != 1 {
		t.Fatal("stop-row not restorable after drained stop")
	}
}

func mirrorStateForTest(t *testing.T, fake *serveFakeS3) mirrorStateView {
	t.Helper()
	fake.mu.Lock()
	defer fake.mu.Unlock()
	var st mirrorStateView
	if b, ok := fake.objects["ws/mirror-state.json"]; ok {
		jsonUnmarshalForTest(t, b, &st)
	}
	return st
}

type mirrorStateView struct {
	Generation int `json:"generation"`
}

func jsonUnmarshalForTest(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}

func ageMirrorLocal(t *testing.T, dir string) {
	t.Helper()
	path := filepath.Join(dir, ".sage", "mirror-local.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	marker := `"last_rotation_at"`
	start := strings.Index(s, marker)
	if start < 0 {
		return
	}
	tail := s[start:]
	end := strings.Index(tail, "\n")
	if end < 0 {
		end = len(tail)
	}
	replaced := `"last_rotation_at": "2020-01-01T00:00:00Z",` + tail[end:]
	os.WriteFile(path, []byte(s[:start]+replaced), 0o644)
}

// TestDepsSetEventSinkReachesMirrorShipper (SPEC-07 serve wiring): the
// workspace bus installed via Deps.SetEventSink receives mirror_shipped
// from the serve-path shipper — the propagation that single-mode
// (main.go) and multi-mode (stack.go) both rely on.
func TestDepsSetEventSinkReachesMirrorShipper(t *testing.T) {
	fake, shipper, dir := shipperFixture(t, shipperFixtureOpts{
		interval:         20 * time.Millisecond,
		snapshotInterval: time.Hour,
		drainTimeout:     300 * time.Millisecond,
	})
	_ = fake

	d := &Deps{dir: dir, mirrorShipper: shipper}
	sink := &mirrorEventCapture{}
	d.SetEventSink(sink) // must bind the shipper's mirror

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.Start(ctx)

	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755)
	os.WriteFile(filepath.Join(dir, "wiki", "concepts", "Bar.md"), []byte("# Bar"), 0o644)

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if sink.count() > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	shipper.Stop()

	if sink.count() == 0 {
		t.Fatal("no mirror_shipped event reached the Deps-installed sink")
	}
	if ws := sink.firstWorkspace(); ws != filepath.Base(dir) {
		t.Errorf("event workspace = %q, want %q (BindWorkspace)", ws, filepath.Base(dir))
	}
}

type mirrorEventCapture struct {
	mu     sync.Mutex
	events []mirrorEvent
}

type mirrorEvent struct {
	typ       string
	workspace string
}

func (c *mirrorEventCapture) Emit(ev events.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, mirrorEvent{typ: string(ev.Type), workspace: ev.Workspace})
}

func (c *mirrorEventCapture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if e.typ == string(events.TypeMirrorShipped) {
			n++
		}
	}
	return n
}

func (c *mirrorEventCapture) firstWorkspace() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.events {
		if e.typ == string(events.TypeMirrorShipped) {
			return e.workspace
		}
	}
	return ""
}
