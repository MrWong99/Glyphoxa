package elevenlabs_test

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/retry"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt/elevenlabs"
)

// frame16k32ms is a helper that builds one [audio.Frame] of 16 kHz / 32 ms
// mono PCM filled with the supplied samples (length must be 512).
func frame16k32ms(t *testing.T, samples []int16) audio.Frame {
	t.Helper()
	f, err := audio.NewFrame(samples, 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// Compile-time assertion: [elevenlabs.Client] satisfies [stt.Recognizer],
// which is the only contract the orchestrator's STT stage depends on.
var _ stt.Recognizer = (*elevenlabs.Client)(nil)

// TestNew_NoKey_NoEnv_TranscribeReturnsMissingKeyError pins the same
// link-time-safety property the TTS adapter has: New must not panic without
// an API key (cassette-replay test binaries link this package
// unconditionally); the missing-key error surfaces at the first request.
func TestNew_NoKey_NoEnv_TranscribeReturnsMissingKeyError(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "")
	c := elevenlabs.New("")
	_, err := c.Transcribe(context.Background(), nil)
	if err == nil {
		t.Fatal("Transcribe without API key returned nil error")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("error %q does not mention missing API key", err)
	}
}

// TestTranscribe_HappyPath_ReturnsResponseText pins the smallest end-to-end
// loop: the adapter posts to /v1/speech-to-text and decodes the response's
// "text" field into [stt.Transcript.Text]. Request-shape assertions live in
// a separate test (TB3); this one cares only about the response decode.
func TestTranscribe_HappyPath_ReturnsResponseText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"language_code":"en","language_probability":0.98,"text":"hello world"}`))
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	frames := []audio.Frame{frame16k32ms(t, make([]int16, 512))}

	tr, err := c.Transcribe(context.Background(), frames)
	if err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if tr.Text != "hello world" {
		t.Errorf("Transcript.Text = %q, want %q", tr.Text, "hello world")
	}
}

// TestTranscribe_RequestShape_PinsMultipartAndRawPCMBytes is the
// adapter↔ElevenLabs API contract: the request must carry the BYOK key in
// xi-api-key, target scribe_v2, declare file_format=pcm_s16le_16, and the
// `file` part bytes must be the concatenated little-endian int16 sample
// stream of the input frames — no WAV wrap, no re-ordering.
//
// Pinning this in a test (rather than only in code) catches accidental
// drift on either side: if a future refactor wraps the body as WAV or
// switches to scribe_v1, this fires.
func TestTranscribe_RequestShape_PinsMultipartAndRawPCMBytes(t *testing.T) {
	// Two distinguishable frames so byte-order matters: frame 0 = 0..511,
	// frame 1 = 1000..1511. Concatenated LE int16 stream is what we expect
	// to see in the `file` part.
	const samplesPerFrame = 512
	mk := func(start int) []int16 {
		s := make([]int16, samplesPerFrame)
		for i := range s {
			s[i] = int16(start + i)
		}
		return s
	}
	frames := []audio.Frame{
		frame16k32ms(t, mk(0)),
		frame16k32ms(t, mk(1000)),
	}

	var wantPCM []byte
	for _, f := range frames {
		for _, s := range f.Samples() {
			var buf [2]byte
			binary.LittleEndian.PutUint16(buf[:], uint16(s))
			wantPCM = append(wantPCM, buf[:]...)
		}
	}

	var sawAPIKey atomic.Value
	var sawModel atomic.Value
	var sawFileFormat atomic.Value
	var sawFileBytes atomic.Value

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/v1/speech-to-text" {
			t.Errorf("path = %q, want /v1/speech-to-text", r.URL.Path)
		}
		sawAPIKey.Store(r.Header.Get("xi-api-key"))

		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if vs := r.MultipartForm.Value["model_id"]; len(vs) == 1 {
			sawModel.Store(vs[0])
		}
		if vs := r.MultipartForm.Value["file_format"]; len(vs) == 1 {
			sawFileFormat.Store(vs[0])
		}
		files := r.MultipartForm.File["file"]
		if len(files) != 1 {
			t.Fatalf("multipart files[\"file\"] len = %d, want 1", len(files))
		}
		fh, err := files[0].Open()
		if err != nil {
			t.Fatalf("file part open: %v", err)
		}
		defer fh.Close()
		got, err := io.ReadAll(fh)
		if err != nil {
			t.Fatalf("file part read: %v", err)
		}
		sawFileBytes.Store(got)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"ok"}`))
	}))
	defer srv.Close()

	c := elevenlabs.New("expected-key", elevenlabs.WithBaseURL(srv.URL))
	if _, err := c.Transcribe(context.Background(), frames); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	if got, _ := sawAPIKey.Load().(string); got != "expected-key" {
		t.Errorf("xi-api-key = %q, want %q", got, "expected-key")
	}
	if got, _ := sawModel.Load().(string); got != "scribe_v2" {
		t.Errorf("model_id = %q, want %q", got, "scribe_v2")
	}
	if got, _ := sawFileFormat.Load().(string); got != "pcm_s16le_16" {
		t.Errorf("file_format = %q, want %q", got, "pcm_s16le_16")
	}
	gotBytes, _ := sawFileBytes.Load().([]byte)
	if len(gotBytes) != len(wantPCM) {
		t.Fatalf("file bytes len = %d, want %d", len(gotBytes), len(wantPCM))
	}
	for i := range gotBytes {
		if gotBytes[i] != wantPCM[i] {
			t.Fatalf("file bytes differ at offset %d: got 0x%02x want 0x%02x", i, gotBytes[i], wantPCM[i])
		}
	}
}

