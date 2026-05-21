//go:build record

package voicecassette

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	"gopkg.in/yaml.v3"
)

// LoadSTT in -tags=record builds returns an [STTRecorder] that proxies
// every Transcribe call to a live ElevenLabs scribe_v2 client, captures
// the resulting transcript, and rewrites tests/voice-cassettes/<name>.yaml
// at test cleanup with the captured (audio_sha256, transcript) pair. The
// ELEVENLABS_API_KEY environment variable supplies credentials.
//
// Any existing cassette's Notes field and leading header comments are
// preserved (with an idempotent dated "Re-recorded against ElevenLabs
// scribe_v2 on <date>" provenance line appended) so reviewer-facing context
// survives the refresh.
func LoadSTT(t *testing.T, name string) stt.Recognizer {
	t.Helper()
	existing, _ := loadSTTCassetteFromDisk(t, name, false)
	r := &STTRecorder{
		name:     name,
		client:   elevenlabs.New(""),
		existing: existing,
	}
	t.Cleanup(func() {
		if err := r.write(); err != nil {
			t.Fatalf("voicecassette.LoadSTT(%q): record write: %v", name, err)
		}
	})
	return r
}

// STTRecorder is the -tags=record counterpart to [STTRecognizer]: it
// forwards every Transcribe call to a live [elevenlabs.Client] and
// captures the (frame hash, returned text) pair so the cassette can be
// rewritten at test cleanup.
//
// One cassette pins one utterance, so calling Transcribe multiple times
// against the same recorder overwrites — the last call wins, matching the
// fact that replay mode also re-hashes-and-compares on each call without
// tracking position.
type STTRecorder struct {
	name       string
	client     *elevenlabs.Client
	existing   STTCassette
	hash       string
	transcript string
	captured   bool
}

// Transcribe implements [stt.Recognizer]. Forwards to the live client,
// captures the frame hash and the returned transcript text, and returns
// the live result so the test under record mode exercises the orchestrator
// against real provider output.
func (r *STTRecorder) Transcribe(ctx context.Context, frames []audio.Frame) (stt.Transcript, error) {
	tr, err := r.client.Transcribe(ctx, frames)
	if err != nil {
		return stt.Transcript{}, fmt.Errorf("voicecassette: STTRecorder live Transcribe for cassette %q: %w", r.name, err)
	}
	r.hash = HashFrames(frames)
	r.transcript = tr.Text
	r.captured = true
	return tr, nil
}

// write serialises the captured (hash, transcript) pair to
// tests/voice-cassettes/<name>.yaml, preserving the existing Notes (with an
// idempotent dated provenance line, see appendProvenance) and re-prepending
// the hand-authored header comment block that yaml.Marshal drops (see
// leadingComment). A no-op if Transcribe was never called — recording a test
// that never invoked the recognizer would clobber the existing fixture with
// an empty audio_sha256.
func (r *STTRecorder) write() error {
	if !r.captured {
		return nil
	}
	out := STTCassette{
		AudioSHA256: r.hash,
		Transcript:  r.transcript,
		Notes:       appendProvenance(r.existing.Notes, "scribe_v2"),
	}
	body, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal cassette: %w", err)
	}
	data := append([]byte(leadingComment(r.name)), body...)
	path := filepath.Join(cassettesDir(), r.name+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
