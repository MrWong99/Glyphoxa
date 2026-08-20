package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/providererr"
	"github.com/MrWong99/Glyphoxa/pkg/voice/soundgen"
)

// outputFormat is the encoded-audio format both endpoints are asked for:
// browser-playable MP3 at a plan-safe bitrate (higher bitrates and PCM are
// tier-gated). The bytes go to the blob seam and an <audio> tag, never the PCM
// hot path (ADR-0022 posture).
const outputFormat = "mp3_44100_128"

// Endpoint bounds, per the vendor API contract (captured 2026-08-18). The
// adapter clamps rather than errors: the caller's duration is derived from a
// Highlight clip, and a clip outside the endpoint's range should still produce
// the closest legal asset, not a dead-letter.
const (
	// stingMinDuration / stingMaxDuration bound the sound-effects endpoint's
	// duration_seconds. The endpoint accepts up to 30s, but the sting contract
	// is ≤22s (#312 decision), so the adapter caps there.
	stingMinDuration = 500 * time.Millisecond
	stingMaxDuration = 22 * time.Second
	// musicMinDuration / musicMaxDuration bound the Music endpoint's
	// music_length_ms (vendor: 3s–10min). The 5m ceiling here is spend
	// hygiene, far above any Highlight sting use.
	musicMinDuration = 3 * time.Second
	musicMaxDuration = 5 * time.Minute
)

// maxResponseBytes caps how much generated audio the adapter will buffer — a
// runaway or hostile response must not exhaust memory. Matches the blob cap
// the bytes are destined for (internal/blob.MaxSize, 32 MiB) without importing
// it (pkg/voice stays internal-free).
const maxResponseBytes = 32 << 20

// GenerateSting implements [soundgen.Generator] against POST
// /v1/sound-generation: a short sound-effect sting for a Highlight. A zero
// req.Duration lets the endpoint infer the optimal length from the prompt;
// otherwise the duration is clamped to the sting bounds.
func (c *Client) GenerateSting(ctx context.Context, req soundgen.Request) (soundgen.Result, error) {
	if c.apiKey == "" {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.GenerateSting: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.GenerateSting: Prompt is required")
	}

	body := struct {
		Text            string   `json:"text"`
		DurationSeconds *float64 `json:"duration_seconds,omitempty"`
		ModelID         string   `json:"model_id"`
	}{Text: req.Prompt, ModelID: SFXModel}
	if req.Duration > 0 {
		secs := clampDuration(req.Duration, stingMinDuration, stingMaxDuration).Seconds()
		body.DurationSeconds = &secs
	}

	res, err := c.generate(ctx, "GenerateSting", "/v1/sound-generation", body)
	if err != nil {
		return soundgen.Result{}, err
	}
	res.Model = SFXModel
	return res, nil
}

// ComposeMusic implements [soundgen.Generator] against POST /v1/music: a
// composed track for a Highlight. force_instrumental is always set — the track
// plays under (or beside) recorded table speech, and generated lyrics quoting a
// transcript excerpt back at the table is not the product (#312: the excerpt
// steers mood, not libretto). A zero req.Duration lets the model choose.
func (c *Client) ComposeMusic(ctx context.Context, req soundgen.Request) (soundgen.Result, error) {
	if c.apiKey == "" {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.ComposeMusic: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.ComposeMusic: Prompt is required")
	}

	body := struct {
		Prompt            string `json:"prompt"`
		MusicLengthMs     int64  `json:"music_length_ms,omitempty"`
		ModelID           string `json:"model_id"`
		ForceInstrumental bool   `json:"force_instrumental"`
	}{Prompt: req.Prompt, ModelID: MusicModel, ForceInstrumental: true}
	if req.Duration > 0 {
		body.MusicLengthMs = clampDuration(req.Duration, musicMinDuration, musicMaxDuration).Milliseconds()
	}

	res, err := c.generate(ctx, "ComposeMusic", "/v1/music", body)
	if err != nil {
		return soundgen.Result{}, err
	}
	res.Model = MusicModel
	return res, nil
}

// generate POSTs one JSON body to path and returns the binary audio response.
// Both endpoints share the shape: JSON in, encoded audio out, usage reported in
// the character-cost response header.
func (c *Client) generate(ctx context.Context, op, path string, body any) (soundgen.Result, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.%s: marshal body: %w", op, err)
	}

	u := strings.TrimRight(c.baseURL, "/") + path + "?output_format=" + outputFormat
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.%s: build request: %w", op, err)
	}
	httpReq.Header.Set("xi-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/mpeg")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.%s: %w", op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return soundgen.Result{}, readErrorResponse(resp, op)
	}

	// Bounded read: one byte past the cap distinguishes "exactly at the cap"
	// from "over it" without buffering an unbounded response.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.%s: read audio: %w", op, err)
	}
	if len(data) > maxResponseBytes {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.%s: response exceeds %d bytes", op, maxResponseBytes)
	}
	if len(data) == 0 {
		return soundgen.Result{}, fmt.Errorf("elevenlabs.%s: response had no audio", op)
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" || strings.HasPrefix(ct, "application/") {
		ct = "audio/mpeg" // the requested output format; some gateways omit or genericize it
	}
	// character-cost is the vendor's own usage unit for this API family;
	// absent or unparsable reads as 0 (the caller prices by duration anyway).
	cost, _ := strconv.ParseInt(strings.TrimSpace(resp.Header.Get("character-cost")), 10, 64)

	return soundgen.Result{Data: data, ContentType: ct, CharacterCost: cost}, nil
}

// clampDuration bounds d to [min, max].
func clampDuration(d, min, max time.Duration) time.Duration {
	if d < min {
		return min
	}
	if d > max {
		return max
	}
	return d
}

// readErrorResponse reads up to 512 bytes of a non-2xx response body for
// diagnostic context and returns it as a typed [*providererr.HTTPError] so
// callers can classify the call by status code via errors.As (ADR-0044) —
// byte-identical in shape to the tts/stt adapters' helper.
func readErrorResponse(resp *http.Response, op string) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return &providererr.HTTPError{
		Op:         "elevenlabs." + op,
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Body:       strings.TrimSpace(string(snippet)),
	}
}
