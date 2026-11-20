//go:build opus

package voiceprobe

import (
	"context"
	"errors"
	"io"
	"math"
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec"
)

// TestClipRoundTrip pushes a real clip through everything the probe does on
// either side of Discord: WAV load -> Opus encode (the speak path) -> inbound
// frames -> decode onto a track (the record path). Only the network is skipped.
func TestClipRoundTrip(t *testing.T) {
	clip, err := LoadWAV("../../../tests/voice-clips/hello-test/audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	ch := make(chan tts.AudioChunk, 1)
	ch <- tts.AudioChunk{PCM: clip.PCM, SampleRate: clip.SampleRate, Channels: clip.Channels}
	close(ch)
	src, err := codec.New().PlaybackSource(ch)
	if err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	rec := newRecorder(start, 0)
	for i := 0; ; i++ {
		opus, err := src.NextFrame(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		pts := time.Duration(i) * 20 * time.Millisecond
		rec.record(gxvoice.Frame{UserID: botID, Opus: opus, PTS: pts}, start.Add(pts))
	}

	tracks := rec.tracksSnapshot()
	if len(tracks) != 1 || tracks[0].DecodeErrors != 0 {
		t.Fatalf("tracks = %+v", tracks)
	}
	tr := tracks[0]
	if diff := (tr.Voiced - clip.Duration()).Abs(); diff > 100*time.Millisecond {
		t.Fatalf("recorded %s, clip is %s", tr.Voiced, clip.Duration())
	}
	var energy float64
	for _, s := range tr.PCM {
		energy += float64(s) * float64(s)
	}
	if rms := math.Sqrt(energy / float64(len(tr.PCM))); rms < 50 {
		t.Fatalf("recording is near-silent (rms %.1f)", rms)
	}
}
