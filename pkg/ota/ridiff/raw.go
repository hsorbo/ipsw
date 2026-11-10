package ridiff

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
)

const RIDIFF10Magic = 0x3031464649444952
const chunkSize = 65536

type ridiff10 struct {
	Magic          uint64
	Variants       uint16
	Flags          uint64
	ControlCount   uint64
	ExcessSpace    uint32
	MetaDataOffset uint64
	ControlsOffset uint64
}

type RIDIFF10 struct {
	ridiff10
	PatchDataOffset []uint64
	PatchSize       uint64
	MetaData        []byte
	Controls        []Control
	PatchData       [][]byte
	Image           *MetaData
	streamSize      uint64
}

// Control describes one full-replacement control. Negative records preserve
// their sign-and-magnitude word in Size; LiteralLength removes the sign bit.
type Control struct{ Offset, Size uint64 }

func (c Control) LiteralLength() uint64 { return c.Size & (math.MaxUint64 >> 1) }

type metadata struct {
	Digest       [32]byte
	TotalBytes   uint64
	UnknownCount uint64
	ExtentCount  uint64
	ForkCount    uint64
}
type MetaData struct {
	metadata
	Extents []Extent
	Forks   []Fork
}
type Extent struct{ Offset, Size uint64 }
type Fork struct {
	Size        uint64
	Compressed  uint64
	Flags       uint64
	ExtentIndex uint64
	ExtentCount uint64
	Algorithm   uint8
	Chunks      []ForkChunk
}
type ForkChunk struct {
	Offset uint64
	Length uint32
}

// ParseRawImageDiff10 is the in-memory convenience parser. Applying large
// patches uses ReaderAt and streams the data section instead.
func ParseRawImageDiff10(r *bytes.Reader) (*RIDIFF10, error) {
	rid, err := parsePatch(r, r.Size())
	if err != nil {
		return nil, err
	}
	s, err := newSectionReader(io.NewSectionReader(r, int64(rid.PatchDataOffset[0]), int64(rid.PatchSize-rid.PatchDataOffset[0])))
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(s)
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != rid.streamSize {
		return nil, fmt.Errorf("patch stream size mismatch")
	}
	rid.PatchData = [][]byte{data}
	return rid, nil
}

func parsePatch(r io.ReaderAt, size int64) (*RIDIFF10, error) {
	sr := io.NewSectionReader(r, 0, size)
	rid := new(RIDIFF10)
	if err := binary.Read(sr, binary.LittleEndian, &rid.ridiff10); err != nil {
		return nil, err
	}
	if rid.Magic != RIDIFF10Magic {
		return nil, fmt.Errorf("invalid RIDIFF10 magic")
	}
	if rid.Variants != 1 {
		return nil, fmt.Errorf("unsupported RIDIFF10 variant count %d (want 1)", rid.Variants)
	}
	rid.PatchDataOffset = make([]uint64, 1)
	if err := binary.Read(sr, binary.LittleEndian, rid.PatchDataOffset); err != nil {
		return nil, err
	}
	if err := binary.Read(sr, binary.LittleEndian, &rid.PatchSize); err != nil {
		return nil, err
	}
	if rid.MetaDataOffset < 62 || rid.ControlsOffset < rid.MetaDataOffset || rid.PatchDataOffset[0] < rid.ControlsOffset || rid.PatchSize < rid.PatchDataOffset[0] || rid.PatchSize != uint64(size) {
		return nil, fmt.Errorf("invalid RIDIFF10 section offsets or patch size")
	}
	section := func(start, end uint64) io.Reader { return io.NewSectionReader(r, int64(start), int64(end-start)) }
	var err error
	rid.MetaData, err = decodeSmallSection(section(rid.MetaDataOffset, rid.ControlsOffset))
	if err != nil {
		return nil, fmt.Errorf("metadata: %w", err)
	}
	rid.Image, rid.streamSize, err = parseMetadata(rid.MetaData)
	if err != nil {
		return nil, err
	}
	controls, err := newSectionReader(section(rid.ControlsOffset, rid.PatchDataOffset[0]))
	if err != nil {
		return nil, fmt.Errorf("controls: %w", err)
	}
	// A control consumes 16 bytes. Bound retained controls as well as metadata.
	if rid.ControlCount > (256<<20)/16 {
		return nil, fmt.Errorf("too many controls")
	}
	var total uint64
	for range rid.ControlCount {
		var c Control
		if err = binary.Read(controls, binary.LittleEndian, &c); err != nil {
			return nil, fmt.Errorf("control: %w", err)
		}
		if c.Offset != 0 {
			return nil, fmt.Errorf("delta controls require a base image and are unsupported")
		}
		if c.LiteralLength() > rid.streamSize-total {
			return nil, fmt.Errorf("control literal lengths exceed stream size")
		}
		total += c.LiteralLength()
		rid.Controls = append(rid.Controls, c)
	}
	var extra [1]byte
	if n, err := controls.Read(extra[:]); n != 0 || err != io.EOF {
		return nil, fmt.Errorf("trailing or invalid controls: %v", err)
	}
	if total != rid.streamSize {
		return nil, fmt.Errorf("control literal lengths do not match stream size")
	}
	return rid, nil
}

