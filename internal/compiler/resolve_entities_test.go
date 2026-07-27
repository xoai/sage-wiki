package compiler

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/xoai/sage-wiki/internal/config"
	"github.com/xoai/sage-wiki/internal/store"
)

// --- defaults ---

// A Config{} literal yields zeros (config.Defaults() has no Ontology entry), and
// the zero values are not merely suboptimal — they are broken. A zero
// MaxBlockSize makes the per-seed candidate cap negative, so every block is
// empty and the pass can never link anything.
func TestApplyResolveDefaults(t *testing.T) {
	got := applyResolveDefaults(config.ResolveConfig{})

	if got.MaxTokens != defaultResolveMaxTokens {
		t.Errorf("MaxTokens = %d, want %d", got.MaxTokens, defaultResolveMaxTokens)
	}
	if got.MaxBlockSize != defaultResolveMaxBlockSize {
		t.Errorf("MaxBlockSize = %d, want %d", got.MaxBlockSize, defaultResolveMaxBlockSize)
	}
	if got.AutoApplyThreshold != defaultResolveAutoApplyThreshold {
		t.Errorf("AutoApplyThreshold = %v, want %v",
			got.AutoApplyThreshold, defaultResolveAutoApplyThreshold)
	}
	// The LITERAL value, not just the symbol: comparing the constant to itself
	// holds for any value, so it cannot pin the default. This pin moves WITH
	// the default, deliberately — the never-at-1.0 guarantee is pinned
	// separately by TestCanAutoApplyNeverAtThresholdOne and the {1.0, 1.0}
	// passthrough row below, which must NOT move.
	if got.AutoApplyThreshold != 0.85 {
		t.Errorf("default AutoApplyThreshold = %v, want 0.85 (auto-apply at or above)",
			got.AutoApplyThreshold)
	}
	if got.MaxTokenDF != defaultResolveMaxTokenDF {
		t.Errorf("MaxTokenDF = %v, want %v", got.MaxTokenDF, defaultResolveMaxTokenDF)
	}
	if got.MinTokenDFFloor != defaultResolveMinTokenDFFloor {
		t.Errorf("MinTokenDFFloor = %d, want %d", got.MinTokenDFFloor, defaultResolveMinTokenDFFloor)
	}
	if got.EmbedThreshold != defaultResolveEmbedThreshold {
		t.Errorf("EmbedThreshold = %v, want %v", got.EmbedThreshold, defaultResolveEmbedThreshold)
	}
	if got.MaxEmbedCandidates != defaultResolveMaxEmbedCandidates {
		t.Errorf("MaxEmbedCandidates = %d, want %d",
			got.MaxEmbedCandidates, defaultResolveMaxEmbedCandidates)
	}
}

// An out-of-range threshold falls back to the DEFAULT — which, since the
// default moved to 0.85, is the PERMISSIVE side. The fallback must not be
// silent: a user who typed 1.5 meaning "stricter than 1.0" would otherwise get
// automatic linking with no signal. Unset (0, the yaml zero value) stays
// silent — that is every default user, not a typo.
func TestApplyResolveDefaultsWarnsOnOutOfRangeThreshold(t *testing.T) {
	for _, tc := range []struct {
		in       float64
		wantWarn bool
	}{
		{1.5, true},
		{-0.5, true},
		{0, false},   // unset — the default path, not a typo
		{0.5, false}, // valid
		{1.0, false}, // valid: explicit review-only
	} {
		out := captureWarns(t)
		applyResolveDefaults(config.ResolveConfig{AutoApplyThreshold: tc.in})
		warned := strings.Contains(out(), "auto_apply_threshold")
		if warned != tc.wantWarn {
			t.Errorf("threshold %v: warned = %v, want %v (log: %q)",
				tc.in, warned, tc.wantWarn, out())
		}
	}
}

// The threshold falls BACK rather than clamping. A configured 0 would otherwise
// auto-apply every proposal including zero-confidence ones — the worst outcome
// this pass can produce, and one a user could reach by typing a plausible value.
func TestApplyResolveDefaultsThresholdOutOfRange(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want float64
	}{
		{0, defaultResolveAutoApplyThreshold},
		{-0.5, defaultResolveAutoApplyThreshold},
		{1.5, defaultResolveAutoApplyThreshold},
		{0.5, 0.5},
		{1.0, 1.0},
	} {
		got := applyResolveDefaults(config.ResolveConfig{AutoApplyThreshold: tc.in})
		if got.AutoApplyThreshold != tc.want {
			t.Errorf("AutoApplyThreshold(%v) = %v, want %v", tc.in, got.AutoApplyThreshold, tc.want)
		}
	}
}

