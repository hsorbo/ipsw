//go:build cgo

package ridiff

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blacktop/ipsw/internal/lzbitmap"
	"github.com/ulikunitz/xz"
)

func put(b *bytes.Buffer, v any) {
	if err := binary.Write(b, binary.LittleEndian, v); err != nil {
		panic(err)
	}
}
func pbzxFixture(data []byte, compressed bool) []byte {
	var b bytes.Buffer
	b.WriteString("pbzx")
	binary.Write(&b, binary.BigEndian, uint64(blockSize))
	for len(data) > 0 {
		n := min(len(data), blockSize)
		p := data[:n]
		data = data[n:]
		packed := p
		if compressed {
			var z bytes.Buffer
			w, _ := xz.NewWriter(&z)
			w.Write(p)
			w.Close()
			if z.Len() < len(p) {
				packed = z.Bytes()
			}
		}
		binary.Write(&b, binary.BigEndian, uint64(n))
		binary.Write(&b, binary.BigEndian, uint64(len(packed)))
		b.Write(packed)
	}
	return b.Bytes()
}

// fixtureDigest independently applies the specified piece boundaries, resetting
// for each fork and for the combined gaps (not for individual gap extents).
func fixtureDigest(parts ...[]byte) [32]byte {
	var hashes []byte
	for _, p := range parts {
		for len(p) > 0 {
			n := min(len(p), 8<<20)
			h := sha256.Sum256(p[:n])
			hashes = append(hashes, h[:]...)
			p = p[n:]
		}
	}
	return sha256.Sum256(hashes)
}

func patchFixture(md *MetaData, parts [][]byte, compressed bool) []byte {
	md.Digest = fixtureDigest(parts...)
	var m bytes.Buffer
	put(&m, md.metadata)
	put(&m, md.Extents)
	for _, f := range md.Forks {
		for _, v := range []any{f.Size, f.Compressed, f.Flags, f.ExtentIndex, f.ExtentCount, f.Algorithm} {
			put(&m, v)
		}
		put(&m, f.Chunks)
	}
	stream := bytes.Join(parts, nil)
	var c bytes.Buffer
	put(&c, uint64(0))
	put(&c, uint64(len(stream)))
	metadata, controls, data := pbzxFixture(m.Bytes(), compressed), pbzxFixture(c.Bytes(), false), pbzxFixture(stream, compressed)
	var b bytes.Buffer
	put(&b, ridiff10{Magic: RIDIFF10Magic, Variants: 1, ControlCount: 1, MetaDataOffset: 62, ControlsOffset: uint64(62 + len(metadata))})
	put(&b, uint64(62+len(metadata)+len(controls)))
	put(&b, uint64(62+len(metadata)+len(controls)+len(data)))
	b.Write(metadata)
	b.Write(controls)
	b.Write(data)
	return b.Bytes()
}

func simpleFixture(raw []byte, lengths []uint32, extents []Extent, compressed uint64) (*MetaData, [][]byte) {
	f := Fork{Size: uint64(len(raw)), Compressed: compressed, Flags: 1, ExtentCount: uint64(len(extents)), Algorithm: 14}
	off := uint64(4 * (len(lengths) + 1))
	for _, n := range lengths {
		f.Chunks = append(f.Chunks, ForkChunk{off, n})
		off += uint64(n)
	}
	imageSize := uint64(4096)
	occupied := uint64(0)
	for _, e := range extents {
		imageSize = max(imageSize, e.Offset+e.Size+4096)
		occupied += e.Size
	}
	gaps := make([]byte, int(imageSize-occupied))
	for i := range gaps {
		gaps[i] = byte(i*37 + 11)
	}
	return &MetaData{metadata: metadata{TotalBytes: imageSize, ExtentCount: uint64(len(extents)), ForkCount: 1}, Extents: extents, Forks: []Fork{f}}, [][]byte{raw, gaps}
}

