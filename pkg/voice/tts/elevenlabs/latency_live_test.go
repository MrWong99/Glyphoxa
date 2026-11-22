//go:build live

// Live first-audio check for the #702 default model. Excluded from the default
// keyless suite by the `live` tag — it makes PAID calls to the real ElevenLabs
// endpoint and runs only with `go test -tags=live` and ELEVENLABS_API_KEY set
// (from the keyring via env, never printed).
//
// It synthesizes German NPC lines through the real [Client] the way the live
// pipeline does — a persisted default voice ([DefaultVoice], pcm_48000), one
// sentence per call, on a connection [Client.Warm] opened — and asserts the
// default model's first audio chunk lands inside the live budget. The same lines
// are rendered on eleven_v3 for comparison. Set GLYPHOXA_TTS_SAMPLES_DIR to
// keep every clip as a WAV for listening (v3 vs v4 Turbo, plus audio tags).
package elevenlabs_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// liveVoiceID is ElevenLabs' public "George" preset, the seed NPC's voice.
const liveVoiceID = "JBFqnCBsd6RMkjVDRZzb"

// warmFirstAudioBudget is the bar for the default model's first audio chunk on a
// warm connection: ~135 ms measured, so 400 ms leaves room for a noisy network
// while still failing anything in eleven_v3's ~600 ms class.
const warmFirstAudioBudget = 400 * time.Millisecond

var liveLines = []struct{ name, text string }{
	{"greeting", "Willkommen im Gasthaus zum Grünen Drachen, Reisender. Was darf es sein?"},
	{"price", "Ein Zimmer kostet drei Silberlinge die Nacht, das Frühstück ist inbegriffen."},
	{"warning", "Hütet euch vor dem Moor im Norden, dort spuken seit Wochen die Irrlichter."},
	{"laughs", "[laughs] Das ist ja köstlich, so etwas habe ich lange nicht mehr gehört!"},
	{"whispers", "[whispers] Der Fremde in der Ecke ist kein gewöhnlicher Händler, glaubt mir."},
}

func liveVoice(t *testing.T, model string) tts.Voice {
	t.Helper()
	v := elevenlabs.DefaultVoice(liveVoiceID, "de")
	if model == "" {
		return v
	}
	var s elevenlabs.Settings
	if err := json.Unmarshal(v.Settings, &s); err != nil {
		t.Fatalf("decode default settings: %v", err)
	}
	s.ModelID = model
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	v.Settings = raw
	return v
}

// synthesizeTimed renders one sentence and reports the time to its first audio
// chunk plus the whole clip.
func synthesizeTimed(t *testing.T, c *elevenlabs.Client, voice tts.Voice, text string) (time.Duration, []byte, int) {
	t.Helper()
	start := time.Now()
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: text, Voice: voice})
	if err != nil {
		t.Fatalf("Synthesize(%q): %v", text, err)
	}
	var (
		first time.Duration
		pcm   []byte
		rate  int
	)
	for chunk := range ch {
		if chunk.Err != nil {
			t.Fatalf("Synthesize(%q) mid-stream: %v", text, chunk.Err)
		}
		if first == 0 {
			first = time.Since(start)
			rate = chunk.SampleRate
		}
		pcm = append(pcm, chunk.PCM...)
	}
	if first == 0 {
		t.Fatalf("Synthesize(%q) produced no audio", text)
	}
	return first, pcm, rate
}

func TestLive_DefaultModelFirstAudioOnWarmConnection(t *testing.T) {
	if os.Getenv(elevenlabs.APIKeyEnv) == "" {
		t.Skipf("%s not set", elevenlabs.APIKeyEnv)
	}
	dir := os.Getenv("GLYPHOXA_TTS_SAMPLES_DIR")
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("samples dir: %v", err)
		}
	}

	for _, arm := range []struct{ label, model string }{
		{"default-" + elevenlabs.DefaultModel, ""},
		{elevenlabs.ModelV3, elevenlabs.ModelV3},
	} {
		c := elevenlabs.New("")
		if err := c.Warm(context.Background()); err != nil {
			t.Fatalf("Warm: %v", err)
		}
		voice := liveVoice(t, arm.model)
		var firsts []time.Duration
		for _, line := range liveLines {
			first, pcm, rate := synthesizeTimed(t, c, voice, line.text)
			firsts = append(firsts, first)
			t.Logf("%-26s %-9s first audio %4d ms, %.2f s of audio", arm.label, line.name,
				first.Milliseconds(), float64(len(pcm))/2/float64(rate))
			if arm.model == "" && first > warmFirstAudioBudget {
				t.Errorf("%s %s: first audio %v on a warm connection, want < %v", arm.label, line.name, first, warmFirstAudioBudget)
			}
			if dir != "" {
				path := filepath.Join(dir, fmt.Sprintf("%s_%s.wav", arm.label, line.name))
				if err := os.WriteFile(path, wav(pcm, rate), 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}
		}
		sort.Slice(firsts, func(i, j int) bool { return firsts[i] < firsts[j] })
		t.Logf("%-26s median first audio %d ms (min %d, max %d, n=%d)", arm.label,
			firsts[len(firsts)/2].Milliseconds(), firsts[0].Milliseconds(), firsts[len(firsts)-1].Milliseconds(), len(firsts))
	}
}

// wav wraps mono int16 PCM in a RIFF header.
func wav(pcm []byte, rate int) []byte {
	b := make([]byte, 44, 44+len(pcm))
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+len(pcm)))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1)
	binary.LittleEndian.PutUint16(b[22:], 1)
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(len(pcm)))
	return append(b, pcm...)
}