func TestApplyResolveDefaultsKeepsConfigured(t *testing.T) {
	in := config.ResolveConfig{
		MaxTokens: 999, MaxBlockSize: 7, AutoApplyThreshold: 0.42,
		MaxTokenDF: 0.9, MinTokenDFFloor: 3,
		EmbedThreshold: 0.1, MaxEmbedCandidates: 5,
	}
	got := applyResolveDefaults(in)
	if got != in {
		t.Errorf("configured values overwritten:\n got  %+v\n want %+v", got, in)
	}
}

// --- name normalization ---

func TestNormalizeNameTokens(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		// The two id spellings of one concept must normalize alike — this is
		// the whole reason normalization runs over Name, not id.
		{"self-attention", []string{"self", "attention"}},
		{"Self Attention", []string{"self", "attention"}},
		{"Buzz Aldrin", []string{"buzz", "aldrin"}},
		{"NASA's Apollo (program)", []string{"nasa", "apollo", "program"}},
		// Single characters carry no signal and would block half the vault.
		{"A B model", []string{"model"}},
		// Stopwords likewise.
		{"the state of the art", []string{"state", "art"}},
		{"", nil},
		{"   ", nil},
	} {
		got := normalizeNameTokens(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("normalizeNameTokens(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// --- discriminating tokens ---

func ent(id, name, def, article, created string) store.Entity {
	return store.Entity{ID: id, Type: "concept", Name: name,
		Definition: def, ArticlePath: article, CreatedAt: created}
}

// A token shared by a large fraction of a type carries no signal: it pulls
// hundreds of entities into one candidate set, and most of the resulting
// arbitration calls cannot produce a link by construction.
func TestDiscriminatingTokensDropsHighDF(t *testing.T) {
	var pool []store.Entity
	for i := 0; i < 480; i++ {
		pool = append(pool, ent(fmt.Sprintf("e%d", i), fmt.Sprintf("model variant %d", i), "", "", ""))
	}
	pool = append(pool, ent("x", "aldrin buzz", "", "", ""))

	ok := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)["concept"]
	if ok["model"] {
		t.Error(`"model" survived the DF filter; it appears in 480 of 481 entities`)
	}
	if !ok["aldrin"] {
		t.Error(`"aldrin" was discarded; it appears once`)
	}
}

// The absolute floor is the half that is easy to omit. Three variants of one
// name among 45 concepts is 6.7% — over a 5% threshold — so a percentage-only
// filter discards the very token that identifies the cluster, and the pass
// silently never links anything.
func TestDiscriminatingTokensFloorKeepsSmallCluster(t *testing.T) {
	var pool []store.Entity
	for i := 0; i < 42; i++ {
		pool = append(pool, ent(fmt.Sprintf("e%d", i), fmt.Sprintf("unrelated thing %d", i), "", "", ""))
	}
	pool = append(pool,
		ent("a1", "Buzz Aldrin", "", "", ""),
		ent("a2", "Edwin Aldrin", "", "", ""),
		ent("a3", "Aldrin Jr", "", "", ""),
	)

	ok := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)["concept"]
	if !ok["aldrin"] {
		t.Error(`"aldrin" (3 of 45 = 6.7%, over the 5% threshold) was discarded; ` +
			`the absolute floor must keep it`)
	}
}

// --- canonical election ---

// The row that owns the article is the one users land on and the one the query
// and web layers render, so it wins regardless of age.
func TestElectCanonicalPrefersArticlePath(t *testing.T) {
	members := []store.Entity{
		ent("described", "Self Attention", "a mechanism", "", "2026-01-01T00:00:00Z"),
		ent("self-attention", "Self Attention", "", "wiki/concepts/self-attention.md", "2026-06-01T00:00:00Z"),
	}
	got := electCanonical(members)
	if got.ID != "self-attention" {
		t.Errorf("elected %q, want the article-bearing row", got.ID)
	}
}

func TestElectCanonicalNewestWins(t *testing.T) {
	members := []store.Entity{
		ent("old", "X", "", "", "2026-01-01T00:00:00Z"),
		ent("new", "X", "", "", "2026-06-01T00:00:00Z"),
	}
	if got := electCanonical(members); got.ID != "new" {
		t.Errorf("elected %q, want the newest", got.ID)
	}
}

// created_at is nullable on both backends and reads back "". Under newest-wins,
// treating "" as newest would let any undated legacy row win every election
// unconditionally.
func TestElectCanonicalEmptyCreatedAtSortsOldest(t *testing.T) {
	members := []store.Entity{
		ent("undated", "X", "", "", ""),
		ent("dated", "X", "", "", "2026-01-01T00:00:00Z"),
	}
	if got := electCanonical(members); got.ID != "dated" {
		t.Errorf("elected %q, want the dated row — an empty created_at must lose", got.ID)
	}
}

func TestElectCanonicalTieBreaksOnID(t *testing.T) {
	members := []store.Entity{
		ent("bbb", "X", "", "", "2026-01-01T00:00:00Z"),
		ent("aaa", "X", "", "", "2026-01-01T00:00:00Z"),
	}
	if got := electCanonical(members); got.ID != "aaa" {
		t.Errorf("elected %q, want the sorted-first id on a tie", got.ID)
	}
}

// --- auto-apply predicate ---

func TestCanAutoApply(t *testing.T) {
	described := ent("a", "A", "a description", "", "")
	bare := ent("b", "B", "", "", "")

	for _, tc := range []struct {
		name    string
		cluster resolvedCluster
		x, y    store.Entity
		want    bool
	}{
		{"happy path", resolvedCluster{SameReferent: true, Confidence: 0.9}, described, bare, true},
		{"below threshold", resolvedCluster{SameReferent: true, Confidence: 0.5}, described, bare, false},
		{"broader flagged", resolvedCluster{SameReferent: true, Broader: true, Confidence: 0.99}, described, bare, false},
		{"not same referent", resolvedCluster{SameReferent: false, Confidence: 0.99}, described, bare, false},
		// Name-only evidence never auto-links. Under the default config concept
		// entities carry no Definition, so without this the pass would delete
		// nothing but would link on surface similarity alone.
		{"no description on either side", resolvedCluster{SameReferent: true, Confidence: 0.99}, bare, bare, false},
		{"description on the other side", resolvedCluster{SameReferent: true, Confidence: 0.99}, bare, described, true},
	} {
		got := canAutoApply(tc.cluster, tc.x, tc.y, 0.85)
		if got != tc.want {
			t.Errorf("%s: canAutoApply = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// --- response normalization ---

func labelled(ids ...string) map[string]store.Entity {
	m := map[string]store.Entity{}
	for i, id := range ids {
		m[fmt.Sprintf("E%d", i+1)] = ent(id, id, "", "", "")
	}
	return m
}

func TestNormalizeClustersDropsUnknownLabels(t *testing.T) {
	labels := labelled("a", "b")
	got := normalizeClusters([]resolvedCluster{
		{Members: []string{"E1", "E2", "E99"}, SameReferent: true, Confidence: 0.9},
	}, labels, nil)

	if len(got) != 1 {
		t.Fatalf("clusters = %d, want 1", len(got))
	}
	if len(got[0].entities) != 2 {
		t.Errorf("members = %d, want 2 (the hallucinated label dropped)", len(got[0].entities))
	}
}

func TestNormalizeClustersDropsSingletonAndNonReferent(t *testing.T) {
	labels := labelled("a", "b")
	got := normalizeClusters([]resolvedCluster{
		{Members: []string{"E1"}, SameReferent: true, Confidence: 0.9},
		{Members: []string{"E1", "E2"}, SameReferent: false, Confidence: 0.9},
	}, labels, nil)
	if len(got) != 0 {
		t.Errorf("clusters = %d, want 0 (one singleton, one not-same-referent)", len(got))
	}
}

// Deterministic, so a re-run proposes the same thing rather than flip-flopping.
func TestNormalizeClustersFirstClusterWinsDuplicateMember(t *testing.T) {
	labels := labelled("a", "b", "c")
	got := normalizeClusters([]resolvedCluster{
		{Members: []string{"E1", "E2"}, SameReferent: true, Confidence: 0.9},
		{Members: []string{"E1", "E3"}, SameReferent: true, Confidence: 0.9},
	}, labels, nil)

	if len(got) != 1 {
		t.Fatalf("clusters = %d, want 1 (second dropped to a singleton after dedup)", len(got))
	}
	ids := map[string]bool{}
	for _, e := range got[0].entities {
		ids[e.ID] = true
	}
	if !ids["a"] || !ids["b"] {
		t.Errorf("first cluster lost members: %v", ids)
	}
}

func TestNormalizeClustersClampsConfidence(t *testing.T) {
	labels := labelled("a", "b")
	got := normalizeClusters([]resolvedCluster{
		{Members: []string{"E1", "E2"}, SameReferent: true, Confidence: 3.7},
	}, labels, nil)
	if len(got) != 1 || got[0].confidence != 1 {
		t.Errorf("confidence = %v, want clamped to 1", got)
	}
}

// --- blocking ---

func idsOf(es []store.Entity) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.ID)
	}
	sort.Strings(out)
	return out
}

func noneRejected(string, string) bool { return false }

// Blocks are seeded ONLY by touched entities. A block with no touched entity
// cannot produce a link by construction, so spending an arbitration call on it
// is pure waste — and with token blocking over a large vault, most candidate
// groups are exactly that.
func TestBuildBlocksSeededByTouchedOnly(t *testing.T) {
	pool := []store.Entity{
		ent("buzz-aldrin", "Buzz Aldrin", "", "wiki/buzz.md", "2026-01-01T00:00:00Z"),
		ent("Edwin Aldrin", "Edwin Aldrin", "an astronaut", "", "2026-02-01T00:00:00Z"),
		ent("neil-armstrong", "Neil Armstrong", "", "", "2026-01-01T00:00:00Z"),
		ent("armstrong-musician", "Armstrong Musician", "", "", "2026-01-01T00:00:00Z"),
	}
	touched := []store.Entity{pool[1]} // only "Edwin Aldrin" was touched
	tokens := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)
	cfg := applyResolveDefaults(config.ResolveConfig{})

	blocks := buildBlocks(touched, pool, tokens, nil, cfg, noneRejected)

	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (one touched seed)", len(blocks))
	}
	got := idsOf(blocks[0].members)
	want := []string{"Edwin Aldrin", "buzz-aldrin"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("block members = %v, want %v", got, want)
	}
	// The two Armstrongs share a token with each other but neither is touched,
	// so no call is spent on them.
	for _, b := range blocks {
		for _, m := range b.members {
			if strings.Contains(m.ID, "armstrong") {
				t.Errorf("untouched-only group %q was blocked", m.ID)
			}
		}
	}
}

