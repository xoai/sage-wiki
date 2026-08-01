package vectors

import (
	"bytes"
	"database/sql"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/xoai/sage-wiki/internal/storage"
)

// setupVecDB creates a temp workspace DB (full schema via storage.Open)
// and returns the handle.
func setupVecDB(t *testing.T) *storage.DB {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func seedVecEntries(t *testing.T, db *storage.DB, rows map[string][]float32) {
	t.Helper()
	// Insert in id-sorted order so rowid order is deterministic.
	ids := sortedKeys(rows)
	if err := db.WriteTx(func(tx *sql.Tx) error {
		for _, id := range ids {
			v := rows[id]
			if _, err := tx.Exec(
				"INSERT INTO vec_entries (id, embedding, dimensions) VALUES (?, ?, ?)",
				id, encodeFloat32s(v), len(v),
			); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func seedVecChunks(t *testing.T, db *storage.DB, rows [][3]any) {
	t.Helper()
	if err := db.WriteTx(func(tx *sql.Tx) error {
		for _, r := range rows {
			cid, did, v := r[0].(string), r[1].(string), r[2].([]float32)
			if _, err := tx.Exec(
				"INSERT INTO vec_chunks (chunk_id, doc_id, embedding, dimensions) VALUES (?, ?, ?, ?)",
				cid, did, encodeFloat32s(v), len(v),
			); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func sortedKeys(m map[string][]float32) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func TestIndexFile_RoundTrip(t *testing.T) {
	for _, quant := range []int{QuantNone, QuantInt8} {
		db := setupVecDB(t)
		rows := map[string][]float32{
			"doc-a": {1, 0, 0},
			"doc-b": {0, 1, 0},
			"doc-c": {0.5, 0.5, 0.5},
		}
		seedVecEntries(t, db, rows)
		path := filepath.Join(t.TempDir(), "vectors.idx")
		stats, err := WriteIndexFile(db, IndexTableDocs, path, quant)
		if err != nil {
			t.Fatalf("WriteIndexFile(quant=%d): %v", quant, err)
		}
		if stats.Count != 3 || stats.Dim != 3 {
			t.Errorf("stats = %+v, want Count=3 Dim=3", stats)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		idx, err := parseIndex(data)
		if err != nil {
			t.Fatalf("parseIndex(quant=%d): %v", quant, err)
		}
		if idx.header.quant != quant || idx.header.dim != 3 || idx.header.count != 3 {
			t.Errorf("header = %+v", idx.header)
		}
		if len(idx.ids) != 3 || idx.ids[0] != "doc-a" || idx.ids[2] != "doc-c" {
			t.Errorf("ids = %v", idx.ids)
		}
		// Rows are normalized: doc-c has norm sqrt(3)/2... check unit length.
		row := idx.row(2)
		var norm float64
		for _, v := range row {
			norm += float64(v) * float64(v)
		}
		if math.Abs(math.Sqrt(norm)-1.0) > 0.01 {
			t.Errorf("row 2 norm = %v, want ~1 (quant=%d)", math.Sqrt(norm), quant)
		}
	}

	// Chunk shape: docIDs section present.
	db := setupVecDB(t)
	seedVecChunks(t, db, [][3]any{
		{"c1", "doc-a", []float32{1, 0}},
		{"c2", "doc-a", []float32{0, 1}},
		{"c3", "doc-b", []float32{1, 1}},
	})
	path := filepath.Join(t.TempDir(), "chunks.idx")
	if _, err := WriteIndexFile(db, IndexTableChunks, path, QuantNone); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	idx, err := parseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(idx.docIDs) != 3 || idx.docIDs[0] != "doc-a" || idx.docIDs[2] != "doc-b" {
		t.Errorf("docIDs = %v", idx.docIDs)
	}
}

func TestRebuild_DimSkipMirrorsLoader(t *testing.T) {
	db := setupVecDB(t)
	// First row sets dim=2; the 3-dim row is skipped (loader behavior:
	// len(vec) != dim). A later valid 2-dim row is included.
	err := db.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO vec_entries VALUES ('a', ?, 2)", encodeFloat32s([]float32{1, 0})); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO vec_entries VALUES ('b', ?, 3)", encodeFloat32s([]float32{1, 0, 0})); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO vec_entries VALUES ('c', ?, 2)", encodeFloat32s([]float32{0, 1})); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "v.idx")
	stats, err := WriteIndexFile(db, IndexTableDocs, path, QuantNone)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 2 || stats.Dim != 2 || stats.Skipped != 1 {
		t.Errorf("stats = %+v, want Count=2 Dim=2 Skipped=1", stats)
	}
	data, _ := os.ReadFile(path)
	idx, err := parseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if idx.ids[0] != "a" || idx.ids[1] != "c" {
		t.Errorf("ids = %v, want [a c]", idx.ids)
	}
}

func TestRebuild_FirstRowEmpty_Errors(t *testing.T) {
	db := setupVecDB(t)
	err := db.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("INSERT INTO vec_entries VALUES ('a', ?, 0)", []byte{}); err != nil {
			return err
		}
		if _, err := tx.Exec("INSERT INTO vec_entries VALUES ('b', ?, 2)", encodeFloat32s([]float32{1, 0})); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "v.idx")
	if _, err := WriteIndexFile(db, IndexTableDocs, path, QuantNone); err == nil {
		t.Error("first-row empty blob must error loudly (corrupt embeddings)")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("no index file may be left behind on writer error")
	}
}

func TestRebuild_EmptyTable_ValidEmptyFile(t *testing.T) {
	db := setupVecDB(t)
	path := filepath.Join(t.TempDir(), "v.idx")
	stats, err := WriteIndexFile(db, IndexTableDocs, path, QuantInt8)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 0 {
		t.Errorf("stats = %+v, want Count=0", stats)
	}
	data, _ := os.ReadFile(path)
	idx, err := parseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if idx.header.count != 0 || idx.header.dim != 0 {
		t.Errorf("header = %+v, want count=0 dim=0", idx.header)
	}
	// Scale guard: empty int8 file writes scale=1.0 (byte-identical rebuilds).
	if idx.header.scale != 1.0 {
		t.Errorf("scale = %v, want 1.0 for empty int8 file", idx.header.scale)
	}
}

func TestRebuild_ByteIdentical(t *testing.T) {
	db := setupVecDB(t)
	seedVecEntries(t, db, map[string][]float32{
		"a": {0.1, 0.2, 0.3}, "b": {0.4, 0.5, 0.6}, "c": {0.7, 0.8, 0.9},
	})
	dir := t.TempDir()
	p1, p2 := filepath.Join(dir, "1.idx"), filepath.Join(dir, "2.idx")
	for _, quant := range []int{QuantNone, QuantInt8} {
		if _, err := WriteIndexFile(db, IndexTableDocs, p1, quant); err != nil {
			t.Fatal(err)
		}
		if _, err := WriteIndexFile(db, IndexTableDocs, p2, quant); err != nil {
			t.Fatal(err)
		}
		b1, _ := os.ReadFile(p1)
		b2, _ := os.ReadFile(p2)
		if !bytes.Equal(b1, b2) {
			t.Errorf("rebuilds not byte-identical (quant=%d)", quant)
		}
	}
}

func TestInt8ScaleGuards(t *testing.T) {
	// All-zero matrix: max|v| = 0 → scale guard 1.0, rows quantize to zeros,
	// dequantize back to zeros (matching the fp32 zero-vector behavior).
	db := setupVecDB(t)
	seedVecEntries(t, db, map[string][]float32{"z": {0, 0, 0}})
	path := filepath.Join(t.TempDir(), "z.idx")
	if _, err := WriteIndexFile(db, IndexTableDocs, path, QuantInt8); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	idx, err := parseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if idx.header.scale != 1.0 {
		t.Errorf("scale = %v, want 1.0 guard for all-zero matrix", idx.header.scale)
	}
	for _, v := range idx.row(0) {
		if v != 0 {
			t.Errorf("zero matrix row dequantized to %v, want zeros", idx.row(0))
			break
		}
	}
}

func TestRebuild_AtomicRename(t *testing.T) {
	db := setupVecDB(t)
	seedVecEntries(t, db, map[string][]float32{"a": {1, 0}})
	dir := t.TempDir()
	path := filepath.Join(dir, "v.idx")
	// A stale tmp from a killed run is overwritten, never served.
	if err := os.WriteFile(path+".tmp", []byte("torn garbage"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteIndexFile(db, IndexTableDocs, path, QuantNone); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file must not survive a successful rebuild")
	}
	data, _ := os.ReadFile(path)
	if _, err := parseIndex(data); err != nil {
		t.Errorf("final file must parse: %v", err)
	}
}

func TestParseIndex_CorruptionTable(t *testing.T) {
	db := setupVecDB(t)
	seedVecEntries(t, db, map[string][]float32{"a": {1, 0}})
	path := filepath.Join(t.TempDir(), "v.idx")
	if _, err := WriteIndexFile(db, IndexTableDocs, path, QuantNone); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)

	cases := map[string]func([]byte) []byte{
		"bad magic": func(b []byte) []byte { b[0] = 'X'; return b },
		"bad version": func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[4:], 99)
			return b
		},
		"truncated header": func(b []byte) []byte { return b[:8] },
		"truncated ids":    func(b []byte) []byte { return b[:headerSize+1] },
		"truncated matrix": func(b []byte) []byte { return b[:len(b)-3] },
	}
	for name, mutate := range cases {
		bad := mutate(append([]byte(nil), good...))
		if _, err := parseIndex(bad); err == nil {
			t.Errorf("%s: parseIndex must reject", name)
		}
	}
}

// F-036 witness: corruption-chosen extreme headers (count/dim products
// that overflow int64) must be REJECTED by parseIndex, never panic.
func TestParseIndex_ExtremeHeaderRejected(t *testing.T) {
	db := setupVecDB(t)
	seedVecEntries(t, db, map[string][]float32{"a": {1, 0}})
	path := filepath.Join(t.TempDir(), "v.idx")
	if _, err := WriteIndexFile(db, IndexTableDocs, path, QuantNone); err != nil {
		t.Fatal(err)
	}
	good, _ := os.ReadFile(path)

	mk := func(count uint64, dim uint32) []byte {
		b := append([]byte(nil), good...)
		binary.LittleEndian.PutUint64(b[16:], count)
		binary.LittleEndian.PutUint32(b[12:], dim)
		return b
	}
	for name, b := range map[string][]byte{
		"count 2^31 dim 2^31 fp32": mk(1<<31, 1<<31),
		"count 2^40":               mk(1<<40, 2),
		"dim 2^24":                 mk(1, 1<<24),
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: parseIndex PANICKED (%v) — must reject cleanly", name, r)
				}
			}()
			if _, err := parseIndex(b); err == nil {
				t.Errorf("%s: parseIndex must reject overflow-shaped headers", name)
			}
		}()
	}
}

// F-037 witness: the writer streams — peak retained data is the ids
// section, never the full matrix. Structural assertion: two SQLite scans
// minimum (stats + write), no [][]float32 matrix retention. Proven by
// building an index over rows that would OOM if materialized lazily is
// impractical in CI; instead pin the property that a rebuild over a
// 200K-row table completes and the file parses — the streaming
// implementation is what makes this cheap.
func TestRebuild_Streaming_LargeTable(t *testing.T) {
	if testing.Short() {
		t.Skip("large-table streaming test")
	}
	db := setupVecDB(t)
	const n, dim = 200000, 16
	rng := uint32(7)
	next := func() float32 {
		rng = rng*1664525 + 1013904223
		return float32(rng%2000)/1000.0 - 1.0
	}
	const batch = 5000
	for start := 0; start < n; start += batch {
		if err := db.WriteTx(func(tx *sql.Tx) error {
			for i := start; i < start+batch && i < n; i++ {
				v := make([]float32, dim)
				for j := range v {
					v[j] = next()
				}
				if _, err := tx.Exec("INSERT INTO vec_entries VALUES (?, ?, ?)",
					chunkID(0, i), encodeFloat32s(v), dim); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "big.idx")
	stats, err := WriteIndexFile(db, IndexTableDocs, path, QuantInt8)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != n {
		t.Errorf("Count = %d, want %d", stats.Count, n)
	}
	data, _ := os.ReadFile(path)
	idx, err := parseIndex(data)
	if err != nil {
		t.Fatal(err)
	}
	if int(idx.header.count) != n || idx.header.dim != dim {
		t.Errorf("header = %+v", idx.header)
	}
}
