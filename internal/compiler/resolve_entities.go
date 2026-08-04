package compiler

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/embed"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
	"github.com/xoai/sage-wiki/internal/metrics"
	"github.com/xoai/sage-wiki/internal/ontology"
	"github.com/xoai/sage-wiki/internal/prompts"
	"github.com/xoai/sage-wiki/internal/store"
	"github.com/xoai/sage-wiki/internal/vectors"
	"github.com/xoai/sage-wiki/pkg/events"
)

// Claude-driven entity resolution (P3-3, GRAPH-03).
//
// Surface-form variants of one entity fracture the graph: internal/ontology
// keys entities by id, and the compiler writes the SAME concept under two
// different ids — write.go writes the hyphen-slug with an article and no
// description, the triples pass writes the model's raw string with a description
// and no article. This pass links them so the canonical carries the union of
// their edges.
//
// It LINKS; it does not collapse. Nothing is ever deleted. See
// internal/ontology/aliases.go LinkAlias for why.

// resolvedCluster is one group the model returned. It deliberately carries NO
// canonical: Go elects that (electCanonical). A model-nominated canonical that
// Go then overrode would make the Broader answer certify a direction that was
// never used, and could fold a broad concept into a specific one — the exact
// inversion of what Broader exists to prevent.
type resolvedCluster struct {
	Members      []string `json:"members"`
	SameReferent bool     `json:"same_referent"`
	Broader      bool     `json:"broader"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason"`
}

type resolveResponse struct {
	Clusters []resolvedCluster `json:"clusters"`
}

// ResolveSchema constrains the structured-output response (P3-3).
//
// No enums, for the reason TriplesSchema documents: StructuredCompletion's
// fallback path validates the schema as strictly as the native one, and a
// violation fails the WHOLE call — so one bad value would cost the entire
// block's clustering rather than one cluster.
//
// `required` must be a Go []string: schema.go type-asserts it, and a []any
// silently skips ALL required-field validation.
var ResolveSchema = llm.JSONSchema{
	Name:        "clusters",
	Description: "groups of candidate labels that denote the same entity",
	Schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"clusters": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"members": map[string]any{
							"type":  "array",
							"items": map[string]any{"type": "string"},
						},
						"same_referent": map[string]any{"type": "boolean"},
						"broader":       map[string]any{"type": "boolean"},
						"confidence":    map[string]any{"type": "number"},
						"reason":        map[string]any{"type": "string"},
					},
					"required": []string{"members", "same_referent", "broader", "confidence", "reason"},
				},
			},
		},
		"required": []string{"clusters"},
	},
}

// Defaults, applied in-function because config.Defaults() has no Ontology entry
// and is only reached through config.Load — a Config{} literal (routine in this
// package's tests, and the shape a zero-valued ResolveConfig arrives in) yields
// zeros. These are not cosmetic: a zero MaxBlockSize makes the per-seed
// candidate cap negative, so every block would be empty.
const maxResolveChain = 32

// maxSweepPasses bounds the sweep's fixpoint replay.
//
// A chain of N links needs N passes to propagate and one more to OBSERVE that
// nothing changed, so the bound is maxAliasChain + 1 rather than the 32 that
// ontology.maxAliasChain places on following a chain. At 32 a 32-link chain
// exited on the bound without ever confirming a fixpoint — correct by luck.
const maxSweepPasses = 33

const (
	defaultResolveMaxTokens          = 4096
	defaultResolveMaxBlockSize       = 60
	defaultResolveAutoApplyThreshold = 0.85
	defaultResolveMaxTokenDF         = 0.05
	defaultResolveMinTokenDFFloor    = 20
	defaultResolveEmbedThreshold     = 0.82
	defaultResolveMaxEmbedCandidates = 500
)

func applyResolveDefaults(c config.ResolveConfig) config.ResolveConfig {
	if c.MaxTokens <= 0 {
		c.MaxTokens = defaultResolveMaxTokens
	}
	// Floored at 2, not 1: a block of one cannot produce a cluster
	// (normalizeClusters requires two members), so max_block_size: 1 would buy a
	// paid arbitration call per seed that can never return anything.
	if c.MaxBlockSize < 2 {
		c.MaxBlockSize = defaultResolveMaxBlockSize
	}
	// Falls BACK rather than clamping. Clamping a configured 0 to some small
	// epsilon would still auto-apply near-zero-confidence proposals; the only
	// safe reading of an out-of-range threshold is "unset". Since the default
	// moved to 0.85 the fallback lands on the PERMISSIVE side, so an explicit
	// out-of-range value (a typo like 1.5 meant as "stricter than 1.0") warns;
	// unset (0) is every default user and stays silent.
	if c.AutoApplyThreshold <= 0 || c.AutoApplyThreshold > 1 {
		if c.AutoApplyThreshold != 0 {
			log.Warn("resolve: auto_apply_threshold out of range (0,1] — "+
				"falling back to the default, which auto-applies",
				"configured", c.AutoApplyThreshold,
				"default", defaultResolveAutoApplyThreshold)
		}
		c.AutoApplyThreshold = defaultResolveAutoApplyThreshold
	}
	if c.MaxTokenDF <= 0 {
		c.MaxTokenDF = defaultResolveMaxTokenDF
	}
	if c.MinTokenDFFloor <= 0 {
		c.MinTokenDFFloor = defaultResolveMinTokenDFFloor
	}
	if c.EmbedThreshold <= 0 {
		c.EmbedThreshold = defaultResolveEmbedThreshold
	}
	if c.MaxEmbedCandidates <= 0 {
		c.MaxEmbedCandidates = defaultResolveMaxEmbedCandidates
	}
	return c
}

// nameStopwords are dropped before blocking. They are frequent enough to link
// unrelated entities and carry no identifying signal.
var nameStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "from": true,
	"into": true, "onto": true, "over": true, "under": true, "of": true,
	"in": true, "on": true, "at": true, "to": true, "by": true, "as": true,
	"an": true, "or": true, "is": true, "it": true, "its": true, "a": true,
}

