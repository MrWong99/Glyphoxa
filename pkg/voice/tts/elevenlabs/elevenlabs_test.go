package elevenlabs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts/elevenlabs"
)

// Compile-time assertions: [elevenlabs.Client] satisfies the full v1.0
// ElevenLabs capability matrix declared in ADR-0023.
var (
	_ tts.Synthesizer         = (*elevenlabs.Client)(nil)
	_ tts.VoiceLister         = (*elevenlabs.Client)(nil)
	_ tts.VoiceCloner         = (*elevenlabs.Client)(nil)
	_ tts.VoiceDesigner       = (*elevenlabs.Client)(nil)
	_ tts.DialogueSynthesizer = (*elevenlabs.Client)(nil)
)

func TestNew_NoKey_NoEnv_SynthesizeReturnsMissingKeyError(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "")
	c := elevenlabs.New("")
	_, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "[whispers] hello",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v1"},
	})
	if err == nil {
		t.Fatal("Synthesize without API key returned nil error")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error %q does not mention missing API key", err)
	}
}

func TestNew_EnvFallback_HeaderCarriesEnvKey(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "env-key-abc")

	var seenKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey.Store(r.Header.Get("xi-api-key"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 4))
	}))
	defer srv.Close()

	c := elevenlabs.New("", elevenlabs.WithBaseURL(srv.URL))
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "hello",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v1"},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	for range ch {
	}
	if got, _ := seenKey.Load().(string); got != "env-key-abc" {
		t.Errorf("xi-api-key header = %q, want %q", got, "env-key-abc")
	}
}

func TestNew_ExplicitKeyWinsOverEnv(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "env-key")

	var seenKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey.Store(r.Header.Get("xi-api-key"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := elevenlabs.New("explicit-key", elevenlabs.WithBaseURL(srv.URL))
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "hello",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v1"},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	for range ch {
	}
	if got, _ := seenKey.Load().(string); got != "explicit-key" {
		t.Errorf("xi-api-key header = %q, want %q", got, "explicit-key")
	}
}

func TestAudioMarkupPrompt_NonEmpty_DescribesV3Tags(t *testing.T) {
	c := elevenlabs.New("k")
	got := c.AudioMarkupPrompt(tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"})
	if got == "" {
		t.Fatal("AudioMarkupPrompt returned empty string; contract requires non-empty per ADR-0022")
	}
	// Spot-check that the prompt actually teaches v3 conventions rather
	// than a generic "be expressive" line.
	for _, must := range []string{"eleven_v3", "square brackets", "[whispers]", "[pause]", "[laughs]"} {
		if !strings.Contains(got, must) {
			t.Errorf("AudioMarkupPrompt missing required hint %q\nfull prompt: %s", must, got)
		}
	}
	if strings.Contains(got, "SSML") && !strings.Contains(got, "NOT SSML") {
		t.Errorf("AudioMarkupPrompt should disclaim SSML, not encourage it; got %q", got)
	}
}

func TestAudioMarkupPrompt_SuggestedTags_AppendedAsPreference(t *testing.T) {
	settings := elevenlabs.DefaultV3Settings()
	settings.SuggestedAudioTags = []string{"confident", "curious"}
	raw, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	c := elevenlabs.New("k")
	got := c.AudioMarkupPrompt(tts.Voice{
		ProviderID: elevenlabs.ProviderID,
		VoiceID:    "v",
		Settings:   raw,
	})
	if !strings.Contains(got, "[confident]") || !strings.Contains(got, "[curious]") {
		t.Errorf("AudioMarkupPrompt did not surface suggested tags; got: %s", got)
	}
}

func TestSynthesize_StreamsPCMChunks_AndClosesOnEOF(t *testing.T) {
	// Server emits 10 KiB of "PCM" (zeros) in three flushed writes.
	const totalBytes = 10 * 1024
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Errorf("ResponseWriter is not a Flusher")
		}
		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(http.StatusOK)
		written := 0
		for _, sz := range []int{3000, 3500, totalBytes - 3000 - 3500} {
			_, _ = w.Write(make([]byte, sz))
			written += sz
			if ok {
				flusher.Flush()
			}
		}
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "hello",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	var got int
	chunks := 0
	for chunk := range ch {
		chunks++
		if chunk.SampleRate != 24000 {
			t.Errorf("chunk SampleRate = %d, want 24000 (DefaultOutputFormat=pcm_24000)", chunk.SampleRate)
		}
		if chunk.Channels != 1 {
			t.Errorf("chunk Channels = %d, want 1", chunk.Channels)
		}
		if len(chunk.PCM)%2 != 0 {
			t.Errorf("chunk PCM length %d is odd; int16 PCM must be even", len(chunk.PCM))
		}
		got += len(chunk.PCM)
	}
	if got != totalBytes {
		t.Errorf("total streamed PCM = %d, want %d", got, totalBytes)
	}
	if chunks < 2 {
		t.Errorf("expected ≥2 chunks across flush boundaries, got %d", chunks)
	}
}

