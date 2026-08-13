// Package postgres implements the store.Backend seam over PostgreSQL +
// pgvector (P2-1). Dialect rules per spec §5: $N placeholders, ON CONFLICT
// spellings, now() for datetime('now'), sage_fts tsvector/tsquery for FTS5,
// to_char quirk reproduction for the demotion query, empty-string↔NULL
// mappings for optional RFC3339 columns, per-column-family timestamp
// rendering, one statement per Exec in migrations (pgx stdlib rejects
// multi-statement).
//
// Concurrency (design D9): process-local writeMu on every write tx +
// session advisory lock (writer open, pinned conn) + per-tx advisory xact
// lock with SET LOCAL lock_timeout — identical effective serialization to
// the sqlite single-writer world. Readers take no lock and no migrations.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	pgvector "github.com/pgvector/pgvector-go/pgx"

	"github.com/xoai/sage-wiki/internal/store"
)

// advisoryKey derives the int64 advisory-lock key for scope+database
// (design D9.2: session and tx keys are NEVER shared).
func advisoryKey(scope, dbName string) int64 {
	h := fnv.New64a()
	h.Write([]byte("sage-wiki:" + scope + ":" + dbName))
	return int64(h.Sum64())
}

type backend struct {
	pool      *sql.DB
	mode      store.Mode
	opts      store.OpenOptions
	lockCon   *sql.Conn // pinned advisory-lock conn (writer only)
	writeMu   sync.Mutex
	session   int64
	txKey     int64
	host      string
	dbName    string
	closeOnce sync.Once

	// Alias-derived edges (decision-035). On the backend, not the store:
	// Ontology() returns a fresh &ontologyStore{} per call, so a per-store flag
	// would re-probe on every construction and never be shared.
	derivedMu  sync.RWMutex
	hasDerived bool
	probedAt   time.Time // zero = never probed
}

var _ store.Backend = (*backend)(nil)

// Open dials the DSN and opens the backend. Writer mode: pinned-conn session
// advisory lock (try-lock polled at 100ms until LockTimeout, then
// ErrWriterActive) + migrations. Reader mode: no lock, no migrations,
// schema_version verified.
func Open(dsn string, opts store.OpenOptions) (store.Backend, error) {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("storage: parse dsn: %w", err)
	}
	pool := stdlib.OpenDB(*cfg, stdlib.OptionAfterConnect(func(ctx context.Context, conn *pgx.Conn) error {
		if err := pgvector.RegisterTypes(ctx, conn); err != nil {
			return fmt.Errorf("storage: register vector types: %w", err)
		}
		if _, err := conn.Exec(ctx, "SET TIME ZONE 'UTC'"); err != nil {
			return fmt.Errorf("storage: set timezone: %w", err)
		}
		return nil
	}))
	maxOpen, maxIdle := opts.Pool.MaxOpen, opts.Pool.MaxIdle
	if maxOpen <= 0 {
		maxOpen = 10
	}
	if maxIdle <= 0 {
		maxIdle = 2
	}
	pool.SetMaxOpenConns(maxOpen)
	pool.SetMaxIdleConns(maxIdle)

	lt := opts.LockTimeout
	if lt <= 0 {
		lt = 5 * time.Second
	}
	opts.LockTimeout = lt

	// Key scope: database + schema (search_path) — vaults sharing one
	// database as schemas must not serialize against each other.
	keyScope := cfg.Database
	if sp := cfg.RuntimeParams["search_path"]; sp != "" {
		keyScope += "." + sp
	}
	b := &backend{
		pool: pool, mode: opts.Mode, opts: opts,
		session: advisoryKey("session", keyScope),
		txKey:   advisoryKey("tx", keyScope),
		host:    cfg.Host, dbName: cfg.Database,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		if strings.Contains(err.Error(), "vector type not found") {
			return nil, fmt.Errorf("storage: pgvector extension missing — run CREATE EXTENSION vector (requires superuser or rds_superuser): %w", err)
		}
		return nil, fmt.Errorf("storage: connect: %w", err)
	}

	if opts.Mode == store.ModeReader {
		if err := b.verifySchemaVersion(ctx); err != nil {
			pool.Close()
			return nil, err
		}
		return b, nil
	}

	// Writer: session advisory lock on a pinned conn.
	if err := b.acquireSessionLock(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	if opts.VectorDimension <= 0 {
		b.Close()
		return nil, fmt.Errorf("storage: vector_dimension required when backend=postgres")
	}
	if err := b.migrate(ctx); err != nil {
		b.Close()
		return nil, fmt.Errorf("storage: migrate: %w", err)
	}
	if err := b.verifyDimension(ctx, opts.VectorDimension); err != nil {
		b.Close()
		return nil, err
	}
	return b, nil
}

