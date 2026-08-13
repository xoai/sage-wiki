package storage

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"github.com/xoai/sage-wiki/internal/store"
)

// HashBytes returns the hex sha256 of b. It is the canonical output-content hash
// stored in output_index and re-computed by the reconciler, so both sides
// compare the same value.
func HashBytes(b []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// OutputIndex records, per compiled output file, the content hash the system
// believes is fully indexed in the DB. The reconciler compares an output file's
// current hash against this record to detect the "changed output" drift case
// (D5), and treats a missing row (with the file present but unindexed) as the
// "file-no-DB" case.
type OutputIndex struct {
	db store.DBHandle
}

// NewOutputIndex returns an OutputIndex over db.
func NewOutputIndex(db store.DBHandle) *OutputIndex {
	return &OutputIndex{db: db}
}

// Get returns the recorded content hash for outputPath.
func (o *OutputIndex) Get(outputPath string) (hash string, ok bool, err error) {
	row := o.db.ReadDB().QueryRow("SELECT content_hash FROM output_index WHERE output_path = ?", outputPath)
	switch err := row.Scan(&hash); err {
	case nil:
		return hash, true, nil
	case sql.ErrNoRows:
		return "", false, nil
	default:
		return "", false, fmt.Errorf("OutputIndex.Get: %w", err)
	}
}

// Set upserts the content hash for outputPath. Callers that re-index an output
// must call this LAST — after FTS, chunks, vectors, and ontology all succeed —
// so a crash mid-reindex never leaves a row that looks indexed but is partial.
func (o *OutputIndex) Set(outputPath, hash string) error {
	return o.db.WriteTx(func(tx *sql.Tx) error {
		return o.SetTx(tx, outputPath, hash)
	})
}

// SetTx upserts within an existing write transaction, so the hash can
// be written atomically with the index rows it certifies.
// (Renamed from SetOutputHashTx, P2-1 T7 — method form for the OutputIndexStore seam.)
func (o *OutputIndex) SetTx(tx *sql.Tx, outputPath, hash string) error {
	_, err := tx.Exec(`
		INSERT INTO output_index (output_path, content_hash, indexed_at)
		VALUES (?, ?, datetime('now'))
		ON CONFLICT(output_path) DO UPDATE SET content_hash = excluded.content_hash, indexed_at = excluded.indexed_at
	`, outputPath, hash)
	if err != nil {
		return fmt.Errorf("OutputIndex.Set: %w", err)
	}
	return nil
}

// Delete removes the record for outputPath (the output file vanished, D5 case b).
func (o *OutputIndex) Delete(outputPath string) error {
	return o.db.WriteTx(func(tx *sql.Tx) error {
		return o.DeleteTx(tx, outputPath)
	})
}

// DeleteTx removes within an existing write transaction.
// (Renamed from DeleteOutputHashTx, P2-1 T7.)
func (o *OutputIndex) DeleteTx(tx *sql.Tx, outputPath string) error {
	if _, err := tx.Exec("DELETE FROM output_index WHERE output_path = ?", outputPath); err != nil {
		return fmt.Errorf("OutputIndex.Delete: %w", err)
	}
	return nil
}

// All returns every recorded output_path → content_hash.
func (o *OutputIndex) All() (map[string]string, error) {
	rows, err := o.db.ReadDB().Query("SELECT output_path, content_hash FROM output_index")
	if err != nil {
		return nil, fmt.Errorf("OutputIndex.All: %w", err)
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, fmt.Errorf("OutputIndex.All: scan: %w", err)
		}
		out[p] = h
	}
	return out, rows.Err()
}

// Backfill records the content hash of each provided output ONLY where no row
// exists yet, hashing the given bytes (the same bytes the reconciler re-hashes).
// It never overwrites an existing row, so it is idempotent and safe to run at
// every startup. This is what keeps the first reconcile after an upgrade from
// flagging every already-indexed output as "changed" — the pre-existing outputs
// get their hash recorded once, cheaply, without a re-index. outputs maps an
// output path to its current on-disk bytes.
func (o *OutputIndex) Backfill(outputs map[string][]byte) error {
	if len(outputs) == 0 {
		return nil
	}
	return o.db.WriteTx(func(tx *sql.Tx) error {
		for path, content := range outputs {
			if _, err := tx.Exec(`
				INSERT OR IGNORE INTO output_index (output_path, content_hash, indexed_at)
				VALUES (?, ?, datetime('now'))
			`, path, HashBytes(content)); err != nil {
				return fmt.Errorf("OutputIndex.Backfill: %w", err)
			}
		}
		return nil
	})
}
