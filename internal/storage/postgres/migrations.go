package postgres

import (
	"context"
	"fmt"
	"strings"
)

// currentSchemaVersion tracks len(schemaMigrations).
const currentSchemaVersion = 1

// schemaMigrations is the append-only Postgres V-series. Each entry is ONE
// statement per Exec (pgx stdlib rejects multi-statement prepared calls).
// V1 ordering is load-bearing: text-search dictionary → configuration →
// tables referencing 'sage_fts' in generated columns → indexes.
//
// {{VECTOR_DIM}} is substituted at migration time from
// OpenOptions.VectorDimension (writer open requires it).
var schemaMigrations = [][]string{
	// V1
	{
		`CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`,

		`CREATE TEXT SEARCH DICTIONARY sage_stem (TEMPLATE = snowball, LANGUAGE = 'english')`,
		`CREATE TEXT SEARCH CONFIGURATION sage_fts (COPY = simple)`,
		`ALTER TEXT SEARCH CONFIGURATION sage_fts ALTER MAPPING FOR asciiword, word WITH sage_stem`,

		// entries — FTS5 parity: no PK/unique (design D7), tsv generated.
		`CREATE TABLE IF NOT EXISTS entries (
			id TEXT,
			content TEXT,
			tags TEXT,
			article_path TEXT,
			tsv TSVECTOR GENERATED ALWAYS AS (to_tsvector('sage_fts', content)) STORED
		)`,
		`CREATE INDEX IF NOT EXISTS idx_entries_tsv ON entries USING GIN (tsv)`,

		`CREATE TABLE IF NOT EXISTS vec_entries (
			id TEXT PRIMARY KEY,
			embedding vector({{VECTOR_DIM}}) NOT NULL,
			dimensions INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_vec_entries_hnsw ON vec_entries USING hnsw (embedding vector_cosine_ops)`,

		`CREATE TABLE IF NOT EXISTS chunks_meta (
			chunk_id TEXT PRIMARY KEY,
			doc_id TEXT NOT NULL,
			chunk_index INTEGER NOT NULL,
			heading TEXT,
			content TEXT NOT NULL,
			start_offset INTEGER,
			end_offset INTEGER,
			tsv TSVECTOR GENERATED ALWAYS AS (to_tsvector('sage_fts', coalesce(heading,'') || ' ' || content)) STORED
		)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks_meta(doc_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_tsv ON chunks_meta USING GIN (tsv)`,

		`CREATE TABLE IF NOT EXISTS vec_chunks (
			chunk_id TEXT PRIMARY KEY,
			doc_id TEXT NOT NULL,
			embedding vector({{VECTOR_DIM}}) NOT NULL,
			dimensions INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_vec_chunks_doc ON vec_chunks(doc_id)`,
		`CREATE INDEX IF NOT EXISTS idx_vec_chunks_hnsw ON vec_chunks USING hnsw (embedding vector_cosine_ops)`,

		`CREATE TABLE IF NOT EXISTS pending_questions_vec (
			question_hash TEXT PRIMARY KEY,
			embedding vector({{VECTOR_DIM}}) NOT NULL,
			dimensions INTEGER NOT NULL
		)`,

		`CREATE TABLE IF NOT EXISTS entities (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			name TEXT NOT NULL,
			definition TEXT,
			article_path TEXT,
			metadata JSONB,
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ
		)`,

		`CREATE TABLE IF NOT EXISTS relations (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			relation TEXT NOT NULL,
			metadata JSONB,
			created_at TIMESTAMPTZ,
			UNIQUE(source_id, target_id, relation)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id)`,
		`CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id)`,
		`CREATE INDEX IF NOT EXISTS idx_relations_type ON relations(relation)`,

		`CREATE TABLE IF NOT EXISTS learnings (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			content TEXT NOT NULL,
			tags TEXT,
			created_at TIMESTAMPTZ,
			source_lint_pass TEXT
		)`,

		`CREATE TABLE IF NOT EXISTS compile_items (
			source_path     TEXT PRIMARY KEY,
			hash            TEXT NOT NULL DEFAULT '',
			file_type       TEXT NOT NULL DEFAULT '',
			size_bytes      BIGINT NOT NULL DEFAULT 0,
			tier            INTEGER NOT NULL DEFAULT 1,
			tier_default    INTEGER NOT NULL DEFAULT 1,
			tier_override   INTEGER,
			pass_indexed    INTEGER NOT NULL DEFAULT 0,
			pass_embedded   INTEGER NOT NULL DEFAULT 0,
			pass_parsed     INTEGER NOT NULL DEFAULT 0,
			pass_summarized INTEGER NOT NULL DEFAULT 0,
			pass_extracted  INTEGER NOT NULL DEFAULT 0,
			pass_written    INTEGER NOT NULL DEFAULT 0,
			compile_id      TEXT,
			error           TEXT,
			error_count     INTEGER NOT NULL DEFAULT 0,
			summary_path    TEXT,
			query_hit_count INTEGER NOT NULL DEFAULT 0,
			last_queried_at TIMESTAMPTZ,
			promoted_at     TIMESTAMPTZ,
			demoted_at      TIMESTAMPTZ,
			source_type     TEXT NOT NULL DEFAULT 'compiler',
			quality_score   DOUBLE PRECISION,
			created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,
		`CREATE INDEX IF NOT EXISTS idx_ci_tier ON compile_items(tier)`,
		`CREATE INDEX IF NOT EXISTS idx_ci_type ON compile_items(file_type)`,
		`CREATE INDEX IF NOT EXISTS idx_ci_compile ON compile_items(compile_id)`,
		`CREATE INDEX IF NOT EXISTS idx_ci_hits ON compile_items(query_hit_count)`,
		`CREATE INDEX IF NOT EXISTS idx_ci_queried ON compile_items(last_queried_at)`,

		`CREATE TABLE IF NOT EXISTS pending_outputs (
			id TEXT PRIMARY KEY,
			question TEXT NOT NULL,
			question_hash TEXT NOT NULL,
			answer TEXT NOT NULL,
			answer_hash TEXT NOT NULL,
			state TEXT NOT NULL DEFAULT 'pending',
			confirmations INTEGER NOT NULL DEFAULT 1,
			grounding_score DOUBLE PRECISION,
			sources_hash TEXT,
			sources_used TEXT,
			file_path TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			promoted_at TIMESTAMPTZ,
			demoted_at TIMESTAMPTZ
		)`,
		`CREATE INDEX IF NOT EXISTS idx_pending_outputs_question_hash ON pending_outputs(question_hash)`,
		`CREATE INDEX IF NOT EXISTS idx_pending_outputs_state ON pending_outputs(state)`,

		`CREATE TABLE IF NOT EXISTS confirmation_sources (
			id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			output_id TEXT NOT NULL REFERENCES pending_outputs(id),
			chunk_ids TEXT NOT NULL,
			answer_hash TEXT NOT NULL,
			confirmed_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_confirmation_sources_output ON confirmation_sources(output_id)`,

		`CREATE TABLE IF NOT EXISTS output_index (
			output_path  TEXT PRIMARY KEY,
			content_hash TEXT NOT NULL,
			indexed_at   TIMESTAMPTZ NOT NULL DEFAULT now()
		)`,

		`INSERT INTO schema_version (version) VALUES (1)`,
	},
}

// migrate applies pending migrations in order, one statement per Exec.
func (b *backend) migrate(ctx context.Context) error {
	if _, err := b.pool.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER NOT NULL)`); err != nil {
		return err
	}
	var version int
	if err := b.pool.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version); err != nil {
		return err
	}
	dim := b.opts.VectorDimension
	if dim <= 0 {
		dim = 768 // placeholder until verifyDimension rejects; DDL needs a value
	}
	for i := version; i < len(schemaMigrations); i++ {
		for _, stmt := range schemaMigrations[i] {
			stmt = strings.ReplaceAll(stmt, "{{VECTOR_DIM}}", fmt.Sprint(dim))
			if _, err := b.pool.ExecContext(ctx, stmt); err != nil {
				if strings.Contains(err.Error(), `type "vector" does not exist`) {
					return fmt.Errorf("pgvector extension missing — run: CREATE EXTENSION vector (requires superuser or rds_superuser): %w", err)
				}
				return fmt.Errorf("migration v%d: %w", i+1, err)
			}
		}
	}
	return nil
}
