---
title: "feat: Add ElevenLabs Scribe v2 Realtime STT provider"
type: feat
status: completed
date: 2026-03-02
deepened: 2026-03-02
---

# feat: Add ElevenLabs Scribe v2 Realtime STT Provider

## Enhancement Summary

**Deepened on:** 2026-03-02
**Sections enhanced:** 8
**Research agents used:** Architecture Strategist, Performance Oracle, Security Sentinel, Silent Failure Hunter, Pattern Recognition Specialist, Code Simplicity Reviewer, Type Design Analyzer, Best Practices Researcher, Framework Docs Researcher, SpecFlow Analyzer

### Key Improvements

1. **Model ID corrected** — `scribe_v2` is the batch model; the WebSocket realtime API requires `scribe_v2_realtime`
2. **Simplified response parser** — removed `messageKind` enum; uses `(stt.Transcript, bool)` matching Deepgram's proven pattern
3. **Close() race condition fixed** — commit message sent by writeLoop (sole writer), not Close(); added 5s timeout on wg.Wait()
4. **session_started handled asynchronously** — removed blocking read from StartStream, saving 50-150ms latency per session
5. **Error classification** — fatal errors (auth_error, quota_exceeded) close session; transient errors logged and skipped
6. **Confidence mapping resolved** — `math.Exp(logprob)` converts log-probability to 0-1 scale

### Critical Corrections from Research

- `defaultModel` must be `"scribe_v2_realtime"` (NOT `"scribe_v2"`)
- `WithEndpoint` renamed to `WithBaseURL` per codebase convention (S2S providers use this name)
- Phases 1+2 merged — message types belong in the same file, no reason to separate
- Server auto-commits at 90s even in manual mode — sessions should be short-lived (they are: VAD segments)

---

## Overview

Implement a new STT provider that uses the ElevenLabs "Scribe v2 Realtime" streaming WebSocket API. This adds a second WebSocket-based STT option alongside Deepgram, leveraging ElevenLabs' transcription model with word-level timestamps and manual commit control.

## Problem Statement / Motivation

Currently the project supports Deepgram (WebSocket), Whisper HTTP, and Whisper Native for STT. Adding ElevenLabs Scribe v2 provides:

- **Provider diversity** — reduces single-vendor dependency for STT
- **ElevenLabs ecosystem** — users already using ElevenLabs TTS can consolidate on one API key/vendor
- **Scribe v2 quality** — ElevenLabs' latest transcription model with strong multilingual support

## Proposed Solution

Create `pkg/provider/stt/elevenlabs/elevenlabs.go` implementing `stt.Provider` and `stt.SessionHandle`, following the established Deepgram WebSocket provider pattern with adaptations for the ElevenLabs protocol.

### Key Design Decisions

1. **Commit strategy: `manual`** — The audio pipeline creates short-lived sessions per speech segment (VAD → StartStream → SendAudio... → Close). Manual commit gives us control: we trigger a commit in `Close()` to flush the final transcript before tearing down the WebSocket. This avoids conflicts with ElevenLabs' built-in VAD.

2. **Audio encoding: base64 JSON** — Unlike Deepgram (raw binary frames), ElevenLabs requires JSON messages with base64-encoded audio in the `audio_base_64` field. The writeLoop must encode each PCM chunk before sending.

3. **Timestamps enabled by default** — Set `include_timestamps=true` to populate `stt.WordDetail` on committed transcripts. This matches the Deepgram provider's behavior.

4. **Authentication via header** — Use `xi-api-key` header in WebSocket dial options (NOT in the JSON body). This is consistent with the ElevenLabs TTS provider at `pkg/provider/tts/elevenlabs/`.

5. **Config name: `"elevenlabs"`** — Registered as `"elevenlabs"` in the STT provider registry, distinct from the TTS `"elevenlabs"` entry (different registry maps).

### Research Insights — Design Decisions

