---
nav_order: 12
---

# Distributed Mode — Gateway Audio Bridge Architecture

Glyphoxa's distributed mode splits the system into a **gateway** and one or more **workers** that communicate over gRPC. This document covers the architecture, audio flow, session lifecycle, configuration, deployment, and known gotchas.

For single-process deployments, see [Deployment](deployment.md) — `--mode=full` requires no distributed configuration.

---

## Architecture Overview

In distributed mode, three gRPC services connect the gateway and worker:

```
┌──────────────────────────────────────────────┐
│                  Gateway                      │
│                                               │
│   Discord Bot (VoiceManager)                  │
│      │                                        │
│      ├─ joins voice channel                   │
│      ├─ receives opus from players            │
│      └─ sends opus to players                 │
│      │                                        │
│   AudioBridge Server                          │
│      │  per-session SessionBridge             │
│      │  toWorker chan ←── Discord audio        │
│      │  fromWorker chan ──→ Discord audio      │
│      │                                        │
│   Session Orchestrator (PostgreSQL)           │
│   K8s Job Dispatcher                          │
│   Admin API (:8081)                           │
│   Slash Command Handlers                      │
└────────┬────────────┬────────────────────────┘
         │            │
    gRPC │     gRPC   │ AudioBridgeService
   control│    (bidi   │ (opus stream)
   plane  │    stream) │
         │            │
┌────────┴────────────┴────────────────────────┐
│                  Worker                       │
│                                               │
│   grpcbridge.Connection                       │
│      │  implements audio.Connection           │
│      │  opus decode → PCM (per-user)          │
│      │  PCM → opus encode (NPC output)        │
│      │                                        │
│   Voice Pipeline                              │
│      VAD (Silero) → STT → LLM → TTS → Mixer  │
│                                               │
│   Session Runtime                             │
│   Heartbeat Reporter                          │
└───────────────────────────────────────────────┘
```

### Why Audio Bridge?

An earlier design ("Voice State Proxy") had the gateway capture Discord voice credentials and pass them to the worker, which would then connect to Discord voice directly. This approach was abandoned because:

1. **Complexity** — capturing voice credentials from Discord gateway events required intercepting `VOICE_STATE_UPDATE` and `VOICE_SERVER_UPDATE` at the right time, with fragile race conditions
2. **Suspend/resume** — the gateway had to suspend its own voice connection before the worker could take over (causing 60-second hangs)
3. **Slash commands** — with the worker owning voice, the gateway couldn't easily handle Discord slash commands for the same session

The Audio Bridge approach is simpler: the gateway keeps the Discord voice connection permanently and transparently proxies opus frames. The worker is completely Discord-unaware — it receives PCM audio and sends PCM audio through the standard `audio.Connection` interface.

---

## How Voice Works

### Audio Flow: Player → NPC

```
Player speaks in Discord
    │
    ▼
Discord sends opus packets to gateway's VoiceManager
    │
    ▼
Gateway: opus packet → SessionBridge.toWorker channel
    │
    ▼ (gRPC AudioBridgeService.StreamAudio)
    │
Worker: grpcbridge.Connection.recvLoop()
    ├─ opus decode → PCM (per-user gopus.Decoder)
    ├─ demux by user_id into per-participant input channels
    ▼
Voice Pipeline: VAD → STT → LLM → TTS → Mixer
    │
    ▼
Worker: grpcbridge.Connection.sendLoop()
    ├─ PCM → opus encode (48 kHz stereo, 20ms frames)
    ▼ (gRPC AudioBridgeService.StreamAudio, return direction)
    │
Gateway: SessionBridge.fromWorker channel → Discord OpusSend
    │
    ▼
All players hear the NPC
```

### Handshake

When a worker starts a session, it connects to the gateway's `AudioBridgeService.StreamAudio` RPC and sends an initial frame containing only the `session_id`. The gateway has 10 seconds to receive this handshake before closing the stream. The `session_id` routes the stream to the correct `SessionBridge` that was pre-created when the gateway dispatched the session.

