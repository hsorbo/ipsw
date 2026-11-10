//go:build cgo

package ridiff

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"

	"github.com/blacktop/ipsw/internal/lzbitmap"
)

// Apply reconstructs a single-variant, full-replacement RIDIFF10 image.
// It supports pbzx sections and LZBITMAP resource forks. The output is
// atomically replaced only after the stream's metadata digest is verified.
// Rules follow the RIDIFF10 specification supplied with this implementation.
func Apply(patch, output string) error {
	in, err := os.Open(patch)
	if err != nil {
		return err
	}
	defer in.Close()
	stat, err := in.Stat()
	if err != nil {
		return err
	}
	rid, err := parsePatch(in, stat.Size())
	if err != nil {
		return err
	}
	data, err := newSectionReader(io.NewSectionReader(in, int64(rid.PatchDataOffset[0]), int64(rid.PatchSize-rid.PatchDataOffset[0])))
	if err != nil {
		return fmt.Errorf("patch data: %w", err)
	}
	out, err := os.CreateTemp(filepath.Dir(output), ".ridiff-*")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	defer out.Close()
	if err = out.Truncate(int64(rid.Image.TotalBytes)); err != nil {
		return err
	}
	digest := &streamDigest{outer: sha256.New(), piece: sha256.New()}
	stream := io.TeeReader(data, digest)
	for i, f := range rid.Image.Forks {
		if err = writeFork(stream, out, f, rid.Image.Extents); err != nil {
			return fmt.Errorf("fork %d: %w", i, err)
		}
		digest.finishPart()
	}
	extents := append([]Extent(nil), rid.Image.Extents...)
	sort.Slice(extents, func(i, j int) bool { return extents[i].Offset < extents[j].Offset })
	var pos uint64
	for _, e := range append(extents, Extent{Offset: rid.Image.TotalBytes}) {
		if e.Offset > pos {
			if _, err = out.Seek(int64(pos), io.SeekStart); err != nil {
				return err
			}
			if _, err = io.CopyN(out, stream, int64(e.Offset-pos)); err != nil {
				return fmt.Errorf("image gaps: %w", err)
			}
		}
		pos = e.Offset + e.Size
	}
	digest.finishPart()
	var extra [1]byte
	if n, err := data.Read(extra[:]); n != 0 || err != io.EOF {
		return fmt.Errorf("patch stream size mismatch or invalid data: %v", err)
	}
	if !bytes.Equal(digest.outer.Sum(nil), rid.Image.Digest[:]) {
		return fmt.Errorf("RIDIFF stream digest mismatch")
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(out.Name(), output)
}

const digestPieceSize = 8 << 20

type streamDigest struct {
	outer, piece hash.Hash
	used         int
}

func (d *streamDigest) Write(p []byte) (int, error) {
	total := len(p)
	for len(p) > 0 {
		n := min(len(p), digestPieceSize-d.used)
		d.piece.Write(p[:n])
		d.used += n
		p = p[n:]
		if d.used == digestPieceSize {
			d.finishPart()
		}
	}
	return total, nil
}
func (d *streamDigest) finishPart() {
	if d.used > 0 {
		d.outer.Write(d.piece.Sum(nil))
		d.piece.Reset()
		d.used = 0
	}
}

// forkWriter maps logical fork offsets to extents in metadata order, which
// need not be image order. Untouched slot and extent padding remains sparse.
type forkWriter struct {
	out     *os.File
	extents []Extent
	starts  []uint64
}

func (w forkWriter) writeAt(p []byte, off uint64) error {
	for len(p) > 0 {
		i := sort.Search(len(w.starts), func(i int) bool { return w.starts[i]+w.extents[i].Size > off })
		if i == len(w.starts) {
			return fmt.Errorf("write exceeds fork extents")
		}
		e := w.extents[i]
		within := off - w.starts[i]
		n := min(uint64(len(p)), e.Size-within)
		written, err := w.out.WriteAt(p[:int(n)], int64(e.Offset+within))
		if err != nil {
			return err
		}
		if written != int(n) {
			return io.ErrShortWrite
		}
		off += n
		p = p[int(n):]
	}
	return nil
}

func writeFork(src io.Reader, out *os.File, f Fork, extents []Extent) error {
	w := forkWriter{out: out, extents: extents[f.ExtentIndex : f.ExtentIndex+f.ExtentCount]}
	var size uint64
	for _, e := range w.extents {
		w.starts = append(w.starts, size)
		size += e.Size
	}
	table := make([]byte, 4*(len(f.Chunks)+1))
	cur := f.Chunks[0].Offset
	left := f.Size
	workers := min(runtime.GOMAXPROCS(0), 16)
	for base := 0; base < len(f.Chunks); base += workers {
		n := min(workers, len(f.Chunks)-base)
		raw, enc := make([][]byte, n), make([][]byte, n)
		var wg sync.WaitGroup
		// Read first so an input error cannot leave encoding workers behind.
		for i := range n {
			raw[i] = make([]byte, int(min(uint64(chunkSize), left)))
			if _, err := io.ReadFull(src, raw[i]); err != nil {
				return err
			}
			left -= uint64(len(raw[i]))
		}
		for i := range n {
			wg.Go(func() { enc[i] = lzbitmap.Encode(raw[i], chunkSize) })
		}
		wg.Wait()
		for i := range n {
			j := base + i
			c := f.Chunks[j]
			length := uint64(len(enc[i]))
			stored := enc[i] == nil || len(enc[i]) > len(raw[i])
			if stored {
				length = uint64(len(raw[i]) + 1)
			}
			end := c.Offset + uint64(c.Length)
			slot := length
			if cur+length < end {
				slot = end - cur
			}
			if cur > f.Compressed || slot > f.Compressed-cur {
				return fmt.Errorf("chunk %d exceeds compressed size; reclaiming earlier padding is unsupported", j)
			}
			payload := enc[i]
			if slot == uint64(len(raw[i])+1) {
				payload = append([]byte{0xff}, raw[i]...)
			} else if stored {
				return fmt.Errorf("chunk %d: stored chunk with oversized slot is unsupported", j)
			}
			binary.LittleEndian.PutUint32(table[j*4:], uint32(cur))
			if err := w.writeAt(payload, cur); err != nil {
				return err
			}
			cur += slot
		}
	}
	binary.LittleEndian.PutUint32(table[len(f.Chunks)*4:], uint32(cur))
	return w.writeAt(table, 0)
}