func parseMetadata(data []byte) (*MetaData, uint64, error) {
	r := bytes.NewReader(data)
	md := new(MetaData)
	fail := func(s string) (*MetaData, uint64, error) {
		return nil, 0, fmt.Errorf("invalid RIDIFF10 metadata: %s", s)
	}
	if err := binary.Read(r, binary.LittleEndian, &md.metadata); err != nil {
		return nil, 0, err
	}
	if md.TotalBytes > math.MaxInt64 || md.ExtentCount > uint64(r.Len()/16) {
		return fail("image size or extent count")
	}
	md.Extents = make([]Extent, int(md.ExtentCount))
	if err := binary.Read(r, binary.LittleEndian, md.Extents); err != nil {
		return nil, 0, err
	}
	var occupied uint64
	for _, e := range md.Extents {
		if e.Offset%4096 != 0 || e.Size == 0 || e.Size%4096 != 0 || e.Offset > md.TotalBytes || e.Size > md.TotalBytes-e.Offset {
			return fail("extent outside image or unaligned")
		}
		if e.Size > md.TotalBytes-occupied {
			return fail("extent sizes exceed image")
		}
		occupied += e.Size
	}
	sorted := append([]Extent(nil), md.Extents...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Offset < sorted[i-1].Offset+sorted[i-1].Size {
			return fail("overlapping extents")
		}
	}
	if md.ForkCount > uint64(r.Len()/41) {
		return fail("fork count")
	}
	owners := make([]bool, len(md.Extents))
	stream := md.TotalBytes - occupied
	for range md.ForkCount {
		var f Fork
		// Fork records are packed: exactly five uint64s and one byte.
		fields := []any{&f.Size, &f.Compressed, &f.Flags, &f.ExtentIndex, &f.ExtentCount, &f.Algorithm}
		for _, v := range fields {
			if err := binary.Read(r, binary.LittleEndian, v); err != nil {
				return nil, 0, err
			}
		}
		if f.Flags != 1 {
			return fail(fmt.Sprintf("unsupported fork flags %#x", f.Flags))
		}
		if f.Algorithm != 14 {
			return fail(fmt.Sprintf("unsupported decmpfs type %d", f.Algorithm))
		}
		if f.Size == 0 || f.Compressed > math.MaxUint32 || f.ExtentIndex > md.ExtentCount || f.ExtentCount == 0 || f.ExtentCount > md.ExtentCount-f.ExtentIndex {
			return fail("fork size or extents")
		}
		n := (f.Size-1)/chunkSize + 1
		if n > uint64(r.Len()/12) || (n+1)*4 > f.Compressed {
			return fail("chunk count")
		}
		var space uint64
		for i := f.ExtentIndex; i < f.ExtentIndex+f.ExtentCount; i++ {
			if owners[i] {
				return fail("extent belongs to multiple forks")
			}
			owners[i] = true
			space += md.Extents[i].Size
		}
		if space < f.Compressed {
			return fail("fork exceeds its extents")
		}
		f.Chunks = make([]ForkChunk, int(n))
		prev := (n + 1) * 4
		for i := range f.Chunks {
			c := &f.Chunks[i]
			if err := binary.Read(r, binary.LittleEndian, c); err != nil {
				return nil, 0, err
			}
			if c.Offset < prev || c.Offset > f.Compressed || c.Length == 0 || uint64(c.Length) > f.Compressed-c.Offset {
				return fail("chunk table out of order or outside fork")
			}
			prev = c.Offset + uint64(c.Length)
		}
		if f.Size > math.MaxInt64-stream {
			return fail("stream size overflow")
		}
		stream += f.Size
		md.Forks = append(md.Forks, f)
	}
	for _, owned := range owners {
		if !owned {
			return fail("unowned extent")
		}
	}
	if r.Len() != 0 {
		return fail("trailing bytes")
	}
	return md, stream, nil
}