// normalizeNameTokens lowercases a display NAME and splits it into blocking
// tokens.
//
// It runs over Name, not id, deliberately: the two rows this pass exists to
// link have different ids for the same concept ("self-attention" from write.go,
// "Self Attention" from the triples pass) but normalize to the same tokens.
//
// Single characters are dropped — they carry no signal and would block a large
// fraction of any vault against each other.
func normalizeNameTokens(name string) []string {
	lowered := strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return unicode.ToLower(r)
		default:
			return ' '
		}
	}, name)

	var out []string
	for _, tok := range strings.Fields(lowered) {
		if len(tok) < 2 || nameStopwords[tok] {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// discriminatingTokens returns the tokens that are specific enough to block on.
//
// A token is discarded when it appears in more than max(floor, pct * len(pool))
// entities of its type. BOTH bounds are required, and the floor is the one that
// is easy to leave out: three variants of one name among 45 concepts is 6.7%,
// over a 5% threshold, so a percentage-only filter discards exactly the token
// that identifies a small cluster and the pass silently links nothing. The
// percentage is what keeps a large vault from blocking on "model" or "data".
func discriminatingTokens(pool []store.Entity, maxDF float64, floor int) map[string]map[string]bool {
	byType := map[string][]store.Entity{}
	for _, e := range pool {
		byType[e.Type] = append(byType[e.Type], e)
	}

	out := make(map[string]map[string]bool, len(byType))
	for typ, entities := range byType {
		df := map[string]int{}
		for _, e := range entities {
			seen := map[string]bool{}
			for _, tok := range normalizeNameTokens(e.Name) {
				if seen[tok] {
					continue
				}
				seen[tok] = true
				df[tok]++
			}
		}
		// Per TYPE, not over the merged pool: 400 techniques named "attention
		// variant N" would otherwise push "attention" over the limit and stop
		// three concepts named "Self Attention" from ever blocking — the exact
		// failure the floor was added to prevent, reintroduced across type
		// boundaries.
		limit := int(maxDF * float64(len(entities)))
		if limit < floor {
			limit = floor
		}
		ok := make(map[string]bool, len(df))
		for tok, n := range df {
			if n <= limit {
				ok[tok] = true
			}
		}
		out[typ] = ok
	}
	return out
}

// electCanonical picks the entity that will accumulate the cluster's edges.
//
// The choice is Go's, never the model's. Order:
//  1. a non-empty ArticlePath — that row is what users land on and what the
//     query and web layers render, so it must be the complete node;
//  2. the NEWEST CreatedAt, so an incremental run prefers the established row;
//  3. sorted id, so the outcome never depends on map iteration.
//
// An empty CreatedAt sorts OLDEST. The column is nullable on both backends and
// reads back "", and under newest-wins treating "" as newest would let any
// undated legacy row win every election unconditionally.
func electCanonical(members []store.Entity) store.Entity {
	best := members[0]
	for _, e := range members[1:] {
		if betterCanonical(e, best) {
			best = e
		}
	}
	return best
}

func betterCanonical(a, b store.Entity) bool {
	aHas, bHas := a.ArticlePath != "", b.ArticlePath != ""
	if aHas != bHas {
		return aHas
	}
	if a.CreatedAt != b.CreatedAt {
		// "" is oldest, so anything non-empty beats it.
		if a.CreatedAt == "" {
			return false
		}
		if b.CreatedAt == "" {
			return true
		}
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID < b.ID
}

// warnPendingBacklog reports the STANDING queue — not this run's delta.
//
// stats is fresh per run, so a delta would fire exactly once, on the run that
// queued the row, and stay silent forever after while the backlog stands. The
// user who most needs the message is the one with proposals queued weeks ago
// compiling in CI, and a delta gives them nothing.
//
// The message says only what this function can know: the count and the command.
// It has no access to what the run did, and under a lowered threshold a run that
// auto-links would make any such claim false.
func warnPendingBacklog(ont store.OntologyStore) {
	pending, err := ont.ListAliases(store.AliasPending)
	if err != nil {
		// Never `pending, _ :=`: a failed read would be indistinguishable from an
		// empty queue, which is the precise silence this warning exists to break.
		log.Warn("resolve: cannot read pending proposals, backlog unknown", "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	log.Warn("resolve: entity-resolution proposals are waiting for review — "+
		"run `sage-wiki ontology resolve --review` to decide them",
		"pending", len(pending))
}

// canAutoApply decides whether a proposed link is applied without review.
//
// The threshold defaults to 0.85 — auto-apply is the default behaviour now
// that --unlink makes a mistaken link exactly reversible. An operator who
// wants review-only sets an explicit 1.0, which the first branch below turns
// into "never, exactly".
//
// The description requirement is the one that matters most among them. Concept
// entities carry no Definition (write.go) unless the triple-extraction pass ran,
// so without it a lowered threshold would link entities on surface-name
// similarity with no grounded evidence at all.
//
// ONE description suffices, not two: no writer in this codebase puts both an
// ArticlePath and a Definition on the same row, and the described row and the
// article-bearing row are exactly the pair this pass links. Requiring both would
// make auto-apply a branch that can never fire.
// threshold: a test passing the package default asserts nothing about the
// guards below this line — see resolveCfg in resolve_pass_test.go.
func canAutoApply(c resolvedCluster, x, y store.Entity, threshold float64) bool {
	// 1.0 means NEVER, exactly — the guarantee outlives the review-only
	// default that motivated it. An operator who sets 1.0 asks for review-only;
	// normalizeClusters clamps confidence to [0,1], so a model returning 1.0
	// would otherwise satisfy `confidence >= threshold` and beat that request —
	// a review-only setting a model can defeat by being confident is not a
	// setting at all.
	//
	// >= 1.0, not an epsilon form: applyResolveDefaults guarantees (0,1], 1.0 is
	// exactly representable, and an epsilon would silently disarm a deliberate
	// 0.99. This branch cannot move to applyResolveDefaults, which has no way to
	// express "never" without a sentinel outside (0,1] that it rejects.
	if threshold >= 1.0 {
		return false
	}
	if !c.SameReferent || c.Broader {
		return false
	}
	if c.Confidence < threshold {
		return false
	}
	return strings.TrimSpace(x.Definition) != "" || strings.TrimSpace(y.Definition) != ""
}

// normalizedCluster is a cluster after label resolution and the guards.
type normalizedCluster struct {
	entities   []store.Entity
	confidence float64
	broader    bool
	reason     string
	raw        resolvedCluster
}

// normalizeClusters maps the model's labels back to entities and applies the
// response guards, in order. Every drop is a decision the caller counts and
// logs; nothing here is silent.
//
// A label the model omits entirely is simply never seen again — the pass acts
// only on what the model placed, so an omitted entity cannot be affected. That
// is the dropped-name guarantee, and it needs no code beyond not inventing work.
func normalizeClusters(raw []resolvedCluster, labels map[string]store.Entity, stats *resolveStats) []normalizedCluster {
	if stats == nil {
		stats = &resolveStats{}
	}
	var out []normalizedCluster
	claimed := map[string]bool{} // label -> already in an earlier cluster

	for _, c := range raw {
		// A cluster the model did not vouch for is not a proposal.
		if !c.SameReferent {
			stats.droppedNotReferent++
			continue
		}

		var members []store.Entity
		seen := map[string]bool{}
		for _, label := range c.Members {
			e, ok := labels[label]
			if !ok {
				// Hallucinated label — the model returned something outside the
				// block it was given.
				stats.droppedUnknownLabel++
				continue
			}
			if seen[label] || claimed[label] {
				// Repeated within this cluster, or already claimed by an earlier
				// one. First cluster wins, by the order the model returned, so a
				// re-run is deterministic.
				stats.droppedDuplicate++
				continue
			}
			seen[label] = true
			members = append(members, e)
		}
		if len(members) < 2 {
			stats.droppedSingleton++
			continue
		}
		for label := range seen {
			claimed[label] = true
		}

		conf := c.Confidence
		switch {
		case conf > 1:
			conf = 1
		case conf < 0:
			conf = 0
		}

		// Deterministic member order so election tie-breaks and logging do not
		// depend on the model's ordering.
		sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })

		out = append(out, normalizedCluster{
			entities:   members,
			confidence: conf,
			broader:    c.Broader,
			reason:     strings.TrimSpace(c.Reason),
			raw:        c,
		})
	}

	// Labels the model never placed in a surviving cluster. Nothing acts on
	// them — that IS the dropped-name guarantee — but they are counted so the
	// run's log accounts for every candidate offered.
	placed := map[string]bool{}
	for _, c := range out {
		for _, e := range c.entities {
			placed[e.ID] = true
		}
	}
	for _, e := range labels {
		if !placed[e.ID] {
			stats.unplacedLabels++
		}
	}
	return out
}

// resolveBlock is one arbitration unit: a touched seed plus the pool entities
// that might denote the same thing.
//
// Blocks are SEED-CENTRIC, not connected components. Every block therefore
// contains at least one touched entity, so no call is ever spent on a group
// that cannot produce a link. Two blocks may overlap; the pass resolves that
// with a per-run proposed set, which is far cheaper than making them disjoint
// and avoids the giant-component problem a shared token like "model" creates.
type resolveBlock struct {
	seed    store.Entity
	members []store.Entity          // seed + candidates, sorted by id
	labels  map[string]store.Entity // opaque label -> entity
}

// buildBlocks groups each touched entity with its same-type candidates.
//
// A candidate qualifies on any of: a shared discriminating token, an equal
// normalized full form, or — when embeddings are on — cosine >= threshold.
// Pairs `rejected` returns true for contribute nothing: re-offering a pair a
// human separated wastes a call and risks re-linking it.
//
// vecs may be nil (embeddings off, or the embedder failed); the lexical signals
// stand on their own.
func buildBlocks(
	touched, pool []store.Entity,
	tokens map[string]map[string]bool,
	vecs map[string][]float32,
	cfg config.ResolveConfig,
	rejected func(a, b string) bool,
) []resolveBlock {
	byType := map[string][]store.Entity{}
	for _, e := range pool {
		byType[e.Type] = append(byType[e.Type], e)
	}

	// Precompute once per pool entity rather than per (seed, candidate) pair.
	norm := make(map[string][]string, len(pool))
	full := make(map[string]string, len(pool))
	for _, e := range pool {
		toks := normalizeNameTokens(e.Name)
		norm[e.ID] = toks
		full[e.ID] = strings.Join(toks, " ")
	}

	seeds := append([]store.Entity(nil), touched...)
	sort.Slice(seeds, func(i, j int) bool { return seeds[i].ID < seeds[j].ID })

	var blocks []resolveBlock
	for _, seed := range seeds {
		var candidates []store.Entity
		for _, c := range byType[seed.Type] {
			if c.ID == seed.ID {
				continue
			}
			// candidateMatches is pure and in-memory; rejected() is a database
			// round-trip. Testing the cheap predicate FIRST turns an
			// O(seeds x type_size) query storm — ~50k lookups for 50 seeds over
			// a 1000-entity pool — into one lookup per actual candidate.
			if !candidateMatches(seed, c, norm, full, tokens[seed.Type], vecs, cfg) {
				continue
			}
			if rejected(seed.ID, c.ID) {
				continue
			}
			candidates = append(candidates, c)
		}
		// Nothing to arbitrate: this is the incremental-cost property — a new,
		// unambiguous entity costs zero LLM calls.
		if len(candidates) == 0 {
			continue
		}

		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
		if limit := cfg.MaxBlockSize - 1; len(candidates) > limit {
			log.Warn("resolve: candidate cap reached, dropping the tail",
				"seed", seed.ID, "kept", limit, "dropped", len(candidates)-limit)
			candidates = candidates[:limit]
		}

		members := append([]store.Entity{seed}, candidates...)
		sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })

		labels := make(map[string]store.Entity, len(members))
		for i, m := range members {
			labels[fmt.Sprintf("E%d", i+1)] = m
		}
		blocks = append(blocks, resolveBlock{seed: seed, members: members, labels: labels})
	}
	return blocks
}