// acquireSessionLock polls pg_try_advisory_lock every 100ms until
// LockTimeout, then fails with ErrWriterActive naming both causes
// (live second writer; crashed writer not yet reaped).
func (b *backend) acquireSessionLock(ctx context.Context) error {
	conn, err := b.pool.Conn(ctx)
	if err != nil {
		return fmt.Errorf("storage: lock conn: %w", err)
	}
	deadline := time.Now().Add(b.opts.LockTimeout)
	for {
		var got bool
		if err := conn.QueryRowContext(ctx, "SELECT pg_try_advisory_lock($1)", b.session).Scan(&got); err != nil {
			conn.Close()
			return fmt.Errorf("storage: advisory lock: %w", err)
		}
		if got {
			b.lockCon = conn
			return nil
		}
		if time.Now().After(deadline) {
			conn.Close()
			return fmt.Errorf("%w — another sage-wiki writer process holds this vault's lock "+
				"(stop it, point this process elsewhere, or if a writer crashed: inspect pg_locks and "+
				"pg_terminate_backend the stale session)", store.ErrWriterActive)
		}
		select {
		case <-ctx.Done():
			conn.Close()
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// verifySchemaVersion (reader mode): the DB must already be at this binary's
// schema version — readers never migrate.
func (b *backend) verifySchemaVersion(ctx context.Context) error {
	var version int
	err := b.pool.QueryRowContext(ctx,
		"SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return fmt.Errorf("%w (no schema_version — run any writer command once)", store.ErrSchemaVersion)
	}
	if version != currentSchemaVersion {
		return fmt.Errorf("%w (have %d, want %d)", store.ErrSchemaVersion, version, currentSchemaVersion)
	}
	return nil
}

// verifyDimension checks all three vector columns agree with config.
func (b *backend) verifyDimension(ctx context.Context, dim int) error {
	want := fmt.Sprintf("vector(%d)", dim)
	for _, table := range []string{"vec_entries", "vec_chunks", "pending_questions_vec"} {
		var typ string
		err := b.pool.QueryRowContext(ctx,
			`SELECT format_type(atttypid, atttypmod) FROM pg_attribute
			 WHERE attrelid = $1::regclass AND attname = 'embedding'`, table).Scan(&typ)
		if err != nil {
			return fmt.Errorf("storage: inspect %s.embedding: %w", table, err)
		}
		if typ != want {
			return fmt.Errorf("%w: %s.embedding is %s, config wants %s — remedy: drop and recreate the "+
				"vector tables, invalidate output_index (forces reconcile), re-embed (docs/guides/storage-backends.md)",
				store.ErrDimensionMismatch, table, typ, want)
		}
	}
	return nil
}

// WriteTx runs fn in a write tx: process-local mutex + SET LOCAL
// lock_timeout + pg_advisory_xact_lock(txKey) first (design D9).
func (b *backend) WriteTx(fn func(tx *sql.Tx) error) error {
	if b.mode == store.ModeReader {
		return store.ErrReadOnly
	}
	b.writeMu.Lock()
	defer b.writeMu.Unlock()

	tx, err := b.beginLockedTx()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (b *backend) beginLockedTx() (*sql.Tx, error) {
	tx, err := b.pool.Begin()
	if err != nil {
		return nil, fmt.Errorf("storage: begin: %w", err)
	}
	if _, err := tx.Exec(fmt.Sprintf("SET LOCAL lock_timeout = '%dms'", b.opts.LockTimeout.Milliseconds())); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("storage: set lock_timeout: %w", err)
	}
	if _, err := tx.Exec("SELECT pg_advisory_xact_lock($1)", b.txKey); err != nil {
		tx.Rollback()
		return nil, fmt.Errorf("storage: xact advisory lock: %w", err)
	}
	return tx, nil
}

// BeginWrite holds writeMu for the tx duration (reembed parity — design
// D9.3); Commit/Rollback releases. Tx-scoped work must not call WriteTx.
func (b *backend) BeginWrite() (*store.Tx, error) {
	if b.mode == store.ModeReader {
		return nil, store.ErrReadOnly
	}
	b.writeMu.Lock()
	var once sync.Once
	release := func() { once.Do(func() { b.writeMu.Unlock() }) }

	tx, err := b.beginLockedTx()
	if err != nil {
		release()
		return nil, err
	}
	return store.NewTx(tx,
		func() error { err := tx.Commit(); release(); return err },
		func() error { err := tx.Rollback(); release(); return err }), nil
}

func (b *backend) ReadDB() *sql.DB { return b.pool }

func (b *backend) WriteDB() *sql.DB {
	if b.mode == store.ModeReader {
		return nil
	}
	return b.pool
}

func (b *backend) Health(ctx context.Context) error {
	if err := b.pool.PingContext(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	var version string
	if err := b.pool.QueryRowContext(ctx, "SELECT version()").Scan(&version); err != nil {
		return fmt.Errorf("version: %w", err)
	}
	return nil
}

func (b *backend) SchemaReady() bool {
	var ready bool
	err := b.pool.QueryRow("SELECT to_regclass('compile_items') IS NOT NULL").Scan(&ready)
	return err == nil && ready
}

// Location returns host/database — never credentials.
func (b *backend) Location() string { return b.host + "/" + b.dbName }

// Close releases the pinned advisory-lock conn BEFORE closing the pool
// (deterministic — a racing opener never sees a stale lock, design D9.2).
func (b *backend) Close() error {
	var err error
	b.closeOnce.Do(func() {
		if b.lockCon != nil {
			// Session lock releases with the conn.
			if cerr := b.lockCon.Close(); cerr != nil {
				err = cerr
			}
		}
		if perr := b.pool.Close(); perr != nil && err == nil {
			err = perr
		}
	})
	return err
}

// --- store accessors (files per store) ---

func (b *backend) Entries() store.EntryStore            { return &entryStore{b: b} }
func (b *backend) Chunks() store.ChunkStore             { return &chunkStore{b: b} }
func (b *backend) Vectors() store.VectorStore           { return &vectorStore{b: b} }
func (b *backend) Ontology() store.OntologyStore        { return &ontologyStore{b: b} }
func (b *backend) Communities() store.CommunityStore    { return &communityStore{b: b} }
func (b *backend) Trust() store.TrustStore              { return &trustStore{b: b} }
func (b *backend) CompileItems() store.CompileItemStore { return &itemStore{b: b} }
func (b *backend) OutputIndex() store.OutputIndexStore  { return &outputIndexStore{b: b} }
func (b *backend) Learnings() store.LearningStore       { return &learningStore{b: b} }