func TestApplyLayout(t *testing.T) {
	random := make([]byte, chunkSize)
	rand.New(rand.NewSource(1)).Read(random)
	repeated := bytes.Repeat([]byte("abcdef01"), chunkSize/8)
	shifted := append(append([]byte(nil), repeated...), bytes.Repeat([]byte("abcdefgh"), 200)...)
	firstEnc := lzbitmap.Encode(repeated, chunkSize)
	cases := []struct {
		name       string
		raw        []byte
		lengths    []uint32
		extents    []Extent
		compressed uint64
		starts     []uint32
		stored     []bool
	}{
		{"padding", repeated, []uint32{2048}, []Extent{{4096, 4096}}, 2056, []uint32{8, 2056}, []bool{false}},
		{"stored despite compression", repeated, []uint32{65537}, []Extent{{4096, 69632}}, 65545, []uint32{8, 65545}, []bool{true}},
		{"incompressible", random, []uint32{65537}, []Extent{{4096, 69632}}, 65545, []uint32{8, 65545}, []bool{true}},
		{"shifted short tail reverse extents", shifted, []uint32{1, 5000}, []Extent{{12288, 4096}, {4096, 4096}}, 5013, []uint32{12, 12 + uint32(len(firstEnc)), 5013}, []bool{false, false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md, parts := simpleFixture(tc.raw, tc.lengths, tc.extents, tc.compressed)
			patch := patchFixture(md, parts, true)
			dir := t.TempDir()
			in, out := filepath.Join(dir, "patch"), filepath.Join(dir, "image")
			os.WriteFile(in, patch, 0600)
			if err := Apply(in, out); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(out)
			if err != nil {
				t.Fatal(err)
			}
			want := make([]byte, md.TotalBytes)
			fork := make([]byte, 0)
			for _, e := range tc.extents {
				fork = append(fork, make([]byte, e.Size)...)
			}
			for j, start := range tc.starts {
				binary.LittleEndian.PutUint32(fork[j*4:], start)
			}
			for j := range tc.stored {
				raw := tc.raw[j*chunkSize : min((j+1)*chunkSize, len(tc.raw))]
				p := lzbitmap.Encode(raw, chunkSize)
				if tc.stored[j] {
					p = append([]byte{0xff}, raw...)
				}
				copy(fork[tc.starts[j]:], p)
			}
			off := 0
			used := make([]bool, len(want))
			for _, e := range tc.extents {
				copy(want[e.Offset:e.Offset+e.Size], fork[off:off+int(e.Size)])
				off += int(e.Size)
				for i := e.Offset; i < e.Offset+e.Size; i++ {
					used[i] = true
				}
			}
			gap := 0
			for i := range want {
				if !used[i] {
					want[i] = parts[1][gap]
					gap++
				}
			}
			if !bytes.Equal(got, want) {
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("image differs at %d: got %x want %x", i, got[i], want[i])
					}
				}
			}
			parsed, err := ParseRawImageDiff10(bytes.NewReader(patch))
			if err != nil {
				t.Fatal(err)
			}
			if len(parsed.Image.Forks) != 1 || !bytes.Equal(parsed.PatchData[0], bytes.Join(parts, nil)) {
				t.Fatal("parser result")
			}
		})
	}
}

func TestDigestBoundaries(t *testing.T) {
	raw := bytes.Repeat([]byte("01234567"), (digestPieceSize+chunkSize+19)/8+1)
	n := (len(raw) + chunkSize - 1) / chunkSize
	lengths := make([]uint32, n)
	for i := range lengths {
		lengths[i] = 1024
	}
	lengths[n-1] = uint32(len(raw) - (n-1)*chunkSize + 1)
	compressed := uint64(4 * (n + 1))
	for _, length := range lengths {
		compressed += uint64(length)
	}
	md, parts := simpleFixture(raw, lengths, []Extent{{4096, 262144}}, compressed)
	dir := t.TempDir()
	in, out := filepath.Join(dir, "patch"), filepath.Join(dir, "image")
	os.WriteFile(in, patchFixture(md, parts, false), 0600)
	if err := Apply(in, out); err != nil {
		t.Fatal(err)
	}
	// More than one part, and a piece boundary inside a fork.
	digest := &streamDigest{outer: sha256.New(), piece: sha256.New()}
	for _, p := range [][]byte{raw, []byte("second fork"), parts[1]} {
		for len(p) > 0 {
			n := min(997, len(p))
			digest.Write(p[:n])
			p = p[n:]
		}
		digest.finishPart()
	}
	want := fixtureDigest(raw, []byte("second fork"), parts[1])
	if !bytes.Equal(digest.outer.Sum(nil), want[:]) {
		t.Fatal("digest boundary mismatch")
	}
}

