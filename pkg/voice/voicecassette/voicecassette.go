// Package voicecassette implements VCR-style record/replay for vendor calls
// in the voice pipeline, per ADR-0021.
//
// Cassettes live at tests/voice-cassettes/*.yaml and are committed. A test
// loads a cassette by name; the cassette-backed provider asserts that the
// incoming request matches the recorded fingerprint (e.g. an audio sha256)
// and replays the recorded response. A mismatch fails the test with a
// pointer to re-record under `-tags=record`.
//
// The v1.0 plumbing covers STT — see [STTRecognizer] — and TTS — see
// [TTSSynthesizer] (replay) and TTSRecorder (record). The shape extends to
// LLM cassettes later per ADR-0021's per-vendor policy.
//
// The TTS LoadTTS entry point has two build-tag-gated variants: the default
// (-tags absent) returns a replay-only [TTSSynthesizer]; -tags=record returns
// a recorder that forwards to a live [elevenlabs.Client], captures the
// dispatched sentences, and rewrites the on-disk cassette at test cleanup.
// Run `ELEVENLABS_API_KEY=… go test -tags=record ./...` to refresh.
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
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"gopkg.in/yaml.v3"
)

// STTSegment is one (audio fingerprint, transcript) pair within an
// [STTCassette] — the recognizer's output for a single utterance the STT
// stage was fed in one Transcribe call.
//
// AudioSHA256 fingerprints that utterance's PCM samples — sha256 over the
// little-endian int16 stream, taken in frame order (see [HashFrames]). A
// call that feeds different audio produces a different hash and is told to
// re-record.
type STTSegment struct {
	// AudioSHA256 is the hex-encoded sha256 of the PCM sample stream the
	// recognizer is expected to see for this segment.
	AudioSHA256 string `yaml:"audio_sha256"`

	// Transcript is the authoritative text the recognizer would return for
	// this segment's audio.
	Transcript string `yaml:"transcript"`
}

// STTCassette is the on-disk record of an STT scenario: an ordered list of
// request/response pairs, one [STTSegment] per Transcribe call.
//
// A single-utterance scenario (TB5/TB7/TB8) records exactly one segment; a
// VAD-segmented clip that drives several Transcribe calls records one segment
// per utterance, matched positionally on replay. This mirrors the TTS
// cassette's positional contract (see [TTSCassette]) so a clip whose
// segmentation changes is told to re-record rather than silently passing.
//
// The cassette's identity is its filename: LoadSTT(t, "stt-hello-test")
// reads tests/voice-cassettes/stt-hello-test.yaml. There is no name field
// on disk — one identity, one source of truth.
type STTCassette struct {
	// Segments is the ordered list of (fingerprint, transcript) pairs the
	// recognizer is expected to be driven through, one per Transcribe call.
	Segments []STTSegment `yaml:"segments"`

	// Notes is free-form provenance (provider, model, recording date). Not
	// load-bearing; survives round-trip for human reviewers.
	Notes string `yaml:"notes,omitempty"`
}

// STTRecognizer is a [stt.Recognizer] that replays a single [STTCassette].
//
// Calls to Transcribe are matched positionally against the cassette's ordered
// segments: the Nth call re-hashes its incoming frames and compares against
// Segments[N].AudioSHA256, returning that segment's pinned transcript on
// match. A hash mismatch, or a call past the end of the recorded list,
// returns an error pointing the caller at the re-record workflow.
type STTRecognizer struct {
	name      string
	cassette  STTCassette
	nextIndex int
}

// loadSTTCassetteFromDisk reads tests/voice-cassettes/<name>.yaml and
// returns the decoded cassette. When mustExist is true (replay mode) every
// failure path — missing file, malformed YAML, empty audio_sha256 — is
// fatal. When mustExist is false (record mode), a missing file yields
// (zero, false) because the recorder will write a fresh cassette;
// malformed existing files still fail so a corrupted fixture is never
// silently overwritten.
//
// One function instead of two so neither build configuration (default
// replay vs -tags=record) sees an unused helper — only one of LoadSTT's
// build-tag variants is compiled at a time.
func loadSTTCassetteFromDisk(t *testing.T, name string, mustExist bool) (STTCassette, bool) {
	t.Helper()
	path := filepath.Join(cassettesDir(), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if !mustExist && os.IsNotExist(err) {
			return STTCassette{}, false
		}
		t.Fatalf("voicecassette.LoadSTT(%q): %v", name, err)
	}
	var c STTCassette
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("voicecassette.LoadSTT(%q): unmarshal: %v", name, err)
	}
	if mustExist {
		if len(c.Segments) == 0 {
			t.Fatalf("voicecassette.LoadSTT(%q): cassette has no segments", name)
		}
		for i, seg := range c.Segments {
			if seg.AudioSHA256 == "" {
				t.Fatalf("voicecassette.LoadSTT(%q): segment %d has empty audio_sha256", name, i)
			}
		}
	}
	return c, true
}