func candidateMatches(
	seed, c store.Entity,
	norm map[string][]string,
	full map[string]string,
	tokens map[string]bool,
	vecs map[string][]float32,
	cfg config.ResolveConfig,
) bool {
	if f := full[seed.ID]; f != "" && f == full[c.ID] {
		return true
	}
	for _, t := range norm[seed.ID] {
		if !tokens[t] {
			continue
		}
		for _, u := range norm[c.ID] {
			if t == u {
				return true
			}
		}
	}
	if !cfg.UseEmbeddings || vecs == nil {
		return false
	}
	a, b := vecs[seed.ID], vecs[c.ID]
	// A dimension mismatch is realistic mid-run (an embedder or model swap) and
	// must skip the pair, never panic on a short slice.
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return false
	}
	return vectors.CosineSimilarity(a, b) >= cfg.EmbedThreshold
}

// renderBlockMembers formats a block for the prompt.
//
// Labels and display names only — never an id. The model must not learn or echo
// ids: Entity.ID != Entity.Name, two rows can share a Name, and a label keeps
// the mapping back to entities total and unambiguous.
func renderBlockMembers(b resolveBlock) string {
	labels := make([]string, 0, len(b.labels))
	for l := range b.labels {
		labels = append(labels, l)
	}
	sort.Slice(labels, func(i, j int) bool { return labelIndex(labels[i]) < labelIndex(labels[j]) })

	var sb strings.Builder
	for _, l := range labels {
		e := b.labels[l]
		desc := strings.TrimSpace(e.Definition)
		if desc == "" {
			desc = "(none)"
		}
		fmt.Fprintf(&sb, "%s  name: %s  type: %s  desc: %s\n", l, e.Name, e.Type, desc)
	}
	return sb.String()
}

func labelIndex(label string) int {
	n := 0
	for _, r := range label {
		if r >= '0' && r <= '9' {
			n = n*10 + int(r-'0')
		}
	}
	return n
}

