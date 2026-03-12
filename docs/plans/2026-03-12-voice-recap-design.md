# Voice Recap Design — "Previously On..." Session Recaps

**Issue:** #33
**Date:** 2026-03-12

## Overview

A new `/session voice-recap` slash command generates a dramatic narrative recap from a session transcript, voices it using the GM helper NPC's TTS voice, plays it into the voice channel, and persists both text and audio in PostgreSQL. Generation happens on-demand when the command is fired.

## Command Flow

```
/session voice-recap [session_id]
    |
    +-- No session_id? -> Find last session for current campaign via ListSessions()
    |
    +-- Recap already stored? -> Play cached audio via Mixer
    |
    +-- No cached recap?
        +-- Fetch transcript from SessionStore (L1)
        +-- LLM.Complete() with dramatic narrator prompt -> recap text
        +-- TTS.SynthesizeStream() with GM helper NPC voice -> PCM audio
        +-- Store text + audio in PostgreSQL via RecapStore
        +-- Play audio via Mixer + post text embed
```

## GM Helper NPC Flag

New `GMHelper bool` field on `NPCConfig`:

```go
type NPCConfig struct {
    // ... existing fields ...
    GMHelper bool `yaml:"gm_helper"` // designates this NPC as the GM helper
}
```

Constraints:
- At most one NPC per campaign can be `gm_helper: true` — config validation rejects duplicates.
- If no NPC is flagged, voice-recap falls back to the first NPC in the config list.
- The GM helper participates in sessions like any other NPC — the flag marks it for special duties.

```yaml
npcs:
  - name: "The Chronicler"
    personality: "A wise, omniscient narrator..."
    gm_helper: true
    voice:
      provider: elevenlabs
      voice_id: narrator_deep
```

Full GM helper behavior (general questions, lookup, dice rolls) is tracked in a separate issue.

## Session History per Campaign

New method on `SessionStore`:

```go
type SessionStore interface {
    // ... existing methods ...
    ListSessions(ctx context.Context, campaignID string, limit int) ([]SessionInfo, error)
}
```

New type:

```go
type SessionInfo struct {
    SessionID  string
    CampaignID string
    StartedAt  time.Time
    EndedAt    time.Time // zero if still active
}
```

Requires a `sessions` table in PostgreSQL — a row inserted on `/session start`, `EndedAt` updated on `/session stop`.

## RecapStore

Interface in `pkg/memory/recap_store.go`:

```go
type RecapStore interface {
    SaveRecap(ctx context.Context, recap Recap) error
    GetRecap(ctx context.Context, sessionID string) (*Recap, error)
}
```

Type:

```go
type Recap struct {
    SessionID   string
    CampaignID  string
    Text        string
    AudioData   []byte
    SampleRate  int
    Channels    int
    Duration    time.Duration
    GeneratedAt time.Time
}
```

PostgreSQL table (created within tenant schema — no `tenant_id` needed due to schema-per-tenant isolation):

```sql
CREATE TABLE recaps (
    session_id   TEXT PRIMARY KEY REFERENCES sessions(session_id),
    campaign_id  TEXT NOT NULL,
    text         TEXT NOT NULL,
    audio_data   BYTEA NOT NULL,
    sample_rate  INT NOT NULL,
    channels     INT NOT NULL,
    duration_ns  BIGINT NOT NULL,
    generated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

## Recap Generator

New `RecapGenerator` in `internal/session/recap_generator.go`:

```go
type RecapGenerator struct {
    llm   llm.Provider
    tts   tts.Provider
    store memory.RecapStore
}

func NewRecapGenerator(llm llm.Provider, tts tts.Provider, store memory.RecapStore) *RecapGenerator

func (g *RecapGenerator) Generate(ctx context.Context, sessionID, campaignID string,
    sessionStore memory.SessionStore, voice tts.VoiceProfile) (*memory.Recap, error)
