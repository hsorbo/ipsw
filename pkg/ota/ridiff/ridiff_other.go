//go:build !darwin && cgo

package ridiff

import "fmt"

// RawImagePatch takes a Raw Image Diff and converts it to an APFS volume.
func RawImagePatch(input, patch, output string, verbose uint32) error {
	if input != "" {
		return fmt.Errorf("RawImagePatch: delta patches with a base image are unsupported")
	}
	return Apply(patch, output)
}
