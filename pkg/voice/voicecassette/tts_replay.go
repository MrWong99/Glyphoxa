//go:build !record

package voicecassette

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// LoadTTS reads tests/voice-cassettes/<name>.yaml and returns a
// [tts.Synthesizer] that replays it.
//
// Default (replay) build: returns a [*TTSSynthesizer] that matches incoming
// Synthesize calls positionally against the recorded sentence list and
// returns a closed audio channel on each hit. Missing, malformed, or empty
// cassettes fail the test. To rewrite a cassette against the live ElevenLabs
// API, rebuild with `-tags=record` — see tts_record.go.
func LoadTTS(t *testing.T, name string) tts.Synthesizer {
	t.Helper()
	c := loadTTSCassetteForReplay(t, name)
	return &TTSSynthesizer{name: name, cassette: c}
}
