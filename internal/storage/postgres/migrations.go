package postgres

import (
	"context"
	"fmt"
	"strings"
)

// currentSchemaVersion tracks len(schemaMigrations).
const currentSchemaVersion = 4

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

		// Idempotent by DO-block guards: PG has no IF NOT EXISTS for text-search
		// objects, and a partial V1 (e.g. pgvector missing) must be retryable.
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_ts_dict WHERE dictname = 'sage_stem') THEN
				CREATE TEXT SEARCH DICTIONARY sage_stem (TEMPLATE = snowball, LANGUAGE = 'english');
			END IF;
		END $$`,
		`DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_ts_config WHERE cfgname = 'sage_fts') THEN
				CREATE TEXT SEARCH CONFIGURATION sage_fts (COPY = simple);
			END IF;
		END $$`,
		`ALTER TEXT SEARCH CONFIGURATION sage_fts ALTER MAPPING FOR asciiword, word WITH sage_stem`,

		// entries — FTS5 parity: no PK/unique (design D7), tsv generated.
		`CREATE TABLE IF NOT EXISTS entries (
			id TEXT,
			content TEXT,
			tags TEXT,
			article_path TEXT,
			tsv TSVECTOR GENERATED ALWAYS AS (to_tsvector('sage_fts', coalesce(id,'') || ' ' || coalesce(content,'') || ' ' || coalesce(tags,'') || ' ' || coalesce(article_path,''))) STORED
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
			metadata JSONB, -- column parity with sqlite V1 (unused by Go paths on both backends)
			created_at TIMESTAMPTZ,
			updated_at TIMESTAMPTZ
		)`,

		`CREATE TABLE IF NOT EXISTS relations (
			id TEXT PRIMARY KEY,
			source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
			relation TEXT NOT NULL,
			metadata JSONB, -- column parity with sqlite V1 (unused by Go paths on both backends)
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

		`INSERT INTO schema_version (version) SELECT 1 WHERE NOT EXISTS (SELECT 1 FROM schema_version WHERE version = 1)`,
	},

	// V2 — durable job queue columns on compile_items (P2-3), mirroring
	// sqlite migration V9. Backfill is per-tier: only rows whose passes are
	// complete for their tier become 'done'. ADD COLUMN IF NOT EXISTS keeps
	// a partial V2 retryable, same idempotence contract as V1.
	{
		`ALTER TABLE compile_items ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'pending'`,
		`ALTER TABLE compile_items ADD COLUMN IF NOT EXISTS lease_owner TEXT`,
		`ALTER TABLE compile_items ADD COLUMN IF NOT EXISTS lease_until TIMESTAMPTZ`,
		`ALTER TABLE compile_items ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ`,
		`ALTER TABLE compile_items ADD COLUMN IF NOT EXISTS attempts INTEGER NOT NULL DEFAULT 0`,
		`CREATE INDEX IF NOT EXISTS idx_ci_claim ON compile_items(status, lease_until)`,
		`UPDATE compile_items SET status = 'done' WHERE
			(tier = 0 AND pass_indexed = 1) OR
			(tier = 1 AND pass_indexed = 1 AND pass_embedded = 1) OR
			(tier = 2 AND pass_indexed = 1 AND pass_embedded = 1 AND pass_parsed = 1) OR
			(tier = 3 AND pass_indexed = 1 AND pass_embedded = 1
				AND pass_summarized = 1 AND pass_extracted = 1 AND pass_written = 1)`,
		`INSERT INTO schema_version (version) SELECT 2 WHERE NOT EXISTS (SELECT 1 FROM schema_version WHERE version = 2)`,
	},

	// v3 — P3-1 (GRAPH-01): evidence and provenance on relations. Mirrors
	// sqlite migrationV10 column-for-column.
	//
	// valid_from/valid_to/invalidated_by are TEXT, not TIMESTAMPTZ, and
	// deliberately inconsistent with created_at: nullRFC silently converts any
	// non-RFC3339 string to NULL, and P3-6 will populate valid_from from
	// document frontmatter dates (commonly "2026-01-15"). TEXT keeps the two
	// backends byte-identical until P3-6 defines the format contract.
	{
		`ALTER TABLE relations ADD COLUMN IF NOT EXISTS evidence TEXT`,
		`ALTER TABLE relations ADD COLUMN IF NOT EXISTS confidence DOUBLE PRECISION`,
		`ALTER TABLE relations ADD COLUMN IF NOT EXISTS source_doc TEXT`,
		`ALTER TABLE relations ADD COLUMN IF NOT EXISTS valid_from TEXT`,
		`ALTER TABLE relations ADD COLUMN IF NOT EXISTS valid_to TEXT`,
		`ALTER TABLE relations ADD COLUMN IF NOT EXISTS invalidated_by TEXT`,
		`INSERT INTO schema_version (version) SELECT 3 WHERE NOT EXISTS (SELECT 1 FROM schema_version WHERE version = 3)`,
	},

	// v4 — P3-3 (GRAPH-03): the entity-alias table. Mirrors sqlite
	// migrationV11 column-for-column.
	//
	// REAL -> DOUBLE PRECISION, but the timestamps stay TEXT and are bound as
	// raw strings, NOT through nullRFC/scanNullRFC: those convert via
	// time.Time, so a TIMESTAMPTZ column would store Postgres's own rendering
	// and read back differently from the byte-identical string SQLite keeps.
	// Same reasoning P3-1 applied to relations.valid_from, with the binding
	// helper named this time.
	//
	// The partial unique index is supported identically on both backends and is
	// what enforces one live decision per alias while letting rejections
	// accumulate.
	{
		`CREATE TABLE IF NOT EXISTS entity_aliases (
			alias        TEXT NOT NULL,
			canonical_id TEXT NOT NULL,
			entity_type  TEXT NOT NULL,
			status       TEXT NOT NULL,
			confidence   DOUBLE PRECISION,
			reason       TEXT,
			source       TEXT NOT NULL,
			created_at   TEXT NOT NULL,
			decided_at   TEXT,
			decided_by   TEXT,
			PRIMARY KEY (alias, canonical_id)
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_aliases_active
			ON entity_aliases(alias) WHERE status IN ('applied','pending')`,
		`CREATE INDEX IF NOT EXISTS idx_entity_aliases_canonical ON entity_aliases(canonical_id)`,
		`CREATE INDEX IF NOT EXISTS idx_entity_aliases_status ON entity_aliases(status)`,
		`INSERT INTO schema_version (version) SELECT 4 WHERE NOT EXISTS (SELECT 1 FROM schema_version WHERE version = 4)`,
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
