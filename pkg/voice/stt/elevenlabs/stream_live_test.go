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
	"context"
	"os"
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