// ResolveEntitiesPass links surface-form variants of one entity so the canonical
// carries the union of the cluster's edges (P3-3, GRAPH-03).
//
// It NEVER returns an error and never fails a compile — additive enrichment,
// the same contract ExtractTriplesPass carries. Every failure is counted and
// logged.
//
// Two halves with different cost profiles:
//   - the SWEEP re-applies already-approved links. Free, and deliberately
//     UNGATED by resolve.enabled: disabling the feature must not silently stop
//     the canonical staying complete as new edges land on an alias.
//   - ARBITRATION asks the model about new candidates. Costs money, so it is
//     gated, and it is skipped entirely when nothing was touched.
func ResolveEntitiesPass(
	ctx context.Context,
	ont store.OntologyStore,
	touched []string,
	cfg *config.Config,
	client *llm.Client,
	embedder embed.Embedder,
	pr *prompts.Registry,
	sinks ...events.Sink,
) {
	// SPEC-07: optional event sink (variadic keeps the ~40 existing call
	// sites untouched); nil = no events.
	var sink events.Sink
	if len(sinks) > 0 {
		sink = sinks[0]
	}
	// cfg == nil FIRST: cfg.Ontology on a nil *Config panics, and the
	// fullpipeline call site can hand over a partially-built config.
	if cfg == nil || ont == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

	// Registered HERE — above the Resolve.Enabled gate and BEFORE the sweep —
	// and the placement is the item, not a detail:
	//   - above the gate, because a user who turns resolve OFF with proposals
	//     standing would otherwise be silenced permanently while those aliases
	//     stay frozen out of resolution; and because len(touched) == 0 is the
	//     ORDINARY case under `resolve: on, triples: off`. sweepAliases sits
	//     above the same gate for the same reason (see the doc comment above).
	//   - before the sweep, so a panic in it still unwinds through this.
	//   - as a DEFER, because the pass has a dozen exits and a backlog forces
	//     several of them: resolvableSeeds skips entities holding a pending row,
	//     so a standing queue guarantees len(seeds) == 0. Calling it inline at
	//     the end reaches none of those; calling it early reads the backlog
	//     before this run's own writes land.
	defer warnPendingBacklog(ont)

	// Registered before the sweep so the duration series covers it. Previously
	// it sat after the disabled/cancelled returns, so a vault with resolution
	// OFF — where the sweep is the only work done — recorded nothing at all.
	defer metrics.ObserveDuration(metrics.HistogramNamed(
		"compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "resolve"), time.Now())

	sweepAliases(ctx, ont)

	rcfg := applyResolveDefaults(cfg.Ontology.Resolve)
	if !cfg.Ontology.Resolve.Enabled || client == nil || len(touched) == 0 {
		return
	}
	// The pass is registered with defer, so it runs on cancelled exits too. A
	// cancelled compile must not buy a paid call or even load the pool.
	if ctx.Err() != nil {
		log.Warn("resolve: skipped, context cancelled")
		return
	}
	if !cfg.Ontology.Triples.Enabled {
		// Not fatal, but the user should know why nothing links: entity
		// descriptions are what auto-apply requires, and the triples pass is
		// their only compile-path writer.
		log.Warn("resolve: ontology.triples is disabled — most entities will have no " +
			"description, and auto-apply requires one on at least one side.")
	}

	// Cost attribution. Without this the spend bills to whatever pass ran last —
	// "write", since this is deferred to the end of runFullPipeline.
	prior := client.Pass()
	client.SetPass("resolve")
	defer client.SetPass(prior)

	pool, err := ont.ListEntities("")
	if err != nil {
		// Never fall through to an empty pool: every touched entity would look
		// like a singleton and the pass would report success having done nothing.
		log.Warn("resolve: entity pool load failed, skipping", "error", err)
		return
	}

	seeds, pool := resolvableSeeds(ont, touched, pool)
	if len(seeds) == 0 {
		return
	}

	// Both indexes are loaded once, before any decision. If rejections cannot be
	// read the pass does NOT fall back to an empty set — it would then link pairs
	// a human explicitly separated. Arbitration is abandoned; the sweep (already
	// done above) is unaffected.
	rej, err := newRejectionIndex(ont)
	if err != nil {
		log.Warn("resolve: cannot read rejections, skipping arbitration "+
			"(linking without them could reverse a user decision)", "error", err)
		return
	}
	graph, err := newAliasGraph(ont)
	if err != nil {
		log.Warn("resolve: cannot read applied aliases, skipping arbitration "+
			"(the co-absorption guard would be blind)", "error", err)
		return
	}

	tokens := discriminatingTokens(pool, rcfg.MaxTokenDF, rcfg.MinTokenDFFloor)
	vecs := embedForBlocking(ctx, embedder, seeds, pool, rcfg)

	// A candidate whose pair is already DECIDED — applied or pending — can never
	// produce a row: applyClusters discards it at the active-alias guard. Letting
	// it through buys an arbitration call per compile that is structurally
	// incapable of returning anything (forever for an applied link, until a human
	// acts for a pending one), and it consumes a max_block_size slot ahead of
	// genuinely new candidates. Both sets are already in memory.
	decided := func(a, b string) bool {
		if rej.is(a, b) || rej.awaitingReview(a, b) {
			return true
		}
		return graph.canonicalOf[a] == b || graph.canonicalOf[b] == a
	}
	blocks := buildBlocks(seeds, pool, tokens, vecs, rcfg, decided)
	if len(blocks) == 0 {
		log.Info("resolve: no candidate blocks", "seeds", len(seeds))
		return
	}

	model := resolveModel(cfg, rcfg)
	touchedSet := make(map[string]bool, len(seeds))
	for _, s := range seeds {
		touchedSet[s.ID] = true
	}
	stats := resolveStats{blocks: len(blocks)}
	proposed := map[string]bool{} // per-run: one proposal per alias

	for _, b := range blocks {
		if ctx.Err() != nil {
			log.Warn("resolve: cancelled mid-arbitration",
				"blocks_done", stats.calls, "blocks", len(blocks))
			break
		}
		clusters, err := arbitrateBlock(ctx, b, rcfg, model, client, &stats, pr, cfg.Compiler.CompileTemperature())
		stats.calls++
		if err != nil {
			stats.failed++
			log.Warn("resolve: arbitration failed", "seed", b.seed.ID, "error", err)
			continue
		}
		applyClusters(ont, clusters, rcfg, touchedSet, proposed, graph, rej, &stats, sink)
	}

	log.Info("resolve complete",
		"blocks", stats.blocks, "calls", stats.calls, "failed", stats.failed,
		"linked", stats.linked, "pending", stats.pending, "skipped", stats.skipped,
		"link_errors", stats.linkErrors,
		"dropped_unknown_label", stats.droppedUnknownLabel,
		"dropped_singleton", stats.droppedSingleton,
		"dropped_not_same_referent", stats.droppedNotReferent,
		"dropped_duplicate_member", stats.droppedDuplicate,
		"unplaced_labels", stats.unplacedLabels)
}

type resolveStats struct {
	blocks, calls, failed int
	linked, pending       int
	skipped               int // proposals dropped by a guard
	linkErrors            int // LinkAlias errored or reported a missing endpoint
	// Response-normalization drops, counted so the log accounts for everything
	// the model returned rather than only what survived.
	droppedUnknownLabel int
	droppedSingleton    int
	droppedNotReferent  int
	droppedDuplicate    int
	unplacedLabels      int
}

// sweepAliases re-applies every approved link, copying forward any edge that
// landed on an alias since the last run. Zero LLM calls.
//
// Ungated on purpose (see ResolveEntitiesPass). For anyone who never enabled
// resolution this is one indexed query returning nothing.
// SweepAliases re-applies every approved link, skipping any pair the user has
// since rejected.
//
// Exported so `ontology resolve --sweep` runs the identical logic as the compile
// pass — the CLI having its own copy is how the rejection filter came to exist
// on one path and not the other.
func SweepAliases(ctx context.Context, ont store.OntologyStore) SweepResult {
	// ResolveEntitiesPass normalises these before anything touches them; a
	// public entry point that skips the normalisation panics on ctx.Err() at the
	// first non-rejected applied row — a data-dependent crash, which is exactly
	// the hazard extract_triples.go documents for its own nilable ctx.
	if ont == nil {
		return SweepResult{}
	}
	if ctx == nil {
		ctx = context.Background()
	}

	return sweepAliases(ctx, ont)
}

// SweepResult reports what a sweep did.
type SweepResult struct {
	Rows, Copied, AlreadyPresent, EndpointMissing, Failed, RejectedSkipped int
}

func sweepAliases(ctx context.Context, ont store.OntologyStore) SweepResult {
	rows, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		log.Warn("resolve: alias sweep skipped, list failed", "error", err)
		return SweepResult{}
	}
	if len(rows) == 0 {
		// No applied links, so no derived edges are justified. Clearing is what
		// makes the sweep an undo rather than an append: after --unlink removed
		// the last link, rows derived transitively under it would otherwise
		// survive with nothing to justify them.
		if err := ont.ClearDerived(); err != nil {
			log.Warn("resolve: could not clear derived edges", "error", err)
		}
		return SweepResult{}
	}

	// NOTE for anyone reading Copied/AlreadyPresent: because this rebuilds, a
	// steady-state sweep reports every edge as Copied. AlreadyPresent counts
	// only pass 0 (see the fixpoint below), so it still means what it always
	// meant — "the canonical asserts this edge itself" — rather than counting
	// this sweep meeting its own derived rows.
	//
	// REBUILD, not append. LinkAlias only ever inserts, so replaying the
	// surviving links cannot remove a row that a now-unlinked alias caused:
	// under A->B->C, LinkAlias(B->C) derived C-side rows FROM A's rows and
	// stamped them B, so `--unlink A` leaves them behind. Rebuilding from the
	// surviving applied links is what self-corrects the chain. Measured with the
	// fixpoint below: 200 flat aliases run two passes — 400 write transactions,
	// ~52ms, against ~34ms for a single pass — on a compile that already made
	// LLM calls. The earlier "74ms vs 24ms" figure predated the fixpoint.
	//
	// The window between clear and replay is real: a concurrent reader sees no
	// derived edges for its duration, and a crash inside it leaves them missing
	// until the next sweep. Both self-heal, and the sweep runs at the end of a
	// compile that already took orders of magnitude longer.
	//
	// CANCELLATION IS CHECKED FIRST, and that ordering is the whole point. This
	// pass is registered with defer, so Ctrl-C reaches it; clearing before the
	// check meant a cancelled compile WIPED every derived edge and returned
	// before replacing any of them. An append-only sweep made a cancel harmless,
	// so the rebuild introduced the hazard and has to close it.
	if ctx.Err() != nil {
		log.Warn("resolve: alias sweep skipped, context cancelled")
		return SweepResult{}
	}
	// A user can reject a pair whose applied row still exists — --reject on a
	// PENDING row does not remove an applied row in the reverse direction. The
	// sweep would otherwise keep copying edges across a pair the user has
	// explicitly separated, on every compile, forever.
	//
	// This is a DIRECT-pair check while every other path uses pairConflict's
	// transitive cross product. That is sufficient, and the reason is an emergent
	// invariant worth stating because it spans three files: the applied graph is
	// a FOREST. Out-degree is at most 1 (idx_entity_aliases_active), and neither
	// the pass nor --apply will close a cycle, so rejecting an edge always splits
	// a tree and pairConflict then prevents the two halves rejoining. A
	// transitively-rejected pair therefore cannot both be applied. If any of
	// those three properties changes, this check must become pairConflict.
	rej, err := newRejectionIndex(ont)
	if err != nil {
		log.Warn("resolve: alias sweep skipped, cannot read rejections "+
			"(sweeping without them could re-copy across a rejected pair)", "error", err)
		return SweepResult{}
	}

	// EVERY early return must come BEFORE this. The clear is destructive and the
	// replay is what puts the edges back, so any path that clears and then bails
	// leaves the graph stripped — which is what the cancellation check above and
	// this ordering exist to prevent. Adding a new early exit below this line
	// reintroduces that hazard.
	if err := ont.ClearDerived(); err != nil {
		log.Warn("resolve: could not clear derived edges before rebuild", "error", err)
		return SweepResult{}
	}

	res := SweepResult{Rows: len(rows)}

	// REPLAY TO A FIXPOINT, because ListAliases returns links in id order
	// (ORDER BY alias, canonical_id) and that is not topological.
	//
	// A chain only propagates in one pass if the links happen to be visited
	// outward: A->B must run before B->C for A's edges to reach C. Alphabetical
	// order gives that for A->B->C and the reverse for zz->yy->xx, so a single
	// pass silently truncated roughly half of all multi-hop chains. The old
	// append-only sweep was equally order-dependent but ACCUMULATED, so
	// successive compiles converged; the rebuild clears first, which made the
	// truncation permanent and re-inflicted it on every compile.
	//
	// COUNTER SEMANTICS, chosen per counter rather than per sweep, because a
	// fixpoint replay makes "count what happened" ambiguous three ways and each
	// naive answer was measured wrong:
	//
	//   - accumulating everything multiplied structural facts by the pass count
	//     ("2 skipped as rejected" for one rejection);
	//   - taking everything from pass 0 hid per-pass EVENTS — a link whose
	//     replay failed only on pass 1 reported a CLEAN sweep while the rebuild
	//     had cleared the very edges that failed replay was meant to restore,
	//     and --sweep exited 0 on a stripped graph;
	//   - Copied alone is safe to accumulate, because LinkAlias's conflict
	//     target makes a re-derivation a no-op — which is also what lets the
	//     loop detect a fixpoint.
	//
	// So: Copied accumulates. Failed / EndpointMissing / RejectedSkipped are
	// per-LINK sets deduped across passes — a link failing on three passes is
	// one broken link, and one failing only on pass 2 still counts.
	// AlreadyPresent is pass 0 only, and is therefore blind to a native shadow
	// first reached on a later pass; re-visits re-conflict with our own derived
	// rows, so later passes cannot distinguish "canonical asserts this" from
	// "we derived this a pass ago", and under-counting a diagnostic beats
	// inventing phantoms in it.
	var first passCounts
	copiedTotal := 0
	failedLinks := map[string]bool{}
	missingLinks := map[string]bool{}
	blockedLinks := map[string]bool{}
	exhausted := true
	for pass := 0; pass < maxSweepPasses; pass++ {
		pc, ok := sweepPass(ctx, ont, rows, rej, pass, failedLinks, missingLinks, blockedLinks)
		if pass == 0 {
			first = pc
		}
		copiedTotal += pc.copied
		if !ok {
			exhausted = false // cancelled: sweepPass logged it, and an exhaustion warn would be wrong
			break
		}
		if pc.copied == 0 {
			exhausted = false // fixpoint confirmed: a full pass derived nothing new
			break
		}
	}
	if exhausted {
		// The loop ran out of passes while edges were still propagating, so
		// convergence was never CONFIRMED. At exactly the cap the chain may in
		// fact be fully propagated (a cap-length chain uses every pass and
		// leaves none to observe the fixpoint); anything deeper is truncated —
		// and because the sweep rebuilds, truncation recurs identically on
		// every compile. Say "may", not "is": sending a user to hunt for
		// missing edges that exist is its own failure.
		log.Warn("resolve: alias sweep hit the pass cap without confirming convergence — "+
			"chains deeper than the cap are missing derived edges, and will be on every compile",
			"passes", maxSweepPasses, "links", len(rows))
	}

	copied, missing, failed, blocked, skippedPresent :=
		copiedTotal, len(missingLinks), len(failedLinks), len(blockedLinks), first.skipped

	if missing > 0 || failed > 0 || copied > 0 || blocked > 0 {
		log.Info("resolve: alias sweep", "rows", len(rows),
			"edges_copied", copied, "endpoint_missing", missing, "failed", failed,
			"rejected_skipped", blocked)
	}
	res.Copied, res.AlreadyPresent = copied, skippedPresent
	res.EndpointMissing, res.Failed, res.RejectedSkipped = missing, failed, blocked
	return res
}

// passCounts is one pass's observations. The caller decides which accumulate;
// see the counter-semantics comment in sweepAliases.
type passCounts struct{ copied, skipped int }

// sweepPass replays every applied link once, recording per-link facts into the
// caller's sets (deduped across passes by alias id — out-degree is at most 1,
// so the alias identifies the link). Reports false if cancelled.
func sweepPass(ctx context.Context, ont store.OntologyStore, rows []store.EntityAlias,
	rej *rejectionIndex, pass int, failedLinks, missingLinks, blockedLinks map[string]bool) (passCounts, bool) {
	var pc passCounts
	for i, a := range rows {
		if rej.is(a.Alias, a.CanonicalID) {
			blockedLinks[a.Alias] = true
			continue
		}
		// The pass is registered with defer, so a Ctrl-C during compile still
		// reaches this loop. Without the check it would run one write
		// transaction per applied row to completion.
		if ctx.Err() != nil {
			// Rows, not edges: copied counts EDGES, so subtracting it from a row
			// count can go negative.
			//
			// Says "incomplete" because it is: the sweep cleared derived edges
			// before replaying, so an abort here leaves the links after `done`
			// with no derived edges until the next successful sweep. Silence
			// would make that look like a normal partial run.
			log.Warn("resolve: alias sweep cancelled — derived edges for the remaining links are "+
				"missing until the next sweep",
				"pass", pass, "done", i, "remaining", len(rows)-i)
			return pc, false
		}
		r, err := ont.LinkAlias(a)
		if err != nil {
			failedLinks[a.Alias] = true
			log.Warn("resolve: sweep re-link failed", "alias", a.Alias, "canonical", a.CanonicalID, "error", err)
			continue
		}
		// A pruned endpoint is a fact, not a failure: --prune and reconcile
		// delete entities without consulting the alias table. The row stays, so
		// the link is still actionable if the entity returns.
		if r.AliasMissing || r.CanonicalMissing {
			missingLinks[a.Alias] = true
			continue
		}
		pc.copied += r.Copied
		pc.skipped += r.Skipped
	}
	return pc, true
}

// resolvableSeeds narrows the touched set to entities worth arbitrating and
// returns the pool they may be compared against.
//
// source-type entities are excluded from BOTH. They are written with the file
// path as the id, the basename as the name and no description, so two documents
// named notes.md in different directories present identically with nothing to
// tell them apart — and linking them would re-point one document's citations at
// the other. A source entity's identity IS its path.
func resolvableSeeds(ont store.OntologyStore, touched []string, pool []store.Entity) (seeds, filtered []store.Entity) {
	byID := make(map[string]store.Entity, len(pool))
	for _, e := range pool {
		if e.Type == ontology.TypeSource || strings.TrimSpace(e.Name) == "" {
			continue
		}
		byID[e.ID] = e
		filtered = append(filtered, e)
	}

	seen := map[string]bool{}
	for _, id := range touched {
		if seen[id] {
			continue
		}
		seen[id] = true
		e, ok := byID[id]
		if !ok {
			continue
		}
		// Already decided — an active row means this entity is linked or is
		// waiting on a human. Re-proposing it would collide with the
		// one-active-row-per-alias index.
		active, err := ont.GetActiveAlias(id)
		if err != nil {
			log.Warn("resolve: active-alias lookup failed, skipping seed", "id", id, "error", err)
			continue
		}
		if active != nil {
			continue
		}
		seeds = append(seeds, e)
	}
	return seeds, filtered
}

// resolveModel mirrors ExtractTriplesPass's chain so both graph passes resolve
// their model the same way.
func resolveModel(cfg *config.Config, rcfg config.ResolveConfig) string {
	if rcfg.Model != "" {
		return rcfg.Model
	}
	if cfg.Models.Extract != "" {
		return cfg.Models.Extract
	}
	if cfg.Models.Summarize != "" {
		return cfg.Models.Summarize
	}
	return "gpt-4o-mini"
}

// arbitrateBlock asks the model to cluster one block and normalizes the reply.
func arbitrateBlock(
	ctx context.Context,
	b resolveBlock,
	rcfg config.ResolveConfig,
	model string,
	client *llm.Client,
	stats *resolveStats,
	pr *prompts.Registry,
	temp *float64,
) ([]normalizedCluster, error) {
	// NeutralizeTags over the whole rendered block: names and descriptions are
	// model-generated text derived from arbitrary source documents, i.e.
	// second-order untrusted input (SEC-04), and Render does not neutralize.
	body := prompts.NeutralizeTags(renderBlockMembers(b))
	prompt, err := renderPrompt(pr, "resolve_entities", prompts.ResolveData{Members: body}, "")
	if err != nil {
		return nil, fmt.Errorf("render resolve_entities: %w", err)
	}

	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "system", Content: "You are an entity-resolution system. Output valid JSON only."},
		{Role: "user", Content: prompt},
	}, ResolveSchema, llm.CallOpts{Model: model, MaxTokens: rcfg.MaxTokens, Temperature: temp})
	if err != nil {
		return nil, err
	}

	var resp resolveResponse
	if err := json.Unmarshal(payload, &resp); err != nil {
		return nil, fmt.Errorf("parse clusters: %w", err)
	}
	return normalizeClusters(resp.Clusters, b.labels, stats), nil
}

