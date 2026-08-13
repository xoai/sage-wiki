package extract

import (
	"archive/zip"
	"bytes"
	"path/filepath"
	"testing"
)

// Fuzz targets (P2-5, spec §1). Assertions are SECURITY INVARIANTS ONLY:
// no panic, no P1-7 budget breach — never implementation-defined error
// paths (errors are accepted, never asserted against; design D2).
// Empty-but-successful parses are allowed (latent epub <t> issue is
// logged in the cycle decisions, not a fuzz failure).

// headerBound computes the EXACT uncharged header bytes the writers can
// add for this zip (spec §1/i1 fix): xlsx `\n--- Sheet: <name> ---\n`
// (17+len(name) per sheet), epub `\n--- Chapter N (<base>) ---\n`
// (~20+digits+len(base) per chapter), pptx sheet analogs, docx none.
// Name-length-aware, so >47-char member names can't slip past a flat
// constant.
func headerBound(data []byte, perEntry func(name string) int) int64 {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0
	}
	var n int64
	for _, f := range r.File {
		n += int64(perEntry(f.Name))
	}
	return n
}

func xlsxHeader(name string) int { return 17 + len(name) }
func epubHeader(name string) int {
	// "\n--- Chapter N (<base>) ---\n" + trailing "\n": 22 fixed + digits
	// (≤ 6 at 1M chapters) + base. 30 covers the realistic ceiling.
	return 30 + len(filepath.Base(name))
}

type extractFn func(path string) (*SourceContent, error)

func fuzzZipExtractor(f *testing.F, fn extractFn, ext string, seeds [][]byte, perEntryHeader func(name string) int) {
	f.Helper()
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeFuzzInput(t, data, ext)
		slack := int64(0)
		if perEntryHeader != nil {
			slack = headerBound(data, perEntryHeader)
		}
		sc, err := fn(path)
		if err != nil {
			return // errors are not invariant violations
		}
		bound := maxZipTotalBytes + slack
		if int64(len(sc.Text)) > bound {
			t.Fatalf("budget invariant violated: output %d bytes > bound %d",
				len(sc.Text), bound)
		}
	})
}

func FuzzExtractDocx(f *testing.F) { fuzzZipExtractor(f, extractDocx, ".docx", seedsDocx(), nil) }
func FuzzExtractXlsx(f *testing.F) {
	fuzzZipExtractor(f, extractXlsx, ".xlsx", seedsXlsx(), xlsxHeader)
}
func FuzzExtractPptx(f *testing.F) {
	fuzzZipExtractor(f, extractPptx, ".pptx", seedsPptx(), xlsxHeader)
}
func FuzzExtractEpub(f *testing.F) {
	fuzzZipExtractor(f, extractEpub, ".epub", seedsEpub(), epubHeader)
}

// FuzzExtractEmail asserts: no panic, and on success the output stays
// within input + 1KB (email.go copies body bytes verbatim and adds only
// the four header label lines — verified, no transcoding; spec §1).
func FuzzExtractEmail(f *testing.F) {
	for _, seed := range seedsEmail() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeFuzzInput(t, data, ".eml")
		sc, err := extractEmail(path)
		if err != nil {
			return
		}
		bound := int64(len(data)) + 1024
		if int64(len(sc.Text)) > bound {
			t.Fatalf("eml invariant violated: output %d > input+slack %d", len(sc.Text), bound)
		}
	})
}

// FuzzExtractPdfGo asserts: no panic, and on success output stays under
// the sanity ceiling (maxZipTotalBytes — pdf has no P1-7 cap; the pure-Go
// path can inflate compressed streams; spec §1). The poppler path is
// deliberately NOT fuzzed (external binary — wrong surface, design D4).
func FuzzExtractPdfGo(f *testing.F) {
	for _, seed := range seedsPdf() {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeFuzzInput(t, data, ".pdf")
		sc, err := extractPDFGo(path)
		if err != nil {
			return
		}
		if int64(len(sc.Text)) > maxZipTotalBytes {
			t.Fatalf("pdf invariant violated: output %d > ceiling %d", len(sc.Text), maxZipTotalBytes)
		}
	})
}
