package bundle_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/blob"
	"github.com/MrWong99/Glyphoxa/internal/bundle"
	"github.com/MrWong99/Glyphoxa/internal/storage"
)

// missingBlobs is a blob seam whose every Get answers "no such object".
type missingBlobs struct{ blob.Store }

func (missingBlobs) Get(context.Context, string) (io.ReadCloser, blob.Meta, error) {
	return nil, blob.Meta{}, blob.ErrNotFound
}

// TestPGStore_ReadMapImage_MissingBlobIsNotFound pins the export seam's error
// contract: a Map row whose bytes are gone from the blob store reads as
// storage.ErrNotFound — the same answer a keyless row gets — so the exporter
// writes the Map without a picture instead of refusing the whole backup for one
// vanished image (it recognises storage.ErrNotFound, not blob.ErrNotFound).
func TestPGStore_ReadMapImage_MissingBlobIsNotFound(t *testing.T) {
	t.Parallel()
	p := bundle.PGStore{Blobs: missingBlobs{}}
	_, _, err := p.ReadMapImage(context.Background(), "tenant/map/x/image")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("err = %v, want storage.ErrNotFound", err)
	}
}
