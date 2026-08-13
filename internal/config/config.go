package config

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/xoai/sage-wiki/internal/limits"
	"github.com/xoai/sage-wiki/pkg/events"
)

var typeNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Config represents the sage-wiki project configuration.
type Config struct {
	Extends     string         `yaml:"extends,omitempty"`
	Version     int            `yaml:"version"`
	Project     string         `yaml:"project"`
	Description string         `yaml:"description"`
	Language    string         `yaml:"language,omitempty"`
	Vault       *VaultConfig   `yaml:"vault,omitempty"`
	Sources     []Source       `yaml:"sources"`
	Output      string         `yaml:"output"`
	Ignore      []string       `yaml:"ignore,omitempty"`
	API         APIConfig      `yaml:"api"`
	Models      ModelsConfig   `yaml:"models"`
	Embed       *EmbedConfig   `yaml:"embed,omitempty"`
	Compiler    CompilerConfig `yaml:"compiler"`
	Search      SearchConfig   `yaml:"search"`
	Linting     LintingConfig  `yaml:"linting"`
	Serve       ServeConfig    `yaml:"serve"`
	Ontology    OntologyConfig `yaml:"ontology,omitempty"`
	Trust       TrustConfig    `yaml:"trust,omitempty"`
	Parsers     ParsersConfig  `yaml:"parsers,omitempty"`
	TypeSignals []TypeSignal   `yaml:"type_signals,omitempty"`
	Storage     StorageConfig  `yaml:"storage,omitempty"`
	Vectors     VectorsConfig  `yaml:"vectors,omitempty"`
	Mirror      MirrorConfig   `yaml:"mirror,omitempty"`
	Events      EventsConfig   `yaml:"events,omitempty"`
	Limits      limits.Limits  `yaml:"limits,omitempty"`
}

// MirrorEncryptionConfig tunes optional client-side encryption (SPEC-03).
type MirrorEncryptionConfig struct {
	Enabled bool   `yaml:"enabled,omitempty"`
	KeyFile string `yaml:"key_file,omitempty"` // 32-byte key, MUST live outside the workspace
}

// MirrorConfig configures the S3-compatible remote mirror (SPEC-03).
// Credentials come from named env vars or a credentials file — inline
// secret values are declared ONLY to be rejected by Validate (forward-compat
// guard: secrets never live in config values).
type MirrorConfig struct {
	Enabled              bool                   `yaml:"enabled,omitempty"`
	Endpoint             string                 `yaml:"endpoint,omitempty"`
	Addressing           string                 `yaml:"addressing,omitempty"` // "auto" (default) | "path" | "virtual"
	Bucket               string                 `yaml:"bucket,omitempty"`
	Prefix               string                 `yaml:"prefix,omitempty"`
	Region               string                 `yaml:"region,omitempty"`
	AccessKeyEnv         string                 `yaml:"access_key_env,omitempty"`
	SecretKeyEnv         string                 `yaml:"secret_key_env,omitempty"`
	SessionTokenEnv      string                 `yaml:"session_token_env,omitempty"`
	CredentialsFile      string                 `yaml:"credentials_file,omitempty"`
	ShipInterval         string                 `yaml:"ship_interval,omitempty"`
	SnapshotInterval     string                 `yaml:"snapshot_interval,omitempty"`
	MinRotationInterval  string                 `yaml:"min_rotation_interval,omitempty"`
	ShipLockTimeout      string                 `yaml:"ship_lock_timeout,omitempty"`
	DrainTimeout         string                 `yaml:"drain_timeout,omitempty"`
	RetainGenerations    int                    `yaml:"retain_generations,omitempty"`
	MaxConsecutiveDefers int                    `yaml:"max_consecutive_defers,omitempty"`
	Encryption           MirrorEncryptionConfig `yaml:"encryption,omitempty"`
	AccessKey            string                 `yaml:"access_key,omitempty"` // rejected by Validate
	SecretKey            string                 `yaml:"secret_key,omitempty"` // rejected by Validate
}

// RegionOrDefault resolves the SigV4 region (default "auto" for R2/MinIO).
func (m *MirrorConfig) RegionOrDefault() string {
	if m.Region == "" {
		return "auto"
	}
	return m.Region
}

// AccessKeyEnvOrDefault resolves the env var NAME holding the access key.
func (m *MirrorConfig) AccessKeyEnvOrDefault() string {
	if m.AccessKeyEnv == "" {
		return "AWS_ACCESS_KEY_ID"
	}
	return m.AccessKeyEnv
}

// SecretKeyEnvOrDefault resolves the env var NAME holding the secret key.
func (m *MirrorConfig) SecretKeyEnvOrDefault() string {
	if m.SecretKeyEnv == "" {
		return "AWS_SECRET_ACCESS_KEY"
	}
	return m.SecretKeyEnv
}

// SessionTokenEnvOrDefault resolves the env var NAME holding the STS session
// token (default AWS_SESSION_TOKEN; empty value reads as absent).
func (m *MirrorConfig) SessionTokenEnvOrDefault() string {
	if m.SessionTokenEnv == "" {
		return "AWS_SESSION_TOKEN"
	}
	return m.SessionTokenEnv
}

// RetainGenerationsOrDefault resolves PITR depth in rotation count (default 2).
func (m *MirrorConfig) RetainGenerationsOrDefault() int {
	if m.RetainGenerations == 0 {
		return 2
	}
	return m.RetainGenerations
}

// MaxConsecutiveDefersOrDefault resolves the VACUUM-fallback defer ceiling
// before status surfaces rotation_deferred (default 10).
func (m *MirrorConfig) MaxConsecutiveDefersOrDefault() int {
	if m.MaxConsecutiveDefers == 0 {
		return 10
	}
	return m.MaxConsecutiveDefers
}

// ShipIntervalDur resolves the WAL seal cadence (default 1s).
func (m *MirrorConfig) ShipIntervalDur() time.Duration {
	return durationOr(m.ShipInterval, time.Second)
}

// SnapshotIntervalDur resolves the scheduled generation cadence (default 1h).
func (m *MirrorConfig) SnapshotIntervalDur() time.Duration {
	return durationOr(m.SnapshotInterval, time.Hour)
}

// MinRotationIntervalDur resolves the fold-forced-rotation debounce (default 60s).
func (m *MirrorConfig) MinRotationIntervalDur() time.Duration {
	return durationOr(m.MinRotationInterval, 60*time.Second)
}

// ShipLockTimeoutDur resolves the ship-mutex wait (default 5s).
func (m *MirrorConfig) ShipLockTimeoutDur() time.Duration {
	return durationOr(m.ShipLockTimeout, 5*time.Second)
}

// DrainTimeoutDur resolves the serve shutdown ship budget (default 10s).
func (m *MirrorConfig) DrainTimeoutDur() time.Duration {
	return durationOr(m.DrainTimeout, 10*time.Second)
}

func durationOr(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}

// VectorsConfig tunes the vector query backend (SPEC-06).
type VectorsConfig struct {
	// Backend selects the query-time vector index: "memory" (default — full
	// in-memory matrix cache) or "mmap" (on-disk snapshot served via mmap,
	// falling back to memory with a warning when missing/corrupt/stale).
	Backend string `yaml:"backend,omitempty"`
	// Quantization selects the on-disk matrix encoding written by
	// `index rebuild-vectors`: "none" (default — fp32, exact) or "int8"
	// (4x smaller, measured recall trade-off).
	Quantization string `yaml:"quantization,omitempty"`
}

// VectorBackend resolves the backend (default: memory).
func (c *Config) VectorBackend() string {
	if c.Vectors.Backend == "" {
		return "memory"
	}
	return c.Vectors.Backend
}

// VectorQuantization resolves the quantization (default: none).
func (c *Config) VectorQuantization() string {
	if c.Vectors.Quantization == "" {
		return "none"
	}
	return c.Vectors.Quantization
}

// StorageConfig selects the storage backend. SQLite is the zero-config default;
// Postgres (with pgvector) is an optional server-grade backend (P2-1).
type StorageConfig struct {
	Backend         string `yaml:"backend"`          // "sqlite" (default) | "postgres"
	DSN             string `yaml:"dsn"`              // postgres only; ${ENV} expansion at Load
	VectorDimension int    `yaml:"vector_dimension"` // required when backend=postgres
	LockTimeout     string `yaml:"lock_timeout"`     // default "5s"; parsed via time.ParseDuration
	Pool            struct {
		MaxOpen int `yaml:"max_open"` // default 10
		MaxIdle int `yaml:"max_idle"` // default 2
	} `yaml:"pool"`
}

// LockTimeoutDuration parses LockTimeout (default "5s").
func (s StorageConfig) LockTimeoutDuration() (time.Duration, error) {
	lt := s.LockTimeout
	if lt == "" {
		lt = "5s"
	}
	d, err := time.ParseDuration(lt)
	if err != nil {
		return 0, fmt.Errorf("config: invalid storage.lock_timeout %q: %w", s.LockTimeout, err)
	}
	return d, nil
}

