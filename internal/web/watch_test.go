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

	// Arm EVENT-DRIVEN, never by a fixed sleep (shadow-shard flake, first
	// sighting 2026-10-02): under -race load the watcher goroutine can take
	// arbitrarily long to reach addRecursiveWatch, and any write that lands
	// BEFORE the walk completes is swallowed silently — the initial walk
	// emits no events. So REWRITE an arming sentinel on a ticker until a
	// broadcast arrives: the first rewrite may be swallowed by the walk,
	// but once the watcher is armed every rewrite is a Write event. The
	// broadcast proves armed-and-processing; only then does the real probe
	// (new subdir) race a genuinely-live watcher. The budget bounds only
	// "the watcher is not working at all", so it is generous.
	sentinel := filepath.Join(outDir, "arming-sentinel.md")
	armDeadline := time.Now().Add(10 * time.Second)
	armed := false
	for !armed {
		if time.Now().After(armDeadline) {
			t.Fatal("watcher never broadcast the arming sentinel — not armed/processing")
		}
		os.WriteFile(sentinel, []byte(time.Now().Format(time.RFC3339Nano)), 0644)
		select {
		case <-ch:
			armed = true
		case <-time.After(500 * time.Millisecond):
			// Rewrite and probe again. The gap must EXCEED the watcher's
			// 300ms trailing debounce — rewrites faster than the debounce
			// reset the timer forever and the broadcast never fires.
		}
	}
	// Drain any duplicate broadcast the sentinel's debounced rewrites queued.
	select {
	case <-ch:
	default:
	}

	os.MkdirAll(filepath.Join(outDir, "concepts"), 0755)
	os.WriteFile(filepath.Join(outDir, "concepts", "new.md"), []byte("x"), 0644)

	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("no broadcast for file in newly-created subdir")
	}
}

// TestWatchFsnotify_IgnoresChmod: metadata-only events don't broadcast —
// the op filter keeps the fallback poll's size+mtime semantics (Gate-8).
func TestWatchFsnotify_IgnoresChmod(t *testing.T) {
	srv := newWatchTestServer(t)
	outDir := filepath.Join(srv.projectDir, srv.cfg.Output)
	os.MkdirAll(outDir, 0755)
	target := filepath.Join(outDir, "target.md")
	os.WriteFile(target, []byte("x"), 0644)

	ch := srv.registerTestClient()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.watchFsnotify(ctx, outDir)

	// Let the watcher arm AND any arming events flush past the debounce.
	time.Sleep(600 * time.Millisecond)
	// Drain any broadcast from the initial state.
	select {
	case <-ch:
	default:
	}

	os.Chmod(target, 0600)
	select {
	case msg := <-ch:
		t.Errorf("chmod triggered a broadcast (%q) — metadata-only events must be ignored", msg)
	case <-time.After(1 * time.Second):
		// correct: no broadcast
	}

	// And a real write still broadcasts (the filter didn't over-suppress).
	os.WriteFile(target, []byte("y"), 0644)
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("content write did not broadcast after chmod suppression")
	}
}
