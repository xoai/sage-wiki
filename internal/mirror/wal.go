package mirror

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// WAL header layout (SQLite): 32 bytes — magic(4), version(4), page size(4),
// checkpoint seq(4), salt1(4), salt2(4), checksum1(4), checksum2(4).
const (
	walHeaderSize = 32
	walFrameHdr   = 24
	walMagicLE    = 0x377f0682 // big-endian read of the LE magic variant
	walMagicBE    = 0x377f0683
)

// WALHeader is the parsed 32-byte WAL header (salts identify the WAL
// incarnation — a checkpoint-restart or close-delete changes them).
type WALHeader struct {
	Salt1    uint32
	Salt2    uint32
	PageSize uint32
}

// SaltID combines the salts into one comparable value for LocalState.WALSalt.
func (h WALHeader) SaltID() uint64 {
	return uint64(h.Salt1)<<32 | uint64(h.Salt2)
}

// ErrTornWALHeader marks a WAL shorter than its 32-byte header (crash
// mid-creation). SQLite recovery ignores such a WAL — the shipper treats
// it as absent (the fold rules own what happens next).
type ErrTornWALHeader struct {
	Path string
	Size int64
}

func (e *ErrTornWALHeader) Error() string {
	return fmt.Sprintf("wal: torn header (%d bytes < 32) in %s", e.Size, e.Path)
}

// WALInfoFromFile reads the WAL header and file size. A missing file
// returns an error wrapping os.ErrNotExist (callers distinguish fold cases).
func WALInfoFromFile(path string) (WALHeader, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return WALHeader{}, 0, fmt.Errorf("wal: open: %w", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return WALHeader{}, 0, fmt.Errorf("wal: stat: %w", err)
	}
	if info.Size() < walHeaderSize {
		return WALHeader{}, info.Size(), &ErrTornWALHeader{Path: path, Size: info.Size()}
	}
	hdr := make([]byte, walHeaderSize)
	if _, err := io.ReadFull(f, hdr); err != nil {
		return WALHeader{}, 0, fmt.Errorf("wal: read header: %w", err)
	}
	magic := binary.BigEndian.Uint32(hdr[0:4])
	if magic != walMagicLE && magic != walMagicBE {
		return WALHeader{}, 0, fmt.Errorf("wal: bad magic %#x in %s", magic, path)
	}
	return WALHeader{
		Salt1:    binary.BigEndian.Uint32(hdr[16:20]),
		Salt2:    binary.BigEndian.Uint32(hdr[20:24]),
		PageSize: binary.BigEndian.Uint32(hdr[8:12]),
	}, info.Size(), nil
}

// ErrSaltMismatch reports a WAL incarnation change between the pass's
// entry read and the seal read (TOCTOU, F-101): a cross-process reset
// mid-pass must never splice new-incarnation frames into the old chain.
type ErrSaltMismatch struct {
	Want, Got uint64
}

func (e *ErrSaltMismatch) Error() string {
	return fmt.Sprintf("wal: salt mismatch: pass validated %x, file now %x", e.Want, e.Got)
}

// SealWALSegment returns the WAL bytes [fromOffset, end-of-last-complete-
// frame). fromOffset 0 includes the 32-byte header (generation start);
// otherwise it must be a frame boundary. A torn trailing frame (crash
// mid-write) is never sealed. expectedSalt != 0 pins the incarnation the
// caller validated at pass entry (F-101).
func SealWALSegment(path string, fromOffset int64, expectedSalt ...uint64) ([]byte, error) {
	hdr, size, err := WALInfoFromFile(path)
	if err != nil {
		return nil, err
	}
	if len(expectedSalt) > 0 && expectedSalt[0] != 0 && hdr.SaltID() != expectedSalt[0] {
		return nil, &ErrSaltMismatch{Want: expectedSalt[0], Got: hdr.SaltID()}
	}
	frameSize := int64(walFrameHdr + hdr.PageSize)
	if fromOffset < 0 || (fromOffset != 0 && (fromOffset-walHeaderSize)%frameSize != 0) {
		return nil, fmt.Errorf("wal: offset %d is not a frame boundary (page size %d)", fromOffset, hdr.PageSize)
	}
	if fromOffset > size {
		return nil, fmt.Errorf("wal: offset %d past EOF %d", fromOffset, size)
	}
	complete := size
	if rem := (size - walHeaderSize) % frameSize; rem != 0 {
		complete = size - rem // drop torn tail
	}
	if complete < fromOffset {
		complete = fromOffset // nothing new to seal (only a torn tail grew)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("wal: open: %w", err)
	}
	defer f.Close()
	if _, err := f.Seek(fromOffset, io.SeekStart); err != nil {
		return nil, fmt.Errorf("wal: seek: %w", err)
	}
	seg := make([]byte, complete-fromOffset)
	if _, err := io.ReadFull(f, seg); err != nil {
		return nil, fmt.Errorf("wal: read segment: %w", err)
	}
	return seg, nil
}
