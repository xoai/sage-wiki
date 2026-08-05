package store

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// Domain types relocated from the concrete store packages (P2-1 amendment
// "D2-prime"): internal/store must not import memory/vectors/ontology/trust/
// compiler, otherwise those packages can never reference the seam (import
// cycle through sqlitestore). Each original package keeps a type alias, so
// all existing code compiles unchanged and interface satisfaction holds.

// --- memory ---

type Entry struct {
	ID          string
	Content     string
	Tags        []string
	ArticlePath string
	CreatedAt   time.Time
}

type SearchResult struct {
	ID          string
	Content     string
	Tags        []string
	ArticlePath string
	BM25Score   float64
	Rank        int
}

// Countable is the narrow interface ChunkStore.NeedsBackfill depends on.
type Countable interface {
	Count() (int, error)
}

type ChunkEntry struct {
	ChunkID     string
	ChunkIndex  int
	Heading     string
	Content     string
	StartOffset int
	EndOffset   int
}

type ChunkEntryWithDoc struct {
	ChunkID     string
	DocID       string
	ChunkIndex  int
	Heading     string
	Content     string
	StartOffset int
	EndOffset   int
}

type ChunkResult struct {
	ChunkID   string
	DocID     string
	Heading   string
	Content   string
	BM25Score float64
	Rank      int
}

// --- vectors ---

type VectorResult struct {
	ID    string
	Score float64
	Rank  int
}

type ChunkVectorResult struct {
	ChunkID string
	DocID   string
	Score   float64
	Rank    int
}

// --- ontology ---

type Entity struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Name        string `json:"name"`
	Definition  string `json:"definition,omitempty"`
	ArticlePath string `json:"article_path,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type Relation struct {
	ID        string `json:"id"`
	SourceID  string `json:"source_id"`
	TargetID  string `json:"target_id"`
	Relation  string `json:"relation"`
	CreatedAt string `json:"created_at"`

	// P3-1 (GRAPH-01): evidence and provenance.
	//
	// Evidence is the span supporting this edge, quoted from the COMPILED
	// SUMMARY of SourceDoc — not from SourceDoc itself. Pass 2 sees summaries,
	// not source content, so a citation rendered as "«Evidence» — SourceDoc"
	// names a file that does not contain those words. The verifiable artifact
	// is the summary; SourceDoc names where the knowledge came from.
	Evidence   string  `json:"evidence,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	SourceDoc  string  `json:"source_doc,omitempty"`

	// P3-6 (temporal validity): ValidFrom is when the fact became true
	// (writer-populated, RFC3339 UTC, "" = always valid); ValidTo is when it
	// stopped ("" = currently valid); InvalidatedBy names the superseding
	// edge. Default reads filter to live edges; ValidFrom joins the upsert
	// SET list first-writer-wins; ValidTo/InvalidatedBy are written only by
	// InvalidateFunctional.
	ValidFrom     string `json:"valid_from,omitempty"`
	ValidTo       string `json:"valid_to,omitempty"`
	InvalidatedBy string `json:"invalidated_by,omitempty"`
}

// --- ontology: entity resolution (P3-3, GRAPH-03) ---

// AliasStatus is the state of one alias->canonical decision.
type AliasStatus string

const (
	// AliasApplied — the link is in force; the sweep replays it each pass.
	AliasApplied AliasStatus = "applied"
	// AliasPending — proposed, awaiting a human. NOTHING has been linked.
	AliasPending AliasStatus = "pending"
	// AliasRejected — a human said these are different entities. Rejections are
	// keyed by pair and are never re-proposed, in either direction.
	AliasRejected AliasStatus = "rejected"
)

// EntityAlias records that one entity id is a surface-form variant of another.
//
// Alias and CanonicalID hold entity IDs, not names: Entity.ID != Entity.Name for
// every entity the compiler writes, and two rows can share a Name.
type EntityAlias struct {
	Alias       string `json:"alias"`
	CanonicalID string `json:"canonical_id"`
	// EntityType is recorded at proposal time so a review stays readable.
	EntityType string      `json:"entity_type"`
	Status     AliasStatus `json:"status"`
	Confidence float64     `json:"confidence"`
	// Reason is the model's one-line justification, or manual provenance.
	Reason string `json:"reason,omitempty"`
	// Source is "llm" or "manual".
	Source string `json:"source"`
	// CreatedAt is the proposal time and is NEVER rewritten — the sweep
	// re-runs the upsert on every compile, and an audit trail whose origin
	// timestamp moves each run cannot explain a link after the fact.
	CreatedAt string `json:"created_at"`
	DecidedAt string `json:"decided_at,omitempty"`
	// DecidedBy is "auto" or "user".
	DecidedBy string `json:"decided_by,omitempty"`
}

