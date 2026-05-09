package trust

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/storage"
)

type ProcessOutputOpts struct {
	ProjectDir string
	OutputDir  string
	Question   string
	Answer     string
	Sources    []string
	ChunksUsed []string
	Embedder   embed.Embedder
	Client     *llm.Client
	Model      string
	Cfg        config.TrustConfig
	DB         *storage.DB
	Stores     IndexStores
	UserNow    string
}

type ProcessOutputResult struct {
	OutputID string
	Action   string // "new", "confirmed", "conflict"
	FilePath string
}

func ProcessOutput(opts ProcessOutputOpts) (*ProcessOutputResult, error) {
	sourcesJSON := "[]"
	if len(opts.Sources) > 0 {
		if b, err := json.Marshal(opts.Sources); err == nil {
			sourcesJSON = string(b)
		}
	}
	sourcesHash := ComputeSourcesHash(opts.ProjectDir, sourcesJSON)

	timestamp := time.Now().Format("2006-01-02")
	slug := slugify(opts.Question)
	baseFilename := fmt.Sprintf("%s-%s.md", timestamp, slug)

	qHash := HashQuestion(opts.Question)
	aHash := HashAnswer(opts.Answer)

	var questionVec []float32
	if opts.Embedder != nil {
		var embedErr error
		questionVec, embedErr = opts.Embedder.Embed(opts.Question)
		if embedErr != nil {
			log.Warn("failed to embed question for trust", "error", embedErr)
		}
	}

	var result ProcessOutputResult
	var promoteAfterTx string // ID to promote+index after tx commits

	err := opts.DB.WriteTx(func(tx *sql.Tx) error {
		// Unique filename inside the tx to avoid race
		filename := baseFilename
		var existingID string
		for i := 0; i < 100; i++ {
			candidate := filename
			if i > 0 {
				candidate = fmt.Sprintf("%s-%s-%d.md", timestamp, slug, i+1)
			}
			err := tx.QueryRow(`SELECT id FROM pending_outputs WHERE id = ?`, candidate).Scan(&existingID)
			if err == sql.ErrNoRows {
				filename = candidate
				break
			}
			if err != nil {
				return err
			}
			filename = candidate
		}

		result.OutputID = filename
		relPath := filepath.Join(opts.OutputDir, "under_review", filename)
		result.FilePath = relPath

		var match *SimilarQuestion
		if questionVec != nil {
			var err error
			match, err = FindSimilarQuestion(tx, questionVec, opts.Cfg.SimilarityThresholdOrDefault())
			if err != nil {
				log.Warn("question similarity search failed", "error", err)
			}
		}

		if match != nil {
			return processMatch(tx, match, opts, questionVec, qHash, aHash, filename, relPath, sourcesJSON, sourcesHash, &result, &promoteAfterTx)
		}

		return insertNewPending(tx, questionVec, qHash, aHash, filename, relPath, sourcesJSON, sourcesHash, opts, &result)
	})

	if err != nil {
		return nil, err
	}

	// Post-tx: index promoted output outside the write lock
	if promoteAfterTx != "" {
		store := NewStore(opts.DB)
		if err := PromoteOutput(store, promoteAfterTx, opts.ProjectDir, opts.Stores); err != nil {
			log.Warn("post-tx promotion indexing failed", "id", promoteAfterTx, "error", err)
		}
	}

	// Only write under_review file for new or conflict outputs (not confirmations of existing)
	if result.Action == "new" || result.Action == "conflict" {
		writeUnderReviewFile(opts.ProjectDir, result.FilePath, opts.Question, opts.Answer, opts.Sources, result.Action, opts.UserNow)
	}

	return &result, nil
}

