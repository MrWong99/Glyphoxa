# Voice State Proxy — Detailed Implementation Plan (Option B)

**Date:** 2026-03-22
**Status:** Ready for implementation
**Based on:** Deep analysis of disgo v0.19.3 internals, Discord API docs, Glyphoxa codebase

---

## Executive Summary

The voice state proxy approach is **fully feasible** using disgo's existing public API with **no fork or patches required**. The voice WebSocket and UDP connections are 100% independent from the main Discord gateway — the main gateway is only needed to send Opcode 4 (join/leave voice channel) and receive VOICE_STATE_UPDATE / VOICE_SERVER_UPDATE dispatch events.

---

## 1. How disgo Voice Actually Works (Source-Level Analysis)

### 1.1 The Voice Connection Lifecycle

From `voice/conn.go`, the normal flow through `conn.Open()` is:

```
conn.Open(ctx, channelID, selfMute, selfDeaf)
  │
  ├── voiceStateUpdateFunc(ctx, guildID, &channelID, selfMute, selfDeaf)
  │     └── This is bot.Client.UpdateVoiceState() — sends Opcode 4 via main gateway
  │
  └── blocks on c.openedChan until SessionDescription arrives
        └── SessionDescription = final step of voice WebSocket handshake
```

Two main gateway dispatch events must arrive (routed by `bot/handlers/voice_handlers.go`):

1. **VOICE_STATE_UPDATE** → `conn.HandleVoiceStateUpdate(event)`:
   - Sets `state.SessionID` and `state.ChannelID` (conn.go:173-195)
   - If ChannelID is nil: closes gateway, UDP, signals `closedChan`

2. **VOICE_SERVER_UPDATE** → `conn.HandleVoiceServerUpdate(event)`:
   - Sets `state.Token` and `state.Endpoint` (conn.go:197-213)
   - Launches goroutine: `c.gateway.Open(ctx, c.state)` — connects voice WebSocket

### 1.2 The Voice Gateway (Separate from Main Gateway)

From `voice/gateway.go`, `gateway.Open()` connects to `wss://{endpoint}?v=8`:

```
gateway.open(ctx, state)
  │
  ├── Dials wss://{endpoint}?v=8
  ├── Starts listen goroutine
  │     │
  │     ├── Receives OpcodeHello → starts heartbeat(), sends identify()
  │     │     └── Identify payload: {server_id, user_id, session_id, token, max_dave_protocol_version}
  │     │
  │     ├── Receives OpcodeReady → sets SSRC, signals readyChan
  │     │     └── conn.handleMessage() opens UDP: c.udp.Open(ctx, d.IP, d.Port, d.SSRC)
  │     │     └── conn.handleMessage() sends OpcodeSelectProtocol with our IP/port
  │     │
  │     ├── Receives OpcodeSessionDescription → sets secret key, signals openedChan
  │     │     └── Voice connection is now READY
  │     │
  │     └── Handles all DAVE opcodes (21-31) — entirely on voice gateway
  │
  └── Returns nil on success
```

**Key insight:** The voice Identify payload (gateway.go:416-438) uses:
- `GuildID` → `state.GuildID` (from NewConn)
- `UserID` → `state.UserID` (from NewConn)
- `SessionID` → `state.SessionID` (from HandleVoiceStateUpdate)
- `Token` → `state.Token` (from HandleVoiceServerUpdate)
- `MaxDaveProtocolVersion` → from DAVE session

**None of these require a main gateway connection on the machine running the voice connection.**

### 1.3 DAVE Encryption

DAVE is handled entirely within the voice gateway and UDP connection:
- `daveSession` is created in `NewConn()` from `DaveSessionCreate` config option (conn_config.go:13)
- All DAVE opcodes (PrepareTransition, ExecuteTransition, PrepareEpoch, MLS*) are handled in `gateway.listen()` (gateway.go:575-611)
- DAVE encryption/decryption happens in `udpConnImpl.Write()`/`ReadPacket()` via `daveSession.Encrypt()`/`Decrypt()` (udp_conn.go:259-284, 295-396)
- **DAVE works without the main gateway** — just pass `voice.WithConnDaveSessionCreateFunc(golibdave.NewSession)`

### 1.4 Voice Heartbeat

Self-contained in `gateway.heartbeat()` (gateway.go:364-413):
- Gets interval from OpcodeHello
- Runs in its own goroutine with `heartbeatCancel` context
- Reconnects on missed ACK
- **No main gateway involvement**

### 1.5 What NewConn Needs (and Doesn't Need)

From `voice/conn.go:63-85`:

