package compiler

import (
	"fmt"
	"reflect"
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

	ok := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)
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

	ok := discriminatingTokens(pool, defaultResolveMaxTokenDF, defaultResolveMinTokenDFFloor)
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
	}, labels)

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
	}, labels)
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
	}, labels)

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
	}, labels)
	if len(got) != 1 || got[0].confidence != 1 {
		t.Errorf("confidence = %v, want clamped to 1", got)
	}
}
