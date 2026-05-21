//go:build !record

package voicecassette

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
)

// LoadSTT reads tests/voice-cassettes/<name>.yaml and returns a
// [stt.Recognizer] that replays it.
//
// Default (replay) build: returns a [*STTRecognizer] that re-hashes the
// frames it is fed and returns the cassette's pinned transcript on match.
// Missing, malformed, or empty cassettes fail the test. To rewrite a
// cassette against the live ElevenLabs API, rebuild with `-tags=record` —
// see stt_record.go.
func LoadSTT(t *testing.T, name string) stt.Recognizer {
	t.Helper()
	c, _ := loadSTTCassetteFromDisk(t, name, true)
	return &STTRecognizer{name: name, cassette: c}
}
