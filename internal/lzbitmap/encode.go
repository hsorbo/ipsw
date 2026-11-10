//go:build cgo

// Package lzbitmap provides the LZBITMAP encoder used to reconstruct RIDIFF forks.
package lzbitmap

/*
#include "lzbitmap.h"
*/
import "C"
import "unsafe"

// Encode encodes with libcompression's LZBITMAP level 2 algorithm.
// Capacity is the destination size, which affects encoding decisions.
// It returns nil if the input is empty, capacity is nonpositive, or encoding fails.
func Encode(src []byte, capacity int) []byte {
	if len(src) == 0 || capacity <= 0 {
		return nil
	}
	dst := make([]byte, capacity)
	n := C.lzbitmap_encode_buffer(unsafe.Pointer(&dst[0]), C.size_t(capacity),
		unsafe.Pointer(&src[0]), C.size_t(len(src)), C.LZBITMAP_2)
	if n == 0 {
		return nil
	}
	return dst[:int(n)]
}