// A new entity that matches nothing costs ZERO arbitration calls. This is the
// incremental-cost property the whole design rests on.
func TestBuildBlocksSkipsSeedWithNoCandidates(t *testing.T) {
	pool := []store.Entity{
		ent("unique-thing", "Unique Thing", "", "", ""),
		ent("other", "Completely Different", "", "", ""),
	}
	tokens := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)
	cfg := applyResolveDefaults(config.ResolveConfig{})

	blocks := buildBlocks([]store.Entity{pool[0]}, pool, tokens, nil, cfg, noneRejected)
	if len(blocks) != 0 {
		t.Errorf("blocks = %d, want 0 — an unmatched entity must cost no LLM call", len(blocks))
	}
}

func TestBuildBlocksRespectsMaxBlockSize(t *testing.T) {
	var pool []store.Entity
	for i := 0; i < 200; i++ {
		pool = append(pool, ent(fmt.Sprintf("aldrin-%d", i), fmt.Sprintf("Aldrin %d", i), "", "", ""))
	}
	cfg := applyResolveDefaults(config.ResolveConfig{MaxBlockSize: 10})
	// Force "aldrin" to count as discriminating so the block would be huge.
	tokens := map[string]map[string]bool{"concept": {"aldrin": true}}

	blocks := buildBlocks([]store.Entity{pool[0]}, pool, tokens, nil, cfg, noneRejected)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	if len(blocks[0].members) > 10 {
		t.Errorf("block size = %d, want <= 10", len(blocks[0].members))
	}
	// The seed must survive its own truncation.
	found := false
	for _, m := range blocks[0].members {
		if m.ID == pool[0].ID {
			found = true
		}
	}
	if !found {
		t.Error("the seed was truncated out of its own block")
	}
}

