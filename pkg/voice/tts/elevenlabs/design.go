package elevenlabs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// DesignVoice implements [tts.VoiceDesigner] against ElevenLabs's
// text-to-voice design endpoint. Previews carry encoded MP3 bytes suitable
// for direct playback in a browser <audio> tag (per ADR-0022 previews never
// enter the PCM hot path) and an opaque PreviewID that flows back into
// [Client.SaveDesignedVoice] to persist a chosen preview.
//
// Caller-supplied [tts.DesignRequest.Settings] are merged into the request
// body as a JSON object, so provider-typed design knobs (guidance_scale,
// loudness, seed, …) flow through without this adapter having to enumerate
// them. Keys already populated by the request shape (voice_description,
// text) take precedence over caller overrides.
func (c *Client) DesignVoice(ctx context.Context, req tts.DesignRequest) ([]tts.VoicePreview, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("elevenlabs.DesignVoice: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	if req.Description == "" {
		return nil, fmt.Errorf("elevenlabs.DesignVoice: Description is required")
	}

	payload := map[string]any{}
	if len(req.Settings) > 0 {
		if err := json.Unmarshal(req.Settings, &payload); err != nil {
			return nil, fmt.Errorf("elevenlabs.DesignVoice: decode Settings: %w", err)
		}
	}
	payload["voice_description"] = req.Description
	if req.SampleText != "" {
		payload["text"] = req.SampleText
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.DesignVoice: marshal body: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/v1/text-to-voice/create-previews"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.DesignVoice: build request: %w", err)
	}
	httpReq.Header.Set("xi-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.DesignVoice: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorResponse(resp, "DesignVoice")
	}

	var respBody struct {
		Previews []struct {
			GeneratedVoiceID string `json:"generated_voice_id"`
			AudioBase64      string `json:"audio_base_64"`
			MediaType        string `json:"media_type"`
			Description      string `json:"voice_description"`
		} `json:"previews"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return nil, fmt.Errorf("elevenlabs.DesignVoice: decode body: %w", err)
	}

	out := make([]tts.VoicePreview, 0, len(respBody.Previews))
	for i, p := range respBody.Previews {
		audio, err := base64.StdEncoding.DecodeString(p.AudioBase64)
		if err != nil {
			return nil, fmt.Errorf("elevenlabs.DesignVoice: decode preview %d audio: %w", i, err)
		}
		mt := p.MediaType
		if mt == "" {
			mt = "audio/mpeg"
		}
		out = append(out, tts.VoicePreview{
			PreviewID:   p.GeneratedVoiceID,
			Audio:       audio,
			MIMEType:    mt,
			Description: p.Description,
		})
	}
	return out, nil
}

// SaveDesignedVoice implements [tts.VoiceDesigner]. Persists a preview from
// a prior [Client.DesignVoice] call as a permanent voice in the ElevenLabs
// library; the returned [tts.Voice] is immediately usable in
// [Client.Synthesize].
func (c *Client) SaveDesignedVoice(ctx context.Context, req tts.SaveDesignedVoiceRequest) (tts.Voice, error) {
	if c.apiKey == "" {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	if req.PreviewID == "" {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: PreviewID is required")
	}
	if req.Name == "" {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: Name is required")
	}

	body := struct {
		VoiceName        string            `json:"voice_name"`
		VoiceDescription string            `json:"voice_description,omitempty"`
		GeneratedVoiceID string            `json:"generated_voice_id"`
		Labels           map[string]string `json:"labels,omitempty"`
	}{
		VoiceName:        req.Name,
		VoiceDescription: req.Description,
		GeneratedVoiceID: req.PreviewID,
		Labels:           req.Labels,
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: marshal body: %w", err)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/v1/text-to-voice/create-voice-from-preview"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: build request: %w", err)
	}
	httpReq.Header.Set("xi-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return tts.Voice{}, readErrorResponse(resp, "SaveDesignedVoice")
	}

	var respBody struct {
		VoiceID string `json:"voice_id"`
		Name    string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&respBody); err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: decode body: %w", err)
	}
	if respBody.VoiceID == "" {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: response had empty voice_id")
	}
	if respBody.Name == "" {
		respBody.Name = req.Name
	}

	defaults, err := json.Marshal(DefaultV3Settings())
	if err != nil {
		return tts.Voice{}, fmt.Errorf("elevenlabs.SaveDesignedVoice: marshal defaults: %w", err)
	}
	meta := make(map[string]string, len(req.Labels))
	for k, v := range req.Labels {
		meta[k] = v
	}

	return tts.Voice{
		ProviderID: ProviderID,
		VoiceID:    respBody.VoiceID,
		Name:       respBody.Name,
		Settings:   defaults,
		Metadata:   meta,
	}, nil
}
