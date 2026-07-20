package extract

import (
	"archive/zip"
	"errors"
	"io"
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
	restore := setTestCaps(1000, 5000)
	defer restore()

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

func TestZipBudget_ChargeTakesMax(t *testing.T) {
	restore := setTestCaps(1000, 5000)
	defer restore()

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
	return &zip.File{
		FileHeader: zip.FileHeader{Name: "e.xml", UncompressedSize64: uint64(declared)},
	}
}
