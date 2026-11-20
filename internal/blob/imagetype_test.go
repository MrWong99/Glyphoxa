package blob_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/blob"
)

// TestIsRasterImageType pins the shared same-origin image allowlist: the four
// raster formats pass, anything scriptable (or unknown) does not.
func TestIsRasterImageType(t *testing.T) {
	t.Parallel()
	for ct, want := range map[string]bool{
		"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true,
		"image/svg+xml": false, "text/html": false, "application/octet-stream": false, "": false,
		"IMAGE/PNG": false,
	} {
		if got := blob.IsRasterImageType(ct); got != want {
			t.Errorf("IsRasterImageType(%q) = %v, want %v", ct, got, want)
		}
	}
}