```go
func NewConn(guildID snowflake.ID, userID snowflake.ID,
    voiceStateUpdateFunc StateUpdateFunc,
    removeConnFunc func(),
    opts ...ConnConfigOpt) Conn
```

- `guildID` — guild snowflake
- `userID` — bot's user snowflake
- `voiceStateUpdateFunc` — sends Opcode 4 via main gateway. **For proxy: no-op function**
- `removeConnFunc` — cleanup callback
- `opts` — config options (DAVE, logging, event handlers)

**Does NOT need:** `bot.Client`, `gateway.Gateway`, Discord token, HTTP client, cache

---

## 2. Data Flow in the Proxy Architecture

```
┌─────────────────────────────────────────────────────────────┐
│  Gateway Pod                                                 │
│                                                              │
│  ┌─ Main Discord Gateway (wss://gateway.discord.gg) ──────┐ │
│  │                                                          │ │
│  │  1. /start slash command arrives                         │ │
│  │  2. Gateway sends Opcode 4 (join voice channel)          │ │
│  │  3. Discord replies with two dispatch events:            │ │
│  │     • VOICE_STATE_UPDATE → session_id                    │ │
│  │     • VOICE_SERVER_UPDATE → token, endpoint              │ │
│  │  4. captureVoiceCredentials() collects all three         │ │
│  │  5. Gateway stays connected for slash commands           │ │
│  └──────────────────────────────────────────────────────────┘ │
│                                                              │
│  VoiceCredentials{session_id, token, endpoint, bot_user_id}  │
│         │                                                    │
└─────────┼────────────────────────────────────────────────────┘
          │ gRPC StartSession (includes voice credentials)
          ▼
┌─────────────────────────────────────────────────────────────┐
│  Worker Pod (NO main Discord gateway connection)             │
│                                                              │
│  ┌─ VoiceProxyPlatform ───────────────────────────────────┐ │
│  │                                                          │ │
│  │  1. voice.NewConn(guildID, botUserID, noopFunc, ...)     │ │
│  │  2. conn.HandleVoiceStateUpdate({SessionID, ChannelID})  │ │
│  │  3. conn.HandleVoiceServerUpdate({Token, Endpoint})      │ │
│  │     └─ Triggers goroutine: gateway.Open(state)           │ │
│  │        └─ Connects wss://{endpoint}?v=8                  │ │
│  │        └─ Identify → Ready → UDP → SelectProtocol        │ │
│  │        └─ SessionDescription → openedChan signaled        │ │
│  │  4. Voice ready! Audio flows directly:                    │ │
│  │     Worker ←UDP→ Discord Voice Server                     │ │
│  │                                                          │ │
│  │  Pipeline: VAD → STT → LLM → TTS → Mixer → UDP          │ │
│  └──────────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────────┘
```

### 2.1 Mid-Session Voice Server Migration

Discord can migrate voice servers mid-session, sending a new VOICE_SERVER_UPDATE:

```
Discord → Gateway: VOICE_SERVER_UPDATE {new_token, new_endpoint, guild_id}
Gateway → Worker:  UpdateVoiceServer gRPC {session_id, new_token, new_endpoint}
Worker:            conn.HandleVoiceServerUpdate({Token: new_token, Endpoint: new_endpoint})
                   └─ voice gateway reconnects to new endpoint automatically
```

### 2.2 Session Teardown

```
Gateway: Receives /stop slash command
Gateway → Worker:   StopSession gRPC {session_id}
Worker:             conn.Close(ctx) — closes voice gateway + UDP
                    (voiceStateUpdateFunc is no-op, so no Opcode 4 sent)
Gateway:            client.UpdateVoiceState(ctx, guildID, nil, false, false)
                    └─ Sends Opcode 4 with channelID=nil → bot leaves voice
```

---

## 3. Exact Implementation Plan

### 3.1 Extend `StartSessionRequest` (contract.go + proto)

**`internal/gateway/contract.go`** — add voice credential fields:

```go
type StartSessionRequest struct {
    SessionID   string
    TenantID    string
    CampaignID  string
    GuildID     string
    ChannelID   string
    LicenseTier string
    NPCConfigs  []NPCConfigMsg
    BotToken    string

    // Voice proxy credentials (populated by gateway in distributed mode).
    // When set, the worker connects directly to the Discord voice server
    // without opening its own bot gateway connection.
    VoiceSessionID string // from VOICE_STATE_UPDATE
    VoiceToken     string // from VOICE_SERVER_UPDATE
    VoiceEndpoint  string // from VOICE_SERVER_UPDATE
    BotUserID      string // bot's user snowflake (for voice.NewConn)
}
```

**`proto/glyphoxa/v1/session.proto`** — add fields to proto message:

```protobuf
message StartSessionRequest {
  // ... existing fields 1-8 ...
  string voice_session_id = 9;   // Discord voice session ID
  string voice_token = 10;       // Discord voice server token
  string voice_endpoint = 11;    // Discord voice server endpoint
  string bot_user_id = 12;       // Bot's Discord user snowflake
}
```

### 3.2 Add `UpdateVoiceServer` RPC (proto + contract)

**`proto/glyphoxa/v1/session.proto`**:

```protobuf
message UpdateVoiceServerRequest {
  string session_id = 1;
  string token = 2;
  string endpoint = 3;
}

message UpdateVoiceServerResponse {}

service SessionWorkerService {
  // ... existing RPCs ...
  rpc UpdateVoiceServer(UpdateVoiceServerRequest) returns (UpdateVoiceServerResponse);
}
```

**`internal/gateway/contract.go`** — extend `WorkerClient`:

```go
type WorkerClient interface {
    StartSession(ctx context.Context, req StartSessionRequest) error
    StopSession(ctx context.Context, sessionID string) error
    GetStatus(ctx context.Context) ([]SessionStatus, error)
    UpdateVoiceServer(ctx context.Context, sessionID, token, endpoint string) error
}
```

### 3.3 Voice Credential Capture on Gateway (`sessionctrl.go`)

New method on `GatewaySessionController`:

```go
// captureVoiceCredentials joins the voice channel via the gateway bot and
// captures the voice server credentials (session_id, token, endpoint) from
// the resulting VOICE_STATE_UPDATE and VOICE_SERVER_UPDATE dispatch events.
func (gc *GatewaySessionController) captureVoiceCredentials(
    ctx context.Context, guildID, channelID string,
) (sessionID, token, endpoint, botUserID string, err error) {
    gID, _ := snowflake.Parse(guildID)
    chID, _ := snowflake.Parse(channelID)

    type creds struct {
        sessionID string
        token     string
        endpoint  string
    }
    credsCh := make(chan creds, 1)

    var (
        mu        sync.Mutex
        c         creds
        gotState  bool
        gotServer bool
    )

    // Temporary event listeners — removed after capture.
    stateListener := bot.NewListenerFunc(func(e *events.GuildVoiceStateUpdate) {
        if e.GuildID != gID || e.UserID != gc.gwBot.Client().ID() {
            return
        }
        mu.Lock()
        defer mu.Unlock()
        c.sessionID = e.SessionID
        gotState = true
        if gotServer {
            select {
            case credsCh <- c:
            default:
            }
        }
    })
    serverListener := bot.NewListenerFunc(func(e *events.VoiceServerUpdate) {
        if e.GuildID != gID || e.Endpoint == nil {
            return
        }
        mu.Lock()
        defer mu.Unlock()
        c.token = e.Token
        c.endpoint = *e.Endpoint
        gotServer = true
        if gotState {
            select {
            case credsCh <- c:
            default:
            }
        }
    })

    gc.gwBot.Client().AddEventListeners(stateListener, serverListener)
    defer gc.gwBot.Client().RemoveEventListeners(stateListener, serverListener)

    // Send Opcode 4 to join voice channel.
    if err := gc.gwBot.Client().UpdateVoiceState(ctx, gID, &chID, false, false); err != nil {
        return "", "", "", "", fmt.Errorf("send voice state update: %w", err)
    }

    select {
    case vc := <-credsCh:
        return vc.sessionID, vc.token, vc.endpoint,
            gc.gwBot.Client().ID().String(), nil
    case <-ctx.Done():
        return "", "", "", "", fmt.Errorf("capture voice credentials: %w", ctx.Err())
    }
}
```

**Ordering guarantee:** VOICE_STATE_UPDATE is dispatched by Discord before VOICE_SERVER_UPDATE (the state update creates the voice session, the server update assigns a voice server). Both may arrive in either order from disgo's event dispatch perspective, but `captureVoiceCredentials()` handles both orderings with the `gotState`/`gotServer` flags.

### 3.4 Modify `GatewaySessionController.Start()`

Replace the suspend/resume dance with credential capture:

```go
func (gc *GatewaySessionController) Start(ctx context.Context, req SessionStartRequest) error {
    // ... existing validation + ValidateAndCreate ...

    if gc.dispatcher != nil {
        // Capture voice credentials BEFORE dispatching to worker.
        // The gateway bot joins voice and stays connected for slash commands.
        voiceCtx, voiceCancel := context.WithTimeout(ctx, 10*time.Second)
        defer voiceCancel()

        vsID, vToken, vEndpoint, botUserID, err := gc.captureVoiceCredentials(
            voiceCtx, req.GuildID, req.ChannelID)
        if err != nil {
            _ = gc.orch.Transition(ctx, sessionID, SessionEnded, err.Error())
            return fmt.Errorf("gateway: capture voice credentials: %w", err)
        }

        // Register ongoing listener for mid-session voice server changes.
        gc.registerVoiceServerForwarder(sessionID, req.GuildID)

        startReq := StartSessionRequest{
            SessionID:      sessionID,
            TenantID:       gc.tenantID,
            CampaignID:     gc.campaignID,
            GuildID:        req.GuildID,
            ChannelID:      req.ChannelID,
            LicenseTier:    gc.tier.String(),
            BotToken:       gc.botToken,
            NPCConfigs:     gc.npcConfigs,
            VoiceSessionID: vsID,
            VoiceToken:     vToken,
            VoiceEndpoint:  vEndpoint,
            BotUserID:      botUserID,
        }

        // NOTE: No SuspendGateway() call! Gateway stays connected.

        starter := func(callCtx context.Context, addr string) error {
            // ... same as before ...
        }
        result, dispErr := gc.dispatcher.Dispatch(ctx, sessionID, gc.tenantID, starter)
        if dispErr != nil {
            // Leave voice on dispatch failure.
            _ = gc.gwBot.Client().UpdateVoiceState(ctx, gID, nil, false, false)
            gc.unregisterVoiceServerForwarder(sessionID)
            // ... existing error handling (NO ResumeGateway needed) ...
        }
        // ...
    }
    // ...
}
```

### 3.5 Modify `GatewaySessionController.Stop()`

Remove `ResumeGateway`, add voice leave:

```go
func (gc *GatewaySessionController) Stop(ctx context.Context, sessionID string) error {
    // ... existing dispatcher.Stop() + orch.Transition() ...

    // Leave the voice channel (send Opcode 4 with channelID=nil).
    gc.mu.Lock()
    for guildID, sid := range gc.active {
        if sid == sessionID {
            delete(gc.active, guildID)
            gID, _ := snowflake.Parse(guildID)
            if gc.gwBot != nil {
                _ = gc.gwBot.Client().UpdateVoiceState(ctx, gID, nil, false, false)
            }
            break
        }
    }
    gc.mu.Unlock()

    // Clean up the voice server forwarder.
    gc.unregisterVoiceServerForwarder(sessionID)

    // NOTE: No ResumeGateway() — gateway never suspended!
    return nil
}
```

### 3.6 Voice Server Forwarder (Mid-Session Reconnection)

New methods on `GatewaySessionController`:

```go
// voiceForwarders tracks active VOICE_SERVER_UPDATE listeners per session.
// Added to GatewaySessionController struct:
//   voiceForwarders   map[string]func() // sessionID -> unregister func
//   voiceForwardersMu sync.Mutex
//   activeWorkers     map[string]WorkerClient // sessionID -> worker client

func (gc *GatewaySessionController) registerVoiceServerForwarder(sessionID, guildID string) {
    gID, _ := snowflake.Parse(guildID)

    listener := bot.NewListenerFunc(func(e *events.VoiceServerUpdate) {
        if e.GuildID != gID || e.Endpoint == nil {
            return
        }
        gc.mu.Lock()
        worker := gc.activeWorkers[sessionID]
        gc.mu.Unlock()

        if worker != nil {
            ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
            defer cancel()
            if err := worker.UpdateVoiceServer(ctx, sessionID, e.Token, *e.Endpoint); err != nil {
                slog.Error("gateway: failed to forward voice server update",
                    "session_id", sessionID, "err", err)
            }
        }
    })

    gc.gwBot.Client().AddEventListeners(listener)

    gc.voiceForwardersMu.Lock()
    gc.voiceForwarders[sessionID] = func() {
        gc.gwBot.Client().RemoveEventListeners(listener)
    }
    gc.voiceForwardersMu.Unlock()
}
```

### 3.7 New `VoiceProxyPlatform` (`pkg/audio/discord/voice_proxy.go`)

