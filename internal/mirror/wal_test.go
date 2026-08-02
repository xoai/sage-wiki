package mirror

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildWAL writes a synthetic WAL: 32-byte header + n complete frames +
// optional partial tail.
func buildWAL(t *testing.T, path string, salt1, salt2 uint32, frames int, partialTail int) []byte {
	t.Helper()
	buf := make([]byte, 0, 32+frames*(24+4096)+partialTail)
	hdr := make([]byte, 32)
	binary.BigEndian.PutUint32(hdr[0:4], 0x377f0682)
	binary.BigEndian.PutUint32(hdr[4:8], 3007000)
	binary.BigEndian.PutUint32(hdr[8:12], 4096)
	binary.BigEndian.PutUint32(hdr[16:20], salt1)
	binary.BigEndian.PutUint32(hdr[20:24], salt2)
	buf = append(buf, hdr...)
	for i := 0; i < frames; i++ {
		frame := make([]byte, 24+4096)
		frame[24] = byte(i + 1) // mark page data with frame index
		buf = append(buf, frame...)
	}
	buf = append(buf, make([]byte, partialTail)...)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return buf
}

func TestReadWALHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db-wal")
	buildWAL(t, path, 111, 222, 2, 0)
	hdr, size, err := WALInfoFromFile(path)
	if err != nil {
		t.Fatalf("WALInfoFromFile: %v", err)
	}
	if hdr.Salt1 != 111 || hdr.Salt2 != 222 {
		t.Fatalf("salts = %d/%d", hdr.Salt1, hdr.Salt2)
	}
	if hdr.PageSize != 4096 {
		t.Fatalf("page size = %d", hdr.PageSize)
	}
	if size != 32+2*(24+4096) {
		t.Fatalf("size = %d", size)
	}
	if hdr.SaltID() != uint64(111)<<32|uint64(222) {
		t.Fatalf("SaltID = %x", hdr.SaltID())
	}
}

func TestReadWALHeader_Missing(t *testing.T) {
	_, _, err := WALInfoFromFile(filepath.Join(t.TempDir(), "nope-wal"))
	if err == nil {
		t.Fatal("missing WAL should error")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err should wrap os.ErrNotExist: %v", err)
	}
}

func TestReadWALHeader_BadMagic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-wal")
	os.WriteFile(path, make([]byte, 64), 0o644)
	if _, _, err := WALInfoFromFile(path); err == nil {
		t.Fatal("bad magic should error")
	}
}

func TestSealWALSegment_FromOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db-wal")
	full := buildWAL(t, path, 1, 2, 2, 0)
	offset := int64(32 + 24 + 4096) // after frame 1
	seg, err := SealWALSegment(path, offset)
	if err != nil {
		t.Fatalf("SealWALSegment: %v", err)
	}
	if string(seg) != string(full[offset:]) {
		t.Fatalf("segment = %d bytes, want exact tail %d", len(seg), len(full)-int(offset))
	}
	if seg[24] != 2 {
		t.Fatal("segment should start at frame 2")
	}
}

func TestSealWALSegment_FromZero_IncludesHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db-wal")
	full := buildWAL(t, path, 9, 9, 1, 0)
	seg, err := SealWALSegment(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(seg) != len(full) || string(seg) != string(full) {
		t.Fatal("seal from 0 must include the header")
	}
}

// TestSealWALSegment_DropsPartialTail: a crash mid-frame-write leaves a
// partial frame; only complete frames seal (spec §Data model torn tail).
func TestSealWALSegment_DropsPartialTail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db-wal")
	buildWAL(t, path, 1, 2, 1, 100) // 100 bytes of a torn next frame
	seg, err := SealWALSegment(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := 32 + (24 + 4096)
	if len(seg) != want {
		t.Fatalf("sealed %d bytes, want %d (partial tail dropped)", len(seg), want)
	}
}

func TestSealWALSegment_BadOffset(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db-wal")
	buildWAL(t, path, 1, 2, 1, 0)
	if _, err := SealWALSegment(path, 33); err == nil {
		t.Fatal("non-frame-boundary offset should error")
	}
	if _, err := SealWALSegment(path, 1<<40); err == nil {
		t.Fatal("offset past EOF should error")
	}
}

func TestWALSegmentRoundTrip_Zstd(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db-wal")
	buildWAL(t, path, 5, 6, 2, 0)
	seg, _ := SealWALSegment(path, 0)
	compressed, err := zstdEncode(seg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := zstdDecode(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if string(back) != string(seg) {
		t.Fatal("zstd round-trip mismatch")
	}
	sha := sha256HexBytes(compressed)
	if len(sha) != 64 {
		t.Fatalf("sha = %q", sha)
	}
}

func TestWALSaltChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wiki.db-wal")
	buildWAL(t, path, 1, 2, 1, 0)
	hdr1, _, _ := WALInfoFromFile(path)
	buildWAL(t, path, 3, 4, 1, 0) // rewritten WAL (new salt)
	hdr2, _, _ := WALInfoFromFile(path)
	if hdr1.SaltID() == hdr2.SaltID() {
		t.Fatal("salt change not detected")
	}
}

// TestSealWALSegment_SaltMismatch (F-101 witness): sealing with a pinned
// salt that no longer matches the file errors with ErrSaltMismatch.
func TestSealWALSegment_SaltMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wiki.db-wal")
	buildWAL(t, path, 1, 2, 1, 0)
	if _, err := SealWALSegment(path, 0, WALHeader{Salt1: 9, Salt2: 9}.SaltID()); err == nil {
		t.Fatal("expected ErrSaltMismatch")
	} else {
		var mism *ErrSaltMismatch
		if !errors.As(err, &mism) {
			t.Fatalf("err = %T %v, want ErrSaltMismatch", err, err)
		}
	}
	// Matching salt seals fine.
	hdr, _, _ := WALInfoFromFile(path)
	if _, err := SealWALSegment(path, 0, hdr.SaltID()); err != nil {
		t.Fatalf("matching salt: %v", err)
	}
}

// TestStateValidate_WalSeqStartsAt1 (F-106 witness).
func TestStateValidate_WalSeqStartsAt1(t *testing.T) {
	s := fixtureState()
	s.DB.WAL = []WALSegmentRef{
		{Key: "ws/db/generation-3/wal/000002.zst", SHA256: strings.Repeat("ab", 32), SealedAt: time.Now().UTC()},
	}
	if err := s.Validate(); err == nil {
		t.Fatal("wal[0] seq != 1 must fail validation")
	}
}
