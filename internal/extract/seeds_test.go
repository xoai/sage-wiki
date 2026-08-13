package extract

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Seed builders (spec §2): programmatic minimal valid inputs that REACH
// the success path (extractXMLText captures only <t>-local-name
// elements; the pdf seed carries a resolvable Type1 font).

func buildSeedZip(members map[string][]byte) []byte {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range members {
		f, err := w.Create(name)
		if err != nil {
			panic(err)
		}
		f.Write(content)
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func seedsDocx() [][]byte {
	return [][]byte{buildSeedZip(map[string][]byte{
		"word/document.xml": []byte(`<?xml version="1.0"?><w:document><w:body><w:p><w:r><w:t>hello world</w:t></w:r></w:p></w:body></w:document>`),
	})}
}

func seedsXlsx() [][]byte {
	return [][]byte{buildSeedZip(map[string][]byte{
		"xl/sharedStrings.xml":     []byte(`<sst><si><t>shared text</t></si></sst>`),
		"xl/worksheets/sheet1.xml": []byte(`<sheetData><row><c><v>42</v></c><c><t>cell text</t></c></row></sheetData>`),
	})}
}

func seedsPptx() [][]byte {
	return [][]byte{buildSeedZip(map[string][]byte{
		"ppt/slides/slide1.xml": []byte(`<p:sld><p:cSld><p:spTree><p:txBody><a:p><a:r><a:t>slide text</a:t></a:r></a:p></p:txBody></p:spTree></p:cSld></p:sld>`),
	})}
}

func seedsEpub() [][]byte {
	return [][]byte{buildSeedZip(map[string][]byte{
		"content.xhtml": []byte(`<html><body><p><t>chapter content</t></p></body></html>`),
	})}
}

func seedsEmail() [][]byte {
	return [][]byte{[]byte("From: a@b.c\nTo: d@e.f\nSubject: test\nDate: Mon, 1 Jan 2024 00:00:00 +0000\n\nbody text here")}
}

// seedsPdf builds a minimal %PDF-1.4 with catalog, one page, a content
// stream with a text op, and a resolvable Type1 font (spec §2 — without
// the font the go parser yields nothing and the invariant never fires).
func seedsPdf() [][]byte {
	objects := []string{
		"1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n",
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n",
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>\nendobj\n",
		"4 0 obj\n<< /Length 44 >>\nstream\nBT /F1 12 Tf 72 720 Td (hello world) Tj ET\nendstream\nendobj\n",
		"5 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>\nendobj\n",
	}
	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = buf.Len()
		buf.WriteString(obj)
	}
	xrefStart := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xrefStart)
	return [][]byte{buf.Bytes()}
}

// writeFuzzInput writes data to a per-case temp file (path-based parsers,
// design D1).
func writeFuzzInput(t *testing.T, data []byte, ext string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fuzz"+ext)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}