```go
package discord

import (
    "context"
    "fmt"
    "log/slog"
    "sync"

    "github.com/MrWong99/glyphoxa/pkg/audio"
    botgateway "github.com/disgoorg/disgo/gateway"
    "github.com/disgoorg/disgo/voice"
    "github.com/disgoorg/snowflake/v2"
)

var _ audio.Platform = (*VoiceProxyPlatform)(nil)

// VoiceProxyPlatform connects to a Discord voice server using pre-captured
// credentials (session_id, token, endpoint) from the gateway pod. The worker
// does NOT need its own Discord gateway connection.
type VoiceProxyPlatform struct {
    conn      voice.Conn
    guildID   snowflake.ID
    botUserID snowflake.ID
    readyCh   chan struct{}
    closeOnce sync.Once
}

// NewVoiceProxyPlatform creates a voice platform that connects using
// pre-captured credentials rather than its own Discord gateway.
func NewVoiceProxyPlatform(
    guildIDStr, botUserIDStr string,
    opts ...voice.ConnConfigOpt,
) (*VoiceProxyPlatform, error) {
    guildID, err := snowflake.Parse(guildIDStr)
    if err != nil {
        return nil, fmt.Errorf("discord: parse guild ID %q: %w", guildIDStr, err)
    }
    botUserID, err := snowflake.Parse(botUserIDStr)
    if err != nil {
        return nil, fmt.Errorf("discord: parse bot user ID %q: %w", botUserIDStr, err)
    }

    vp := &VoiceProxyPlatform{
        guildID:   guildID,
        botUserID: botUserID,
        readyCh:   make(chan struct{}, 1),
    }

    // No-op: the gateway pod handles Opcode 4 (join/leave voice channel).
    noopStateUpdate := func(ctx context.Context, guildID snowflake.ID,
        channelID *snowflake.ID, selfMute, selfDeaf bool) error {
        return nil
    }

    allOpts := append([]voice.ConnConfigOpt{
        voice.WithConnEventHandlerFunc(func(_ voice.Gateway, op voice.Opcode,
            _ int, data voice.GatewayMessageData) {
            if _, ok := data.(voice.GatewayMessageDataSessionDescription); ok {
                select {
                case vp.readyCh <- struct{}{}:
                default:
                }
            }
        }),
    }, opts...)

    vp.conn = voice.NewConn(guildID, botUserID, noopStateUpdate, func() {}, allOpts...)
    return vp, nil
}

// Connect feeds pre-captured voice credentials into the connection, triggering
// the voice WebSocket + UDP handshake. The ctx governs the setup phase only.
func (vp *VoiceProxyPlatform) Connect(
    ctx context.Context,
    channelIDStr, voiceSessionID, voiceToken, voiceEndpoint string,
) (audio.Connection, error) {
    channelID, err := snowflake.Parse(channelIDStr)
    if err != nil {
        return nil, fmt.Errorf("discord: parse channel ID: %w", err)
    }

    slog.Info("discord: voice proxy connecting",
        "guild_id", vp.guildID,
        "channel_id", channelID,
        "endpoint", voiceEndpoint,
    )

    // Feed the credentials that the gateway captured.
    // Order matters: HandleVoiceStateUpdate sets SessionID,
    // HandleVoiceServerUpdate triggers gateway.Open() which needs SessionID.
    vp.conn.HandleVoiceStateUpdate(botgateway.EventVoiceStateUpdate{
        VoiceState: discord.VoiceState{
            GuildID:   vp.guildID,
            ChannelID: &channelID,
            UserID:    vp.botUserID,
            SessionID: voiceSessionID,
        },
    })
    vp.conn.HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate{
        Token:    voiceToken,
        GuildID:  vp.guildID,
        Endpoint: &voiceEndpoint,
    })

    // Wait for the voice WebSocket handshake to complete.
    select {
    case <-vp.readyCh:
        slog.Info("discord: voice proxy connected", "guild_id", vp.guildID)
        return newConnection(vp.conn, vp.guildID), nil
    case <-ctx.Done():
        vp.conn.Close(ctx)
        return nil, fmt.Errorf("discord: voice proxy connect: %w", ctx.Err())
    }
}

// UpdateVoiceServer handles mid-session voice server changes. Discord sends
// a new VOICE_SERVER_UPDATE when migrating voice servers. The gateway
// forwards this to the worker, which calls this method.
func (vp *VoiceProxyPlatform) UpdateVoiceServer(token, endpoint string) {
    vp.conn.HandleVoiceServerUpdate(botgateway.EventVoiceServerUpdate{
        Token:    token,
        GuildID:  vp.guildID,
        Endpoint: &endpoint,
    })
}

// Close tears down the voice connection. It is safe to call more than once.
func (vp *VoiceProxyPlatform) Close() error {
    vp.closeOnce.Do(func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        vp.conn.Close(ctx)
        slog.Info("discord: voice proxy closed", "guild_id", vp.guildID)
    })
    return nil
}
```

**Note on imports:** The `Connect` method references `discord.VoiceState` from `github.com/disgoorg/disgo/discord`. The actual import will be:
```go
import discodiscord "github.com/disgoorg/disgo/discord"
```
And use `discodiscord.VoiceState{...}`.

### 3.8 Update `workerFactory.CreateRuntime()` (`cmd/glyphoxa/worker_factory.go`)

Replace step 3 (Discord voice connection):

