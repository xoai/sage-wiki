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
	// safe reading of an out-of-range threshold is "unset".
	if c.AutoApplyThreshold <= 0 || c.AutoApplyThreshold > 1 {
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

// canAutoApply decides whether a proposed link is applied without review.
//
// The description requirement is the guard that matters in practice. Under the
// DEFAULT configuration concept entities carry no Definition (write.go), and the
// only compile-path writer of one is the triple-extraction pass, which defaults
// off — so without this, enabling resolution alone would link entities on
// surface-name similarity with no grounded evidence at all.
//
// ONE description suffices, not two: no writer in this codebase puts both an
// ArticlePath and a Definition on the same row, and the described row and the
// article-bearing row are exactly the pair this pass links. Requiring both would
// make auto-apply a branch that can never fire.
func canAutoApply(c resolvedCluster, x, y store.Entity, threshold float64) bool {
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
) {
	// cfg == nil FIRST: cfg.Ontology on a nil *Config panics, and the
	// fullpipeline call site can hand over a partially-built config.
	if cfg == nil || ont == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}

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
		log.Warn("resolve: ontology.triples is disabled — entities have no descriptions, " +
			"so proposals will be queued for review rather than applied automatically")
	}

	defer metrics.ObserveDuration(metrics.HistogramNamed(
		"compile_pass_duration_seconds", metrics.CompileBuckets(), "pass", "resolve"), time.Now())

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

	tokens := discriminatingTokens(pool, rcfg.MaxTokenDF, rcfg.MinTokenDFFloor)
	vecs := embedForBlocking(ctx, embedder, seeds, pool, rcfg)

	rejected := func(a, b string) bool {
		no, err := ont.IsRejected(a, b)
		if err != nil {
			// Treat a lookup failure as "rejected": re-linking a pair a human
			// separated is the worse outcome.
			log.Warn("resolve: rejection lookup failed, excluding the pair", "a", a, "b", b, "error", err)
			return true
		}
		return no
	}

	blocks := buildBlocks(seeds, pool, tokens, vecs, rcfg, rejected)
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
	siblings := newSiblingIndex(ont)

	for _, b := range blocks {
		if ctx.Err() != nil {
			log.Warn("resolve: cancelled mid-arbitration",
				"blocks_done", stats.calls, "blocks", len(blocks))
			break
		}
		clusters, err := arbitrateBlock(ctx, b, rcfg, model, client, &stats)
		stats.calls++
		if err != nil {
			stats.failed++
			log.Warn("resolve: arbitration failed", "seed", b.seed.ID, "error", err)
			continue
		}
		applyClusters(ont, clusters, rcfg, touchedSet, proposed, siblings, rejected, &stats)
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
func sweepAliases(ctx context.Context, ont store.OntologyStore) {
	rows, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		log.Warn("resolve: alias sweep skipped, list failed", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	copied, missing, failed := 0, 0, 0
	for i, a := range rows {
		// The pass is registered with defer, so a Ctrl-C during compile still
		// reaches this loop. Without the check it would run one write
		// transaction per applied row to completion.
		if ctx.Err() != nil {
			// Rows, not edges: copied counts EDGES, so subtracting it from a row
			// count can go negative.
			log.Warn("resolve: alias sweep cancelled", "done", i, "remaining", len(rows)-i)
			break
		}
		res, err := ont.LinkAlias(a)
		if err != nil {
			failed++
			log.Warn("resolve: sweep re-link failed", "alias", a.Alias, "canonical", a.CanonicalID, "error", err)
			continue
		}
		// A pruned endpoint is a fact, not a failure: --prune and reconcile
		// delete entities without consulting the alias table. The row stays, so
		// the link is still actionable if the entity returns.
		if res.AliasMissing || res.CanonicalMissing {
			missing++
			continue
		}
		copied += res.Copied
	}
	if missing > 0 || failed > 0 || copied > 0 {
		log.Info("resolve: alias sweep", "rows", len(rows),
			"edges_copied", copied, "endpoint_missing", missing, "failed", failed)
	}
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
) ([]normalizedCluster, error) {
	// NeutralizeTags over the whole rendered block: names and descriptions are
	// model-generated text derived from arbitrary source documents, i.e.
	// second-order untrusted input (SEC-04), and Render does not neutralize.
	body := prompts.NeutralizeTags(renderBlockMembers(b))
	prompt, err := prompts.Render("resolve_entities", prompts.ResolveData{Members: body}, "")
	if err != nil {
		return nil, fmt.Errorf("render resolve_entities: %w", err)
	}

	payload, _, err := client.StructuredCompletion(ctx, []llm.Message{
		{Role: "system", Content: "You are an entity-resolution system. Output valid JSON only."},
		{Role: "user", Content: prompt},
	}, ResolveSchema, llm.CallOpts{Model: model, MaxTokens: rcfg.MaxTokens})
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
// siblingIndex maps a canonical to every alias already linked into it, seeded
// from the store so it spans previous runs as well as earlier blocks in this one.
type siblingIndex struct{ byCanonical map[string][]string }

func newSiblingIndex(ont store.OntologyStore) *siblingIndex {
	idx := &siblingIndex{byCanonical: map[string][]string{}}
	rows, err := ont.ListAliases(store.AliasApplied)
	if err != nil {
		// Not fatal, but say so: without the seed the co-absorption guard only
		// covers links made in this run.
		log.Warn("resolve: could not load applied aliases; the co-absorption guard "+
			"will not see links from previous runs", "error", err)
		return idx
	}
	for _, a := range rows {
		idx.byCanonical[a.CanonicalID] = append(idx.byCanonical[a.CanonicalID], a.Alias)
	}
	return idx
}

func (s *siblingIndex) add(canonical, alias string) {
	s.byCanonical[canonical] = append(s.byCanonical[canonical], alias)
}

// conflict returns the sibling that alias must not join under canonical, or "".
func (s *siblingIndex) conflict(alias, canonical string, rejected func(a, b string) bool) string {
	for _, other := range s.byCanonical[canonical] {
		if other != alias && rejected(alias, other) {
			return other
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
	siblings *siblingIndex,
	rejected func(a, b string) bool,
	stats *resolveStats,
) {
	now := time.Now().UTC().Format(time.RFC3339)

	// No canonical-first reordering here, deliberately. normalizeClusters'
	// `claimed` map makes surviving clusters disjoint, so within one call no
	// cluster's elected canonical can be another cluster's alias — a sort on
	// that predicate is a guaranteed no-op, and dead code that looks like a
	// guarantee is worse than none. Chains that span BLOCKS converge through the
	// sweep, which replays every applied link on the next pass.
	for _, c := range clusters {
		// The cluster must involve something THIS compile touched. A block is
		// seeded by a touched entity but its candidates are ordinary pool rows,
		// so the model can return a cluster naming two entities neither of which
		// this compile wrote — acting on that would let an incremental run
		// decide about entities it never looked at.
		//
		// The check is per CLUSTER, not per alias. Per alias it would break the
		// primary use case: the article row written this compile wins the
		// election (ArticlePath beats everything), so the alias is the triples
		// row from an EARLIER compile — skipping it links nothing, queues
		// nothing, and re-bills the same arbitration call forever. Absorbing an
		// untouched entity is safe here precisely because linking is
		// non-destructive: its row and its own edges are never modified.
		relevant := false
		for _, e := range c.entities {
			if touched[e.ID] {
				relevant = true
				break
			}
		}
		if !relevant {
			stats.skipped += len(c.entities)
			continue
		}

		canonical := electCanonical(c.entities)
		for _, alias := range c.entities {
			if alias.ID == canonical.ID {
				continue
			}
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
			// Re-checked here, not only during candidate generation: a rejected
			// pair can re-enter a block through a third entity.
			if rejected(alias.ID, canonical.ID) {
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
			// Re-check against the CHAIN-RESOLVED target, not just the elected
			// canonical. When the canonical is itself an applied alias the link
			// lands on a different entity than the one just checked — and since
			// putAliasTx suppresses writes over a rejected row, the edges would
			// be copied with NO audit row, leaving the alias to be re-seeded and
			// re-copied on every later compile with nothing recording it.
			if target != canonical.ID && rejected(alias.ID, target) {
				log.Warn("resolve: skipped, the pair is rejected once the canonical chain is followed",
					"alias", alias.ID, "elected", canonical.ID, "target", target)
				stats.skipped++
				continue
			}
			// A rejection between two entities is also a rejection of folding
			// both into one canonical: absorbing them together reconstructs
			// exactly the merge the user refused.
			//
			// Checked against everything already absorbed into `target` — by an
			// earlier block in this run OR by a previous compile. An in-memory
			// per-call map cannot see either: applyClusters runs once per block,
			// so two halves of a rejected pair land in separate calls, and a link
			// from a previous run is not in memory at all.
			if conflict := siblings.conflict(alias.ID, target, rejected); conflict != "" {
				log.Warn("resolve: skipped, co-absorbing a rejected pair into one canonical",
					"alias", alias.ID, "with", conflict, "canonical", target)
				stats.skipped++
				continue
			}

			// The guard must judge the pair that will ACTUALLY be linked. When
			// the elected canonical is itself an alias, `target` is a different
			// entity — evaluating against `canonical` could auto-apply a link
			// where neither linked entity has a description, which is the one
			// thing the description rule exists to prevent.
			judged := canonical
			if target != canonical.ID {
				if te, err := ont.GetEntity(target); err == nil && te != nil {
					judged = *te
				} else {
					log.Warn("resolve: could not load the chain-resolved canonical; queuing for review",
						"target", target, "error", err)
					judged = store.Entity{ID: target} // no description -> cannot auto-apply
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

			if !canAutoApply(c.raw, alias, judged, rcfg.AutoApplyThreshold) {
				row.Status = store.AliasPending
				if err := ont.PutAlias(row); err != nil {
					log.Warn("resolve: pending proposal write failed", "alias", alias.ID, "error", err)
					continue
				}
				proposed[alias.ID] = true
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
			siblings.add(target, alias.ID)
			stats.linked++
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

	var targets []store.Entity
	seen := map[string]bool{}
	for _, e := range append(append([]store.Entity{}, seeds...), pool...) {
		if seen[e.ID] || !wanted[e.Type] {
			continue
		}
		seen[e.ID] = true
		targets = append(targets, e)
	}
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
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
