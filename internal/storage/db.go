package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
	_ "modernc.org/sqlite"
)

// DB manages SQLite connections with WAL mode and single-writer pattern.
type DB struct {
	write     *sql.DB
	read      *sql.DB
	writeMu   sync.Mutex
	closeOnce sync.Once
}

// Open creates a new DB connection to the given path.
// It enables WAL mode, foreign keys, and busy timeout.
// The parent directory is created if it doesn't exist.
func Open(path string) (*DB, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("storage.Open: create parent dir: %w", err)
		}
	}

	// Write connection. _txlock=immediate reserves SQLite's sole writer at
	// BEGIN (spec R1): a deferred begin only takes a read snapshot, so a
	// second handle's transaction can enter, write, and commit first, and the
	// paused handle's later snapshot upgrade fails BUSY_SNAPSHOT — exactly
	// the cross-handle contention class seen in hosted CI. Waiting at begin
	// is safer than failing mid-callback: transaction callbacks are never
	// replayed.
	writeDB, err := sql.Open("sqlite", path+"?_txlock=immediate")
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
		// SPEC-04 D7: zero freed-cell content on delete/update. Without it,
		// overwritten values (e.g. a released lease's pid+nonce owner token)
		// survive as garbage bytes in pages, and two logically-identical DBs
		// still differ at the byte level — the last blocker for AC-1's
		// byte-identical wiki.db. Cost is minor write amplification.
		"PRAGMA secure_delete=ON",
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
	if db.write == nil {
		return errors.New("storage.WriteTx: read-only database")
	}
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
		if db.read != nil {
			if err := db.read.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if db.write != nil {
			if err := db.write.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if len(errs) > 0 {
			closeErr = fmt.Errorf("storage.Close: %v", errs)
		}
	})
	return closeErr
}

// migration is one schema migration step.
type migration struct {
	sql       string
	disableFK bool // run PRAGMA foreign_keys=OFF before tx, restore after
}

// schemaMigrations is the append-only V-series. CurrentSchemaVersion tracks it.
var schemaMigrations = []migration{
	{sql: migrationV1},
	{sql: migrationV2},
	{sql: migrationV3},
	{sql: migrationV4, disableFK: true},
	{sql: migrationV5},
	{sql: migrationV6},
	{sql: migrationV7},
	{sql: migrationV8},
	{sql: migrationV9},
	{sql: migrationV10},
	{sql: migrationV11},
	{sql: migrationV12},
	{sql: migrationV13},
	{sql: migrationV14},
	{sql: migrationV15},
}

// migrationV15 adds the SPEC-04 compile-key columns: content-addressed
// dedup state per source doc. Additive — old binaries tolerate the columns
// (all reads/writes use explicit column lists), and empty keys mark
// pre-SPEC-04 rows for the adoption path.
const migrationV15 = `
ALTER TABLE compile_items ADD COLUMN compile_key TEXT NOT NULL DEFAULT '';
ALTER TABLE compile_items ADD COLUMN compile_key_parts TEXT NOT NULL DEFAULT '';
`

// migrationV14 adds graph community storage (P3-5): detected communities
// plus their entity membership. Membership is derived, rebuilt state —
// replaced wholesale per detection run — so there is no data migration.
const migrationV14 = `
CREATE TABLE IF NOT EXISTS communities (
	id           TEXT PRIMARY KEY,
	level        INTEGER NOT NULL,
	parent_id    TEXT,
	member_count INTEGER NOT NULL DEFAULT 0,
	edge_count   INTEGER NOT NULL DEFAULT 0,
	summary      TEXT,
	summary_hash TEXT,
	model        TEXT,
	updated_at   TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS community_members (
	community_id TEXT NOT NULL REFERENCES communities(id) ON DELETE CASCADE,
	entity_id    TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	level        INTEGER NOT NULL,
	PRIMARY KEY (community_id, entity_id)
);
`

// migrationV13 adds the entry_dates sidecar (20260728-search-upgrade M3,
// ADR-039). A sidecar because entries is an FTS5 virtual table that cannot
// take ALTER TABLE ADD COLUMN, and a rebuild (the V8 precedent) re-indexes
// the whole corpus; the sidecar is purely additive and leaves BM25 term
// statistics untouched. source_date is a unix timestamp meaning "when the
// knowledge originated" (source frontmatter date > ingest mtime > manifest
// first-seen; Q&A outputs stamp creation time) — NEVER a row/compile
// timestamp. A missing row means "no date": no recency contribution, no
// date emitted.
const migrationV13 = `
CREATE TABLE IF NOT EXISTS entry_dates (
	id          TEXT PRIMARY KEY,
	source_date INTEGER NOT NULL
);
`

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

	migrations := schemaMigrations

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