func TestRefusalsAndAtomicOutput(t *testing.T) {
	for _, name := range []string{"variants", "offsets", "delta", "pbzm metadata", "pbzm controls", "pbzm data", "flags2", "flags4", "type", "chunk order", "extent overlap", "extent ownership", "trailing metadata", "short stream", "long stream", "digest", "overflow", "oversized stored slot", "truncated block"} {
		t.Run(name, func(t *testing.T) {
			raw := bytes.Repeat([]byte("abcde"), 200)
			md, parts := simpleFixture(raw, []uint32{512}, []Extent{{4096, 4096}}, 520)
			switch name {
			case "flags2":
				md.Forks[0].Flags = 2
			case "flags4":
				md.Forks[0].Flags = 4
			case "type":
				md.Forks[0].Algorithm = 8
			case "chunk order":
				md.Forks[0].Chunks[0].Offset = 0
			case "extent overlap":
				md.Extents = append(md.Extents, md.Extents[0])
				md.ExtentCount++
			case "extent ownership":
				md.Forks = nil
				md.ForkCount = 0
			case "overflow":
				md.Forks[0].Chunks[0].Length = 1
				md.Forks[0].Compressed = 9
			case "oversized stored slot":
				parts[0] = make([]byte, chunkSize)
				rand.New(rand.NewSource(2)).Read(parts[0])
				md.Forks[0].Size = chunkSize
				md.Forks[0].Compressed = 70000
				md.Forks[0].Chunks[0].Length = 69992
				md.Extents[0].Size = 73728
				md.TotalBytes = 81920
				parts[1] = make([]byte, 8192)
			}
			patch := patchFixture(md, parts, false)
			controls := binary.LittleEndian.Uint64(patch[38:46])
			data := binary.LittleEndian.Uint64(patch[46:54])
			switch name {
			case "variants":
				binary.LittleEndian.PutUint16(patch[8:], 2)
			case "offsets":
				binary.LittleEndian.PutUint64(patch[30:], ^uint64(0))
			case "delta":
				binary.LittleEndian.PutUint64(patch[controls+28:], 1)
			case "pbzm metadata":
				copy(patch[62:], "pbzm")
			case "pbzm controls":
				copy(patch[controls:], "pbzm")
			case "pbzm data":
				copy(patch[data:], "pbzm")
			case "trailing metadata":
				binary.LittleEndian.PutUint64(patch[62+28+56:], 0)
			case "short stream":
				patch = patch[:len(patch)-1]
				binary.LittleEndian.PutUint64(patch[54:], uint64(len(patch)))
			case "long stream":
				extra := pbzxFixture([]byte{42}, false)
				patch = append(patch, extra[12:]...)
				binary.LittleEndian.PutUint64(patch[54:], uint64(len(patch)))
			case "digest":
				patch[62+28] ^= 1
			case "truncated block":
				binary.BigEndian.PutUint64(patch[data+20:], ^uint64(0))
			}
			dir := t.TempDir()
			in, out := filepath.Join(dir, "patch"), filepath.Join(dir, "image")
			os.WriteFile(in, patch, 0600)
			os.WriteFile(out, []byte("keep me"), 0600)
			if err := Apply(in, out); err == nil {
				t.Fatal("accepted invalid patch")
			}
			got, _ := os.ReadFile(out)
			if string(got) != "keep me" {
				t.Fatal("clobbered existing output")
			}
			leftovers, _ := filepath.Glob(filepath.Join(dir, ".ridiff-*"))
			if len(leftovers) > 0 {
				t.Fatal("temporary output leaked")
			}
		})
	}
}

