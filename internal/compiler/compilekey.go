package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/prompts"
)

// PipelineVersion is bumped by any code change that alters compile output
// (SPEC-04): pipeline stages, inline prompts, serializers, defaults
// resolution. The version participates in every doc's compile key, so a
// bump recompiles with drift reason "pipeline". docs/determinism.md
// documents the contributor duty.
const PipelineVersion = "1"

// CompileKeyParts is the compile_key_parts schema (spec §The compile key):
// the per-component preimages that make drift attribution and --explain
// computable. Templates/Models are empty for tier < 3 (R6 — no LLM passes).
type CompileKeyParts struct {
	Source    string `json:"source"`
	Pipeline  string `json:"pipeline"`
	Templates string `json:"templates"`
	Models    string `json:"models"`
	Config    string `json:"config"`
	Embed     string `json:"embed"`
}

// JSON serializes the parts canonically (struct field order is fixed).
func (p CompileKeyParts) JSON() string {
	b, _ := json.Marshal(p)
	return string(b)
}

// Key computes the final digest from the parts (tier-aware shape per spec
// R6: tier < 3 keys cover only index/embed inputs).
func (p CompileKeyParts) Key(tier int) string {
	var input string
	if tier >= 3 {
		input = "spec04/v1\n" + p.Source + "\n" + p.Pipeline + "\n" +
			p.Templates + "\n" + p.Models + "\n" + p.Config + "\n" + p.Embed
	} else {
		input = "spec04/v1\n" + p.Source + "\n" + p.Pipeline + "\n" + p.Embed + "\n" + p.Config
	}
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])
}

// DriftClass returns the first differing component name between stored and
// current parts ("pipeline", "templates", "models", "config", "embed"),
// "content" when the source component differs, or "" when identical.
// Evaluation order matches the spec's "first differing component" contract.
func DriftClass(stored, current CompileKeyParts) string {
	if stored.Source != current.Source {
		return "content"
	}
	if stored.Pipeline != current.Pipeline {
		return "pipeline"
	}
	if stored.Templates != current.Templates {
		return "templates"
	}
	if stored.Models != current.Models {
		return "models"
	}
	if stored.Config != current.Config {
		return "config"
	}
	if stored.Embed != current.Embed {
		return "embed"
	}
	return ""
}

// KeyContext is the per-run part of the compile key (everything except the
// source hash), computed ONCE — not per doc (review M3: on a 5k-doc
// workspace per-doc computation re-read template files ~30k times).
type KeyContext struct {
	Templates string
	Models    string
	Config    string // tier≥3 full-subset hash
	Chunk     string // tier<3 chunk-subset hash
	Embed     string
}

// NewKeyContext computes the run-level key components. pr may be nil
// (package-default registry). cfg must be the compile-time resolved config
// (per-run overrides already applied by the caller).
func NewKeyContext(cfg *config.Config, pr *prompts.Registry) (*KeyContext, error) {
	return NewKeyContextForMode(cfg, pr, false)
}

// NewKeyContextForMode computes key material for config-backed or injected
// completion mode. injected only changes Tier-3 completion inputs.
func NewKeyContextForMode(cfg *config.Config, pr *prompts.Registry, injected bool) (*KeyContext, error) {
	if pr == nil {
		pr = prompts.DefaultRegistry()
	}
	tmpl, err := templateKeyComponent(pr)
	if err != nil {
		return nil, fmt.Errorf("compile key: templates: %w", err)
	}
	full, err := canonicalSubsetJSON(compileConfigSubsetForMode(cfg, injected))
	if err != nil {
		return nil, fmt.Errorf("compile key: config subset: %w", err)
	}
	chunk, err := canonicalSubsetJSON(chunkConfigSubset(cfg))
	if err != nil {
		return nil, fmt.Errorf("compile key: chunk subset: %w", err)
	}
	return &KeyContext{
		Templates: tmpl,
		Models:    modelKeyComponent(cfg),
		Config:    sha256Hex(full),
		Chunk:     sha256Hex(chunk),
		Embed:     resolveEmbedIdentity(cfg),
	}, nil
}

