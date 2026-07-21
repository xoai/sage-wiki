package store

import (
	"database/sql"
	"time"

	"github.com/xoai/sage-wiki/internal/compiler"
	"github.com/xoai/sage-wiki/internal/memory"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/trust"
	"github.com/xoai/sage-wiki/internal/vectors"
)

// Interfaces declared here match TODAY's concrete method sets exactly
// (interface-growth rule: additions enter in the same task as their
// implementations — spec §3, plan T4/T7/T8).

type EntryStore interface {
	Add(e memory.Entry) error
	Update(e memory.Entry) error
	Delete(id string) error
	Get(id string) (*memory.Entry, error)
	Search(query string, tags []string, limit int) ([]memory.SearchResult, error)
	Count() (int, error)
}

type ChunkStore interface {
	IndexChunks(tx *sql.Tx, docID string, chunks []memory.ChunkEntry) error
	DeleteDocChunks(tx *sql.Tx, docID string) error
	SearchChunks(query string, limit int) ([]memory.ChunkResult, error)
	SearchChunksMultiQuery(queries []string, limit int) ([]memory.ChunkResult, error)
	Count() (int, error)
	NeedsBackfill(memStore *memory.Store) bool // pre-T7 concrete signature
}

type VectorStore interface {
	Upsert(id string, embedding []float32) error
	Get(id string) ([]float32, error) // (nil, nil) when absent — legacy contract
	Delete(id string) error
	Search(query []float32, limit int) ([]vectors.VectorResult, error)
	UpsertChunk(tx *sql.Tx, chunkID string, docID string, embedding []float32) error
	SearchChunks(query []float32, limit int) ([]vectors.ChunkVectorResult, error)
	SearchChunksFiltered(query []float32, docIDs []string, limit int) ([]vectors.ChunkVectorResult, error)
	DeleteDocChunkVectors(docID string) error
	HasChunkVectors(docID string) (bool, error)
	InvalidateChunkCache()
	Count() (int, error)
	Dimensions() (int, error)
}

type OntologyStore interface {
	IsValidType(t string) bool
	AddEntity(e ontology.Entity) error
	UpdateEntity(e ontology.Entity) error
	GetEntity(id string) (*ontology.Entity, error)
	ListEntities(entityType string) ([]ontology.Entity, error)
	ListRelations(relationType string, limit int) ([]ontology.Relation, error)
	DeleteEntity(id string) error
	AddRelation(r ontology.Relation) error
	GetRelations(entityID string, direction ontology.Direction, relationType string) ([]ontology.Relation, error)
	Traverse(entityID string, opts ontology.TraverseOpts) ([]ontology.Entity, error)
	DetectCycles(entityID string) ([][]string, error)
	EntityCount(entityType string) (int, error)
	RelationCount() (int, error)
	EntityDegree(id string) (int, error)
	EntitiesCiting(targetID string) ([]ontology.Entity, error)
	CitedBy(entityID string) ([]ontology.Entity, error)
}

type TrustStore interface {
	InsertPending(o *trust.PendingOutput) error
	Get(id string) (*trust.PendingOutput, error)
	ListByState(state trust.OutputState) ([]*trust.PendingOutput, error)
	UpdateGroundingScore(id string, score float64) error
	IncrementConfirmations(id string) error
	SetState(id string, state trust.OutputState) error
	Promote(id string) error
	Demote(id string) error
	UpdateFilePath(id string, filePath string) error
	Delete(id string) error
	IsConfirmed(docID string) bool
	RecordConfirmation(outputID string, chunkIDs string, answerHash string) error
	GetConfirmations(outputID string) ([]*trust.Confirmation, error)
	ListConfirmed() ([]*trust.PendingOutput, error)
	ListByQuestionHash(qHash string) ([]*trust.PendingOutput, error)
	ListOlderThan(cutoff time.Time) ([]*trust.PendingOutput, error)
}

type CompileItemStore interface {
	Upsert(item compiler.CompileItem) error
	GetByPath(path string) (*compiler.CompileItem, error)
	ListByTier(tier int) ([]compiler.CompileItem, error)
	ListPending(tier int) ([]compiler.CompileItem, error)
	MarkPass(path string, pass string) error
	SetTier(path string, tier int, reason string) error
	MarkError(path string, compileErr error) error
	IncrementQueryHits(paths []string) error
	Stats() (*compiler.CompileStats, error)
	DeleteByPaths(paths []string) error
	SetQualityScore(path string, score float64) error
	Count() (int, error)
	ListPromotionCandidates(hitThreshold int) ([]string, error)
	ListDemotionCandidates(staleThreshold string) ([]string, error)
}

type OutputIndexStore interface {
	Get(outputPath string) (hash string, ok bool, err error)
	Set(outputPath, hash string) error
	Delete(outputPath string) error
	All() (map[string]string, error)
	Backfill(outputs map[string][]byte) error
}