// TestNew_EnvFallback_HeaderCarriesEnvKey mirrors the TTS adapter:
// New("") consults ELEVENLABS_API_KEY and the resolved key reaches the
// outbound request's xi-api-key header.
func TestNew_EnvFallback_HeaderCarriesEnvKey(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "env-key-abc")

	var seenKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey.Store(r.Header.Get("xi-api-key"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":""}`))
	}))
	defer srv.Close()

	c := elevenlabs.New("", elevenlabs.WithBaseURL(srv.URL))
	if _, err := c.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got, _ := seenKey.Load().(string); got != "env-key-abc" {
		t.Errorf("xi-api-key header = %q, want %q", got, "env-key-abc")
	}
}

// TestNew_ExplicitKeyWinsOverEnv pins the precedence rule: an explicit key
// passed to New beats whatever ELEVENLABS_API_KEY is set to.
func TestNew_ExplicitKeyWinsOverEnv(t *testing.T) {
	t.Setenv("ELEVENLABS_API_KEY", "env-key")

	var seenKey atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenKey.Store(r.Header.Get("xi-api-key"))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":""}`))
	}))
	defer srv.Close()

	c := elevenlabs.New("explicit-key", elevenlabs.WithBaseURL(srv.URL))
	if _, err := c.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}
	if got, _ := seenKey.Load().(string); got != "explicit-key" {
		t.Errorf("xi-api-key header = %q, want %q", got, "explicit-key")
	}
}

// TestTranscribe_Non2xx_WrapsOpAndStatus pins the error-surface shape:
// a non-2xx response yields an error that names the operation and the HTTP
// status, with a snippet of the body for diagnostic context. Test harnesses
// and on-call reviewers grep for this shape, so it is a contract.
func TestTranscribe_Non2xx_WrapsOpAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"invalid_api_key"}`))
	}))
	defer srv.Close()

	c := elevenlabs.New("bad-key", elevenlabs.WithBaseURL(srv.URL))
	_, err := c.Transcribe(context.Background(), nil)
	if err == nil {
		t.Fatal("Transcribe with 401 returned nil error")
	}
	got := err.Error()
	for _, must := range []string{"Transcribe", "401", "invalid_api_key"} {
		if !strings.Contains(got, must) {
			t.Errorf("error %q missing required substring %q", got, must)
		}
	}
}

// TestTranscribe_429_TypedHTTPError pins that a non-2xx surfaces as an
// errors.As-able [*providererr.HTTPError] the retry helper classifies (a 429 is
// retryable), and that the error text stays byte-identical to the adapter's
// pre-typed readErrorResponse literal (#124, ADR-0044).
func TestTranscribe_429_TypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("slow down"))
	}))
	defer srv.Close()

	c := elevenlabs.New("k", elevenlabs.WithBaseURL(srv.URL))
	_, err := c.Transcribe(context.Background(), nil)
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
	const want = "elevenlabs.Transcribe: HTTP 429 429 Too Many Requests: slow down"
	if err.Error() != want {
		t.Errorf("error text = %q, want %q (byte-identical)", err.Error(), want)
	}
}
