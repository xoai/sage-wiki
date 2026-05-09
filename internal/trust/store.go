package trust

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"time"

	"github.com/xoai/sage-wiki/internal/storage"
)

type Store struct {
	db *storage.DB
}

func NewStore(db *storage.DB) *Store {
	return &Store{db: db}
}

func HashQuestion(question string) string {
	h := sha256.Sum256([]byte(question))
	return fmt.Sprintf("%x", h[:16])
}

func HashAnswer(answer string) string {
	h := sha256.Sum256([]byte(answer))
	return fmt.Sprintf("%x", h[:16])
}

func (s *Store) InsertPending(o *PendingOutput) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO pending_outputs
			(id, question, question_hash, answer, answer_hash, state,
			 confirmations, sources_hash, sources_used, file_path, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			o.ID, o.Question, o.QuestionHash, o.Answer, o.AnswerHash,
			string(o.State), o.Confirmations, o.SourcesHash, o.SourcesUsed,
			o.FilePath, o.CreatedAt.Format(time.RFC3339))
		return err
	})
}

func (s *Store) Get(id string) (*PendingOutput, error) {
	row := s.db.ReadDB().QueryRow(`SELECT id, question, question_hash, answer, answer_hash,
		state, confirmations, grounding_score, sources_hash, sources_used,
		file_path, created_at, promoted_at, demoted_at
		FROM pending_outputs WHERE id = ?`, id)
	return scanOutput(row)
}

func (s *Store) ListByState(state OutputState) ([]*PendingOutput, error) {
	rows, err := s.db.ReadDB().Query(`SELECT id, question, question_hash, answer, answer_hash,
		state, confirmations, grounding_score, sources_hash, sources_used,
		file_path, created_at, promoted_at, demoted_at
		FROM pending_outputs WHERE state = ? ORDER BY created_at DESC`, string(state))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*PendingOutput
	for rows.Next() {
		o, err := scanOutputRow(rows)
		if err != nil {
			return nil, err
		}
		results = append(results, o)
	}
	return results, rows.Err()
}

func (s *Store) UpdateGroundingScore(id string, score float64) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE pending_outputs SET grounding_score = ? WHERE id = ?`, score, id)
		return err
	})
}

func (s *Store) IncrementConfirmations(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE pending_outputs SET confirmations = confirmations + 1 WHERE id = ?`, id)
		return err
	})
}

func (s *Store) SetState(id string, state OutputState) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE pending_outputs SET state = ? WHERE id = ?`, string(state), id)
		return err
	})
}

func (s *Store) Promote(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		now := time.Now().Format(time.RFC3339)
		_, err := tx.Exec(`UPDATE pending_outputs SET state = 'confirmed', promoted_at = ? WHERE id = ?`, now, id)
		return err
	})
}

func (s *Store) Demote(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		now := time.Now().Format(time.RFC3339)
		_, err := tx.Exec(`UPDATE pending_outputs SET state = 'stale', demoted_at = ?,
			confirmations = 0, grounding_score = NULL WHERE id = ?`, now, id)
		return err
	})
}

func (s *Store) Delete(id string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		tx.Exec(`DELETE FROM confirmation_sources WHERE output_id = ?`, id)
		_, err := tx.Exec(`DELETE FROM pending_outputs WHERE id = ?`, id)
		return err
	})
}

func (s *Store) IsConfirmed(docID string) bool {
	var state string
	err := s.db.ReadDB().QueryRow(`SELECT state FROM pending_outputs WHERE id = ?`, docID).Scan(&state)
	return err == nil && state == string(StateConfirmed)
}

func (s *Store) RecordConfirmation(outputID string, chunkIDs string, answerHash string) error {
	return s.db.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO confirmation_sources (output_id, chunk_ids, answer_hash, confirmed_at)
			VALUES (?, ?, ?, ?)`, outputID, chunkIDs, answerHash, time.Now().Format(time.RFC3339))
		return err
	})
}

func (s *Store) GetConfirmations(outputID string) ([]*Confirmation, error) {
	rows, err := s.db.ReadDB().Query(`SELECT id, output_id, chunk_ids, answer_hash, confirmed_at
		FROM confirmation_sources WHERE output_id = ? ORDER BY confirmed_at`, outputID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*Confirmation
	for rows.Next() {
		var c Confirmation
		var confirmedAt string
		if err := rows.Scan(&c.ID, &c.OutputID, &c.ChunkIDs, &c.AnswerHash, &confirmedAt); err != nil {
			return nil, err
		}
		c.ConfirmedAt, _ = time.Parse(time.RFC3339, confirmedAt)
		results = append(results, &c)
	}
	return results, rows.Err()
}

func (s *Store) ListConfirmed() ([]*PendingOutput, error) {
	return s.ListByState(StateConfirmed)
}

type scannable interface {
	Scan(dest ...any) error
}

func scanOutput(row *sql.Row) (*PendingOutput, error) {
	var o PendingOutput
	var state, createdAt string
	var groundingScore sql.NullFloat64
	var promotedAt, demotedAt sql.NullString

	err := row.Scan(&o.ID, &o.Question, &o.QuestionHash, &o.Answer, &o.AnswerHash,
		&state, &o.Confirmations, &groundingScore, &o.SourcesHash, &o.SourcesUsed,
		&o.FilePath, &createdAt, &promotedAt, &demotedAt)
	if err != nil {
		return nil, err
	}

	o.State = OutputState(state)
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if groundingScore.Valid {
		o.GroundingScore = &groundingScore.Float64
	}
	if promotedAt.Valid {
		t, _ := time.Parse(time.RFC3339, promotedAt.String)
		o.PromotedAt = &t
	}
	if demotedAt.Valid {
		t, _ := time.Parse(time.RFC3339, demotedAt.String)
		o.DemotedAt = &t
	}
	return &o, nil
}

func scanOutputRow(rows *sql.Rows) (*PendingOutput, error) {
	var o PendingOutput
	var state, createdAt string
	var groundingScore sql.NullFloat64
	var promotedAt, demotedAt sql.NullString

	err := rows.Scan(&o.ID, &o.Question, &o.QuestionHash, &o.Answer, &o.AnswerHash,
		&state, &o.Confirmations, &groundingScore, &o.SourcesHash, &o.SourcesUsed,
		&o.FilePath, &createdAt, &promotedAt, &demotedAt)
	if err != nil {
		return nil, err
	}

	o.State = OutputState(state)
	o.CreatedAt, _ = time.Parse(time.RFC3339, createdAt)
	if groundingScore.Valid {
		o.GroundingScore = &groundingScore.Float64
	}
	if promotedAt.Valid {
		t, _ := time.Parse(time.RFC3339, promotedAt.String)
		o.PromotedAt = &t
	}
	if demotedAt.Valid {
		t, _ := time.Parse(time.RFC3339, demotedAt.String)
		o.DemotedAt = &t
	}
	return &o, nil
}