// Parts assembles the per-doc parts from the run context + source hash
// (tier selects the shape, spec R6).
func (kc *KeyContext) Parts(sourceHash string, tier int) CompileKeyParts {
	parts := CompileKeyParts{
		Source:   sourceHash,
		Pipeline: PipelineVersion,
		Embed:    kc.Embed,
	}
	if tier >= 3 {
		parts.Templates = kc.Templates
		parts.Models = kc.Models
		parts.Config = kc.Config
	} else {
		parts.Config = kc.Chunk
	}
	return parts
}

// ComputeCompileKeyParts resolves every key component for one source doc.
// pr may be nil (package-default registry). cfg must be the compile-time
// resolved config (per-run overrides already applied by the caller, as
// pipeline.go does for CompileOpts.Model/Tier). Errors propagate (Gate-3
// review): key material must never embed error text. Callers handling many
// docs should prefer NewKeyContext + Parts (one computation per run).
func ComputeCompileKeyParts(sourceHash string, tier int, cfg *config.Config, pr *prompts.Registry) (CompileKeyParts, error) {
	return ComputeCompileKeyPartsForMode(sourceHash, tier, cfg, pr, false)
}

// ComputeCompileKeyPartsForMode is ComputeCompileKeyParts with an explicit
// completion mode for engine embedders.
func ComputeCompileKeyPartsForMode(sourceHash string, tier int, cfg *config.Config, pr *prompts.Registry, injected bool) (CompileKeyParts, error) {
	kc, err := NewKeyContextForMode(cfg, pr, injected)
	if err != nil {
		return CompileKeyParts{}, err
	}
	return kc.Parts(sourceHash, tier), nil
}

// templateKeyComponent joins "name@version:hash16" sorted by name.
func templateKeyComponent(pr *prompts.Registry) (string, error) {
	versions := prompts.TemplateVersions()
	hashes, err := prompts.EffectiveTemplateHashes(pr)
	if err != nil {
		return "", err
	}
	names := prompts.CompileTemplateNames()
	entries := make([]string, 0, len(names))
	for _, name := range names {
		entries = append(entries, name+"@"+versions[name]+":"+hashes[name])
	}
	return strings.Join(entries, ","), nil
}

// modelKeyComponent joins "pass=model" sorted by pass, mirroring the
// passes' own fallback chains (fullpipeline.go:144/258/389,
// extract_triples.go:652, resolve_entities.go:1067, communities.go:242).
func modelKeyComponent(cfg *config.Config) string {
	summarize := cfg.Models.Summarize
	if summarize == "" {
		summarize = "gpt-4o-mini"
	}
	extract := cfg.Models.Extract
	if extract == "" {
		extract = summarize
	}
	write := cfg.Models.Write
	if write == "" {
		write = summarize
	}
	triples := cfg.Ontology.Triples.Model
	if triples == "" {
		triples = extract
	}
	resolve := cfg.Ontology.Resolve.Model
	if resolve == "" {
		resolve = extract
	}
	communities := cfg.Ontology.Communities.Model
	if communities == "" {
		communities = extract
	}
	models := map[string]string{
		"summarize": summarize, "extract": extract, "write": write,
		"triples": triples, "resolve": resolve, "communities": communities,
	}
	names := make([]string, 0, len(models))
	for name := range models {
		names = append(names, name)
	}
	sort.Strings(names)
	pairs := make([]string, 0, len(names))
	for _, name := range names {
		pairs = append(pairs, name+"="+models[name])
	}
	return strings.Join(pairs, ",")
}

