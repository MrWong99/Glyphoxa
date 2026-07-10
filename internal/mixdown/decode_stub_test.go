//go:build !opus

package mixdown

import (
	"errors"
	"testing"
	"time"
)

// In the default build (no -tags opus) the built-in decoder is unavailable, so
// a snapshot with frames to decode and no injected Options.Decoder must report
// ErrDecoderUnavailable rather than silently producing garbage.
func TestWAVClip_StubDecoderErrors(t *testing.T) {
	base := time.Unix(8000, 0)
	snap := Snapshot{From: base, To: base.Add(time.Second), Lanes: []LaneSnapshot{
		{LaneID: "spk", Frames: []Frame{{Opus: []byte{0x01, 0x02}, At: base}}},
	}}

	_, err := WAVClip(snap, Options{}) // nil Decoder → build default
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("err = %v, want ErrDecoderUnavailable", err)
	}
}

// An empty snapshot needs no decoder, so it succeeds even in the stub build.
func TestWAVClip_StubEmptySnapshotOK(t *testing.T) {
	base := time.Unix(8100, 0)
	clip, err := WAVClip(Snapshot{From: base, To: base.Add(time.Second)}, Options{})
	if err != nil {
		t.Fatalf("empty snapshot: %v", err)
	}
	if len(clip) != 44+48000*2 {
		t.Fatalf("clip length = %d, want %d", len(clip), 44+48000*2)
	}
}
