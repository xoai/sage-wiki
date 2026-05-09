package storage

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
	_ "modernc.org/sqlite"
)

// DB manages SQLite connections with WAL mode and single-writer pattern.
type DB struct {
	write    *sql.DB
	read     *sql.DB
	writeMu  sync.Mutex
	closeOnce sync.Once
}

// Open creates a new DB connection to the given path.
// It enables WAL mode, foreign keys, and busy timeout.
func Open(path string) (*DB, error) {
	// Write connection
	writeDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("storage.Open: %w", err)
	}
	writeDB.SetMaxOpenConns(1)

	// Pragmas for write connection
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := writeDB.Exec(pragma); err != nil {
			writeDB.Close()
			return nil, fmt.Errorf("storage.Open: %s: %w", pragma, err)
		}
	}

	// Read connection pool
	readDB, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("storage.Open: read pool: %w", err)
	}
	readDB.SetMaxOpenConns(4)

	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := readDB.Exec(pragma); err != nil {
			writeDB.Close()
			readDB.Close()
			return nil, fmt.Errorf("storage.Open: read %s: %w", pragma, err)
		}
	}

	db := &DB{write: writeDB, read: readDB}

	if err := db.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("storage.Open: migrate: %w", err)
	}

	log.Info("database opened", "path", path)
	return db, nil
}

// WriteDB returns the write connection for use in transactions.
// Callers MUST hold the write lock via WriteTx.
func (db *DB) WriteDB() *sql.DB {
	return db.write
}

// ReadDB returns the read connection pool.
func (db *DB) ReadDB() *sql.DB {
	return db.read
}

// WriteTx executes fn within a serialized write transaction.
// Only one write transaction runs at a time.
func (db *DB) WriteTx(fn func(tx *sql.Tx) error) error {
	db.writeMu.Lock()
	defer db.writeMu.Unlock()

	tx, err := db.write.Begin()
	if err != nil {
		return fmt.Errorf("storage.WriteTx: begin: %w", err)
	}

	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}

	return tx.Commit()
}

// Close closes both read and write connections. Safe for concurrent calls.
func (db *DB) Close() error {
	var closeErr error
	db.closeOnce.Do(func() {
		var errs []error
		if err := db.read.Close(); err != nil {
			errs = append(errs, err)
		}
		if err := db.write.Close(); err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			closeErr = fmt.Errorf("storage.Close: %v", errs)
		}
	})
	return closeErr
}

// migrate runs schema migrations.
func (db *DB) migrate() error {
	// Create schema version table
	if _, err := db.write.Exec(`
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER NOT NULL
		)
	`); err != nil {
		return err
	}

	var version int
	err := db.write.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return err
	}

	type migration struct {
		sql            string
		disableFK      bool // run PRAGMA foreign_keys=OFF before tx, restore after
	}

	migrations := []migration{
		{sql: migrationV1},
		{sql: migrationV2},
		{sql: migrationV3},
		{sql: migrationV4, disableFK: true},
		{sql: migrationV5},
		{sql: migrationV6},
	}

	for i := version; i < len(migrations); i++ {
		m := migrations[i]
		log.Info("running migration", "version", i+1)

		if m.disableFK {
			if _, err := db.write.Exec("PRAGMA foreign_keys = OFF"); err != nil {
				return fmt.Errorf("migration v%d: disable FK: %w", i+1, err)
			}
		}

		tx, err := db.write.Begin()
		if err != nil {
			return fmt.Errorf("migration v%d: begin: %w", i+1, err)
		}
		if _, err := tx.Exec(m.sql); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration v%d: %w", i+1, err)
		}
		if _, err := tx.Exec("INSERT INTO schema_version (version) VALUES (?)", i+1); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration v%d: version insert: %w", i+1, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("migration v%d: commit: %w", i+1, err)
		}

		if m.disableFK {
			if _, err := db.write.Exec("PRAGMA foreign_keys = ON"); err != nil {
				return fmt.Errorf("migration v%d: restore FK: %w", i+1, err)
			}
		}
	}

	return nil
}