// applyClusters turns normalized clusters into links or pending proposals.
// rejectionIndex is a snapshot of every rejected pair, loaded ONCE per pass.
//
// It replaces a per-pair IsRejected round-trip. Two reasons beyond speed: the
// old shape cost one query per (seed x candidate) and per (alias x sibling), and
// it treated a transient read failure as "rejected", which logged a co-absorption
// conflict naming a pair nobody had rejected. A snapshot has neither problem —
// the pass never creates rejections, so it cannot go stale mid-run.
type rejectionIndex struct {
	partners map[string]map[string]bool
	// awaiting holds pairs a human has been asked about but has not decided.
	// Auto-applying such a pair in the REVERSE direction bypasses the review bar
	// entirely: the election flips as soon as one side gains an ArticlePath, and
	// the other side has no row of its own, so every per-alias guard passes.
	awaiting map[string]map[string]bool
}

func newRejectionIndex(ont store.OntologyStore) (*rejectionIndex, error) {
	rejected, err := ont.ListAliases(store.AliasRejected)
	if err != nil {
		return nil, err
	}
	pending, err := ont.ListAliases(store.AliasPending)
	if err != nil {
		return nil, err
	}
	idx := &rejectionIndex{
		partners: map[string]map[string]bool{},
		awaiting: map[string]map[string]bool{},
	}
	for _, r := range rejected {
		idx.mark(r.Alias, r.CanonicalID)
		idx.mark(r.CanonicalID, r.Alias) // symmetric
	}
	for _, p := range pending {
		mark(idx.awaiting, p.Alias, p.CanonicalID)
		mark(idx.awaiting, p.CanonicalID, p.Alias) // symmetric
	}
	return idx, nil
}