type VaultConfig struct {
	Root string `yaml:"root"`
}

type Source struct {
	Path  string `yaml:"path"`
	Type  string `yaml:"type"`
	Watch bool   `yaml:"watch"`
}

type APIConfig struct {
	Provider    string                 `yaml:"provider"`
	Auth        string                 `yaml:"auth,omitempty"` // "api_key" (default) or "subscription"
	APIKey      string                 `yaml:"api_key"`
	BaseURL     string                 `yaml:"base_url,omitempty"`
	RateLimit   int                    `yaml:"rate_limit,omitempty"`
	ExtraParams map[string]interface{} `yaml:"extra_params,omitempty"` // provider-specific params merged into request body
}

type ModelsConfig struct {
	Summarize string `yaml:"summarize"`
	Extract   string `yaml:"extract"`
	Write     string `yaml:"write"`
	Lint      string `yaml:"lint"`
	Query     string `yaml:"query"`
}

type EmbedConfig struct {
	Provider   string `yaml:"provider"`
	Model      string `yaml:"model"`
	Dimensions int    `yaml:"dimensions,omitempty"`
	APIKey     string `yaml:"api_key,omitempty"`
	BaseURL    string `yaml:"base_url,omitempty"`
	RateLimit  int    `yaml:"rate_limit,omitempty"` // embedding RPM (0 = no limit, -1 = explicit disable)
}

type CompilerConfig struct {
	MaxParallel      int `yaml:"max_parallel"`
	DebounceSeconds  int `yaml:"debounce_seconds"`
	SummaryMaxTokens int `yaml:"summary_max_tokens"`
	ArticleMaxTokens int `yaml:"article_max_tokens"`
	ExtractMaxTokens int `yaml:"extract_max_tokens,omitempty"` // max output tokens for concept extraction (default: 8192)
	ExtractBatchSize int `yaml:"extract_batch_size,omitempty"` // summaries per concept extraction call (default: 20)
	// Temperature (SPEC-04 D2): sampling temperature for compile passes.
	// *float64 so nil means "compile default 0"; pointer distinguishes
	// "unset" from an explicit 0 the same way llm.CallOpts does.
	Temperature        *float64 `yaml:"temperature,omitempty"`
	AutoCommit         bool     `yaml:"auto_commit"`
	AutoLint           bool     `yaml:"auto_lint"`
	Mode               string   `yaml:"mode,omitempty"`                    // standard, batch, or auto
	EstimateBefore     bool     `yaml:"estimate_before,omitempty"`         // prompt with cost estimate before compiling
	PromptCache        *bool    `yaml:"prompt_cache,omitempty"`            // enable prompt caching (default: true)
	BatchThreshold     int      `yaml:"batch_threshold,omitempty"`         // min sources to auto-select batch mode
	TokenPriceOverride float64  `yaml:"token_price_per_million,omitempty"` // override price per 1M input tokens
	// PriceTable is an optional JSON price-table path (PERF-04): entries
	// override built-in prices per provider/model; built-ins cover the rest.
	// Relative paths resolve against the project dir.
	PriceTable    string   `yaml:"price_table,omitempty"`
	Timezone      string   `yaml:"timezone,omitempty"`       // IANA timezone for user-facing timestamps (default: UTC)
	ArticleFields []string `yaml:"article_fields,omitempty"` // custom frontmatter fields extracted from LLM response

	// Tiered compilation
	DefaultTier  int            `yaml:"default_tier,omitempty"`  // default tier for sources (default: 3)
	TierDefaults map[string]int `yaml:"tier_defaults,omitempty"` // file extension → default tier
	// MinConceptSources is the minimum declared sources a concept needs
	// before an article is written for it (issue #128). *int so "unset"
	// (nil → 1, skip only truly source-less concepts) is distinguishable
	// from an explicit 0 (gate disabled).
	MinConceptSources *int           `yaml:"min_concept_sources,omitempty"`
	AutoPromote       *bool          `yaml:"auto_promote,omitempty"` // auto-promote based on signals (default: true)
	PromoteSignals    PromoteSignals `yaml:"promote_signals,omitempty"`
	AutoDemote        *bool          `yaml:"auto_demote,omitempty"` // auto-demote stale articles (default: true)
	DemoteSignals     DemoteSignals  `yaml:"demote_signals,omitempty"`

	// Document splitting (Phase B)
	SplitThreshold         int    `yaml:"split_threshold,omitempty"`           // chars, enable section-aware writing above this (default: 15000)
	MaxSourceContextTokens *int   `yaml:"max_source_context_tokens,omitempty"` // max estimated tokens in source context (default: 100000)
	SplitStrategy          string `yaml:"split_strategy,omitempty"`            // "headings" (default)

	// Backpressure
	BackpressureEnabled *bool `yaml:"backpressure,omitempty"` // enable adaptive backpressure (default: true)

	// Concept deduplication
	DedupThreshold float64 `yaml:"dedup_threshold,omitempty"` // cosine similarity for auto-merge (default: 0.85)
	DedupStrategy  string  `yaml:"dedup_strategy,omitempty"`  // "embedding" (default) or "llm"

	// Wikilink validation
	StripBrokenLinks *bool `yaml:"strip_broken_links,omitempty"` // strip [[wikilinks]] to non-existent concept articles after compile (default: true). Set false to preserve broken links (useful when expecting future compiles to fill them in). Issue #90.

	// Article quality scoring (issue #97)
	Quality QualityConfig `yaml:"quality,omitempty"`

	// Article post-processing (issue #95). Sentences containing any of these
	// phrases are stripped from compiled articles. nil (omitted) → built-in
	// default list; explicit `[]` → disabled (no stripping).
	AntiPatternPhrases []string `yaml:"anti_pattern_phrases,omitempty"`

	// Summary filename scheme (issue #107). "full" (default) hyphen-joins every
	// source-path segment (collision-safe, issue #51). "relative" strips the
	// configured source-root prefix and collapses a duplicated trailing segment
	// (e.g. marker-pdf's <X>/<X>.md → X.md), trading some cross-path collision
	// safety for cleaner names. Changing this renames summaries going forward;
	// enabling it requires a full recompile to rename existing files.
	SummaryNaming string `yaml:"summary_naming,omitempty"`

	resolvedTZ *time.Location `yaml:"-"` // cached by Validate(); not serialized
}

// defaultAntiPatternPhrases is the built-in bilingual filler/meta-phrase list
// stripped from article bodies when compiler.anti_pattern_phrases is unset
// (issue #95). Matched case-insensitively as substrings of a sentence.
var defaultAntiPatternPhrases = []string{
	"this article will",
	"in summary",
	"in conclusion",
	"the source documents don't mention",
	"the source document does not mention",
	"本文将介绍",
	"本文将",
	"综上所述",
	"源文档未提及",
}

// AntiPatternPhrasesOrDefault returns the configured anti-pattern phrase list.
// A nil (omitted) value yields the built-in default; an explicit empty list
// disables stripping. The returned slice must not be mutated by callers.
func (c CompilerConfig) AntiPatternPhrasesOrDefault() []string {
	if c.AntiPatternPhrases == nil {
		return defaultAntiPatternPhrases
	}
	return c.AntiPatternPhrases
}

// SummaryNamingOrDefault returns the summary filename scheme, defaulting to
// "full" when unset (issue #107). Validate() rejects any other value.
func (c CompilerConfig) SummaryNamingOrDefault() string {
	if c.SummaryNaming == "" {
		return "full"
	}
	return c.SummaryNaming
}

// QualityConfig configures the zero-LLM 5-dimension article quality scorer
// (issue #97). All fields default when zero — see CompilerConfig.QualityThreshold
// and CompilerConfig.QualityWeights. The composite score is stored in
// compile_items.quality_score and surfaced (not gated) by `sage-wiki lint`.
type QualityConfig struct {
	Threshold         float64 `yaml:"threshold,omitempty"`          // warn below this composite score (default: 0.5)
	WeightFormat      float64 `yaml:"weight_format,omitempty"`      // default: 0.15
	WeightGrounding   float64 `yaml:"weight_grounding,omitempty"`   // default: 0.30
	WeightCoverage    float64 `yaml:"weight_coverage,omitempty"`    // default: 0.20
	WeightWikilink    float64 `yaml:"weight_wikilink,omitempty"`    // default: 0.15
	WeightAntiPattern float64 `yaml:"weight_antipattern,omitempty"` // default: 0.20
}

// Default quality weights (issue #97 proposal). Kept here so the config
// accessors can supply per-field fallbacks without importing the compiler.
const (
	defaultQualityThreshold         = 0.5
	defaultQualityWeightFormat      = 0.15
	defaultQualityWeightGrounding   = 0.30
	defaultQualityWeightCoverage    = 0.20
	defaultQualityWeightWikilink    = 0.15
	defaultQualityWeightAntiPattern = 0.20
)