func TestRealPatch(t *testing.T) {
	path := os.Getenv("RIDIFF_TEST_PATCH")
	if path == "" {
		t.Skip("set RIDIFF_TEST_PATCH to a real full-replacement patch")
	}
	out := filepath.Join(t.TempDir(), "image")
	if err := Apply(path, out); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	t.Logf("image SHA-256: %s", got)
	want := os.Getenv("RIDIFF_TEST_SHA256")
	if want == "" {
		in, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer in.Close()
		st, _ := in.Stat()
		rid, err := parsePatch(in, st.Size())
		if err != nil {
			t.Fatal(err)
		}
		switch hex.EncodeToString(rid.Image.Digest[:]) {
		case "69ccdad9f06ba8242d59120b627d212a6f5465c53098f083fe2705953e5f82f1":
			want = "656e4b4e5b3810a28fe24153a2008be716a37611d991488ce65d16fedab82eb8"
		case "d5190b2dc487867cf1f1f34863146c5039cd7af237a49da346aa0ddb43d3ca6a":
			want = "bdb90473cddd94e5cdb2c91752975bea79aaf7327a2b6a55a2e90d5a2579ee3e"
		default:
			t.Fatal("set RIDIFF_TEST_SHA256 for an unknown patch")
		}
	}
	if !strings.EqualFold(got, want) {
		t.Fatalf("image hash: got %s want %s", got, want)
	}
}

func TestMultipleForksAndSignedControl(t *testing.T) {
	first := bytes.Repeat([]byte("a"), 3000)
	second := bytes.Repeat([]byte("b"), 4000)
	gaps := bytes.Repeat([]byte("g"), 12288)
	md := &MetaData{
		metadata: metadata{TotalBytes: 20480, ExtentCount: 2, ForkCount: 2},
		Extents:  []Extent{{12288, 4096}, {4096, 4096}},
		Forks: []Fork{
			{Size: 3000, Compressed: 520, Flags: 1, ExtentIndex: 0, ExtentCount: 1, Algorithm: 14, Chunks: []ForkChunk{{8, 512}}},
			{Size: 4000, Compressed: 520, Flags: 1, ExtentIndex: 1, ExtentCount: 1, Algorithm: 14, Chunks: []ForkChunk{{8, 512}}},
		},
	}
	patch := patchFixture(md, [][]byte{first, second, gaps}, false)
	control := binary.LittleEndian.Uint64(patch[38:])
	literal := patch[control+28+8:]
	binary.LittleEndian.PutUint64(literal, binary.LittleEndian.Uint64(literal)|(uint64(1)<<63))
	dir := t.TempDir()
	in, out := filepath.Join(dir, "patch"), filepath.Join(dir, "image")
	os.WriteFile(in, patch, 0600)
	if err := Apply(in, out); err != nil {
		t.Fatal(err)
	}
	image, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range [][]byte{first, second} {
		start := int(md.Extents[i].Offset)
		if binary.LittleEndian.Uint32(image[start:]) != 8 || binary.LittleEndian.Uint32(image[start+4:]) != 520 {
			t.Fatal("fork table")
		}
		enc := lzbitmap.Encode(raw, chunkSize)
		if !bytes.Equal(image[start+8:start+8+len(enc)], enc) {
			t.Fatal("fork order")
		}
	}
	for _, start := range []int{0, 8192, 16384} {
		if !bytes.Equal(image[start:start+4096], gaps[:4096]) {
			t.Fatal("gap order")
		}
	}
}

func TestForkWriterCrossesExtents(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "image")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = f.Truncate(16384); err != nil {
		t.Fatal(err)
	}
	w := forkWriter{out: f, extents: []Extent{{12288, 4096}, {4096, 4096}}, starts: []uint64{0, 4096}}
	payload := bytes.Repeat([]byte{0x71}, 6000)
	if err = w.writeAt(payload, 1000); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 16384)
	if _, err = f.ReadAt(got, 0); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[13288:], payload[:3096]) || !bytes.Equal(got[4096:7000], payload[3096:]) {
		t.Fatal("extent split")
	}
}
