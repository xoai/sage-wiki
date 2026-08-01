package vectors

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

// IndexTable selects the source vec table for a rebuild.
type IndexTable int

const (
	// IndexTableDocs is vec_entries (id, embedding).
	IndexTableDocs IndexTable = iota
	// IndexTableChunks is vec_chunks (chunk_id, doc_id, embedding).
	IndexTableChunks
)

// IndexStats reports what a rebuild wrote.
type IndexStats struct {
	Count        int
	Dim          int
	Skipped      int // dimension-mismatch rows (mirrors the loader's skip rule)
	Bytes        int64
	Quantization int
}

// tableSpec parameterizes the two rebuild shapes. Column names are compile-
// time constants — no user input reaches the query text.
type tableSpec struct {
	kind      int
	selectSQL string
	probeSQL  string
}

var docTable = tableSpec{
	kind:      tableKindDoc,
	selectSQL: "SELECT id, embedding FROM vec_entries ORDER BY rowid",
	probeSQL:  "SELECT COUNT(*) FROM vec_entries WHERE LENGTH(embedding)/4 = ?",
}

var chunkTable = tableSpec{
	kind:      tableKindChunk,
	selectSQL: "SELECT chunk_id, doc_id, embedding FROM vec_chunks ORDER BY rowid",
	probeSQL:  "SELECT COUNT(*) FROM vec_chunks WHERE LENGTH(embedding)/4 = ?",
}

func specFor(t IndexTable) tableSpec {
	if t == IndexTableChunks {
		return chunkTable
	}
	return docTable
}

// WriteIndexFile regenerates the on-disk index for one vec table from the
// persisted SQLite embeddings. The write is atomic: a sibling .tmp file is
// fully written, fsynced, then renamed over the target — readers see the
// old file or the new one, never a torn write.
//
// The writer STREAMS (spec §2 — no full materialization): a stats pass
// over SQLite (dim/count/skipped/scale), an ids pass (collects the small
// id sections), and a matrix pass (one row at a time). Peak retention is
// the ids section — never the matrix.
//
// Row selection MIRRORS THE LOADER exactly so fp32 files serve
// bit-identical results: rowid order (the loader's ORDER BY rowid), dim
// latches from the first row's DECODED length (len(blob)/4 — the
// dimensions column is advisory and unused by the loader), decoded-length
// mismatches skipped. Deliberate divergence: a first-row empty blob
// (dim=0 with rows present) ERRORS — corrupt embeddings must not
// silently produce an all-skipped index. All passes apply the identical
// rule; a table mutated mid-rebuild is caught by a count cross-check
// (the workspace lock excludes concurrent writers — belt and braces).
func WriteIndexFile(db store.DBHandle, table IndexTable, path string, quant int) (IndexStats, error) {
	var stats IndexStats
	spec := specFor(table)
	if quant != QuantNone && quant != QuantInt8 {
		return stats, fmt.Errorf("vectors.WriteIndexFile: unknown quantization %d", quant)
	}
	stats.Quantization = quant

	// Pass 1: stats. Rows are normalized on the fly for the scale; nothing
	// is retained.
	var maxAbs float32
	err := iterateRows(db.ReadDB(), spec, func(_, _ string, nv []float32) error {
		for _, v := range nv {
			if a := float32(math.Abs(float64(v))); a > maxAbs {
				maxAbs = a
			}
		}
		return nil
	}, &stats)
	if err != nil {
		return stats, err
	}

	h := indexHeader{quant: quant, kind: spec.kind, dim: stats.Dim, count: uint64(stats.Count), scale: 1.0}
	if quant == QuantInt8 && stats.Count > 0 && maxAbs > 0 {
		h.scale = maxAbs
	}

	// Pass 2: ids sections (small — the only retained data).
	var ids, docIDs []string
	var check IndexStats
	err = iterateRows(db.ReadDB(), spec, func(id, docID string, _ []float32) error {
		ids = append(ids, id)
		if spec.kind == tableKindChunk {
			docIDs = append(docIDs, docID)
		}
		return nil
	}, &check)
	if err != nil {
		return stats, err
	}
	if check.Count != stats.Count || check.Skipped != stats.Skipped || check.Dim != stats.Dim {
		return stats, fmt.Errorf("vectors.WriteIndexFile: table changed during rebuild (pass 1 %d/%d rows, pass 2 %d/%d)",
			stats.Count, stats.Skipped, check.Count, check.Skipped)
	}

	tmp := path + ".tmp"
	if err := writeIndexTmp(tmp, h, ids, docIDs); err != nil {
		_ = os.Remove(tmp)
		return stats, err
	}

	// Pass 3: stream the matrix, one row at a time.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		_ = os.Remove(tmp)
		return stats, err
	}
	writeErr := error(nil)
	var matrixCheck IndexStats
	err = iterateRows(db.ReadDB(), spec, func(_, _ string, nv []float32) error {
		return writeMatrixRow(f, h, nv)
	}, &matrixCheck)
	if err != nil {
		writeErr = err
	}
	if writeErr == nil {
		if matrixCheck.Count != stats.Count {
			writeErr = fmt.Errorf("vectors.WriteIndexFile: table changed during rebuild (matrix pass %d rows, want %d)", matrixCheck.Count, stats.Count)
		} else if err := f.Sync(); err != nil {
			writeErr = err
		}
	}
	if cerr := f.Close(); cerr != nil && writeErr == nil {
		writeErr = cerr
	}
	if writeErr != nil {
		_ = os.Remove(tmp)
		return stats, writeErr
	}

	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return stats, fmt.Errorf("vectors.WriteIndexFile: rename: %w", err)
	}
	if info, err := os.Stat(path); err == nil {
		stats.Bytes = info.Size()
	}
	if stats.Skipped > 0 {
		log.Warn("index rebuild: skipped dimension-mismatch rows", "count", stats.Skipped)
	}
	return stats, nil
}

