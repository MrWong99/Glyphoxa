---
nav_order: 2
---

# 🏗️ Architecture Overview

## 🎯 System Overview

Glyphoxa is a real-time voice AI framework that brings AI-driven talking NPCs into live TTRPG voice chat sessions. It captures player speech from a voice channel (Discord, WebRTC), routes it through a streaming AI pipeline (STT, LLM, TTS — or a single speech-to-speech model), and plays back NPC dialogue with distinct voices, personalities, and persistent memory — all within a 1.2-second mouth-to-ear latency target.

Written in Go for native concurrency, every pipeline stage runs as a goroutine connected by channels, enabling true end-to-end streaming where TTS starts synthesising before the LLM finishes generating.

---

## 🗺️ Architecture Diagram

```
┌────────────────────────────────────────────────────────────────────────────┐
│                          Audio Transport Layer                             │
│                    (Discord / WebRTC / Custom Platform)                    │
├───────────────────────┬────────────────────────────────────────────────────┤
│   Audio In            │                Audio Out                           │
│   ┌───────────────┐   │   ┌───────────────────────────────────────────┐    │
│   │ Per-Speaker   │   │   │ Audio Mixer                               │    │
│   │ Streams       │   │   │  - Priority queue (addressed NPC first)   │    │
│   │ (chan AudioFr)│   │   │  - Barge-in detection (truncate on VAD)   │    │
│   └───────┬───────┘   │   │  - Natural pacing (200-500ms gaps)        │    │
│           │           │   └──────────────────────┬────────────────────┘    │
├───────────┼───────────┴──────────────────────────┼─────────────────────────┤
│           │     Agent Orchestrator + Router      │                         │
│           │   ┌─────────────────────────────┐    │                         │
│           │   │  Address Detection          │    │                         │
│           │   │  Turn-Taking / Barge-in     │    │                         │
│           │   │  DM Commands & Puppet Mode  │    │                         │
│           │   └──────────┬──────────────────┘                              │
│           │              │                       │                         │
│    ┌──────┴──────┐ ┌─────┴──────┐ ┌──────────┐   │                         │
│    │   NPC #1    │ │   NPC #2   │ │  NPC #3  │ ...                         │
│    │  (Agent)    │ │  (Agent)   │ │ (Agent)  │   │                         │
│    └──────┬──────┘ └─────┬──────┘ └────┬─────┘   │                         │
├───────────┴──────────────┴─────────────┴─────────┴─────────────────────────┤
│                          Voice Engines                                     │
│                                                                            │
│  ┌─────────────────────────┐ ┌──────────────┐ ┌───────────────────────┐    │
│  │  Cascaded Engine        │ │  S2S Engine  │ │  Sentence Cascade     │    │
│  │  STT → LLM → TTS        │ │  Gemini Live │ │  ⚠️ Experimental      │    │
│  │  (full pipeline)        │ │  OpenAI RT   │ │  Fast+Strong models   │    │
│  └─────────────────────────┘ └──────────────┘ └───────────────────────┘    │
│                                                                            │
├──────────────────────────────────┬─────────────────────────────────────────┤
│   Memory Subsystem               │    MCP Tool Execution                   │
│                                  │                                         │
│  ┌───────┐ ┌────────┐ ┌──────┐   │  ┌───────┐ ┌───────┐ ┌────────┐         │
│  │  L1   │ │   L2   │ │  L3  │   │  │ Dice  │ │ Rules │ │ Memory │         │
│  │Session│ │Semantic│ │Graph │   │  │Roller │ │Lookup │ │ Query  │ ...     │
│  │  Log  │ │ Index  │ │(KG)  │   │  └───────┘ └───────┘ └────────┘         │
│  └───────┘ └────────┘ └──────┘   │  Budget Tiers: instant/fast/standard    │
│  ─── all on PostgreSQL ───────   │                                         │
├──────────────────────────────────┴─────────────────────────────────────────┤
│   Observability (OpenTelemetry)  │  Resilience (Fallback + Circuit Break)  │
│   Metrics · Traces · Middleware  │  LLM / STT / TTS provider failover      │
└────────────────────────────────────────────────────────────────────────────┘
```

### Distributed Topology (`--mode=gateway` + `--mode=worker`)

In multi-tenant deployments, the system splits into separate processes:

```
┌─────────────────────────────────┐     ┌──────────────────────────────────┐
│           Gateway               │     │            Worker                │
│                                 │     │                                  │
│  Admin API (tenant CRUD)        │     │  VAD → STT → LLM → TTS → Mixer  │
│  Bot Manager (per-tenant bots)  │ gRPC│  Session Runtime                 │
│  Session Orchestrator     ──────┼─────┤  Discord Voice (direct)          │
│  Usage / Quota Tracking         │     │  MCP Tool Calls                  │
│  Health + Metrics               │     │  Health + Metrics                │
└─────────────────────────────────┘     └──────────────────────────────────┘
         │                                          │
         └──── PostgreSQL (session state) ──────────┘
```

The gateway manages tenant lifecycle and session orchestration. Workers run the voice pipeline and connect directly to Discord voice channels — audio never flows through the gateway. Control signals (start/stop/heartbeat) use gRPC. In `--mode=full`, both roles run in-process with direct function calls instead of gRPC.

See [Multi-Tenant Architecture](multi-tenant.md) for details.

---

## 🔀 Data Flow

The voice pipeline is a streaming chain. Each stage is a goroutine reading from an input channel and writing to an output channel. Stages overlap — TTS starts before the LLM finishes, audio playback starts before TTS finishes.

### Cascaded Path (default)

```
 Player speaks
      │
      ▼
 ┌──────────┐   50-100ms    Local Silero VAD, no network hop
 │    VAD   │──────────────  Segments speech from silence
 └────┬─────┘
      │
      ▼
 ┌──────────┐   200-300ms   Deepgram streaming / whisper.cpp
 │   STT    │──────────────  Keyword boost from knowledge graph
 └────┬─────┘               Phonetic entity correction on final
      │
      │──────────────────▶ Speculative Memory Pre-fetch (parallel)
      │                    Vector search starts on STT partials
      ▼
 ┌──────────┐   30-50ms    In-memory graph + recent transcript
 │ Hot Ctx  │──────────────  NPC identity, scene, relationships
 │ Assembly │
 └────┬─────┘
      │
      ▼
 ┌──────────┐   300-500ms   GPT-4o-mini / Claude / Gemini Flash
 │   LLM    │──────────────  Streaming tokens via Go channel
 └────┬─────┘               MCP tool calls execute inline
      │
      ▼
 ┌──────────┐   75-150ms    ElevenLabs Flash / Coqui XTTS
 │   TTS    │──────────────  Sentence-by-sentence as tokens arrive
 └────┬─────┘
      │
      ▼
 ┌──────────┐   20-50ms     Opus encoding + platform transport
 │  Mixer   │──────────────  Priority queue, barge-in, pacing
 └────┬─────┘
      │
      ▼
 NPC voice plays
                ────────────
                Total: 650-1100ms (pipelined)
```

### S2S Path (speech-to-speech)

Audio goes directly to the S2S provider (Gemini Live or OpenAI Realtime), which handles recognition, generation, and synthesis in a single API call. Audio streams back through the same mixer and transport layer.

**Latency:** 150-600ms first audio, depending on provider.

### Memory Write-back (shared)

After both paths, the complete exchange (player utterance + NPC response) is written to the session transcript (L1). A background goroutine runs phonetic correction and entity extraction for the knowledge graph (L3).

---

## 📦 Key Packages

### Application Layer (`cmd/` and `internal/`)

