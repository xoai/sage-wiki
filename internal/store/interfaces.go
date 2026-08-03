package store

import (
	"database/sql"
	"time"
)

// Interfaces declared here reference the domain types in domain.go (relocated
// per the D2-prime amendment — this package imports no concrete store
// packages). Interface growth rule: methods enter in the same task as their
// implementations.

type EntryStore interface {
	Add(e Entry) error
	Update(e Entry) error
	Delete(id string) error
	Get(id string) (*Entry, error)
	// GetMany is Get for a result set: one batched round trip instead of
	// N (M5 — per-doc Get was the facade's dominant cost). Missing IDs are
	// absent from the map, exactly as a nil Get result is today.
	GetMany(ids []string) (map[string]*Entry, error)
	Search(query string, tags []string, limit int) ([]SearchResult, error)
	Count() (int, error)
	// T8 additions (D3 moves).
	ListAll() ([]Entry, error)
	CountUncompiled(query string) (int, error)
	// M3 (20260728-search-upgrade, ADR-039): entry_dates sidecar.
	SetSourceDate(id string, ts int64) error
	GetSourceDates(ids []string) (map[string]int64, error)
}

type ChunkStore interface {
	IndexChunks(tx *sql.Tx, docID string, chunks []ChunkEntry) error
	DeleteDocChunks(tx *sql.Tx, docID string) error
	SearchChunks(query string, limit int) ([]ChunkResult, error)
	Count() (int, error)
	NeedsBackfill(memStore Countable) bool
	// T8 additions.
	ListAll() ([]ChunkEntryWithDoc, error)
	// M1 (20260728-search-upgrade): hydration of vector-only chunk hits.
	GetChunksMeta(ids []string) (map[string]ChunkEntry, error)
	// M5: the doc IDs currently carrying chunks — reindex needs them to
	// re-chunk doc families that have no article file on disk (src: docs).
	ListDocIDs() ([]string, error)
}

type VectorStore interface {
	Upsert(id string, embedding []float32) error
	Get(id string) ([]float32, error) // (nil, nil) when absent — legacy contract
	Delete(id string) error
	Search(query []float32, limit int) ([]VectorResult, error)
	UpsertChunk(tx *sql.Tx, chunkID string, docID string, embedding []float32) error
	SearchChunks(query []float32, limit int) ([]ChunkVectorResult, error)
	SearchChunksFiltered(query []float32, docIDs []string, limit int) ([]ChunkVectorResult, error)
	DeleteDocChunkVectors(docID string) error
	HasChunkVectors(docID string) (bool, error)
	InvalidateChunkCache()
	Count() (int, error)
	Dimensions() (int, error)
}

type OntologyStore interface {
	IsValidType(t string) bool
	AddEntity(e Entity) error
	UpdateEntity(e Entity) error
	GetEntity(id string) (*Entity, error)
	ListEntities(entityType string) ([]Entity, error)
	ListRelations(relationType string, limit int) ([]Relation, error)
	DeleteEntity(id string) error
	AddRelation(r Relation) error
	GetRelations(entityID string, direction Direction, relationType string) ([]Relation, error)
	// GetRelationsAt is the point-in-time read (P3-6): only edges live at
	// asOf are returned. A zero asOf behaves exactly like GetRelations
	// (live-at-now when temporal behavior is enabled; unfiltered when the
	// store was built with it disabled).
	GetRelationsAt(entityID string, direction Direction, relationType string, asOf time.Time) ([]Relation, error)
	// InvalidateFunctional supersedes every not-yet-invalidated edge
	// (any ID form of sourceID, predicate, target NOT any ID form of
	// keepTargetID) by setting valid_to/invalidated_by in relations AND
	// derived_relations, in one transaction (P3-6). ID forms close over the
	// applied-alias chain root. valid_to is set per row:
	// max(newValidFrom, old.valid_from) — plain string max over RFC3339.
	// Equality means the winner was not later than the loser: the loser is
	// then live at NO T (a retroactive win), which is what makes winner and
	// loser mutually exclusive at every T. An empty newValidFrom is treated
	// as now. Returns the invalidated edge IDs.
	// No-op (nil, nil) when the store was built with temporal behavior
	// disabled.
	InvalidateFunctional(sourceID, predicate, keepTargetID, newValidFrom, invalidatedBy string) ([]string, error)
	Traverse(entityID string, opts TraverseOpts) ([]Entity, error)
	DetectCycles(entityID string) ([][]string, error)
	EntityCount(entityType string) (int, error)
	RelationCount() (int, error)
	EntityDegree(id string) (int, error)
	EntitiesCiting(targetID string) ([]Entity, error)
	CitedBy(entityID string) ([]Entity, error)
	// T8 additions.
	AllRelations() ([]Relation, error)
	RelationsByType(relationType string) ([]Relation, error)
	EntityConnectionCounts() (map[string]int, error)

	// Entity resolution (P3-3, GRAPH-03). Alias rows key on entity IDs, never
	// names. Nothing here mutates the semantics of the methods above: linking is
	// additive, so every existing caller behaves exactly as before.
	CanonicalID(id string) (string, error)
	PutAlias(a EntityAlias) error
	GetActiveAlias(alias string) (*EntityAlias, error)
	ListAliases(status AliasStatus) ([]EntityAlias, error)
	IsRejected(a, b string) (bool, error)
	SetAliasStatus(alias, canonicalID string, status AliasStatus, decidedBy string) error
	// LinkAlias derives the alias's edges onto the canonical and records the
	// link, in ONE transaction. Non-destructive: nothing is deleted and no
	// edge the canonical asserted itself is overwritten. Derived edges land in
	// derived_relations stamped with the alias that caused them
	// (decision-035), which is what makes UnlinkAlias exact.
	LinkAlias(a EntityAlias) (LinkResult, error)
	// UnlinkAlias reverses a link: it deletes exactly the edges this alias
	// caused and records the pair as rejected, in ONE transaction.
	//
	// The rejection is not optional. resolvableSeeds and applyClusters both
	// gate on GetActiveAlias, so deleting the row alone would make the entity a
	// live seed again and the next compile would re-propose and — at the
	// default auto_apply_threshold of 0.85 — re-apply it. A delete without the
	// status change is a pause, not an undo.
	//
	// It does NOT rebuild transitively derived rows: under A->B->C, rows
	// derived from A's edges but stamped B survive. The caller runs a sweep
	// afterwards, OUTSIDE this transaction — WriteTx is not reentrant.
	UnlinkAlias(alias, canonicalID string) error
	// ClearDerived removes every derived edge. The sweep calls it before
	// replaying the surviving applied links, because replaying alone cannot
	// remove anything and so cannot undo.
	ClearDerived() error
}

