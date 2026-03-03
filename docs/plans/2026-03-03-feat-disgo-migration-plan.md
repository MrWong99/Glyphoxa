---
title: "feat: Migrate Discord integration from discordgo to disgo"
type: feat
status: active
date: 2026-03-03
---

# feat: Migrate Discord integration from discordgo to disgo

## Overview

Full migration of the Discord integration from `bwmarrin/discordgo` to `disgoorg/disgo` (v0.19.2+) to support the DAVE (Discord Audio Video Encryption) protocol. Discord enforces DAVE for all non-stage voice calls as of 2026-03-01 — voice is completely broken (close code 4017) until this migration is done.

The migration covers both the voice/audio layer (`pkg/audio/discord/`) and the bot/interaction layer (`internal/discord/`), using disgo types directly throughout (no adapter layer). The audio pipeline above the transport layer (`internal/app/`, `internal/engine/`, `internal/agent/`) is unaffected due to the existing `audio.Platform`/`audio.Connection` interface abstraction.

**Scope:** ~20 files, ~169 discordgo references, 5 implementation phases.

## Problem Statement

Discord's DAVE (Discord Audio Video Encryption) protocol is now mandatory for all non-stage voice connections. The bot receives WebSocket close code 4017 ("E2EE/DAVE protocol required") immediately after joining any voice channel, triggering an infinite auto-reconnect loop. `bwmarrin/discordgo` does not implement DAVE and has no timeline for support. Voice functionality is 100% broken.

## Proposed Solution