| Package | Location | Responsibility |
|---------|----------|----------------|
| `cmd/glyphoxa` | `cmd/glyphoxa/` | Entry point. Parses flags, loads config, wires providers, starts the app, handles signals (SIGINT/SIGTERM). |
| `internal/app` | `internal/app/` | Top-level wiring. Creates and connects all subsystems via functional options. Owns `App.Run()` lifecycle and `SessionManager` for multi-guild sessions. |
| `internal/agent` | `internal/agent/` | `NPCAgent` and `Router` interfaces. NPC identity, scene context, utterance handling. Sub-packages: `orchestrator` (address detection, turn-taking, utterance buffering), `npcstore` (PostgreSQL-backed NPC definitions). |
| `internal/engine` | `internal/engine/` | `VoiceEngine` interface — the core abstraction over the conversational loop. Sub-packages: `cascade` (STT→LLM→TTS pipeline), `s2s` (Gemini Live / OpenAI Realtime wrapper). |
| `internal/config` | `internal/config/` | Configuration schema, YAML loader, environment variable overlay, provider registry, file watcher for hot-reload, and config diffing. |
| `internal/discord` | `internal/discord/` | Discord bot layer. Slash command router, interaction handlers (`/npc`, `/session`, `/entity`, `/campaign`, `/recap`, `/feedback`), DM role permissions, voice command filtering, pipeline stats dashboard. |
| `internal/mcp` | `internal/mcp/` | MCP host interface and implementation. Tool registry with budget tiers, latency calibration, LLM-to-MCP bridge. Built-in tools: dice roller, rules lookup, memory query, file I/O. |
| `internal/session` | `internal/session/` | Session lifecycle management. Context window tracking with auto-summarisation, memory guard (L1 write-through), reconnection handling, transcript consolidation. |
| `internal/hotctx` | `internal/hotctx/` | Hot context assembly and formatting. Concurrent fetch of NPC identity (L3), recent transcript (L1), and scene context. Speculative memory pre-fetch on STT partials. Target: <50ms. |
| `internal/observe` | `internal/observe/` | OpenTelemetry metrics (Prometheus exporter), distributed tracing, HTTP middleware for latency/status instrumentation, per-provider metric recording. |
| `internal/entity` | `internal/entity/` | Entity management: CRUD operations, YAML campaign import, VTT import (Foundry VTT, Roll20), in-memory store. |
| `internal/transcript` | `internal/transcript/` | Transcript correction pipeline. Phonetic matching against known entity names, LLM-based correction for low-confidence segments, verification. |
| `internal/resilience` | `internal/resilience/` | Provider failover with circuit breakers. `LLMFallback`, `STTFallback`, `TTSFallback` — each wraps multiple backends and auto-switches on failure. |
| `internal/health` | `internal/health/` | HTTP health endpoints. `/healthz` (liveness) and `/readyz` (readiness with pluggable checkers). |
| `internal/feedback` | `internal/feedback/` | Closed-alpha feedback storage. Append-only JSON lines file store. |
| `internal/gateway` | `internal/gateway/` | Multi-tenant gateway: admin API, bot management, session orchestration, usage tracking, gRPC transport |
| `internal/session` | `internal/session/` | Voice pipeline lifecycle: Runtime, WorkerHandler, context management |
| `internal/observe` | `internal/observe/` | Observability: Prometheus metrics, OTel traces, structured logging, HTTP middleware |
| `internal/health` | `internal/health/` | Health probes: `/healthz`, `/readyz` with pluggable readiness checkers |
| `internal/resilience` | `internal/resilience/` | Resilience: circuit breaker for gRPC clients and provider calls |

### Public Libraries (`pkg/`)

| Package | Location | Responsibility |
|---------|----------|----------------|
| `pkg/audio` | `pkg/audio/` | `Platform` and `Connection` interfaces for voice channel connectivity. `AudioFrame` types, drain utilities. Sub-packages: `discord` (disgo voice adapter, Opus encode/decode), `webrtc` (Pion-based WebRTC platform, signaling, transport), `mixer` (priority queue with barge-in, natural pacing, heap-based scheduling), `mock`. |
| `pkg/memory` | `pkg/memory/` | Three-layer memory interfaces: `SessionStore` (L1), `SemanticIndex` (L2), `KnowledgeGraph` / `GraphRAGQuerier` (L3). Query options, schema SQL. Sub-packages: `postgres` (pgx/pgvector implementation, knowledge graph with recursive CTEs, semantic index), `mock`. |
| `pkg/provider` | `pkg/provider/` | Provider interfaces and implementations for all external AI services. Sub-packages by capability: `llm` (Provider interface + any-llm-go adapter), `stt` (Provider interface + Deepgram, whisper.cpp), `tts` (Provider interface + ElevenLabs, Coqui XTTS), `s2s` (Provider interface + Gemini Live, OpenAI Realtime), `vad` (Engine interface + Silero), `embeddings` (Provider interface + OpenAI, Ollama). Each has a `mock` sub-package. |
| `pkg/memory/export` | `pkg/memory/export/` | Campaign export/import as `.tar.gz` archives |

---

## 🧩 Interface-First Design

Every subsystem in Glyphoxa defines a Go interface as its public contract. Concrete implementations satisfy the interface, and compile-time assertions verify correctness at build time.

### The Pattern

```go
// 1. Define the interface (in a "types" or package-level file)
type Provider interface {
    StreamCompletion(ctx context.Context, req CompletionRequest) (<-chan Chunk, error)
    Complete(ctx context.Context, req CompletionRequest) (*CompletionResponse, error)
    CountTokens(messages []Message) (int, error)
    Capabilities() ModelCapabilities
}

// 2. Implement it (in a provider-specific package)
type AnyLLMProvider struct { /* ... */ }

// 3. Compile-time assertion (top of the implementation file)
var _ llm.Provider = (*AnyLLMProvider)(nil)
```

