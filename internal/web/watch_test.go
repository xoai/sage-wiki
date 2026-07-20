package web

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/wiki"
)

func newWatchTestServer(t *testing.T) *WebServer {
	t.Helper()
	dir := t.TempDir()
	wiki.InitGreenfield(dir, "test", "gemini-2.5-flash")
	srv, err := NewWebServer(dir)
	if err != nil {
		t.Fatalf("NewWebServer: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func (s *WebServer) registerTestClient() chan string {
	ch := make(chan string, 4)
	s.wsMu.Lock()
	s.wsClients[ch] = true
	s.wsMu.Unlock()
	return ch
}

// TestWatchFsnotify_BroadcastsOnChange: the event-driven path fires a
// debounced BroadcastReload when a file appears in the output dir.
func TestWatchFsnotify_BroadcastsOnChange(t *testing.T) {
	srv := newWatchTestServer(t)
	outDir := filepath.Join(srv.projectDir, srv.cfg.Output)
	os.MkdirAll(outDir, 0755)

	ch := srv.registerTestClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.watchFsnotify(ctx, outDir)

	// Let the watcher arm.
	time.Sleep(200 * time.Millisecond)
	os.WriteFile(filepath.Join(outDir, "trigger.md"), []byte("change"), 0644)

	select {
	case msg := <-ch:
		if msg != "reload" {
			t.Errorf("message = %q, want reload", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no broadcast within 3s of file creation")
	}
}

// TestWatchFsnotify_FallsBackToPoll: when the recursive add fails (output
// dir does not exist), the watcher falls back to the dirSnapshot poll,
// which still broadcasts once the dir appears (poll interval injected).
func TestWatchFsnotify_FallsBackToPoll(t *testing.T) {
	srv := newWatchTestServer(t)
	srv.pollInterval = 50 * time.Millisecond
	outDir := filepath.Join(srv.projectDir, srv.cfg.Output)
	os.RemoveAll(outDir) // force the addRecursive failure → fallback

	ch := srv.registerTestClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.watchFsnotify(ctx, outDir)

	// Let the fallback engage, then create the dir + a file.
	time.Sleep(100 * time.Millisecond)
	os.MkdirAll(outDir, 0755)
	os.WriteFile(filepath.Join(outDir, "trigger.md"), []byte("change"), 0644)

	select {
	case msg := <-ch:
		if msg != "reload" {
			t.Errorf("message = %q, want reload", msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poll fallback did not broadcast within 3s")
	}
}

// TestWatchFsnotify_NewSubdirWatched: a file created inside a NEW
// subdirectory (created after the watch started) is also detected —
// subdirs are added as they appear.
func TestWatchFsnotify_NewSubdirWatched(t *testing.T) {
	srv := newWatchTestServer(t)
	outDir := filepath.Join(srv.projectDir, srv.cfg.Output)
	os.MkdirAll(outDir, 0755)

	ch := srv.registerTestClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.watchFsnotify(ctx, outDir)

	time.Sleep(200 * time.Millisecond)
	os.MkdirAll(filepath.Join(outDir, "concepts"), 0755)
	os.WriteFile(filepath.Join(outDir, "concepts", "new.md"), []byte("x"), 0644)

	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("no broadcast for file in newly-created subdir")
	}
}