const migrationV1 = `
-- FTS5 full-text index
CREATE VIRTUAL TABLE IF NOT EXISTS entries USING fts5(
	id, content, tags, article_path,
	tokenize='porter unicode61'
);

-- Vector embeddings (pure Go, no sqlite-vec)
CREATE TABLE IF NOT EXISTS vec_entries (
	id TEXT PRIMARY KEY,
	embedding BLOB NOT NULL,
	dimensions INTEGER NOT NULL
);

-- Ontology: entities
CREATE TABLE IF NOT EXISTS entities (
	id TEXT PRIMARY KEY,
	type TEXT NOT NULL CHECK(type IN ('concept','technique','source','claim','artifact')),
	name TEXT NOT NULL,
	definition TEXT,
	article_path TEXT,
	metadata JSON,
	created_at TEXT,
	updated_at TEXT
);

-- Ontology: relations
CREATE TABLE IF NOT EXISTS relations (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	relation TEXT NOT NULL,
	metadata JSON,
	created_at TEXT,
	UNIQUE(source_id, target_id, relation)
);

CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id);
CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id);
CREATE INDEX IF NOT EXISTS idx_relations_type ON relations(relation);

-- Self-learning
CREATE TABLE IF NOT EXISTS learnings (
	id TEXT PRIMARY KEY,
	type TEXT NOT NULL,
	content TEXT NOT NULL,
	tags TEXT,
	created_at TEXT,
	source_lint_pass TEXT
);
`

// migrationV2 removes the CHECK constraint on relations.relation to support custom types.
// SQLite doesn't support ALTER TABLE DROP CONSTRAINT, so we recreate the table.
const migrationV2 = `
CREATE TABLE IF NOT EXISTS relations_new (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	relation TEXT NOT NULL,
	metadata JSON,
	created_at TEXT,
	UNIQUE(source_id, target_id, relation)
);

INSERT OR IGNORE INTO relations_new SELECT * FROM relations;
DROP TABLE IF EXISTS relations;
ALTER TABLE relations_new RENAME TO relations;

CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id);
CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id);
CREATE INDEX IF NOT EXISTS idx_relations_type ON relations(relation);
`

// migrationV3 adds chunk-level indexing tables for enhanced search.
const migrationV3 = `
-- Chunk metadata (IDs, positions, content)
CREATE TABLE IF NOT EXISTS chunks_meta (
	chunk_id TEXT PRIMARY KEY,
	doc_id TEXT NOT NULL,
	chunk_index INTEGER NOT NULL,
	heading TEXT,
	content TEXT NOT NULL,
	start_offset INTEGER,
	end_offset INTEGER
);
CREATE INDEX IF NOT EXISTS idx_chunks_doc ON chunks_meta(doc_id);

-- FTS5 for chunk search (regular table, stores its own copy of text)
-- chunk_id is UNINDEXED so it doesn't pollute BM25 rankings but is available for JOIN
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	chunk_id UNINDEXED,
	heading, content,
	tokenize='porter unicode61'
);

-- Chunk vector embeddings
CREATE TABLE IF NOT EXISTS vec_chunks (
	chunk_id TEXT PRIMARY KEY,
	doc_id TEXT NOT NULL,
	embedding BLOB NOT NULL,
	dimensions INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_vec_chunks_doc ON vec_chunks(doc_id);
`

// migrationV4 removes the CHECK constraint on entities.type to support custom entity types.
// Requires disableFK=true in the migration runner because we drop and recreate the entities
// table, which temporarily invalidates the relations FK reference.
const migrationV4 = `
-- Remove CHECK constraint on entities.type for custom entity types
CREATE TABLE IF NOT EXISTS entities_new (
	id TEXT PRIMARY KEY,
	type TEXT NOT NULL,
	name TEXT NOT NULL,
	definition TEXT,
	article_path TEXT,
	metadata JSON,
	created_at TEXT,
	updated_at TEXT
);

INSERT OR IGNORE INTO entities_new SELECT * FROM entities;
DROP TABLE IF EXISTS entities;
ALTER TABLE entities_new RENAME TO entities;

-- Recreate relations table to restore CASCADE DELETE on the new entities table
CREATE TABLE IF NOT EXISTS relations_rebuild (
	id TEXT PRIMARY KEY,
	source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	relation TEXT NOT NULL,
	metadata JSON,
	created_at TEXT,
	UNIQUE(source_id, target_id, relation)
);

INSERT OR IGNORE INTO relations_rebuild SELECT * FROM relations;
DROP TABLE IF EXISTS relations;
ALTER TABLE relations_rebuild RENAME TO relations;

CREATE INDEX IF NOT EXISTS idx_relations_source ON relations(source_id);
CREATE INDEX IF NOT EXISTS idx_relations_target ON relations(target_id);
CREATE INDEX IF NOT EXISTS idx_relations_type ON relations(relation);
`