// LinkResult reports what one alias link copied onto the canonical.
//
// Every counter is of edges on the CANONICAL. Linking is non-destructive: the
// alias keeps every one of its own edges, and nothing existing is overwritten.
type LinkResult struct {
	// Copied — edges newly derived onto the canonical. A CONVERTED edge counts
	// here too: it is newly present in derived_relations, even though the same
	// edge already existed as an anonymous copy. Converted tells the two apart.
	Copied int `json:"copied"`
	// Skipped — the canonical already asserted this edge, so its own row was
	// left untouched. A derived edge must never overwrite a native assertion:
	// the confidence-guarded upsert AddRelation uses is sound only when both
	// sides assert the SAME edge, which is not the case here.
	Skipped int `json:"skipped"`
	// SelfLoops — not derived because the other endpoint IS the canonical. The
	// original alias-to-canonical edge is retained, not deleted.
	SelfLoops int `json:"self_loops"`
	// Converted — a P3-3 anonymous copy found in `relations` and moved into
	// derived_relations with its cause recorded (decision-035). Non-zero only
	// while upgrading a vault written before that change; it is how those
	// pre-existing copies become reversible.
	Converted int `json:"converted,omitempty"`
	// AliasMissing / CanonicalMissing are typed rather than errors: a zero
	// LinkResult is otherwise indistinguishable from a successful link of an
	// edgeless entity, and both the sweep and the CLI must tell those apart
	// without matching on error strings.
	AliasMissing     bool `json:"alias_missing,omitempty"`
	CanonicalMissing bool `json:"canonical_missing,omitempty"`
}

type Direction int

const (
	Outbound Direction = iota
	Inbound
	Both
)

type TraverseOpts struct {
	Direction    Direction
	RelationType string // optional filter
	MaxDepth     int    // 1-5, default 1
	// AsOf makes the traversal point-in-time (P3-6): only edges live at AsOf
	// are followed. Zero means now. Has no effect when the store was built
	// with temporal behavior disabled.
	AsOf time.Time
	// MaxNodes caps the visited set (SPEC-08 AC12). When the traversal
	// visits more than MaxNodes entities it stops and returns the partial
	// result together with limits.ErrTraversalTooWide. Zero = unlimited.
	MaxNodes int
}

// --- communities (P3-5) ---

// Community is one detected graph community row. Summary fields are
// preserved across re-detection via upsert UNLESS the member set changed
// (member-hash mismatch clears them — see CommunityStore.ReplaceDetection).
type Community struct {
	ID          string // c<level>-<seq>, per-run; NOT a stable user reference
	Level       int
	ParentID    string
	MemberCount int
	EdgeCount   int
	Summary     string
	SummaryHash string
	Model       string
	UpdatedAt   string
}

// MemberHash is THE hash of a community's member set (sha256 of the sorted
// IDs joined with \n). It lives in store because both the store layer
// (conditional summary clear) and the compiler pass (staleness check) use
// it, and graph/community cannot be imported from here (import cycle).
func MemberHash(members []string) string {
	cp := append([]string(nil), members...)
	sort.Strings(cp)
	sum := sha256.Sum256([]byte(strings.Join(cp, "\n")))
	return hex.EncodeToString(sum[:])
}

// --- trust ---

type OutputState string

const (
	StatePending   OutputState = "pending"
	StateConfirmed OutputState = "confirmed"
	StateConflict  OutputState = "conflict"
	StateStale     OutputState = "stale"
)

type PendingOutput struct {
	ID             string
	Question       string
	QuestionHash   string
	Answer         string
	AnswerHash     string
	State          OutputState
	Confirmations  int
	GroundingScore *float64
	SourcesHash    string
	SourcesUsed    string // JSON array
	FilePath       string
	CreatedAt      time.Time
	PromotedAt     *time.Time
	DemotedAt      *time.Time
}

type Confirmation struct {
	ID          int
	OutputID    string
	ChunkIDs    string // JSON array
	AnswerHash  string
	ConfirmedAt time.Time
}

type SimilarQuestion struct {
	Output *PendingOutput
	Score  float64
}

// --- compiler ---

type CompileItem struct {
	SourcePath   string
	Hash         string
	FileType     string
	SizeBytes    int64
	Tier         int
	TierDefault  int
	TierOverride *int // nil = no override

	// Per-pass completion
	PassIndexed    bool
	PassEmbedded   bool
	PassParsed     bool
	PassSummarized bool
	PassExtracted  bool
	PassWritten    bool

	// Compilation metadata
	CompileID   string
	Error       string
	ErrorCount  int
	SummaryPath string

	// Promotion/demotion signals
	QueryHitCount int
	LastQueriedAt string
	PromotedAt    string
	DemotedAt     string

	// Compile-key dedup (SPEC-04): the content-addressed compile key and its
	// component preimages. Empty = never computed (pre-SPEC-04 row → the
	// adoption path).
	CompileKey      string
	CompileKeyParts string

	// Quality tracking
	SourceType   string
	QualityScore *float64

	// Durable queue state (P2-3). Status: pending/leased/done/failed.
	// Lease fields are set only while a worker holds the item.
	Status      string
	LeaseOwner  string
	LeaseUntil  string
	HeartbeatAt string
	Attempts    int

	CreatedAt string
	UpdatedAt string
}

type CompileStats struct {
	TotalSources  int
	ByTier        map[int]int // tier -> count
	BySourceType  map[string]int
	FullyCompiled int // pass_written=1
	WithErrors    int
	AvgQuality    float64

	// Queue state (P2-3): status -> count, plus the active lease holder
	// (empty when nothing is leased).
	ByStatus      map[string]int
	ActiveOwner   string
	LastHeartbeat string
}

// ReleaseOutcome is how a worker releases a claimed queue item (P2-3).
type ReleaseOutcome int

const (
	// ReleaseDone — processing succeeded; status becomes 'done' only when
	// every pass applicable to the item's tier is complete, else 'pending'.
	ReleaseDone ReleaseOutcome = iota
	// ReleaseRetry — transient failure; status 'pending', lease cleared.
	ReleaseRetry
	// ReleaseFailed — attempt cap hit; status 'failed' (dead letter).
	ReleaseFailed
)

type QualityScoreRow struct {
	SourcePath string
	Score      float64
}
