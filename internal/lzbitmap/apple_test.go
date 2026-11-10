//go:build darwin && cgo

package lzbitmap

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/blacktop/ipsw/internal/lzbitmap/internal/apple"
)

func TestLzBitMapAgainstApple(t *testing.T) {
	rng := rand.New(rand.NewSource(702))
	sizes := []int{1, 2, 3, 7, 16, 0x90, 0x91, 0x7fff, 0x8000, 0x8001, 0xffff, 0x10000}
	for i := 0; i < 3000; i++ {
		size := rng.Intn(65536) + 1
		if i < len(sizes) {
			size = sizes[i]
		}
		raw := make([]byte, size)
		rng.Read(raw)
		switch i % 5 {
		case 0:
			for j := range raw {
				raw[j] &= 7
			}
		case 1:
			for j := range raw {
				raw[j] = byte(j % 19)
			}
		case 2:
			for j := range raw {
				raw[j] = 0
			}
		case 3:
			for j := 1; j < len(raw); j++ {
				if rng.Intn(4) != 0 {
					raw[j] = raw[rng.Intn(j)]
				}
			}
		}
		for _, capacity := range []int{65536, size + 1024} {
			got, want := Encode(raw, capacity), apple.Encode(raw, capacity)
			if !bytes.Equal(got, want) {
				t.Fatalf("case %d size %d capacity %d: got %d bytes, Apple %d", i, size, capacity, len(got), len(want))
			}
		}
	}
}
