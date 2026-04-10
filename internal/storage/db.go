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

	migrations := []string{
		migrationV1,
		migrationV2,
	}

	for i := version; i < len(migrations); i++ {
		log.Info("running migration", "version", i+1)
		tx, err := db.write.Begin()
		if err != nil {
			return fmt.Errorf("migration v%d: begin: %w", i+1, err)
		}
		if _, err := tx.Exec(migrations[i]); err != nil {
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
