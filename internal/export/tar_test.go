package export

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildTree makes a small workspace-shaped tree; file mtimes are set
// DIFFERENTLY per call so mtime-dependence shows up as byte differences.
func buildTree(t *testing.T, mtime time.Time) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "wiki", "concepts"), 0755)
	os.MkdirAll(filepath.Join(dir, ".sage"), 0755)
	for _, f := range []string{"wiki/concepts/a.md", "wiki/concepts/b.md", "config.yaml", ".sage/usage.jsonl"} {
		p := filepath.Join(dir, f)
		if err := os.WriteFile(p, []byte("content of "+f), 0644); err != nil {
			t.Fatal(err)
		}
	}
	// The lock artifact must be excluded.
	if err := os.WriteFile(filepath.Join(dir, ".sage", "engine.lock"), []byte("12345"), 0644); err != nil {
		t.Fatal(err)
	}
	// A symlink must be skipped (empty link targets break tar readers).
	if err := os.Symlink(filepath.Join(dir, "wiki"), filepath.Join(dir, "wiki-link")); err != nil {
		t.Logf("symlink not creatable here (%v) — exclusion untested on this platform", err)
	}
	// Diverge mtimes on purpose.
	os.Chtimes(filepath.Join(dir, "wiki", "concepts", "a.md"), mtime, mtime)
	return dir
}

func tarEntries(t *testing.T, b []byte) (names []string, hdrs []*tar.Header) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, hdr.Name)
		hdrs = append(hdrs, hdr)
	}
	return names, hdrs
}

func TestTar_ByteIdenticalAcrossRunsAndMtimes(t *testing.T) {
	dirA := buildTree(t, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	dirB := buildTree(t, time.Date(2026, 6, 6, 6, 6, 6, 0, time.UTC))

	var bufA, bufB bytes.Buffer
	if err := Tar(context.Background(), dirA, &bufA); err != nil {
		t.Fatal(err)
	}
	if err := Tar(context.Background(), dirB, &bufB); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bufA.Bytes(), bufB.Bytes()) {
		t.Fatal("two exports of identical trees (different mtimes) differ")
	}
}

func TestTar_HeadersNormalizedAndExclusions(t *testing.T) {
	dir := buildTree(t, time.Now())
	var buf bytes.Buffer
	if err := Tar(context.Background(), dir, &buf); err != nil {
		t.Fatal(err)
	}
	names, hdrs := tarEntries(t, buf.Bytes())

	for _, n := range names {
		if n == ".sage/engine.lock" {
			t.Error("engine.lock present in export — must be excluded")
		}
		if n == "wiki-link" {
			t.Error("symlink present in export — must be skipped")
		}
	}
	for _, h := range hdrs {
		if h.ModTime.Unix() != 0 {
			t.Errorf("%s: ModTime = %v, want unix 0 (no SOURCE_DATE_EPOCH set)", h.Name, h.ModTime)
		}
		if h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: ids not zeroed: uid=%d gid=%d uname=%q gname=%q", h.Name, h.Uid, h.Gid, h.Uname, h.Gname)
		}
	}
	// Lexical order (WalkDir guarantee pinned as a contract).
	for i := 1; i < len(names); i++ {
		if names[i-1] > names[i] {
			t.Errorf("entry order not lexical: %q > %q", names[i-1], names[i])
		}
	}
}

func TestTar_ModTimeFromSourceDateEpoch(t *testing.T) {
	t.Setenv("SOURCE_DATE_EPOCH", "1700000000")
	dir := buildTree(t, time.Now())
	var buf bytes.Buffer
	if err := Tar(context.Background(), dir, &buf); err != nil {
		t.Fatal(err)
	}
	_, hdrs := tarEntries(t, buf.Bytes())
	want := time.Unix(1700000000, 0).UTC()
	for _, h := range hdrs {
		if !h.ModTime.Equal(want) {
			t.Errorf("%s: ModTime = %v, want %v (SOURCE_DATE_EPOCH)", h.Name, h.ModTime, want)
		}
	}
}

func TestTar_ContextCancel(t *testing.T) {
	dir := buildTree(t, time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var buf bytes.Buffer
	if err := Tar(ctx, dir, &buf); err == nil {
		t.Error("cancelled ctx: want error, got nil")
	}
}