// MinConceptSourcesOrDefault resolves the gate: nil → 1, explicit 0 →
// disabled (no filtering), N → N.
func (c CompilerConfig) MinConceptSourcesOrDefault() int {
	if c.MinConceptSources == nil {
		return 1
	}
	return *c.MinConceptSources
}

// MaxSourceContextTokensOrDefault resolves the budget: nil → 100000, explicit 0 → 100000.
func (c CompilerConfig) MaxSourceContextTokensOrDefault() int {
	if c.MaxSourceContextTokens == nil {
		return 100000
	}
	if *c.MaxSourceContextTokens <= 0 {
		return 100000
	}
	return *c.MaxSourceContextTokens
}

// QualityThreshold returns the configured low-quality warning threshold,
// or the default (0.5) when unset.
func (c CompilerConfig) QualityThreshold() float64 {
	if c.Quality.Threshold > 0 {
		return c.Quality.Threshold
	}
	return defaultQualityThreshold
}

// QualityWeights returns the five scorer dimension weights as plain floats
// (config must not import the compiler package). Each field falls back to
// its #97 default when left at zero, so partial overrides work.
func (c CompilerConfig) QualityWeights() (format, grounding, coverage, wikilink, antiPattern float64) {
	q := c.Quality
	format = q.WeightFormat
	if format == 0 {
		format = defaultQualityWeightFormat
	}
	grounding = q.WeightGrounding
	if grounding == 0 {
		grounding = defaultQualityWeightGrounding
	}
	coverage = q.WeightCoverage
	if coverage == 0 {
		coverage = defaultQualityWeightCoverage
	}
	wikilink = q.WeightWikilink
	if wikilink == 0 {
		wikilink = defaultQualityWeightWikilink
	}
	antiPattern = q.WeightAntiPattern
	if antiPattern == 0 {
		antiPattern = defaultQualityWeightAntiPattern
	}
	return format, grounding, coverage, wikilink, antiPattern
}

// PromoteSignals configures when sources are promoted to higher tiers.
type PromoteSignals struct {
	QueryHitCount     int    `yaml:"query_hit_count,omitempty"`     // promote after N search hits (default: 3)
	ClusterSize       int    `yaml:"cluster_size,omitempty"`        // promote when N+ sources on same topic (default: 5)
	ManualTag         string `yaml:"manual_tag,omitempty"`          // promote if tagged (default: "compile")
	ImportCentrality  int    `yaml:"import_centrality,omitempty"`   // code: promote when N+ files import this (default: 10)
	SourceRecencyDays int    `yaml:"source_recency_days,omitempty"` // boost recently modified (default: 7)
}

// DemoteSignals configures when sources are demoted to lower tiers.
type DemoteSignals struct {
	SourceModified bool `yaml:"source_modified,omitempty"` // revert to Tier 1 on source change (default: true)
	StaleDays      int  `yaml:"stale_days,omitempty"`      // demote after N days with no queries (default: 90)
}

// AutoPromoteEnabled returns whether auto-promotion is enabled (default: true).
func (c CompilerConfig) AutoPromoteEnabled() bool {
	if c.AutoPromote == nil {
		return true
	}
	return *c.AutoPromote
}

// AutoDemoteEnabled returns whether auto-demotion is enabled (default: true).
func (c CompilerConfig) AutoDemoteEnabled() bool {
	if c.AutoDemote == nil {
		return true
	}
	return *c.AutoDemote
}

// BackpressureIsEnabled returns whether backpressure is enabled (default: true).
func (c CompilerConfig) BackpressureIsEnabled() bool {
	if c.BackpressureEnabled == nil {
		return true
	}
	return *c.BackpressureEnabled
}

// StripBrokenLinksEnabled returns whether dead [[wikilinks]] are stripped
// from articles after compile (default: true). Issue #90.
func (c CompilerConfig) StripBrokenLinksEnabled() bool {
	if c.StripBrokenLinks == nil {
		return true
	}
	return *c.StripBrokenLinks
}

type SearchConfig struct {
	HybridWeightBM25   float64 `yaml:"hybrid_weight_bm25"`
	HybridWeightVector float64 `yaml:"hybrid_weight_vector"`
	// HybridWeightGraph fuses the graph channel (ADR-037; 0 → 0.2 default,
	// flat key matching its two siblings above).
	HybridWeightGraph float64 `yaml:"hybrid_weight_graph,omitempty"`
	// GraphRelationWeights overrides per-relation-type graph-leg weights
	// (config-extensible relation types default to 1.0).
	GraphRelationWeights map[string]float64 `yaml:"graph_relation_weights,omitempty"`
	// Pipeline selects the retrieval pipeline for the entry-point
	// adapters: "unified" (default — search.Run) or "legacy" (the
	// pre-M5 doc-level path, retained through this release as the
	// rollback per ADR-036; deleted after M6 validates).
	Pipeline           string   `yaml:"pipeline,omitempty"`
	DefaultLimit       int      `yaml:"default_limit"`
	QueryExpansion     *bool    `yaml:"query_expansion,omitempty"`      // enable LLM query expansion (default: true)
	Rerank             *bool    `yaml:"rerank,omitempty"`               // enable LLM re-ranking (default: true)
	RerankMinCoverage  *float64 `yaml:"rerank_min_coverage,omitempty"`  // min fraction of candidates the LLM must score for blending (default: 0.5)
	ChunkSize          int      `yaml:"chunk_size,omitempty"`           // tokens per chunk for indexing (default: 800)
	ChunkOverlapTokens int      `yaml:"chunk_overlap_tokens,omitempty"` // tokens of overlap between adjacent chunks (default: 0; recommended opt-in: 80). Takes effect only on reindex.
	ResultMaxChars     int      `yaml:"result_max_chars,omitempty"`     // max chars (runes) of content per wiki_search result before truncation (default: 2000; set very high to effectively disable)

	// Graph-enhanced retrieval
	GraphExpansion       *bool    `yaml:"graph_expansion,omitempty"`        // enable graph-based context expansion (default: true)
	GraphMaxExpand       int      `yaml:"graph_max_expand,omitempty"`       // max articles added via graph (default: 10)
	GraphDepth           int      `yaml:"graph_depth,omitempty"`            // traversal depth for expansion (default: 2)
	ContextMaxTokens     int      `yaml:"context_max_tokens,omitempty"`     // token budget for query context (default: 8000)
	WeightDirectLink     *float64 `yaml:"weight_direct_link,omitempty"`     // graph signal weight (default: 3.0, set 0 to disable)
	WeightSourceOverlap  *float64 `yaml:"weight_source_overlap,omitempty"`  // graph signal weight (default: 4.0, set 0 to disable)
	WeightCommonNeighbor *float64 `yaml:"weight_common_neighbor,omitempty"` // graph signal weight (default: 1.5, set 0 to disable)
	WeightTypeAffinity   *float64 `yaml:"weight_type_affinity,omitempty"`   // graph signal weight (default: 1.0, set 0 to disable)

	// ANN selects the approximate (HNSW) vector index for large vaults
	// (P2-7). Default off: exact brute-force search.
	ANN ANNConfig `yaml:"ann,omitempty"`
}

// ANNConfig toggles the approximate vector index backend.
type ANNConfig struct {
	Enabled *bool `yaml:"enabled,omitempty"`
}

// ANNEnabled resolves the ANN flag (default: false — brute-force).
func (c *SearchConfig) ANNEnabled() bool {
	return c.ANN.Enabled != nil && *c.ANN.Enabled
}

// QueryExpansionEnabled returns whether query expansion is enabled (default: true).
func (s SearchConfig) QueryExpansionEnabled() bool {
	if s.QueryExpansion == nil {
		return true
	}
	return *s.QueryExpansion
}

// ChunkOverlapOrDefault returns the chunk overlap in tokens (default 0 —
// byte-identical to historical chunking; recommended opt-in: 80).
func (s SearchConfig) ChunkOverlapOrDefault() int {
	if s.ChunkOverlapTokens > 0 {
		return s.ChunkOverlapTokens
	}
	return 0
}

// PipelineOrDefault resolves the adapter pipeline: "unified" (default)
// or "legacy". Any other value is rejected by Validate — a rollback switch
// that silently ignores a typo is worse than no switch at all.
func (s SearchConfig) PipelineOrDefault() string {
	if s.Pipeline == "legacy" {
		return "legacy"
	}
	return "unified"
}

// RerankMinCoverageOrDefault returns the minimum scored-candidate fraction
// required for rerank blending (default 0.5 — ADR-038's coverage gate).
func (s SearchConfig) RerankMinCoverageOrDefault() float64 {
	if s.RerankMinCoverage == nil || *s.RerankMinCoverage <= 0 {
		return 0.5
	}
	return *s.RerankMinCoverage
}

// RerankEnabled returns whether re-ranking is enabled (default: true).
func (s SearchConfig) RerankEnabled() bool {
	if s.Rerank == nil {
		return true
	}
	return *s.Rerank
}

