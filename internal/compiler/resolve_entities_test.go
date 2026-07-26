package compiler

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

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
		got := canAutoApply(tc.cluster, tc.x, tc.y, defaultResolveAutoApplyThreshold)
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
