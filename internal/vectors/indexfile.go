package vectors

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"unsafe"
)

// On-disk vector index format (SPEC-06): a per-table flat matrix of
// L2-normalized rows in rowid order, served via mmap. The format is a
// derived, regenerable cache of the SQLite vec tables — never a source of
// truth. Layout (all integers little-endian):
//
//	offset 0   magic "SWVI" (4B)
//	offset 4   format_version u32 (=1)
//	offset 8   quantization u8 (0=fp32, 1=int8) | table_kind u8 (0=doc,
//	           1=chunk) | reserved u8 ×2
//	offset 12  dim u32
//	offset 16  count u64
//	offset 24  scale fp32 (int8 only; 1.0 for fp32, 1.0 for count=0, and 1.0
//	           when max|v| over the included rows is 0 — forbids division by
//	           zero and keeps rebuilds byte-identical)
//	offset 28  row_order u8 (=1 rowid) | reserved u8 ×3
//	offset 32  ids: count × { id_len u16, id bytes }
//	           (chunk files only: docids: count × { id_len u16, id bytes })
//	           then ZERO PADDING to a 4-byte boundary — the fp32 matrix is
//	           reinterpreted in place (no decode pass), which requires
//	           4-byte row alignment
//	offset …   matrix: count × dim × (4B fp32 | 1B int8), row-major
const (
	indexMagic    = "SWVI"
	indexVersion  = 1
	headerSize    = 32
	rowOrderRowid = 1
)

// Quantization modes for the matrix section.
const (
	QuantNone = 0 // fp32, exact — bit-identical scores to the memory cache
	QuantInt8 = 1 // int8 with one global scale; recall trade-off documented
)

// Table kinds (header offset 9) — doc files carry one id section, chunk
// files carry ids + docIDs; the kind byte makes the layout unambiguous.
const (
	tableKindDoc   = 0
	tableKindChunk = 1
)

var errCorruptIndex = errors.New("vectors: corrupt index file")

// maxIndexDim caps the row dimension a file may declare — far beyond any
// embedding model (65,536) and small enough that count×dim×4 can never
// overflow int64 within a memory-mappable file.
const maxIndexDim = 1 << 16

type indexHeader struct {
	quant int
	kind  int // tableKindDoc | tableKindChunk
	dim   int
	count uint64
	scale float32
}

func (h indexHeader) encode() []byte {
	b := make([]byte, headerSize)
	copy(b[0:4], indexMagic)
	binary.LittleEndian.PutUint32(b[4:], indexVersion)
	b[8] = byte(h.quant)
	b[9] = byte(h.kind)
	binary.LittleEndian.PutUint32(b[12:], uint32(h.dim))
	binary.LittleEndian.PutUint64(b[16:], h.count)
	binary.LittleEndian.PutUint32(b[24:], math.Float32bits(h.scale))
	b[28] = rowOrderRowid
	return b
}

func parseHeader(b []byte) (indexHeader, error) {
	var h indexHeader
	if len(b) < headerSize {
		return h, fmt.Errorf("%w: header truncated (%d bytes)", errCorruptIndex, len(b))
	}
	if string(b[0:4]) != indexMagic {
		return h, fmt.Errorf("%w: bad magic %q", errCorruptIndex, b[0:4])
	}
	if v := binary.LittleEndian.Uint32(b[4:]); v != indexVersion {
		return h, fmt.Errorf("%w: unsupported version %d", errCorruptIndex, v)
	}
	h.quant = int(b[8])
	if h.quant != QuantNone && h.quant != QuantInt8 {
		return h, fmt.Errorf("%w: unknown quantization %d", errCorruptIndex, h.quant)
	}
	h.kind = int(b[9])
	if h.kind != tableKindDoc && h.kind != tableKindChunk {
		return h, fmt.Errorf("%w: unknown table kind %d", errCorruptIndex, h.kind)
	}
	h.dim = int(binary.LittleEndian.Uint32(b[12:]))
	h.count = binary.LittleEndian.Uint64(b[16:])
	h.scale = math.Float32frombits(binary.LittleEndian.Uint32(b[24:]))
	if b[28] != rowOrderRowid {
		return h, fmt.Errorf("%w: unknown row order %d", errCorruptIndex, b[28])
	}
	return h, nil
}

// parsedIndex is a fully validated view over the mapped file bytes. The
// matrix stays inside b (mmap-backed) — nothing here copies it.
type parsedIndex struct {
	header indexHeader
	ids    []string
	docIDs []string // chunk files only; nil for doc files
	data   []byte   // the mapped file (kept alive for matrix slices)
	matOff int      // offset of the matrix section in data
}

