package compiler

import (
	"strings"

	"github.com/xoai/sage-wiki/internal/manifest"
)

// QualityScores holds the zero-LLM 5-dimension quality metrics for a
// compiled article (issue #97). Each dimension is in [0,1]; Combined is the
// normalised weighted average.
type QualityScores struct {
	Format      float64 // valid frontmatter, section headings, balanced code fences
	Grounding   float64 // fraction of source key phrases present in the article
	Coverage    float64 // article length adequacy vs. source size
	Wikilink    float64 // fraction of [[wikilinks]] that resolve to known concepts
	AntiPattern float64 // inverse of filler/meta-phrase density
	Combined    float64 // weighted average (0.0–1.0)
}

// QualityWeights are the per-dimension weights for the composite score.
type QualityWeights struct {
	Format      float64
	Grounding   float64
	Coverage    float64
	Wikilink    float64
	AntiPattern float64
}

// DefaultQualityWeights returns the issue #97 proposed weights.
func DefaultQualityWeights() QualityWeights {
	return QualityWeights{
		Format:      0.15,
		Grounding:   0.30,
		Coverage:    0.20,
		Wikilink:    0.15,
		AntiPattern: 0.20,
	}
}

// Normalized scales the weights so they sum to 1. If the weights sum to
// zero (or less), it falls back to DefaultQualityWeights — this guards
// against a mis-configured all-zero weight set.
func (w QualityWeights) Normalized() QualityWeights {
	sum := w.Format + w.Grounding + w.Coverage + w.Wikilink + w.AntiPattern
	if sum <= 0 {
		return DefaultQualityWeights()
	}
	return QualityWeights{
		Format:      w.Format / sum,
		Grounding:   w.Grounding / sum,
		Coverage:    w.Coverage / sum,
		Wikilink:    w.Wikilink / sum,
		AntiPattern: w.AntiPattern / sum,
	}
}

// fillerPhrases are lowercased meta/filler phrases the article prompt is
// expected to suppress. Hits penalise the AntiPattern dimension. Fixed list
// (issue #97); refine during calibration.
var fillerPhrases = []string{
	"in conclusion",
	"in summary",
	"it is important to note",
	"it's important to note",
	"it is worth noting",
	"it's worth noting",
	"as an ai",
	"this article discusses",
	"in this article",
	"this article will",
	"delve into",
	"needless to say",
	"at the end of the day",
	"when it comes to",
	"in today's world",
	"last but not least",
}

// ScoreArticle computes the 5-dimension quality score for a compiled article.
// articleText is the final on-disk article (frontmatter already built),
// sourceText is the concatenated raw source content, mf provides the set of
// known concepts for wikilink resolution, and w supplies the dimension
// weights (normalised internally). conceptName is retained for signature
// stability and future per-concept heuristics.
func ScoreArticle(articleText, sourceText, conceptName string, mf *manifest.Manifest, w QualityWeights) QualityScores {
	nw := w.Normalized()

	s := QualityScores{
		Format:      scoreFormat(articleText),
		Grounding:   scoreGrounding(articleText, sourceText),
		Coverage:    scoreCoverage(articleText, sourceText),
		Wikilink:    scoreWikilink(articleText, mf),
		AntiPattern: scoreAntiPattern(articleText),
	}

	s.Combined = s.Format*nw.Format +
		s.Grounding*nw.Grounding +
		s.Coverage*nw.Coverage +
		s.Wikilink*nw.Wikilink +
		s.AntiPattern*nw.AntiPattern

	return s
}

// scoreFormat averages three structural sub-checks (each 0 or 1):
// valid frontmatter, at least one section heading, and balanced code fences.
func scoreFormat(article string) float64 {
	var score float64

	// Sub-check 1: frontmatter present and carries the concept key.
	trimmed := strings.TrimSpace(article)
	if strings.HasPrefix(trimmed, "---") && strings.Contains(article, "concept:") {
		score++
	}

	// Sub-check 2: at least one "## " section heading.
	for _, line := range strings.Split(article, "\n") {
		if strings.HasPrefix(line, "## ") {
			score++
			break
		}
	}

	// Sub-check 3: balanced (even) count of code fences.
	if strings.Count(article, "```")%2 == 0 {
		score++
	}

	return score / 3.0
}

// scoreGrounding measures how well the article reflects the source: the
// fraction of source key phrases that appear in the article.
func scoreGrounding(article, source string) float64 {
	return computeSourceCoverage(article, source)
}

// scoreCoverage measures article length adequacy relative to source size.
// expected = clamp(len(source)*0.15, 400, 6000) chars; score = min(1, len/expected).
func scoreCoverage(article, source string) float64 {
	if source == "" || article == "" {
		return 0
	}
	expected := clampFloat(float64(len(source))*0.15, 400, 6000)
	ratio := float64(len(article)) / expected
	if ratio > 1 {
		return 1
	}
	return ratio
}

// scoreWikilink returns the fraction of [[wikilinks]] in the article that
// resolve to a known concept. Resolution uses the manifest concept keyset
// (fully populated before the write loop), NOT on-disk files, so the score
// is stable regardless of article write ordering. No links (or no manifest)
// → 1.0 (nothing broken).
func scoreWikilink(article string, mf *manifest.Manifest) float64 {
	links := wikilinkRe.FindAllStringSubmatch(article, -1)
	if len(links) == 0 || mf == nil {
		return 1.0
	}
	resolved := 0
	for _, m := range links {
		if _, ok := mf.Concepts[m[1]]; ok {
			resolved++
		}
	}
	return float64(resolved) / float64(len(links))
}

// scoreAntiPattern penalises filler/meta phrases: 1 - min(1, 0.1*hits).
func scoreAntiPattern(article string) float64 {
	lower := strings.ToLower(article)
	hits := 0
	for _, p := range fillerPhrases {
		hits += strings.Count(lower, p)
	}
	penalty := 0.1 * float64(hits)
	if penalty > 1 {
		penalty = 1
	}
	return 1 - penalty
}

func clampFloat(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// computeSourceCoverage extracts key phrases from the source and checks
// how many appear in the article text.
func computeSourceCoverage(articleText, sourceText string) float64 {
	if sourceText == "" || articleText == "" {
		return 0
	}

	// Extract sentences from source (split on . ! ? and newlines)
	phrases := extractKeyPhrases(sourceText)
	if len(phrases) == 0 {
		return 0
	}

	articleLower := strings.ToLower(articleText)
	found := 0
	for _, phrase := range phrases {
		if strings.Contains(articleLower, strings.ToLower(phrase)) {
			found++
		}
	}

	return float64(found) / float64(len(phrases))
}

// extractKeyPhrases extracts short, meaningful phrases from text.
// Uses a simple approach: take unique multi-word segments (3-6 words) from sentences.
func extractKeyPhrases(text string) []string {
	// Split into sentences
	sentences := strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?' || r == '\n'
	})

	seen := make(map[string]bool)
	var phrases []string

	for _, sentence := range sentences {
		sentence = strings.TrimSpace(sentence)
		words := strings.Fields(sentence)

		// Take 3-word windows as key phrases
		for i := 0; i+3 <= len(words) && i < 5; i++ {
			phrase := strings.Join(words[i:i+3], " ")
			phrase = strings.ToLower(phrase)
			if len(phrase) < 10 {
				continue // skip very short phrases
			}
			if !seen[phrase] {
				seen[phrase] = true
				phrases = append(phrases, phrase)
			}
		}
	}

	// Cap at 50 phrases to keep scoring fast
	if len(phrases) > 50 {
		phrases = phrases[:50]
	}

	return phrases
}