func mark(m map[string]map[string]bool, a, b string) {
	if m[a] == nil {
		m[a] = map[string]bool{}
	}
	m[a][b] = true
}

// awaitingReview reports whether this pair is already queued for a human, in
// either direction.
func (r *rejectionIndex) awaitingReview(a, b string) bool { return r.awaiting[a][b] }

// markAwaiting records a pending pair created during this run, keeping the
// snapshot fresh for later blocks.
func (r *rejectionIndex) markAwaiting(a, b string) {
	mark(r.awaiting, a, b)
	mark(r.awaiting, b, a)
}

func (r *rejectionIndex) mark(a, b string) { mark(r.partners, a, b) }

func (r *rejectionIndex) is(a, b string) bool { return r.partners[a][b] }

// aliasGraph answers "which entities already resolve to this one?".
//
// Keyed on the TERMINAL canonical, not the direct one. LinkAlias deliberately
// never rewrites rows to the terminal (see aliases.go), so multi-hop chains
// x->y->z are the normal steady state — an index keyed on the direct canonical
// cannot see x when asked about z, which is how a rejected pair was still being
// folded together after two rounds of narrower fixes.
type aliasGraph struct {
	canonicalOf map[string]string
	// memo caches terminal(). NOTE, corrected in Gate 3 round 8: this is NOT the
	// hot path. terminal() is called only from the two exported wrappers, once
	// each on a freshly built graph, so in production it is always a cold miss.
	// cluster() — which IS called per candidate pair — rebuilds the adjacency map
	// each time and is deliberately not memoized, because add() would have to
	// invalidate it and the component sets are small in practice. Kept because
	// terminal() is also used by tests and the cost is one map.
	memo map[string]string
}

func newAliasGraph(ont store.OntologyStore) (*aliasGraph, error) {
	rows, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		return nil, err
	}
	g := &aliasGraph{canonicalOf: map[string]string{}, memo: map[string]string{}}
	for _, a := range rows {
		g.canonicalOf[a.Alias] = a.CanonicalID
	}
	return g, nil
}

// terminal follows the applied chain to its end, bounded and cycle-guarded.
func (g *aliasGraph) terminal(id string) string {
	if t, ok := g.memo[id]; ok {
		return t
	}
	t := g.walk(id)
	g.memo[id] = t
	return t
}

