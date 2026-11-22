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

// voiceOnModel is an ElevenLabs voice whose Settings name model ("" = none).
func voiceOnModel(t *testing.T, model string) tts.Voice {
	t.Helper()
	v := tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"}
	if model != "" {
		raw, err := json.Marshal(elevenlabs.Settings{ModelID: model})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		v.Settings = raw
	}
	return v
}

// TestAudioMarkupPrompt_TagModels_TeachTheTagVocabulary: the v3/v4 families
// (the default eleven_v4_turbo included) get the bracketed audio-tag vocabulary,
// worded without naming one model, and pauses are taught as tags — never SSML
// <break>, which eleven_v4_turbo silently strips (#702, measured).
func TestAudioMarkupPrompt_TagModels_TeachTheTagVocabulary(t *testing.T) {
	c := elevenlabs.New("k")
	for _, model := range []string{"", elevenlabs.ModelV4Turbo, elevenlabs.ModelV3, "eleven_v4"} {
		t.Run("model="+model, func(t *testing.T) {
			got := c.AudioMarkupPrompt(voiceOnModel(t, model))
			if got == "" {
				t.Fatal("AudioMarkupPrompt returned empty string; contract requires non-empty per ADR-0022")
			}
			for _, must := range []string{"square brackets", "[whispers]", "[pause]", "[laughs]", "NOT SSML"} {
				if !strings.Contains(got, must) {
					t.Errorf("AudioMarkupPrompt missing required hint %q\nfull prompt: %s", must, got)
				}
			}
			for _, banned := range []string{"eleven_v3", "eleven_v4", "<break"} {
				if strings.Contains(got, banned) {
					t.Errorf("AudioMarkupPrompt contains %q; the tag wording must be model-neutral and never teach SSML breaks\nfull prompt: %s", banned, got)
				}
			}
		})
	}
}

// TestAudioMarkupPrompt_NonTagModels_PlainProse: a voice pinned to a model with
// no audio-tag support (flash, multilingual v2, turbo v2.5) must not be taught
// tags — they would be read aloud — and its per-voice tag preferences are moot.
func TestAudioMarkupPrompt_NonTagModels_PlainProse(t *testing.T) {
	c := elevenlabs.New("k")
	for _, model := range []string{"eleven_flash_v2_5", "eleven_multilingual_v2", "eleven_turbo_v2_5"} {
		t.Run(model, func(t *testing.T) {
			s := elevenlabs.Settings{ModelID: model, SuggestedAudioTags: []string{"confident"}}
			raw, err := json.Marshal(s)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			got := c.AudioMarkupPrompt(tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v", Settings: raw})
			if got == "" {
				t.Fatal("AudioMarkupPrompt returned empty string; contract requires non-empty per ADR-0022 (\"use plain prose only\")")
			}
			if !strings.Contains(got, "plain") {
				t.Errorf("non-tag model prompt should ask for plain prose; got %q", got)
			}
			for _, banned := range []string{"[whispers]", "[pause]", "[laughs]", "[confident]", "<break"} {
				if strings.Contains(got, banned) {
					t.Errorf("non-tag model prompt teaches %q; tags are read aloud on %s\nfull prompt: %s", banned, model, got)
				}
			}
		})
	}
}

func TestAudioMarkupPrompt_SuggestedTags_AppendedAsPreference(t *testing.T) {
	settings := elevenlabs.DefaultSettings()
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

// TestSynthesize_BodyCarriesSentenceAndDefaultModel pins the #702 default: a
// Voice with no model_id synthesizes on eleven_v4_turbo (~135 ms first audio over
// this same HTTP /stream path, against ~600 ms for eleven_v3, at half the credits).
func TestSynthesize_BodyCarriesSentenceAndDefaultModel(t *testing.T) {
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
	if parsed["model_id"] != "eleven_v4_turbo" {
		t.Errorf("body.model_id = %v, want %q", parsed["model_id"], "eleven_v4_turbo")
	}
	if _, ok := parsed["optimize_streaming_latency"]; ok {
		t.Error("body carries optimize_streaming_latency; eleven_v4_turbo rejects it with 400")
	}
	if strings.Contains(gotURL, "optimize_streaming_latency") {
		t.Errorf("request URL = %q carries optimize_streaming_latency; eleven_v4_turbo rejects it with 400", gotURL)
	}
	if !strings.Contains(gotURL, "/v1/text-to-speech/voice-xyz/stream") {
		t.Errorf("request URL = %q, missing /v1/text-to-speech/{voice_id}/stream", gotURL)
	}
	if !strings.Contains(gotURL, "output_format=pcm_24000") {
		t.Errorf("request URL = %q, missing output_format=pcm_24000", gotURL)
	}
}

// TestModelOf_ResolvesTheModelSynthesizeSends pins the one model-selection seam
// metering prices on: a voice's explicit model_id wins, and a voice naming none
// (or carrying undecodable Settings) resolves to the adapter default.
func TestModelOf_ResolvesTheModelSynthesizeSends(t *testing.T) {
	cases := []struct {
		name     string
		settings string
		want     string
	}{
		{"no settings", "", elevenlabs.ModelV4Turbo},
		{"settings without model", `{"output_format":"pcm_48000"}`, elevenlabs.ModelV4Turbo},
		{"explicit v3", `{"model_id":"eleven_v3"}`, elevenlabs.ModelV3},
		{"explicit flash", `{"model_id":"eleven_flash_v2_5"}`, "eleven_flash_v2_5"},
		{"undecodable", `{"model_id":`, elevenlabs.ModelV4Turbo},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v", Settings: json.RawMessage(tc.settings)}
			if got := elevenlabs.ModelOf(v); got != tc.want {
				t.Errorf("ModelOf(%s) = %q, want %q", tc.settings, got, tc.want)
			}
		})
	}
}