// A rejected pair must not even be offered to the model — re-proposing it wastes
// a call and risks re-linking what a human separated.
func TestBuildBlocksExcludesRejectedPairs(t *testing.T) {
	pool := []store.Entity{
		ent("armstrong-astronaut", "Neil Armstrong", "", "", ""),
		ent("armstrong-musician", "Louis Armstrong", "", "", ""),
	}
	tokens := map[string]map[string]bool{"concept": {"armstrong": true, "neil": true, "louis": true}}
	cfg := applyResolveDefaults(config.ResolveConfig{})

	rejected := func(a, b string) bool {
		return (a == "armstrong-astronaut" && b == "armstrong-musician") ||
			(a == "armstrong-musician" && b == "armstrong-astronaut")
	}
	blocks := buildBlocks([]store.Entity{pool[0]}, pool, tokens, nil, cfg, rejected)
	if len(blocks) != 0 {
		t.Errorf("blocks = %d, want 0 — the only candidate is a rejected pair", len(blocks))
	}
}

// Embeddings widen recall to names sharing no tokens at all, which is the whole
// reason the signal exists.
func TestBuildBlocksEmbeddingAddsTokenlessCandidate(t *testing.T) {
	pool := []store.Entity{
		ent("nyc", "NYC", "", "", ""),
		ent("new-york-city", "New York City", "", "", ""),
	}
	tokens := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)
	cfg := applyResolveDefaults(config.ResolveConfig{UseEmbeddings: true})

	// No shared tokens: lexical blocking alone finds nothing.
	if blocks := buildBlocks([]store.Entity{pool[0]}, pool, tokens, nil, cfg, noneRejected); len(blocks) != 0 {
		t.Fatalf("lexical blocking unexpectedly matched: %d blocks", len(blocks))
	}

	vecs := map[string][]float32{
		"nyc":           {1, 0, 0},
		"new-york-city": {0.99, 0.14, 0},
	}
	blocks := buildBlocks([]store.Entity{pool[0]}, pool, tokens, vecs, cfg, noneRejected)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1 (cosine above threshold)", len(blocks))
	}
	if got := idsOf(blocks[0].members); !reflect.DeepEqual(got, []string{"new-york-city", "nyc"}) {
		t.Errorf("members = %v", got)
	}
}

