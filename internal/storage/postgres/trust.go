package postgres

import (
	"database/sql"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/xoai/sage-wiki/internal/store"
)

type trustStore struct{ b *backend }

var _ store.TrustStore = (*trustStore)(nil)

const pendingCols = `id, question, question_hash, answer, answer_hash,
	state, confirmations, grounding_score, sources_hash, sources_used,
	file_path, created_at, promoted_at, demoted_at`

func scanPending(row interface{ Scan(...any) error }) (*store.PendingOutput, error) {
	var o store.PendingOutput
	var state string
	var gs sql.NullFloat64
	var sh, su sql.NullString
	var created time.Time
	var promoted, demoted *time.Time
	if err := row.Scan(&o.ID, &o.Question, &o.QuestionHash, &o.Answer, &o.AnswerHash,
		&state, &o.Confirmations, &gs, &sh, &su, &o.FilePath, &created, &promoted, &demoted); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	o.State = store.OutputState(state)
	if gs.Valid {
		o.GroundingScore = &gs.Float64
	}
	o.SourcesHash, o.SourcesUsed = sh.String, su.String
	o.CreatedAt = created.UTC()
	o.PromotedAt, o.DemotedAt = promoted, demoted
	return &o, nil
}

func (s *trustStore) InsertPending(o *store.PendingOutput) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO pending_outputs
			(id, question, question_hash, answer, answer_hash, state,
			 confirmations, sources_hash, sources_used, file_path, created_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			o.ID, o.Question, o.QuestionHash, o.Answer, o.AnswerHash,
			string(o.State), o.Confirmations, nullStr(o.SourcesHash), nullStr(o.SourcesUsed),
			o.FilePath, o.CreatedAt.UTC())
		return err
	})
}

func (s *trustStore) Get(id string) (*store.PendingOutput, error) {
	return scanPending(s.b.pool.QueryRow("SELECT "+pendingCols+" FROM pending_outputs WHERE id=$1", id))
}

func (s *trustStore) ListByState(state store.OutputState) ([]*store.PendingOutput, error) {
	return s.listWhere("state=$1", string(state))
}

func (s *trustStore) ListConfirmed() ([]*store.PendingOutput, error) {
	return s.listWhere("state=$1", string(store.StateConfirmed))
}

func (s *trustStore) ListByQuestionHash(qHash string) ([]*store.PendingOutput, error) {
	return s.listWhere("question_hash=$1", qHash)
}

func (s *trustStore) ListOlderThan(cutoff time.Time) ([]*store.PendingOutput, error) {
	rows, err := s.b.pool.Query(`SELECT `+pendingCols+` FROM pending_outputs
		WHERE state IN ('pending','stale') AND created_at < $1 ORDER BY created_at`, cutoff.UTC())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingRows(rows)
}

func (s *trustStore) listWhere(cond string, arg any) ([]*store.PendingOutput, error) {
	rows, err := s.b.pool.Query("SELECT "+pendingCols+" FROM pending_outputs WHERE "+cond+" ORDER BY created_at", arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPendingRows(rows)
}

func scanPendingRows(rows *sql.Rows) ([]*store.PendingOutput, error) {
	var out []*store.PendingOutput
	for rows.Next() {
		o, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *trustStore) UpdateGroundingScore(id string, score float64) error {
	return s.exec1("UPDATE pending_outputs SET grounding_score=$2 WHERE id=$1", id, score)
}

func (s *trustStore) IncrementConfirmations(id string) error {
	return s.exec1("UPDATE pending_outputs SET confirmations=confirmations+1 WHERE id=$1", id)
}

func (s *trustStore) SetState(id string, state store.OutputState) error {
	return s.exec1("UPDATE pending_outputs SET state=$2 WHERE id=$1", id, string(state))
}

func (s *trustStore) Promote(id string) error {
	// RFC3339 write parity: sqlite writes time.Now().Format(RFC3339) (local
	// offset); postgres sessions are UTC-pinned (spec §5) — same instant.
	return s.exec1("UPDATE pending_outputs SET state='confirmed', promoted_at=$2 WHERE id=$1",
		id, time.Now().UTC())
}

func (s *trustStore) Demote(id string) error {
	return s.exec1(`UPDATE pending_outputs SET state='stale', demoted_at=$2,
		confirmations=0, grounding_score=NULL WHERE id=$1`, id, time.Now().UTC())
}

func (s *trustStore) UpdateFilePath(id string, filePath string) error {
	return s.exec1("UPDATE pending_outputs SET file_path=$2 WHERE id=$1", id, filePath)
}

func (s *trustStore) exec1(sqlText string, args ...any) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(sqlText, args...)
		return err
	})
}