// ChunkSizeOrDefault returns the chunk size or 800 if not set.
func (s SearchConfig) ChunkSizeOrDefault() int {
	if s.ChunkSize <= 0 {
		return 800
	}
	return s.ChunkSize
}

// GraphExpansionEnabled returns whether graph expansion is enabled (default: true).
func (s SearchConfig) GraphExpansionEnabled() bool {
	if s.GraphExpansion == nil {
		return true
	}
	return *s.GraphExpansion
}

// GraphMaxExpandOrDefault returns the max expand or 10 if not set.
func (s SearchConfig) GraphMaxExpandOrDefault() int {
	if s.GraphMaxExpand <= 0 {
		return 10
	}
	return s.GraphMaxExpand
}

// GraphDepthOrDefault returns the graph depth or 2 if not set.
func (s SearchConfig) GraphDepthOrDefault() int {
	if s.GraphDepth <= 0 {
		return 2
	}
	return s.GraphDepth
}

// ContextMaxTokensOrDefault returns the context token budget or 8000 if not set.
func (s SearchConfig) ContextMaxTokensOrDefault() int {
	if s.ContextMaxTokens <= 0 {
		return 8000
	}
	return s.ContextMaxTokens
}

// ResultMaxCharsOrDefault returns the per-result content cap (in runes) for
// wiki_search, or 2000 if not set. Bounds the MCP search payload so a single
// search can't overflow the calling agent's context; full text stays available
// via wiki_read.
func (s SearchConfig) ResultMaxCharsOrDefault() int {
	if s.ResultMaxChars <= 0 {
		return 2000
	}
	return s.ResultMaxChars
}

// WeightDirectLinkOrDefault returns the direct link weight or 3.0 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightDirectLinkOrDefault() float64 {
	if s.WeightDirectLink == nil {
		return 3.0
	}
	return *s.WeightDirectLink
}

// WeightSourceOverlapOrDefault returns the source overlap weight or 4.0 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightSourceOverlapOrDefault() float64 {
	if s.WeightSourceOverlap == nil {
		return 4.0
	}
	return *s.WeightSourceOverlap
}

// WeightCommonNeighborOrDefault returns the common neighbor weight or 1.5 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightCommonNeighborOrDefault() float64 {
	if s.WeightCommonNeighbor == nil {
		return 1.5
	}
	return *s.WeightCommonNeighbor
}

// WeightTypeAffinityOrDefault returns the type affinity weight or 1.0 if not set.
// Explicit 0 disables this signal.
func (s SearchConfig) WeightTypeAffinityOrDefault() float64 {
	if s.WeightTypeAffinity == nil {
		return 1.0
	}
	return *s.WeightTypeAffinity
}

type LintingConfig struct {
	AutoFixPasses          []string `yaml:"auto_fix_passes"`
	StalenessThresholdDays int      `yaml:"staleness_threshold_days"`
}

type ServeConfig struct {
	Transport string `yaml:"transport"`
	Port      int    `yaml:"port"`
	// Metrics, when true, registers a Prometheus /metrics endpoint on the web
	// server (P2-2). Off by default; gated by the same bearer-token auth as
	// /api/* when a token is configured.
	Metrics bool `yaml:"metrics,omitempty"`
	// Token, when set, requires an `Authorization: Bearer <token>` header (or a
	// `?token=` query param) on all /api/* and /ws requests. Lowest-precedence
	// source; the --token flag and SAGE_WIKI_TOKEN env override it. A token is
	// mandatory when binding to a non-loopback address (see cmd serve).
	Token string `yaml:"token,omitempty"`
	// AllowedHost is a comma-separated list of extra Host header values accepted
	// beyond loopback (defeats DNS rebinding). Set this to the public hostname
	// when exposing the server beyond localhost.
	AllowedHost string `yaml:"allowed_host,omitempty"`
	// Worker configures the durable compile-queue worker that runs compiles
	// inside serve mode (P2-3). Enabled by default in serve; zero values
	// resolve to the documented defaults (5s/120s/30s/5/16).
	Worker WorkerConfig `yaml:"worker,omitempty"`
	// Webhooks lists event-delivery endpoints (SPEC-07). Empty = no
	// webhook delivery.
	Webhooks []WebhookConfig `yaml:"webhooks,omitempty"`
}

// WebhookConfig is one event-delivery endpoint (SPEC-07). The secret comes
// from an env var or a file — never inline in config (secrets never in
// code/config, Base Principle 3).
type WebhookConfig struct {
	URL string `yaml:"url"`
	// SecretEnv names an environment variable holding the HMAC secret.
	SecretEnv string `yaml:"secret_env,omitempty"`
	// SecretFile names a file whose contents are the HMAC secret.
	SecretFile string `yaml:"secret_file,omitempty"`
	// Types filters which event types are delivered; empty = all types.
	Types []string `yaml:"types,omitempty"`
	// TimeoutSeconds bounds each delivery attempt (default 5).
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
	// MaxRetries caps retries on 5xx/timeout/connection errors
	// (default 3; explicit 0 = no retries).
	MaxRetries *int `yaml:"max_retries,omitempty"`
}

// TimeoutSecondsOrDefault resolves the per-delivery timeout (default 5s).
func (w WebhookConfig) TimeoutSecondsOrDefault() int {
	if w.TimeoutSeconds <= 0 {
		return 5
	}
	return w.TimeoutSeconds
}

// MaxRetriesOrDefault resolves the retry cap (default 3; explicit 0 means
// no retries — the pointer carries the unset-vs-zero distinction).
func (w WebhookConfig) MaxRetriesOrDefault() int {
	if w.MaxRetries == nil || *w.MaxRetries < 0 {
		return 3
	}
	return *w.MaxRetries
}

// EventsConfig controls the engine event stream (SPEC-07).
type EventsConfig struct {
	// Enable is the master emit switch (default true).
	Enable *bool `yaml:"enable,omitempty"`
	// Dir is the JSONL audit-trail directory, workspace-relative
	// (default "events").
	Dir string `yaml:"dir,omitempty"`
	// BufferSize is the bus ring capacity in events (default 1024).
	BufferSize int `yaml:"buffer_size,omitempty"`
	// Stdout also tees events to stdout for piping (default false).
	Stdout bool `yaml:"stdout,omitempty"`
	// RawQueries includes raw query text in search_performed events
	// (default false — local debug only; the stream carries hashes).
	RawQueries bool `yaml:"raw_queries,omitempty"`
}

// EnabledOrDefault resolves the master emit switch (default true).
func (e EventsConfig) EnabledOrDefault() bool {
	if e.Enable == nil {
		return true
	}
	return *e.Enable
}

// DirOrDefault resolves the audit-trail directory (default "events").
func (e EventsConfig) DirOrDefault() string {
	if e.Dir == "" {
		return "events"
	}
	return e.Dir
}

// BufferSizeOrDefault resolves the bus ring capacity (default 1024).
func (e EventsConfig) BufferSizeOrDefault() int {
	if e.BufferSize <= 0 {
		return 1024
	}
	return e.BufferSize
}

// WorkerConfig tunes the serve-mode compile worker (P2-3). Enabled is a
// *bool so an explicit `enabled: false` is distinguishable from unset
// (PromptCache precedent).
type WorkerConfig struct {
	Enabled                  *bool `yaml:"enabled,omitempty"`
	PollIntervalSeconds      int   `yaml:"poll_interval_seconds,omitempty"`
	LeaseTTLSeconds          int   `yaml:"lease_ttl_seconds,omitempty"`
	HeartbeatIntervalSeconds int   `yaml:"heartbeat_interval_seconds,omitempty"`
	MaxAttempts              int   `yaml:"max_attempts,omitempty"`
	ClaimLimit               int   `yaml:"claim_limit,omitempty"`
}

// WorkerEnabled resolves the worker on/off flag (default: on in serve mode).
func (c *ServeConfig) WorkerEnabled() bool {
	if c.Worker.Enabled == nil {
		return true
	}
	return *c.Worker.Enabled
}

// TypeSignal defines a content-based type detection rule.
// Files are matched by filename keywords and/or content keywords.
type TypeSignal struct {
	Type             string   `yaml:"type"`
	Pattern          string   `yaml:"pattern,omitempty"`           // simple substring match (legacy)
	FilenameKeywords []string `yaml:"filename_keywords,omitempty"` // keywords matched against filename
	ContentKeywords  []string `yaml:"content_keywords,omitempty"`  // keywords matched against content head
	MinContentHits   int      `yaml:"min_content_hits,omitempty"`  // minimum content keyword matches required
}