-- NB (decision-035): this rebuild reads the relations table only, and must keep
-- doing so. derived_relations holds alias-derived copies; folding them in here
-- would launder them into originals and make un-link impossible again.
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

// migrationV7 adds the output_index table: the content hash of each compiled
// output file (summary/article) the system believes is fully indexed. It is the
// reconciler's comparand for the "changed output file" drift case (D5) —
// detected against this indexed-output record, NOT the manifest input hash. A
// dedicated table (rather than a column on entries/compile_items) covers every
// output uniformly, including MCP-authored concept articles that have no
// compile_items row, and the FTS5 entries table cannot take an ALTER ADD COLUMN.
const migrationV7 = `
CREATE TABLE IF NOT EXISTS output_index (
	output_path  TEXT PRIMARY KEY,
	content_hash TEXT NOT NULL,
	indexed_at   TEXT NOT NULL DEFAULT (datetime('now'))
);
`

// migrationV8 adds FTS5 prefix indexes (prefix='2 3') to entries and
// chunks_fts so buildFTSQuery's "term"* prefix queries are index-backed
// instead of full index scans (PERF-02, P1-5). FTS5 virtual tables can't
// ALTER ADD options, so the tables are recreated with the same columns,
// tokenizer, and the chunk_id UNINDEXED property (dropping it would
// pollute BM25 rankings with chunk IDs), then refilled from the old
// tables — the same CREATE _new → INSERT OR IGNORE SELECT → DROP → RENAME
// pattern V2/V4 used. One-time rebuild cost at open; the runner's single
// transaction makes it atomic.
const migrationV8 = `
CREATE VIRTUAL TABLE entries_new USING fts5(
	id, content, tags, article_path,
	tokenize='porter unicode61',
	prefix='2 3'
);
INSERT OR IGNORE INTO entries_new (id, content, tags, article_path)
	SELECT id, content, tags, article_path FROM entries;
DROP TABLE entries;
ALTER TABLE entries_new RENAME TO entries;

CREATE VIRTUAL TABLE chunks_fts_new USING fts5(
	chunk_id UNINDEXED,
	heading, content,
	tokenize='porter unicode61',
	prefix='2 3'
);
INSERT OR IGNORE INTO chunks_fts_new (chunk_id, heading, content)
	SELECT chunk_id, heading, content FROM chunks_fts;
DROP TABLE chunks_fts;
ALTER TABLE chunks_fts_new RENAME TO chunks_fts;
`

// migrationV9 promotes compile_items into a durable work queue (P2-3):
// claim columns (status/lease_owner/lease_until/heartbeat_at/attempts)
// plus the claim index. Backfill is per-tier: only rows whose passes are
// complete for their tier become 'done' (a tier-3 row missing
// pass_embedded stays pending even if all three tier-3 passes are set —
// it still owes the embed pass). Additive only; pre-V9 rows are
// indistinguishable from new ones after backfill.
const migrationV9 = `
ALTER TABLE compile_items ADD COLUMN status TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE compile_items ADD COLUMN lease_owner TEXT;
ALTER TABLE compile_items ADD COLUMN lease_until TEXT;
ALTER TABLE compile_items ADD COLUMN heartbeat_at TEXT;
ALTER TABLE compile_items ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_ci_claim ON compile_items(status, lease_until);

UPDATE compile_items SET status = 'done' WHERE
	(tier = 0 AND pass_indexed = 1) OR
	(tier = 1 AND pass_indexed = 1 AND pass_embedded = 1) OR
	(tier = 2 AND pass_indexed = 1 AND pass_embedded = 1 AND pass_parsed = 1) OR
	(tier = 3 AND pass_indexed = 1 AND pass_embedded = 1
		AND pass_summarized = 1 AND pass_extracted = 1 AND pass_written = 1);
`

// migrationV10 gives relations evidence and provenance (P3-1, GRAPH-01):
// the source span supporting the edge, extractor confidence, and the
// originating document. Plain ADD COLUMN — no table rebuild, so no FK dance
// (contrast V4). All columns are nullable: pre-V10 rows read back as zero
// values through the COALESCE in relationCols, and the pre-V10 five-column
// INSERT would still succeed.
//
// valid_from/valid_to/invalidated_by are RESERVED FOR P3-6 (temporal
// validity). They are written on INSERT and returned by every read, but
// nothing populates them and no query filters on them until P3-6 lands —
// they are added now so bi-temporal edges do not need a second migration.
// They are deliberately TEXT rather than a timestamp type on both backends:
// P3-6 will source them from document frontmatter dates (commonly
// "2026-01-15"), and Postgres's nullRFC silently NULLs anything that is not
// RFC3339. Verbatim TEXT keeps the two backends byte-identical until P3-6
// defines the format contract.
const migrationV10 = `
ALTER TABLE relations ADD COLUMN evidence TEXT;
ALTER TABLE relations ADD COLUMN confidence REAL;
ALTER TABLE relations ADD COLUMN source_doc TEXT;
ALTER TABLE relations ADD COLUMN valid_from TEXT;
ALTER TABLE relations ADD COLUMN valid_to TEXT;
ALTER TABLE relations ADD COLUMN invalidated_by TEXT;
`