The `var _ Interface = (*Impl)(nil)` line ensures the compiler checks that `*Impl` satisfies `Interface` at build time, catching missing methods before any test runs.

### Where This Pattern Appears

| Interface | Package | Implementations |
|-----------|---------|-----------------|
| `audio.Platform` | `pkg/audio` | `discord.Platform`, `webrtc.Platform` |
| `audio.Connection` | `pkg/audio` | `discord.Connection`, `webrtc.Connection` |
| `engine.VoiceEngine` | `internal/engine` | `cascade.Engine`, `s2s.Engine`, `mock.VoiceEngine` |
| `llm.Provider` | `pkg/provider/llm` | `anyllm.Provider`, `resilience.LLMFallback`, `mock.Provider` |
| `stt.Provider` | `pkg/provider/stt` | `deepgram.Provider`, `whisper.Provider`, `whisper.NativeProvider`, `resilience.STTFallback`, `mock.Provider` |
| `tts.Provider` | `pkg/provider/tts` | `elevenlabs.Provider`, `coqui.Provider`, `resilience.TTSFallback`, `mock.Provider` |
| `s2s.Provider` | `pkg/provider/s2s` | `gemini.Provider`, `openai.Provider`, `mock.Provider` |
| `vad.Engine` | `pkg/provider/vad` | `mock.Engine` (Silero via silero-vad-go) |
| `embeddings.Provider` | `pkg/provider/embeddings` | `openai.Provider`, `ollama.Provider`, `mock.Provider` |
| `memory.SessionStore` | `pkg/memory` | `postgres.Store`, `session.MemoryGuard`, `mock.Store` |
| `memory.KnowledgeGraph` | `pkg/memory` | `postgres.KnowledgeGraph`, `mock.Store` |
| `mcp.Host` | `internal/mcp` | `mcphost.Host`, `mock.Host` |
| `agent.NPCAgent` | `internal/agent` | `agent.NPC`, `mock.NPCAgent` |

This design means swapping any provider (e.g., replacing ElevenLabs with Coqui XTTS, or Deepgram with whisper.cpp) is a configuration change — the orchestrator never imports a concrete provider package.

---

## ⏱️ Latency Budget

The hard constraint is **< 1.2 seconds mouth-to-ear** (player finishes speaking to NPC voice starts playing). The hard limit is 2.0 seconds.

### Cascaded Pipeline Breakdown

| Stage | Budget | Hard Limit | Technique |
|-------|--------|------------|-----------|
| VAD + silence detection | 50–100ms | — | Local Silero VAD. No network hop. Sub-ms inference. |
| STT (streaming final) | 200–300ms | 500ms | Deepgram streaming. Transcript ready ~200ms after speech ends. |
| Speculative pre-fetch | 0ms (overlapped) | — | Vector search + graph query fires on STT partials, in parallel. |
| Hot context assembly | 30–50ms | 150ms | In-memory graph traversal + recent transcript slice. |
| LLM time-to-first-token | 300–500ms | 800ms | GPT-4o-mini or Gemini Flash streaming. |
| TTS time-to-first-byte | 75–150ms | 500ms | ElevenLabs Flash v2.5 streaming. |
| Audio transport overhead | 20–50ms | — | Opus encoding + platform playback. |
| **Total (pipelined)** | **650–1100ms** | **2000ms** | Pipelining overlaps STT tail with pre-fetch, LLM streaming with TTS streaming. |

### S2S Comparison

| Engine | First Audio | Trade-offs |
|--------|-------------|------------|
| Cascaded (pipelined) | 650–1100ms | Full control over voice, model, tools |
| OpenAI Realtime (mini) | 150–400ms | Limited voices, 32k context window |
| OpenAI Realtime (full) | 200–500ms | Better quality, higher cost |
| Gemini Live (flash) | 300–600ms | 128k context, session resumption, free tier |

### Why Streaming is Non-Negotiable

Without end-to-end streaming, latencies would be additive rather than overlapping:

```
Sequential:  VAD(100) + STT(300) + LLM(500) + TTS(200) + Transport(50) = 1150ms minimum
                                                                          (often 1500-2000ms)

Pipelined:   VAD(100) + STT(200) ──┐
                                   ├── LLM TTFT(400) ──┐
             Pre-fetch(parallel) ──┘                   ├── TTS TTFB(100) + Transport(50)
                                                       └── Total: ~850ms typical
```

