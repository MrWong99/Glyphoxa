//go:build record

package voicecassette

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"gopkg.in/yaml.v3"
)

// LoadTTS in -tags=record builds returns a [TTSRecorder] that proxies every
// Synthesize call to a live ElevenLabs eleven_v3 client, captures the
// dispatched sentence, and rewrites tests/voice-cassettes/<name>.yaml at
// test cleanup with the captured ordered list. The ELEVENLABS_API_KEY
// environment variable supplies credentials.
//
// Per ADR-0021's TTS cassette policy only the dispatched sentences are
// persisted; rendered audio is drained and discarded. Any existing cassette's
// Notes field is preserved (with a "recorded against eleven_v3 on <date>"
// provenance line appended) so reviewer-facing context survives the refresh.
func LoadTTS(t *testing.T, name string) tts.Synthesizer {
	t.Helper()
	existing, _ := loadTTSCassetteFromDisk(t, name, false)
	r := &TTSRecorder{
		name:     name,
		client:   elevenlabs.New(""),
		existing: existing,
	}
	t.Cleanup(func() {
		if err := r.write(); err != nil {
			t.Fatalf("voicecassette.LoadTTS(%q): record write: %v", name, err)
		}
	})
	return r
}

// TTSRecorder is the -tags=record counterpart to [TTSSynthesizer]: it
// forwards every Synthesize call to a live [elevenlabs.Client] and captures
// the dispatched sentence so the cassette can be rewritten at test cleanup.
//
// Per ADR-0021 the TTS cassette pins only the ordered sentence list — the
// rendered PCM is drained and discarded here, matching what the orchestrator
// does in production.
type TTSRecorder struct {
	name      string
	client    *elevenlabs.Client
	existing  TTSCassette
	sentences []string
}

// Synthesize implements [tts.Synthesizer]. Forwards to the live client,
// captures req.Sentence, drains the response synchronously (so a subsequent
// Dispatch from the same test sees a settled state), and returns an
// already-closed channel — the orchestrator's drain loop unblocks
// immediately.
func (r *TTSRecorder) Synthesize(ctx context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	in, err := r.client.Synthesize(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("voicecassette: TTSRecorder live Synthesize for cassette %q: %w", r.name, err)
	}
	for range in {
	}
	r.sentences = append(r.sentences, req.Sentence)
	out := make(chan tts.AudioChunk)
	close(out)
	return out, nil
}

// AudioMarkupPrompt implements [tts.Synthesizer]; delegates to the live
// client so the LLM sees the same v3 tag vocabulary in record mode as in
// production.
func (r *TTSRecorder) AudioMarkupPrompt(voice tts.Voice) string {
	return r.client.AudioMarkupPrompt(voice)
}

// write serialises the captured sentences (plus preserved Notes + a fresh
// provenance line) to tests/voice-cassettes/<name>.yaml. A no-op if
// Synthesize was never called — recording a test that never dispatched a
// sentence would clobber the existing fixture with an empty list.
func (r *TTSRecorder) write() error {
	if len(r.sentences) == 0 {
		return nil
	}
	out := TTSCassette{
		Sentences: r.sentences,
		Notes:     r.existing.Notes,
	}
	stamp := time.Now().UTC().Format("2006-01-02")
	provenance := fmt.Sprintf("Re-recorded against ElevenLabs eleven_v3 on %s.", stamp)
	if out.Notes == "" {
		out.Notes = provenance
	} else {
		out.Notes = out.Notes + "\n\n" + provenance
	}
	data, err := yaml.Marshal(out)
	if err != nil {
		return fmt.Errorf("marshal cassette: %w", err)
	}
	path := filepath.Join(cassettesDir(), r.name+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
