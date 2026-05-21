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

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
	"gopkg.in/yaml.v3"
)

// TestSTTRecorder_WriteOnCleanup wires the recorder against an httptest
// server standing in for the ElevenLabs API and asserts the end-to-end
// record loop: dispatched frames are forwarded, the live transcript is
// captured, and the on-disk cassette is rewritten at cleanup with the
// captured (audio_sha256, transcript) pair plus preserved Notes + a
// provenance stamp.
//
// Whitebox by necessity — we construct [STTRecorder] directly to inject a
// fake-base-URL client, which the LoadSTT entry point does not expose.
func TestSTTRecorder_WriteOnCleanup(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"live transcription"}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	orig := cassettesDir
	cassettesDir = func() string { return dir }
	t.Cleanup(func() { cassettesDir = orig })

	name := "stt-record-test"
	client := elevenlabs.New("test-key", elevenlabs.WithBaseURL(srv.URL))
	r := &STTRecorder{
		name:     name,
		client:   client,
		existing: STTCassette{Notes: "preserved provenance"},
	}

	samples := make([]int16, 512)
	for i := range samples {
		samples[i] = int16(i)
	}
	frame, err := audio.NewFrame(samples, 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	frames := []audio.Frame{frame}
	wantHash := HashFrames(frames)

	tr, err := r.Transcribe(context.Background(), frames)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if tr.Text != "live transcription" {
		t.Errorf("Transcript.Text = %q, want %q", tr.Text, "live transcription")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("HTTP calls = %d, want 1", got)
	}

	if err := r.write(); err != nil {
		t.Fatalf("write: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, name+".yaml"))
	if err != nil {
		t.Fatalf("read written cassette: %v", err)
	}
	var got STTCassette
	if err := yaml.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal written cassette: %v", err)
	}
	if got.AudioSHA256 != wantHash {
		t.Errorf("AudioSHA256 = %q, want %q", got.AudioSHA256, wantHash)
	}
	if got.Transcript != "live transcription" {
		t.Errorf("Transcript = %q, want %q", got.Transcript, "live transcription")
	}
	if !strings.Contains(got.Notes, "preserved provenance") {
		t.Errorf("Notes lost preserved content: %q", got.Notes)
	}
	if !strings.Contains(got.Notes, "Re-recorded against ElevenLabs scribe_v2") {
		t.Errorf("Notes missing provenance stamp: %q", got.Notes)
	}
}

// TestSTTRecorder_NoCallsNoWrite verifies the guard that prevents an empty
// recording (test never invoked the recognizer) from clobbering the
// existing fixture with an empty audio_sha256.
func TestSTTRecorder_NoCallsNoWrite(t *testing.T) {
	dir := t.TempDir()
	orig := cassettesDir
	cassettesDir = func() string { return dir }
	t.Cleanup(func() { cassettesDir = orig })

	name := "stt-empty-record"
	r := &STTRecorder{name: name, client: elevenlabs.New("test-key")}
	if err := r.write(); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, name+".yaml")); !os.IsNotExist(err) {
		t.Errorf("write created file despite zero captures: err=%v", err)
	}
}
