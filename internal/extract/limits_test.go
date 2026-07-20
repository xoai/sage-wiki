package extract

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestBoundedReader_UnderCap(t *testing.T) {
	br := &boundedReader{r: strings.NewReader("hello world"), limit: 100}
	data, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read under cap: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("data = %q, want hello world", data)
	}
	if br.exceeded {
		t.Error("exceeded set under cap")
	}
	if br.n != 11 {
		t.Errorf("n = %d, want 11", br.n)
	}
}

func TestBoundedReader_ExactCap(t *testing.T) {
	br := &boundedReader{r: strings.NewReader("12345"), limit: 5}
	data, err := io.ReadAll(br)
	if err != nil {
		t.Fatalf("read at exact cap should succeed: %v", err)
	}
	if string(data) != "12345" || br.exceeded {
		t.Errorf("exact cap: data=%q exceeded=%v, want 12345/false", data, br.exceeded)
	}
}

func TestBoundedReader_Overflow(t *testing.T) {
	br := &boundedReader{r: strings.NewReader("1234567890"), limit: 5}
	_, err := io.ReadAll(br)
	if err == nil {
		t.Fatal("expected error on overflow")
	}
	if !br.exceeded {
		t.Error("exceeded flag not set on overflow")
	}
	// The distinctive error must be identifiable (not a generic EOF).
	if !errors.Is(err, errZipLimitExceeded) {
		t.Errorf("err = %v, want errZipLimitExceeded", err)
	}
}

func TestZipBudget_PreCheck(t *testing.T) {
	t.Cleanup(setTestCaps(1000, 5000))

	b := newZipBudget()
	// Entry under both caps → ok.
	if err := b.preCheck("arc.zip", fakeZipEntry(500)); err != nil {
		t.Errorf("preCheck 500/1000: %v", err)
	}
	// Entry over per-entry cap → entry-too-large.
	err := b.preCheck("arc.zip", fakeZipEntry(2000))
	var zle *ZipLimitError
	if !errors.As(err, &zle) {
		t.Fatalf("preCheck 2000/1000: expected ZipLimitError, got %v", err)
	}
	if zle.Reason != "entry-too-large" || zle.Archive != "arc.zip" || zle.Entry != "e.xml" || zle.Limit != 1000 {
		t.Errorf("ZipLimitError = %+v", zle)
	}
	// Entries individually under per-entry cap but exceeding the aggregate →
	// archive-total-exceeded.
	b = newZipBudget()
	for i := 0; i < 5; i++ {
		if err := b.preCheck("arc.zip", fakeZipEntry(900)); err != nil {
			t.Fatalf("preCheck entry %d: %v", i, err)
		}
		b.charge(900, 900)
	}
	err = b.preCheck("arc.zip", fakeZipEntry(900))
	if !errors.As(err, &zle) || zle.Reason != "archive-total-exceeded" || zle.Limit != 5000 {
		t.Errorf("aggregate: err=%v zle=%+v", err, zle)
	}
}

func TestZipBudget_PreCheck_HostileHugeDeclared(t *testing.T) {
	t.Cleanup(setTestCaps(1000, 5000))

	// A declared size ≥ 2^63 must not wrap negative and bypass layer 1.
	b := newZipBudget()
	err := b.preCheck("arc.zip", fakeZipEntryRaw(1<<63+4000))
	var zle *ZipLimitError
	if !errors.As(err, &zle) {
		t.Errorf("hostile 2^63 declared size bypassed preCheck (int64 wrap): err=%v", err)
	}
}

func TestZipBudget_ChargeTakesMax(t *testing.T) {
	t.Cleanup(setTestCaps(1000, 5000))

	b := newZipBudget()
	b.charge(100, 400) // lying header: declared small, read more
	if b.remaining != 5000-400 {
		t.Errorf("remaining = %d, want %d (charge must take max(declared, read))", b.remaining, 4600)
	}
	b.charge(400, 100) // honest: declared larger (compressed padding)
	if b.remaining != 4600-400 {
		t.Errorf("remaining = %d, want %d", b.remaining, 4200)
	}
}

