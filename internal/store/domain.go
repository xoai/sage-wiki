package store

import "time"

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