### Barge-In and Flush

When a player starts speaking while an NPC is outputting audio:

1. The worker's VAD detects player speech
2. The mixer interrupts the current NPC segment and clears the queue
3. `grpcbridge.Connection.Flush()` drains the local output buffer
4. A `flush` control frame is sent to the gateway over the gRPC stream
5. The gateway's `SessionBridge.Flush()` drains all buffered frames from `fromWorker`
6. Stale NPC audio stops playing immediately on both sides

### Buffer Sizes

| Buffer | Size | Purpose |
|--------|------|---------|
| `toWorker` | 128 frames (~2.5s) | Discord→worker, realtime pace (50 fps) |
| `fromWorker` | 1500 frames (~30s) | Worker→Discord, handles TTS bursts (faster-than-realtime generation) |
| Per-participant input | 64 frames | Worker-side, per-user PCM |
| Output | 64 frames | Worker-side, NPC audio from mixer |

---

## How Sessions Work

### Session Lifecycle

```
1. Player runs /session start in Discord
       │
2. Gateway slash command handler → SessionOrchestrator.ValidateAndCreate()
   ├─ checks concurrent session limit (license tier)
   ├─ checks monthly quota (QuotaGuard)
   └─ creates session record in PostgreSQL (state: pending)
       │
3. Gateway → K8s Job Dispatcher
   ├─ creates K8s Job from template
   ├─ polls until pod is Ready
   └─ returns worker pod IP + gRPC port
       │
4. Gateway → AudioBridge.NewSessionBridge(sessionID)
   (pre-creates the channel pair for the gRPC stream)
       │
5. Gateway → VoiceManager.JoinVoiceChannel()
   (connects bot to the player's voice channel)
       │
6. Gateway → Worker (gRPC): StartSession(req)
   ├─ worker builds voice pipeline (RuntimeFactory)
   ├─ worker connects to AudioBridgeService.StreamAudio
   ├─ worker sends handshake frame with session_id
   └─ worker starts VAD→STT→LLM→TTS→Mixer loop
       │
7. Worker → Gateway (gRPC): ReportState(session_id, ACTIVE)
       │
8. Audio flows bidirectionally via AudioBridgeService
       │
9. Player runs /session stop (or session times out)
       │
10. Gateway → Worker (gRPC): StopSession(session_id)
    ├─ worker stops pipeline, disconnects audio
    └─ worker reports state ENDED
       │
11. Gateway cleans up: remove bridge, disconnect voice, update DB
```

### Worker Dispatch (K8s Jobs)

The `dispatch.Dispatcher` (`internal/gateway/dispatch/`) creates Kubernetes Jobs for each voice session:

- **Template**: A pre-configured `batchv1.Job` with environment variables for the worker
- **Pod readiness**: Polls until the worker pod is Running and Ready (default timeout: 120s)
- **Address resolution**: Uses the pod IP + gRPC port (default: 50051) as the worker address
- **Cleanup**: Gateway deletes the K8s Job when the session ends

Key environment variables injected into worker pods:

| Variable | Value | Purpose |
|----------|-------|---------|
| `GLYPHOXA_GRPC_ADDR` | `:50051` | Worker gRPC listen address |
| `GLYPHOXA_GATEWAY_ADDR` | `<gateway-service>:50051` | Gateway gRPC address for callbacks |
| `GLYPHOXA_AUDIO_BRIDGE_ADDR` | `<gateway-service>:50051` | Gateway AudioBridge address |
| `GLYPHOXA_DATABASE_DSN` | `postgres://...` | PostgreSQL connection string |
| `GLYPHOXA_MCP_GATEWAY_URL` | `http://...` | MCP gateway URL (optional) |

### Heartbeat and Failure Detection

Workers send periodic heartbeats to the gateway via `SessionGatewayService.Heartbeat`. If the heartbeat stops:

- **Audio stream disconnect**: The `AudioBridgeService` detects stream closure immediately and fires `OnStreamDetach`, triggering cleanup without waiting for heartbeat timeout
- **Heartbeat timeout**: `CleanupZombies(timeout)` transitions sessions with no heartbeat for >90 seconds to `ended` state
- **Combined**: The audio stream detach provides fast detection; the heartbeat timeout is a safety net

---

## Configuration

### Gateway Configuration

The gateway is configured primarily through environment variables and the admin API:

```yaml
# Gateway mode
--mode=gateway

# Required environment variables:
GLYPHOXA_ADMIN_KEY=your-secret-key      # Admin API authentication
GLYPHOXA_GRPC_ADDR=:50051               # gRPC listen address
GLYPHOXA_DATABASE_DSN=postgres://...     # PostgreSQL with pgvector
```

### Worker Configuration

Workers receive their configuration via the `StartSessionRequest` gRPC message, which includes tenant ID, campaign ID, NPC configs, and bot token. Provider configuration (which STT/LLM/TTS to use) comes from a ConfigMap mounted into the worker pod.

Example worker config (mounted as ConfigMap):

```yaml
providers:
  vad:
    name: silero
    options:
      frame_size_ms: 32   # Must be 32 for Silero with 16kHz input

  stt:
    name: elevenlabs
    api_key: ${ELEVENLABS_API_KEY}
    options:
      language: de          # Must be set explicitly for non-English

  llm:
    name: gemini
    api_key: ${GEMINI_API_KEY}
    model: gemini-2.0-flash

  tts:
    name: elevenlabs
    api_key: ${ELEVENLABS_API_KEY}

memory:
  postgres_dsn: ${GLYPHOXA_DATABASE_DSN}
  embedding_dimensions: 768

  embeddings:
    name: gemini
    api_key: ${GEMINI_API_KEY}
    model: gemini-embedding-001
```

### NPC Configuration Format

NPCs are defined in the `StartSessionRequest` and stored in PostgreSQL (`npc_definitions` table). Key fields:

```yaml
npcs:
  - name: Heinrich
    personality: "A gruff dwarven blacksmith..."
    engine: cascaded          # Must be "cascaded" (not "cascade")
    voice:
      voice_id: "abc123..."  # voice is a struct, not a plain string
    knowledge_scope:
      - blacksmithing
      - local_gossip
    budget_tier: standard
    gm_helper: false
    address_only: false
```

**Common mistakes:**
- `engine` must be `cascaded` (not `cascade`)
- `voice` is a struct `{voice_id: "..."}`, not a plain string
- `voice_id` must match a voice in your TTS provider (e.g., ElevenLabs voice ID)

---

## Known Gotchas

### gRPC Context Kills Long-Lived Resources

**Problem**: Using the gRPC RPC context for resources that outlive the RPC causes silent cancellation.

**Solution**: Always use `context.Background()` for long-lived pipeline components (VAD sessions, STT connections, TTS streams). Only use the RPC context for the RPC call itself.

```go
// Wrong: pipeline dies when StartSession RPC returns
pipeline := newPipeline(rpcCtx)

// Right: pipeline lives until explicitly stopped
pipeline := newPipeline(context.Background())
```

### DAVE E2EE Voice Encryption

Discord's DAVE (Discord Audio Video Encryption) is mandatory since 2026-03-01. The gateway must use `safedave.NewSession` (a thread-safe wrapper around `golibdave`) when joining voice:

```go
voice.WithDaveSessionCreateFunc(safedave.NewSession)
```

Without this, the gateway will connect to voice but receive/send encrypted frames that the pipeline can't process. The symptom is silence in both directions.

### VAD Frame Size Must Be 32ms

Silero VAD with 16 kHz input requires `frame_size_ms: 32`. Using 30ms (the default documented elsewhere) causes a dimension mismatch in the ONNX model and panics at runtime.

### STT Language Must Be Set Explicitly

For non-English sessions, the STT provider's `language` option must be set explicitly:

```yaml
stt:
  options:
    language: de  # German
```