func (g *aliasGraph) walk(id string) string {
	seen := map[string]bool{id: true}
	cur := id
	for i := 0; i < maxResolveChain; i++ {
		next, ok := g.canonicalOf[cur]
		if !ok {
			return cur
		}
		if seen[next] {
			// Same answer ontology.CanonicalID gives: the input id. Returning an
			// entry-point-dependent node instead would make terminal(a) and
			// terminal(b) disagree for a 2-cycle, so the two resolvers would
			// answer differently about the same graph.
			return id
		}
		seen[next] = true
		cur = next
	}
	return cur
}

// add records a new link. The memo is dropped rather than patched: a new edge
// can change the terminal of any entity upstream of `alias`, and a stale memo
// would silently answer with the pre-link topology.
func (g *aliasGraph) add(alias, canonical string) {
	g.canonicalOf[alias] = canonical
	g.memo = map[string]string{}
}

// cluster returns every entity transitively linked to id — the weakly-connected
// component of the alias graph containing it.
//
// A component, not "everything sharing a terminal". On an applied CYCLE, walk()
// returns the input id (to agree with ontology.CanonicalID), so terminal-keying
// would split a 2-cycle into two singletons and hide a rejection between the two
// sides. A component is cycle-proof, and it is the right semantic anyway:
// anything transitively linked is unified, whichever direction the edges point.
func (g *aliasGraph) cluster(id string) []string {
	adj := map[string][]string{}
	for a, c := range g.canonicalOf {
		adj[a] = append(adj[a], c)
		adj[c] = append(adj[c], a)
	}
	seen := map[string]bool{id: true}
	queue := []string{id}
	out := []string{id}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, n := range adj[cur] {
			if seen[n] {
				continue
			}
			seen[n] = true
			out = append(out, n)
			queue = append(queue, n)
		}
	}
	sort.Strings(out)
	return out
}

// pairConflict reports why linking alias -> target must not happen, or "".
//
// ONE gate for both rejected and awaiting-review pairs, over the CROSS PRODUCT
// of both transitive clusters. Two guards side by side with different scopes is
// exactly the bug this cycle kept reproducing: rounds 3 and 4 were about the
// rejection gate not being transitive, and round 5 then added a pending gate
// that checked only the direct pair — the same mistake, one line away from the
// code that gets it right.
//
// Linking does not move one entity: everything already resolving to `alias`
// follows it under `target`, and everything already under `target` is unified
// with it. So both sides expand, and a contradiction anywhere in the product
// blocks the link.
//
// Shared by the compile pass and `ontology resolve --apply` so the two cannot
// disagree.
func pairConflict(g *aliasGraph, rej *rejectionIndex, alias, target string) string {
	return pairConflictExcept(g, rej, alias, target, "", "")
}

// pairConflictExcept is pairConflict with one awaiting pair ignored.
//
// `ontology resolve --apply` needs this: the human IS settling that exact pair,
// so treating its own pending row as an "awaiting review" conflict would make
// every proposal permanently unapplicable. A REJECTION of the same pair still
// blocks — that is a decision, not a question.
func pairConflictExcept(g *aliasGraph, rej *rejectionIndex, alias, target, exA, exB string) string {
	moving := g.cluster(alias)   // alias + everything that resolves to it
	landing := g.cluster(target) // target + everything already resolving there
	for _, m := range moving {
		for _, l := range landing {
			if m == l {
				continue
			}
			if rej.is(m, l) {
				return "rejected: " + m + " / " + l
			}
			// A pair a human is already deciding must not be settled by the
			// pass, in either direction and by no route — including linking a
			// third entity that unifies them transitively.
			if rej.awaitingReview(m, l) {
				if (m == exA && l == exB) || (m == exB && l == exA) {
					continue // the pair the caller is settling
				}
				return "awaiting review: " + m + " / " + l
			}
		}
	}
	return ""
}

func applyClusters(
	ont store.OntologyStore,
	clusters []normalizedCluster,
	rcfg config.ResolveConfig,
	touched map[string]bool,
	proposed map[string]bool,
	graph *aliasGraph,
	rej *rejectionIndex,
	stats *resolveStats,
	sink events.Sink,
) {
	now := config.NowUTC().Format(time.RFC3339)

	// No canonical-first reordering here, deliberately. normalizeClusters'
	// `claimed` map makes surviving clusters disjoint, so within one call no
	// cluster's elected canonical can be another cluster's alias — a sort on
	// that predicate is a guaranteed no-op, and dead code that looks like a
	// guarantee is worse than none. Chains that span BLOCKS converge through the
	// sweep, which replays every applied link on the next pass.
	for _, c := range clusters {
		canonical := electCanonical(c.entities)
		for _, alias := range c.entities {
			if alias.ID == canonical.ID {
				continue
			}
			// The PAIR must involve something this compile touched — not merely
			// the cluster. A cluster check is too loose: with one touched member
			// and two untouched, it writes an alias row for a pair neither side
			// of which was touched, and any active row permanently removes that
			// entity from future resolution (resolvableSeeds skips it). A
			// per-ALIAS check is too tight and broke the primary use case, where
			// the article row written this compile is elected canonical and the
			// alias is the triples row from an earlier one.
			// NOTE: judged against `target` below, not `canonical.ID` — see the
			// re-check after chain resolution.
			// Per-run guard: blocks may overlap, and two proposals for one alias
			// would collide on the one-active-row index.
			if proposed[alias.ID] {
				stats.skipped++
				continue
			}
			// Cross-run guard: an entity linked by an EARLIER run is still in
			// the pool (nothing is deleted), so it can be pulled into a block
			// again. A second active row is a non-target unique violation that
			// the upsert does not absorb — it would abort the whole transaction
			// and lose this run's edge copies with it.
			active, err := ont.GetActiveAlias(alias.ID)
			if err != nil {
				log.Warn("resolve: active-alias lookup failed, skipping", "alias", alias.ID, "error", err)
				stats.skipped++
				continue
			}
			if active != nil {
				stats.skipped++
				continue
			}
			target, err := ont.CanonicalID(canonical.ID)
			if err != nil {
				log.Warn("resolve: canonical resolution failed", "id", canonical.ID, "error", err)
				stats.skipped++
				continue
			}
			if target == alias.ID {
				stats.skipped++
				continue
			}
			// The touched check belongs HERE, against the pair actually written.
			// Checking the ELECTED canonical instead leaks: a seed can become an
			// alias in an earlier block, after which its terminal — the entity
			// the row really names — was never touched by this compile, while
			// touched[canonical.ID] is still true. The result is a row for a pair
			// neither side of which this compile looked at, and any active row
			// permanently removes that entity from future resolution.
			//
			// Per PAIR, not per alias and not per cluster: per alias breaks the
			// primary use case (the article row written this compile is elected
			// canonical, the alias is the triples row from an earlier one), and
			// per cluster is looser still.
			if !touched[alias.ID] && !touched[target] {
				stats.skipped++
				continue
			}
			// The single gate: rejected OR awaiting review, across both
			// transitive clusters. It subsumes the direct pair and the elected
			// canonical, since cluster(target) contains both.
			if conflict := pairConflict(graph, rej, alias.ID, target); conflict != "" {
				log.Warn("resolve: skipped, linking would settle a pair the user owns",
					"alias", alias.ID, "canonical", target, "conflict", conflict)
				stats.skipped++
				continue
			}

			// The guard must judge the pair that will ACTUALLY be linked. When
			// the elected canonical is itself an alias, `target` is a different
			// entity — judging `canonical` could auto-apply a link where neither
			// linked entity has a description.
			judged := canonical
			mustReview := false
			if target != canonical.ID {
				te, err := ont.GetEntity(target)
				switch {
				case err != nil || te == nil:
					// FORCE review rather than relying on an empty Definition to
					// block auto-apply: canAutoApply is an OR over both sides, so
					// a described alias would auto-apply anyway. A pruned
					// canonical with a surviving alias row is routine.
					//
					// The row is queued against the ELECTED canonical, which
					// exists, not the ghost target. A pending row naming a
					// deleted entity can never be applied — --apply always errors
					// — while its active status permanently removes the alias
					// from future resolution, leaving --reject (recording a
					// judgement the user never made) as the only escape.
					log.Warn("resolve: chain-resolved canonical unavailable, queuing against the elected canonical",
						"ghost_target", target, "queued_against", canonical.ID, "error", err)
					target = canonical.ID
					mustReview = true
				default:
					judged = *te
				}
			}

			row := store.EntityAlias{
				Alias:       alias.ID,
				CanonicalID: target,
				EntityType:  alias.Type,
				Confidence:  c.confidence,
				Reason:      c.reason,
				Source:      "llm",
				CreatedAt:   now,
				// DecidedAt/DecidedBy are stamped only on the applied branch: a
				// pending row has NOT been decided, and nullText keeps those
				// columns NULL so a review cannot read as already-decided.
			}

			judgedCluster := c.raw
			judgedCluster.Confidence = c.confidence // the clamped value the row records
			if mustReview || !canAutoApply(judgedCluster, alias, judged, rcfg.AutoApplyThreshold) {
				row.Status = store.AliasPending
				if err := ont.PutAlias(row); err != nil {
					log.Warn("resolve: pending proposal write failed", "alias", alias.ID, "error", err)
					stats.linkErrors++
					continue
				}
				proposed[alias.ID] = true
				// Keep the snapshot fresh WITHIN the run. Round 5 made
				// `awaiting` depend on pending rows, and the pass writes them, so
				// a pair queued by an early block would otherwise be invisible to
				// a later one.
				//
				// CORRECTION: an earlier version of this comment claimed no
				// fixture could isolate this line. That was wrong — a Gate-3
				// reviewer built one. The direction flips through a PRE-EXISTING
				// APPLIED CHAIN, not through the election: with `applied x->a`,
				// a later block's cluster {b,x} elects x and then chain-resolves
				// to a, which is an earlier block's pending ALIAS. My reasoning
				// checked only the mechanism I had in mind (electCanonical is
				// deterministic) and concluded no mechanism existed.
				//
				// The in-repo test below is still weaker than that probe, so
				// treat this line as load-bearing rather than optional.
				rej.markAwaiting(alias.ID, target)
				stats.pending++
				continue
			}

			row.Status = store.AliasApplied
			row.DecidedAt = now
			row.DecidedBy = "auto"
			res, err := ont.LinkAlias(row)
			if err != nil {
				log.Warn("resolve: link failed", "alias", alias.ID, "canonical", target, "error", err)
				stats.linkErrors++
				continue
			}
			if res.AliasMissing || res.CanonicalMissing {
				log.Warn("resolve: link skipped, endpoint missing",
					"alias", alias.ID, "canonical", target)
				stats.linkErrors++
				continue
			}
			proposed[alias.ID] = true
			graph.add(alias.ID, target)
			stats.linked++
			// SPEC-07: the alias is now merged into the canonical entity.
			// Workspace is filled by the caller's BindWorkspace wrapper.
			if sink != nil {
				sink.Emit(events.NewEvent("", events.TypeEntityResolved, events.EntityResolved{
					Canonical:  target,
					Alias:      alias.ID,
					Confidence: row.Confidence,
					Auto:       true,
				}))
			}
		}
	}
}

