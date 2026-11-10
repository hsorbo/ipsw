//go:build cgo

package lzbitmap

import (
	"bytes"
	"math/rand"
	"testing"

	lzfse "github.com/blacktop/lzfse-cgo"
)

func TestEncode(t *testing.T) {
	if Encode(nil, 65536) != nil || Encode([]byte{1}, 0) != nil || Encode([]byte{1}, -1) != nil {
		t.Fatal("invalid buffer accepted")
	}
	rng := rand.New(rand.NewSource(3))
	for _, size := range []int{1, 17, 0x90, 0x91, 32768, 32769, 65535, 65536} {
		for mode := 0; mode < 3; mode++ {
			raw := make([]byte, size)
			rng.Read(raw)
			if mode == 1 {
				for i := range raw {
					raw[i] &= 7
				}
			}
			if mode == 2 {
				for i := range raw {
					raw[i] = byte(i % 17)
				}
			}
			enc := Encode(raw, size+1024)
			if len(enc) == 0 {
				t.Fatalf("encode %d/%d", size, mode)
			}
			out := make([]byte, size)
			if code := lzfse.LzBitMapDecompress(enc, out); code != 0 {
				t.Fatalf("decode error %d", code)
			}
			if !bytes.Equal(raw, out) {
				t.Fatalf("round trip %d/%d", size, mode)
			}
		}
	}
}
