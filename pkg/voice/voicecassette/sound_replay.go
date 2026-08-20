//go:build !record

package voicecassette

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
)

// LoadSound reads tests/voice-cassettes/<name>.yaml and returns a
// [soundgen.Generator] that replays it.
//
// Default (replay) build: returns a [*SoundGenerator] that hashes each request
// it is handed (kind + model + duration + prompt) and replays the recorded
// generation's metadata over deterministic stub bytes — the audio itself is
// never pinned (the TTS stub-cassette policy, ADR-0021). Missing, malformed,
// or empty cassettes — and any unrecorded request hash — fail the test, so a
// prompt-derivation change is caught. To rewrite a cassette against the live
// ElevenLabs endpoints, rebuild with `-tags=record` — see sound_record.go.
func LoadSound(t *testing.T, name string) soundgen.Generator {
	t.Helper()
	c, _ := loadSoundCassetteFromDisk(t, name, true)
	return &SoundGenerator{name: name, byHash: indexSoundByHash(c)}
}