// OntologyConfig configures ontology relation and entity types.
type OntologyConfig struct {
	Relations     []RelationConfig   `yaml:"relations,omitempty"`
	RelationTypes []RelationConfig   `yaml:"relation_types,omitempty"` // preferred key; "relations" accepted for backwards compat
	EntityTypes   []EntityTypeConfig `yaml:"entity_types,omitempty"`
	Triples       TriplesConfig      `yaml:"triples,omitempty"`
	Resolve       ResolveConfig      `yaml:"resolve,omitempty"`
	GraphQuery    GraphQueryConfig   `yaml:"graph_query,omitempty"`
	Temporal      TemporalConfig     `yaml:"temporal,omitempty"`
	Communities   CommunitiesConfig  `yaml:"communities,omitempty"`
}

// CommunitiesConfig controls community detection + global queries (P3-5).
// Enabled defaults OFF for the same reason TriplesConfig does: the pass adds
// LLM calls (summaries), and an upgrade must not raise anyone's bill unasked.
type CommunitiesConfig struct {
	Enabled        bool   `yaml:"enabled,omitempty"`
	Model          string `yaml:"model,omitempty"`
	MaxTokens      int    `yaml:"max_tokens,omitempty"`
	MaxCommunities int    `yaml:"max_communities,omitempty"`
	MinMembers     int    `yaml:"min_members,omitempty"`
}

// MaxTokensOrDefault returns the summary token cap (default 1024).
func (c CommunitiesConfig) MaxTokensOrDefault() int {
	if c.MaxTokens <= 0 {
		return 1024
	}
	return c.MaxTokens
}

// MaxCommunitiesOrDefault returns the global-query map breadth (default 8).
func (c CommunitiesConfig) MaxCommunitiesOrDefault() int {
	if c.MaxCommunities <= 0 {
		return 8
	}
	return c.MaxCommunities
}

// MinMembersOrDefault returns the smallest community that gets a summary
// (default 3).
func (c CommunitiesConfig) MinMembersOrDefault() int {
	if c.MinMembers <= 0 {
		return 3
	}
	return c.MinMembers
}

// TemporalConfig controls bi-temporal edge validity (P3-6): default
// live-at-now filtering on relation reads, functional-predicate supersession,
// and as_of point-in-time queries.
type TemporalConfig struct {
	// Enabled gates all temporal behavior (default: true). False means: no
	// validity filtering, no supersession, and as_of is rejected by callers.
	// *bool so "unset" is distinguishable from an explicit false.
	Enabled *bool `yaml:"enabled,omitempty"`
	// AutoApplyThreshold is the minimum confidence for a contradicting edge to
	// auto-invalidate the superseded one (default: 0.8). Below it, a trust
	// conflict is recorded instead. Valid range: (0, 1].
	AutoApplyThreshold float64 `yaml:"auto_apply_threshold,omitempty"`
}

// EnabledOrDefault returns whether temporal behavior is enabled (default: true).
func (t TemporalConfig) EnabledOrDefault() bool {
	if t.Enabled == nil {
		return true
	}
	return *t.Enabled
}

// AutoApplyThresholdOrDefault returns the threshold or 0.8 when unset or out
// of range — a zero/negative value would auto-apply everything, and >1 would
// auto-apply nothing; both silently invert the intended semantics.
func (t TemporalConfig) AutoApplyThresholdOrDefault() float64 {
	if t.AutoApplyThreshold <= 0 || t.AutoApplyThreshold > 1 {
		return 0.8
	}
	return t.AutoApplyThreshold
}

// GraphQueryConfig bounds the multi-hop graph-query surface (P3-4): the
// wiki_graph_query MCP tool's subgraph serialization. Both values fall back
// to defaults when unset or out of range — resolution lives in
// internal/query (applyGraphQueryDefaults), next to its one consumer.
type GraphQueryConfig struct {
	// MaxHops bounds BFS expansion from the seed entities. Default 2,
	// valid range 1..5 (the same ceiling wiki_ontology_query's depth uses).
	MaxHops int `yaml:"max_hops,omitempty"`
	// MaxEdges bounds the serialized subgraph — it is a token budget in
	// disguise. Default 60, valid range 1..500. Within each hop edges are
	// sorted before the cap applies, so the retained SET is deterministic.
	MaxEdges int `yaml:"max_edges,omitempty"`
}

// ResolveConfig controls Claude-driven entity resolution (P3-3).
//
// Enabled defaults to false for the same reason TriplesConfig does: the pass
// adds LLM calls, and an upgrade must not raise anyone's bill unasked.
//
// Enabling it links by default: AutoApplyThreshold defaults to 0.85, so
// high-confidence, fully-guarded proposals are applied — and exactly
// reversible with `ontology resolve --unlink` (decision-035), which is why
// the default is no longer review-only. Set an explicit 1.0 for review-only:
// that means never auto-apply, exactly, and every proposal queues for a
// human. The pass warns at WARN level whenever proposals are standing, on
// every exit path.
//
// Lower the threshold to opt in. Once lowered, auto-apply ALSO requires a
// description on at least one side, and the only COMPILE-PATH writer of entity
// descriptions is the triple-extraction pass — so Resolve pairs with Triples.
// That pairing is not itself a guarantee: internal/scribe writes Definition too,
// so a scribe-described entity can auto-link with triples off. The threshold is
// the guarantee; the description is a second condition on top of it.
//
// A VALUE, not a pointer, with `omitempty` on the field: yaml.v3 elides a zero
// struct (unlike encoding/json), which is what stops `sage-wiki pack apply` from
// writing a zeroed resolve: block into the config.yaml of every user who never
// configured one.
//
// The zero value of each numeric key is NOT usable — Defaults() has no Ontology
// entry and is only reached through config.Load, so a Config{} literal yields
// zeros. applyResolveDefaults (internal/compiler) supplies the fallbacks; a zero
// MaxBlockSize would otherwise make the candidate cap negative and every block
// empty, and a zero AutoApplyThreshold would auto-apply every proposal.
type ResolveConfig struct {
	Enabled bool   `yaml:"enabled,omitempty"`
	Model   string `yaml:"model,omitempty"`

	MaxTokens    int `yaml:"max_tokens,omitempty"`
	MaxBlockSize int `yaml:"max_block_size,omitempty"`

	// AutoApplyThreshold is the confidence at or above which a link is applied
	// without review — EXCEPT at 1.0, which means never, by an explicit branch in
	// canAutoApply. normalizeClusters clamps confidence to [0,1], so without that
	// branch a model returning 1.0 would defeat a 1.0 threshold.
	//
	// 0.85 is the DEFAULT. Review-only (1.0) was the default while a link
	// could not be undone; `--unlink` (decision-035) makes a mistake cost one
	// command, so auto-apply is the default and an explicit 1.0 is the
	// review-only opt-in.
	//
	// Outside (0,1] it falls back to the default rather than clamping: a
	// configured 0 would auto-apply every proposal including zero-confidence
	// ones, which is the worst outcome this pass can produce. An explicit
	// out-of-range value warns — the fallback now lands on the permissive
	// 0.85, not on review-only.
	AutoApplyThreshold float64 `yaml:"auto_apply_threshold,omitempty"`

	// MaxTokenDF and MinTokenDFFloor together decide which name tokens are
	// discriminating enough to block on. BOTH are required: a percentage alone
	// discards the very token that identifies a small cluster (three "aldrin"
	// rows among 45 concepts is 6.7%, over a 5% threshold), while a floor alone
	// does not scale to a large vault.
	MaxTokenDF      float64 `yaml:"max_token_df,omitempty"`
	MinTokenDFFloor int     `yaml:"min_token_df_floor,omitempty"`

	// UseEmbeddings widens candidate generation to names sharing no tokens
	// ("NYC" / "New York City"). Off by default: embed.Embedder has no batch
	// method, so every vector is one HTTP call, and vectors are held in memory
	// for the pass and discarded rather than persisted.
	UseEmbeddings      bool    `yaml:"use_embeddings,omitempty"`
	EmbedThreshold     float64 `yaml:"embed_threshold,omitempty"`
	MaxEmbedCandidates int     `yaml:"max_embed_candidates,omitempty"`
}

// TriplesConfig controls LLM structured-output triple extraction (P3-2).
//
// Enabled defaults to false: the pass adds one LLM call per Tier-3 document, so
// a default-on upgrade would raise every existing user's compile bill without
// them asking. For a key that costs money, "safe default" means preserving
// today's spend.
//
// A VALUE, not a pointer, and `omitempty` on the struct: yaml.v3 elides a zero
// struct (unlike encoding/json), which is what stops `sage-wiki pack apply`
// from writing a zeroed triples: block into the config.yaml of every user who
// never configured one.
//
// The zero value of each cap is NOT usable — Defaults() has no Ontology entry
// and is only reached via config.Load, so a Config{} literal yields zeros.
// ExtractTriplesPass applies <= 0 fallbacks, the way the rest of the compiler
// does (see concepts.go, fullpipeline.go).
type TriplesConfig struct {
	Enabled            bool   `yaml:"enabled,omitempty"`
	Model              string `yaml:"model,omitempty"`
	MaxTokens          int    `yaml:"max_tokens,omitempty"`
	MaxEntitiesPerDoc  int    `yaml:"max_entities_per_doc,omitempty"`
	MaxRelationsPerDoc int    `yaml:"max_relations_per_doc,omitempty"`
}

