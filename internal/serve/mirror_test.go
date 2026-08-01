package serve

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/xoai/sage-wiki/internal/mirror"
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
			b, _ := io.ReadAll(r.Body)
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

// TestShipper_WALSegmentWithin2xInterval (AC-9(a)): a db write through a
// served workspace produces a new WAL-SEGMENT object (not just a docs
// object) within 2×ship_interval.
func TestShipper_WALSegmentWithin2xInterval(t *testing.T) {
	interval := 25 * time.Millisecond
	fake, shipper, dir := shipperFixture(t, shipperFixtureOpts{
		interval:         interval,
		snapshotInterval: time.Hour,
		drainTimeout:     300 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	shipper.Start(ctx)
	defer shipper.Stop()

	// db write with the connection held open (WAL persists for sealing).
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('serve-row')"); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * interval)
	for time.Now().Before(deadline) {
		if fake.hasPrefix("ws/db/generation-1/wal/") {
			return // a WAL segment object shipped within 2×ship_interval
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no WAL segment shipped within 2×ship_interval of a db write")
}