// migrationV5 adds the compile_items table for per-item compilation state and tier tracking.
// This replaces the JSON compile-state.json with per-item state in SQLite.
const migrationV5 = `
CREATE TABLE IF NOT EXISTS compile_items (
	source_path     TEXT PRIMARY KEY,
	hash            TEXT NOT NULL DEFAULT '',
	file_type       TEXT NOT NULL DEFAULT '',
	size_bytes      INTEGER NOT NULL DEFAULT 0,

	-- Tier state (0=index only, 1=index+embed, 2=code parse, 3=full compile)
	tier            INTEGER NOT NULL DEFAULT 1,
	tier_default    INTEGER NOT NULL DEFAULT 1,
	tier_override   INTEGER,

	-- Per-pass completion (0 = not done, 1 = done)
	pass_indexed    INTEGER NOT NULL DEFAULT 0,
	pass_embedded   INTEGER NOT NULL DEFAULT 0,
	pass_parsed     INTEGER NOT NULL DEFAULT 0,
	pass_summarized INTEGER NOT NULL DEFAULT 0,
	pass_extracted  INTEGER NOT NULL DEFAULT 0,
	pass_written    INTEGER NOT NULL DEFAULT 0,

	-- Compilation metadata
	compile_id      TEXT,
	error           TEXT,
	error_count     INTEGER NOT NULL DEFAULT 0,
	summary_path    TEXT,

	-- Promotion/demotion signals
	query_hit_count INTEGER NOT NULL DEFAULT 0,
	last_queried_at TEXT,
	promoted_at     TEXT,
	demoted_at      TEXT,

	-- Quality tracking
	source_type     TEXT NOT NULL DEFAULT 'compiler',
	quality_score   REAL,

	-- Timestamps
	created_at      TEXT NOT NULL DEFAULT (datetime('now')),
	updated_at      TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_ci_tier ON compile_items(tier);
CREATE INDEX IF NOT EXISTS idx_ci_type ON compile_items(file_type);
CREATE INDEX IF NOT EXISTS idx_ci_compile ON compile_items(compile_id);
CREATE INDEX IF NOT EXISTS idx_ci_hits ON compile_items(query_hit_count);
CREATE INDEX IF NOT EXISTS idx_ci_queried ON compile_items(last_queried_at);
`

// migrationV6 adds the output trust system tables.
const migrationV6 = `
CREATE TABLE IF NOT EXISTS pending_outputs (
	id TEXT PRIMARY KEY,
	question TEXT NOT NULL,
	question_hash TEXT NOT NULL,
	answer TEXT NOT NULL,
	answer_hash TEXT NOT NULL,
	state TEXT NOT NULL DEFAULT 'pending',
	confirmations INTEGER NOT NULL DEFAULT 1,
	grounding_score REAL,
	sources_hash TEXT,
	sources_used TEXT,
	file_path TEXT NOT NULL,
	created_at TEXT NOT NULL,
	promoted_at TEXT,
	demoted_at TEXT
);

CREATE INDEX IF NOT EXISTS idx_pending_outputs_question_hash
	ON pending_outputs(question_hash);
CREATE INDEX IF NOT EXISTS idx_pending_outputs_state
	ON pending_outputs(state);

CREATE TABLE IF NOT EXISTS confirmation_sources (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	output_id TEXT NOT NULL REFERENCES pending_outputs(id),
	chunk_ids TEXT NOT NULL,
	answer_hash TEXT NOT NULL,
	confirmed_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_confirmation_sources_output
	ON confirmation_sources(output_id);

CREATE TABLE IF NOT EXISTS pending_questions_vec (
	question_hash TEXT PRIMARY KEY,
	embedding BLOB NOT NULL,
	dimensions INTEGER NOT NULL
);
`
