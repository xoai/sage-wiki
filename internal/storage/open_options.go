package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
)

// OpenOptions controls DB open behavior (P2-1 reader/writer modes).
type OpenOptions struct {
	// ReadOnly skips the write handle and migrations, and verifies the
	// existing schema_version equals CurrentSchemaVersion (reader mode).
	ReadOnly bool
}

// ErrSchemaVersionMismatch reports a reader open against a DB whose
// schema_version differs from this binary's expected version.
var ErrSchemaVersionMismatch = errors.New("storage: schema version mismatch — run any writer command once")

// OpenWithOptions is Open with explicit options; Open(path) is
// OpenWithOptions(path, OpenOptions{}) with identical behavior.
func OpenWithOptions(path string, opts OpenOptions) (*DB, error) {
	if opts.ReadOnly {
		return openReadOnly(path)
	}
	return Open(path)
}

func openReadOnly(path string) (*DB, error) {
	readDB, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("storage.Open: read pool: %w", err)
	}
	readDB.SetMaxOpenConns(4)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := readDB.Exec(pragma); err != nil {
			readDB.Close()
			return nil, fmt.Errorf("storage.Open: read %s: %w", pragma, err)
		}
	}

	var version int
	err = readDB.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		readDB.Close()
		return nil, ErrSchemaVersionMismatch
	}
	if version != CurrentSchemaVersion() {
		readDB.Close()
		return nil, fmt.Errorf("%w (have %d, want %d)", ErrSchemaVersionMismatch, version, CurrentSchemaVersion())
	}

	log.Info("database opened read-only", "path", path)
	return &DB{write: nil, read: readDB}, nil
}

// CurrentSchemaVersion returns the schema version a writer open migrates to.
func CurrentSchemaVersion() int {
	return len(schemaMigrations)
}

// WriteTx wraps *sql.Tx and releases the write mutex on Commit or Rollback
// (exactly once, whichever happens first). Obtained from BeginWrite.
type WriteTx struct {
	*sql.Tx
	mu   *sync.Mutex
	once sync.Once
}

// Commit commits the transaction and releases the write mutex.
func (w *WriteTx) Commit() error {
	err := w.Tx.Commit()
	w.once.Do(func() { w.mu.Unlock() })
	return err
}

// Rollback rolls back the transaction and releases the write mutex.
func (w *WriteTx) Rollback() error {
	err := w.Tx.Rollback()
	w.once.Do(func() { w.mu.Unlock() })
	return err
}

// BeginWrite acquires the write mutex and begins a long-lived write
// transaction (reembed). The mutex is held until the returned WriteTx's
// Commit or Rollback. Tx-scoped work must not call WriteTx (the mutex is
// not reentrant). Errors on a read-only DB.
func (db *DB) BeginWrite() (*WriteTx, error) {
	if db.write == nil {
		return nil, errors.New("storage.BeginWrite: read-only database")
	}
	db.writeMu.Lock()
	tx, err := db.write.Begin()
	if err != nil {
		db.writeMu.Unlock()
		return nil, fmt.Errorf("storage.BeginWrite: %w", err)
	}
	return &WriteTx{Tx: tx, mu: &db.writeMu}, nil
}