// A dimension mismatch skips the pair rather than panicking — an embedder swap
// mid-run is the realistic trigger.
func TestBuildBlocksEmbeddingDimensionMismatchSkips(t *testing.T) {
	pool := []store.Entity{
		ent("nyc", "NYC", "", "", ""),
		ent("new-york-city", "New York City", "", "", ""),
	}
	cfg := applyResolveDefaults(config.ResolveConfig{UseEmbeddings: true})
	vecs := map[string][]float32{
		"nyc":           {1, 0, 0},
		"new-york-city": {1, 0}, // different dimension
	}
	blocks := buildBlocks([]store.Entity{pool[0]}, pool, map[string]map[string]bool{}, vecs, cfg, noneRejected)
	if len(blocks) != 0 {
		t.Errorf("blocks = %d, want 0 — a dimension mismatch must skip, not match", len(blocks))
	}
}

// Labels are what the model sees. They must cover every member and map back
// uniquely, because Entity.ID != Entity.Name and two rows can share a Name.
func TestBlockLabelsAreTotalAndUnique(t *testing.T) {
	pool := []store.Entity{
		ent("a", "Shared Name", "", "", ""),
		ent("b", "Shared Name", "", "", ""),
	}
	tokens := map[string]map[string]bool{"concept": {"shared": true, "name": true}}
	cfg := applyResolveDefaults(config.ResolveConfig{})

	blocks := buildBlocks([]store.Entity{pool[0]}, pool, tokens, nil, cfg, noneRejected)
	if len(blocks) != 1 {
		t.Fatalf("blocks = %d, want 1", len(blocks))
	}
	b := blocks[0]
	if len(b.labels) != len(b.members) {
		t.Fatalf("labels = %d, members = %d; the mapping must be total",
			len(b.labels), len(b.members))
	}
	seen := map[string]bool{}
	for label, e := range b.labels {
		if seen[e.ID] {
			t.Errorf("entity %q has two labels", e.ID)
		}
		seen[e.ID] = true
		if !strings.HasPrefix(label, "E") {
			t.Errorf("label %q is not opaque", label)
		}
	}

	// The rendered block must carry the labels and names but never an id — ids
	// are what the model must not echo back.
	rendered := renderBlockMembers(b)
	for _, want := range []string{"E1", "E2", "Shared Name"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered block missing %q:\n%s", want, rendered)
		}
	}
}

