// Package pathological synthesizes the SPEC-08 D8 pathological corpus at test
// time (a committed generator, not committed files — AC4). The corpus is
// logically ~1 GiB: one oversized document that exceeds max_doc_bytes, enough
// documents to exceed max_compile_batch, deep/long names, and binary junk.
//
// The oversized document is synthesized as a lazy stream (OversizedReader),
// NOT written to disk: the property under test is precisely that the engine's
// streaming size gate (io.LimitReader at max_doc_bytes+1) bounds memory in the
// face of an arbitrarily large input, so materializing 1 GiB on disk would add
// cost without adding signal. Without the gate, capturing it would allocate
// the full 1 GiB and blow the AC4 RSS ceiling.
package pathological

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	// DefaultOversizedBytes is the logical size of the streaming oversized
	// document (~1 GiB) — comfortably above the default max_doc_bytes (10 MiB).
	DefaultOversizedBytes int64 = 1 << 30

	// DefaultBatchDocs exceeds the default max_compile_batch (1000) so the
	// compile leg trips the batch ceiling.
	DefaultBatchDocs = 1001
)

// Corpus is a generated pathological corpus.
type Corpus struct {
	// Dir holds the on-disk documents (small, deep, long-named).
	Dir string
	// Docs are the on-disk document paths, captured via Source.Path.
	Docs []string
	// OversizedBytes is the logical size of the streaming oversized document.
	OversizedBytes int64
}

// Generate writes the on-disk portion of the corpus into dir using the
// default sizes (DefaultBatchDocs, DefaultOversizedBytes).
func Generate(dir string) (*Corpus, error) {
	return GenerateWith(dir, DefaultBatchDocs, DefaultOversizedBytes)
}

// GenerateWith writes batchDocs small markdown documents into dir, plus a
// deeply nested document and a long-named document, and records
// oversizedBytes as the logical size of the streaming oversized document.
func GenerateWith(dir string, batchDocs int, oversizedBytes int64) (*Corpus, error) {
	c := &Corpus{Dir: dir, OversizedBytes: oversizedBytes}

	for i := 0; i < batchDocs; i++ {
		name := fmt.Sprintf("doc-%04d.md", i)
		path := filepath.Join(dir, name)
		body := fmt.Sprintf("# Doc %d\n\nPathological corpus document %d: filler content for the batch ceiling.\n", i, i)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return nil, fmt.Errorf("pathological: write %s: %w", name, err)
		}
		c.Docs = append(c.Docs, path)
	}

	// Deep nesting: ten directory levels.
	deepDir := filepath.Join(dir, "a", "b", "c", "d", "e", "f", "g", "h", "i", "j")
	if err := os.MkdirAll(deepDir, 0o755); err != nil {
		return nil, fmt.Errorf("pathological: mkdir deep: %w", err)
	}
	deepPath := filepath.Join(deepDir, "deep-doc.md")
	if err := os.WriteFile(deepPath, []byte("# Deep\n\nDeeply nested corpus document.\n"), 0o644); err != nil {
		return nil, fmt.Errorf("pathological: write deep: %w", err)
	}
	c.Docs = append(c.Docs, deepPath)

	// Long name: ~180 bytes, under the 255-byte per-segment ceiling
	// (pathsafe.MaxNameLen) so it is accepted, not rejected.
	longName := strings.Repeat("longname-", 20) + ".md"
	longPath := filepath.Join(dir, longName)
	if err := os.WriteFile(longPath, []byte("# Long name\n\nLong-named corpus document.\n"), 0o644); err != nil {
		return nil, fmt.Errorf("pathological: write long: %w", err)
	}
	c.Docs = append(c.Docs, longPath)

	return c, nil
}

// OversizedReader returns a reader that yields OversizedBytes of content.
// It is lazy: bytes are produced only as read, so pairing it with the
// engine's max_doc_bytes+1 LimitReader keeps memory bounded.
func (c *Corpus) OversizedReader() io.Reader {
	return io.LimitReader(repeatReader{b: 'A'}, c.OversizedBytes)
}

// BinaryJunk returns content that is not valid UTF-8 and carries no
// recognized BOM, so it trips the encoding gate's invalid-UTF-8 branch.
func BinaryJunk() []byte {
	return []byte{0x41, 0x80, 0x81, 0xff, 0x00, 0x42}
}

// repeatReader is an infinite reader that fills every byte with b. It never
// returns EOF on its own; wrap it with io.LimitReader to bound it.
type repeatReader struct{ b byte }

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.b
	}
	return len(p), nil
}