func TestZipLimitError_Message(t *testing.T) {
	zle := &ZipLimitError{Archive: "bomb.docx", Entry: "word/document.xml", Limit: 50 << 20, Reason: "entry-too-large"}
	msg := zle.Error()
	for _, want := range []string{"bomb.docx", "word/document.xml", "entry-too-large"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}

// setTestCaps lowers the caps for a test and returns a restore func
// (t.Cleanup candidate). Tests using it must NOT call t.Parallel.
func setTestCaps(entry, total int64) (restore func()) {
	oldEntry, oldTotal := maxZipEntryBytes, maxZipTotalBytes
	maxZipEntryBytes, maxZipTotalBytes = entry, total
	return func() { maxZipEntryBytes, maxZipTotalBytes = oldEntry, oldTotal }
}

// fakeZipEntry builds a *zip.File with a given declared size for preCheck
// tests (no real archive needed — only UncompressedSize64 and Name are read).
func fakeZipEntry(declared int64) *zip.File {
	return fakeZipEntryRaw(uint64(declared))
}

func fakeZipEntryRaw(declared uint64) *zip.File {
	return &zip.File{
		FileHeader: zip.FileHeader{Name: "e.xml", UncompressedSize64: declared},
	}
}

// ── T2: extractor wiring tests ──────────────────────────────────────────

var docxXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:t>REPLACE_ME</w:t></w:p></w:body></w:document>`

// buildZip writes entries to a temp zip file and returns its path.
func buildZip(t *testing.T, entries map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fixture.zip")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(out)
	// Deterministic order for aggregate-budget assertions.
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(entries[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	out.Close()
	return path
}

// patchCentralDeclaredSize rewrites one entry's central-directory
// uncompressed-size field (4 bytes at offset 24 from the PK\x01\x02
// signature). The signature is located by a BACKWARD scan from the file
// tail (the central directory lives at the end) — never a forward
// bytes.Index, which can false-match inside compressed entry data. The
// entry filename is verified so a signature-like byte string in another
// entry's data can't be mis-patched.
func patchCentralDeclaredSize(t *testing.T, path, entryName string, newSize uint32) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sig := []byte{'P', 'K', 0x01, 0x02}
	for i := len(data) - len(sig); i >= 0; i-- {
		if !bytes.Equal(data[i:i+len(sig)], sig) {
			continue
		}
		nameLen := int(binary.LittleEndian.Uint16(data[i+28:]))
		if i+46+nameLen > len(data) {
			continue
		}
		if string(data[i+46:i+46+nameLen]) != entryName {
			continue
		}
		binary.LittleEndian.PutUint32(data[i+24:], newSize)
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	t.Fatalf("central directory entry %q not found", entryName)
}

func validDocxEntry(t *testing.T, text string) []byte {
	t.Helper()
	return []byte(strings.Replace(docxXML, "REPLACE_ME", text, 1))
}

// TestExtractDocx_DeclaredBomb: an honestly-declared >50MB matched entry is
// rejected by the layer-1 pre-check WITHOUT decompression — pre-change it
// hits extractXMLText, fails XML decoding, and docx returns the generic
// "no text content" error (a different type, so this test fails pre-change).
func TestExtractDocx_DeclaredBomb(t *testing.T) {
	zeros := make([]byte, 60<<20) // 60 MB, compresses to ~KB
	path := buildZip(t, map[string][]byte{"word/document.xml": zeros})

	_, err := extractDocx(path)
	var zle *ZipLimitError
	if !errors.As(err, &zle) {
		t.Fatalf("expected ZipLimitError, got %v", err)
	}
	if zle.Reason != "entry-too-large" || zle.Entry != "word/document.xml" || zle.Limit != 50<<20 {
		t.Errorf("zle = %+v", zle)
	}
}

// TestExtractXlsx_AggregateBomb: 10 matched sheet entries × 30MB declared
// (each under the per-entry cap) exceed the 200MB aggregate budget.
func TestExtractXlsx_AggregateBomb(t *testing.T) {
	zeros := make([]byte, 30<<20) // one buffer, reused across all 10 entries
	entries := map[string][]byte{}
	for i := 1; i <= 10; i++ {
		entries[fmt.Sprintf("xl/worksheets/sheet%d.xml", i)] = zeros
	}
	path := buildZip(t, entries)

	_, err := extractXlsx(path)
	var zle *ZipLimitError
	if !errors.As(err, &zle) {
		t.Fatalf("expected ZipLimitError, got %v", err)
	}
	if zle.Reason != "archive-total-exceeded" || zle.Limit != 200<<20 {
		t.Errorf("zle = %+v", zle)
	}
}

// TestExtractDocx_LyingHeader_Rejected: the declared size is patched down
// to 1KB while the real content is ~64KB of valid XML. Go's stdlib zip
// bounds decompressed reads to the declared size (checksumReader returns
// ErrFormat past it — D2 stdlib-reality note), so the lying entry can never
// inflate; assert the SAFE rejection: extraction fails and the text is not
// returned. (The boundedReader flag path is unreachable via stdlib reads —
// it is unit-tested in isolation instead.)
func TestExtractDocx_LyingHeader_Rejected(t *testing.T) {
	text := strings.Repeat("lorem ipsum dolor sit amet ", 2400) // ~64 KB
	path := buildZip(t, map[string][]byte{"word/document.xml": validDocxEntry(t, text)})
	patchCentralDeclaredSize(t, path, "word/document.xml", 1024)

	// The SECURITY invariant is "no inflation past the patched 1KB
	// declaration" — not a particular error. Go's stdlib bounds reads to the
	// declared size, so depending on flate's fill boundaries the outcome is
	// either an extraction error OR a ≤1KB truncated result; both are safe.
	// (An err!=nil assertion would be implementation-defined — Gate-3 i1.)
	sc, _ := extractDocx(path)
	if sc != nil && len(sc.Text) > 2048 {
		t.Errorf("inflated content returned (%d bytes) despite lying header", len(sc.Text))
	}
}

// TestExtractDocx_UnmatchedMediaIgnored: a >50MB UNMATCHED part (embedded
// media, never opened) must not trip the caps at either layer; the small
// matched document extracts normally. The companion variant — the same
// media-sized payload in the MATCHED entry — is a whole-file error.
func TestExtractDocx_UnmatchedMediaIgnored(t *testing.T) {
	media := make([]byte, 60<<20)
	small := validDocxEntry(t, "the quick brown fox")

	good := buildZip(t, map[string][]byte{
		"word/document.xml": small,
		"media/image1.png":  media,
	})
	sc, err := extractDocx(good)
	if err != nil {
		t.Fatalf("unmatched media part must not trip caps: %v", err)
	}
	if !strings.Contains(sc.Text, "the quick brown fox") {
		t.Errorf("text = %q", sc.Text)
	}

	bomb := buildZip(t, map[string][]byte{
		"word/document.xml": media, // same payload, but MATCHED
		"media/image1.png":  small,
	})
	_, err = extractDocx(bomb)
	var zle *ZipLimitError
	if !errors.As(err, &zle) {
		t.Fatalf("matched over-cap entry must error: %v", err)
	}
}

// TestExtractors_SmallFixturesRegression: small valid files in all four
// formats extract correctly (all four fixtures are NET-NEW — no pre-existing
// zip-extractor coverage; they pass pre- and post-change and guard against
// the wiring breaking the happy path).
func TestExtractors_SmallFixturesRegression(t *testing.T) {
	t.Run("docx", func(t *testing.T) {
		path := buildZip(t, map[string][]byte{"word/document.xml": validDocxEntry(t, "alpha beta gamma")})
		sc, err := extractDocx(path)
		if err != nil || !strings.Contains(sc.Text, "alpha beta gamma") {
			t.Errorf("docx: sc=%+v err=%v", sc, err)
		}
	})
	t.Run("xlsx", func(t *testing.T) {
		path := buildZip(t, map[string][]byte{
			"xl/sharedStrings.xml":     []byte(`<sst><si><t>shared one</t></si><si><t>shared two</t></si></sst>`),
			"xl/worksheets/sheet1.xml": []byte(`<worksheet><sheetData><row><c><v>cell</v></c></row></sheetData></worksheet>`),
			"xl/worksheets/sheet2.xml": []byte(`<worksheet><sheetData><row><c><v>cell2</v></c></row></sheetData></worksheet>`),
		})
		sc, err := extractXlsx(path)
		if err != nil || !strings.Contains(sc.Text, "shared one") {
			t.Errorf("xlsx: sc=%+v err=%v", sc, err)
		}
	})
	t.Run("pptx", func(t *testing.T) {
		path := buildZip(t, map[string][]byte{
			"ppt/slides/slide1.xml": []byte(`<p:sld xmlns:p="x"><p:cSld><p:spTree><p:sp><p:txBody><a:p xmlns:a="y"><a:r><a:t>slide text</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`),
		})
		sc, err := extractPptx(path)
		if err != nil || !strings.Contains(sc.Text, "slide text") {
			t.Errorf("pptx: sc=%+v err=%v", sc, err)
		}
	})
	t.Run("epub", func(t *testing.T) {
		path := buildZip(t, map[string][]byte{
			"OPS/chapter1.xhtml": []byte(`<html><body><p><t>chapter content here</t></p></body></html>`),
		})
		sc, err := extractEpub(path)
		if err != nil || !strings.Contains(sc.Text, "chapter content here") {
			t.Errorf("epub: sc=%+v err=%v", sc, err)
		}
	})
}