// GATE-3 regression. Document frequency must be computed PER TYPE, as the spec
// and both docs state. Over the merged pool, a large type swamps a small one:
// 400 techniques sharing "attention" would push the token over the limit and
// stop three concepts named "Self Attention" from ever blocking — the exact
// failure min_token_df_floor exists to prevent, reintroduced across types.
func TestDiscriminatingTokensAreScopedPerType(t *testing.T) {
	var pool []store.Entity
	for i := 0; i < 400; i++ {
		e := ent(fmt.Sprintf("t%d", i), fmt.Sprintf("attention variant %d", i), "", "", "")
		e.Type = "technique"
		pool = append(pool, e)
	}
	for i := 0; i < 3; i++ {
		pool = append(pool, ent(fmt.Sprintf("c%d", i), fmt.Sprintf("Self Attention %d", i), "", "", ""))
	}

	byType := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)

	if byType["technique"]["attention"] {
		t.Error(`"attention" should be discarded for technique (400 of 400)`)
	}
	if !byType["concept"]["attention"] {
		t.Error(`"attention" was discarded for concept (3 of 3) because a larger ` +
			`type swamped it — DF must be per type`)
	}
}

// --- GATE-3 R4: the co-absorption closure, pinned by construction ---
//
// Round 4 mutation-proved the previous tests did not pin this: regressing
// cluster() to round 2's direct-canonical keying failed ZERO tests. These
// exercise the graph directly so a topology regression cannot hide behind the
// pass's earlier guards (GetActiveAlias, buildBlocks) firing first.

func graphOf(pairs ...[2]string) *aliasGraph {
	g := &aliasGraph{canonicalOf: map[string]string{}, memo: map[string]string{}}
	for _, p := range pairs {
		g.canonicalOf[p[0]] = p[1]
	}
	return g
}

func rejectionsOf(pairs ...[2]string) *rejectionIndex {
	r := &rejectionIndex{partners: map[string]map[string]bool{}}
	for _, p := range pairs {
		r.mark(p[0], p[1])
		r.mark(p[1], p[0])
	}
	return r
}

// Terminal keying, not direct keying. x sits TWO hops under z; a guard keyed on
// the direct canonical_id sees only y when asked about z and lets the link
// through. LinkAlias never rewrites rows to the terminal, so this is the steady
// state, not an edge case.
func TestCoAbsorptionSeesThroughMultipleHops(t *testing.T) {
	g := graphOf([2]string{"x", "y"}, [2]string{"y", "z"})
	rej := rejectionsOf([2]string{"a", "x"})

	if got := pairConflict(g, rej, "a", "z"); got == "" {
		t.Error("linking a -> z was allowed although x, which the user separated " +
			"from a, resolves to z through two hops")
	}
	// An unrelated rejection must not block.
	if got := pairConflict(g, rejectionsOf([2]string{"p", "q"}), "a", "z"); got != "" {
		t.Errorf("false positive: %q", got)
	}
}

// The check must be SYMMETRIC. Linking does not move one entity — everything
// already resolving to the alias follows it under the target. An entity that is
// a canonical rather than an alias is a perfectly legal seed.
func TestCoAbsorptionSeesTheAliasSideCluster(t *testing.T) {
	// x resolves to a; the user separated x from t.
	g := graphOf([2]string{"x", "a"})
	rej := rejectionsOf([2]string{"x", "t"})

	if got := pairConflict(g, rej, "a", "t"); got == "" {
		t.Error("linking a -> t was allowed although x resolves to a and the user " +
			"separated x from t; linking drags x under t as well")
	}
}