// resolveEmbedIdentity mirrors embed.NewFromConfig's provider/model/dims
// resolution (embed.go:28-42).
func resolveEmbedIdentity(cfg *config.Config) string {
	provider := cfg.API.Provider
	model := ""
	dims := 0
	if cfg.Embed != nil {
		if cfg.Embed.Provider != "" {
			provider = cfg.Embed.Provider
		}
		model = cfg.Embed.Model
		dims = cfg.Embed.Dimensions
	}
	if model == "" {
		model = embed.DefaultModel(provider)
	}
	if dims == 0 {
		dims = embed.DefaultDimensions(model)
	}
	return provider + ":" + model + ":" + strconv.Itoa(dims)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// canonicalSubsetJSON marshals the subset deterministically: encoding/json
// sorts map keys, so the flat dotted-key map has a canonical byte form.
func canonicalSubsetJSON(subset map[string]any) (string, error) {
	b, err := json.Marshal(subset)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// compileConfigSubset extracts the resolved, output-affecting config values
// as a flat map keyed by yaml leaf path (so the reflection guard can prove
// completeness). Resolution mirrors the passes' own fallbacks; every
// fallback is cited.
func compileConfigSubset(cfg *config.Config) map[string]any {
	c := cfg.Compiler
	s := map[string]any{}

	put := func(path string, v any) { s[path] = v }

	put("language", cfg.Language)
	put("version", cfg.Version)
	put("output", cfg.Output) // article_path strings embed it (review M5)
	put("api.provider", cfg.API.Provider)
	put("api.extra_params", cfg.API.ExtraParams)

	put("compiler.temperature", *c.CompileTemperature())
	put("compiler.summary_max_tokens", intOrDefault(c.SummaryMaxTokens, 2000)) // fullpipeline.go:148
	put("compiler.article_max_tokens", intOrDefault(c.ArticleMaxTokens, 4000)) // fullpipeline.go:393
	put("compiler.extract_max_tokens", intOrDefault(c.ExtractMaxTokens, 8192)) // concepts.go:67
	put("compiler.extract_batch_size", intOrDefault(c.ExtractBatchSize, 20))   // concepts.go:66
	put("compiler.anti_pattern_phrases", c.AntiPatternPhrasesOrDefault())
	put("compiler.article_fields", c.ArticleFields)
	put("compiler.dedup_threshold", floatOrDefault(c.DedupThreshold, 0.85)) // dedup.go:37
	put("compiler.dedup_strategy", strOrDefault(c.DedupStrategy, "embedding"))
	put("compiler.split_threshold", intOrDefault(c.SplitThreshold, 15000)) // write.go:60
	put("compiler.max_source_context_tokens", c.MaxSourceContextTokensOrDefault())
	put("compiler.split_strategy", strOrDefault(c.SplitStrategy, "headings"))
	put("compiler.min_concept_sources", c.MinConceptSourcesOrDefault())
	put("compiler.strip_broken_links", c.StripBrokenLinks)
	put("compiler.summary_naming", c.SummaryNamingOrDefault())
	put("compiler.mode", c.Mode)
	put("compiler.prompt_cache", c.PromptCacheEnabled())
	put("compiler.default_tier", intOrDefault(c.DefaultTier, 3)) // config.go:275 doc
	put("compiler.tier_defaults", c.TierDefaults)
	put("compiler.auto_promote", c.AutoPromoteEnabled())
	put("compiler.auto_demote", c.AutoDemoteEnabled())
	put("compiler.promote_signals.cluster_size", c.PromoteSignals.ClusterSize)
	put("compiler.promote_signals.import_centrality", c.PromoteSignals.ImportCentrality)
	put("compiler.promote_signals.manual_tag", c.PromoteSignals.ManualTag)
	put("compiler.promote_signals.query_hit_count", c.PromoteSignals.QueryHitCount)
	put("compiler.promote_signals.source_recency_days", c.PromoteSignals.SourceRecencyDays)
	put("compiler.demote_signals.source_modified", c.DemoteSignals.SourceModified)
	put("compiler.demote_signals.stale_days", c.DemoteSignals.StaleDays)
	qr := qualityResolved(c)
	put("compiler.quality.threshold", qr.Threshold)
	put("compiler.quality.weight_antipattern", qr.WeightAntiPattern)
	put("compiler.quality.weight_coverage", qr.WeightCoverage)
	put("compiler.quality.weight_format", qr.WeightFormat)
	put("compiler.quality.weight_grounding", qr.WeightGrounding)
	put("compiler.quality.weight_wikilink", qr.WeightWikilink)
	put("compiler.timezone", c.Timezone)

	put("models.summarize", cfg.Models.Summarize)
	put("models.extract", cfg.Models.Extract)
	put("models.write", cfg.Models.Write)

	put("ontology.relations", cfg.Ontology.Relations)
	put("ontology.relation_types", cfg.Ontology.RelationTypes)
	put("ontology.entity_types", cfg.Ontology.EntityTypes)
	put("ontology.triples.enabled", cfg.Ontology.Triples.Enabled)
	put("ontology.triples.max_entities_per_doc", cfg.Ontology.Triples.MaxEntitiesPerDoc)
	put("ontology.triples.max_relations_per_doc", cfg.Ontology.Triples.MaxRelationsPerDoc)
	put("ontology.triples.max_tokens", cfg.Ontology.Triples.MaxTokens)
	put("ontology.triples.model", cfg.Ontology.Triples.Model)
	put("ontology.resolve.enabled", cfg.Ontology.Resolve.Enabled)
	put("ontology.resolve.model", cfg.Ontology.Resolve.Model)
	put("ontology.resolve.auto_apply_threshold", cfg.Ontology.Resolve.AutoApplyThreshold)
	put("ontology.resolve.embed_threshold", cfg.Ontology.Resolve.EmbedThreshold)
	put("ontology.resolve.max_block_size", cfg.Ontology.Resolve.MaxBlockSize)
	put("ontology.resolve.max_embed_candidates", cfg.Ontology.Resolve.MaxEmbedCandidates)
	put("ontology.resolve.max_token_df", cfg.Ontology.Resolve.MaxTokenDF)
	put("ontology.resolve.max_tokens", cfg.Ontology.Resolve.MaxTokens)
	put("ontology.resolve.min_token_df_floor", cfg.Ontology.Resolve.MinTokenDFFloor)
	put("ontology.resolve.use_embeddings", cfg.Ontology.Resolve.UseEmbeddings)
	put("ontology.communities.enabled", cfg.Ontology.Communities.Enabled)
	put("ontology.communities.model", cfg.Ontology.Communities.Model)
	put("ontology.communities.max_communities", cfg.Ontology.Communities.MaxCommunitiesOrDefault())
	put("ontology.communities.max_tokens", cfg.Ontology.Communities.MaxTokensOrDefault())
	put("ontology.communities.min_members", cfg.Ontology.Communities.MinMembersOrDefault())
	put("ontology.temporal.enabled", cfg.Ontology.Temporal.EnabledOrDefault())
	put("ontology.temporal.auto_apply_threshold", cfg.Ontology.Temporal.AutoApplyThresholdOrDefault())

	put("parsers.external", cfg.Parsers.External)
	put("parsers.trust_external", cfg.Parsers.TrustExternal)

	put("search.chunk_size", cfg.Search.ChunkSizeOrDefault())
	put("search.chunk_overlap_tokens", cfg.Search.ChunkOverlapOrDefault())

	put("trust.auto_promote", cfg.Trust.AutoPromote)
	put("trust.consensus_threshold", cfg.Trust.ConsensusThresholdOrDefault())
	put("trust.grounding_threshold", cfg.Trust.GroundingThresholdOrDefault())
	put("trust.include_outputs", cfg.Trust.IncludeOutputs)
	put("trust.similarity_threshold", cfg.Trust.SimilarityThresholdOrDefault())

	put("type_signals", cfg.TypeSignals)

	put("vectors.backend", strOrDefault(cfg.Vectors.Backend, "memory"))
	put("vectors.quantization", strOrDefault(cfg.Vectors.Quantization, "none"))

	return s
}

func compileConfigSubsetForMode(cfg *config.Config, injected bool) map[string]any {
	subset := compileConfigSubset(cfg)
	if !injected {
		return subset
	}
	for _, key := range []string{
		"api.provider",
		"api.extra_params",
		"compiler.temperature",
		"compiler.mode",
		"compiler.prompt_cache",
	} {
		delete(subset, key)
	}
	subset["completion.mode"] = "injected"
	return subset
}

// chunkConfigSubset is the tier < 3 key's config component (spec R6):
// only the fields that shape index artifacts.
func chunkConfigSubset(cfg *config.Config) map[string]any {
	return map[string]any{
		"search.chunk_size":           cfg.Search.ChunkSizeOrDefault(),
		"search.chunk_overlap_tokens": cfg.Search.ChunkOverlapOrDefault(),
		"parsers.external":            cfg.Parsers.External,
		"parsers.trust_external":      cfg.Parsers.TrustExternal,
		"type_signals":                cfg.TypeSignals,
	}
}

// qualityResolved delegates to the config accessors (one implementation —
// ground rule 2; the mirror here was the Gate-2 finding).
func qualityResolved(c config.CompilerConfig) config.QualityConfig {
	format, grounding, coverage, wikilink, antiPattern := c.QualityWeights()
	return config.QualityConfig{
		Threshold:         c.QualityThreshold(),
		WeightFormat:      format,
		WeightGrounding:   grounding,
		WeightCoverage:    coverage,
		WeightWikilink:    wikilink,
		WeightAntiPattern: antiPattern,
	}
}

func intOrDefault(v, d int) int {
	if v <= 0 {
		return d
	}
	return v
}

func floatOrDefault(v, d float64) float64 {
	if v <= 0 {
		return d
	}
	return v
}

func strOrDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// subsetPolicy is THE disposition table for the reflection guard: every
// config leaf path is either in the compile key (subset / models / embed
// component) or explicitly omitted with a one-line justification.
// Dispositions: "" = include in config_key subset; "models" / "embed" =
// carried by that key component instead; anything else = exclusion reason.
var subsetPolicy = map[string]string{
	"language":         "",
	"version":          "",
	"api.provider":     "",
	"api.extra_params": "",
	"api.api_key":      "secret, not output identity",
	"api.auth":         "auth mechanism, not output content",
	"api.base_url":     "endpoint location, not output identity",
	"api.rate_limit":   "runtime throttling, no artifact effect",

	"models.summarize": "models",
	"models.extract":   "models",
	"models.write":     "models",
	"models.lint":      "lint is not a compile pass",
	"models.query":     "query is not a compile pass",

	"embed.provider":   "embed",
	"embed.model":      "embed",
	"embed.dimensions": "embed",
	"embed.api_key":    "secret",
	"embed.base_url":   "endpoint, not identity",
	"embed.rate_limit": "runtime throttling",

	"compiler.anti_pattern_phrases":                "",
	"compiler.article_fields":                      "",
	"compiler.article_max_tokens":                  "",
	"compiler.auto_demote":                         "",
	"compiler.auto_promote":                        "",
	"compiler.dedup_strategy":                      "",
	"compiler.dedup_threshold":                     "",
	"compiler.default_tier":                        "",
	"compiler.demote_signals.source_modified":      "",
	"compiler.demote_signals.stale_days":           "",
	"compiler.extract_batch_size":                  "",
	"compiler.extract_max_tokens":                  "",
	"compiler.min_concept_sources":                 "",
	"compiler.mode":                                "",
	"compiler.prompt_cache":                        "",
	"compiler.promote_signals.cluster_size":        "",
	"compiler.promote_signals.import_centrality":   "",
	"compiler.promote_signals.manual_tag":          "",
	"compiler.promote_signals.query_hit_count":     "",
	"compiler.promote_signals.source_recency_days": "",
	"compiler.quality.threshold":                   "",
	"compiler.quality.weight_antipattern":          "",
	"compiler.quality.weight_coverage":             "",
	"compiler.quality.weight_format":               "",
	"compiler.quality.weight_grounding":            "",
	"compiler.quality.weight_wikilink":             "",
	"compiler.split_strategy":                      "",
	"compiler.split_threshold":                     "",
	"compiler.strip_broken_links":                  "",
	"compiler.summary_max_tokens":                  "",
	"compiler.summary_naming":                      "",
	"compiler.temperature":                         "",
	"compiler.tier_defaults":                       "",
	"compiler.timezone":                            "",
	"compiler.max_parallel":                        "runtime scheduling; D3 makes output order-independent",
	"compiler.max_source_context_tokens":           "",
	"compiler.debounce_seconds":                    "watch-mode runtime knob",
	"compiler.auto_commit":                         "git side effect, not artifact content",
	"compiler.auto_lint":                           "lint trigger, not compile output",
	"compiler.backpressure":                        "runtime rate control",
	"compiler.batch_threshold":                     "mode selection timing, not content",
	"compiler.estimate_before":                     "UX prompt, not content",
	"compiler.price_table":                         "cost accounting (usage ledger is volatile state)",
	"compiler.token_price_per_million":             "cost accounting",

	"ontology.relations":                     "",
	"ontology.relation_types":                "",
	"ontology.entity_types":                  "",
	"ontology.graph_query.max_edges":         "graph query is a read path, not compile",
	"ontology.graph_query.max_hops":          "graph query is a read path, not compile",
	"ontology.triples.enabled":               "",
	"ontology.triples.max_entities_per_doc":  "",
	"ontology.triples.max_relations_per_doc": "",
	"ontology.triples.max_tokens":            "",
	"ontology.triples.model":                 "models",
	"ontology.resolve.enabled":               "",
	"ontology.resolve.model":                 "models",
	"ontology.resolve.auto_apply_threshold":  "",
	"ontology.resolve.embed_threshold":       "",
	"ontology.resolve.max_block_size":        "",
	"ontology.resolve.max_embed_candidates":  "",
	"ontology.resolve.max_token_df":          "",
	"ontology.resolve.max_tokens":            "",
	"ontology.resolve.min_token_df_floor":    "",
	"ontology.resolve.use_embeddings":        "",
	"ontology.communities.enabled":           "",
	"ontology.communities.model":             "models",
	"ontology.communities.max_communities":   "",
	"ontology.communities.max_tokens":        "",
	"ontology.communities.min_members":       "",
	"ontology.temporal.enabled":              "",
	"ontology.temporal.auto_apply_threshold": "",

	"parsers.external":       "",
	"parsers.trust_external": "",

	"search.chunk_size":             "",
	"search.chunk_overlap_tokens":   "",
	"search.ann.enabled":            "in-memory ANN structure, never persisted",
	"search.context_max_tokens":     "query-time assembly",
	"search.default_limit":          "query-time",
	"search.graph_depth":            "query-time",
	"search.graph_expansion":        "query-time",
	"search.graph_max_expand":       "query-time",
	"search.graph_relation_weights": "query-time",
	"search.hybrid_weight_bm25":     "query-time",
	"search.hybrid_weight_graph":    "query-time",
	"search.hybrid_weight_vector":   "query-time",
	"search.pipeline":               "query-time",
	"search.query_expansion":        "query-time",
	"search.rerank":                 "query-time",
	"search.rerank_min_coverage":    "query-time",
	"search.result_max_chars":       "query-time",
	"search.weight_common_neighbor": "query-time",
	"search.weight_direct_link":     "query-time",
	"search.weight_source_overlap":  "query-time",
	"search.weight_type_affinity":   "query-time",

	"trust.auto_promote":         "",
	"trust.consensus_threshold":  "",
	"trust.grounding_threshold":  "",
	"trust.include_outputs":      "",
	"trust.similarity_threshold": "",

	"type_signals": "",

	"vectors.backend":      "",
	"vectors.quantization": "",

	"description": "project metadata, not output",
	"project":     "project metadata, not output",
	"extends":     "config inheritance mechanism; resolved values are already in the key",
	"ignore":      "membership is Diff's domain (a newly-included file arrives as Added)",
	"output":      "",
	"sources":     "source type changes are Diff's Modified classification (diff.go:121-128)",
	"vault.root":  "hub/vault runtime",

	"linting.auto_fix_passes":          "lint is not compile",
	"linting.staleness_threshold_days": "lint is not compile",

	"storage.backend":          "storage location, not content",
	"storage.dsn":              "storage location",
	"storage.lock_timeout":     "runtime",
	"storage.pool":             "runtime",
	"storage.vector_dimension": "storage layout of the same vectors; SWVI format is vectors.backend's domain",

	"serve.allowed_host":                      "serve runtime",
	"serve.metrics":                           "serve runtime",
	"serve.port":                              "serve runtime",
	"serve.token":                             "serve auth secret",
	"serve.transport":                         "serve runtime",
	"serve.worker.claim_limit":                "queue runtime",
	"serve.worker.enabled":                    "queue runtime",
	"serve.worker.heartbeat_interval_seconds": "queue runtime",
	"serve.worker.lease_ttl_seconds":          "queue runtime",
	"serve.worker.max_attempts":               "queue runtime",
	"serve.worker.poll_interval_seconds":      "queue runtime",
	"serve.webhooks":                          "event delivery (SPEC-07), not compile output",

	// SPEC-07: the event stream is an observability side-channel — none of
	// these change compiled artifacts.
	"events.enable":      "event emission switch, not compile output",
	"events.dir":         "audit-trail location, not compile output",
	"events.buffer_size": "event bus runtime",
	"events.stdout":      "event tee, not compile output",
	"events.raw_queries": "search-event privacy opt-in, not compile output",

	// SPEC-08 limits: runtime resource caps — they bound work, they do not
	// change what a compile produces (same class as api.rate_limit). The
	// walker sees internal/limits.Limits as one leaf (external package).
	"limits": "runtime resource caps (SPEC-08), no artifact effect",

	"mirror.access_key":             "mirror subsystem (backup), not compile output",
	"mirror.access_key_env":         "mirror subsystem",
	"mirror.addressing":             "mirror subsystem",
	"mirror.bucket":                 "mirror subsystem",
	"mirror.credentials_file":       "mirror subsystem",
	"mirror.drain_timeout":          "mirror subsystem",
	"mirror.enabled":                "mirror subsystem",
	"mirror.encryption.enabled":     "mirror subsystem",
	"mirror.encryption.key_file":    "mirror subsystem",
	"mirror.endpoint":               "mirror subsystem",
	"mirror.max_consecutive_defers": "mirror subsystem",
	"mirror.min_rotation_interval":  "mirror subsystem",
	"mirror.prefix":                 "mirror subsystem",
	"mirror.region":                 "mirror subsystem",
	"mirror.retain_generations":     "mirror subsystem",
	"mirror.secret_key":             "mirror subsystem",
	"mirror.secret_key_env":         "mirror subsystem",
	"mirror.session_token_env":      "mirror subsystem",
	"mirror.ship_interval":          "mirror subsystem",
	"mirror.ship_lock_timeout":      "mirror subsystem",
	"mirror.snapshot_interval":      "mirror subsystem",
}

// policyDispositionsForTest exposes the policy to the guard test.
func policyDispositionsForTest() map[string]string { return subsetPolicy }

// configLeafPaths walks config.Config via yaml tags and returns every leaf
// path (the reflection guard's universe).
func configLeafPaths() []string {
	var out []string
	walkConfigLeaves(reflect.TypeOf(config.Config{}), "", &out)
	sort.Strings(out)
	return out
}

func walkConfigLeaves(v reflect.Type, prefix string, out *[]string) {
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Tag.Get("yaml")
		for j, c := range name {
			if c == ',' {
				name = name[:j]
				break
			}
		}
		if name == "" || name == "-" {
			name = f.Name
		}
		path := prefix + name
		t := f.Type
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() == reflect.Struct && t.PkgPath() == "github.com/xoai/sage-wiki/internal/config" {
			walkConfigLeaves(t, path+".", out)
		} else {
			*out = append(*out, path)
		}
	}
}
