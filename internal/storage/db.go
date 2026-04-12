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

// migrationV5 adds the facts table for structured numeric data from file-extract.
const migrationV5 = `
CREATE TABLE IF NOT EXISTS facts (
	id              INTEGER PRIMARY KEY AUTOINCREMENT,
	source_file     TEXT NOT NULL,
	source_project  TEXT DEFAULT 'local',
	value           TEXT NOT NULL,
	numeric         REAL,
	sign            TEXT DEFAULT 'positive',
	number_type     TEXT,
	certainty       TEXT DEFAULT 'exact',
	entity          TEXT DEFAULT 'unknown',
	entity_type     TEXT,
	period          TEXT DEFAULT 'unknown',
	period_type     TEXT DEFAULT 'unknown',
	semantic_label  TEXT,
	source_location TEXT,
	context_type    TEXT,
	exact_quote     TEXT,
	verified        BOOLEAN DEFAULT 0,
	extraction_method TEXT,
	schema_version  TEXT,
	imported_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
	quote_hash      TEXT,
	UNIQUE(source_file, entity, semantic_label, period, numeric, quote_hash)
);

CREATE INDEX IF NOT EXISTS idx_facts_entity ON facts(entity);
CREATE INDEX IF NOT EXISTS idx_facts_period ON facts(period);
CREATE INDEX IF NOT EXISTS idx_facts_source ON facts(source_file);
CREATE INDEX IF NOT EXISTS idx_facts_label  ON facts(semantic_label);
`
