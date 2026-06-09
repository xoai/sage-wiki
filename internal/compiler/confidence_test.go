package compiler

import (
	"strings"
	"testing"

	"github.com/xoai/sage-wiki/internal/manifest"
)

func TestComputeSourceCoverage(t *testing.T) {
	source := "Self-attention computes contextual representations. Flash attention optimizes memory access patterns for transformer models."
	article := "Self-attention computes contextual representations by weighing input tokens. Flash attention reduces memory overhead."

	coverage := computeSourceCoverage(article, source)
	if coverage < 0.2 {
		t.Errorf("coverage = %.2f, expected >= 0.2 (some source phrases should appear)", coverage)
	}
	if coverage > 1.0 {
		t.Errorf("coverage = %.2f, should be <= 1.0", coverage)
	}
}

func TestComputeSourceCoverage_Empty(t *testing.T) {
	if c := computeSourceCoverage("", "some source"); c != 0 {
		t.Errorf("empty article should give 0, got %.2f", c)
	}
	if c := computeSourceCoverage("some article", ""); c != 0 {
		t.Errorf("empty source should give 0, got %.2f", c)
	}
}

func TestExtractKeyPhrases(t *testing.T) {
	text := "Self-attention computes contextual representations by weighing tokens. This enables parallel processing of sequences."
	phrases := extractKeyPhrases(text)
	if len(phrases) == 0 {
		t.Fatal("expected at least one key phrase")
	}
	if len(phrases) > 50 {
		t.Errorf("phrases should be capped at 50, got %d", len(phrases))
	}
}

func TestScoreFormat(t *testing.T) {
	good := "---\nconcept: x\n---\n\nIntro.\n\n## Definition\n\nBody text here."
	if s := scoreFormat(good); s != 1.0 {
		t.Errorf("well-formed article Format = %.2f, want 1.0", s)
	}

	// Heading-less stub still scores > 0 (frontmatter + balanced fences pass).
	stub := "---\nconcept: x\n---\n\nJust a sentence, no headings."
	if s := scoreFormat(stub); s <= 0 {
		t.Errorf("heading-less stub Format = %.2f, want > 0", s)
	}

	// Unbalanced code fence drops the fence sub-check.
	unbalanced := "---\nconcept: x\n---\n\n## H\n\n```go\ncode"
	full := "---\nconcept: x\n---\n\n## H\n\n```go\ncode\n```"
	if scoreFormat(unbalanced) >= scoreFormat(full) {
		t.Errorf("unbalanced fences should score lower: %.2f !< %.2f",
			scoreFormat(unbalanced), scoreFormat(full))
	}
}

func TestScoreCoverage(t *testing.T) {
	// Empty source → 0.
	if c := scoreCoverage("article", ""); c != 0 {
		t.Errorf("empty source coverage = %.2f, want 0", c)
	}
	// Long article vs small source → capped at 1.0.
	src := strings.Repeat("x", 1000)
	long := strings.Repeat("y", 5000)
	if c := scoreCoverage(long, src); c != 1.0 {
		t.Errorf("long article coverage = %.2f, want 1.0 (capped)", c)
	}
	// Very short article vs large source → < 1.0.
	bigSrc := strings.Repeat("x", 100000)
	short := strings.Repeat("y", 100)
	if c := scoreCoverage(short, bigSrc); c >= 1.0 {
		t.Errorf("short article vs huge source = %.2f, want < 1.0", c)
	}
}

func TestScoreWikilink(t *testing.T) {
	mf := manifest.New()
	mf.AddConcept("alpha", "wiki/concepts/alpha.md", []string{"raw/a.md"})
	mf.AddConcept("beta", "wiki/concepts/beta.md", []string{"raw/b.md"})

	// No links → 1.0 (nothing broken).
	if w := scoreWikilink("plain text, no links", mf); w != 1.0 {
		t.Errorf("no-links Wikilink = %.2f, want 1.0", w)
	}
	// Nil manifest → 1.0.
	if w := scoreWikilink("see [[alpha]]", nil); w != 1.0 {
		t.Errorf("nil-manifest Wikilink = %.2f, want 1.0", w)
	}
	// All resolve → 1.0.
	if w := scoreWikilink("see [[alpha]] and [[beta]]", mf); w != 1.0 {
		t.Errorf("all-resolve Wikilink = %.2f, want 1.0", w)
	}
	// Half resolve → 0.5.
	if w := scoreWikilink("see [[alpha]] and [[ghost]]", mf); w != 0.5 {
		t.Errorf("half-resolve Wikilink = %.2f, want 0.5", w)
	}
}

func TestScoreAntiPattern(t *testing.T) {
	clean := "Self-attention computes weighted token representations efficiently."
	if a := scoreAntiPattern(clean); a != 1.0 {
		t.Errorf("clean article AntiPattern = %.2f, want 1.0", a)
	}
	// Filler phrases drop the score.
	filler := "In conclusion, it is important to note that this article will delve into the topic."
	if a := scoreAntiPattern(filler); a >= 1.0 {
		t.Errorf("filler-laden article AntiPattern = %.2f, want < 1.0", a)
	}
	// Heavy filler floors at 0 (never negative).
	heavy := strings.Repeat("in conclusion ", 50)
	if a := scoreAntiPattern(heavy); a < 0 {
		t.Errorf("AntiPattern = %.2f, must not go negative", a)
	}
}

func TestQualityWeightsNormalized(t *testing.T) {
	w := QualityWeights{Format: 1, Grounding: 1, Coverage: 1, Wikilink: 1, AntiPattern: 1}.Normalized()
	sum := w.Format + w.Grounding + w.Coverage + w.Wikilink + w.AntiPattern
	if sum < 0.999 || sum > 1.001 {
		t.Errorf("normalized weights sum = %.4f, want 1.0", sum)
	}
	if w.Format < 0.199 || w.Format > 0.201 {
		t.Errorf("equal weights should each normalize to 0.2, got %.4f", w.Format)
	}

	// All-zero weights fall back to defaults.
	zero := QualityWeights{}.Normalized()
	if zero != DefaultQualityWeights() {
		t.Errorf("all-zero weights should fall back to defaults, got %+v", zero)
	}
}

func TestScoreArticle_Combined(t *testing.T) {
	source := "Neural networks learn hierarchical features. Backpropagation computes gradients efficiently."
	article := "---\nconcept: neural-networks\n---\n\n## Definition\n\nNeural networks learn hierarchical features through multiple layers. Backpropagation is used to compute gradients."

	mf := manifest.New()
	mf.AddConcept("neural-networks", "wiki/concepts/neural-networks.md", []string{"raw/nn.md"})

	scores := ScoreArticle(article, source, "neural-networks", mf, DefaultQualityWeights())
	if scores.Combined < 0 || scores.Combined > 1 {
		t.Errorf("combined score = %.2f, should be 0-1", scores.Combined)
	}
	if scores.Format != 1.0 {
		t.Errorf("well-formed article Format = %.2f, want 1.0", scores.Format)
	}
	if scores.Grounding <= 0 {
		t.Error("grounding should be positive (source phrases present)")
	}
	if scores.Wikilink != 1.0 {
		t.Errorf("no wikilinks → Wikilink = %.2f, want 1.0", scores.Wikilink)
	}
}
