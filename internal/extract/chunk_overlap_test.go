package extract

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// longDoc builds a multi-paragraph document that reliably splits into
// several chunks at the given token budget.
func longDoc() string {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString("Paragraph ")
		b.WriteString(string(rune('a' + i%26)))
		b.WriteString(" contains a sentence about retrieval quality and chunk boundaries. ")
		b.WriteString("It is long enough that the splitter has real work to do.\n\n")
	}
	return b.String()
}

// Overlap 0 (and absent) must be byte-identical to the historical chunking —
// the config default is 0 precisely so existing indexes never shift (spec §2.5).
func TestChunkTextOverlapZeroIsByteIdentical(t *testing.T) {
	text := longDoc()

	base := ChunkText(text, 100)
	zero := ChunkText(text, 100, 0)

	if len(base) < 2 {
		t.Fatalf("fixture produced %d chunks, want >= 2", len(base))
	}
	if len(zero) != len(base) {
		t.Fatalf("chunk count with overlap 0 = %d, want %d", len(zero), len(base))
	}
	for i := range base {
		if zero[i].Text != base[i].Text {
			t.Errorf("chunk %d differs with explicit overlap 0", i)
		}
		if zero[i].Index != base[i].Index || zero[i].Heading != base[i].Heading {
			t.Errorf("chunk %d metadata differs with explicit overlap 0", i)
		}
	}
}

// With overlap > 0 every chunk after the first carries the tail of its
// predecessor, so a fact straddling a boundary is retrievable from both sides.
func TestChunkTextOverlapPrefixesPredecessorTail(t *testing.T) {
	text := longDoc()

	base := ChunkText(text, 100)
	over := ChunkText(text, 100, 20) // 20 tokens ≈ 80 chars

	if len(over) != len(base) {
		t.Fatalf("overlap changed chunk count: %d, want %d", len(over), len(base))
	}
	if over[0].Text != base[0].Text {
		t.Error("first chunk must be untouched — it has no predecessor")
	}

	for i := 1; i < len(base); i++ {
		if !strings.HasSuffix(over[i].Text, base[i].Text) {
			t.Fatalf("chunk %d does not end with its original text", i)
		}
		prefix := strings.TrimSuffix(over[i].Text, base[i].Text)
		prefix = strings.TrimSuffix(prefix, "\n")
		if prefix == "" {
			t.Errorf("chunk %d gained no overlap prefix", i)
			continue
		}
		if !strings.HasSuffix(base[i-1].Text, prefix) {
			t.Errorf("chunk %d prefix %q is not a tail of chunk %d", i, prefix, i-1)
		}
		// The overlap never starts mid-word.
		if strings.HasPrefix(prefix, " ") || strings.HasPrefix(prefix, "\n") {
			t.Errorf("chunk %d prefix starts with whitespace: %q", i, prefix)
		}
		if len(prefix) > 20*4 {
			t.Errorf("chunk %d prefix is %d chars, want <= 80", i, len(prefix))
		}
	}
}

// A document short enough to stay a single chunk is unaffected by overlap —
// there is no predecessor to borrow from.
func TestChunkTextOverlapSingleChunkUnchanged(t *testing.T) {
	text := "Short enough to stay one chunk."

	single := ChunkText(text, 800, 80)
	if len(single) != 1 {
		t.Fatalf("chunk count = %d, want 1", len(single))
	}
	if single[0].Text != text {
		t.Errorf("single chunk text = %q, want %q", single[0].Text, text)
	}
}

// A negative or zero overlap is treated as "off" rather than as an error —
// callers pass a config value straight through.
func TestChunkTextOverlapNegativeIsOff(t *testing.T) {
	text := longDoc()

	base := ChunkText(text, 100)
	neg := ChunkText(text, 100, -5)

	if len(neg) != len(base) {
		t.Fatalf("chunk count = %d, want %d", len(neg), len(base))
	}
	for i := range base {
		if neg[i].Text != base[i].Text {
			t.Errorf("chunk %d differs with negative overlap", i)
		}
	}
}

// A CJK paragraph has no interior whitespace, so the word-boundary trim has
// nothing to find and a raw byte cut would land mid-rune — invalid UTF-8 in
// the chunk index, and a mangled embedding request (Go's JSON encoder
// substitutes U+FFFD, so the vector stops matching the stored text).
func TestChunkTextOverlapKeepsValidUTF8OnCJK(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 60; i++ {
		b.WriteString("検索品質はチャンク境界の位置に依存し、境界をまたぐ事実は失われやすい。")
	}
	// Two paragraphs, each internally whitespace-free.
	text := b.String() + "\n\n" + b.String()

	chunks := ChunkText(text, 100, 20)
	if len(chunks) < 2 {
		t.Fatalf("fixture produced %d chunks, want >= 2", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c.Text) {
			t.Errorf("chunk %d is not valid UTF-8", i)
		}
	}
	for i := 1; i < len(chunks); i++ {
		prefix, _, found := strings.Cut(chunks[i].Text, "\n")
		if !found || prefix == "" {
			continue // no overlap prefix on this boundary
		}
		if !utf8.ValidString(prefix) {
			t.Errorf("chunk %d overlap prefix is not valid UTF-8", i)
		}
		if !strings.HasSuffix(chunks[i-1].Text, prefix) {
			t.Errorf("chunk %d prefix is not a tail of its predecessor", i)
		}
	}
}