// Both sides at once, each several hops deep.
func TestCoAbsorptionCrossProductBothSides(t *testing.T) {
	g := graphOf(
		[2]string{"m1", "m2"}, [2]string{"m2", "alias"}, // m1 -> m2 -> alias
		[2]string{"l1", "l2"}, [2]string{"l2", "target"}, // l1 -> l2 -> target
	)
	rej := rejectionsOf([2]string{"m1", "l1"})

	if got := pairConflict(g, rej, "alias", "target"); got == "" {
		t.Error("a rejection between the deepest member of each side was missed")
	}
}

func TestAliasGraphTerminalAndCluster(t *testing.T) {
	g := graphOf([2]string{"x", "y"}, [2]string{"y", "z"}, [2]string{"w", "z"})

	if got := g.terminal("x"); got != "z" {
		t.Errorf("terminal(x) = %q, want z", got)
	}
	if got := g.terminal("z"); got != "z" {
		t.Errorf("terminal(z) = %q, want z", got)
	}
	if got := g.terminal("unknown"); got != "unknown" {
		t.Errorf("terminal(unknown) = %q, want itself", got)
	}

	members := map[string]bool{}
	for _, m := range g.cluster("z") {
		members[m] = true
	}
	for _, want := range []string{"z", "x", "y", "w"} {
		if !members[want] {
			t.Errorf("cluster(z) missing %q: %v", want, members)
		}
	}
}

// add() must invalidate the memo: a new edge changes the terminal of everything
// upstream of the alias, and a stale memo answers with the pre-link topology.
func TestAliasGraphAddInvalidatesMemo(t *testing.T) {
	g := graphOf([2]string{"x", "a"})
	if got := g.terminal("x"); got != "a" { // populates the memo
		t.Fatalf("terminal(x) = %q, want a", got)
	}
	g.add("a", "b")
	if got := g.terminal("x"); got != "b" {
		t.Errorf("terminal(x) = %q after a -> b was added, want b — the memo is stale", got)
	}
}