// embedForBlocking computes in-memory vectors for the seeds and their
// same-type pool candidates, or nil when embeddings are off or unusable.
//
// Nothing is persisted: no vec_entries row, no cache to invalidate, no new
// keyspace. The cap is GLOBAL for the run, not per type — embed.Embedder has no
// batch method, so every vector is one HTTP call.
func embedForBlocking(
	ctx context.Context,
	embedder embed.Embedder,
	seeds, pool []store.Entity,
	rcfg config.ResolveConfig,
) map[string][]float32 {
	if !rcfg.UseEmbeddings {
		return nil
	}
	if embedder == nil {
		log.Warn("resolve: use_embeddings is set but no embedder is configured — " +
			"falling back to lexical blocking")
		return nil
	}

	// Only types that actually contain a seed can produce a link, so embedding
	// any other type is pure spend.
	wanted := map[string]bool{}
	for _, s := range seeds {
		wanted[s.Type] = true
	}

	// Seeds first, then candidates — each sorted for determinism. Sorting the
	// combined set would let a seed whose id sorts past the cap go un-embedded,
	// so every one of its pairs fails the cosine test and the whole embedding
	// budget buys nothing for it.
	var targets []store.Entity
	seen := map[string]bool{}
	appendUnique := func(es []store.Entity) {
		var batch []store.Entity
		for _, e := range es {
			if seen[e.ID] || !wanted[e.Type] {
				continue
			}
			seen[e.ID] = true
			batch = append(batch, e)
		}
		sort.Slice(batch, func(i, j int) bool { return batch[i].ID < batch[j].ID })
		targets = append(targets, batch...)
	}
	appendUnique(seeds)
	appendUnique(pool)
	if len(targets) > rcfg.MaxEmbedCandidates {
		log.Warn("resolve: embed cap reached, blocking the tail lexically only",
			"cap", rcfg.MaxEmbedCandidates, "dropped", len(targets)-rcfg.MaxEmbedCandidates)
		targets = targets[:rcfg.MaxEmbedCandidates]
	}

	vecs := make(map[string][]float32, len(targets))
	failures := 0
	for _, e := range targets {
		if ctx.Err() != nil {
			log.Warn("resolve: embedding cancelled", "done", len(vecs), "of", len(targets))
			break
		}
		text := e.Name
		if d := strings.TrimSpace(e.Definition); d != "" {
			text += " — " + d
		}
		v, err := embedder.Embed(text)
		if err != nil {
			failures++
			continue
		}
		vecs[e.ID] = v
	}
	if failures > 0 {
		// An embedding outage must not cost the vault its resolution: lexical
		// blocking stands on its own.
		log.Warn("resolve: some embeddings failed, continuing on lexical blocking",
			"failed", failures, "of", len(targets))
	}
	if len(vecs) == 0 {
		return nil
	}
	return vecs
}

// CoAbsorptionConflict reports the entity that `alias` must not join under
// `canonical`, or "".
//
// Exported so `sage-wiki ontology resolve --apply` enforces the same rule the
// compile pass does. A direct-pair rejection check is not enough on either side:
// folding both halves of a rejected pair under a third entity unifies them just
// as surely, and the pass and the CLI disagreeing on that means a human can
// finish a merge the pass refused.
func CoAbsorptionConflict(ont store.OntologyStore, alias, canonical string) (string, error) {
	rej, err := newRejectionIndex(ont)
	if err != nil {
		return "", err
	}
	graph, err := newAliasGraph(ont)
	if err != nil {
		return "", err
	}
	return pairConflictExcept(graph, rej, alias, graph.terminal(canonical), alias, canonical), nil
}

// TerminalCanonical resolves an id through the applied-alias chain to its end.
//
// Exported for `ontology resolve --apply`, which must refuse a link whose target
// resolves back to the alias — that produces a cycle in which neither entity is
// canonical and the sweep copies edges both ways forever.
func TerminalCanonical(ont store.OntologyStore, id string) (string, error) {
	g, err := newAliasGraph(ont)
	if err != nil {
		return "", err
	}
	return g.terminal(id), nil
}
