//go:build !cgo

package ridiff

import "fmt"

// RawImagePatch requires cgo for the LZBITMAP encoder.
func RawImagePatch(input, patch, output string, verbose uint32) error {
	return fmt.Errorf("RawImagePatch: LZBITMAP encoding requires cgo")
}
