package main

import (
	"database/sql"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/internal/wiki"
	"github.com/xoai/sage-wiki/pkg/engine"
)

// indexTestCmd builds a cobra command carrying the same flags as
// rebuildVectorsCmd so tests can set them without the full root.
func indexTestCmd() *cobra.Command {
	cmd := &cobra.Command{}
	cmd.Flags().String("quantize", "", "")
	cmd.Flags().Bool("upgrade", false, "")
	return cmd
}

// seedVecRows writes doc+chunk vectors straight into the workspace DB.
func seedVecRows(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	enc := func(v ...float32) []byte {
		b := make([]byte, len(v)*4)
		for i, f := range v {
			binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
		}
		return b
	}
	for _, row := range []struct {
		id string
		v  []float32
	}{
		{"a", []float32{1, 0}},
		{"b", []float32{0, 1}},
	} {
		if _, err := db.Exec("INSERT INTO vec_entries (id, embedding, dimensions) VALUES (?, ?, ?)",
			row.id, enc(row.v...), len(row.v)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("INSERT INTO vec_chunks (chunk_id, doc_id, embedding, dimensions) VALUES (?, ?, ?, ?)",
		"c1", "a", enc(1, 0), 2); err != nil {
		t.Fatal(err)
	}
}

func initIndexWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatalf("init: %v", err)
	}
	seedVecRows(t, dir)
	return dir
}

func runIndexCmd(t *testing.T, dir string, setFlags func(cmd *cobra.Command)) error {
	t.Helper()
	oldProject, oldConfig := projectDir, configPath
	projectDir, configPath = dir, ""
	t.Cleanup(func() { projectDir, configPath = oldProject, oldConfig })
	cmd := indexTestCmd()
	if setFlags != nil {
		setFlags(cmd)
	}
	return runIndexRebuildVectors(cmd, nil)
}

func TestIndexRebuildVectors_EndToEnd(t *testing.T) {
	dir := initIndexWorkspace(t)
	if err := runIndexCmd(t, dir, nil); err != nil {
		t.Fatalf("rebuild-vectors: %v", err)
	}
	for _, name := range []string{"vectors.idx", "vectors-chunks.idx"} {
		data, err := os.ReadFile(filepath.Join(dir, ".sage", name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(data) < 32 || string(data[:4]) != "SWVI" {
			t.Errorf("%s does not look like an index file (%d bytes)", name, len(data))
		}
	}
	// Search through the mmap backend sees the same results as memory.
	db, err := storage.Open(filepath.Join(dir, ".sage", "wiki.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sageDir := filepath.Join(dir, ".sage")
	mm := vectors.NewStore(db,
		vectors.WithVectorBackend("mmap"), vectors.WithIndexDir(sageDir))
	res, err := mm.Search([]float32{1, 0}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].ID != "a" {
		t.Errorf("post-rebuild mmap search = %+v, want a first of 2", res)
	}
}

func TestIndexRebuildVectors_LockedWorkspace(t *testing.T) {
	dir := initIndexWorkspace(t)
	w, err := engine.Open(nil, dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer w.Close()
	err = runIndexCmd(t, dir, nil)
	if err == nil || !strings.Contains(err.Error(), "locked") {
		t.Errorf("err = %v, want lock contention", err)
	}
}

func TestIndexRebuildVectors_QuantizeFlag(t *testing.T) {
	dir := initIndexWorkspace(t)
	err := runIndexCmd(t, dir, func(cmd *cobra.Command) {
		_ = cmd.Flags().Set("quantize", "int8")
	})
	if err != nil {
		t.Fatalf("rebuild-vectors --quantize int8: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".sage", "vectors.idx"))
	if err != nil {
		t.Fatal(err)
	}
	if data[8] != 1 {
		t.Errorf("quantization byte = %d, want 1 (int8)", data[8])
	}
}

func TestIndexRebuildVectors_BadQuantize(t *testing.T) {
	dir := initIndexWorkspace(t)
	err := runIndexCmd(t, dir, func(cmd *cobra.Command) {
		_ = cmd.Flags().Set("quantize", "fp16")
	})
	if err == nil {
		t.Error("invalid --quantize value must error")
	}
}

func TestIndexRebuildVectors_PreFormat(t *testing.T) {
	// A v0.2.x workspace (no format_version) must be refused without
	// --upgrade, exactly like the other mutating commands.
	dir := t.TempDir()
	if err := wiki.InitGreenfield(dir, "test", "gemini-2.5-flash"); err != nil {
		t.Fatal(err)
	}
	// Strip format_version to simulate pre-format.
	mfPath := filepath.Join(dir, ".manifest.json")
	raw, err := os.ReadFile(mfPath)
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.Replace(string(raw), strings.TrimSpace(extractLine(string(raw), "format_version")), "", 1)
	if err := os.WriteFile(mfPath, []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	seedVecRows(t, dir)
	if err := runIndexCmd(t, dir, nil); err == nil {
		t.Error("pre-format workspace without --upgrade must be refused")
	}
}

func TestIndexRebuildVectors_Flags(t *testing.T) {
	// --project/--config resolve exactly like every other command: run
	// against a non-default project dir (already the harness shape) AND an
	// explicit --config file relocated from the default path.
	dir := initIndexWorkspace(t)
	relocated := filepath.Join(t.TempDir(), "elsewhere.yaml")
	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relocated, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	oldProject, oldConfig := projectDir, configPath
	projectDir, configPath = dir, relocated
	t.Cleanup(func() { projectDir, configPath = oldProject, oldConfig })
	if err := runIndexRebuildVectors(indexTestCmd(), nil); err != nil {
		t.Fatalf("rebuild-vectors with --config: %v", err)
	}
}

func extractLine(s, key string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, key) {
			return l
		}
	}
	return ""
}
