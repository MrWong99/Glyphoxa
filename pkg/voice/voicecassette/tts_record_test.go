//go:build record

package voicecassette

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
	"gopkg.in/yaml.v3"
)

// TestTTSRecorder_WriteOnCleanup wires the recorder against an httptest server
// standing in for the ElevenLabs API and asserts the end-to-end record loop:
// dispatched sentences are captured, audio is drained, and the on-disk
// cassette is rewritten at cleanup with the captured ordered list.
//
// Whitebox by necessity — we construct [TTSRecorder] directly to inject a
// fake-base-URL client, which the LoadTTS entry point does not expose.
func TestTTSRecorder_WriteOnCleanup(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 256))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	orig := cassettesDir
	cassettesDir = func() string { return dir }
	t.Cleanup(func() { cassettesDir = orig })

	name := "tts-record-test"
	client := elevenlabs.New("test-key", elevenlabs.WithBaseURL(srv.URL))
	r := &TTSRecorder{
		name:     name,
		client:   client,
		existing: TTSCassette{Notes: "preserved provenance"},
	}

	voice := tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v1"}
	for _, sentence := range []string{"[curious] First sentence.", "[laughs] Second sentence."} {
		ch, err := r.Synthesize(context.Background(), tts.SynthesizeRequest{Sentence: sentence, Voice: voice})
		if err != nil {
			t.Fatalf("Synthesize(%q): %v", sentence, err)
		}
		for range ch {
		}
	}

	if got := calls.Load(); got != 2 {
		t.Errorf("HTTP calls = %d, want 2", got)
	}

	if err := r.write(); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
	if err != nil {
		t.Fatalf("read written cassette: %v", err)
	}
	var got TTSCassette
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal written cassette: %v", err)
	}
	wantSentences := []string{"[curious] First sentence.", "[laughs] Second sentence."}
	if len(got.Sentences) != len(wantSentences) {
		t.Fatalf("Sentences len = %d, want %d", len(got.Sentences), len(wantSentences))
	}
	for i, s := range got.Sentences {
		if s != wantSentences[i] {
			t.Errorf("Sentences[%d] = %q, want %q", i, s, wantSentences[i])
		}
	}
	if !strings.Contains(got.Notes, "preserved provenance") {
		t.Errorf("Notes lost preserved content: %q", got.Notes)
	}
	if !strings.Contains(got.Notes, "Re-recorded against ElevenLabs eleven_v3") {
		t.Errorf("Notes missing provenance stamp: %q", got.Notes)
	}
}

// TestTTSRecorder_NoCallsNoWrite verifies the guard that prevents an empty
// recording (test never dispatched anything) from clobbering the existing
// fixture with an empty sentence list.
func TestTTSRecorder_NoCallsNoWrite(t *testing.T) {
	dir := t.TempDir()
	orig := cassettesDir
	cassettesDir = func() string { return dir }
	t.Cleanup(func() { cassettesDir = orig })

	name := "tts-empty-record"
	r := &TTSRecorder{name: name, client: elevenlabs.New("test-key")}
	if err := r.write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name+".yaml")); !os.IsNotExist(err) {
		t.Errorf("write created file despite zero sentences: err=%v", err)
	}
}
