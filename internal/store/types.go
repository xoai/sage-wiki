// Package store defines the storage backend seam (P2-1): interfaces, shared
// types, and error sentinels. It imports concrete type packages (memory,
// vectors, ontology, trust, compiler) for method signatures but NEVER imports
// backend implementations (storage, sqlitestore, storage/postgres, storedial)
// or linter — that direction would close an import cycle.
package store

import (
	"context"
	"database/sql"
	"time"
)

// Mode selects writer vs reader open semantics (spec §2).
type Mode int

const (
	// ModeWriter is the default: migrations run, advisory locks taken (postgres),
	// write paths enabled.
	ModeWriter Mode = iota
	// ModeReader: no lock, no migrations, schema_version verified read-only,
	// write paths fail with ErrReadOnly, WriteDB() returns nil.
	ModeReader
)

// PoolConfig sizes the database/sql pool. Owned by this package; config
// carries its own yaml struct and converts in storedial.
type PoolConfig struct {
	MaxOpen int
	MaxIdle int
}

// OpenOptions parameterizes a backend open.
type OpenOptions struct {
	Mode            Mode
	ProjectDir      string
	LockTimeout     time.Duration // default 5s when zero
	Pool            PoolConfig    // default 10/2 when zero; hub reader opens use 4/2
	VectorDimension int           // required for postgres writer open
	ANN             bool          // opt-in HNSW vector index (P2-7); false = brute-force
	VectorBackend   string        // SPEC-06: ""|"memory" (default) | "mmap"
	// Ontology validation lists (config-derived) for the concrete ontology store.
	ValidRelations   []string
	ValidEntityTypes []string
	// TemporalEnabled gates P3-6 bi-temporal edge behavior (validity
	// filtering, supersession). *bool so nil means the default (enabled) — a
	// plain bool would silently DISABLE filtering in every unset literal
	// (OpenConcrete, test/hub opens), inverting the spec default.
	TemporalEnabled *bool
	// Now is the artifact clock (SPEC-04 D4). Compile paths pass
	// config.NowUTC (SOURCE_DATE_EPOCH-aware) so DB bytes are reproducible;
	// nil keeps the wall clock, which is only acceptable on opens that never
	// serve the compile path.
	Now func() time.Time
}

// Learning mirrors the learnings table (and linter.Learning) for
// LearningStore; defined here so store does not import linter.
type Learning struct {
	ID         string // output only; Store derives it via LearningID
	Type       string
	Content    string
	Tags       string
	CreatedAt  string
	SourcePass string
}

// LearningStore wraps the learnings table (linter's persistence).
type LearningStore interface {
	Store(l Learning) error
	List() ([]Learning, error)
	Recall(query string, limit int) ([]Learning, error)
	Prune() (int, error)
}

// Tx is a write transaction from BeginWrite. Commit/Rollback release the
// write mutex exactly once (delegate functions provided by the backend).
// Tx-scoped work must not call WriteTx (the mutex is not reentrant).
type Tx struct {
	*sql.Tx
	commit   func() error
	rollback func() error
}

// NewTx wraps a raw tx with releasing Commit/Rollback delegates.
func NewTx(tx *sql.Tx, commit, rollback func() error) *Tx {
	return &Tx{Tx: tx, commit: commit, rollback: rollback}
}

// Commit commits and releases the write mutex.
func (t *Tx) Commit() error { return t.commit() }

// Rollback rolls back and releases the write mutex.
func (t *Tx) Rollback() error { return t.rollback() }

// DBHandle is the minimal database handle the concrete (sqlite) store
// implementations need — satisfied by *storage.DB, by Backend, and by the
// postgres backend. ReadDB/WriteDB are database/sql handles (the D1
// portability substrate); WriteDB may be nil in reader mode.
type DBHandle interface {
	WriteTx(fn func(tx *sql.Tx) error) error
	BeginWrite() (*Tx, error)
	ReadDB() *sql.DB
	WriteDB() *sql.DB
}

// WriteTxer is the tx-only half of DBHandle, for consumers that never query.
type WriteTxer interface {
	WriteTx(fn func(tx *sql.Tx) error) error
}

// Backend is the aggregate storage seam. sqlite and postgres implementations
// live in their own packages; consumers see only this.
type Backend interface {
	Entries() EntryStore
	Chunks() ChunkStore
	Vectors() VectorStore
	Ontology() OntologyStore
	Communities() CommunityStore
	Trust() TrustStore
	CompileItems() CompileItemStore
	OutputIndex() OutputIndexStore
	Learnings() LearningStore

	// WriteTx runs fn in a serialized write transaction.
	WriteTx(fn func(tx *sql.Tx) error) error
	// BeginWrite acquires the write mutex and begins a long-lived write tx
	// (reembed). Commit/Rollback releases the mutex; tx-scoped work must not
	// call WriteTx (non-reentrant).
	BeginWrite() (*Tx, error)

	// Transitional escape hatches (spec §6) — no consumer outside store
	// packages may call them after the D3 moves; WriteDB returns nil in
	// ModeReader.
	ReadDB() *sql.DB
	WriteDB() *sql.DB

	Health(ctx context.Context) error
	// SchemaReady reports whether the compile_items table exists (probe
	// semantics, NOT a version check — status.go:211 parity).
	SchemaReady() bool
	// Location returns a display-safe identifier: file path, or host/db.
	Location() string
	Close() error
}