func TestSynthesize_ContextCancel_ClosesChannelEarly(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(http.StatusOK)
		// Emit one initial chunk so the client side sees data, then block.
		_, _ = w.Write(make([]byte, 1024))
		if flusher != nil {
			flusher.Flush()
		}
		<-released
	}))
	defer srv.Close()
	defer close(released)

	ctx, cancel := context.WithCancel(context.Background())
	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	ch, err := c.Synthesize(ctx, tts.SynthesizeRequest{
		Sentence: "hello",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}

	// Consume the first chunk, then cancel; the channel must close.
	select {
	case _, ok := <-ch:
		if !ok {
			t.Fatal("channel closed before first chunk arrived")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}
	cancel()

	done := make(chan struct{})
	go func() {
		for range ch {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
}

func TestSynthesize_NonOKResponse_ReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":{"status":"missing_permissions"}}`)
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	_, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "hi",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"},
	})
	if err == nil {
		t.Fatal("Synthesize against 401 returned nil error")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error %q does not mention HTTP 401", err)
	}
}

// TestSynthesize_429_TypedHTTPError pins that a non-2xx surfaces as an
// errors.As-able [*providererr.HTTPError] the retry helper classifies (a 429 is
// retryable), with the error text byte-identical to the adapter's pre-typed
// readErrorResponse literal (#124, ADR-0044).
func TestSynthesize_429_TypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, "slow down")
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	_, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "hi",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"},
	})
	var he *providererr.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("error %v is not a *providererr.HTTPError", err)
	}
	if he.StatusCode != 429 {
		t.Errorf("StatusCode = %d, want 429", he.StatusCode)
	}
	if !retry.Retryable(err) {
		t.Error("a 429 must be retryable")
	}
	const want = "elevenlabs.Synthesize: HTTP 429 429 Too Many Requests: slow down"
	if err.Error() != want {
		t.Errorf("error text = %q, want %q (byte-identical)", err.Error(), want)
	}
}

func TestSynthesize_MissingVoiceID_FailsBeforeNetwork(t *testing.T) {
	var hit atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Store(true)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	_, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "hi",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID},
	})
	if err == nil {
		t.Fatal("Synthesize with empty VoiceID returned nil error")
	}
	if hit.Load() {
		t.Error("Synthesize with empty VoiceID hit the network; validation should be local")
	}
}

func TestSynthesize_BodyCarriesSentenceAndModelV3(t *testing.T) {
	var (
		mu      sync.Mutex
		gotBody []byte
		gotURL  string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		gotURL = r.URL.String()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	const sentence = "[whispers] Roll a perception check, [pause] please."
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: sentence,
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "voice-xyz"},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	for range ch {
	}

	mu.Lock()
	defer mu.Unlock()
	var parsed map[string]any
	if err := json.Unmarshal(gotBody, &parsed); err != nil {
		t.Fatalf("decode captured body: %v\nbody: %s", err, gotBody)
	}
	if parsed["text"] != sentence {
		t.Errorf("body.text = %v, want %q (v3 brackets must pass through verbatim)", parsed["text"], sentence)
	}
	if parsed["model_id"] != elevenlabs.ModelV3 {
		t.Errorf("body.model_id = %v, want %q", parsed["model_id"], elevenlabs.ModelV3)
	}
	if !strings.Contains(gotURL, "/v1/text-to-speech/voice-xyz/stream") {
		t.Errorf("request URL = %q, missing /v1/text-to-speech/{voice_id}/stream", gotURL)
	}
	if !strings.Contains(gotURL, "output_format=pcm_24000") {
		t.Errorf("request URL = %q, missing output_format=pcm_24000", gotURL)
	}
}

func TestDefaultV3Settings_RoundTripsThroughJSON(t *testing.T) {
	in := elevenlabs.DefaultV3Settings()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out elevenlabs.Settings
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ModelID != elevenlabs.ModelV3 {
		t.Errorf("ModelID = %q, want %q", out.ModelID, elevenlabs.ModelV3)
	}
	if out.OutputFormat != elevenlabs.DefaultOutputFormat {
		t.Errorf("OutputFormat = %q, want %q", out.OutputFormat, elevenlabs.DefaultOutputFormat)
	}
	if out.VoiceSettings == nil || out.VoiceSettings.Stability == nil || out.VoiceSettings.SimilarityBoost == nil {
		t.Fatalf("VoiceSettings did not round-trip: %+v", out.VoiceSettings)
	}
}
