package extract

import (
	"archive/zip"
	"bytes"
	"testing"
)

// Fuzz targets (P2-5, spec §1). Assertions are SECURITY INVARIANTS ONLY:
// no panic, no P1-7 budget breach — never implementation-defined error
// paths (errors are accepted, never asserted against; design D2).
// Empty-but-successful parses are allowed (latent epub <t> issue is
// logged in the cycle decisions, not a fuzz failure).

// headerSlackPerEntry bounds the uncharged per-entry header bytes
// (--- Sheet: <name> --- / --- Chapter N (<name>) --- forms) the writers
// add on top of charged content (spec §1: the bound scales with matched
// entries so max-length member names can't inflate past it).
const headerSlackPerEntry = 64

type extractFn func(path string) (*SourceContent, error)

func fuzzZipExtractor(f *testing.F, fn extractFn, ext string, seeds [][]byte) {
	f.Helper()
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		path := writeFuzzInput(t, data, ext)
		matched := countZipEntries(t, data)
		sc, err := fn(path)
		if err != nil {
			return // errors are not invariant violations
		}
		bound := maxZipTotalBytes + int64(matched)*headerSlackPerEntry
		if int64(len(sc.Text)) > bound {
			t.Fatalf("budget invariant violated: output %d bytes > bound %d (entries %d)",
				len(sc.Text), bound, matched)
		}
	})
}

// countZipEntries returns the number of entries in data if it's a valid
// zip (best-effort; the harness needs it only for the header bound).
func countZipEntries(t *testing.T, data []byte) int {
	t.Helper()
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return 0
	}
	return len(r.File)
}

func FuzzExtractDocx(f *testing.F)  { fuzzZipExtractor(f, extractDocx, ".docx", seedsDocx()) }
func FuzzExtractXlsx(f *testing.F)  { fuzzZipExtractor(f, extractXlsx, ".xlsx", seedsXlsx()) }
func FuzzExtractPptx(f *testing.F)  { fuzzZipExtractor(f, extractPptx, ".pptx", seedsPptx()) }
func FuzzExtractEpub(f *testing.F)  { fuzzZipExtractor(f, extractEpub, ".epub", seedsEpub()) }

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