// RelationConfig defines a custom or extended relation type.
type RelationConfig struct {
	Name         string   `yaml:"name"`
	Synonyms     []string `yaml:"synonyms"`
	ValidSources []string `yaml:"valid_sources,omitempty"`
	ValidTargets []string `yaml:"valid_targets,omitempty"`
	// Functional asserts OUTBOUND uniqueness (P3-6): each source has at most
	// one live target for this predicate, so a new edge (s, p, o2) supersedes
	// the live (s, p, o1). Edges are stored only as asserted — never mark an
	// inbound-unique relation (e.g. employs) functional; use the outbound form.
	Functional bool `yaml:"functional,omitempty"`
}

// EntityTypeConfig defines a custom or extended entity type.
type EntityTypeConfig struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
}

// TrustConfig controls the output trust system (staged QA for query outputs).
type TrustConfig struct {
	IncludeOutputs      string  `yaml:"include_outputs,omitempty"`      // "false" (default), "verified", "true"
	ConsensusThreshold  int     `yaml:"consensus_threshold,omitempty"`  // confirmations for promotion (default: 3)
	GroundingThreshold  float64 `yaml:"grounding_threshold,omitempty"`  // min grounding score (default: 0.8)
	AutoPromote         *bool   `yaml:"auto_promote,omitempty"`         // auto-promote when thresholds met (default: true)
	SimilarityThreshold float64 `yaml:"similarity_threshold,omitempty"` // cosine sim for question matching (default: 0.85)
}

// IncludeOutputsMode returns the effective include_outputs mode.
// Empty string is treated as "false" (safe default).
func (t TrustConfig) IncludeOutputsMode() string {
	if t.IncludeOutputs == "" {
		return "false"
	}
	return t.IncludeOutputs
}

// AutoPromoteEnabled returns whether auto-promotion is enabled (default: true).
func (t TrustConfig) AutoPromoteEnabled() bool {
	if t.AutoPromote == nil {
		return true
	}
	return *t.AutoPromote
}

// ConsensusThresholdOrDefault returns the consensus threshold or 3 if not set.
func (t TrustConfig) ConsensusThresholdOrDefault() int {
	if t.ConsensusThreshold <= 0 {
		return 3
	}
	return t.ConsensusThreshold
}

// GroundingThresholdOrDefault returns the grounding threshold or 0.8 if not set.
func (t TrustConfig) GroundingThresholdOrDefault() float64 {
	if t.GroundingThreshold <= 0 {
		return 0.8
	}
	return t.GroundingThreshold
}

// ParsersConfig controls external parser behavior.
type ParsersConfig struct {
	External      bool `yaml:"external,omitempty"`       // enable external parsers (default: false)
	TrustExternal bool `yaml:"trust_external,omitempty"` // acknowledge that external parsers run unsandboxed code (default: false)
}

// SimilarityThresholdOrDefault returns the similarity threshold or 0.85 if not set.
func (t TrustConfig) SimilarityThresholdOrDefault() float64 {
	if t.SimilarityThreshold <= 0 {
		return 0.85
	}
	return t.SimilarityThreshold
}

// Defaults returns a Config with sensible defaults for greenfield mode.
func Defaults() Config {
	return Config{
		Version: 1,
		Output:  "wiki",
		Sources: []Source{{Path: "raw", Type: "auto", Watch: true}},
		Compiler: CompilerConfig{
			MaxParallel:      20,
			DebounceSeconds:  2,
			SummaryMaxTokens: 4000,
			ArticleMaxTokens: 4000,
			ExtractMaxTokens: 8192,
			ExtractBatchSize: 20,
			AutoCommit:       true,
			AutoLint:         true,
			DefaultTier:      3,
			Mode:             "auto",
		},
		Search: SearchConfig{
			HybridWeightBM25:   0.7,
			HybridWeightVector: 0.3,
			DefaultLimit:       10,
		},
		Linting: LintingConfig{
			AutoFixPasses:          []string{"consistency", "completeness", "style"},
			StalenessThresholdDays: 90,
		},
		Serve: ServeConfig{
			Transport: "stdio",
			Port:      3333,
		},
		Events: EventsConfig{
			Dir: "events",
		},
		Limits: limits.Limits{}.Resolve(),
		Trust: TrustConfig{
			IncludeOutputs: "false",
		},
		Storage: StorageConfig{
			Backend:     "sqlite",
			LockTimeout: "5s",
			Pool: struct {
				MaxOpen int `yaml:"max_open"`
				MaxIdle int `yaml:"max_idle"`
			}{MaxOpen: 10, MaxIdle: 2},
		},
	}
}

// PromptCacheEnabled returns whether prompt caching is enabled (default: true).
func (c *CompilerConfig) PromptCacheEnabled() bool {
	if c.PromptCache == nil {
		return true
	}
	return *c.PromptCache
}

