package mirror

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Crash-injection loop (spec AC-2): kill -9 mid-ship ×N (env
// SAGE_MIRROR_CRASH_LOOPS, default 5; CI sets 100). The helper is a
// re-exec'd test process shipping a workload against THIS process's fake
// S3; the parent kills it at randomized points (small min_rotation_interval
// forces frequent rotations, so kills land in rotation windows too), then
// verify must report a valid restorable state every time.

func crashLoops() int {
	if v := os.Getenv("SAGE_MIRROR_CRASH_LOOPS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}

// TestCrashHelperProcess is the re-exec target — never runs directly.
func TestCrashHelperProcess(t *testing.T) {
	if os.Getenv("SAGE_MIRROR_CRASH_HELPER") != "1" {
		t.Skip("helper process only")
	}
	endpoint := os.Getenv("CRASH_ENDPOINT")
	bucket := os.Getenv("CRASH_BUCKET")
	dir := os.Getenv("CRASH_WORKSPACE")
	t.Setenv("MIRROR_TEST_AK", "ak")
	t.Setenv("MIRROR_TEST_SK", "sk")
	cfg := Config{
		Endpoint: endpoint, Bucket: bucket, Prefix: "ws/", Region: "auto",
		AccessKeyEnv: "MIRROR_TEST_AK", SecretKeyEnv: "MIRROR_TEST_SK",
		MinRotationInterval: time.Millisecond,
		ShipInterval:        time.Millisecond,
	}
	m, err := Open(dir, cfg, NewDiffChangeSource(dir))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Enable(context.Background()); err != nil && err != ErrAlreadyEnabled {
		t.Fatal(err)
	}
	for i := 0; ; i++ {
		dbWriteCrash(dir, i)
		if _, err := m.shipPass(context.Background()); err != nil {
			// Best-effort loop: keep shipping (the kill is the point).
			time.Sleep(time.Millisecond)
		}
	}
}

func dbWriteCrash(dir string, i int) {
	// Real db writes (F-080): open + insert + close folds the WAL every
	// iteration, so with the 1ms debounce every pass forces a rotation —
	// kills land inside rotation windows by construction. Markdown writes
	// alone never rotate (the reviewer's witness).
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		return
	}
	db.Exec("INSERT INTO t (v) VALUES (?)", fmt.Sprintf("crash-%d", i))
	db.Close()
	writeWSHelper(dir, fmt.Sprintf("wiki/crash/%d.md", i), fmt.Sprintf("crash-%d", i))
}

func writeWSHelper(dir, rel, content string) {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(content), 0o644)
}

func TestCrashKillLoop(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skipf("ship-mutex uses lockfile fallback on %s (24h stale timeout); crash-recovery requires flock", runtime.GOOS)
	}
	loops := crashLoops()
	testBin := os.Args[0]
	for i := 0; i < loops; i++ {
		fake := newFakeS3()
		_, cfg := setupFakeMirror(t, fake)
		dir := makeWorkspaceWithDB(t)

		env := append(os.Environ(),
			"SAGE_MIRROR_CRASH_HELPER=1",
			"CRASH_ENDPOINT="+cfg.Endpoint,
			"CRASH_BUCKET="+cfg.Bucket,
			"CRASH_WORKSPACE="+dir,
		)
		cmd := exec.Command(testBin, "-test.run", "TestCrashHelperProcess", "-test.count=1", "-test.v")
		cmd.Env = env
		var helperLog strings.Builder
		cmd.Stdout = &helperLog
		cmd.Stderr = &helperLog
		if err := cmd.Start(); err != nil {
			t.Fatalf("iter %d: start helper: %v", i, err)
		}
		// Wait for the helper's first commit (mirror-state appears) before
		// killing — otherwise the kill can land pre-enable and there is no
		// committed state to verify (that case is a no-op, not a crash
		// window).
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, ok := fake.get(StateKey("ws/")); ok {
				break
			}
			if time.Now().After(deadline) {
				cmd.Process.Kill()
				t.Fatalf("iter %d: helper never committed initial state", i)
			}
			time.Sleep(5 * time.Millisecond)
		}
		// Randomized kill point (20–120ms) — lands mid-ship; frequent
		// rotations (1ms debounce) put some kills inside rotation windows.
		time.Sleep(time.Duration(20+i*17%100) * time.Millisecond)
		if err := cmd.Process.Kill(); err != nil {
			t.Fatalf("iter %d: kill: %v", i, err)
		}
		_ = cmd.Wait()

		// Recovery pass FIRST (F-080): the crash-window repairs run here,
		// then verify must report a valid restorable state.
		m, err := Open(dir, cfg, NewDiffChangeSource(dir))
		if err != nil {
			t.Fatalf("iter %d: open: %v", i, err)
		}
		if _, err := m.shipPass(context.Background()); err != nil {
			t.Fatalf("iter %d: recovery pass: %v", i, err)
		}
		rep, err := m.Verify(context.Background())
		if err != nil {
			t.Fatalf("iter %d: verify err: %v", i, err)
		}
		if !rep.Valid {
			stDump, _ := m.remoteState(context.Background())
			t.Logf("iter %d remote state: gen=%d snapshot=%s wal=%d", i, stDump.Generation, stDump.DB.Snapshot, len(stDump.DB.WAL))
			var objList []string
			for k := range fake.objects {
				objList = append(objList, k)
			}
			sort.Strings(objList)
			t.Logf("iter %d objects: %v", i, objList)
			t.Fatalf("iter %d: mirror invalid after kill -9: %v", i, rep.Violations)
		}
	}
}