func (s *trustStore) Delete(id string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM confirmation_sources WHERE output_id=$1", id); err != nil {
			return err
		}
		_, err := tx.Exec("DELETE FROM pending_outputs WHERE id=$1", id)
		return err
	})
}

// IsConfirmed: id-only state probe (trust/store.go:125-129 parity).
func (s *trustStore) IsConfirmed(docID string) bool {
	var state string
	err := s.b.pool.QueryRow("SELECT state FROM pending_outputs WHERE id=$1", docID).Scan(&state)
	return err == nil && state == string(store.StateConfirmed)
}

func (s *trustStore) RecordConfirmation(outputID string, chunkIDs string, answerHash string) error {
	return s.b.WriteTx(func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO confirmation_sources (output_id, chunk_ids, answer_hash, confirmed_at)
			VALUES ($1,$2,$3,$4)`, outputID, chunkIDs, answerHash, time.Now().UTC())
		return err
	})
}

func (s *trustStore) GetConfirmations(outputID string) ([]*store.Confirmation, error) {
	rows, err := s.b.pool.Query(
		"SELECT id, output_id, chunk_ids, answer_hash, confirmed_at FROM confirmation_sources WHERE output_id=$1 ORDER BY confirmed_at",
		outputID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*store.Confirmation
	for rows.Next() {
		var c store.Confirmation
		var id int64
		var confirmed time.Time
		if err := rows.Scan(&id, &c.OutputID, &c.ChunkIDs, &c.AnswerHash, &confirmed); err != nil {
			return nil, err
		}
		c.ID = int(id)
		c.ConfirmedAt = confirmed.UTC()
		out = append(out, &c)
	}
	return out, rows.Err()
}

// EmbedAndStoreQuestion: INSERT OR REPLACE → ON CONFLICT DO UPDATE (spec §5).
func (s *trustStore) EmbedAndStoreQuestion(tx *sql.Tx, questionHash string, embedding []float32) error {
	_, err := tx.Exec(`
		INSERT INTO pending_questions_vec (question_hash, embedding, dimensions) VALUES ($1,$2,$3)
		ON CONFLICT (question_hash) DO UPDATE SET embedding=excluded.embedding, dimensions=excluded.dimensions`,
		questionHash, pgvector.NewVector(embedding), len(embedding))
	return err
}

// FindSimilarQuestion: DB-side cosine with the dimension guard as a WHERE
// predicate (the sqlite per-row dim-skip is vacuous on a fixed-dim column
// but preserved as a guard, spec D6) and threshold in SQL.
func (s *trustStore) FindSimilarQuestion(tx *sql.Tx, questionVec []float32, threshold float64) (*store.SimilarQuestion, error) {
	if len(questionVec) == 0 || len(questionVec) != s.b.opts.VectorDimension {
		return nil, nil // dim guard, Go-side (see vectors.go)
	}
	row := tx.QueryRow(`
		SELECT pqv.question_hash, 1 - (pqv.embedding <=> $1) AS score
		FROM pending_questions_vec pqv
		INNER JOIN pending_outputs po ON po.question_hash = pqv.question_hash
		WHERE po.state IN ('pending','confirmed')
		  AND pqv.dimensions = $2
		  AND 1 - (pqv.embedding <=> $1) >= $3
		ORDER BY pqv.embedding <=> $1
		LIMIT 1`, pgvector.NewVector(questionVec), len(questionVec), threshold)
	var qHash string
	var score float64
	if err := row.Scan(&qHash, &score); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}

	o, err := scanPending(tx.QueryRow(`SELECT `+pendingCols+` FROM pending_outputs
		WHERE question_hash=$1 AND state IN ('pending','confirmed') LIMIT 1`, qHash))
	if err != nil {
		return nil, err
	}
	return &store.SimilarQuestion{Output: o, Score: score}, nil
}