Replace `bwmarrin/discordgo` with `disgoorg/disgo`, which supports DAVE via `disgoorg/godave` (CGO wrapper around Discord's reference C++ `libdave`). The migration is phased: voice layer first (unblocks voice immediately), then bot core, router, commands, and dashboard.

## Technical Approach

### Architecture Changes

#### 1. Voice: Channel-based → Interface-based

**Current (discordgo):** `OpusSend chan []byte` and `OpusRecv chan *discordgo.Packet` — goroutines read/write channels.

**Target (disgo):** `OpusFrameProvider` (pull model, called at 20ms cadence) and `OpusFrameReceiver` (callback per packet with resolved `userID`).

**Adapter design:** The `audio.Connection` interface (`OutputStream() chan<- AudioFrame`, `InputStreams() map[string]<-chan AudioFrame`) is preserved unchanged. The disgo interfaces are bridged internally:

```
OutputStream channel → Opus encoder → internal frame buffer → OpusFrameProvider.ProvideOpusFrame() [pull]
OpusFrameReceiver.ReceiveOpusFrame(userID, packet) [callback] → per-user Opus decoder → InputStreams channels
```

- `ProvideOpusFrame()` does a non-blocking channel read on an internal encoded-frame channel. On underrun, returns an Opus silence frame (5 bytes). This preserves the `<1.2s` latency budget without blocking.
- `ReceiveOpusFrame(userID, packet)` creates/reuses per-user Opus decoders and delivers decoded PCM to per-user input channels. The `userID snowflake.ID` is converted to `string` via `.String()` at the `audio.Connection` boundary (keeps the platform-agnostic interface Discord-independent).

#### 2. Events: Runtime → Creation-time registration

**Current:** `session.AddHandler(func)` called at any time.
**Target:** `bot.WithEventListenerFunc(func)` at `disgo.New()` construction.

**Resolved via closure capture:** Event listeners capture a `*CommandRouter` pointer set after construction. Same pattern already used in `main.go` with `**discordbot.Bot`.

```go
var router *CommandRouter
client, _ := disgo.New(token,
    bot.WithEventListenerFunc(func(e *events.ApplicationCommandInteractionCreate) {
        router.HandleCommand(e)
    }),
    // ... other listeners
)
router = newCommandRouter(...)
```

#### 3. Interactions: Monolithic → Split event types

**Current:** One `InteractionCreate` handler switches on `i.Type`.
**Target:** Four separate listeners:
- `events.ApplicationCommandInteractionCreate`
- `events.AutocompleteInteractionCreate`
- `events.ComponentInteractionCreate`
- `events.ModalSubmitInteractionCreate`

The `CommandRouter` is redesigned with four registration/dispatch methods instead of one.

#### 4. Voice state detection: BeforeUpdate comparison

**Current:** `handleVoiceStateUpdate` compares `vsu.BeforeUpdate.ChannelID` vs `vsu.ChannelID`.
**Target:** disgo's `events.GuildVoiceStateUpdate` provides `OldVoiceState` (from cache) and new state. Same comparison logic with different field names. Requires `cache.FlagVoiceStates` enabled.

#### 5. SSRC-to-user mapping: Eliminated

**Current:** `handleSpeakingUpdate` builds `ssrcUser map[uint32]string`, re-keys input channels.
**Target:** disgo resolves SSRC→user internally. `ReceiveOpusFrame(userID, packet)` provides the user ID directly. The entire `ssrcUser` map, `handleSpeakingUpdate` handler, and channel re-keying logic are deleted.

### Complete Type Mapping

| discordgo | disgo | Notes |
|---|---|---|
| `*discordgo.Session` | `*bot.Client` | Struct since v0.19.0 |
| `discordgo.New("Bot "+token)` | `disgo.New(token, ...bot.ConfigOpt)` | Token format handled internally |
| `session.Open()` | `client.OpenGateway(ctx)` | Explicit, separate from construction |
| `session.Close()` | `client.Close(ctx)` | Takes context |
| `session.Identify.Intents` | `bot.WithGatewayConfigOpts(gateway.WithIntents(...))` | At construction |
| `session.AddHandler(func)` | `bot.WithEventListenerFunc(func)` | At construction (generic) |
| `session.State.User.ID` | `client.ID()` | Returns `snowflake.ID` |
| `session.State.VoiceState(g, u)` | `client.Caches.VoiceState(g, u)` | Returns `(discord.VoiceState, bool)` |
| `session.ChannelVoiceJoin(g, ch, m, d)` | `voiceMgr.CreateConn(g)` + `conn.Open(ctx, ch, m, d)` | Two-step |
| `*discordgo.VoiceConnection` | `voice.Conn` | Interface |
| `vc.OpusSend chan []byte` | `OpusFrameProvider.ProvideOpusFrame()` | Pull model |
| `vc.OpusRecv chan *Packet` | `OpusFrameReceiver.ReceiveOpusFrame()` | Callback model |
| `vc.Speaking(bool)` | `conn.SetSpeaking(ctx, voice.SpeakingFlagMicrophone)` | Flags-based |
| `vc.Disconnect()` | `conn.Close(ctx)` | Takes context |
| `*discordgo.Packet` | `*voice.Packet` | Same fields: SSRC, Opus, Timestamp |
| `*discordgo.VoiceStateUpdate` | `*events.GuildVoiceStateUpdate` | Has old/new state via cache |
| `*discordgo.VoiceSpeakingUpdate` | *(eliminated)* | disgo handles SSRC mapping internally |
| `*discordgo.InteractionCreate` | Split into 4 event types | See §3 above |
| `i.ApplicationCommandData()` | `e.SlashCommandInteractionData()` | On `ApplicationCommandInteractionCreate` |
| `i.ModalSubmitData()` | `e.Data` (embedded) | On `ModalSubmitInteractionCreate` |
| `i.MessageComponentData()` | `e.ButtonInteractionData()` etc. | On `ComponentInteractionCreate` |
| `s.InteractionRespond(i, resp)` | `e.CreateMessage(discord.MessageCreate{...})` | Method on event |
| `s.InteractionRespond(i, deferred)` | `e.DeferCreateMessage(ephemeral)` | Method on event |
| `s.InteractionRespond(i, modal)` | `e.Modal(discord.ModalCreate{...})` | Method on event |
| `s.InteractionRespond(i, autocomplete)` | `e.AutocompleteResult(choices)` | Method on event |
| `s.FollowupMessageCreate(i, params)` | `client.Rest.CreateFollowupMessage(appID, token, msg)` | Via REST client |
| `session.ApplicationCommandBulkOverwrite(...)` | `client.Rest.SetGuildCommands(appID, gID, cmds)` | REST method |
| `session.ApplicationCommandDelete(...)` | `client.Rest.DeleteGuildCommand(appID, gID, cmdID)` | REST method |
| `*discordgo.ApplicationCommand` | `discord.SlashCommandCreate` | For definitions |
| `*discordgo.ApplicationCommandOption` | `discord.ApplicationCommandOption` | Interface, concrete types per kind |
| `discordgo.ApplicationCommandOptionSubCommand` | `discord.ApplicationCommandOptionTypeSubCommand` | Constant |
| `discordgo.ApplicationCommandOptionString` | `discord.ApplicationCommandOptionTypeString` | Constant |
| `*discordgo.ApplicationCommandOptionChoice` | `discord.AutocompleteChoiceString` etc. | Typed choices |
| `*discordgo.MessageEmbed` | `discord.Embed` | Direct struct or `NewEmbedBuilder()` |
| `*discordgo.MessageEmbedField` | `discord.EmbedField` | Same structure |
| `*discordgo.MessageEmbedFooter` | `discord.EmbedFooter` (via builder) | `SetFooter(text, iconURL)` |
| `discordgo.MessageFlagsEphemeral` | `discord.MessageFlagEphemeral` | Flag constant |
| `*discordgo.WebhookParams` | `discord.MessageCreate` | For follow-ups |
| `discordgo.ActionsRow` | `discord.NewActionRow(...)` | Function returning `ActionRowComponent` |
| `discordgo.Button` | `discord.NewPrimaryButton(label, customID)` etc. | Constructor per style |
| `discordgo.TextInput` | `discord.NewShortTextInput(customID)` etc. | Constructor per style |
| `*discordgo.Member` | `discord.Member` / `*discord.ResolvedMember` | Value type |
| `i.Member.Roles` | `e.Member().RoleIDs` | `[]snowflake.ID` not `[]string` |
| `i.Member.User.ID` | `e.User().ID` | `snowflake.ID` |
| `i.Member.User.Username` | `e.User().Username` | Same |
| `i.Member.DisplayName()` | `e.Member().Nick` or `e.User().EffectiveName()` | Different accessor |
| `*discordgo.MessageAttachment` | `discord.Attachment` | Same fields |
| `session.ChannelMessageSendEmbed(ch, embed)` | `client.Rest.CreateMessage(ch, discord.MessageCreate{Embeds: [...]})` | REST |
| `session.ChannelMessageEditEmbed(ch, msg, embed)` | `client.Rest.UpdateMessage(ch, msg, discord.MessageUpdate{Embeds: ...})` | REST |
| `discordgo.IntentsGuildMessages` | `gateway.IntentGuildMessages` | Same concept |
| `discordgo.IntentsGuildVoiceStates` | `gateway.IntentGuildVoiceStates` | Same concept |
| `discordgo.IntentsGuilds` | `gateway.IntentGuilds` | Same concept |

### Implementation Phases

#### Phase 0: Build Dependencies and go.mod

**Goal:** Get the project compiling with disgo as a dependency alongside discordgo (temporarily).

**Tasks:**
- [ ] Add `github.com/disgoorg/disgo v0.19.2` to `go.mod`
- [ ] Add `github.com/disgoorg/godave` to `go.mod`
- [ ] Add `github.com/disgoorg/snowflake/v2` to `go.mod`
- [ ] Document libdave installation in CLAUDE.md prerequisites (similar to existing whisper.cpp/ONNX docs)
- [ ] Add `make dave-libs` target to Makefile for downloading/building libdave shared library
- [ ] Verify CGO compilation succeeds with godave on the target platform
- [ ] Run `make check` — existing tests must still pass

**Files:**
- `go.mod` — add dependencies
- `Makefile` — add `dave-libs` target
- `CLAUDE.md` — update prerequisites section

**Success criteria:** `go build ./...` succeeds with both discordgo and disgo importable. Existing tests pass.

---

#### Phase 1: Voice Layer (`pkg/audio/discord/`)

**Goal:** Replace the voice transport to support DAVE. Preserve the `audio.Platform`/`audio.Connection` interface contract so upstream code is unaffected.

**Tasks:**

- [ ] **`pkg/audio/discord/platform.go`** — Replace `*discordgo.Session` with `*bot.Client` and `voice.Manager`

  ```go
  type Platform struct {
      client   *bot.Client
      voiceMgr voice.Manager
      guildID  snowflake.ID
  }

  func (p *Platform) Connect(ctx context.Context, channelID string) (audio.Connection, error) {
      chID := snowflake.MustParse(channelID)
      conn := p.voiceMgr.CreateConn(p.guildID)
      if err := conn.Open(ctx, chID, false, false); err != nil {
          p.voiceMgr.RemoveConn(p.guildID)
          return nil, fmt.Errorf("discord: voice connect: %w", err)
      }
      return newConnection(conn, p.client, p.guildID)
  }
  ```

- [ ] **`pkg/audio/discord/connection.go`** — Complete rewrite

  Replace `*discordgo.VoiceConnection` with `voice.Conn`. Key changes:

  1. **Implement `OpusFrameProvider`** (send adapter):
     ```go
     type opusSender struct {
         frames chan []byte  // buffered channel of encoded Opus frames
         done   chan struct{}
     }
     func (s *opusSender) ProvideOpusFrame() ([]byte, error) {
         select {
         case frame := <-s.frames:
             return frame, nil
         case <-s.done:
             return nil, io.EOF
         default:
             return opusSilenceFrame, nil  // no audio ready, send silence
         }
     }
     ```

  2. **Implement `OpusFrameReceiver`** (receive adapter):
     ```go
     type opusReceiver struct {
         mu       sync.RWMutex
         inputs   map[string]chan audio.AudioFrame  // keyed by userID string
         decoders map[string]*gopus.Decoder
         changeCb func(audio.Event)
         done     chan struct{}
     }
     func (r *opusReceiver) ReceiveOpusFrame(userID snowflake.ID, packet *voice.Packet) error {
         uid := userID.String()
         // Get or create decoder + channel for this user
         // Decode Opus to PCM, deliver AudioFrame to per-user channel
         // Emit EventJoin on first packet from new user
     }
     func (r *opusReceiver) CleanupUser(userID snowflake.ID) {
         // Close per-user channel, remove decoder, emit EventLeave
     }
     ```

  3. **Implement `sendLoop()`** — reads from `output chan audio.AudioFrame`, resamples to 48kHz stereo, encodes to Opus, pushes to `opusSender.frames` channel. Calls `conn.SetSpeaking(ctx, flags)` on activity transitions.

  4. **Register voice state handler** via client event listener for join/leave detection (replaces `handleVoiceStateUpdate`). Use disgo cache's old voice state vs new state comparison.

  5. **Delete `handleSpeakingUpdate`** and `ssrcUser` map entirely — disgo resolves SSRC→user internally.

  6. **Wire DAVE** via voice manager config:
     ```go
     voice.WithDaveSessionCreateFunc(golibdave.NewSession)
     ```

  7. **Disconnect cleanup:** `conn.Close(ctx)` + `voiceMgr.RemoveConn(guildID)`. Preserve `sync.Once` pattern. Deregister event listeners.

- [ ] **`pkg/audio/discord/opus.go`** — Keep `layeh.com/gopus` encoder/decoder helpers. Update constants only if needed (Discord Opus format is unchanged: 48kHz, stereo, 20ms, 960 samples). Delete `int16sToBytes`/`bytesToInt16s` if no longer needed (depends on decoder output format).

- [ ] **`pkg/audio/discord/platform_test.go`** — Rewrite tests for the new interface model. Cannot directly construct `voice.Conn` with fake channels; instead, test through the `OpusFrameProvider`/`OpusFrameReceiver` implementations directly. Test:
  - `opusSender.ProvideOpusFrame()` returns silence on empty buffer, returns frame when available, returns `io.EOF` on close
  - `opusReceiver.ReceiveOpusFrame()` creates per-user channels, decodes Opus, emits join events
  - `opusReceiver.CleanupUser()` closes channels, emits leave events
  - `Connection.Disconnect()` is safe to call multiple times (`sync.Once`)
  - Race detector passes with concurrent send/receive/disconnect

- [ ] **Verify `audio.Connection` contract** — run `make test` for `internal/app/` and `pkg/audio/` to confirm the interface is preserved. No changes should be needed above the transport layer.

**Files changed:**
- `pkg/audio/discord/platform.go` — rewrite
- `pkg/audio/discord/connection.go` — rewrite
- `pkg/audio/discord/opus.go` — minor updates
- `pkg/audio/discord/platform_test.go` — rewrite

**Files NOT changed:** `pkg/audio/platform.go`, `pkg/audio/mock/mock.go`, `internal/app/audio_pipeline.go`, `internal/app/session_manager.go`

**Success criteria:** Voice connects to a DAVE-enabled Discord channel. Audio sends and receives. Participant join/leave events fire. `make test` passes for `pkg/audio/...` and `internal/app/...`.

---

#### Phase 2: Bot Core (`internal/discord/bot.go`)

**Goal:** Replace `*discordgo.Session` lifecycle with `*bot.Client`.

**Tasks:**

- [ ] **`internal/discord/bot.go`** — Rewrite `Bot` struct and lifecycle

  ```go
  type Bot struct {
      mu        sync.RWMutex
      client    *bot.Client
      platform  *discordaudio.Platform
      router    *CommandRouter
      perms     *PermissionChecker
      guildID   snowflake.ID
      done      chan struct{}
      closeOnce sync.Once
  }
  ```

  `New()` changes:
  - Create `disgo.New(token, ...ConfigOpt)` with:
    - `bot.WithGatewayConfigOpts(gateway.WithIntents(IntentGuilds, IntentGuildVoiceStates, IntentGuildMessages))`
    - `bot.WithVoiceManagerConfigOpts(voice.WithDaveSessionCreateFunc(golibdave.NewSession))`
    - `bot.WithCacheConfigOpts(cache.WithCaches(cache.FlagVoiceStates, cache.FlagGuilds, cache.FlagMembers))`
    - Four `bot.WithEventListenerFunc` calls (one per interaction type), each delegating to the router
    - One `bot.WithEventListenerFunc` for `events.GuildVoiceStateUpdate` (forwarded to the active connection)
  - Call `client.OpenGateway(ctx)` to connect
  - Construct `Platform` with `client.VoiceManager()` and parsed `guildID`

  `Run()` changes:
  - Replace `session.ApplicationCommandBulkOverwrite()` with `client.Rest.SetGuildCommands(client.ID(), guildID, cmds)`
  - Block on `<-ctx.Done()`

  `Close()` changes:
  - Replace per-command `session.ApplicationCommandDelete()` with `client.Rest.SetGuildCommands(client.ID(), guildID, nil)` (empty slice clears all)
  - Replace `session.Close()` with `client.Close(ctx)`

  Accessors:
  - `Session() *discordgo.Session` → `Client() *bot.Client`
  - `Platform()`, `GuildID()`, `Router()`, `Permissions()` — same concept, update types

- [ ] **`internal/discord/bot_test.go`** — Update `PermissionChecker` tests to use disgo interaction types (member/roles).

**Files changed:**
- `internal/discord/bot.go` — rewrite
- `internal/discord/bot_test.go` — update

**Success criteria:** Bot connects to Discord gateway, receives events, and can register commands. `make test` passes for `internal/discord/...`.

---

#### Phase 3: Router & Response Helpers

**Goal:** Redesign the `CommandRouter` for disgo's split event model and update response helpers.

**Tasks:**

- [ ] **`internal/discord/router.go`** — Redesign `CommandRouter`

  Replace the monolithic `Handle(s, i)` with four dispatch methods:

  ```go
  type CommandRouter struct {
      commands       map[string]commandEntry        // key → definition + handler
      autocomplete   map[string]AutocompleteFunc
      components     map[string]ComponentFunc
      componentPfx   map[string]ComponentFunc
      modals         map[string]ModalFunc
  }

  // New handler type aliases
  type CommandFunc     func(e *events.ApplicationCommandInteractionCreate)
  type AutocompleteFunc func(e *events.AutocompleteInteractionCreate)
  type ComponentFunc   func(e *events.ComponentInteractionCreate)
  type ModalFunc       func(e *events.ModalSubmitInteractionCreate)

  func (r *CommandRouter) HandleCommand(e *events.ApplicationCommandInteractionCreate)
  func (r *CommandRouter) HandleAutocomplete(e *events.AutocompleteInteractionCreate)
  func (r *CommandRouter) HandleComponent(e *events.ComponentInteractionCreate)
  func (r *CommandRouter) HandleModal(e *events.ModalSubmitInteractionCreate)
  ```

  Registration methods stay the same names but accept new function types. `ApplicationCommands()` returns `[]discord.ApplicationCommandCreate` instead of `[]*discordgo.ApplicationCommand`.

- [ ] **`internal/discord/respond.go`** — Simplify or eliminate helpers

  Since disgo puts response methods directly on events, consider whether wrappers add value. Options:

  **Option A (Eliminate):** Delete `respond.go` entirely. Command handlers call `e.CreateMessage()`, `e.DeferCreateMessage()`, etc. directly. Ephemeral flag is `discord.MessageCreate{}.WithEphemeral(true)`.

  **Option B (Thin wrappers for common patterns):** Keep helpers for patterns repeated across many handlers:
  ```go
  func RespondEphemeral(e *events.ApplicationCommandInteractionCreate, content string) error {
      return e.CreateMessage(discord.MessageCreate{Content: content, Flags: discord.MessageFlagEphemeral})
  }
  func RespondEmbed(e *events.ApplicationCommandInteractionCreate, embed discord.Embed) error {
      return e.CreateMessage(discord.MessageCreate{Embeds: []discord.Embed{embed}})
  }
  func RespondError(e *events.ApplicationCommandInteractionCreate, err error) error {
      return RespondEphemeral(e, "Error: "+err.Error())
  }
  func DeferReply(e *events.ApplicationCommandInteractionCreate) error {
      return e.DeferCreateMessage(true)
  }
  func FollowUp(e *events.ApplicationCommandInteractionCreate, content string) error {
      _, err := e.Client().Rest.CreateFollowupMessage(e.ApplicationID(), e.Token(),
          discord.MessageCreate{Content: content})
      return err
  }
  ```

  Recommended: **Option B** — keeps command handlers concise and centralizes the ephemeral/error patterns.

**Files changed:**
- `internal/discord/router.go` — rewrite
- `internal/discord/respond.go` — rewrite

**Success criteria:** Router compiles with new handler types. Response helpers work with disgo events.

---

#### Phase 4: Command Handlers (`internal/discord/commands/`)

**Goal:** Rewrite all command handlers to use disgo types. This is the highest-volume phase (8 modules, ~2,800 lines) but mostly mechanical.

**Per-module pattern:**

1. Replace `func(s *discordgo.Session, i *discordgo.InteractionCreate)` → `func(e *events.ApplicationCommandInteractionCreate)` (or appropriate event type for autocomplete/component/modal handlers)
2. Replace `i.ApplicationCommandData()` → `e.SlashCommandInteractionData()`
3. Replace option extraction (`data.Options[0].Options`) → `data.Options` (disgo provides typed accessors: `data.String("name")`, `data.Int("name")`)
4. Replace response calls (see §respond.go above)
5. Replace `*discordgo.ApplicationCommand` definitions → `discord.SlashCommandCreate` with typed option structs
6. Replace autocomplete choice types → `discord.AutocompleteChoiceString{Name, Value}`
7. Replace component/modal types → disgo constructors (`discord.NewPrimaryButton`, `discord.NewShortTextInput`, etc.)
8. Replace `i.Member` access → `e.Member()` / `e.User()`
9. Update tests

**Tasks (per module):**

- [ ] **`commands/session.go`** (157 lines)
  - Replace `s.State.VoiceState(guildID, userID)` → `e.Client().Caches.VoiceState(guildID, userID)`
  - Replace `i.Member.User.ID` → `e.User().ID.String()`
  - Replace command definition to `discord.SlashCommandCreate`
  - Update `subcommandStringOption` helper to use disgo's `data.String("name")`

- [ ] **`commands/session_test.go`** — Update test fixtures to use disgo types

- [ ] **`commands/npc.go`** (339 lines)
  - 6 subcommand handlers + 3 autocomplete handlers
  - Replace `*discordgo.ApplicationCommandInteractionDataOption` extraction → `data.String("name")`
  - Replace autocomplete result → `e.AutocompleteResult([]discord.AutocompleteChoice{...})`

- [ ] **`commands/npc_test.go`** — Update fixtures

- [ ] **`commands/entity.go`** (449 lines)
  - Largest command module: CRUD + autocomplete + modal + component handlers
  - Replace modal creation → `discord.NewModalCreate(customID, title, components)`
  - Replace component creation → `discord.NewActionRow(discord.NewPrimaryButton(...))`
  - Replace `i.ModalSubmitData()` → `e.Data` (embedded on `ModalSubmitInteractionCreate`)

- [ ] **`commands/entity_test.go`** — Update fixtures

- [ ] **`commands/entity_modal.go`** (93 lines)
  - Modal handler: replace `i.ModalSubmitData().Components` → `e.Data.Text("customID")`

- [ ] **`commands/campaign.go`** (261 lines)
  - Campaign CRUD + autocomplete
  - Replace component interactions for campaign selection

- [ ] **`commands/campaign_test.go`** — Update fixtures

- [ ] **`commands/feedback.go`** (212 lines)
  - Modal-based feedback submission

- [ ] **`commands/recap.go`** (276 lines)
  - Embed-heavy: replace all `*discordgo.MessageEmbed` → `discord.Embed` or `discord.NewEmbedBuilder()`

- [ ] **`commands/attachment.go`** (112 lines)
  - Replace `*discordgo.MessageAttachment` → `discord.Attachment`

- [ ] **`commands/attachment_test.go`** — Update fixtures

**Files changed:** All 13 files in `internal/discord/commands/`

**Success criteria:** All command handlers compile and pass their tests. `make test` passes for `internal/discord/...`.

---

#### Phase 5: Dashboard, Permissions & Mock

**Goal:** Update remaining Discord infrastructure.

**Tasks:**

- [ ] **`internal/discord/dashboard.go`** — Replace `*discordgo.Session` with `*bot.Client`
  - `session.ChannelMessageSendEmbed(ch, embed)` → `client.Rest.CreateMessage(ch, discord.MessageCreate{Embeds: []discord.Embed{embed}})`
  - `session.ChannelMessageEditEmbed(ch, msg, embed)` → `client.Rest.UpdateMessage(ch, msg, discord.MessageUpdate{Embeds: &[]discord.Embed{embed}})`
  - `*discordgo.MessageEmbed` → `discord.Embed`

- [ ] **`internal/discord/dashboard_test.go`** — Update embed builder tests

- [ ] **`internal/discord/permissions.go`** — Update `IsDM()` to accept relevant disgo types
  - `i.Member.Roles` → `e.Member().RoleIDs` (type changes from `[]string` to `[]snowflake.ID`)
  - Consider making `IsDM` accept `*discord.ResolvedMember` + the `dmRoleID snowflake.ID` for flexibility across event types

- [ ] **`internal/discord/mock/session.go`** — Rewrite or remove
  - Current mock wraps `InteractionRespond` and `FollowupMessageCreate` which no longer exist as session methods
  - If response helpers (Option B above) are used, mock the event types instead
  - Consider whether mock is still needed — disgo's event types may be constructible enough for direct use in tests

**Files changed:**
- `internal/discord/dashboard.go`
- `internal/discord/dashboard_test.go`
- `internal/discord/permissions.go`
- `internal/discord/mock/session.go`

**Success criteria:** Dashboard updates messages. Permission checks work. All mocks compile.

---

#### Phase 6: Cleanup

**Goal:** Remove discordgo dependency entirely.

**Tasks:**

- [ ] **`cmd/glyphoxa/main.go`** — Update Discord audio factory
  - Replace `discordbot.New(ctx, Config{Token, GuildID, DMRoleID})` with updated constructor
  - Replace `bot.Session()` usages with `bot.Client()`
  - Update command module construction (same constructors, new types)
  - `guildID` becomes `snowflake.ID` throughout

- [ ] **`go.mod`** — Remove `github.com/bwmarrin/discordgo`, run `go mod tidy` to clean indirect deps (`gorilla/websocket` if unused)
- [ ] **Verify** no file imports `bwmarrin/discordgo`: `grep -r "bwmarrin/discordgo" --include="*.go"`
- [ ] Run full `make check` (fmt + vet + test with race detector)
- [ ] Run `make lint`

**Files changed:**
- `cmd/glyphoxa/main.go` — update wiring
- `go.mod` / `go.sum` — dependency cleanup

**Success criteria:** `grep -r discordgo --include="*.go"` returns nothing. `make check` passes. `make lint` passes.

## Acceptance Criteria

### Functional Requirements

- [ ] Bot joins a DAVE-enabled Discord voice channel without close code 4017
- [ ] Audio is received from participants and routed to per-user `InputStreams` channels
- [ ] Audio from `OutputStream` channel is sent to the voice channel
- [ ] Participant join/leave events fire correctly for `OnParticipantChange` callbacks
- [ ] All slash commands (`/session`, `/npc`, `/entity`, `/campaign`, `/feedback`) work
- [ ] Autocomplete works for NPC names, entity names, campaign names
- [ ] Modal dialogs work for entity creation and feedback submission
- [ ] Component interactions (buttons) work for entity/campaign selection
- [ ] Dashboard embed updates display correctly
- [ ] DM permission checks work for restricted commands
- [ ] Graceful shutdown cleans up voice connections and deregisters commands

### Non-Functional Requirements

- [ ] No race conditions: `make test` with `-race -count=1` passes
- [ ] Latency budget preserved: voice round-trip stays under 2.0s hard limit
- [ ] No goroutine leaks: disconnect and reconnection scenarios don't leak goroutines
- [ ] `Disconnect()` is safe to call multiple times (`sync.Once`)
- [ ] All tests use `t.Parallel()` and table-driven patterns per CLAUDE.md
- [ ] All exported symbols have godoc comments
- [ ] Error wrapping uses `%w` with package prefix

### Quality Gates

- [ ] `make check` passes (fmt + vet + test)
- [ ] `make lint` passes
- [ ] Zero imports of `bwmarrin/discordgo` remain
- [ ] Compile-time interface assertions: `var _ audio.Platform = (*Platform)(nil)`, `var _ audio.Connection = (*Connection)(nil)`

## Dependencies & Prerequisites

- **libdave shared library** — Required for DAVE support via godave. Must be installed on build system.
- **CGO enabled** — Already required for libopus and ONNX Runtime. No new constraint.
- **disgo v0.19.2+** — Latest stable version with DAVE support and v0.19.0 breaking changes applied.
- **Go 1.23+** — Required by disgo for `iter.Seq` range-over-func (project already uses Go 1.26).

## Risk Analysis & Mitigation

| Risk | Impact | Likelihood | Mitigation |
|---|---|---|---|
| libdave build failures on CI | Blocks all builds | Medium | Document install steps, add CI install target, test on clean VM first |
| disgo voice API doesn't match expectations | Blocks Phase 1 | Low | Prototype voice connection in isolation before full migration |
| `ReceiveOpusFrame` userID is sometimes zero | Corrupts audio routing | Low | Add nil/zero check, fallback to SSRC string key if needed |
| Initialization order issues with creation-time listeners | Blocks Phase 2 | Low | Closure capture pattern already proven in codebase |
| Command handler migration introduces regressions | Breaks user-facing commands | Medium | Migrate one module at a time, run tests after each |
| Auto-reconnect conflicts with `sync.Once` disconnect | Zombie connections | Medium | Disable auto-reconnect initially, handle reconnection explicitly |

## References & Research

### Internal References

- Brainstorm: `docs/brainstorms/2026-03-03-disgo-migration-brainstorm.md`
- TODOS #16: `TODOS.md:116-148`
- Voice connection lifecycle fix: commit `81fc51f`
- Audio platform interface: `pkg/audio/platform.go:58-113`
- Current Discord implementation: `pkg/audio/discord/connection.go`, `pkg/audio/discord/platform.go`
- Bot layer: `internal/discord/bot.go`
- Command router: `internal/discord/router.go`
- App wiring: `cmd/glyphoxa/main.go:363-385`

### External References

- [disgo repository](https://github.com/disgoorg/disgo) — v0.19.2
- [disgo voice package](https://pkg.go.dev/github.com/disgoorg/disgo@v0.19.2/voice)
- [disgo events package](https://pkg.go.dev/github.com/disgoorg/disgo@v0.19.2/events)
- [disgo handler package](https://pkg.go.dev/github.com/disgoorg/disgo@v0.19.2/handler)
- [godave repository](https://github.com/disgoorg/godave) — DAVE protocol Go bindings
- [Discord DAVE whitepaper](https://daveprotocol.com/)
- [Discord DAVE announcement](https://discord.com/blog/bringing-dave-to-all-discord-platforms)
- [disgo v0.19.0 breaking changes](https://github.com/disgoorg/disgo/releases/tag/v0.19.0)