Without this, STT defaults to English and produces garbled transcriptions of non-English speech.

### German NPC Routing: Short Word False Matches

The NPC address detection router matches keywords in player speech. German articles like "der", "die", "das" can falsely match NPC names containing those substrings. Fixed by requiring a minimum 4-rune keyword length. If you define German NPCs, avoid very short names.

### zhi Re-Render Drops Manual ConfigMap Edits

If you deploy with `zhi` and manually edit the ConfigMap (e.g., to add NPCs), running `zhi apply` will re-render the template and overwrite your changes. Solution: add NPCs to the zhi workspace template, not the live ConfigMap.

### Worker Pod DNS Resolution

Worker pods dispatched as K8s Jobs may not have DNS resolution for the gateway service if the DNS entry hasn't propagated. The dispatcher uses pod IP + port directly to avoid this issue.

---

## Deployment on K3s

This section covers the actual deployment topology used on the Glyphoxa home server (K3s at `192.168.178.44`).

### Cluster Layout

```
K3s Node (192.168.178.44)
├── glyphoxa namespace
│   ├── Deployment: glyphoxa-gateway (1 replica)
│   │   ├── Discord bot (VoiceManager)
│   │   ├── Admin API (:8081)
│   │   ├── gRPC server (:50051)
│   │   └── AudioBridge server (same gRPC port)
│   ├── Deployment: glyphoxa-postgres (1 replica)
│   │   └── PostgreSQL + pgvector
│   ├── Job: glyphoxa-session-<id> (per-session, created dynamically)
│   │   └── Worker pod (voice pipeline)
│   └── Service: glyphoxa-gateway (ClusterIP)
│       ├── port 8081 → admin API
│       └── port 50051 → gRPC (control + audio)
```

### zhi Workspace

The deployment is managed by `zhi` at `~/zhi-deploy/glyphoxa-k8s`:

```
glyphoxa-k8s/
├── workspace.yaml          # zhi workspace definition
├── templates/
│   ├── gateway-deployment.yaml
│   ├── gateway-service.yaml
│   ├── postgres-deployment.yaml
│   ├── postgres-service.yaml
│   ├── worker-job-template.yaml
│   ├── configmap.yaml      # Provider config (STT, LLM, TTS, etc.)
│   └── secrets.yaml        # API keys (sealed)
```

### Worker Job Template

The gateway uses a pre-configured Job template to dispatch workers. Key aspects:

- **Image**: Same image as the gateway (`ghcr.io/mrwong99/glyphoxa`)
- **Command**: `--mode=worker`
- **Resources**: CPU/memory limits appropriate for the voice pipeline
- **activeDeadlineSeconds**: 14400 (4 hours max session)
- **Environment**: Gateway address, audio bridge address, database DSN, API keys
- **ConfigMap mount**: Provider configuration (STT language, models, etc.)

---

## Comparison: Full Mode vs Distributed Mode

| Aspect | Full Mode (`--mode=full`) | Distributed Mode (`--mode=gateway` + `--mode=worker`) |
|--------|--------------------------|-------------------------------------------------------|
| Processes | Single binary | Gateway + worker(s) as separate pods |
| Discord voice | Worker connects directly via `VoiceOnlyPlatform` | Gateway owns connection, proxies via AudioBridge |
| audio.Connection | `discord.Connection` | `grpcbridge.Connection` |
| Session control | `local.Client` (direct function calls) | `grpctransport.Client` (gRPC with circuit breaker) |
| State callbacks | `local.Callback` (direct function calls) | gRPC `SessionGatewayService` |
| Worker lifecycle | In-process | K8s Jobs (created/deleted per session) |
| Multi-tenant | No (single config) | Yes (admin API, per-tenant bots, quota) |
| Scaling | Vertical only | Horizontal (one worker per session) |

---

**See also:** [Architecture](architecture.md) · [Multi-Tenant](multi-tenant.md) · [Audio Pipeline](audio-pipeline.md) · [Deployment](deployment.md) · [Configuration](configuration.md)