// row returns the dequantized fp32 row i. For fp32 files this decodes the
// stored bytes; for int8 it applies the global scale. Callers scanning all
// rows should use rowInto to avoid per-row allocation.
func (p *parsedIndex) row(i int) []float32 {
	out := make([]float32, p.header.dim)
	p.rowInto(i, out)
	return out
}

// fp32Row reinterprets row i in place — valid only for fp32 files whose
// matrix offset is 4-byte aligned (guaranteed by the writer's zero
// padding). Returns nil for int8 files or any alignment surprise, and the
// caller falls back to the decode path. The returned slice ALIASES the
// mapped file: it is read-only by contract (MAP_PRIVATE) and must never
// escape into a mutation.
func (p *parsedIndex) fp32Row(i int) []float32 {
	if p.header.quant != QuantNone {
		return nil
	}
	off := p.matOff + i*p.header.dim*4
	if off%4 != 0 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&p.data[off])), p.header.dim)
}

// rowInto decodes row i into out (len(out) must equal dim).
func (p *parsedIndex) rowInto(i int, out []float32) {
	dim := p.header.dim
	if p.header.quant == QuantInt8 {
		off := p.matOff + i*dim
		for j := 0; j < dim; j++ {
			out[j] = float32(int8(p.data[off+j])) * p.header.scale / 127
		}
		return
	}
	off := p.matOff + i*dim*4
	for j := 0; j < dim; j++ {
		out[j] = math.Float32frombits(binary.LittleEndian.Uint32(p.data[off+j*4:]))
	}
}

// parseIndex validates the mapped bytes end to end: header, both id
// sections, and the exact matrix extent. ANY inconsistency is
// errCorruptIndex — the caller falls back to the in-memory path.
func parseIndex(b []byte) (*parsedIndex, error) {
	h, err := parseHeader(b)
	if err != nil {
		return nil, err
	}
	// Overflow-safe sanity bounds (F-036): corruption-chosen headers must
	// be rejected BEFORE the ids loop allocates count strings or the
	// extent check multiplies count×dim into wraparound.
	if h.dim > maxIndexDim {
		return nil, fmt.Errorf("%w: dim %d exceeds limit %d", errCorruptIndex, h.dim, maxIndexDim)
	}
	if h.count > uint64(len(b))/3 {
		// Every row costs ≥ 2 id bytes + ≥ 1 matrix byte (dim ≥ 1 when
		// count > 0): a count above len/3 cannot fit any valid layout.
		return nil, fmt.Errorf("%w: count %d impossible for %d-byte file", errCorruptIndex, h.count, len(b))
	}
	p := &parsedIndex{header: h, data: b}
	off := headerSize
	readIDs := func() ([]string, error) {
		ids := make([]string, 0, h.count)
		for i := uint64(0); i < h.count; i++ {
			if off+2 > len(b) {
				return nil, fmt.Errorf("%w: id %d length truncated", errCorruptIndex, i)
			}
			n := int(binary.LittleEndian.Uint16(b[off:]))
			off += 2
			if off+n > len(b) {
				return nil, fmt.Errorf("%w: id %d bytes truncated", errCorruptIndex, i)
			}
			ids = append(ids, string(b[off:off+n]))
			off += n
		}
		return ids, nil
	}
	if p.ids, err = readIDs(); err != nil {
		return nil, err
	}
	if h.kind == tableKindChunk {
		if p.docIDs, err = readIDs(); err != nil {
			return nil, err
		}
	}
	// Skip the 4-byte alignment padding the writer emitted.
	if pad := (4 - off%4) % 4; pad > 0 {
		for i := 0; i < pad; i++ {
			if off+i >= len(b) || b[off+i] != 0 {
				return nil, fmt.Errorf("%w: non-zero alignment padding", errCorruptIndex)
			}
		}
		off += pad
	}
	matBytes := uint64(h.count) * uint64(h.dim) * uint64(h.quantRowBytes())
	if uint64(off)+matBytes != uint64(len(b)) {
		return nil, fmt.Errorf("%w: matrix extent mismatch (want %d bytes at %d, file %d)",
			errCorruptIndex, matBytes, off, len(b))
	}
	p.matOff = off
	return p, nil
}

// quantRowBytes returns bytes per matrix element for the quantization mode.
func (h indexHeader) quantRowBytes() int {
	if h.quant == QuantInt8 {
		return 1
	}
	return 4
}
