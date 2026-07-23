package store

import "errors"

var (
	// ErrReadOnly is returned by write paths on a ModeReader backend.
	ErrReadOnly = errors.New("storage: backend opened read-only")
	// ErrWriterActive is returned at writer open when another writer process
	// holds this vault's lock (advisory lock on postgres; sqlite reports its
	// own busy/locked error instead).
	ErrWriterActive = errors.New("storage: another sage-wiki writer process holds this vault")
	// ErrSchemaVersion is returned by reader opens when schema_version differs
	// from the binary's expected version ("run any writer command once").
	ErrSchemaVersion = errors.New("storage: schema version mismatch — run any writer command once")
	// ErrDimensionMismatch is returned at open when storage.vector_dimension
	// disagrees with the existing vector columns.
	ErrDimensionMismatch = errors.New("storage: vector dimension mismatch")
	// ErrNotFound is the shared not-found sentinel. Note: VectorStore.Get is an
	// exception by legacy contract — it returns (nil, nil) for absent vectors.
	ErrNotFound = errors.New("storage: not found")
)
