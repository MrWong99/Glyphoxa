package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// cloneSettings is the provider-typed knob set carried via
// [tts.CloneRequest.Settings] for the ElevenLabs Instant Voice Clone API.
type cloneSettings struct {
	RemoveBackgroundNoise *bool `json:"remove_background_noise,omitempty"`
}

// CloneVoice implements [tts.VoiceCloner] against the ElevenLabs Instant
// Voice Clone endpoint. Samples must be WAV-encoded; the API accepts up to
// the vendor-side per-request limit (currently ~25 files / 11 MiB total).
//
// Per ADR-0022 the returned voice may be quality-pending — the
// [tts.Voice.Metadata] map carries "requires_verification" as a string
// "true"/"false" when the API surfaces that field on the response.
func (c *Client) CloneVoice(ctx context.Context, req tts.CloneRequest) (tts.Voice, error) {
	if c.apiKey == "" {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	if req.Name == "" {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: Name is required")
	}
	if len(req.Samples) == 0 {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: at least one WAV sample is required")
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("name", req.Name); err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: write name: %w", err)
	}
	if req.Description != "" {
		if err := mw.WriteField("description", req.Description); err != nil {
			return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: write description: %w", err)
		}
	}
	if len(req.Labels) > 0 {
		labelsJSON, err := json.Marshal(req.Labels)
		if err != nil {
			return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: marshal labels: %w", err)
		}
		if err := mw.WriteField("labels", string(labelsJSON)); err != nil {
			return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: write labels: %w", err)
		}
	}
	if len(req.Settings) > 0 {
		var cs cloneSettings
		if err := json.Unmarshal(req.Settings, &cs); err != nil {
			return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: decode Settings: %w", err)
		}
		if cs.RemoveBackgroundNoise != nil {
			if err := mw.WriteField("remove_background_noise", strconv.FormatBool(*cs.RemoveBackgroundNoise)); err != nil {
				return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: write remove_background_noise: %w", err)
			}
		}
	}
	for i, sample := range req.Samples {
		part, err := mw.CreateFormFile("files", fmt.Sprintf("sample-%d.wav", i))
		if err != nil {
			return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: create form file %d: %w", i, err)
		}
		if _, err := part.Write(sample); err != nil {
			return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: write sample %d: %w", i, err)
		}
	}
	if err := mw.Close(); err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: close multipart writer: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/v1/voices/add"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &buf)
	if err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: build request: %w", err)
	}
	httpReq.Header.Set("xi-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tts.Voice{}, readErrorResponse(resp, "CloneVoice")
	}

	var respBody struct {
		VoiceID              string `json:"voice_id"`
		Name                 string `json:"name"`
		RequiresVerification *bool  `json:"requires_verification,omitempty"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: decode body: %w", err)
	}
	if respBody.VoiceID == "" {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: response had empty voice_id")
	}
	if respBody.Name == "" {
		respBody.Name = req.Name
	}

	defaults, err := json.Marshal(DefaultV3Settings())
	if err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.CloneVoice: marshal defaults: %w", err)
	}
	meta := make(map[string]string, len(req.Labels)+1)
	for k, v := range req.Labels {
		meta[k] = v
	}
	if respBody.RequiresVerification != nil {
		meta["requires_verification"] = strconv.FormatBool(*respBody.RequiresVerification)
	}

	return tts.Voice{
		ProviderID: ProviderID,
		VoiceID:    respBody.VoiceID,
		Name:       respBody.Name,
		Settings:   defaults,
		Metadata:   meta,
	}, nil
}
