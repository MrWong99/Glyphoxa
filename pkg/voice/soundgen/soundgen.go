// Package soundgen defines the provider-neutral sound-generation surface for
// Highlight sound enrichment (#312, Epic 8): a [Generator] turns a text prompt
// into one encoded audio asset — a short sound-effect Sting or a composed Music
// track. Like the TTS voice-design previews (ADR-0022), the produced bytes are
// opaque encoded audio destined for the blob seam and browser playback; they
// never enter the PCM hot path.
//
// v1.0's only adapter is ElevenLabs (see the elevenlabs subpackage): per the
// ADR-0004 amendment (2026-07-22, #312) sound generation rides the Tenant's
// existing `tts` Provider Config iff that provider is ElevenLabs — deliberately
// NOT a new Component.
package soundgen

import (
	"context"
	"errors"
	"time"
)

// ErrNotConfigured is the sentinel a factory returns when the tenant has no
// usable sound-generation key: no `tts` Provider Config, a non-ElevenLabs tts
// provider, or no resolvable key (ADR-0004 amendment). Consumers treat it as a
// clean no-op — no retry, no spend. Declared ONCE here and aliased by every
// consumer (the imagegen #541 lesson): two distinct sentinels compare unequal
// under errors.Is and silently disable a consumer's actionable branch.
var ErrNotConfigured = errors.New("soundgen: sound generation is not configured")

// Request is one sound-generation ask: the prompt describing the desired audio
// and the target duration. A zero Duration lets the provider pick (the SFX
// endpoint infers an optimal length from the prompt).
type Request struct {
	// Prompt describes the sound to generate.
	Prompt string
	// Duration is the requested audio length. Adapters clamp it to the
	// endpoint's own bounds; zero means provider-chosen.
	Duration time.Duration
}

// Result is one generated audio asset.
type Result struct {
	// Data is the encoded audio (MP3 for the ElevenLabs adapter).
	Data []byte
	// ContentType is the audio MIME type (e.g. "audio/mpeg").
	ContentType string
	// Model is the provider model id that produced the audio — the ADR-0046
	// price-map / Usage-Ledger attribution key (SFX and Music meter separately,
	// ADR-0004 amendment).
	Model string
	// CharacterCost is the vendor-reported usage cost in its own credit unit
	// (ElevenLabs' character-cost response header), 0 when not reported. It is
	// recorded for attribution; pricing uses the requested duration.
	CharacterCost int64
}

// Generator produces sound assets. Implemented by the ElevenLabs adapter and
// replayed by voicecassette.LoadSound in tests.
type Generator interface {
	// GenerateSting produces a short sound-effect sting (the ElevenLabs
	// sound-effects endpoint; bounded to tens of seconds).
	GenerateSting(ctx context.Context, req Request) (Result, error)
	// ComposeMusic produces a composed music track (the ElevenLabs Music
	// endpoint; longer-form than a sting).
	ComposeMusic(ctx context.Context, req Request) (Result, error)
}
