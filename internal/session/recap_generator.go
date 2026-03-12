package session

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/MrWong99/glyphoxa/pkg/memory"
	"github.com/MrWong99/glyphoxa/pkg/provider/llm"
	"github.com/MrWong99/glyphoxa/pkg/provider/tts"
)

// recapNarratorPrompt is the system prompt for generating dramatic session recaps.
const recapNarratorPrompt = `You are the dramatic narrator of an epic tabletop RPG campaign. Craft a gripping "Previously On..." recap from the session transcript below.

Guidelines:
- 200-300 words (~90-120 seconds spoken)
- Third person, past tense, vivid cinematic language
- Open with a strong hook: a memorable moment, looming threat, or unresolved question
- Highlight: key decisions, secrets revealed, dangers faced, bonds forged or broken
- Close with a cliffhanger that sets the stage for the next session
- Do NOT include dice rolls or mechanical terms — narrate outcomes only
- Do NOT invent events not in the transcript`

// RecapGenerator orchestrates LLM + TTS to produce voiced session recaps.
type RecapGenerator struct {
	llm   llm.Provider
	tts   tts.Provider
	store memory.RecapStore
}

// NewRecapGenerator creates a RecapGenerator.
func NewRecapGenerator(llmProv llm.Provider, ttsProv tts.Provider, store memory.RecapStore) *RecapGenerator {
	return &RecapGenerator{
		llm:   llmProv,
		tts:   ttsProv,
		store: store,
	}
}

// Generate reads the session transcript, calls LLM for a dramatic recap,
// synthesises TTS audio, and persists the result via RecapStore.
func (g *RecapGenerator) Generate(ctx context.Context, sessionID, campaignID string, sessionStore memory.SessionStore, voice tts.VoiceProfile) (*memory.Recap, error) {
	// Fetch transcript.
	entries, err := sessionStore.GetRecent(ctx, sessionID, 24*time.Hour)
	if err != nil {
		return nil, fmt.Errorf("recap: fetch transcript: %w", err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("recap: no transcript entries for session %s", sessionID)
	}

	// Format transcript for LLM.
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "[%s]: %s\n", e.SpeakerName, e.Text)
	}

	// Generate dramatic recap text.
	resp, err := g.llm.Complete(ctx, llm.CompletionRequest{
		SystemPrompt: recapNarratorPrompt,
		Messages: []llm.Message{
			{Role: "user", Content: sb.String()},
		},
		Temperature: 0.7,
	})
	if err != nil {
		return nil, fmt.Errorf("recap: llm complete: %w", err)
	}

	recapText := strings.TrimSpace(resp.Content)

	// Synthesise TTS audio.
	textCh := make(chan string, 1)
	go func() {
		defer close(textCh)
		textCh <- recapText
	}()

	audioCh, err := g.tts.SynthesizeStream(ctx, textCh, voice)
	if err != nil {
		return nil, fmt.Errorf("recap: tts synthesize: %w", err)
	}

	var audioBuf bytes.Buffer
	for chunk := range audioCh {
		audioBuf.Write(chunk)
	}

	now := time.Now().UTC()
	recap := &memory.Recap{
		SessionID:   sessionID,
		CampaignID:  campaignID,
		Text:        recapText,
		AudioData:   audioBuf.Bytes(),
		SampleRate:  22050,
		Channels:    1,
		Duration:    estimateDuration(audioBuf.Len(), 22050, 1),
		GeneratedAt: now,
	}

	if err := g.store.SaveRecap(ctx, *recap); err != nil {
		return nil, fmt.Errorf("recap: save: %w", err)
	}

	return recap, nil
}

// estimateDuration calculates audio duration from PCM byte count.
// Assumes 16-bit samples (2 bytes per sample).
func estimateDuration(byteCount, sampleRate, channels int) time.Duration {
	if sampleRate <= 0 || channels <= 0 {
		return 0
	}
	samples := byteCount / (2 * channels) // 16-bit = 2 bytes
	return time.Duration(samples) * time.Second / time.Duration(sampleRate)
}
