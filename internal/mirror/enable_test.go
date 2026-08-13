package mirror

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

	_ "modernc.org/sqlite"
)

// fakeS3 is an in-memory S3 for enable/ship tests (path-style).
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
	putLog  []string
}

func newFakeS3() *fakeS3 { return &fakeS3{objects: map[string][]byte{}} }

func (f *fakeS3) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/b/") // bucket "b"
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
			f.putLog = append(f.putLog, key)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
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
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
}

func TestFakeS3InterruptedPutPreservesObject(t *testing.T) {
	fake := newFakeS3()
	key := StateKey("ws/")
	want := []byte(`{"generation":1}`)
	fake.objects[key] = append([]byte(nil), want...)

	req := httptest.NewRequest(http.MethodPut, "/b/"+key, nil)
	req.Body = io.NopCloser(io.MultiReader(
		strings.NewReader(`{"generation":`),
		iotest.ErrReader(io.ErrUnexpectedEOF),
	))
	rec := httptest.NewRecorder()
	fake.handler().ServeHTTP(rec, req)

	if rec.Code >= 200 && rec.Code < 300 {
		t.Errorf("interrupted PUT status = %d, want non-2xx", rec.Code)
	}
	got, ok := fake.get(key)
	if !ok || !bytes.Equal(got, want) {
		t.Errorf("object after interrupted PUT = %q, want prior object %q", got, want)
	}
	if len(fake.putLog) != 0 {
		t.Errorf("successful PUT log = %v, want empty", fake.putLog)
	}
}

func (f *fakeS3) get(key string) ([]byte, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.objects[key]
	return b, ok
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}
	return out
}

// fakeList adds ListObjectsV2 support (separate handler for query requests).
func (f *fakeS3) handlerWithList() http.Handler {
	base := f.handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Query().Get("list-type") == "2" {
			f.mu.Lock()
			defer f.mu.Unlock()
			prefix := r.URL.Query().Get("prefix")
			var keys []string
			for k := range f.objects {
				if strings.HasPrefix(k, prefix) {
					keys = append(keys, k)
				}
			}
			w.Header().Set("Content-Type", "application/xml")
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
			for _, k := range keys {
				b.WriteString("<Contents><Key>" + k + "</Key></Contents>")
			}
			b.WriteString(`</ListBucketResult>`)
			w.Write([]byte(b.String()))
			return
		}
		base.ServeHTTP(w, r)
	})
}

func setupFakeMirror(t *testing.T, fake *fakeS3) (*httptest.Server, Config) {
	t.Helper()
	srv := httptest.NewServer(fake.handlerWithList())
	t.Cleanup(srv.Close)
	t.Setenv("MIRROR_TEST_AK", "ak")
	t.Setenv("MIRROR_TEST_SK", "sk")
	return srv, Config{
		Endpoint:     srv.URL,
		Bucket:       "b",
		Prefix:       "ws/",
		Region:       "auto",
		AccessKeyEnv: "MIRROR_TEST_AK",
		SecretKeyEnv: "MIRROR_TEST_SK",
	}
}

func makeWorkspaceWithDB(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT); INSERT INTO t VALUES (1, 'x')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return dir
}

func TestEnable_WritesManifestSnapshotState(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	dir := makeWorkspaceWithDB(t)

	m, err := Open(dir, cfg, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}

	// manifest.json
	mb, ok := fake.get("ws/manifest.json")
	if !ok {
		t.Fatal("manifest.json not PUT")
	}
	var man MirrorManifest
	if err := json.Unmarshal(mb, &man); err != nil {
		t.Fatalf("manifest parse: %v", err)
	}
	if man.FormatVersion != FormatVersion || man.Tool != "sage-wiki" || man.Encrypted {
		t.Fatalf("manifest = %+v", man)
	}

	// snapshot + mirror-state committed (state LAST).
	sb, ok := fake.get("ws/mirror-state.json")
	if !ok {
		t.Fatal("mirror-state.json not committed")
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		t.Fatalf("state parse: %v", err)
	}
	if err := st.Validate(); err != nil {
		t.Fatalf("committed state invalid: %v", err)
	}
	if st.Generation != 1 {
		t.Fatalf("generation = %d, want 1", st.Generation)
	}
	// Writer witness: retain_generations lands in the committed state
	// (independent review issue 1 — the writer half had no witness).
	if st.RetainGenerations != 2 {
		t.Fatalf("RetainGenerations = %d, want 2 (normalized default written to state)", st.RetainGenerations)
	}
	if _, ok := fake.get(st.DB.Snapshot); !ok {
		t.Fatalf("committed snapshot %q missing from bucket", st.DB.Snapshot)
	}
	// State was PUT after the snapshot (write-then-commit).
	snapIdx, stateIdx := -1, -1
	for i, k := range fake.putLog {
		if k == st.DB.Snapshot {
			snapIdx = i
		}
		if k == "ws/mirror-state.json" {
			stateIdx = i
		}
	}
	if snapIdx < 0 || stateIdx < snapIdx {
		t.Fatalf("commit order wrong (snapshot@%d state@%d): %v", snapIdx, stateIdx, fake.putLog)
	}

	// Local state recorded.
	local, err := LoadLocalState(localStatePath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if local.Generation != 1 || local.LastDBSHA256 == "" || local.LastRotationAt.IsZero() {
		t.Fatalf("local state = %+v", local)
	}
}

func TestEnable_AlreadyEnabled(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	dir := makeWorkspaceWithDB(t)
	m, _ := Open(dir, cfg, nil)
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	m2, _ := Open(dir, cfg, nil)
	if err := m2.Enable(context.Background()); err == nil {
		t.Fatal("second enable should error (already enabled)")
	}
}

func TestEnable_NoWikiDB(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	dir := t.TempDir() // no .sage/wiki.db — pre-compile workspace
	m, _ := Open(dir, cfg, nil)
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable without wiki.db: %v", err)
	}
	if _, ok := fake.get("ws/manifest.json"); !ok {
		t.Fatal("manifest.json missing")
	}
	if _, ok := fake.get("ws/mirror-state.json"); ok {
		t.Fatal("state should not exist before first ship pass")
	}
}