// iterateRows streams the table in rowid order, applies the loader's dim
// rule, normalizes each included row, and calls fn per row. stats
// accumulates count/dim/skipped.
func iterateRows(db *sql.DB, spec tableSpec, fn func(id, docID string, nv []float32) error, stats *IndexStats) error {
	r, err := db.Query(spec.selectSQL)
	if err != nil {
		return fmt.Errorf("vectors.WriteIndexFile: scan: %w", err)
	}
	defer func() { _ = r.Close() }()

	dim := 0
	for r.Next() {
		var id string
		var docID string
		var blob []byte
		if spec.kind == tableKindChunk {
			if err := r.Scan(&id, &docID, &blob); err != nil {
				return err
			}
		} else {
			if err := r.Scan(&id, &blob); err != nil {
				return err
			}
		}
		vec := decodeFloat32s(blob)
		if dim == 0 {
			if len(vec) == 0 {
				return fmt.Errorf(
					"vectors.WriteIndexFile: first row %q has an empty/undecodable embedding — corrupt embeddings; re-embed the workspace", id)
			}
			dim = len(vec)
		}
		if len(vec) != dim {
			stats.Skipped++
			continue
		}
		if err := fn(id, docID, normalizeCopy(vec)); err != nil {
			return err
		}
		stats.Count++
	}
	if err := r.Err(); err != nil {
		return err
	}
	stats.Dim = dim
	return nil
}

// writeIndexTmp writes header + ids sections + alignment padding (the
// matrix follows via append).
func writeIndexTmp(tmp string, h indexHeader, ids, docIDs []string) error {
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	writeIDs := func(list []string) error {
		var lenBuf [2]byte
		for _, id := range list {
			if len(id) > 0xFFFF {
				return fmt.Errorf("vectors.WriteIndexFile: id longer than 65535 bytes")
			}
			binary.LittleEndian.PutUint16(lenBuf[:], uint16(len(id)))
			if _, err := f.Write(lenBuf[:]); err != nil {
				return err
			}
			if _, err := f.Write([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := f.Write(h.encode()); err != nil {
		_ = f.Close()
		return err
	}
	if err := writeIDs(ids); err != nil {
		_ = f.Close()
		return err
	}
	if h.kind == tableKindChunk {
		if err := writeIDs(docIDs); err != nil {
			_ = f.Close()
			return err
		}
	}
	// Zero-pad to a 4-byte boundary: the fp32 matrix is reinterpreted in
	// place at query time, which requires aligned rows.
	written := int64(headerSize)
	for _, id := range ids {
		written += 2 + int64(len(id))
	}
	if h.kind == tableKindChunk {
		for _, id := range docIDs {
			written += 2 + int64(len(id))
		}
	}
	if pad := (4 - int(written%4)) % 4; pad > 0 {
		if _, err := f.Write(make([]byte, pad)); err != nil {
			_ = f.Close()
			return err
		}
	}
	return f.Close()
}

// writeMatrixRow appends one normalized row to the matrix section.
func writeMatrixRow(f *os.File, h indexHeader, row []float32) error {
	if h.quant == QuantInt8 {
		buf := make([]byte, len(row))
		for j, v := range row {
			q := int(math.Round(float64(v / h.scale * 127)))
			if q > 127 {
				q = 127
			} else if q < -127 {
				q = -127
			}
			buf[j] = byte(int8(q))
		}
		_, err := f.Write(buf)
		return err
	}
	buf := make([]byte, len(row)*4)
	for j, v := range row {
		binary.LittleEndian.PutUint32(buf[j*4:], math.Float32bits(v))
	}
	_, err := f.Write(buf)
	return err
}
