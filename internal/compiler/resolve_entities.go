package compiler

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/log"
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
	if c.MaxBlockSize <= 0 {
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
func discriminatingTokens(pool []store.Entity, maxDF float64, floor int) map[string]bool {
	df := map[string]int{}
	for _, e := range pool {
		seen := map[string]bool{}
		for _, tok := range normalizeNameTokens(e.Name) {
			if seen[tok] {
				continue
			}
			seen[tok] = true
			df[tok]++
		}
	}

	limit := int(maxDF * float64(len(pool)))
	if limit < floor {
		limit = floor
	}

	ok := make(map[string]bool, len(df))
	for tok, n := range df {
		if n <= limit {
			ok[tok] = true
		}
	}
	return ok
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
func normalizeClusters(raw []resolvedCluster, labels map[string]store.Entity) []normalizedCluster {
	var out []normalizedCluster
	claimed := map[string]bool{} // label -> already in an earlier cluster

	for _, c := range raw {
		// A cluster the model did not vouch for is not a proposal.
		if !c.SameReferent {
			continue
		}

		var members []store.Entity
		seen := map[string]bool{}
		for _, label := range c.Members {
			e, ok := labels[label]
			if !ok || seen[label] || claimed[label] {
				// Unknown label (hallucinated), repeated within this cluster, or
				// already claimed by an earlier one. First cluster wins, by the
				// order the model returned, so a re-run is deterministic.
				continue
			}
			seen[label] = true
			members = append(members, e)
		}
		if len(members) < 2 {
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
	tokens map[string]bool,
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
			if c.ID == seed.ID || rejected(seed.ID, c.ID) {
				continue
			}
			if candidateMatches(seed, c, norm, full, tokens, vecs, cfg) {
				candidates = append(candidates, c)
			}
		}
		// Nothing to arbitrate: this is the incremental-cost property — a new,
		// unambiguous entity costs zero LLM calls.
		if len(candidates) == 0 {
			continue
		}

		sort.Slice(candidates, func(i, j int) bool { return candidates[i].ID < candidates[j].ID })
		if cap := cfg.MaxBlockSize - 1; len(candidates) > cap {
			log.Warn("resolve: candidate cap reached, dropping the tail",
				"seed", seed.ID, "kept", cap, "dropped", len(candidates)-cap)
			candidates = candidates[:cap]
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
