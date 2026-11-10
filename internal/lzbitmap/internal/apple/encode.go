//go:build darwin && cgo

// Package apple provides a libcompression oracle for encoder tests.
package apple

/*
#cgo LDFLAGS: -lcompression
#include <compression.h>
*/
import "C"
import "unsafe"

func Encode(src []byte, capacity int) []byte {
	if len(src) == 0 || capacity <= 0 {
		return nil
	}
	dst := make([]byte, capacity)
	n := C.compression_encode_buffer((*C.uint8_t)(unsafe.Pointer(&dst[0])), C.size_t(capacity), (*C.uint8_t)(unsafe.Pointer(&src[0])), C.size_t(len(src)), nil, C.compression_algorithm(0x702))
	if n == 0 {
		return nil
	}
	return dst[:int(n)]
}