func TestEnable_StateIsZstdDecompressible(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	dir := makeWorkspaceWithDB(t)
	m, _ := Open(dir, cfg, nil)
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	sb, _ := fake.get("ws/mirror-state.json")
	st, _ := UnmarshalState(sb)
	raw, _ := fake.get(st.DB.Snapshot)
	dbBytes, err := zstdDecode(raw)
	if err != nil {
		t.Fatalf("snapshot not zstd: %v", err)
	}
	// SQLite magic header (16 bytes: "SQLite format 3" + NUL).
	if len(dbBytes) < 16 || string(dbBytes[:16]) != "SQLite format 3\x00" {
		t.Fatalf("decompressed snapshot lacks SQLite magic: %q", dbBytes[:15])
	}
}

// TestBootstrap_PreDBEnableThenShipPass (F-113 regression): enable on a
// db-less workspace (manifest only), create the db, ONE ship pass must
// commit generation 1 — this deadlocked via mutex re-acquire before the
// wrapper/inner split.
func TestBootstrap_PreDBEnableThenShipPass(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	dir := t.TempDir() // no .sage/wiki.db
	m, err := Open(dir, cfg, NewDiffChangeSource(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if _, ok := fake.get("ws/manifest.json"); !ok {
		t.Fatal("manifest.json missing")
	}
	// Create the db now (the pinned pre-compile flow).
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t (v TEXT); INSERT INTO t VALUES ('boot')"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// ONE pass must bootstrap + ship — before F-113 this spun 5s into
	// ErrShipLocked forever.
	res, err := m.shipPass(context.Background())
	if err != nil {
		t.Fatalf("ship pass after pre-db enable: %v", err)
	}
	_ = res
	sb, ok := fake.get("ws/mirror-state.json")
	if !ok {
		t.Fatal("generation 1 never committed after bootstrap pass")
	}
	st, err := UnmarshalState(sb)
	if err != nil {
		t.Fatal(err)
	}
	if st.Generation != 1 {
		t.Fatalf("generation = %d, want 1", st.Generation)
	}
	if _, ok := fake.get(st.DB.Snapshot); !ok {
		t.Fatal("gen-1 snapshot missing after bootstrap")
	}
}

// TestBootstrap_ForeignManifestRefused (F-118): a bucket whose manifest
// carries a different format_version must not receive a gen-1 commit.
func TestBootstrap_ForeignManifestRefused(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	fake.objects["ws/manifest.json"] = []byte(`{"format_version":99,"tool":"other","workspace":"x","created_at":"2026-08-01T00:00:00Z","encrypted":false}`)
	dir := makeWorkspaceWithDB(t)
	m, err := Open(dir, cfg, NewDiffChangeSource(dir))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.shipPass(context.Background()); err == nil {
		t.Fatal("foreign manifest must refuse bootstrap")
	}
}

// TestEnable_WritesConfiguredRetain (N4): the state carries the CONFIGURED
// value, not a constant (a hardcoded-2 writer fails this).
func TestEnable_WritesConfiguredRetain(t *testing.T) {
	fake := newFakeS3()
	_, cfg := setupFakeMirror(t, fake)
	cfg.RetainGenerations = 5
	dir := makeWorkspaceWithDB(t)
	m, err := Open(dir, cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := remoteStateFromFake(t, fake)
	if st.RetainGenerations != 5 {
		t.Fatalf("RetainGenerations = %d, want configured 5", st.RetainGenerations)
	}
}