func TestAliasGraphTerminalTerminatesOnCycle(t *testing.T) {
	g := graphOf([2]string{"a", "b"}, [2]string{"b", "a"})
	done := make(chan string, 1)
	go func() { done <- g.terminal("a") }()
	select {
	case got := <-done:
		// The same answer ontology.CanonicalID gives. Returning an
		// entry-point-dependent node instead makes the two resolvers disagree
		// about the same graph.
		if got != "a" {
			t.Errorf("terminal on a cycle = %q, want the input id (parity with CanonicalID)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("terminal looped forever on a cycle")
	}
}

// A cycle must not split the component: terminal-keying puts a and b in separate
// singletons and hides a rejection between their two sides.
func TestAliasGraphClusterIsCycleProof(t *testing.T) {
	g := graphOf([2]string{"a", "b"}, [2]string{"b", "a"}, [2]string{"x", "a"})
	got := map[string]bool{}
	for _, m := range g.cluster("b") {
		got[m] = true
	}
	for _, want := range []string{"a", "b", "x"} {
		if !got[want] {
			t.Errorf("cluster(b) missing %q on a cycle: %v", want, got)
		}
	}
}

// GATE-3 R6. The pending gate must be the CROSS PRODUCT, like the rejection
// gate beside it. A direct-pair check lets a pending A/B be settled by linking a
// third entity that unifies them transitively.
func TestPairConflictAwaitingIsTransitive(t *testing.T) {
	// B already resolves to T; A/B is awaiting a human.
	g := graphOf([2]string{"B", "T"})
	rej := &rejectionIndex{
		partners: map[string]map[string]bool{},
		awaiting: map[string]map[string]bool{},
	}
	rej.markAwaiting("A", "B")

	if got := pairConflict(g, rej, "T", "A"); got == "" {
		t.Error("linking T -> A was allowed although B resolves to T and A/B is " +
			"awaiting review — the link settles that pair transitively")
	}
	// An unrelated pending pair must not block.
	clean := &rejectionIndex{
		partners: map[string]map[string]bool{},
		awaiting: map[string]map[string]bool{},
	}
	clean.markAwaiting("P", "Q")
	if got := pairConflict(g, clean, "T", "A"); got != "" {
		t.Errorf("false positive on an unrelated pending pair: %q", got)
	}
}

// The pair a human is settling must not block itself, or every proposal becomes
// permanently unapplicable — but a REJECTION of that same pair still blocks,
// because that is a decision rather than a question.
func TestPairConflictExceptIgnoresOnlyTheSettledPair(t *testing.T) {
	g := graphOf()
	rej := &rejectionIndex{
		partners: map[string]map[string]bool{},
		awaiting: map[string]map[string]bool{},
	}
	rej.markAwaiting("A", "B")

	if got := pairConflictExcept(g, rej, "A", "B", "A", "B"); got != "" {
		t.Errorf("the pair being settled blocked itself: %q", got)
	}
	rej.mark("A", "B")
	rej.mark("B", "A")
	if got := pairConflictExcept(g, rej, "A", "B", "A", "B"); got == "" {
		t.Error("a REJECTED pair must still block even when it is the pair being settled")
	}
}

// The snapshot must stay fresh: the pass writes pending rows, so a pair queued
// by an early block has to be visible to a later one.
func TestRejectionIndexMarkAwaitingIsSymmetricAndLive(t *testing.T) {
	rej := &rejectionIndex{
		partners: map[string]map[string]bool{},
		awaiting: map[string]map[string]bool{},
	}
	if rej.awaitingReview("A", "B") {
		t.Fatal("empty index reported an awaiting pair")
	}
	rej.markAwaiting("A", "B")
	if !rej.awaitingReview("A", "B") || !rej.awaitingReview("B", "A") {
		t.Error("markAwaiting must record the pair in both directions")
	}
}

// Seeds must be embedded before pool candidates, or a seed whose id sorts past
// the cap gets no vector and its entire embedding spend buys nothing.
func TestEmbedForBlockingPrioritisesSeeds(t *testing.T) {
	seed := ent("zzz-seed", "Zzz Seed", "", "", "")
	var pool []store.Entity
	for i := 0; i < 5; i++ {
		pool = append(pool, ent(fmt.Sprintf("a%d", i), fmt.Sprintf("A %d", i), "", "", ""))
	}
	cfg := applyResolveDefaults(config.ResolveConfig{
		UseEmbeddings: true, MaxEmbedCandidates: 3,
	})
	emb := &seedFirstEmbedder{}

	vecs := embedForBlocking(context.Background(), emb, []store.Entity{seed}, pool, cfg)

	if _, ok := vecs["zzz-seed"]; !ok {
		t.Errorf("the seed was truncated out of the embed set by the cap; "+
			"every one of its pairs now fails the cosine test. embedded: %v", keysOf(vecs))
	}
	if len(vecs) > 3 {
		t.Errorf("embedded %d, want <= the cap of 3", len(vecs))
	}
}

type seedFirstEmbedder struct{ n int }

func (c *seedFirstEmbedder) Embed(string) ([]float32, error) {
	c.n++
	return []float32{1, 0, 0}, nil
}
func (c *seedFirstEmbedder) Dimensions() int { return 3 }
func (c *seedFirstEmbedder) Name() string    { return "seed-first" }

func keysOf(m map[string][]float32) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// 1.0 means NEVER, exactly — not "practically never". normalizeClusters clamps
// confidence to [0,1], so a model returning 1.0 is legal and would otherwise
// satisfy `confidence >= threshold`. A safety default a model can defeat by
// being confident is not a safety default.
func TestCanAutoApplyNeverAtThresholdOne(t *testing.T) {
	both := resolvedCluster{SameReferent: true, Broader: false, Confidence: 1.0}
	x := ent("a", "A", "a description", "", "")
	y := ent("b", "B", "another description", "", "")
	if canAutoApply(both, x, y, 1.0) {
		t.Error("canAutoApply = true at threshold 1.0 with confidence 1.0; " +
			"1.0 must mean never, by an explicit branch rather than float luck")
	}
}

// The opt-in is intact: a lowered threshold behaves exactly as before. One side
// described, because the description requirement is an OR — two bare entities
// would return false and this test would pass for the wrong reason.
func TestCanAutoApplyStillWorksBelowOne(t *testing.T) {
	c := resolvedCluster{SameReferent: true, Broader: false, Confidence: 0.9}
	x := ent("a", "A", "a description", "", "")
	y := ent("b", "B", "", "", "")
	if !canAutoApply(c, x, y, 0.85) {
		t.Error("canAutoApply = false at threshold 0.85 with confidence 0.9; " +
			"lowering the threshold must still opt in")
	}
}