**Commit Strategy Nuance:**
- ElevenLabs auto-commits at 90 seconds even in manual mode. Since our sessions are short-lived VAD segments (typically 1-15 seconds), this is a non-issue.
- Sending an empty audio chunk with `commit: true` triggers the final committed transcript. The server responds with `committed_transcript_with_timestamps` (when timestamps enabled) before allowing clean close.

**Base64 Encoding Performance:**
- At 16kHz/16-bit/mono, a 30ms frame is ~960 bytes raw → ~1280 bytes base64. Negligible overhead.
- `encoding/base64.StdEncoding.EncodeToString` is allocation-efficient for these sizes. No sync.Pool needed.

**Authentication Security:**
- API key transmitted as HTTP header during WebSocket upgrade — encrypted in transit via TLS (wss://).
- Never log the API key. Provider struct should implement `slog.LogValuer` to redact the key in debug logs.

## Technical Approach

### Protocol Mapping

| Pipeline Action | ElevenLabs WebSocket Protocol |
|---|---|
| `StartStream(ctx, cfg)` | Dial `wss://api.elevenlabs.io/v1/speech-to-text/realtime?...` with `xi-api-key` header |
| `SendAudio(chunk)` | Send `{"message_type":"input_audio_chunk","audio_base_64":"...","sample_rate":16000}` |
| Receive partial | Parse `{"message_type":"partial_transcript","text":"..."}` → `partials` channel |
| Receive final | Parse `{"message_type":"committed_transcript_with_timestamps","text":"...","words":[...]}` → `finals` channel |
| `Close()` | Signal writeLoop to send commit + drain, wait with timeout, close WebSocket |

### Research Insights — Protocol

**Server Error Types (13 total):**
- **Fatal** (close session): `auth_error`, `invalid_api_key`, `quota_exceeded`, `invalid_audio_format`
- **Transient** (log and continue): `rate_limited`, `service_unavailable`, `transcription_error`
- **Informational** (log at debug): `session_started`, `session_ended`

**Concurrency Limits:**
- ElevenLabs allows a limited number of concurrent WebSocket connections per API key. Our short-lived session pattern minimizes this risk, but the error `quota_exceeded` should be handled gracefully.

### Architecture

```
┌──────────────────────────────────────────────────┐
│  Provider (immutable config)                     │
│  apiKey, model, language, sampleRate, baseURL    │
│                                                  │
│  StartStream(ctx, cfg) → SessionHandle           │
└──────────────────────────────────────────────────┘
                    │
                    ▼
┌──────────────────────────────────────────────────┐
│  session (per-utterance, short-lived)            │
│                                                  │
│  ┌─────────┐   audio chan   ┌──────────┐        │
│  │SendAudio│──────────────▶│writeLoop │        │
│  └─────────┘               │ base64   │        │
│                            │ encode   │──▶ WS  │
│                            │ commit   │        │
│                            └──────────┘        │
│                                                  │
│  ┌─────────┐   partials    ┌──────────┐        │
│  │Partials │◀──────────────│readLoop  │        │
│  └─────────┘               │ parse    │◀── WS  │
│  ┌─────────┐   finals      │ dispatch │        │
│  │Finals   │◀──────────────│ errors   │        │
│  └─────────┘               └──────────┘        │
│                                                  │
│  done chan, sync.Once, sync.WaitGroup            │
└──────────────────────────────────────────────────┘
```

### Research Insights — Architecture

**writeLoop owns all writes:** The writeLoop is the sole goroutine that writes to the WebSocket. Close() signals via the `done` channel, and writeLoop handles the commit message as its final action before returning. This eliminates the concurrent-writer race condition that would occur if Close() wrote directly to the WebSocket.

**readLoop handles session_started:** Instead of blocking StartStream with a synchronous read of `session_started`, the readLoop handles it as just another message type (log and skip). This saves 50-150ms per session and avoids a timeout/error-handling edge case in the hot path.

### Implementation Plan

#### Provider and Message Types (`pkg/provider/stt/elevenlabs/elevenlabs.go`)

**Constants and Provider struct:**

```go
// Package elevenlabs provides an ElevenLabs Scribe v2 Realtime STT provider
// using the ElevenLabs streaming WebSocket API. It implements the stt.Provider
// interface.
package elevenlabs

const (
    defaultBaseURL    = "wss://api.elevenlabs.io/v1/speech-to-text/realtime"
    defaultModel      = "scribe_v2_realtime"
    defaultLanguage   = "en"
    defaultSampleRate = 16000

    closeTimeout = 5 * time.Second
)

type Option func(*Provider)
// WithModel, WithLanguage, WithSampleRate, WithBaseURL (for testing)

type Provider struct {
    apiKey     string
    model      string
    language   string
    sampleRate int
    baseURL    string  // overridable for tests
}

var _ stt.Provider = (*Provider)(nil)

func New(apiKey string, opts ...Option) (*Provider, error)
```

**JSON message types (same file):**

```go
// --- Client → Server ---

type audioChunkMessage struct {
    MessageType string `json:"message_type"`    // "input_audio_chunk"
    AudioBase64 string `json:"audio_base_64"`
    Commit      bool   `json:"commit,omitempty"`
    SampleRate  int    `json:"sample_rate"`
}

// --- Server → Client ---

// serverMessage is the envelope used to dispatch on message_type.
type serverMessage struct {
    MessageType string `json:"message_type"`
}

type partialTranscriptMessage struct {
    MessageType string `json:"message_type"`
    Text        string `json:"text"`
}

type committedTranscriptMessage struct {
    MessageType  string          `json:"message_type"`
    Text         string          `json:"text"`
    LanguageCode string          `json:"language_code"`
    Words        []wordTimestamp `json:"words"`
}

type wordTimestamp struct {
    Text      string  `json:"text"`
    Start     float64 `json:"start"`
    End       float64 `json:"end"`
    Type      string  `json:"type"`      // "word" or "spacing"
    SpeakerID string  `json:"speaker_id"`
    LogProb   float64 `json:"logprob"`
}

type errorMessage struct {
    MessageType string `json:"message_type"`
    Error       string `json:"error"`
}
```

**URL builder:**

Build WebSocket URL with query parameters:
- `model_id` — from Provider.model (default: `scribe_v2_realtime`)
- `language_code` — from cfg.Language or Provider.language
- `audio_format` — `pcm_<sampleRate>` (e.g., `pcm_16000`)
- `include_timestamps` — `true`
- `commit_strategy` — `manual`

Authentication via `xi-api-key` header in DialOptions.

**Session struct and lifecycle:**

```go
type session struct {
    conn     *websocket.Conn
    partials chan stt.Transcript  // buffer: 64
    finals   chan stt.Transcript  // buffer: 64
    audio    chan []byte          // buffer: 256

    sampleRate int
    done       chan struct{}
    once       sync.Once
    wg         sync.WaitGroup
}

var _ stt.SessionHandle = (*session)(nil)
```

**StartStream flow:**
1. Build WebSocket URL from Provider config + StreamConfig overrides
2. `websocket.Dial` with `xi-api-key` header
3. Set read limit to 1 MiB (matching ElevenLabs TTS pattern)
4. Create session with buffered channels
5. Start `readLoop` and `writeLoop` goroutines (wg.Add(2))
6. Return session

Note: No synchronous `session_started` read. The readLoop handles it asynchronously.

**writeLoop:**
- Read PCM chunks from `audio` channel
- Base64-encode each chunk
- Marshal JSON: `{"message_type":"input_audio_chunk","audio_base_64":"<encoded>","sample_rate":<rate>}`
- Send as `websocket.MessageText`
- On `<-s.done`:
  1. Drain remaining audio chunks from channel, sending each
  2. Send commit message: `{"message_type":"input_audio_chunk","audio_base_64":"","commit":true,"sample_rate":<rate>}`
  3. Return

**readLoop:**
- `defer close(partials)` and `defer close(finals)`
- Read JSON messages from WebSocket
- Two-pass parse: unmarshal `serverMessage` envelope first, then dispatch on `message_type`:
  - `"partial_transcript"` → unmarshal `partialTranscriptMessage`, emit `stt.Transcript{Text: msg.Text}` on `partials` channel
  - `"committed_transcript"` → unmarshal `committedTranscriptMessage`, emit on `finals` channel (text only, no words)
  - `"committed_transcript_with_timestamps"` → unmarshal `committedTranscriptMessage`, build `stt.Transcript` with words, emit on `finals` channel
  - `"session_started"` → log at Debug level, skip
  - Fatal errors (`"auth_error"`, `"invalid_api_key"`, `"quota_exceeded"`) → log at Error level, return (closes session)
  - Other errors → log at Warn level, skip
  - Unknown → log at Debug level, skip

**Response parser function:**

```go
func parseResponse(data []byte) (stt.Transcript, bool)
```

Returns `(Transcript, true)` for partials and finals. Returns `(zero, false)` for all other message types. The `IsFinal` field on the returned Transcript distinguishes partials from finals.

Word timestamps mapping:
- Filter `words` to only `type == "word"` (skip `"spacing"`)
- Map `Start`/`End` from seconds (float64) to `time.Duration`
- Convert `LogProb` to Confidence via `math.Exp(logprob)` (log-probability → probability in 0-1 range)

**SendAudio:** Double-select pattern (check `done`, then send to `audio`) — identical to Deepgram.

**Close:**
- `sync.Once` guard
- Close `done` channel (signals writeLoop to drain + commit)
- Wait for goroutines with timeout: `wg.Wait()` with a 5-second deadline via `time.AfterFunc`
- Close WebSocket with normal closure status
- Always returns nil (matching Deepgram convention)

**SetKeywords:** Return `fmt.Errorf("elevenlabs: %w", stt.ErrNotSupported)` — ElevenLabs Scribe v2 does not support keyword boosting. No keyword storage needed (unlike Deepgram which stores them for reference).

### Research Insights — Implementation

**Close() Timeout Pattern:**
```go
func (s *session) Close() error {
    s.once.Do(func() {
        close(s.done)
        // writeLoop will drain audio + send commit before exiting.
        // readLoop will receive the committed transcript + exit on WebSocket close.
        done := make(chan struct{})
        go func() {
            s.wg.Wait()
            close(done)
        }()
        select {
        case <-done:
        case <-time.After(closeTimeout):
            slog.Warn("elevenlabs: close timed out waiting for goroutines")
        }
        s.conn.Close(websocket.StatusNormalClosure, "session closed")
    })
    return nil
}
```

**Fatal Error Detection in readLoop:**
```go
// Fatal server errors that should terminate the session.
var fatalErrors = map[string]bool{
    "auth_error":            true,
    "invalid_api_key":       true,
    "quota_exceeded":        true,
    "invalid_audio_format":  true,
}
```

When the readLoop encounters a fatal error message, it logs at Error level and returns, which closes the partials and finals channels and signals session termination.

**Language Code Handling:**
ElevenLabs accepts ISO 639-1 codes (e.g., `"en"`, `"de"`) while StreamConfig.Language may contain BCP-47 (e.g., `"en-US"`, `"de-DE"`). The buildURL method should take the first component before the hyphen if the language contains one. This simple normalization avoids API rejections.

#### Registration and Config

**`cmd/glyphoxa/main.go`** — Add factory registration (after existing STT registrations, ~line 266):

```go
reg.RegisterSTT("elevenlabs", func(entry config.ProviderEntry) (stt.Provider, error) {
    var opts []elevenlabsstt.Option
    if entry.Model != "" {
        opts = append(opts, elevenlabsstt.WithModel(entry.Model))
    }
    if lang := optString(entry.Options, "language"); lang != "" {
        opts = append(opts, elevenlabsstt.WithLanguage(lang))
    }
    return elevenlabsstt.New(entry.APIKey, opts...)
})
```

Import alias: `elevenlabsstt` to avoid collision with the TTS `elevenlabs` import.

**`internal/config/loader.go`** line 19 — Add `"elevenlabs"` to STT valid names:

```go
"stt": {"deepgram", "whisper", "whisper-native", "elevenlabs"},
```

#### Tests (`pkg/provider/stt/elevenlabs/elevenlabs_test.go`)

Following the Deepgram test pattern with table-driven subtests:

1. **Constructor tests** (`TestNew`):
   - `EmptyAPIKey` — error for empty key
   - `Defaults` — verify default model (`scribe_v2_realtime`), language, sampleRate
   - `WithOptions` — verify functional options override defaults

2. **URL builder tests** (`TestBuildURL`):
   - `Defaults` — verify all query params: model_id, language_code, audio_format, include_timestamps, commit_strategy
   - `LanguageOverriddenByCfg` — cfg.Language takes precedence over provider default
   - `CustomModel` — WithModel reflected in URL
   - `LanguageNormalization` — `"en-US"` → `"en"` in query param

3. **Response parser tests** (`TestParseResponse`):
   - `PartialTranscript` — partial text, IsFinal=false, no words
   - `CommittedTranscript` — final text without timestamps, IsFinal=true
   - `CommittedTranscriptWithTimestamps` — final text + word details, IsFinal=true
   - `WordFiltering` — only `type:"word"` entries become WordDetails, `"spacing"` skipped
   - `ConfidenceMapping` — `math.Exp(logprob)` produces correct probability
   - `SessionStarted` — returns (zero, false)
   - `ErrorMessage` — returns (zero, false)
   - `InvalidJSON` — returns (zero, false)
   - `UnknownMessageType` — returns (zero, false)

4. **Audio chunk message tests** (`TestAudioChunkMessage`):
   - `Normal` — verify JSON structure with base64 audio
   - `WithCommit` — verify commit flag serialization

5. **SetKeywords test:**
   - `TestSetKeywords_ReturnsErrNotSupported` — returns wrapped `stt.ErrNotSupported`

All tests use `t.Parallel()` and table-driven `t.Run` subtests per CLAUDE.md convention.

## Acceptance Criteria

### Functional Requirements

- [x] `pkg/provider/stt/elevenlabs/elevenlabs.go` implements `stt.Provider` and `stt.SessionHandle`
- [x] Compile-time assertions: `var _ stt.Provider = (*Provider)(nil)` and `var _ stt.SessionHandle = (*session)(nil)`
- [x] `New(apiKey, ...Option)` constructor with `WithModel`, `WithLanguage`, `WithSampleRate`, `WithBaseURL` options
- [x] `StartStream` dials WebSocket with `xi-api-key` header and correct query params (model_id=`scribe_v2_realtime`)
- [x] `SendAudio` base64-encodes PCM and sends as JSON `input_audio_chunk`
- [x] `readLoop` dispatches `partial_transcript` → Partials, `committed_transcript*` → Finals
- [x] `readLoop` classifies fatal server errors and terminates session on auth/quota failures
- [x] `Close` signals writeLoop to commit, waits with 5s timeout, closes WebSocket
- [x] `SetKeywords` returns wrapped `stt.ErrNotSupported`
- [x] Registered as `"elevenlabs"` in STT provider registry
- [x] Added to `ValidProviderNames["stt"]` in config/loader.go
- [x] writeLoop is the sole WebSocket writer (no concurrent writes from Close)

### Non-Functional Requirements

- [x] All public methods safe for concurrent use
- [x] Race detector passes (`-race -count=1`)
- [x] Error messages use `"elevenlabs: <context>: %w"` wrapping convention
- [x] All exported symbols have godoc comments
- [x] No naked returns
- [x] API key never appears in log output

### Quality Gates

- [x] `pkg/provider/stt/elevenlabs/elevenlabs_test.go` covers constructor, URL builder, response parser, audio chunk serialization, SetKeywords
- [x] `t.Parallel()` on all tests and subtests
- [x] Table-driven tests with `t.Run` for parser and URL builder
- [x] `make check` passes (fmt + vet + test — whisper link failure is pre-existing, unrelated)

## Files to Create / Modify

| File | Action | Description |
|---|---|---|
| `pkg/provider/stt/elevenlabs/elevenlabs.go` | **Create** | Provider + session + message types (single file) |
| `pkg/provider/stt/elevenlabs/elevenlabs_test.go` | **Create** | Unit tests |
| `cmd/glyphoxa/main.go` | **Modify** | Register `"elevenlabs"` STT factory (~line 266) |
| `internal/config/loader.go` | **Modify** | Add `"elevenlabs"` to `ValidProviderNames["stt"]` (line 19) |

## Dependencies & Risks

**Dependencies:**
- `github.com/coder/websocket` — already in go.mod (used by Deepgram STT and ElevenLabs TTS)
- `encoding/base64` — stdlib
- `math` — stdlib (for `math.Exp` logprob conversion)

**Risks:**
- **Base64 overhead**: Each audio chunk is ~33% larger after base64 encoding. At 16kHz/16-bit/mono, a 30ms frame is ~960 bytes raw → ~1280 bytes base64. Negligible for WebSocket bandwidth.
- **Commit timing in Close()**: The writeLoop sends the commit as its last action before returning. The readLoop then receives the committed transcript and dispatches it before the WebSocket closes. The 5s timeout in Close() prevents unbounded blocking if the server is slow.
- **No keyword boosting**: ElevenLabs Scribe v2 does not support keyword boosting. Fantasy proper nouns may have higher word error rate compared to Deepgram with keywords. This is a known limitation.
- **Concurrent connection limits**: ElevenLabs limits concurrent WebSocket connections per API key. Short-lived sessions mitigate this, but `quota_exceeded` errors are handled as fatal.
- **90-second auto-commit**: Server auto-commits even in manual mode after 90 seconds. Our VAD segments are 1-15s so this is not an issue, but worth documenting.

### Research Insights — Risks

**Language code mismatch**: If a BCP-47 code like `"en-US"` is passed through unchanged, ElevenLabs may reject it or produce unexpected results. The buildURL method normalizes by taking the primary language subtag (before the first hyphen).

**Empty speech segments**: If the VAD triggers a session but the user doesn't speak (false positive), we send commit on an empty audio stream. ElevenLabs responds with a committed transcript containing empty text. The readLoop should handle this gracefully (emit transcript with empty text, which the pipeline already handles).

## References & Research

### Internal References

- STT Provider interface: `pkg/provider/stt/provider.go:83-96`
- STT SessionHandle interface: `pkg/provider/stt/provider.go:53-81`
- STT types (Transcript, WordDetail): `pkg/provider/stt/types.go`
- Deepgram STT (primary reference): `pkg/provider/stt/deepgram/deepgram.go`
- Deepgram STT tests: `pkg/provider/stt/deepgram/deepgram_test.go`
- ElevenLabs TTS (WebSocket reference): `pkg/provider/tts/elevenlabs/elevenlabs.go`
- Provider registry: `internal/config/registry.go`
- STT factory registration: `cmd/glyphoxa/main.go:234-266`
- Valid provider names: `internal/config/loader.go:19`
- Audio pipeline STT usage: `internal/app/audio_pipeline.go:159-261`
- Mock STT provider: `pkg/provider/stt/mock/mock.go`

### External References

- ElevenLabs Scribe v2 Realtime API reference: https://elevenlabs.io/docs/api-reference/speech-to-text/v-1-speech-to-text-realtime
- Event reference: https://elevenlabs.io/docs/eleven-api/guides/cookbooks/speech-to-text/realtime/event-reference
- Transcript commit strategies: https://elevenlabs.io/docs/eleven-api/guides/cookbooks/speech-to-text/realtime/transcripts-and-commit-strategies
- ElevenLabs blog — Scribe v2 launch: https://elevenlabs.io/blog/scribe-v2 (confirms `scribe_v2_realtime` model ID for WebSocket)
- `coder/websocket` docs: https://pkg.go.dev/github.com/coder/websocket
