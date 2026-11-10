package ridiff

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"runtime"
	"sync"

	"github.com/ulikunitz/xz"
)

const blockSize = 1 << 20

// sectionReader decodes independent pbzx blocks in bounded, ordered batches.
// Unlike an unbounded reorder queue, a slow first block cannot retain the
// remainder of a multi-gigabyte patch in memory.
type sectionReader struct {
	src     io.Reader
	pending *bytes.Reader
	err     error
	workers int
}

func newSectionReader(src io.Reader) (*sectionReader, error) {
	var h [12]byte
	if _, err := io.ReadFull(src, h[:]); err != nil {
		return nil, err
	}
	if string(h[:4]) == "pbzm" {
		return nil, fmt.Errorf("pbzm/LZRAVEN sections are unsupported")
	}
	if string(h[:4]) != "pbzx" {
		return nil, fmt.Errorf("invalid pbzx magic")
	}
	if binary.BigEndian.Uint64(h[4:]) != blockSize {
		return nil, fmt.Errorf("unsupported pbzx block size")
	}
	return &sectionReader{src: src, workers: min(runtime.GOMAXPROCS(0), 16)}, nil
}

func (s *sectionReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for s.pending == nil || s.pending.Len() == 0 {
		if s.err != nil {
			return 0, s.err
		}
		s.fill()
	}
	return s.pending.Read(p)
}

func (s *sectionReader) fill() {
	type block struct {
		raw  uint64
		data []byte
		err  error
	}
	blocks := make([]block, 0, s.workers)
	for range s.workers {
		var h [16]byte
		_, err := io.ReadFull(s.src, h[:])
		if err != nil {
			s.err = err
			break
		}
		raw, packed := binary.BigEndian.Uint64(h[:8]), binary.BigEndian.Uint64(h[8:])
		if raw == 0 || raw > blockSize || packed == 0 || packed > raw {
			s.err = fmt.Errorf("invalid pbzx block lengths %d/%d", raw, packed)
			break
		}
		b := block{raw: raw, data: make([]byte, int(packed))}
		if _, err = io.ReadFull(s.src, b.data); err != nil {
			s.err = err
			break
		}
		blocks = append(blocks, b)
	}
	var wg sync.WaitGroup
	for i := range blocks {
		wg.Go(func() {
			b := &blocks[i]
			if uint64(len(b.data)) == b.raw {
				return
			}
			r, err := xz.NewReader(bytes.NewReader(b.data))
			if err != nil {
				b.err = err
				return
			}
			b.data, b.err = io.ReadAll(io.LimitReader(r, int64(b.raw)+1))
			if b.err == nil && uint64(len(b.data)) != b.raw {
				b.err = fmt.Errorf("pbzx inflated size mismatch")
			}
		})
	}
	wg.Wait()
	var out bytes.Buffer
	for _, b := range blocks {
		if b.err != nil {
			s.err = b.err
			break
		}
		out.Write(b.data)
	}
	s.pending = bytes.NewReader(out.Bytes())
}

func decodeSmallSection(r io.Reader) ([]byte, error) {
	s, err := newSectionReader(r)
	if err != nil {
		return nil, err
	}
	const limit = 256 << 20
	b, err := io.ReadAll(io.LimitReader(s, limit+1))
	if err == nil && len(b) > limit {
		err = fmt.Errorf("RIDIFF metadata exceeds %d bytes", limit)
	}
	return b, err
}
