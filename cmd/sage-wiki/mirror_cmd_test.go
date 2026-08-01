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
