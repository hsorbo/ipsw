//go:build !darwin && cgo

package ridiff

import (
	"strings"
	"testing"
)

func TestRawImagePatchRejectsBase(t *testing.T) {
	err := RawImagePatch("base", "missing", "output", 0)
	if err == nil || !strings.Contains(err.Error(), "base image") {
		t.Fatalf("unexpected error: %v", err)
	}
}
