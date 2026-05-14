// Package voicecassette implements VCR-style record/replay for vendor calls
// in the voice pipeline, per ADR-0021.
//
// Cassettes live at tests/voice-cassettes/*.yaml and are committed. A test
// loads a cassette by name; the cassette-backed provider asserts that the
// incoming request matches the recorded fingerprint (e.g. an audio sha256)
// and replays the recorded response. A mismatch fails the test with a
// pointer to re-record under `-tags=record`.
//
// The v1.0 plumbing covers STT — see [STTRecognizer] — and is shaped to
// extend to LLM and TTS cassettes later (per ADR-0021's per-vendor policy).
package voicecassette

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"gopkg.in/yaml.v3"
)

// STTCassette is the on-disk record of one STT request/response pair.
//
// AudioSHA256 fingerprints the PCM samples the recognizer was fed — sha256
// over the little-endian int16 stream, taken in frame order. A test that
// feeds different audio produces a different hash and is told to re-record.
type STTCassette struct {
	// Name matches the cassette filename without the ".yaml" suffix; used
	// only for error messages.
	Name string `yaml:"name"`

	// AudioSHA256 is the hex-encoded sha256 of the PCM sample stream the
	// recognizer is expected to see.
	AudioSHA256 string `yaml:"audio_sha256"`

	// Transcript is the authoritative text the recognizer would return for
	// this audio.
	Transcript string `yaml:"transcript"`

	// Notes is free-form provenance (provider, model, recording date). Not
	// load-bearing; survives round-trip for human reviewers.
	Notes string `yaml:"notes,omitempty"`
}

// STTRecognizer is a [stt.Recognizer] that replays a single [STTCassette].
//
// Each call to Transcribe re-hashes the incoming frames and compares against
// the cassette's AudioSHA256; on mismatch the call returns an error pointing
// the caller at the re-record workflow. On match it returns the cassette's
// pinned transcript verbatim.
type STTRecognizer struct {
	cassette STTCassette
}

// LoadSTT reads tests/voice-cassettes/<name>.yaml and returns a recognizer
// that replays it. Missing or malformed cassettes fail the test.
func LoadSTT(t *testing.T, name string) *STTRecognizer {
	t.Helper()
	path := filepath.Join(cassettesDir(), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("voicecassette.LoadSTT(%q): %v", name, err)
	}
	var c STTCassette
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("voicecassette.LoadSTT(%q): unmarshal: %v", name, err)
	}
	if c.Name == "" {
		c.Name = name
	}
	if c.AudioSHA256 == "" {
		t.Fatalf("voicecassette.LoadSTT(%q): cassette has empty audio_sha256", name)
	}
	return &STTRecognizer{cassette: c}
}

// Transcribe implements [stt.Recognizer]. Returns the pinned transcript on
// hash match; otherwise an error that names both hashes so the diff is
// obvious in test output.
func (r *STTRecognizer) Transcribe(_ context.Context, frames []audio.Frame) (stt.Transcript, error) {
	got := HashFrames(frames)
	if got != r.cassette.AudioSHA256 {
		return stt.Transcript{}, fmt.Errorf(
			"voicecassette: audio hash mismatch for cassette %q (got %s, recorded %s); re-record with -tags=record",
			r.cassette.Name, got, r.cassette.AudioSHA256,
		)
	}
	return stt.Transcript{Text: r.cassette.Transcript}, nil
}

// HashFrames returns the hex-encoded sha256 of the concatenated little-endian
// int16 sample stream across frames. Exported so test helpers (and the
// re-record path, once it exists) can compute the same fingerprint.
func HashFrames(frames []audio.Frame) string {
	h := sha256.New()
	var buf [2]byte
	for _, f := range frames {
		for _, s := range f.Samples() {
			binary.LittleEndian.PutUint16(buf[:], uint16(s))
			h.Write(buf[:])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// cassettesDir locates tests/voice-cassettes/ from the running test binary.
// Same trick as voicetest.repoRoot: walk up from this source file to the
// nearest go.mod.
var cassettesDir = sync.OnceValue(func() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("voicecassette: runtime.Caller(0) failed")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "tests", "voice-cassettes")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			panic("voicecassette: go.mod not found above " + filepath.Dir(file))
		}
		dir = parent
	}
})
