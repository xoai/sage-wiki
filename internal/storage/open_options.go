package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"sync"

	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/store"
)

// OpenOptions controls DB open behavior (P2-1 reader/writer modes).
type OpenOptions struct {
	// ReadOnly skips the write handle and migrations, and verifies the
	// existing schema_version equals CurrentSchemaVersion (reader mode).
	ReadOnly bool
}

// ErrSchemaVersionMismatch is the backend-neutral schema sentinel (aliased
// to store.ErrSchemaVersion so errors.Is works across backends).
var ErrSchemaVersionMismatch = store.ErrSchemaVersion

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

// BeginWrite acquires the write mutex and begins a long-lived write
// transaction (reembed). The mutex is held until the returned tx's Commit
// or Rollback (store.Tx handles the release, exactly once). Tx-scoped work
// must not call WriteTx (the mutex is not reentrant). Errors on read-only.
func (db *DB) BeginWrite() (*store.Tx, error) {
	if db.write == nil {
		return nil, errors.New("storage.BeginWrite: read-only database")
	}
	db.writeMu.Lock()
	tx, err := db.write.Begin()
	if err != nil {
		db.writeMu.Unlock()
		return nil, fmt.Errorf("storage.BeginWrite: %w", err)
	}
	var once sync.Once
	release := func() { once.Do(func() { db.writeMu.Unlock() }) }
	return store.NewTx(tx,
		func() error { err := tx.Commit(); release(); return err },
		func() error { err := tx.Rollback(); release(); return err }), nil
}
