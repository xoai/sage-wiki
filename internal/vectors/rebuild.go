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
	kind     int
	selectSQL string
	probeSQL string
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
// The writer MIRRORS THE LOADER's row-selection rule exactly so fp32 files
// serve bit-identical results to the in-memory cache: rows stream in rowid
// order (matching the loader's ORDER BY rowid), dim latches from the first
// row's DECODED length (len(blob)/4 — the dimensions column is advisory and
// unused by the loader), and rows whose decoded length differs are skipped.
// Deliberate divergence (spec §2): a first-row empty blob (dim=0 with rows
// present) ERRORS — corrupt embeddings must not silently produce an
// all-skipped index.
func WriteIndexFile(db store.DBHandle, table IndexTable, path string, quant int) (IndexStats, error) {
	var stats IndexStats
	spec := specFor(table)
	if quant != QuantNone && quant != QuantInt8 {
		return stats, fmt.Errorf("vectors.WriteIndexFile: unknown quantization %d", quant)
	}
	stats.Quantization = quant

	ids, docIDs, rows, err := scanRows(db.ReadDB(), spec, &stats)
	if err != nil {
		return stats, err
	}

	h := indexHeader{quant: quant, kind: spec.kind, dim: stats.Dim, count: uint64(stats.Count), scale: 1.0}
	// int8 needs the global scale over the INCLUDED normalized rows; the
	// scan already normalized them (rows are small enough to hold for one
	// table — the writer is an offline command, not the query path).
	if quant == QuantInt8 && stats.Count > 0 {
		var maxAbs float32
		for _, r := range rows {
			for _, v := range r {
				if a := float32(math.Abs(float64(v))); a > maxAbs {
					maxAbs = a
				}
			}
		}
		if maxAbs > 0 {
			h.scale = maxAbs
		}
	}

	tmp := path + ".tmp"
	if err := writeIndexTmp(tmp, h, ids, docIDs, rows); err != nil {
		_ = os.Remove(tmp)
		return stats, err
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

// scanRows streams the table in rowid order, applies the loader's dim rule,
// and returns normalized included rows.
func scanRows(db *sql.DB, spec tableSpec, stats *IndexStats) (ids, docIDs []string, rows [][]float32, err error) {
	r, err := db.Query(spec.selectSQL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("vectors.WriteIndexFile: scan: %w", err)
	}
	defer func() { _ = r.Close() }()

	dim := 0
	for r.Next() {
		var id string
		var docID string
		var blob []byte
		if spec.kind == tableKindChunk {
			if err := r.Scan(&id, &docID, &blob); err != nil {
				return nil, nil, nil, err
			}
		} else {
			if err := r.Scan(&id, &blob); err != nil {
				return nil, nil, nil, err
			}
		}
		vec := decodeFloat32s(blob)
		if dim == 0 {
			if len(vec) == 0 {
				return nil, nil, nil, fmt.Errorf(
					"vectors.WriteIndexFile: first row %q has an empty/undecodable embedding — corrupt embeddings; re-embed the workspace", id)
			}
			dim = len(vec)
		}
		if len(vec) != dim {
			stats.Skipped++
			continue
		}
		ids = append(ids, id)
		if spec.kind == tableKindChunk {
			docIDs = append(docIDs, docID)
		}
		rows = append(rows, normalizeCopy(vec))
	}
	if err := r.Err(); err != nil {
		return nil, nil, nil, err
	}
	stats.Count = len(ids)
	stats.Dim = dim
	return ids, docIDs, rows, nil
}

// writeIndexTmp serializes header + sections + matrix to tmp and fsyncs.
func writeIndexTmp(tmp string, h indexHeader, ids, docIDs []string, rows [][]float32) error {
	if err := os.MkdirAll(filepath.Dir(tmp), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	writeIDs := func(w *os.File, list []string) error {
		var lenBuf [2]byte
		for _, id := range list {
			if len(id) > 0xFFFF {
				return fmt.Errorf("vectors.WriteIndexFile: id longer than 65535 bytes")
			}
			binary.LittleEndian.PutUint16(lenBuf[:], uint16(len(id)))
			if _, err := w.Write(lenBuf[:]); err != nil {
				return err
			}
			if _, err := w.Write([]byte(id)); err != nil {
				return err
			}
		}
		return nil
	}

	if _, err := f.Write(h.encode()); err != nil {
		_ = f.Close()
		return err
	}
	if err := writeIDs(f, ids); err != nil {
		_ = f.Close()
		return err
	}
	if h.kind == tableKindChunk {
		if err := writeIDs(f, docIDs); err != nil {
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
	for _, id := range docIDs {
		written += 2 + int64(len(id))
	}
	if pad := (4 - int(written%4)) % 4; pad > 0 {
		if _, err := f.Write(make([]byte, pad)); err != nil {
			_ = f.Close()
			return err
		}
	}

	var elem [4]byte
	for _, row := range rows {
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
			if _, err := f.Write(buf); err != nil {
				_ = f.Close()
				return err
			}
			continue
		}
		for _, v := range row {
			binary.LittleEndian.PutUint32(elem[:], math.Float32bits(v))
			if _, err := f.Write(elem[:]); err != nil {
				_ = f.Close()
				return err
			}
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
