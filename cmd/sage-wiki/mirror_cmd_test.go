package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

type cmdFakeS3 struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newCmdFakeS3() *cmdFakeS3 { return &cmdFakeS3{objects: map[string][]byte{}} }

func (f *cmdFakeS3) server() *httptest.Server {
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
				var b strings.Builder
				b.WriteString(`<?xml version="1.0"?><ListBucketResult><IsTruncated>false</IsTruncated>`)
				for k := range f.objects {
					if strings.HasPrefix(k, prefix) {
						b.WriteString("<Contents><Key>" + k + "</Key></Contents>")
					}
				}
				b.WriteString(`</ListBucketResult>`)
				w.Write([]byte(b.String()))
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
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

func (f *cmdFakeS3) has(key string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[key]
	return ok
}

func writeMirrorWorkspace(t *testing.T, endpoint string) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".sage"), 0o755)
	os.MkdirAll(filepath.Join(dir, "raw"), 0o755)
	cfg := "project: p\noutput: wiki\nsources:\n  - path: raw\nmirror:\n  endpoint: \"" + endpoint + "\"\n  bucket: bk\n  prefix: ws/\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL; CREATE TABLE t (v TEXT)"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return dir
}

func TestMirrorEnableCmd(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL)

	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")

	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatalf("runMirrorEnable: %v", err)
	}
	if !fake.has("ws/manifest.json") {
		t.Fatal("manifest.json not PUT")
	}
	if !fake.has("ws/mirror-state.json") {
		t.Fatal("mirror-state.json not committed")
	}
	cfgBytes, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(cfgBytes), "enabled: true") {
		t.Fatalf("config.yaml not updated:\n%s", cfgBytes)
	}
}

func TestMirrorEnableCmd_BadCreds_ConfigUntouched(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL)
	// No env creds set.
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil)
	if err == nil {
		t.Fatal("expected credential error")
	}
	cfgBytes, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if strings.Contains(string(cfgBytes), "enabled: true") {
		t.Fatal("config.yaml written despite failed enable")
	}
}

func TestMirrorStatusCmd_JSON(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()

	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := mirrorStatusCmd.RunE(mirrorStatusCmd, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
}

func TestMirrorVerifyCmd_JSON(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	oldFmt := outputFormat
	outputFormat = "json"
	defer func() { outputFormat = oldFmt }()

	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := mirrorVerifyCmd.RunE(mirrorVerifyCmd, nil); err != nil {
		t.Fatalf("verify: %v", err)
	}
	mirrorVerifyFast = true
	defer func() { mirrorVerifyFast = false }()
	if err := mirrorVerifyCmd.RunE(mirrorVerifyCmd, nil); err != nil {
		t.Fatalf("verify --fast: %v", err)
	}
}

func TestMirrorEnableCmd_AlreadyEnabled(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatal(err)
	}
	// Second run: clean message, exit nil.
	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatalf("second enable should be a no-op message: %v", err)
	}
}

func TestMirrorSnapshotCmd(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()

	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := mirrorSnapshotCmd.RunE(mirrorSnapshotCmd, nil); err != nil {
		t.Fatalf("mirror snapshot: %v", err)
	}
	// Gen-2 snapshot committed.
	found := false
	for k := range fake.objects {
		if strings.HasPrefix(k, "ws/db/generation-2/snapshot.db.zst") {
			found = true
		}
	}
	if !found {
		t.Fatal("gen-2 snapshot not committed by mirror snapshot")
	}
	// meta.json for gen 1 written.
	if !fake.has("ws/db/generation-1/meta.json") {
		t.Fatal("gen-1 meta.json missing after forced rotation")
	}
}

func TestMirrorStatusCmd_PendingChangesReal(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatal(err)
	}
	// Unshipped change → status must report it through the CLI path (F-080).
	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755)
	os.WriteFile(filepath.Join(dir, "wiki", "concepts", "New.md"), []byte("# New"), 0o644)
	if err := mirrorStatusCmd.RunE(mirrorStatusCmd, nil); err != nil {
		t.Fatalf("status: %v", err)
	}
}

func TestMirrorStatusCmd_DisabledNoCreds(t *testing.T) {
	dir := writeMirrorWorkspace(t, "http://127.0.0.1:1")
	// No env creds; mirror NOT enabled → clean disabled report, exit nil (F-081).
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	if err := mirrorStatusCmd.RunE(mirrorStatusCmd, nil); err != nil {
		t.Fatalf("disabled status should not require creds: %v", err)
	}
}