```

Dramatic narrator prompt:

> You are the dramatic narrator of an epic tabletop RPG campaign. Craft a gripping "Previously On..." recap from the session transcript below.
>
> Guidelines:
> - 200-300 words (~90-120 seconds spoken)
> - Third person, past tense, vivid cinematic language
> - Open with a strong hook: a memorable moment, looming threat, or unresolved question
> - Highlight: key decisions, secrets revealed, dangers faced, bonds forged or broken
> - Close with a cliffhanger that sets the stage for the next session
> - Do NOT include dice rolls or mechanical terms — narrate outcomes only
> - Do NOT invent events not in the transcript

Flow within `Generate()`:
1. `sessionStore.GetRecent(ctx, sessionID, 24*time.Hour)` -> transcript entries
2. Format entries into `[speaker]: text` block
3. `llm.Complete()` with dramatic prompt, temperature 0.7 -> recap text
4. Feed text sentence-by-sentence into `tts.SynthesizeStream()` with GM helper voice -> collect PCM bytes
5. `store.SaveRecap()` -> persist
6. Return `*Recap`

Uses `Complete()` not `StreamCompletion()` since full text is needed before TTS. Timeout: ~2 minutes.

## Commands

### `/session voice-recap [session_id]` (new)

New `VoiceRecapCommands` in `internal/discord/commands/voice_recap.go`:
- Dependencies: `SessionManager`, `RecapGenerator`, `RecapStore`, `SessionStore`, `PermissionChecker`
- Resolves voice from the GM helper NPC (falls back to first NPC)
- Flow:
  1. Check permissions (DM role)
  2. Resolve session ID — explicit param, or `SessionStore.ListSessions(campaignID, 1)`
  3. Check `RecapStore.GetRecap(sessionID)` — if cached, skip to playback
  4. If not cached, call `RecapGenerator.Generate()` (deferred reply, ~30-60s)
  5. Play audio via `Mixer.Enqueue(segment, priority)` into active voice connection
  6. Post recap text as Discord embed

### `/session recap [session_id]` (updated)

- Add optional `session_id` parameter
- If no session ID provided, use `SessionStore.ListSessions(campaignID, 1)` instead of requiring an active session
- Otherwise behavior unchanged (text embed, no audio)

## File Summary

### New files

| File | Purpose |
|------|---------|
| `pkg/memory/recap_store.go` | `RecapStore` interface |
| `pkg/memory/mock/recap_store.go` | Hand-written mock |
| `pkg/memory/postgres/recap_store.go` | PostgreSQL implementation |
| `pkg/memory/postgres/recap_store_test.go` | Integration tests |
| `internal/session/recap_generator.go` | LLM prompt + TTS orchestration |
| `internal/session/recap_generator_test.go` | Unit tests with mock LLM/TTS/store |
| `internal/discord/commands/voice_recap.go` | `/session voice-recap` handler |
| `internal/discord/commands/voice_recap_test.go` | Handler tests |

### Modified files

| File | Change |
|------|--------|
| `internal/config/config.go` | Add `GMHelper bool` to `NPCConfig`, validation |
| `internal/config/config_test.go` | Test duplicate GM helper rejection |
| `pkg/memory/store.go` | Add `ListSessions()` to `SessionStore` |
| `pkg/memory/types.go` | Add `SessionInfo`, `Recap` structs |
| `pkg/memory/postgres/session_store.go` | Implement `ListSessions()`, `sessions` table |
| `pkg/memory/mock/session_store.go` | Add `ListSessions()` to mock |
| `internal/discord/commands/session.go` | Add `voice-recap` subcommand definition |
| `internal/discord/commands/recap.go` | Add optional `session_id`, `ListSessions()` fallback |
| `internal/app/app.go` | Construct `RecapGenerator`, GM helper lookup |
| `cmd/glyphoxa/main.go` | Wire `VoiceRecapCommands` |
| `pkg/memory/postgres/migrate.go` | Add `sessions` and `recaps` table migrations |

## Testing Strategy

- **RecapGenerator**: table-driven tests with mock LLM, mock TTS, mock RecapStore — verify prompt content, audio concatenation, error paths
- **RecapStore (Postgres)**: integration test — save/get round-trip
- **ListSessions**: integration test — insert sessions, verify ordering
- **VoiceRecapCommands**: unit test handler logic with mocks — session resolution, cache hit/miss, permission checks
- **Config validation**: reject duplicate `gm_helper: true`
- All tests `t.Parallel()`, race detector on