func processMatch(tx *sql.Tx, match *SimilarQuestion, opts ProcessOutputOpts,
	questionVec []float32, qHash, aHash, filename, relPath, sourcesJSON, sourcesHash string,
	result *ProcessOutputResult, promoteAfterTx *string) error {

	agree, err := CompareAnswers(match.Output.Answer, opts.Answer, opts.Embedder, opts.Client, opts.Model)
	if err != nil {
		log.Warn("answer comparison failed", "error", err)
		agree = false
	}

	if agree {
		chunksJSON := "[]"
		if len(opts.ChunksUsed) > 0 {
			if b, err := json.Marshal(opts.ChunksUsed); err == nil {
				chunksJSON = string(b)
			}
		}

		// Idempotency: skip if this exact evidence (same chunks) already recorded
		var existing int
		tx.QueryRow(`SELECT COUNT(*) FROM confirmation_sources
			WHERE output_id = ? AND chunk_ids = ?`, match.Output.ID, chunksJSON).Scan(&existing)
		if existing > 0 {
			result.Action = "confirmed"
			result.OutputID = match.Output.ID
			result.FilePath = match.Output.FilePath
			return nil
		}

		now := time.Now().Format(time.RFC3339)
		if _, err := tx.Exec(`INSERT INTO confirmation_sources (output_id, chunk_ids, answer_hash, confirmed_at)
			VALUES (?, ?, ?, ?)`, match.Output.ID, chunksJSON, aHash, now); err != nil {
			return fmt.Errorf("insert confirmation: %w", err)
		}
		if _, err := tx.Exec(`UPDATE pending_outputs SET confirmations = confirmations + 1 WHERE id = ?`, match.Output.ID); err != nil {
			return fmt.Errorf("increment confirmations: %w", err)
		}

		newConfirmations := match.Output.Confirmations + 1
		rows, err := tx.Query(`SELECT id, output_id, chunk_ids, answer_hash, confirmed_at
			FROM confirmation_sources WHERE output_id = ? ORDER BY confirmed_at`, match.Output.ID)
		if err == nil {
			defer rows.Close()
			var confs []*Confirmation
			for rows.Next() {
				var c Confirmation
				var confirmedAt string
				if err := rows.Scan(&c.ID, &c.OutputID, &c.ChunkIDs, &c.AnswerHash, &confirmedAt); err == nil {
					c.ConfirmedAt, _ = time.Parse(time.RFC3339, confirmedAt)
					confs = append(confs, &c)
				}
			}
			independence := IndependenceScore(confs)

			if opts.Cfg.AutoPromoteEnabled() &&
				ShouldAutoPromote(newConfirmations, independence, opts.Cfg.ConsensusThresholdOrDefault()) {
				if match.Output.GroundingScore != nil && *match.Output.GroundingScore >= opts.Cfg.GroundingThresholdOrDefault() {
					tx.Exec(`UPDATE pending_outputs SET state = 'confirmed', promoted_at = ? WHERE id = ?`,
						now, match.Output.ID)
					*promoteAfterTx = match.Output.ID
				}
			}
		}

		result.Action = "confirmed"
		result.OutputID = match.Output.ID
		result.FilePath = match.Output.FilePath
		return nil
	}

	if _, err := tx.Exec(`UPDATE pending_outputs SET state = ? WHERE id = ?`, string(StateConflict), match.Output.ID); err != nil {
		return fmt.Errorf("set conflict state: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO pending_outputs
		(id, question, question_hash, answer, answer_hash, state,
		 confirmations, sources_hash, sources_used, file_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		filename, opts.Question, qHash, opts.Answer, aHash,
		string(StateConflict), 1, sourcesHash, sourcesJSON, relPath, now); err != nil {
		return err
	}
	if questionVec != nil {
		if err := EmbedAndStoreQuestion(tx, qHash, questionVec); err != nil {
			return fmt.Errorf("store question embedding: %w", err)
		}
	}
	result.Action = "conflict"
	result.FilePath = relPath
	return nil
}

func insertNewPending(tx *sql.Tx, questionVec []float32,
	qHash, aHash, filename, relPath, sourcesJSON, sourcesHash string,
	opts ProcessOutputOpts, result *ProcessOutputResult) error {

	now := time.Now().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO pending_outputs
		(id, question, question_hash, answer, answer_hash, state,
		 confirmations, sources_hash, sources_used, file_path, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		filename, opts.Question, qHash, opts.Answer, aHash,
		string(StatePending), 1, sourcesHash, sourcesJSON, relPath, now); err != nil {
		return err
	}
	if questionVec != nil {
		if err := EmbedAndStoreQuestion(tx, qHash, questionVec); err != nil {
			return fmt.Errorf("store question embedding: %w", err)
		}
	}
	result.Action = "new"
	result.FilePath = relPath
	return nil
}

func writeUnderReviewFile(projectDir, relPath, question, answer string, sources []string, state, userNow string) {
	absPath := filepath.Join(projectDir, relPath)
	os.MkdirAll(filepath.Dir(absPath), 0755)

	escapedQ := strings.ReplaceAll(question, `"`, `\"`)
	escapedQ = strings.ReplaceAll(escapedQ, "\n", " ")

	sourcesStr := "[]"
	if len(sources) > 0 {
		quoted := make([]string, len(sources))
		for i, s := range sources {
			quoted[i] = fmt.Sprintf("%q", s)
		}
		sourcesStr = "[" + strings.Join(quoted, ", ") + "]"
	}

	frontmatter := fmt.Sprintf("---\nquestion: %q\nsources: %s\nstate: %s\ncreated_at: %s\n---\n\n",
		escapedQ, sourcesStr, state, userNow)

	if err := os.WriteFile(absPath, []byte(frontmatter+answer), 0644); err != nil {
		log.Warn("failed to write under_review file", "path", relPath, "error", err)
	}
}

func slugify(s string) string {
	s = fmt.Sprintf("%.50s", s)
	var b []byte
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b = append(b, byte(c))
		case c >= 'A' && c <= 'Z':
			b = append(b, byte(c-'A'+'a'))
		case c == ' ' || c == '-' || c == '_':
			if len(b) > 0 && b[len(b)-1] != '-' {
				b = append(b, '-')
			}
		}
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "output"
	}
	return string(b)
}
