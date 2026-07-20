package extract

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
)

// Zip-bomb resource limits (SEC-08, P1-7). All four zip extractors stream
// decompressed entry content into memory; without bounds a crafted archive
// inflates unboundedly (OOM). TWO caps are required — a per-entry cap is not
// enough, because OOXML/EPUB containers have arbitrarily many parts (the
// Athena kbpkg lesson): a bomb of many small highly-compressible entries
// sails under any per-entry cap.
//
// Both caps apply ONLY to entries an extractor's name-filter matches and
// will actually open — never to unopened entries (an embedded 100 MB video
// is never decompressed, so it is zero risk and must not trip the caps).
//
// These are package-level vars (not consts) ONLY so tests can lower them to
// exercise the streaming-overflow path at KB scale — Go's zip.Writer always
// writes honest declared sizes, so the lying-header path cannot be reached
// at default caps without a >50 MB fixture. Config-tunable caps are a
// documented non-goal (05 §P1-7).
var (
	maxZipEntryBytes int64 = 50 << 20  // 50 MB per decompressed entry
	maxZipTotalBytes int64 = 200 << 20 // 200 MB aggregate per archive;
	// peak RSS is ~2–2.5× this (strings.Builder growth + per-entry string +
	// token churn) — size host limits accordingly.
)

// errZipLimitExceeded is the distinctive error boundedReader returns from
// Read on overflow — identifiable via errors.Is even after a decoder
// swallows the read error.
var errZipLimitExceeded = errors.New("zip entry exceeds resource limit")

// ZipLimitError describes a rejected zip bomb: which archive, which entry,
// which cap was breached, and why.
type ZipLimitError struct {
	Archive string
	Entry   string
	Limit   int64  // the BREACHED cap (per-entry or aggregate)
	Reason  string // "entry-too-large" | "archive-total-exceeded"
}

func (e *ZipLimitError) Error() string {
	return fmt.Sprintf("zip resource limit: %s: entry %s: %s (limit %d bytes)",
		e.Archive, e.Entry, e.Reason, e.Limit)
}

// zipBudget tracks the remaining aggregate allowance for ONE extraction.
// Fresh per call, never shared; for xlsx one budget spans both the
// sharedStrings and sheets loops.
type zipBudget struct {
	remaining int64
}

func newZipBudget() *zipBudget {
	return &zipBudget{remaining: maxZipTotalBytes}
}

// preCheck is the layer-1 fast reject: an honest declared size over either
// cap fails before a single byte is decompressed. archive/zip populates
// UncompressedSize64 from the central directory, so it is reliable even for
// data-descriptor entries; a lying (small) declared size falls through to
// layer 2 by design — do NOT special-case declared == 0. Comparisons stay
// in uint64: a hostile declared size ≥ 2^63 would wrap negative in int64
// and silently bypass both checks.
func (b *zipBudget) preCheck(archive string, f *zip.File) error {
	declared := f.UncompressedSize64
	if declared > uint64(maxZipEntryBytes) {
		return &ZipLimitError{Archive: archive, Entry: f.Name, Limit: maxZipEntryBytes, Reason: "entry-too-large"}
	}
	if declared > uint64(b.remaining) {
		return &ZipLimitError{Archive: archive, Entry: f.Name, Limit: maxZipTotalBytes, Reason: "archive-total-exceeded"}
	}
	return nil
}

// charge debits an entry from the aggregate budget after reading. The debit
// is max(declared, bytesRead) so a lying header (declared small, read more)
// still burns the real cost.
func (b *zipBudget) charge(declared, bytesRead int64) {
	if bytesRead > declared {
		declared = bytesRead
	}
	b.remaining -= declared
}

// entryLimit returns the streaming cap for one entry: the smaller of the
// per-entry cap and the remaining aggregate budget.
func (b *zipBudget) entryLimit() int64 {
	if b.remaining < maxZipEntryBytes {
		return b.remaining
	}
	return maxZipEntryBytes
}

// open runs the layer-1 pre-check on a MATCHED entry and, if it passes,
// opens it wrapped in a layer-2 bounded reader. A pre-check failure returns
// the *ZipLimitError directly; an f.Open failure returns the raw error so
// callers can keep their existing open-error behavior (continue in
// xlsx/pptx/epub, wrapped return in docx). After reading, callers MUST
// check br.exceeded (return b.limitError on true) and then b.charge.
func (b *zipBudget) open(archive string, f *zip.File) (io.ReadCloser, *boundedReader, error) {
	if err := b.preCheck(archive, f); err != nil {
		return nil, nil, err
	}
	rc, err := f.Open()
	if err != nil {
		return nil, nil, err
	}
	return rc, &boundedReader{r: rc, limit: b.entryLimit()}, nil
}

// limitError builds the ZipLimitError for a streaming (layer-2) overflow,
// choosing the breached cap by which bound was tighter at the time.
func (b *zipBudget) limitError(archive, entry string) *ZipLimitError {
	if b.remaining < maxZipEntryBytes {
		return &ZipLimitError{Archive: archive, Entry: entry, Limit: maxZipTotalBytes, Reason: "archive-total-exceeded"}
	}
	return &ZipLimitError{Archive: archive, Entry: entry, Limit: maxZipEntryBytes, Reason: "entry-too-large"}
}

// boundedReader enforces layer 2: a hard streaming cap on ONE entry's
// decompressed bytes. On overflow it sets exceeded and returns
// errZipLimitExceeded from Read — a hard error, never a silent truncation
// (truncation would mask the bomb as a legitimately short entry). The
// exceeded flag is the ONLY sound overflow signal for callers: a decoder
// (extractXMLText) breaks its loop on ANY read error, and a byte total
// cannot distinguish an entry read to exactly its bound from a capped one.
type boundedReader struct {
	r        io.Reader
	limit    int64
	n        int64
	exceeded bool
}

func (b *boundedReader) Read(p []byte) (int, error) {
	if b.exceeded {
		return 0, errZipLimitExceeded
	}
	n, err := b.r.Read(p)
	b.n += int64(n)
	if b.n > b.limit {
		b.exceeded = true
		return 0, errZipLimitExceeded
	}
	return n, err
}
