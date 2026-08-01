package main

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// hydrateCmdFixture builds a mirrored workspace via the enable+ship path.
func hydrateCmdFixture(t *testing.T) (*cmdFakeS3, string) {
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
		t.Fatal(err)
	}
	// Content + ship via the hook.
	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0o755)
	os.WriteFile(filepath.Join(dir, "wiki", "concepts", "Foo.md"), []byte("# Foo"), 0o644)
	db, _ := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	db.Exec("INSERT INTO t (v) VALUES ('cli-row')")
	db.Close()
	ageLocalRotation(t, dir, -2*time.Hour) // defeat the rotation debounce
	maybeShipAfterCommand()
	return fake, srv.URL
}

func resetHydrateFlags() {
	hydrateEndpoint = ""
	hydrateRegion = "auto"
	hydrateCredentialsFile = ""
	hydrateGeneration = 0
	hydrateAt = ""
	hydratePartial = false
	hydrateKeyFile = ""
}

func TestHydrateCmd_FullFlow(t *testing.T) {
	defer resetHydrateFlags()
	fake, endpoint := hydrateCmdFixture(t)
	_ = fake
	dst := filepath.Join(t.TempDir(), "restored")
	hydrateEndpoint = endpoint
	if err := hydrateCmd.RunE(hydrateCmd, []string{"s3://bk/ws", dst}); err != nil {
		t.Fatalf("hydrate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "wiki", "concepts", "Foo.md")); err != nil {
		t.Fatal("Foo.md not restored")
	}
	db, _ := sql.Open("sqlite", filepath.Join(dst, ".sage", "wiki.db"))
	defer db.Close()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM t WHERE v='cli-row'").Scan(&n)
	if n != 1 {
		t.Fatal("cli-row missing from restored db")
	}
}

func TestHydrateCmd_PartialMarkersAndResume(t *testing.T) {
	defer resetHydrateFlags()
	fake, endpoint := hydrateCmdFixture(t)
	dst := filepath.Join(t.TempDir(), "restored")
	hydrateEndpoint = endpoint
	hydratePartial = true

	// Break the markdown phase: corrupt a committed doc object (sha mismatch
	// aborts) AFTER db succeeded — progress must persist for resume.
	// Find the committed state and corrupt the Foo object.
	stBytes := fake.objects["ws/mirror-state.json"]
	var st struct {
		Objects map[string]struct {
			Key string `json:"key"`
		} `json:"objects"`
	}
	if err := json.Unmarshal(stBytes, &st); err != nil {
		t.Fatal(err)
	}
	var fooKey string
	for _, ref := range st.Objects {
		fooKey = ref.Key
	}
	fake.objects[fooKey] = []byte("CORRUPTED")

	if err := hydrateCmd.RunE(hydrateCmd, []string{"s3://bk/ws", dst}); err == nil {
		t.Fatal("corrupted doc should abort")
	}
	// Progress marker: db done, markdown not.
	pb, err := os.ReadFile(filepath.Join(dst, ".sage", "hydrate-state.json"))
	if err != nil {
		t.Fatalf("progress marker missing: %v", err)
	}
	if !containsStr(string(pb), "db") || containsStr(string(pb), "markdown") {
		t.Fatalf("marker phases wrong: %s", pb)
	}

	// Fix the object; resume completes without re-downloading the db phase.
	fake.objects[fooKey] = []byte("# Foo")
	// Recompute the sha reference issue: committed state pins the original
	// sha of "# Foo", which the restored bytes now match again.
	if err := hydrateCmd.RunE(hydrateCmd, []string{"s3://bk/ws", dst}); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "wiki", "concepts", "Foo.md")); err != nil {
		t.Fatal("resume did not restore Foo.md")
	}
}

func TestHydrateCmd_EncryptedNeedsKey(t *testing.T) {
	defer resetHydrateFlags()
	fake, endpoint := hydrateCmdFixture(t)
	// Mark the mirror encrypted in manifest.json.
	fake.objects["ws/manifest.json"] = []byte(`{"format_version":1,"tool":"sage-wiki","workspace":"x","created_at":"2026-08-01T00:00:00Z","encrypted":true}`)
	dst := filepath.Join(t.TempDir(), "restored")
	hydrateEndpoint = endpoint
	if err := hydrateCmd.RunE(hydrateCmd, []string{"s3://bk/ws", dst}); err == nil {
		t.Fatal("encrypted mirror without --key-file must fail loudly")
	}
}

func TestParseS3URL(t *testing.T) {
	b, p, err := parseS3URL("s3://bucket/some/prefix/")
	if err != nil || b != "bucket" || p != "some/prefix" {
		t.Fatalf("parseS3URL = %q, %q, %v", b, p, err)
	}
	if _, _, err := parseS3URL("https://bucket/x"); err == nil {
		t.Fatal("non-s3 scheme must fail")
	}
	if _, _, err := parseS3URL("s3:///x"); err == nil {
		t.Fatal("empty bucket must fail")
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