// migrationV11 adds the entity-alias table for entity resolution (P3-3,
// GRAPH-03). Pure addition — a new table and three indexes, no rebuild, so no
// FK dance (contrast V4).
//
// The key is (alias, canonical_id), NOT (alias) as the upstream spec proposed.
// A single-column key cannot hold rejections: rejecting
// "Armstrong (musician) -> Armstrong (astronaut)" would occupy the alias's only
// row and block it from ever resolving to "Louis Armstrong". Rejections must
// accumulate freely, so they are keyed by pair.
//
// idx_entity_aliases_active is what enforces "at most one live decision per
// alias" — partial on status, so any number of 'rejected' rows may coexist with
// the one 'applied'/'pending' row. It is a HARD constraint: a second active row
// is a non-target unique violation, which an ON CONFLICT (alias, canonical_id)
// clause does NOT absorb, so it aborts the enclosing transaction. Callers guard
// against reaching it rather than relying on the upsert to swallow it.
//
// No foreign keys, deliberately. --prune (compiler/pipeline.go) and reconcile
// delete entity rows without consulting this table; an FK would either block
// them or CASCADE the audit trail away. A row whose endpoints no longer exist is
// a fact to report, not one to erase.
//
// Timestamps are TEXT on both backends and bound as raw strings — see the
// Postgres v4 note; nullRFC would round-trip them through time.Time and break
// byte parity.
const migrationV11 = `
CREATE TABLE IF NOT EXISTS entity_aliases (
	alias        TEXT NOT NULL,
	canonical_id TEXT NOT NULL,
	entity_type  TEXT NOT NULL,
	status       TEXT NOT NULL,
	confidence   REAL,
	reason       TEXT,
	source       TEXT NOT NULL,
	created_at   TEXT NOT NULL,
	decided_at   TEXT,
	decided_by   TEXT,
	PRIMARY KEY (alias, canonical_id)
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_entity_aliases_active
	ON entity_aliases(alias) WHERE status IN ('applied','pending');
CREATE INDEX IF NOT EXISTS idx_entity_aliases_canonical ON entity_aliases(canonical_id);
CREATE INDEX IF NOT EXISTS idx_entity_aliases_status    ON entity_aliases(status);
`

// migrationV12 adds derived_relations (decision-035). LinkAlias used to copy an
// alias's edges into `relations`, where a copy is indistinguishable from an
// original — which is why a link could never be undone: "which alias put this
// row here?" has no answer in the data. Derived edges now live here, each
// stamped with the alias that caused it, so un-link is a delete by cause.
//
// `id` carries the same copiedRelationID a copy carries today. It is NOT a
// primary key: two aliases may legitimately derive the same edge, and
// un-linking one must leave the other's row. The primary key is therefore
// (alias_id, source_id, target_id, relation) — idempotent per alias, permissive
// across aliases. A read returns one row for such an edge, so this table is not
// 1:1 with what a read returns.
//
// ON DELETE CASCADE mirrors `relations`: a pruned entity must take its derived
// edges with it. There is deliberately no FK to entity_aliases — prune and
// reconcile delete entities without consulting it, and an FK there would turn a
// prune into a failure.
const migrationV12 = `
CREATE TABLE IF NOT EXISTS derived_relations (
	alias_id       TEXT NOT NULL,
	id             TEXT NOT NULL,
	source_id      TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id      TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	relation       TEXT NOT NULL,
	created_at     TEXT,
	evidence       TEXT,
	confidence     REAL,
	source_doc     TEXT,
	valid_from     TEXT,
	valid_to       TEXT,
	invalidated_by TEXT,
	PRIMARY KEY (alias_id, source_id, target_id, relation)
);

-- source/target carry the union's per-arm predicate; alias_id carries un-link.
CREATE INDEX IF NOT EXISTS idx_derived_source ON derived_relations(source_id);
CREATE INDEX IF NOT EXISTS idx_derived_target ON derived_relations(target_id);
CREATE INDEX IF NOT EXISTS idx_derived_alias  ON derived_relations(alias_id);
`
