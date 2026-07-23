package postgres

import (
	"database/sql"
	"fmt"

	"github.com/xoai/sage-wiki/internal/store"
)

type outputIndexStore struct{ b *backend }

var _ store.OutputIndexStore = (*outputIndexStore)(nil)

func (s *outputIndexStore) Get(outputPath string) (string, bool, error) {
	var hash string
	err := s.b.pool.QueryRow(
		"SELECT content_hash FROM output_index WHERE output_path=$1", outputPath).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

func (s *outputIndexStore) Set(outputPath, hash string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		return s.SetTx(tx, outputPath, hash)
	})
}

func (s *outputIndexStore) SetTx(tx *sql.Tx, outputPath, hash string) error {
	_, err := tx.Exec(`
		INSERT INTO output_index (output_path, content_hash, indexed_at) VALUES ($1, $2, now())
		ON CONFLICT (output_path) DO UPDATE SET
			content_hash=excluded.content_hash, indexed_at=excluded.indexed_at`,
		outputPath, hash)
	if err != nil {
		return fmt.Errorf("OutputIndex.Set: %w", err)
	}
	return nil
}

func (s *outputIndexStore) Delete(outputPath string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		return s.DeleteTx(tx, outputPath)
	})
}

func (s *outputIndexStore) DeleteTx(tx *sql.Tx, outputPath string) error {
	if _, err := tx.Exec("DELETE FROM output_index WHERE output_path=$1", outputPath); err != nil {
		return fmt.Errorf("OutputIndex.Delete: %w", err)
	}
	return nil
}

func (s *outputIndexStore) All() (map[string]string, error) {
	rows, err := s.b.pool.Query("SELECT output_path, content_hash FROM output_index")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var p, h string
		if err := rows.Scan(&p, &h); err != nil {
			return nil, err
		}
		out[p] = h
	}
	return out, rows.Err()
}

// Backfill: INSERT OR IGNORE parity → ON CONFLICT DO NOTHING (spec §5).
func (s *outputIndexStore) Backfill(outputs map[string][]byte) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		for path, content := range outputs {
			hash := hashBytes(content)
			if _, err := tx.Exec(`
				INSERT INTO output_index (output_path, content_hash, indexed_at) VALUES ($1, $2, now())
				ON CONFLICT (output_path) DO NOTHING`, path, hash); err != nil {
				return err
			}
		}
		return nil
	})
}
