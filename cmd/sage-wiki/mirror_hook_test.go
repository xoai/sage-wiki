package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	_ "modernc.org/sqlite"
)

// enableMirrorInWorkspace writes config with mirror.enabled: true.
func enableMirrorInWorkspace(t *testing.T, dir string) {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.yaml")
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "enabled: true") {
		t.Fatalf("mirror not enabled in config:\n%s", b)
	}
}

type hookFixture struct {
	dir  string
	fake *cmdFakeS3
}

// ageLocalRotation rewrites mirror-local.json's last_rotation_at by delta
// (debounce control for tests).
func ageLocalRotation(t *testing.T, dir string, delta time.Duration) {
	t.Helper()
	path := filepath.Join(dir, ".sage", "mirror-local.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var st map[string]any
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	st["last_rotation_at"] = time.Now().Add(delta).UTC().Format(time.RFC3339)
	out, _ := json.Marshal(st)
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	fake := newCmdFakeS3()
	srv := fake.server()
	t.Cleanup(srv.Close)
	dir := writeMirrorWorkspace(t, srv.URL)
	t.Setenv("AWS_ACCESS_KEY_ID", "ak")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "sk")
	old := projectDir
	projectDir = dir
	t.Cleanup(func() { projectDir = old })
	if err := mirrorEnableCmd.RunE(mirrorEnableCmd, nil); err != nil {
		t.Fatalf("enable: %v", err)
	}
	enableMirrorInWorkspace(t, dir)
	return &hookFixture{dir: dir, fake: fake}
}

func (f *hookFixture) remoteUpdatedAt(t *testing.T) time.Time {
	t.Helper()
	sb, ok := f.fake.objects["ws/mirror-state.json"]
	if !ok {
		t.Fatal("no remote state")
	}
	var st struct {
		UpdatedAt time.Time `json:"updated_at"`
	}
	if err := json.Unmarshal(sb, &st); err != nil {
		t.Fatal(err)
	}
	return st.UpdatedAt
}

func TestMirrorHook_ShipsAfterMutation(t *testing.T) {
	f := newHookFixture(t)
	before := f.remoteUpdatedAt(t)
	time.Sleep(1100 * time.Millisecond) // RFC3339 second granularity
	if err := os.MkdirAll(filepath.Join(f.dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "wiki", "concepts", "Foo.md"), []byte("# Foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	maybeShipAfterCommand()
	if !f.remoteUpdatedAt(t).After(before) {
		t.Fatal("mirror-state updated_at did not advance after mutating command")
	}
}

func TestMirrorHook_DBOnlyMutation_Restorable(t *testing.T) {
	f := newHookFixture(t)
	// Age the local rotation timestamp past min_rotation_interval (the common
	// case: a command runs >60s after enable) so the fold-forced rotation
	// fires this pass rather than debouncing.
	ageLocalRotation(t, f.dir, -2*time.Hour)
	// A db-only mutation with the WAL folded by close (the normal CLI case).
	db, err := sql.Open("sqlite", filepath.Join(f.dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO t (v) VALUES ('cli-row')"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	time.Sleep(1100 * time.Millisecond)
	maybeShipAfterCommand()
	// Forced rotation committed the db (gen 2 snapshot or wal segments).
	found := false
	for k := range f.fake.objects {
		if strings.HasPrefix(k, "ws/db/generation-2/") || strings.Contains(k, "/wal/") {
			found = true
		}
	}
	if !found {
		t.Fatal("db-only mutation never shipped (no rotation, no segments)")
	}
}

func TestMirrorHook_UnreachableBucket_ExitUnchanged(t *testing.T) {
	f := newHookFixture(t)
	// Point the config at a dead endpoint (cfg.Save uses 4-space indent).
	cfgPath := filepath.Join(f.dir, "config.yaml")
	b, _ := os.ReadFile(cfgPath)
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		if strings.Contains(l, "endpoint:") {
			lines[i] = "    endpoint: \"http://127.0.0.1:1\""
		}
	}
	os.WriteFile(cfgPath, []byte(strings.Join(lines, "\n")), 0o644)
	before := f.remoteUpdatedAt(t)
	if err := os.MkdirAll(filepath.Join(f.dir, "wiki", "concepts"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(f.dir, "wiki", "concepts", "Foo.md"), []byte("# Foo"), 0o644)
	// Must warn and return — never panic/error back to the command.
	maybeShipAfterCommand()
	if f.remoteUpdatedAt(t) != before {
		t.Fatal("remote state changed despite unreachable bucket")
	}
}

func TestMirrorHook_DisabledNoOp(t *testing.T) {
	fake := newCmdFakeS3()
	srv := fake.server()
	defer srv.Close()
	dir := writeMirrorWorkspace(t, srv.URL) // enabled: false (not enabled)
	old := projectDir
	projectDir = dir
	defer func() { projectDir = old }()
	requestsBefore := len(fake.objects)
	maybeShipAfterCommand()
	if len(fake.objects) != requestsBefore {
		t.Fatal("disabled mirror performed network I/O")
	}
}

// TestMirrorHook_MutationThenErrorCommand (AC-9(c-i)): a command that
// mutates THEN returns an error still ships its mutation — the wrapper
// fires on the error path by construction.
func TestMirrorHook_MutationThenErrorCommand(t *testing.T) {
	f := newHookFixture(t)
	// Substitute rootCmd with a command that mutates then errors.
	oldRoot := rootCmd
	rootCmd = &cobra.Command{
		Use: "fail-after-mutate",
		RunE: func(cmd *cobra.Command, args []string) error {
			os.MkdirAll(filepath.Join(f.dir, "wiki", "concepts"), 0o755)
			os.WriteFile(filepath.Join(f.dir, "wiki", "concepts", "Err.md"), []byte("# Err"), 0o644)
			return fmt.Errorf("deliberate failure")
		},
	}
	defer func() { rootCmd = oldRoot }()

	err := executeWithShipPass()
	if err == nil {
		t.Fatal("command error must propagate")
	}
	// The mutation shipped despite the error.
	found := false
	for k := range f.fake.objects {
		if strings.HasPrefix(k, "ws/objects/docs/") {
			found = true
		}
	}
	if !found {
		t.Fatal("mutation-then-error: change never shipped")
	}
}