```go
// ── 3. Discord voice connection ──────────────────────────────────────────
if req.BotToken == "" {
    // ... existing error handling ...
}

var platform interface{ Close() error }
var conn audio.Connection

if req.VoiceSessionID != "" && req.VoiceToken != "" && req.VoiceEndpoint != "" {
    // Distributed mode with voice proxy: use pre-captured credentials.
    proxyPlatform, err := discord.NewVoiceProxyPlatform(
        req.GuildID, req.BotUserID,
        voice.WithConnDaveSessionCreateFunc(golibdave.NewSession),
    )
    if err != nil {
        if storeCloser != nil { _ = storeCloser() }
        return nil, fmt.Errorf("worker: create voice proxy platform: %w", err)
    }

    conn, err = proxyPlatform.Connect(sessionCtx,
        req.ChannelID, req.VoiceSessionID, req.VoiceToken, req.VoiceEndpoint)
    if err != nil {
        _ = proxyPlatform.Close()
        if storeCloser != nil { _ = storeCloser() }
        return nil, fmt.Errorf("worker: voice proxy connect to %s: %w", req.ChannelID, err)
    }
    platform = proxyPlatform

    // Store the proxy platform for mid-session voice server updates.
    // The WorkerHandler will call proxyPlatform.UpdateVoiceServer() when
    // it receives an UpdateVoiceServer gRPC call.
} else {
    // Full mode: open own gateway (existing code).
    voicePlatform, err := discord.NewVoiceOnlyPlatform(sessionCtx, req.BotToken, req.GuildID,
        discord.WithVoiceManagerOpts(voice.WithDaveSessionCreateFunc(golibdave.NewSession)),
    )
    if err != nil {
        if storeCloser != nil { _ = storeCloser() }
        return nil, fmt.Errorf("worker: create voice platform: %w", err)
    }

    conn, err = voicePlatform.Connect(sessionCtx, req.ChannelID)
    if err != nil {
        _ = voicePlatform.Close()
        if storeCloser != nil { _ = storeCloser() }
        return nil, fmt.Errorf("worker: connect to voice channel %s: %w", req.ChannelID, err)
    }
    platform = voicePlatform
}
```

### 3.9 Update gRPC Transport (`grpctransport/client.go` and `server.go`)

**Client** — add `UpdateVoiceServer` + pass voice credentials in `StartSession`:

```go
func (c *Client) StartSession(ctx context.Context, req gateway.StartSessionRequest) error {
    // ... existing NPC config mapping ...
    return c.breaker.Execute(func() error {
        _, err := c.client.StartSession(ctx, &pb.StartSessionRequest{
            // ... existing fields ...
            VoiceSessionId: req.VoiceSessionID,
            VoiceToken:     req.VoiceToken,
            VoiceEndpoint:  req.VoiceEndpoint,
            BotUserId:      req.BotUserID,
        })
        return err
    })
}

func (c *Client) UpdateVoiceServer(ctx context.Context, sessionID, token, endpoint string) error {
    return c.breaker.Execute(func() error {
        _, err := c.client.UpdateVoiceServer(ctx, &pb.UpdateVoiceServerRequest{
            SessionId: sessionID,
            Token:     token,
            Endpoint:  endpoint,
        })
        return err
    })
}
```

**Server** — add handler for `UpdateVoiceServer`:

```go
func (s *Server) UpdateVoiceServer(ctx context.Context, req *pb.UpdateVoiceServerRequest) (*pb.UpdateVoiceServerResponse, error) {
    if err := s.handler.UpdateVoiceServer(ctx, req.GetSessionId(), req.GetToken(), req.GetEndpoint()); err != nil {
        return nil, status.Errorf(codes.Internal, "update voice server: %v", err)
    }
    return &pb.UpdateVoiceServerResponse{}, nil
}
```

### 3.10 Update `WorkerHandler` for Voice Server Updates

The `session.WorkerHandler` needs a method to route `UpdateVoiceServer` to the right session's proxy platform. This requires the `VoiceProxyPlatform` to be accessible from the runtime.

Add to `session.Runtime`:
```go
type Runtime struct {
    // ... existing fields ...
    voiceProxy *discord.VoiceProxyPlatform // nil in full mode
}

func (r *Runtime) UpdateVoiceServer(token, endpoint string) {
    if r.voiceProxy != nil {
        r.voiceProxy.UpdateVoiceServer(token, endpoint)
    }
}
```

Add to `session.WorkerHandler`:
```go
func (h *WorkerHandler) UpdateVoiceServer(ctx context.Context, sessionID, token, endpoint string) error {
    h.mu.RLock()
    rt, ok := h.sessions[sessionID]
    h.mu.RUnlock()
    if !ok {
        return fmt.Errorf("session %s not found", sessionID)
    }
    rt.UpdateVoiceServer(token, endpoint)
    return nil
}
```