// Transcribe implements [stt.Recognizer]. Matches the call positionally
// against the cassette's ordered segments: returns the pinned transcript on
// hash match, otherwise an error that names both hashes — or reports the
// cassette exhausted when called more times than were recorded — so the diff
// is obvious in test output.
func (r *STTRecognizer) Transcribe(_ context.Context, frames []audio.Frame) (stt.Transcript, error) {
	if r.nextIndex >= len(r.cassette.Segments) {
		return stt.Transcript{}, fmt.Errorf(
			"voicecassette: STT cassette %q exhausted at index %d (%d segment(s) recorded); re-record with -tags=record",
			r.name, r.nextIndex, len(r.cassette.Segments),
		)
	}
	seg := r.cassette.Segments[r.nextIndex]
	got := HashFrames(frames)
	if got != seg.AudioSHA256 {
		return stt.Transcript{}, fmt.Errorf(
			"voicecassette: audio hash mismatch for cassette %q at index %d (got %s, recorded %s); re-record with -tags=record",
			r.name, r.nextIndex, got, seg.AudioSHA256,
		)
	}
	r.nextIndex++
	return stt.Transcript{Text: seg.Transcript}, nil
}

// TTSCassette is the on-disk record of one TTS-dispatch sequence.
//
// Per ADR-0021's TTS cassette policy the cassette pins only the dispatched
// sentences — synthesized audio is not fed back to tests. [TTSSynthesizer]
// asserts that each Synthesize call's Sentence matches the next recorded
// entry; a mismatch (or running past the end of the recorded list) fails
// the test and points at the re-record workflow.
type TTSCassette struct {
	// Sentences is the ordered list of sentences the TTS provider is expected
	// to be invoked with for this scenario. Matched positionally against
	// incoming Synthesize calls.
	Sentences []string `yaml:"sentences"`

	// Notes is free-form provenance (provider, model, recording date). Not
	// load-bearing; survives round-trip for human reviewers.
	Notes string `yaml:"notes,omitempty"`
}

// TTSSynthesizer is a [tts.Synthesizer] that replays a single [TTSCassette].
//
// Each call to Synthesize checks the incoming Sentence against the next
// recorded entry; on match it returns an immediately-closed audio channel
// (the orchestrator's drain loop returns at once, per ADR-0022). On mismatch
// — or after the cassette is exhausted — the call returns an error pointing
// the caller at the re-record workflow.
//
// TTSSynthesizer also implements [tts.Synthesizer]'s AudioMarkupPrompt with a
// neutral plain-prose instruction. Tests that need a provider-specific markup
// prompt should construct their own stub.
type TTSSynthesizer struct {
	name      string
	cassette  TTSCassette
	nextIndex int
}

// loadTTSCassetteFromDisk reads tests/voice-cassettes/<name>.yaml and returns
// the decoded cassette. When mustExist is true (replay mode) every failure
// path — missing file, malformed YAML, empty sentences list — is fatal. When
// mustExist is false (record mode), a missing file yields (zero, false)
// because the recorder will write a fresh cassette; malformed existing files
// still fail so a corrupted fixture is never silently overwritten.
//
// One function instead of two so neither build configuration (default replay
// vs -tags=record) sees an unused helper — only one of [LoadTTS]'s build-tag
// variants is compiled at a time.
func loadTTSCassetteFromDisk(t *testing.T, name string, mustExist bool) (TTSCassette, bool) {
	t.Helper()
	path := filepath.Join(cassettesDir(), name+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		if !mustExist && os.IsNotExist(err) {
			return TTSCassette{}, false
		}
		t.Fatalf("voicecassette.LoadTTS(%q): %v", name, err)
	}
	var c TTSCassette
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("voicecassette.LoadTTS(%q): unmarshal: %v", name, err)
	}
	if mustExist && len(c.Sentences) == 0 {
		t.Fatalf("voicecassette.LoadTTS(%q): cassette has empty sentences list", name)
	}
	return c, true
}

// Synthesize implements [tts.Synthesizer]. Returns a closed empty audio
// channel on sentence match; otherwise an error that names both sentences so
// the diff is obvious in test output.
func (r *TTSSynthesizer) Synthesize(_ context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	if r.nextIndex >= len(r.cassette.Sentences) {
		return nil, fmt.Errorf(
			"voicecassette: TTS cassette %q exhausted at index %d (got sentence %q); re-record with -tags=record",
			r.name, r.nextIndex, req.Sentence,
		)
	}
	want := r.cassette.Sentences[r.nextIndex]
	if req.Sentence != want {
		return nil, fmt.Errorf(
			"voicecassette: TTS sentence mismatch for cassette %q at index %d (got %q, recorded %q); re-record with -tags=record",
			r.name, r.nextIndex, req.Sentence, want,
		)
	}
	r.nextIndex++
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

// AudioMarkupPrompt implements [tts.Synthesizer]. Returns a neutral
// plain-prose instruction; the cassette policy does not pin markup.
func (r *TTSSynthesizer) AudioMarkupPrompt(tts.Voice) string {
	return "Speak in plain prose. Do not include bracketed tags or SSML markup."
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
