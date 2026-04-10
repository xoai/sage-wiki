package quality

import (
	"regexp"
	"strings"
)

// ArticleScore holds quality metrics for a compiled wiki article.
type ArticleScore struct {
	ConceptName string

	// Per-dimension scores, 0.0–1.0
	FormatScore      float64 // structural elements: code blocks, tables, subheadings
	GroundingScore   float64 // facts from source appear in article
	CoverageScore    float64 // source code blocks/tables appear in article
	WikilinkScore    float64 // wikilink format compliance (no Chinese)
	AntiPatternScore float64 // 1.0 = clean, lower = more anti-patterns found

	// Weighted composite
	Composite float64

	// Diagnostics
	Issues           []string
	CodeBlockCount   int
	TableRowCount    int
	WikilinkCount    int
	ChineseWikilinks int
	AntiPatterns     []string
}

// Scorer evaluates article quality without calling an LLM.
type Scorer struct {
	FormatWeight      float64
	GroundingWeight   float64
	CoverageWeight    float64
	WikilinkWeight    float64
	AntiPatternWeight float64
}

// DefaultScorer returns a scorer with balanced default weights.
func DefaultScorer() *Scorer {
	return &Scorer{
		FormatWeight:      0.25,
		GroundingWeight:   0.20,
		CoverageWeight:    0.20,
		WikilinkWeight:    0.20,
		AntiPatternWeight: 0.15,
	}
}

var antiPatternPhrases = []string{
	"源文档未提及",
	"未详细说明",
	"源文档中未",
	"文档未提及",
	"没有明确",
	"the system uses",
	"no explicit mention",
}

var (
	codeBlockRe = regexp.MustCompile("(?s)```[^`]*```")
	tableRowRe  = regexp.MustCompile(`(?m)^\|.*\|.*\|`)
	h3Re        = regexp.MustCompile(`(?m)^###\s`)
	h2Re        = regexp.MustCompile(`(?m)^##\s`)
	wikilinkRe  = regexp.MustCompile(`\[\[([^\]]+)\]\]`)
	numberRe    = regexp.MustCompile(`\d+\.?\d*`)
	backtickRe  = regexp.MustCompile("`([^`]+)`")
)

// ScoreArticle evaluates a single article against its source context (the summary text
// that was injected into the write prompt).
func (s *Scorer) ScoreArticle(conceptName, articleContent, sourceContext string) ArticleScore {
	score := ArticleScore{ConceptName: conceptName}

	score.FormatScore = s.scoreFormat(&score, articleContent)
	score.GroundingScore = s.scoreGrounding(articleContent, sourceContext)
	score.CoverageScore = s.scoreCoverage(articleContent, sourceContext)
	score.WikilinkScore = s.scoreWikilinks(&score, articleContent)
	score.AntiPatternScore = s.scoreAntiPatterns(&score, articleContent)

	score.Composite = score.FormatScore*s.FormatWeight +
		score.GroundingScore*s.GroundingWeight +
		score.CoverageScore*s.CoverageWeight +
		score.WikilinkScore*s.WikilinkWeight +
		score.AntiPatternScore*s.AntiPatternWeight

	return score
}

func (s *Scorer) scoreFormat(score *ArticleScore, content string) float64 {
	codeBlocks := len(codeBlockRe.FindAllString(content, -1))
	tableRows := len(tableRowRe.FindAllString(content, -1))
	h3Count := len(h3Re.FindAllString(content, -1))
	hasH2 := h2Re.MatchString(content)

	score.CodeBlockCount = codeBlocks
	score.TableRowCount = tableRows

	val := 0.0
	if hasH2 {
		val += 0.25
	}
	val += clamp(float64(codeBlocks)*0.2, 0, 0.3)
	// Table sections (discount separator rows by halving)
	tableSections := tableRows / 3 // rough: header + separator + at least 1 data row
	val += clamp(float64(tableSections)*0.2, 0, 0.3)
	val += clamp(float64(h3Count)*0.05, 0, 0.15)

	return clamp(val, 0, 1.0)
}

func (s *Scorer) scoreGrounding(article, source string) float64 {
	if source == "" {
		return 0.5 // neutral when no source context
	}

	// Extract numbers from source
	sourceNumbers := uniqueStrings(numberRe.FindAllString(source, -1))
	// Extract backtick content (field names, code refs)
	sourceFields := uniqueStrings(backtickRe.FindAllString(source, -1))

	if len(sourceNumbers) == 0 && len(sourceFields) == 0 {
		return 0.5 // neutral
	}

	numFound := 0
	for _, n := range sourceNumbers {
		if strings.Contains(article, n) {
			numFound++
		}
	}

	fieldFound := 0
	for _, f := range sourceFields {
		if strings.Contains(article, f) {
			fieldFound++
		}
	}

	numScore := 0.0
	if len(sourceNumbers) > 0 {
		numScore = float64(numFound) / float64(len(sourceNumbers))
	}
	fieldScore := 0.0
	if len(sourceFields) > 0 {
		fieldScore = float64(fieldFound) / float64(len(sourceFields))
	}

	if len(sourceNumbers) > 0 && len(sourceFields) > 0 {
		return 0.5*numScore + 0.5*fieldScore
	}
	if len(sourceNumbers) > 0 {
		return numScore
	}
	return fieldScore
}

func (s *Scorer) scoreCoverage(article, source string) float64 {
	if source == "" {
		return 0.5
	}
	sourceCodeBlocks := len(codeBlockRe.FindAllString(source, -1))
	articleCodeBlocks := len(codeBlockRe.FindAllString(article, -1))

	if sourceCodeBlocks == 0 {
		return 0.5 // neutral
	}
	return clamp(float64(articleCodeBlocks)/float64(sourceCodeBlocks), 0, 1.0)
}

func (s *Scorer) scoreWikilinks(score *ArticleScore, content string) float64 {
	links := wikilinkRe.FindAllStringSubmatch(content, -1)
	score.WikilinkCount = len(links)
	if len(links) == 0 {
		return 0.5 // neutral
	}

	chinese := 0
	for _, m := range links {
		if containsCJK(m[1]) {
			chinese++
		}
	}
	score.ChineseWikilinks = chinese
	return 1.0 - float64(chinese)/float64(len(links))
}

func (s *Scorer) scoreAntiPatterns(score *ArticleScore, content string) float64 {
	found := 0
	contentLower := strings.ToLower(content)
	for _, ap := range antiPatternPhrases {
		if strings.Contains(contentLower, strings.ToLower(ap)) {
			found++
			score.AntiPatterns = append(score.AntiPatterns, ap)
		}
	}
	if found == 0 {
		return 1.0
	}
	// Each anti-pattern found reduces score
	return clamp(1.0-float64(found)*0.25, 0, 1.0)
}

// containsCJK returns true if s contains any CJK Unified Ideograph.
func containsCJK(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func uniqueStrings(ss []string) []string {
	seen := make(map[string]bool, len(ss))
	var result []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			result = append(result, s)
		}
	}
	return result
}
