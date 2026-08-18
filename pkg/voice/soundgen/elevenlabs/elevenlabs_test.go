package elevenlabs_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen/elevenlabs"
)

// The adapter must satisfy the provider-neutral surface.
var _ soundgen.Generator = (*elevenlabs.Client)(nil)

// TestMissingKeyFailsAtRequestTime pins the New-never-fails contract: a keyless
// client links (cassette-replay binaries import this package unconditionally)
// and errors only when a call is attempted.
func TestMissingKeyFailsAtRequestTime(t *testing.T) {
	t.Setenv(elevenlabs.APIKeyEnv, "")
	c := elevenlabs.New("")
	if _, err := c.GenerateSting(context.Background(), soundgen.Request{Prompt: "boom"}); err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("GenerateSting keyless err = %v, want missing-API-key", err)
	}
	if _, err := c.ComposeMusic(context.Background(), soundgen.Request{Prompt: "theme"}); err == nil || !strings.Contains(err.Error(), "missing API key") {
		t.Fatalf("ComposeMusic keyless err = %v, want missing-API-key", err)
	}
}

// TestGenerateStingRequestShape pins the sting call: endpoint, auth header,
// output format, body fields (prompt, clamped duration, model id), and the
// decoded result (bytes, content type, model, vendor character-cost).
func TestGenerateStingRequestShape(t *testing.T) {
	audio := []byte("mp3-sting-bytes")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/sound-generation" {
			t.Errorf("got %s %s, want POST /v1/sound-generation", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("output_format"); got != "mp3_44100_128" {
			t.Errorf("output_format = %q, want mp3_44100_128", got)
		}
		if got := r.Header.Get("xi-api-key"); got != "test-key" {
			t.Errorf("xi-api-key = %q, want test-key", got)
		}
		var body struct {
			Text            string   `json:"text"`
			DurationSeconds *float64 `json:"duration_seconds"`
			ModelID         string   `json:"model_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Text != "a triumphant brass sting" {
			t.Errorf("text = %q", body.Text)
		}
		// 90s clamps to the 22s sting ceiling (#312).
		if body.DurationSeconds == nil || *body.DurationSeconds != 22 {
			t.Errorf("duration_seconds = %v, want 22", body.DurationSeconds)
		}
		if body.ModelID != elevenlabs.SFXModel {
			t.Errorf("model_id = %q, want %q", body.ModelID, elevenlabs.SFXModel)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		w.Header().Set("character-cost", "100")
		_, _ = w.Write(audio)
	}))
	defer srv.Close()

	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(srv.URL))
	res, err := c.GenerateSting(context.Background(), soundgen.Request{
		Prompt:   "a triumphant brass sting",
		Duration: 90 * time.Second,
	})
	if err != nil {
		t.Fatalf("GenerateSting: %v", err)
	}
	if string(res.Data) != string(audio) {
		t.Errorf("Data = %q, want %q", res.Data, audio)
	}
	if res.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q", res.ContentType)
	}
	if res.Model != elevenlabs.SFXModel {
		t.Errorf("Model = %q, want %q", res.Model, elevenlabs.SFXModel)
	}
	if res.CharacterCost != 100 {
		t.Errorf("CharacterCost = %d, want 100", res.CharacterCost)
	}
}

// TestGenerateStingOmitsAutoDuration pins the zero-Duration path: no
// duration_seconds field at all, so the endpoint infers the optimal length.
func TestGenerateStingOmitsAutoDuration(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), "duration_seconds") {
			t.Errorf("body %s carries duration_seconds, want omitted for auto", raw)
		}
		_, _ = w.Write([]byte("x"))
	}))
	defer srv.Close()

	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(srv.URL))
	if _, err := c.GenerateSting(context.Background(), soundgen.Request{Prompt: "boom"}); err != nil {
		t.Fatalf("GenerateSting: %v", err)
	}
}

// TestComposeMusicRequestShape pins the Music call: endpoint, body fields
// (prompt, length in ms, model id, force_instrumental always true), and the
// content-type fallback when the vendor omits the header.
func TestComposeMusicRequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/music" {
			t.Errorf("got %s %s, want POST /v1/music", r.Method, r.URL.Path)
		}
		var body struct {
			Prompt            string `json:"prompt"`
			MusicLengthMs     int64  `json:"music_length_ms"`
			ModelID           string `json:"model_id"`
			ForceInstrumental bool   `json:"force_instrumental"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body.Prompt != "an ominous dungeon theme" {
			t.Errorf("prompt = %q", body.Prompt)
		}
		if body.MusicLengthMs != 45_000 {
			t.Errorf("music_length_ms = %d, want 45000", body.MusicLengthMs)
		}
		if body.ModelID != elevenlabs.MusicModel {
			t.Errorf("model_id = %q, want %q", body.ModelID, elevenlabs.MusicModel)
		}
		if !body.ForceInstrumental {
			t.Errorf("force_instrumental = false, want true")
		}
		w.Header().Set("Content-Type", "application/octet-stream") // genericized: adapter falls back
		_, _ = w.Write([]byte("mp3-music-bytes"))
	}))
	defer srv.Close()

	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(srv.URL))
	res, err := c.ComposeMusic(context.Background(), soundgen.Request{
		Prompt:   "an ominous dungeon theme",
		Duration: 45 * time.Second,
	})
	if err != nil {
		t.Fatalf("ComposeMusic: %v", err)
	}
	if res.ContentType != "audio/mpeg" {
		t.Errorf("ContentType = %q, want the audio/mpeg fallback", res.ContentType)
	}
	if res.Model != elevenlabs.MusicModel {
		t.Errorf("Model = %q, want %q", res.Model, elevenlabs.MusicModel)
	}
}

// TestNon2xxIsTypedHTTPError pins the ADR-0044 contract: a non-2xx start
// response surfaces as *providererr.HTTPError so callers classify by status
// code via errors.As, never substring matching.
func TestNon2xxIsTypedHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"detail":"quota exhausted"}`, http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(srv.URL))
	_, err := c.GenerateSting(context.Background(), soundgen.Request{Prompt: "boom"})
	var httpErr *providererr.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("err = %v (%T), want *providererr.HTTPError", err, err)
	}
	if httpErr.StatusCode != http.StatusTooManyRequests {
		t.Errorf("StatusCode = %d, want 429", httpErr.StatusCode)
	}
	if httpErr.Op != "elevenlabs.GenerateSting" {
		t.Errorf("Op = %q", httpErr.Op)
	}
}

// TestEmptyAudioIsAnError pins that a 200 with no bytes fails loudly instead of
// landing a zero-byte blob on the Highlight.
func TestEmptyAudioIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := elevenlabs.New("test-key", elevenlabs.WithBaseURL(srv.URL))
	if _, err := c.ComposeMusic(context.Background(), soundgen.Request{Prompt: "theme"}); err == nil || !strings.Contains(err.Error(), "no audio") {
		t.Fatalf("err = %v, want no-audio error", err)
	}
}
