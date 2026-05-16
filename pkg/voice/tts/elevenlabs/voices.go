package elevenlabs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// ListVoices implements [tts.VoiceLister]. Returned voices have Settings
// pre-populated with [DefaultV3Settings] so each one is immediately usable
// in [Client.Synthesize] without further configuration, per ADR-0022.
//
// All ElevenLabs labels (category, gender, accent, description, …) are
// surfaced via [tts.Voice.Metadata]; the `language` label is also
// promoted to [tts.Voice.Language] as a BCP-47 hint.
func (c *Client) ListVoices(ctx context.Context) ([]tts.Voice, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("elevenlabs.ListVoices: missing API key (set %s or pass it to New)", APIKeyEnv)
	}

	u := strings.TrimRight(c.baseURL, "/") + "/v1/voices"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.ListVoices: build request: %w", err)
	}
	req.Header.Set("xi-api-key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.ListVoices: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, readErrorResponse(resp, "ListVoices")
	}

	var body struct {
		Voices []struct {
			VoiceID     string            `json:"voice_id"`
			Name        string            `json:"name"`
			Category    string            `json:"category"`
			Description string            `json:"description"`
			Labels      map[string]string `json:"labels"`
		} `json:"voices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("elevenlabs.ListVoices: decode body: %w", err)
	}

	defaults, err := json.Marshal(DefaultV3Settings())
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.ListVoices: marshal defaults: %w", err)
	}

	out := make([]tts.Voice, 0, len(body.Voices))
	for _, v := range body.Voices {
		meta := make(map[string]string, len(v.Labels)+2)
		for k, val := range v.Labels {
			meta[k] = val
		}
		if v.Category != "" {
			meta["category"] = v.Category
		}
		if v.Description != "" {
			meta["description"] = v.Description
		}
		out = append(out, tts.Voice{
			ProviderID: ProviderID,
			VoiceID:    v.VoiceID,
			Name:       v.Name,
			Language:   v.Labels["language"],
			Settings:   defaults,
			Metadata:   meta,
		})
	}
	return out, nil
}