// UserTimeLocation returns the configured timezone for user-facing timestamps.
// Returns the cached location set by Validate(), or resolves from Timezone string.
// Defaults to UTC if Timezone is empty or invalid.
func (c *CompilerConfig) UserTimeLocation() *time.Location {
	if c.resolvedTZ != nil {
		return c.resolvedTZ
	}
	if c.Timezone != "" {
		if loc, err := time.LoadLocation(c.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// UserNow returns the current time formatted in RFC3339 using the configured timezone.
func (c *CompilerConfig) UserNow() string {
	return NowUTC().In(c.UserTimeLocation()).Format(time.RFC3339)
}

// CompileTemperature resolves the sampling temperature compile passes send
// on the wire (SPEC-04 D2): the configured value when set, else explicit 0
// (the provider's most-deterministic setting). Always non-nil so compile
// requests always carry the field.
func (c *CompilerConfig) CompileTemperature() *float64 {
	if c.Temperature != nil {
		return c.Temperature
	}
	zero := 0.0
	return &zero
}

// NowUTC returns the current time in UTC. SOURCE_DATE_EPOCH (the
// reproducible-builds convention) overrides the clock: with it set, every
// caller gets the epoch — the single clock behind SPEC-04's deterministic
// artifacts (frontmatter, manifest, DB rows, compile IDs). An unparseable
// SDE falls back to wall clock — warned once, never silently (an SDE typo
// must not quietly disable pinning).
func NowUTC() time.Time {
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
			return time.Unix(sec, 0).UTC()
		}
		sdeWarnOnce.Do(func() {
			log.Printf("config: ignoring unparseable SOURCE_DATE_EPOCH %q — timestamps are NOT pinned", s)
		})
	}
	return time.Now().UTC()
}

var sdeWarnOnce sync.Once

// Load reads and parses a config file, expanding environment variables.
// If the config contains an "extends" field, the base config is loaded first
// and deep-merged with the child config (maps merge recursively, scalars/slices
// from child replace base). At most one level of inheritance (base's extends is ignored).
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config.Load: %w", err)
	}

	// Expand environment variables in ${VAR} format
	expanded := expandEnvVars(string(data))

	// Quick parse to check for extends field
	var peek struct {
		Extends string `yaml:"extends"`
	}
	yaml.Unmarshal([]byte(expanded), &peek)

	finalYAML := expanded
	if peek.Extends != "" {
		basePath := peek.Extends
		if !filepath.IsAbs(basePath) {
			basePath = filepath.Join(filepath.Dir(path), basePath)
		}

		baseData, err := os.ReadFile(basePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: extends base %q not found, using child config only\n", peek.Extends)
		} else {
			baseExpanded := expandEnvVars(string(baseData))

			// Deep merge via map[string]any to avoid yaml.v3 zero-value clobbering
			var baseMap, childMap map[string]any
			if err := yaml.Unmarshal([]byte(baseExpanded), &baseMap); err != nil {
				return nil, fmt.Errorf("config.Load: parse base %q: %w", peek.Extends, err)
			}
			if err := yaml.Unmarshal([]byte(expanded), &childMap); err != nil {
				return nil, fmt.Errorf("config.Load: parse child: %w", err)
			}
			// Remove extends from child before merge
			delete(childMap, "extends")

			merged := deepMerge(baseMap, childMap)
			mergedBytes, err := yaml.Marshal(merged)
			if err != nil {
				return nil, fmt.Errorf("config.Load: marshal merged: %w", err)
			}
			finalYAML = string(mergedBytes)
		}
	}

	cfg := Defaults()
	if err := yaml.Unmarshal([]byte(finalYAML), &cfg); err != nil {
		return nil, fmt.Errorf("config.Load: parse error: %w", err)
	}
	cfg.Extends = "" // clear after merge

	// Resolve the price table relative to the config file's directory
	// (PERF-04): absolute paths pass through untouched.
	if cfg.Compiler.PriceTable != "" && !filepath.IsAbs(cfg.Compiler.PriceTable) {
		cfg.Compiler.PriceTable = filepath.Join(filepath.Dir(path), cfg.Compiler.PriceTable)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Save writes the config to a YAML file.
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("config.Save: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// validateStorage checks the storage backend selection (P2-1).
func (c *Config) validateStorage() error {
	b := c.Storage.Backend
	if b == "" {
		b = "sqlite"
	}
	if b != "sqlite" && b != "postgres" {
		return fmt.Errorf("config: invalid storage.backend %q (valid: sqlite, postgres)", c.Storage.Backend)
	}
	if _, err := c.Storage.LockTimeoutDuration(); err != nil {
		return err
	}
	if b == "postgres" {
		if c.Storage.DSN == "" {
			return fmt.Errorf("config: storage.dsn required when backend=postgres")
		}
		if c.Storage.VectorDimension <= 0 {
			return fmt.Errorf("config: storage.vector_dimension required when backend=postgres")
		}
	}
	return nil
}

// Validate checks required fields and values.
func (c *Config) Validate() error {
	if c.Project == "" {
		return fmt.Errorf("config: 'project' is required")
	}
	// SPEC-07 webhooks: fail at load, not at 3am delivery time.
	for i, wh := range c.Serve.Webhooks {
		if wh.URL == "" {
			return fmt.Errorf("config: serve.webhooks[%d].url is required", i)
		}
		switch {
		case wh.SecretEnv != "" && wh.SecretFile != "":
			return fmt.Errorf("config: serve.webhooks[%d]: set exactly one of secret_env, secret_file", i)
		case wh.SecretEnv == "" && wh.SecretFile == "":
			return fmt.Errorf("config: serve.webhooks[%d]: one of secret_env, secret_file is required", i)
		}
		if wh.TimeoutSeconds < 0 {
			return fmt.Errorf("config: serve.webhooks[%d].timeout_seconds must be >= 0", i)
		}
		if wh.MaxRetries != nil && *wh.MaxRetries < 0 {
			return fmt.Errorf("config: serve.webhooks[%d].max_retries must be >= 0", i)
		}
		// SPEC-07 §5 fail-at-startup: a typo'd type name would match
		// nothing and the endpoint would silently receive no events.
		for _, ty := range wh.Types {
			if events.PayloadType(events.Type(ty)) == nil {
				return fmt.Errorf("config: serve.webhooks[%d].types: unknown event type %q", i, ty)
			}
		}
	}
	if b := c.Vectors.Backend; b != "" && b != "memory" && b != "mmap" {
		return fmt.Errorf("config: invalid vectors.backend %q (valid: memory, mmap)", b)
	}
	if t := c.Compiler.Temperature; t != nil && (*t < 0 || *t > 2) {
		return fmt.Errorf("config: invalid compiler.temperature %v (valid: 0-2)", *t)
	}
	if q := c.Vectors.Quantization; q != "" && q != "none" && q != "int8" {
		return fmt.Errorf("config: invalid vectors.quantization %q (valid: none, int8)", q)
	}
	if c.Output == "" {
		return fmt.Errorf("config: 'output' is required")
	}
	if len(c.Sources) == 0 {
		return fmt.Errorf("config: at least one source is required")
	}
	if c.API.Provider != "" {
		validProviders := map[string]bool{
			"anthropic": true, "openai": true, "gemini": true, "ollama": true, "openai-compatible": true, "qwen": true,
		}
		if !validProviders[c.API.Provider] {
			return fmt.Errorf("config: invalid provider %q (valid: anthropic, openai, gemini, ollama, openai-compatible, qwen)", c.API.Provider)
		}
	}
	if c.API.Auth != "" && c.API.Auth != "api_key" {
		if c.API.Auth != "subscription" {
			return fmt.Errorf("config: invalid api.auth %q (valid: api_key, subscription)", c.API.Auth)
		}
		if c.API.Provider == "" {
			return fmt.Errorf("config: subscription auth requires api.provider to be set (openai, anthropic, or gemini)")
		}
		subscriptionProviders := map[string]bool{"openai": true, "anthropic": true, "gemini": true}
		if !subscriptionProviders[c.API.Provider] {
			return fmt.Errorf("config: subscription auth is not supported for provider %q (requires: openai, anthropic, or gemini)", c.API.Provider)
		}
	}
	if mode := c.Trust.IncludeOutputs; mode != "" {
		validModes := map[string]bool{"false": true, "verified": true, "true": true}
		if !validModes[mode] {
			return fmt.Errorf("config: invalid trust.include_outputs %q (valid: \"false\", \"verified\", \"true\")", mode)
		}
	}
	if err := c.validateStorage(); err != nil {
		return err
	}
	// Mirror (SPEC-03): inline secrets are rejected unconditionally; the
	// rest is required only when enabled.
	if c.Mirror.AccessKey != "" || c.Mirror.SecretKey != "" {
		return fmt.Errorf("config: mirror credentials must come from env vars or credentials_file, never inline mirror.access_key/secret_key values")
	}
	if m := &c.Mirror; m.Enabled {
		if m.Endpoint == "" {
			return fmt.Errorf("config: mirror.endpoint required when mirror.enabled")
		}
		if m.Bucket == "" {
			return fmt.Errorf("config: mirror.bucket required when mirror.enabled")
		}
		for name, s := range map[string]string{
			"mirror.ship_interval":         m.ShipInterval,
			"mirror.snapshot_interval":     m.SnapshotInterval,
			"mirror.min_rotation_interval": m.MinRotationInterval,
			"mirror.ship_lock_timeout":     m.ShipLockTimeout,
			"mirror.drain_timeout":         m.DrainTimeout,
		} {
			if s != "" {
				if _, err := time.ParseDuration(s); err != nil {
					return fmt.Errorf("config: invalid %s %q: %w", name, s, err)
				}
			}
		}
		if m.Addressing != "" && m.Addressing != "auto" && m.Addressing != "path" && m.Addressing != "virtual" {
			return fmt.Errorf("config: invalid mirror.addressing %q (valid: auto, path, virtual)", m.Addressing)
		}
		if m.RetainGenerations < 0 {
			return fmt.Errorf("config: mirror.retain_generations must be non-negative")
		}
		if m.MaxConsecutiveDefers < 0 {
			return fmt.Errorf("config: mirror.max_consecutive_defers must be non-negative")
		}
		if m.Encryption.Enabled && m.Encryption.KeyFile == "" {
			return fmt.Errorf("config: mirror.encryption.key_file required when mirror.encryption.enabled")
		}
	}
	if c.Serve.Transport != "" {
		if c.Serve.Transport != "stdio" && c.Serve.Transport != "sse" {
			return fmt.Errorf("config: invalid transport %q (valid: stdio, sse)", c.Serve.Transport)
		}
	}
	// Worker (P2-3): the heartbeat must fire well inside the lease TTL —
	// the manifest-lock invariant (heartbeat << stale < timeout) applied to
	// queue leases. Zero means "use the default" and passes.
	w := c.Serve.Worker
	if w.PollIntervalSeconds < 0 || w.LeaseTTLSeconds < 0 || w.HeartbeatIntervalSeconds < 0 || w.MaxAttempts < 0 || w.ClaimLimit < 0 {
		return fmt.Errorf("config: serve.worker values must be non-negative")
	}
	// Validate the RESOLVED pair: unset TTL defaults to 120s and unset
	// heartbeat to 30s (ResolveWorkerConfig), so raw zeros on either side
	// must not slip the invariant past validation.
	resolvedTTL := w.LeaseTTLSeconds
	if resolvedTTL <= 0 {
		resolvedTTL = 120
	}
	resolvedHeartbeat := w.HeartbeatIntervalSeconds
	if resolvedHeartbeat <= 0 {
		resolvedHeartbeat = 30
	}
	if resolvedHeartbeat >= resolvedTTL {
		return fmt.Errorf("config: serve.worker.heartbeat_interval_seconds (%d) must be less than lease_ttl_seconds (%d)",
			resolvedHeartbeat, resolvedTTL)
	}
	if c.Compiler.Mode != "" {
		validModes := map[string]bool{"standard": true, "batch": true, "auto": true}
		if !validModes[c.Compiler.Mode] {
			return fmt.Errorf("config: invalid compiler.mode %q (valid: standard, batch, auto)", c.Compiler.Mode)
		}
	}
	if c.Compiler.SummaryNaming != "" {
		validNaming := map[string]bool{"full": true, "relative": true}
		if !validNaming[c.Compiler.SummaryNaming] {
			return fmt.Errorf("config: invalid summary_naming %q (valid: full, relative)", c.Compiler.SummaryNaming)
		}
	}
	// Merge relation_types (preferred) and relations (deprecated) keys.
	// If both are set, relation_types takes precedence.
	if len(c.Ontology.RelationTypes) > 0 {
		c.Ontology.Relations = c.Ontology.RelationTypes
		c.Ontology.RelationTypes = nil // normalize to single field
	} else if len(c.Ontology.Relations) > 0 {
		log.Println("config: ontology.relations is deprecated, use ontology.relation_types instead")
	}
	// Functional predicates promise single-valued edges, but the compile-time
	// supersession trigger lives in the triples pass — without it, only
	// manual MCP/CLI adds supersede. Warn rather than silently under-deliver.
	if !c.Ontology.Triples.Enabled && c.Ontology.Temporal.EnabledOrDefault() {
		for _, r := range c.Ontology.Relations {
			if r.Functional {
				log.Printf("config: relation %q is functional but ontology.triples.enabled is false — compile-time supersession is inactive (manual adds still supersede)", r.Name)
			}
		}
	}
	for _, r := range c.Ontology.Relations {
		if r.Name == "" {
			return fmt.Errorf("config: ontology.relation_types: name is required")
		}
		if !typeNameRe.MatchString(r.Name) {
			return fmt.Errorf("config: ontology.relation_types: invalid name %q (must match [a-z][a-z0-9_]*)", r.Name)
		}
	}
	for _, et := range c.Ontology.EntityTypes {
		if et.Name == "" {
			return fmt.Errorf("config: ontology.entity_types: name is required")
		}
		if !typeNameRe.MatchString(et.Name) {
			return fmt.Errorf("config: ontology.entity_types: invalid name %q (must match [a-z][a-z0-9_]*)", et.Name)
		}
	}
	if c.Search.Pipeline != "" && c.Search.Pipeline != "unified" && c.Search.Pipeline != "legacy" {
		return fmt.Errorf("config: search.pipeline must be \"unified\" or \"legacy\", got %q", c.Search.Pipeline)
	}
	if c.Search.ChunkSize != 0 && (c.Search.ChunkSize < 100 || c.Search.ChunkSize > 5000) {
		return fmt.Errorf("config: search.chunk_size must be 100-5000, got %d", c.Search.ChunkSize)
	}
	// Overlap must leave the chunk mostly its own content — at half the chunk
	// size every chunk would be one-third duplicate text, which inflates the
	// index without adding recall.
	if c.Search.ChunkOverlapTokens != 0 {
		if c.Search.ChunkOverlapTokens < 0 {
			return fmt.Errorf("config: search.chunk_overlap_tokens must be >= 0, got %d", c.Search.ChunkOverlapTokens)
		}
		if max := c.Search.ChunkSizeOrDefault() / 2; c.Search.ChunkOverlapTokens > max {
			return fmt.Errorf("config: search.chunk_overlap_tokens must be <= half of search.chunk_size (%d), got %d", max, c.Search.ChunkOverlapTokens)
		}
	}
	for i, ts := range c.TypeSignals {
		if ts.Type == "" {
			return fmt.Errorf("config: type_signals[%d]: type is required", i)
		}
		if len(ts.FilenameKeywords) == 0 && len(ts.ContentKeywords) == 0 && ts.Pattern == "" {
			return fmt.Errorf("config: type_signals[%d] (%s): at least one keyword (filename, content, or pattern) is required", i, ts.Type)
		}
		if len(ts.ContentKeywords) > 0 && ts.MinContentHits <= 0 {
			return fmt.Errorf("config: type_signals[%d] (%s): min_content_hits must be > 0 when content_keywords is set", i, ts.Type)
		}
	}
	if c.Compiler.Timezone != "" {
		loc, err := time.LoadLocation(c.Compiler.Timezone)
		if err != nil {
			return fmt.Errorf("config: invalid compiler.timezone %q: %w", c.Compiler.Timezone, err)
		}
		c.Compiler.resolvedTZ = loc
	}
	// Quality scorer ranges (issue #97). Zero is valid everywhere — it means
	// "use the default" (see QualityThreshold / QualityWeights). Reject only
	// out-of-range values, never zero.
	if t := c.Compiler.Quality.Threshold; t < 0 || t > 1 {
		return fmt.Errorf("config: compiler.quality.threshold must be in [0,1], got %g", t)
	}
	qw := []struct {
		name string
		val  float64
	}{
		{"weight_format", c.Compiler.Quality.WeightFormat},
		{"weight_grounding", c.Compiler.Quality.WeightGrounding},
		{"weight_coverage", c.Compiler.Quality.WeightCoverage},
		{"weight_wikilink", c.Compiler.Quality.WeightWikilink},
		{"weight_antipattern", c.Compiler.Quality.WeightAntiPattern},
	}
	for _, w := range qw {
		if w.val < 0 {
			return fmt.Errorf("config: compiler.quality.%s must be >= 0, got %g", w.name, w.val)
		}
	}
	// SPEC-08 limits: fail at load, not at the first oversized input.
	// Zero values are legal (they resolve to defaults via Resolve).
	lim := []struct {
		name string
		val  int64
	}{
		{"limits.max_doc_bytes", c.Limits.MaxDocBytes},
		{"limits.max_docs_per_capture_batch", c.Limits.MaxDocsPerCaptureBatch},
		{"limits.max_compile_batch", c.Limits.MaxCompileBatch},
		{"limits.max_query_bytes", c.Limits.MaxQueryBytes},
		{"limits.max_graph_traversal_nodes", c.Limits.MaxGraphTraversalNodes},
		{"limits.max_concurrent_provider_calls", c.Limits.MaxConcurrentProviderCalls},
		{"limits.max_concurrent_requests_per_conn", c.Limits.MaxConcurrentRequestsPerConn},
	}
	for _, l := range lim {
		if l.val < 0 {
			return fmt.Errorf("config: %s must be >= 0, got %d", l.name, l.val)
		}
	}
	if c.Limits.ProviderTimeout < 0 {
		return fmt.Errorf("config: limits.provider_timeout must be >= 0, got %v", c.Limits.ProviderTimeout)
	}
	if c.Limits.CompileDocTimeout < 0 {
		return fmt.Errorf("config: limits.compile_doc_timeout must be >= 0, got %v", c.Limits.CompileDocTimeout)
	}
	return nil
}

// IsVaultOverlay returns true if this is a vault overlay project.
func (c *Config) IsVaultOverlay() bool {
	return c.Vault != nil
}

// ResolveOutput returns the absolute output path relative to projectDir.
func (c *Config) ResolveOutput(projectDir string) string {
	if filepath.IsAbs(c.Output) {
		return c.Output
	}
	return filepath.Join(projectDir, c.Output)
}

// ResolveSources returns absolute source paths relative to projectDir.
func (c *Config) ResolveSources(projectDir string) []string {
	paths := make([]string, len(c.Sources))
	for i, s := range c.Sources {
		if filepath.IsAbs(s.Path) {
			paths[i] = s.Path
		} else {
			paths[i] = filepath.Join(projectDir, s.Path)
		}
	}
	return paths
}

// TypeForPath returns the configured Source.Type for the source root that
// contains the given path, or "" if no source matches or the source has no
// explicit type. Both unset ("") and "auto" return "" so callers fall back
// to extension/signal detection.
//
// When multiple configured sources have overlapping paths (e.g. "raw/" and
// "raw/adr/"), the longest matching prefix wins. Path-boundary anchoring
// prevents "raw/adr" from matching files under "raw/adr-old/".
//
// The input path may be absolute or relative to projectDir. Sources with
// relative paths are resolved against projectDir for comparison.
func (c *Config) TypeForPath(projectDir, path string) string {
	if len(c.Sources) == 0 {
		return ""
	}
	absPath := path
	if !filepath.IsAbs(absPath) {
		absPath = filepath.Join(projectDir, path)
	}
	absPath = filepath.Clean(absPath)

	var bestSrc Source
	var bestLen int
	matched := false
	for _, s := range c.Sources {
		srcAbs := s.Path
		if !filepath.IsAbs(srcAbs) {
			srcAbs = filepath.Join(projectDir, s.Path)
		}
		srcAbs = filepath.Clean(srcAbs)

		// path-boundary anchoring: srcAbs is a parent of absPath only if
		// absPath equals srcAbs OR has srcAbs + separator as prefix
		sep := string(filepath.Separator)
		if absPath == srcAbs || strings.HasPrefix(absPath, srcAbs+sep) {
			if len(srcAbs) > bestLen {
				bestLen = len(srcAbs)
				bestSrc = s
				matched = true
			}
		}
	}
	if !matched {
		return ""
	}
	if bestSrc.Type == "" || bestSrc.Type == "auto" {
		return ""
	}
	return bestSrc.Type
}

// expandEnvVars replaces ${VAR} references with environment variable values.
func expandEnvVars(s string) string {
	var result strings.Builder
	i := 0
	for i < len(s) {
		if i+1 < len(s) && s[i] == '$' && s[i+1] == '{' {
			end := strings.Index(s[i:], "}")
			if end != -1 {
				varName := s[i+2 : i+end]
				result.WriteString(os.Getenv(varName))
				i += end + 1
				continue
			}
		}
		result.WriteByte(s[i])
		i++
	}
	return result.String()
}
