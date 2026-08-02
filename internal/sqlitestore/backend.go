// Package sqlitestore adapts the existing SQLite concrete stores to the
// store.Backend seam (P2-1). Behavior is identical to direct construction;
// the adapter adds reader/writer modes and the Backend aggregate surface.
package sqlitestore

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/linter"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/storage"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// Options carries the ontology validation lists the concrete ontology store
// needs (config-derived; see ontology.MergedRelations/MergedEntityTypes).
type Options struct {
	ValidRelations   []string
	ValidEntityTypes []string
	ANN              bool   // opt-in HNSW vector index (P2-7)
	TemporalEnabled  *bool  // nil = enabled (P3-6 default); see store.OpenOptions
	VectorBackend    string // SPEC-06: ""|"memory" | "mmap"
}

// temporalEnabledOrDefault resolves the P3-6 gate: nil = enabled (default).
func temporalEnabledOrDefault(p *bool) bool {
	if p == nil {
		return true
	}
	return *p
}

type backend struct {
	db      *storage.DB
	path    string
	mode    store.Mode
	entries *memory.Store
	chunks  *memory.ChunkStore
	vec     *vectors.Store
	ont     *ontology.Store
	trustS  *trust.Store
	items   *compiler.CompileItemStore
	outIdx  *storage.OutputIndex
	learn   *learningStore
}

var _ store.Backend = (*backend)(nil)

// Open opens (or creates) the vault's SQLite DB at
// <projectDir>/.sage/wiki.db. ModeWriter runs migrations (identical to
// storage.Open); ModeReader skips them and verifies the schema version.
func Open(projectDir string, mode store.Mode, o Options) (store.Backend, error) {
	path := filepath.Join(projectDir, ".sage", "wiki.db")
	return OpenPath(path, mode, o)
}

// OpenPath is Open with an explicit DB path.
func OpenPath(path string, mode store.Mode, o Options) (store.Backend, error) {
	var db *storage.DB
	var err error
	if mode == store.ModeReader {
		db, err = storage.OpenWithOptions(path, storage.OpenOptions{ReadOnly: true})
		if err != nil {
			return nil, err
		}
	} else {
		db, err = storage.Open(path)
		if err != nil {
			return nil, err
		}
	}
	return newBackend(db, path, mode, o), nil
}

// Unwrap returns the underlying *storage.DB — TRANSITIONAL bridge for the
// direct-open sites not yet rewired to the Backend (plan T6); grep-forbidden
// outside store packages after T9.
func Unwrap(b store.Backend) *storage.DB {
	if sb, ok := b.(*backend); ok {
		return sb.db
	}
	return nil
}

func newBackend(db *storage.DB, path string, mode store.Mode, o Options) *backend {
	return &backend{
		db:      db,
		path:    path,
		mode:    mode,
		entries: memory.NewStore(db),
		chunks:  memory.NewChunkStore(db),
		vec: vectors.NewStore(db, vectors.WithANN(o.ANN),
			vectors.WithVectorBackend(o.VectorBackend),
			vectors.WithIndexDir(filepath.Dir(path))),
		ont:    ontology.NewStore(db, o.ValidRelations, o.ValidEntityTypes, ontology.WithTemporalEnabled(temporalEnabledOrDefault(o.TemporalEnabled)), ontology.WithNow(config.NowUTC)),
		trustS: trust.NewStore(db),
		items:  compiler.NewCompileItemStore(db, config.NowUTC),
		outIdx: storage.NewOutputIndex(db),
		learn:  &learningStore{db: db},
	}
}

func (b *backend) Entries() store.EntryStore            { return b.entries }
func (b *backend) Chunks() store.ChunkStore             { return b.chunks }
func (b *backend) Vectors() store.VectorStore           { return b.vec }
func (b *backend) Ontology() store.OntologyStore        { return b.ont }
func (b *backend) Communities() store.CommunityStore    { return b.ont }
func (b *backend) Trust() store.TrustStore              { return b.trustS }
func (b *backend) CompileItems() store.CompileItemStore { return b.items }
func (b *backend) OutputIndex() store.OutputIndexStore  { return b.outIdx }
func (b *backend) Learnings() store.LearningStore       { return b.learn }

func (b *backend) WriteTx(fn func(tx *sql.Tx) error) error {
	if b.mode == store.ModeReader {
		return store.ErrReadOnly
	}
	return b.db.WriteTx(fn)
}

func (b *backend) BeginWrite() (*store.Tx, error) {
	if b.mode == store.ModeReader {
		return nil, store.ErrReadOnly
	}
	return b.db.BeginWrite()
}

func (b *backend) ReadDB() *sql.DB  { return b.db.ReadDB() }
func (b *backend) WriteDB() *sql.DB { return b.db.WriteDB() } // nil in reader mode

func (b *backend) Health(ctx context.Context) error {
	if _, err := os.Stat(b.path); err != nil {
		return fmt.Errorf("db file: %w", err)
	}
	if err := b.db.ReadDB().PingContext(ctx); err != nil {
		return fmt.Errorf("ping: %w", err)
	}
	var quick string
	if err := b.db.ReadDB().QueryRowContext(ctx, "PRAGMA quick_check").Scan(&quick); err != nil {
		return fmt.Errorf("quick_check: %w", err)
	}
	if quick != "ok" {
		return fmt.Errorf("quick_check: %s", quick)
	}
	return nil
}

func (b *backend) SchemaReady() bool {
	var name string
	err := b.db.ReadDB().QueryRow(
		"SELECT name FROM sqlite_master WHERE type='table' AND name='compile_items'").Scan(&name)
	return err == nil
}

func (b *backend) Location() string { return b.path }

func (b *backend) Close() error {
	// Release mapped vector index files before the DB under them goes
	// away (F-038: without this, mmap'd indexes stay mapped for process
	// lifetime and LRU-evicted workspaces keep pages resident).
	_ = b.vec.Close()
	return b.db.Close()
}

// learningStore adapts linter's learning persistence to store.LearningStore.
type learningStore struct {
	db *storage.DB
}

var _ store.LearningStore = (*learningStore)(nil)

func (l *learningStore) Store(le store.Learning) error {
	return linter.StoreLearning(l.db, le.Type, le.Content, le.Tags, le.SourcePass)
}

func (l *learningStore) List() ([]store.Learning, error) {
	ls, err := linter.ListLearnings(l.db)
	return convertLearnings(ls), err
}

func (l *learningStore) Recall(query string, limit int) ([]store.Learning, error) {
	ls, err := linter.RecallLearnings(l.db, query, limit)
	return convertLearnings(ls), err
}

func (l *learningStore) Prune() (int, error) {
	return linter.PruneLearnings(l.db)
}

func convertLearnings(ls []linter.Learning) []store.Learning {
	out := make([]store.Learning, len(ls))
	for i, l := range ls {
		out[i] = store.Learning{
			ID: l.ID, Type: l.Type, Content: l.Content,
			Tags: l.Tags, CreatedAt: l.CreatedAt, SourcePass: l.SourcePass,
		}
	}
	return out
}
