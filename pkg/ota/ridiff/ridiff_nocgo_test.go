//go:build !cgo

package ridiff

import (
	"strings"
	"testing"
)

func TestRawImagePatchRequiresCGO(t *testing.T) {
	err := RawImagePatch("", "missing", "output", 0)
	if err == nil || !strings.Contains(err.Error(), "requires cgo") {
		t.Fatalf("unexpected error: %v", err)
	}
}
