# TTS provider interface: small core, opt-in capabilities, opaque markup

The v2 Text-to-Speech surface is a small required `Synthesizer` plus a set of
opt-in capability interfaces (`VoiceLister`, `VoiceCloner`, `VoiceDesigner`,
`DialogueSynthesizer`). Hot-path callers depend only on `Synthesizer`; admin
and catalog tooling type-asserts the capabilities it needs.

**Hot-path lifecycle: one call per sentence.**

```go
Synthesize(ctx, SynthesizeRequest) (<-chan AudioChunk, error)
```

A call renders exactly one sentence; the returned channel's close marks the
sentence as fully synthesized. This aligns the interface boundary with
ADR-0012's deliver-then-commit boundary: once the orchestrator forwards the
last frame to Discord, the Transcript utterance commits. Independent
lifecycles per call also make barge-in trivial — cancel the in-flight call's
ctx, drop future calls. Per-sentence cassette stubs (ADR-0021) map directly
onto per-call invocations. Pipelining is preserved because the orchestrator
issues sentence N+1's call concurrently with sentence N's audio still
streaming.

**`AudioChunk` is self-describing; the orchestrator owns resampling.**

```go
type AudioChunk struct { PCM []byte; SampleRate int; Channels int }
```

Providers emit chunks at whatever native rate the vendor returns
(ElevenLabs PCM 16/22.05/24/44.1k; OpenAI PCM/MP3/Opus/etc.). One resampler
in the orchestrator pins to Discord's 48kHz mono Opus pipeline. New
providers stay thin — no DSP knowledge required.

**`Voice` carries identity + opaque provider-typed `Settings`.**

```go
type Voice struct {
    ProviderID string          // "elevenlabs" | "openai" | …
    VoiceID    string
    Name       string
    Language   string          // BCP-47 hint
    Settings   json.RawMessage // provider-typed; round-trips through Postgres jsonb
    Metadata   map[string]string
}
```

Provider-specific tuning (ElevenLabs `model_id`/`stability`/`similarity_boost`,
OpenAI `instructions`) lives in `Settings`. Per-provider packages export a
typed `Settings` struct and marshal/unmarshal against this field. The core
`tts` package treats `Settings` as opaque. Per-call deviation from the
persisted defaults uses an `OverrideSettings json.RawMessage` field on
`SynthesizeRequest` (and `DialogueRequest`), merged over `Voice.Settings`
by the provider.

**Sentence text is opaque. `AudioMarkupPrompt(voice) string` is required.**

The five TTS-markup philosophies in 2026 (ElevenLabs `[brackets]`, SSML, OpenAI
out-of-band `instructions`, Cartesia mixed, Coqui reference-audio) are
fundamentally incompatible. Glyphoxa does not invent a portable markup
vocabulary. Sentence text passes through to the provider verbatim; the
LLM/Persona layer is taught provider-appropriate syntax via a system-prompt
fragment returned by `Synthesizer.AudioMarkupPrompt(voice)`. The method is
required (not an opt-in capability) because every provider has *something* to
say about markup, even if the answer is "use plain prose only."

**Capability interfaces.**

- `VoiceLister.ListVoices(ctx) ([]Voice, error)` — no pagination, no
  filtering args in MVP. Returned Voices have `Settings` pre-populated with
  sensible per-model defaults.
- `VoiceCloner.CloneVoice(ctx, CloneRequest) (Voice, error)` — synchronous;
  WAV-encoded samples; verification state stashed in
  `Voice.Metadata["requires_verification"]`.
- `VoiceDesigner` — two-method lifecycle:
  `DesignVoice(ctx, DesignRequest) ([]VoicePreview, error)` returns previews
  carrying *encoded* audio bytes + MIME type (mp3 to a `<audio>` tag, no PCM
  decode round-trip), then `SaveDesignedVoice(ctx, SaveDesignedVoiceRequest)
  (Voice, error)` persists the chosen preview.
- `DialogueSynthesizer.SynthesizeDialogue(ctx, DialogueRequest) (<-chan AudioChunk, error)`
  — multi-voice batch render, streams audio chunks; off the conversational
  hot path (recap, cutscenes); not committed to Transcripts; cancellation
  drops the remainder.

**Considered options:**

- **One fat `Provider` interface** (v1's shape) — rejected. Forces stub
  implementations of capabilities a provider can't do (Coqui's `CloneVoice`
  was a non-trivial op; OpenAI has neither cloner nor designer). Capability
  interfaces are idiomatic Go (`io.ReadCloser` style).
- **Universal markup vocabulary / SSML translation layer** — rejected. The
  five markup philosophies don't translate without lossy round-trips; the
  Persona is the right home for provider-aware prompting; building an
  abstraction for two providers is yak-shaving.
- **Per-turn streaming text input + sentence-marked output frames** —
  rejected. Misaligns with ADR-0012's commit boundary and complicates
  cassette stubs; sub-sentence-push first-sentence TTFA gain (~50–200 ms,
  ElevenLabs only) is recoverable later via a `StreamingSynthesizer`
  capability if measurement demands it.
- **Providers normalize output to canonical 48kHz PCM** — rejected. Pushes
  resampling into N implementations; locks future encoder changes into
  multi-provider rewrites.

**Out of scope (flagged for follow-up):**

- Pronunciation dictionaries (ElevenLabs supports up to 3 per call;
  TTRPG-relevant for campaign vocab) — future capability.
- Generation continuity (ElevenLabs `previous_text`/`previous_request_ids`)
  — orchestrator passes through `OverrideSettings`; no interface change.
- Voice deletion / pruning of designed voices — future `VoiceManager`
  capability.
- Streaming-vs-synchronous markup gotchas (Google Chirp 3 HD rejects SSML
  in streaming) — provider implementation concern, not interface.