// CommunityStore persists detected graph communities (P3-5). Membership is
// derived, rebuilt state — replaced wholesale per detection run.
type CommunityStore interface {
	// ReplaceDetection upserts the new partition in ONE tx: member rows are
	// deleted and reinserted wholesale; each community upserts preserving
	// summary/summary_hash/model UNLESS the incoming member hash differs
	// from the stored one (conditional clear — repurposed IDs can never
	// keep a stale summary); communities absent from the new set are
	// deleted and their IDs returned for artifact cleanup. Tx order:
	// members delete → level-ordered upsert → absent delete → reinsert.
	ReplaceDetection(comms []Community, members map[string][]string) (removed []string, err error)
	ListCommunities(level int) ([]Community, error) // level -1 = all
	CommunityMembers(id string) ([]string, error)
	EntityCommunity(entityID string, level int) (string, error)
	SetSummary(id, summary, summaryHash, model string) error
	MaxLevel() (int, error)
	ClearCommunities() error
}

type TrustStore interface {
	InsertPending(o *PendingOutput) error
	Get(id string) (*PendingOutput, error)
	ListByState(state OutputState) ([]*PendingOutput, error)
	UpdateGroundingScore(id string, score float64) error
	IncrementConfirmations(id string) error
	SetState(id string, state OutputState) error
	Promote(id string) error
	Demote(id string) error
	UpdateFilePath(id string, filePath string) error
	Delete(id string) error
	IsConfirmed(docID string) bool
	RecordConfirmation(outputID string, chunkIDs string, answerHash string) error
	GetConfirmations(outputID string) ([]*Confirmation, error)
	ListConfirmed() ([]*PendingOutput, error)
	ListByQuestionHash(qHash string) ([]*PendingOutput, error)
	ListOlderThan(cutoff time.Time) ([]*PendingOutput, error)
	// Consensus (methods since T7, same names/types — spec §3).
	EmbedAndStoreQuestion(tx *sql.Tx, questionHash string, embedding []float32) error
	FindSimilarQuestion(tx *sql.Tx, questionVec []float32, threshold float64) (*SimilarQuestion, error)
}

type CompileItemStore interface {
	Upsert(item CompileItem) error
	GetByPath(path string) (*CompileItem, error)
	ListByTier(tier int) ([]CompileItem, error)
	ListPending(tier int) ([]CompileItem, error)
	MarkPass(path string, pass string) error
	SetTier(path string, tier int, reason string) error
	MarkError(path string, compileErr error) error
	IncrementQueryHits(paths []string) error
	Stats() (*CompileStats, error)
	DeleteByPaths(paths []string) error
	SetQualityScore(path string, score float64) error
	Count() (int, error)
	ListPromotionCandidates(hitThreshold int) ([]string, error)
	ListDemotionCandidates(staleThreshold string) ([]string, error)
	// T8 additions.
	ListBelowQualityScore(threshold float64) ([]QualityScoreRow, error)
	// Durable queue (P2-3). Claim fences via conditional update — an item
	// whose lease changed underneath is skipped, never double-claimed.
	Claim(tier int, owner string, ttl time.Duration, limit int) ([]CompileItem, error)
	Heartbeat(owner string, paths []string, ttl time.Duration) error
	Release(path string, owner string, outcome ReleaseOutcome) error
	RequeueExpired(now time.Time) (int, error)
	ResetFailed() (int, error)
	// SPEC-04 compile-key dedup.
	SetCompileKey(path, key, partsJSON string) error
	ClearCompileKey(path string) error
	// InvalidatePasses zeroes every pass flag so the doc recompiles
	// (key-drift and --force resets). Queue state is untouched.
	InvalidatePasses(path string) error
}

type OutputIndexStore interface {
	Get(outputPath string) (hash string, ok bool, err error)
	Set(outputPath, hash string) error
	Delete(outputPath string) error
	All() (map[string]string, error)
	Backfill(outputs map[string][]byte) error
	// Tx variants for atomic writes with the index rows they certify.
	SetTx(tx *sql.Tx, outputPath, hash string) error
	DeleteTx(tx *sql.Tx, outputPath string) error
}
