package compiler

import (
	"sort"
	"strings"
	"unicode"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/llm"
	"github.com/xoai/sage-wiki/internal/store"
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
