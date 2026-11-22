//go:build live

// Live smoke for the Scribe v2 Realtime streaming adapter. Excluded from the
// default keyless suite by the `live` tag — it opens a real websocket to
// ElevenLabs and only runs with `go test -tags=live` and ELEVENLABS_API_KEY set
// (key from the environment, never printed).
//
// The one thing only a live run can prove is the StreamModel constant
// ("scribe_v2_realtime"): a wrong model_id is rejected by the provider, either
// at the handshake (dial StreamError) or as an error frame that resolves the
// commit with a *stt.StreamError. This test drives a short silent utterance
// through OpenStream -> Send -> Commit and fails loudly with the provider's
// error text (which lists accepted model ids) so the constant can be corrected.
package elevenlabs_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
)

func TestLive_StreamingScribeV2Realtime_CommitResolves(t *testing.T) {
	if os.Getenv("ELEVENLABS_API_KEY") == "" {
		t.Skip("ELEVENLABS_API_KEY not set; skipping live streaming smoke")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c := elevenlabs.New("")
	partials := 0
	s, err := c.OpenStream(ctx, stt.StreamConfig{
		SampleRate: 16000,
		OnPartial:  func(string) { partials++ },
	})
	if err != nil {
		t.Fatalf("OpenStream failed (a model_id rejection surfaces here as a dial StreamError): %v", err)
	}
	defer s.Close()

	// ~2.5s of 32 ms silent frames — the provider begins processing after the
	// first 2s, so this is enough audio to elicit a committed_transcript (empty
	// text is a valid result for silence).
	silence := make([]int16, 512)
	for i := 0; i < 78; i++ {
		f, err := audio.NewFrame(silence, 16000, 32)
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		if err := s.Send(f); err != nil {
			t.Fatalf("Send: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}

	commitCh, err := s.Commit()
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	select {
	case res := <-commitCh:
		if res.Err != nil {
			t.Fatalf("commit resolved with error (check the code/text for accepted model ids): %v", res.Err)
		}
		t.Logf("live commit resolved: text=%q partials=%d", res.Transcript.Text, partials)
	case <-time.After(20 * time.Second):
		t.Fatal("commit did not resolve within 20s")
	}
}

// clipFrames loads a tests/voice-clips clip (16 kHz mono s16le WAV) as 32 ms
// frames, the geometry the voice loop streams.
func clipFrames(t *testing.T, clip string) []audio.Frame {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "tests", "voice-clips", clip, "audio.wav"))
	if err != nil {
		t.Fatalf("read clip %s: %v", clip, err)
	}
	i := bytes.Index(raw, []byte("data"))
	if i < 0 || i+8 > len(raw) {
		t.Fatalf("clip %s: no data chunk", clip)
	}
	pcm := raw[i+8:]
	var frames []audio.Frame
	for off := 0; off+1024 <= len(pcm); off += 1024 {
		samples := make([]int16, 512)
		for j := range samples {
			samples[j] = int16(binary.LittleEndian.Uint16(pcm[off+2*j:]))
		}
		f, err := audio.NewFrame(samples, 16000, 32)
		if err != nil {
			t.Fatalf("NewFrame: %v", err)
		}
		frames = append(frames, f)
	}
	return frames
}

// TestLive_StreamingScribeV2Realtime_SurvivesIdleGap is the live proof of the
// idle keepalive (#702, ADR-0042 amendment 2026-11-22). The provider closes a
// realtime session 15 s after the last client message, so before the keepalive
// the second clip below could never be committed on the session that carried the
// first. It streams a real clip at real-time pace, commits, stays silent for 20 s
// (longer than the idle window), then streams and commits a second clip ON THE
// SAME SESSION. Both commits must resolve with text and the session must not have
// been closed in between.
//
//	set -a; . ./.env; set +a
//	go test -tags=live -run TestLive_StreamingScribeV2Realtime_SurvivesIdleGap -v ./pkg/voice/stt/elevenlabs/
func TestLive_StreamingScribeV2Realtime_SurvivesIdleGap(t *testing.T) {
	if os.Getenv("ELEVENLABS_API_KEY") == "" {
		t.Skip("ELEVENLABS_API_KEY not set; skipping live streaming smoke")
	}
	const idleGap = 20 * time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var partials atomic.Int64
	closed := make(chan error, 1)
	s, err := elevenlabs.New("").OpenStream(ctx, stt.StreamConfig{
		SampleRate: 16000,
		OnPartial:  func(string) { partials.Add(1) },
		OnClosed:   func(err error) { closed <- err },
	})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	defer s.Close()

	speak := func(clip string) {
		t.Helper()
		frames := clipFrames(t, clip)
		tick := time.NewTicker(32 * time.Millisecond)
		defer tick.Stop()
		for _, f := range frames {
			if err := s.Send(f); err != nil {
				t.Fatalf("%s: Send: %v", clip, err)
			}
			<-tick.C
		}
		sent := time.Now()
		ch, err := s.Commit()
		if err != nil {
			t.Fatalf("%s: Commit: %v", clip, err)
		}
		select {
		case res := <-ch:
			if res.Err != nil {
				t.Fatalf("%s: commit resolved with error: %v", clip, res.Err)
			}
			if res.Transcript.Text == "" {
				t.Errorf("%s: commit resolved with empty text for a spoken clip", clip)
			}
			t.Logf("%s: %d frames (%.2fs) committed in %v, %d partials so far, text=%q",
				clip, len(frames), float64(len(frames))*0.032, time.Since(sent).Round(time.Millisecond),
				partials.Load(), res.Transcript.Text)
		case <-time.After(20 * time.Second):
			t.Fatalf("%s: commit did not resolve within 20s", clip)
		}
	}

	speak("hello-test")

	t.Logf("idle for %v (provider idle window is 15s)…", idleGap)
	select {
	case err := <-closed:
		t.Fatalf("the session was closed during the idle gap (keepalive did not hold it): %v", err)
	case <-time.After(idleGap):
	}

	speak("bart-test")

	select {
	case err := <-closed:
		t.Fatalf("the session closed before the test ended: %v", err)
	default:
	}
}