Go's channel-based concurrency makes this natural: each stage is a goroutine reading from an input channel and writing to an output channel.

---

## 🎙️ Engine Types

Each NPC declares its engine type in configuration. The `VoiceEngine` interface unifies all types so the orchestrator is engine-agnostic.

### Cascaded Engine (`cascade.Engine`)

The full **STT → LLM → TTS** pipeline. Each stage is a separate provider, giving maximum flexibility:

- **Use when:** You need distinct voices (ElevenLabs voice cloning), specific LLM choice (Claude for reasoning, GPT-4o-mini for speed), tool calling, or fine-grained control.
- **Latency:** 650–1100ms first audio.
- **Location:** `internal/engine/cascade/`

### S2S Engine (`s2s.Engine`)

A single API call handles audio-in to audio-out. Wraps **Gemini Live** or **OpenAI Realtime**.

- **Use when:** Lowest latency is the priority and you can accept the provider's built-in voices and limited tool support.
- **Latency:** 150–600ms first audio.
- **Location:** `internal/engine/s2s/`

### Sentence Cascade (experimental)

A dual-model approach: a **fast model** (GPT-4o-mini) generates the opening sentence for immediate TTS playback (~500ms), while a **strong model** (Claude Sonnet) generates the substantive continuation in parallel. The listener hears a single continuous utterance.

- **Use when:** You want perceived sub-600ms latency with the quality of a strong model.
- **Status:** Experimental. See [design/05-sentence-cascade.md](design/05-sentence-cascade.md).
- **Location:** `internal/engine/cascade/` (built as a mode of the cascade engine)

| Engine | First Audio | Voice Control | Tool Calling | Context Window |
|--------|-------------|---------------|--------------|----------------|
| Cascaded | 650–1100ms | Full (any TTS provider) | Full (MCP budget tiers) | Provider-dependent |
| S2S (Gemini) | 300–600ms | Provider voices only | Limited | 128k tokens |
| S2S (OpenAI) | 150–500ms | Provider voices only | Limited | 32k tokens |
| Sentence Cascade | ~500ms perceived | Full | Full | Provider-dependent |

---

## 📚 Design Documents

The full design is captured in a series of detailed documents. This architecture overview is the starting point; each design doc goes deep on its topic.

| # | Document | Description |
|---|----------|-------------|
| 00 | [Overview](design/00-overview.md) | Vision, product principles, core capabilities, performance targets |
| 01 | [Architecture](design/01-architecture.md) | System layers, detailed data flow, audio mixing, streaming requirements |
| 02 | [Providers](design/02-providers.md) | LLM, STT, TTS, S2S, Audio platform interfaces and provider trade-offs |
| 03 | [Memory](design/03-memory.md) | Three-layer hybrid memory: session log, semantic index, knowledge graph |
| 04 | [MCP Tools](design/04-mcp-tools.md) | Tool integration, budget tiers, built-in tools, performance constraints |
| 05 | [Sentence Cascade](design/05-sentence-cascade.md) | Experimental dual-model cascade for perceived sub-600ms latency |
| 06 | [NPC Agents](design/06-npc-agents.md) | Agent design, multi-NPC orchestration, address detection, turn-taking |
| 07 | [Technology](design/07-technology.md) | Why Go, dependency stack, CGo decisions, latency budget breakdown |
| 08 | [Open Questions](design/08-open-questions.md) | Unresolved design questions and decisions in progress |
| 09 | [Roadmap](design/09-roadmap.md) | Development phases and milestone planning |
| 10 | [Knowledge Graph](design/10-knowledge-graph.md) | L3 graph schema, PostgreSQL adjacency tables, recursive CTEs, GraphRAG |
| — | [To Be Discussed](design/to-be-discussed.md) | Items pending team discussion |

---

## 👀 See Also

- [getting-started.md](getting-started.md) — Setup, build, and first run guide
- [providers.md](providers.md) — Provider configuration and swapping guide
- [audio-pipeline.md](audio-pipeline.md) — Deep dive into audio transport, mixing, and barge-in
- [memory.md](memory.md) — Memory system usage, hot context, and knowledge graph queries
- [design/00-overview.md](design/00-overview.md) — Vision and product principles
- [design/01-architecture.md](design/01-architecture.md) — Detailed system architecture
- [design/07-technology.md](design/07-technology.md) — Technology decisions and dependency stack
- [CONTRIBUTING.md](https://github.com/MrWong99/glyphoxa/blob/main/CONTRIBUTING.md) — Development setup, code style, and workflow