---

## 4. Potential Blockers & Mitigations

### 4.1 godave: Key Ratchet Race Condition (Open Issue)

**Issue:** "Fix key ratchet race condition during DAVE epoch transitions" on disgoorg/godave.

**Impact:** Could cause audio drops during DAVE epoch transitions, regardless of architecture (proxy or direct).

**Mitigation:** This is a pre-existing issue not specific to the proxy approach. Monitor the godave repo for fixes. If it becomes critical, we can temporarily disable DAVE with `voice.WithConnDaveSessionCreateFunc(godave.NewNoopSession)`.

### 4.2 godave: "failed to read packet" (Open Issue)

**Impact:** Another pre-existing DAVE reliability issue.

**Mitigation:** Same as above — not proxy-specific.

### 4.3 disgo: "Data race in gateway implementation" (Open Issue)

**Impact:** Affects the main gateway, not the voice gateway. Our gateway pod would be affected regardless of voice architecture.

**Mitigation:** Monitor the issue. The voice proxy actually reduces exposure since the worker doesn't run the main gateway.

### 4.4 Gateway bot shows as "in voice" while worker handles audio

**Impact:** Cosmetic only. The gateway bot appears to be in the voice channel because it sent the Opcode 4 join. The worker connects to the voice WebSocket/UDP directly.

**Mitigation:** Not a problem — this is actually correct behavior from Discord's perspective. The bot IS in the voice channel; the proxy architecture just moves the audio processing to a different machine.

### 4.5 External voice state changes

**Scenario:** Someone disconnects the bot from voice externally (admin kicks, etc.).

**Flow:** Discord sends VOICE_STATE_UPDATE with channelID=nil to the main gateway. The gateway receives this and should notify the worker to stop.

**Implementation:** The gateway registers a listener for VOICE_STATE_UPDATE where the bot user is removed from voice. When detected, it stops the session:

```go
// In captureVoiceCredentials or registerVoiceServerForwarder:
disconnectListener := bot.NewListenerFunc(func(e *events.GuildVoiceStateUpdate) {
    if e.GuildID != gID || e.UserID != gc.gwBot.Client().ID() {
        return
    }
    if e.ChannelID == nil {
        // Bot was disconnected from voice externally.
        slog.Info("gateway: bot disconnected from voice externally",
            "session_id", sessionID, "guild_id", guildID)
        go gc.Stop(context.Background(), sessionID)
    }
})
```

### 4.6 connImpl.Close() no-op for Opcode 4

**Issue:** `connImpl.Close()` calls `voiceStateUpdateFunc(ctx, guildID, nil, false, false)` to send Opcode 4 (leave voice). With our no-op function, this does nothing.

**Impact:** None — the gateway handles leaving voice in `Stop()`. The no-op is intentional.

**Caveat:** If the worker crashes without the gateway calling `Stop()`, the gateway bot will remain in the voice channel. The gateway should detect worker crash (via heartbeat timeout) and leave voice.

---

## 5. DAVE Protocol Details

### 5.1 DAVE Handshake Flow (All on Voice Gateway)

```
Voice Gateway Connect
  → Identify (with max_dave_protocol_version=1)
  ← Ready (SSRC, IP, Port, Modes)
  ← DavePrepareTransition (transition_id, protocol_version)
  → DaveMLSKeyPackage
  ← DaveMLSExternalSenderPackage
  ← DaveMLSProposals
  ← DaveMLSPrepareCommitTransition (transition_id, commit_message)
  → DaveMLSCommitWelcome
  ← DaveMLSWelcome (transition_id, welcome_message)
  → DaveTransitionReady (transition_id)
  ← DaveExecuteTransition (transition_id)
  ← SessionDescription (with dave_protocol_version)
```

All of this happens within `voice/gateway.go:listen()` and the `godave.Session` interface. The main Discord gateway is not involved at any point.

### 5.2 DAVE Mandatory Deadline

Per Discord API docs: "We will only support E2EE calls starting on March 1st, 2026 for all audio and video conversations." This is already past, so DAVE is mandatory. The proxy approach supports DAVE fully — just pass `voice.WithConnDaveSessionCreateFunc(golibdave.NewSession)` to `NewVoiceProxyPlatform`.

---

## 6. What to Delete After Migration

1. **`GatewayBot.SuspendGateway()`** — no longer called
2. **`GatewayBot.ResumeGateway()`** — no longer called
3. **Suspend/resume logic in `sessionctrl.go`** — replaced by credential capture
4. **`VoiceOnlyPlatform`** — keep for `--mode=full` backward compatibility, but it's no longer used in distributed mode

