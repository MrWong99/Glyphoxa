package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// dialogueInput is one speaker turn in the ElevenLabs text-to-dialogue body.
type dialogueInput struct {
	Text    string `json:"text"`
	VoiceID string `json:"voice_id"`
}

// dialogueBody is the POST /v1/text-to-dialogue body.
type dialogueBody struct {
	Inputs        []dialogueInput `json:"inputs"`
	ModelID       string          `json:"model_id,omitempty"`
	VoiceSettings *VoiceSettings  `json:"voice_settings,omitempty"`
}

// SynthesizeDialogue implements [tts.DialogueSynthesizer]. ElevenLabs's
// eleven_v3 dialogue mode weaves multi-voice scripts with conversational
// pacing in a single render — per ADR-0022 this is off the hot path
// (recap/cutscene use) and is not committed to Transcripts.
//
// Cancellation via ctx aborts the render; partial audio drained before
// cancellation remains valid PCM.
func (c *Client) SynthesizeDialogue(ctx context.Context, req tts.DialogueRequest) (<-chan tts.AudioChunk, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: missing API key (set %s or pass it to New)", APIKeyEnv)
	}
	if len(req.Segments) == 0 {
		return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: DialogueRequest.Segments is empty")
	}

	inputs := make([]dialogueInput, 0, len(req.Segments))
	for i, seg := range req.Segments {
		if seg.Voice.VoiceID == "" {
			return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: Segments[%d].Voice.VoiceID is empty", i)
		}
		inputs = append(inputs, dialogueInput{Text: seg.Text, VoiceID: seg.Voice.VoiceID})
	}

	settings, err := mergeSettings(nil, req.OverrideSettings)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: %w", err)
	}
	if settings.ModelID == "" {
		settings.ModelID = ModelV3
	}
	if settings.OutputFormat == "" {
		settings.OutputFormat = DefaultOutputFormat
	}
	sr := sampleRateFromOutputFormat(settings.OutputFormat)
	if sr == 0 {
		return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: output_format %q is not PCM", settings.OutputFormat)
	}

	body := dialogueBody{
		Inputs:        inputs,
		ModelID:       settings.ModelID,
		VoiceSettings: settings.VoiceSettings,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: marshal body: %w", err)
	}

	u := fmt.Sprintf("%s/v1/text-to-dialogue/stream?output_format=%s",
		strings.TrimRight(c.baseURL, "/"),
		url.QueryEscape(settings.OutputFormat),
	)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: build request: %w", err)
	}
	httpReq.Header.Set("xi-api-key", c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "audio/*")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("elevenlabs.SynthesizeDialogue: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readErrorResponse(resp, "SynthesizeDialogue")
	}

	ch := make(chan tts.AudioChunk)
	go streamPCM(ctx, resp.Body, ch, sr)
	return ch, nil
}