func TestDefaultSettings_RoundTripsThroughJSON(t *testing.T) {
	in := elevenlabs.DefaultSettings()
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out elevenlabs.Settings
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.ModelID != elevenlabs.ModelV4Turbo {
		t.Errorf("ModelID = %q, want %q", out.ModelID, elevenlabs.ModelV4Turbo)
	}
	if out.OutputFormat != elevenlabs.DefaultOutputFormat {
		t.Errorf("OutputFormat = %q, want %q", out.OutputFormat, elevenlabs.DefaultOutputFormat)
	}
	if out.VoiceSettings == nil || out.VoiceSettings.Stability == nil || out.VoiceSettings.SimilarityBoost == nil {
		t.Fatalf("VoiceSettings did not round-trip: %+v", out.VoiceSettings)
	}
}

// TestSynthesize_MidStreamFailure_EmitsTerminalErrChunk pins the #436 adapter
// contract: a response body that dies mid-stream (the server promises more bytes
// than it delivers, then drops the connection) surfaces as ONE terminal
// [tts.AudioChunk] with Err set, after the successfully-read audio chunks and
// immediately before the close — so the dispatch layer can refuse to commit the
// truncated sentence.
func TestSynthesize_MidStreamFailure_EmitsTerminalErrChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/pcm")
		// Promise more than is written: the handler returning early makes the
		// client's next read fail (unexpected EOF) — a provider dying mid-stream.
		w.Header().Set("Content-Length", "8192")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(make([]byte, 1024))
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	ch, err := c.Synthesize(context.Background(), tts.SynthesizeRequest{
		Sentence: "hello",
		Voice:    tts.Voice{ProviderID: elevenlabs.ProviderID, VoiceID: "v"},
	})
	if err != nil {
		t.Fatalf("Synthesize: %v (the start succeeded; the failure is mid-stream)", err)
	}

	var audioBytes int
	var terminal *tts.AudioChunk
	for chunk := range ch {
		if chunk.Err != nil {
			if terminal != nil {
				t.Fatal("more than one terminal Err chunk emitted")
			}
			cp := chunk
			terminal = &cp
			continue
		}
		if terminal != nil {
			t.Fatal("audio chunk emitted AFTER the terminal Err chunk; Err must be the stream's last element")
		}
		audioBytes += len(chunk.PCM)
	}
	if terminal == nil {
		t.Fatal("stream died mid-delivery but no terminal Err chunk was emitted (#436)")
	}
	if len(terminal.PCM) != 0 {
		t.Errorf("terminal Err chunk carries %d PCM bytes, want none (it is a signal, not audio)", len(terminal.PCM))
	}
	if audioBytes != 1024 {
		t.Errorf("audio bytes before the failure = %d, want the 1024 the server actually sent", audioBytes)
	}
}

// TestSynthesize_ContextCancel_NoTerminalErrChunk pins the #436 carve-out: a ctx
// cancellation (barge-in) closes the stream WITHOUT a terminal Err chunk — the
// caller cut the stream deliberately, and reporting it as a provider failure
// would turn every barge into a vendor fault.
func TestSynthesize_ContextCancel_NoTerminalErrChunk(t *testing.T) {
	released := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "audio/pcm")
		w.WriteHeader(http.StatusOK)
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
	var sawErrChunk bool
	go func() {
		for chunk := range ch {
			if chunk.Err != nil {
				sawErrChunk = true
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after ctx cancel")
	}
	if sawErrChunk {
		t.Error("a barge-cancelled stream emitted a terminal Err chunk; a caller cut is not a provider failure")
	}
}