---

## 7. Testing Strategy

### 7.1 Unit Tests

1. **`VoiceProxyPlatform`**:
   - `TestVoiceProxyPlatform_Connect_Success` — mock voice.Conn, verify HandleVoiceStateUpdate and HandleVoiceServerUpdate called with correct args, simulate SessionDescription event
   - `TestVoiceProxyPlatform_Connect_ContextTimeout` — verify cleanup on timeout
   - `TestVoiceProxyPlatform_UpdateVoiceServer` — verify HandleVoiceServerUpdate forwarded
   - `TestVoiceProxyPlatform_Close_Idempotent` — verify double-close is safe

2. **`captureVoiceCredentials`**:
   - `TestCaptureVoiceCredentials_BothEvents` — simulate both events arriving
   - `TestCaptureVoiceCredentials_Timeout` — context cancellation
   - `TestCaptureVoiceCredentials_OrderIndependent` — server update before state update

3. **`GatewaySessionController.Start()`**:
   - `TestStart_DistributedMode_CapturesCredentials` — verify no SuspendGateway called
   - `TestStart_FailedCapture_TransitionsToEnded` — verify cleanup

### 7.2 Integration Test

Manual test with a real Discord bot:
1. Start gateway with `--mode=gateway`
2. Start worker with `--mode=worker`
3. Use `/start` command → verify bot joins voice, NPCs respond
4. Verify gateway still handles slash commands while voice is active
5. Use `/stop` → verify bot leaves voice
6. Test: start session, then have admin move bot out of voice → verify cleanup

### 7.3 Load Test

Start multiple concurrent sessions across different guilds to verify:
- Each session gets its own voice credentials
- Voice server forwarders don't leak
- Cleanup is complete on stop

---

## 8. Implementation Order

| Step | File(s) | Estimated Effort |
|------|---------|-----------------|
| 1 | `proto/glyphoxa/v1/session.proto` + `make proto` | Small |
| 2 | `internal/gateway/contract.go` — extend types | Small |
| 3 | `pkg/audio/discord/voice_proxy.go` — new file | Medium |
| 4 | `pkg/audio/discord/voice_proxy_test.go` — tests | Medium |
| 5 | `internal/gateway/sessionctrl.go` — captureVoiceCredentials + modify Start/Stop | Medium |
| 6 | `internal/gateway/sessionctrl_test.go` — tests | Medium |
| 7 | `internal/gateway/grpctransport/client.go` — pass voice creds, add UpdateVoiceServer | Small |
| 8 | `internal/gateway/grpctransport/server.go` — add UpdateVoiceServer handler | Small |
| 9 | `cmd/glyphoxa/worker_factory.go` — use VoiceProxyPlatform when creds present | Small |
| 10 | `internal/session/runtime.go` — add voiceProxy field + UpdateVoiceServer method | Small |
| 11 | `internal/session/worker_handler.go` — route UpdateVoiceServer | Small |
| 12 | Remove SuspendGateway/ResumeGateway calls | Small |
| 13 | Manual integration test | - |

---

## 9. Key Disgo API Surface Used

| API | File | Purpose |
|-----|------|---------|
| `voice.NewConn(guildID, userID, stateUpdateFunc, removeFunc, opts...)` | `voice/conn.go:63` | Create voice connection without bot.Client |
| `conn.HandleVoiceStateUpdate(event)` | `voice/conn.go:173` | Feed session_id from gateway |
| `conn.HandleVoiceServerUpdate(event)` | `voice/conn.go:197` | Feed token+endpoint, triggers voice gateway connect |
| `voice.WithConnDaveSessionCreateFunc(fn)` | `voice/conn_config.go:106` | Enable DAVE E2EE |
| `voice.WithConnEventHandlerFunc(fn)` | `voice/conn_config.go:99` | Listen for SessionDescription |
| `gateway.EventVoiceStateUpdate` | `gateway/gateway_events.go:796` | Struct for state update event |
| `gateway.EventVoiceServerUpdate` | `gateway/gateway_events.go:804` | Struct for server update event |
| `bot.Client.UpdateVoiceState(ctx, guildID, channelID, mute, deaf)` | `bot/client.go:103` | Send Opcode 4 via main gateway |
| `bot.Client.ID()` | `bot/client.go:53` | Get bot's user snowflake |
| `bot.Client.AddEventListeners(listeners...)` | bot package | Register event listeners |
| `bot.Client.RemoveEventListeners(listeners...)` | bot package | Unregister event listeners |

All of these are **exported public APIs** — no internal/unexported access needed.
