# Voice Pipeline End-to-End Test Plan

## Overview

This plan describes a comprehensive end-to-end test suite for the Glyphoxa voice pipeline, verifying multi-NPC behaviour with pre-recorded audio input. Tests focus on observable behaviour — latency, correct NPC addressing, barge-in reliability, speaker switching, and concurrent arbitration — rather than exact response text.

**Goal:** Catch regressions in the voice pipeline's real-time behaviour before they reach production, running entirely in CI without Discord, API keys, or human participants.

---

## Design Principles

1. **Build on the existing loopback harness** — extend `pkg/audio/loopback.Connection` and the pipeline wiring from `internal/app/pipeline_loopback_test.go` (PR #78). No new test framework.
2. **Mock only external dependencies** — use real mixer (`mixer.PriorityMixer`), real orchestrator (`orchestrator.Orchestrator`), real address detector, real pipeline wiring. Mock VAD, STT, LLM, TTS.
3. **Test behaviour, not content** — never assert on exact NPC response text. Assert on which NPC responded, timing, audio output presence, and interrupt semantics.
4. **Deterministic by default** — scripted VAD sequences and fixed STT transcripts ensure reproducible results. No flaky network calls.
5. **Measurable** — every test records latency metrics (speech-end to first output frame) and reports them. CI can track regressions over time.

---

## Architecture

### Test Harness Stack

```
┌─────────────────────────────────────────────────────────────────┐
│                     E2E Test (Go test function)                  │
│                                                                  │
│  ┌────────────────┐  ┌──────────────────┐  ┌────────────────┐   │
│  │  Loopback      │  │  Instrumented    │  │  Assertion      │   │
│  │  Connection    │  │  Mock Providers  │  │  Helpers        │   │
│  │  (extended)    │  │  (timing-aware)  │  │  (latency,      │   │
│  │                │  │                  │  │   routing, etc.) │   │
│  └───────┬────────┘  └────────┬─────────┘  └────────────────┘   │
│          │                    │                                   │
│  ┌───────┴────────────────────┴──────────────────────────────┐   │
│  │              Real Pipeline Wiring                          │   │
│  │   Loopback → VAD → STT → Orchestrator → NPC → Mixer →    │   │
│  │                                              Loopback      │   │
│  └────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────┘
```

### Component Roles

| Component | Implementation | Why |
|-----------|---------------|-----|
| Audio transport | `loopback.Connection` (extended) | No Discord/WebRTC needed |
| VAD | `scriptedVADSession` | Deterministic speech segments |
| STT | `scriptedSTTProvider` | Returns configurable transcripts per speech segment |
| LLM | not used directly | NPC agents mock the engine |
| NPC Agent | `instrumentedNPCAgent` | Wraps `respondingNPCAgent` with timing hooks |
| Mixer | **real** `mixer.PriorityMixer` | Tests actual priority queue, barge-in, gap logic |
| Orchestrator | **real** `orchestrator.Orchestrator` | Tests actual address detection, routing, muting |
| Address Detector | **real** (built by orchestrator) | Tests actual name matching |
| Pipeline | **real** `audioPipeline` | Tests actual frame routing, participant workers |

### Loopback Connection Extensions

The existing `loopback.Connection` needs two additions:

```go
// TimedParticipant extends Participant with frame-level timing control.
type TimedParticipant struct {
    Participant
    // DelayBetweenFrames controls playback speed. Zero means all frames
    // are available immediately (current behaviour). Non-zero simulates
    // real-time playback at the given interval.
    DelayBetweenFrames time.Duration
}

// OutputEvent records a captured frame with its arrival timestamp.
type OutputEvent struct {
    Frame     audio.AudioFrame
    Timestamp time.Time
}
```

Add a `NewTimed(participants []TimedParticipant)` constructor that drip-feeds frames at the specified interval using a goroutine per participant. This lets tests simulate realistic timing (e.g., 30ms per frame = real-time 16kHz audio) without modifying the existing `New()` constructor.

Add `CapturedOutputTimed() []OutputEvent` to record timestamps alongside frames, enabling latency measurement.

### Instrumented NPC Agent

Wraps an NPC agent to record timing:

```go
type instrumentedNPCAgent struct {
    agent.NPCAgent
    mu              sync.Mutex
    calls           []utteranceCall
    responseLatency []time.Duration
}

type utteranceCall struct {
    Speaker    string
    Transcript string
    CalledAt   time.Time
}
```

`HandleUtterance` records the call timestamp. The test helper correlates this with the last VAD `SpeechEnd` timestamp to compute routing latency, and with the first output frame timestamp to compute end-to-end latency.

### Scripted STT Provider

Instead of the existing `echoSTTProvider` that returns one fixed string, use a `scriptedSTTProvider` that returns different transcripts per invocation:

```go
type scriptedSTTProvider struct {
    mu         sync.Mutex
    transcripts []string // FIFO: first call gets transcripts[0], etc.
    callIndex   int
}
```

This lets tests script a conversation: first utterance says "Hey Grimjaw, how are you?", second says "Greymantle, what do you know about dragons?", etc.

---

## Pre-Recorded Audio Samples

### What's Needed

Tests do not need real speech audio. Since VAD and STT are mocked, the audio content doesn't matter — only the frame count, timing, and silence/speech structure matter.

**Synthetic frames are sufficient:**

```go
func makeSpeechFrames(count int) []audio.AudioFrame {
    frames := make([]audio.AudioFrame, count)
    for i := range frames {
        frames[i] = audio.AudioFrame{
            Data:       make([]byte, 960), // 30ms @ 16kHz mono, 16-bit
            SampleRate: 16000,
            Channels:   1,
        }
    }
    return frames
}
```

### Frame Timing Reference

| Parameter | Value | Notes |
|-----------|-------|-------|
| Sample rate | 16000 Hz | Standard for STT input |
| Frame size | 30 ms | 480 samples * 2 bytes = 960 bytes |
| Typical utterance | 40-60 frames | ~1.2-1.8s of speech |
| Silence gap | 10-15 frames | 300-450ms between utterances |
| Barge-in overlap | 5-10 frames | 150-300ms of overlap before interrupt |

### Future Enhancement: Real Audio Samples

For smoke tests with real providers (gated by `GLYPHOXA_TEST_REAL_PROVIDERS=1`), pre-record PCM files:

```
testdata/audio/
├── address-grimjaw.pcm      # "Hey Grimjaw, how's the forge?"
├── address-greymantle.pcm    # "Greymantle, tell me about the prophecy"
├── interrupt-early.pcm       # Short burst mid-sentence
├── interrupt-late.pcm        # Interruption near end of NPC response
├── rapid-switch.pcm          # "Grimjaw... actually, Greymantle, you tell me"
└── silence.pcm               # 2 seconds of silence (control)
```

Conversion: `ffmpeg -i input.wav -f s16le -acodec pcm_s16le -ar 16000 -ac 1 output.pcm`

---

## Test Scenarios

### Scenario 1: Correct NPC Addressing (Single Speaker)

**Setup:**
- 2 NPCs: "Grimjaw the Blacksmith" (cascaded), "Greymantle the Sage" (cascaded)
- 1 player participant
- Scripted STT returns `"Hey Grimjaw, how is the forge today?"`

**VAD script:** silence(5) → speech(40) → silence(10)

**Assertions:**
- [ ] Grimjaw's agent receives `HandleUtterance` — not Greymantle's
- [ ] Grimjaw's agent receives correct speaker ID
- [ ] Output frames appear on the loopback connection (NPC responded)
- [ ] Greymantle's agent call count is zero
- [ ] Latency from speech-end to first output frame < 200ms (mock stack, no network)

**Subtest variants (table-driven):**

| Name | Transcript | Expected target |
|------|-----------|-----------------|
| full name | "Grimjaw the Blacksmith, sell me a sword" | Grimjaw |
| first name only | "Grimjaw, hello" | Grimjaw |
| other NPC | "Greymantle, what prophecy?" | Greymantle |
| partial name | "Ask the Blacksmith about iron" | Grimjaw |
| no name (last-speaker) | "Tell me more" (after Grimjaw spoke) | Grimjaw |
| no name (single unmuted) | "Tell me more" (Greymantle muted) | Grimjaw |

---

### Scenario 2: Speaker Switching

**Setup:**
- 2 NPCs: Grimjaw, Greymantle
- 1 player, 3 sequential utterances

**VAD script:** silence(5) → speech(40) → silence(15) → speech(40) → silence(15) → speech(40) → silence(10)

**STT script (FIFO):**
1. "Grimjaw, how's business?"
2. "Greymantle, any prophecies lately?"
3. "Back to you, Grimjaw — what about that shipment?"

**Assertions:**
- [ ] Utterance 1 routes to Grimjaw
- [ ] Utterance 2 routes to Greymantle (switch)
- [ ] Utterance 3 routes to Grimjaw (switch back)
- [ ] Each NPC receives exactly the utterances addressed to it
- [ ] Output frames produced for all 3 utterances
- [ ] `lastSpeaker` state updates correctly between turns

---

### Scenario 3: Rapid Speaker Switching

**Setup:** Same NPCs, 1 player, 5 rapid-fire utterances with minimal silence gaps.

**VAD script:** 5 speech segments with only 5-frame (150ms) silence gaps between them.

**STT script:**
1. "Grimjaw" → Grimjaw
2. "Greymantle" → Greymantle
3. "Grimjaw" → Grimjaw
4. "Greymantle" → Greymantle
5. "Grimjaw" → Grimjaw

**Assertions:**
- [ ] All 5 utterances route to the correct NPC (alternating)
- [ ] No deadlocks or race conditions under rapid switching (race detector will catch)
- [ ] Barge-in fires correctly when new speech starts during NPC response from previous turn
- [ ] All NPC agents eventually receive their utterances (ordering may vary due to barge-in)

---

### Scenario 4: Barge-In — Early Interrupt

**Setup:** 2 NPCs (Grimjaw, Greymantle), 1 player.

**Sequence:**
1. Player addresses Grimjaw → Grimjaw starts responding (mock agent enqueues 20 output frames)
2. After 5 frames play, player starts speaking again → VAD fires SpeechStart
3. Expected: mixer.BargeIn fires, remaining 15 frames are dropped, queue cleared

**Implementation:**
- Use `TimedParticipant` with `DelayBetweenFrames: 20ms` for realistic playback timing
- NPC mock enqueues a slow-draining audio segment (20ms per frame via a channel that delivers frames with delays)
- Second speech segment starts 100ms after first output frame

**Assertions:**
- [ ] Mixer `BargeIn` fires (verified via mock callback counter)
- [ ] Output frame count < 20 (some frames were dropped due to interrupt)
- [ ] OnDone callback on the first segment fires with `interrupted=true`
- [ ] Second utterance is processed (player's new speech goes through pipeline)

---

### Scenario 5: Barge-In — Late Interrupt

**Setup:** Same as Scenario 4, but player interrupts near the end of NPC response.

**Sequence:**
1. Player addresses Grimjaw → 20 output frames enqueued
2. After 17 frames play (~510ms), player starts speaking
3. Expected: only 3 frames dropped, barge-in still fires

**Assertions:**
- [ ] Barge-in fires
- [ ] Output count is between 15-19 (most frames played before interrupt)
- [ ] Pipeline correctly processes the interrupting utterance

---

### Scenario 6: Barge-In — Rapid Back-to-Back

**Setup:** 2 NPCs, 1 player who interrupts three times in rapid succession.

**Sequence:**
1. Address Grimjaw → response starts
2. Interrupt after 3 frames → new utterance for Greymantle
3. Interrupt Greymantle after 3 frames → new utterance for Grimjaw
4. Let Grimjaw finish

**Assertions:**
- [ ] 3 barge-in events fire
- [ ] Final response (Grimjaw's third) plays to completion
- [ ] No goroutine leaks (checked via runtime.NumGoroutine delta)
- [ ] No panics or race conditions

---

### Scenario 7: Concurrent NPC Triggering

**Setup:** 2 NPCs, 2 players speaking simultaneously.

**Participants:**
- Player A addresses Grimjaw
- Player B addresses Greymantle (both speech segments start at the same frame)

**VAD scripts:** Both participants have speech at the same time.

**Assertions:**
- [ ] Both agents receive their respective utterances
- [ ] Mixer serializes output correctly (one NPC plays, other queues)
- [ ] Priority ordering is respected (equal priority → FIFO)
- [ ] Both NPC responses eventually produce output frames
- [ ] No race conditions

---

### Scenario 8: Mute During Speech

**Setup:** 2 NPCs, 1 player.

**Sequence:**
1. Address Grimjaw → response starts playing
2. Mid-playback, call `orchestrator.MuteAgent("grimjaw")`
3. Expected: Grimjaw's output stops, further utterances to Grimjaw return `ErrNoTarget`

**Assertions:**
- [ ] `InterruptNPC("grimjaw", DMOverride)` fires on mixer
- [ ] Grimjaw's audio stops
- [ ] Greymantle's agent is unaffected
- [ ] Subsequent "Hey Grimjaw" returns `ErrNoTarget` from Route
- [ ] After `UnmuteAgent("grimjaw")`, routing resumes normally

---

### Scenario 9: No Speech (Silence Control)

**Setup:** 2 NPCs, 1 player, all-silence input.

**Assertions:**
- [ ] No `HandleUtterance` calls on either agent
- [ ] Zero output frames
- [ ] Pipeline shuts down cleanly

---

### Scenario 10: Mid-Session NPC Join

**Setup:** Start with 1 NPC (Grimjaw). After first utterance, add Greymantle via `orchestrator.AddAgent()`.

**Sequence:**
1. "Grimjaw, hello" → routes to Grimjaw (only NPC)
2. Add Greymantle to session
3. "Greymantle, hello" → routes to Greymantle

**Assertions:**
- [ ] First utterance routes to Grimjaw
- [ ] After AddAgent, address detector rebuilds index
- [ ] Second utterance routes to Greymantle
- [ ] Both NPCs produce output

---

### Scenario 11: Last-Speaker Continuation (No Name Mentioned)

**Setup:** 2 NPCs, 1 player.

**STT script:**
1. "Grimjaw, hello" → routes to Grimjaw (sets lastSpeaker)
2. "Tell me more" (no name) → should route to Grimjaw (lastSpeaker continuation)
3. "Greymantle, hello" → routes to Greymantle (updates lastSpeaker)
4. "And then what happened?" (no name) → should route to Greymantle

**Assertions:**
- [ ] Utterances 1 and 2 go to Grimjaw
- [ ] Utterances 3 and 4 go to Greymantle
- [ ] Last-speaker state tracks correctly across turns

---

### Scenario 12: Address-Only NPC

**Setup:** 2 NPCs — Grimjaw (normal), Greymantle (addressOnly=true).

**STT script:**
1. "Grimjaw, hello" → Grimjaw (sets lastSpeaker, but Greymantle is addressOnly so shouldn't affect it)
2. "Tell me more" (no name) → should route to Grimjaw (Greymantle is addressOnly, not eligible for last-speaker)
3. "Greymantle, hello" → Greymantle (explicit address works)
4. "Tell me more" (no name) → should route to Grimjaw (Greymantle is addressOnly, lastSpeaker should still be Grimjaw)

**Assertions:**
- [ ] AddressOnly NPC never receives last-speaker continuation utterances
- [ ] AddressOnly NPC still receives explicitly addressed utterances

---

## Latency Measurement

### Measurement Points

```
                    T0              T1                T2              T3
                    │               │                 │               │
 Player speaks ─────┤  VAD SpeechEnd ├── Route + Agent ├── First Frame  │
                    │               │                 │  from Mixer    │
                    │               │                 │               │
                    ├─── VAD time ──┤                 │               │
                    │               ├── Routing time ─┤               │
                    │               │                 ├── Agent time ─┤
                    │               │                 │               │
                    ├───────── End-to-end latency ────────────────────┤
```

| Metric | Start | End | Expected (mock stack) |
|--------|-------|-----|----------------------|
| VAD latency | First speech frame submitted | SpeechEnd event | ~0ms (scripted) |
| Routing latency | SpeechEnd | Route returns target agent | <1ms |
| Agent processing | HandleUtterance called | First frame enqueued to mixer | <5ms (mock) |
| Mixer dispatch | Frame enqueued | Frame delivered to output callback | <1ms |
| **End-to-end** | **SpeechEnd event** | **First OutputEvent timestamp** | **<50ms (mock stack)** |

### Implementation

```go
type latencyRecorder struct {
    mu      sync.Mutex
    records []latencyRecord
}

type latencyRecord struct {
    Scenario         string
    SpeechEndAt      time.Time
    RouteCompleteAt  time.Time
    AgentCalledAt    time.Time
    FirstOutputAt    time.Time
    EndToEndLatency  time.Duration
    RoutingLatency   time.Duration
    AgentLatency     time.Duration
}
```

Each test scenario records latencies and logs them via `t.Logf`. A CI-visible summary can be extracted from test output.

### Latency Budget Assertions

For the mock stack (no network, no real providers):

| Metric | Hard limit | Notes |
|--------|-----------|-------|
| End-to-end | 200ms | Conservative for mocked stack; real stack is <1200ms |
| Routing | 5ms | Address detection is in-memory string matching |
| Agent dispatch | 50ms | Mock agent enqueues frames immediately |

These limits catch performance regressions (e.g., a new lock contention, accidental blocking I/O) without being so tight that tests flake.

---

## CI Integration

### Test File Location

```
internal/app/
├── pipeline_loopback_test.go          # existing loopback tests (PR #78)
└── pipeline_e2e_test.go               # new E2E multi-NPC test suite

pkg/audio/loopback/
├── connection.go                      # existing loopback connection
├── connection_test.go                 # existing tests
├── timed.go                           # NEW: TimedParticipant, OutputEvent, NewTimed()
└── timed_test.go                      # NEW: tests for timed extensions
```

### Build Tags

No special build tags. All E2E tests run with `make test` and `go test -race -count=1 ./...`. They require no external services.

Future real-provider smoke tests will be gated by environment variable:

```go
func TestE2E_RealProviders(t *testing.T) {
    if os.Getenv("GLYPHOXA_TEST_REAL_PROVIDERS") == "" {
        t.Skip("GLYPHOXA_TEST_REAL_PROVIDERS not set")
    }
    // ... tests with real VAD, STT, LLM, TTS ...
}
```

### Resource Requirements

- **CPU:** Minimal — no ONNX inference, no network calls
- **Memory:** <50 MB — synthetic audio frames, mock providers
- **Time:** <30s for the full E2E suite (all scenarios run in parallel via `t.Parallel()`)
- **Dependencies:** None beyond `go test` — no Docker, no API keys, no Discord

### CI Pipeline Integration

```yaml
# In existing CI workflow (GitHub Actions):
- name: Run tests
  run: make test  # includes E2E tests automatically
```

No changes to CI config needed. The tests are standard Go tests that run with the existing `make test` target.

---

## Implementation Plan

### Phase 1: Loopback Extensions (1-2 days)

1. **Add `TimedParticipant` and `NewTimed()`** to `pkg/audio/loopback/`
   - Frame drip-feeding goroutine with configurable delay
   - `OutputEvent` with timestamps for latency measurement
   - `CapturedOutputTimed()` method
   - Unit tests

2. **Add `scriptedSTTProvider`** to test helpers
   - FIFO transcript queue (thread-safe)
   - Records call count and timestamps

3. **Add `instrumentedNPCAgent`** wrapper
   - Records `HandleUtterance` call timestamps
   - Wraps existing `respondingNPCAgent` pattern

4. **Add `latencyRecorder`** helper
   - Records timing at each pipeline stage
   - `t.Logf` summary output

### Phase 2: Core Test Scenarios (2-3 days)

5. **Scenario 1: Correct NPC Addressing** — table-driven with 6+ variants
6. **Scenario 2: Speaker Switching** — 3 sequential utterances alternating NPCs
7. **Scenario 9: Silence Control** — baseline sanity check
8. **Scenario 11: Last-Speaker Continuation** — 4 utterances testing state tracking
9. **Scenario 12: Address-Only NPC** — verify addressOnly flag behaviour

### Phase 3: Barge-In Scenarios (2-3 days)

10. **Scenario 4: Early Interrupt** — barge-in after 25% of response
11. **Scenario 5: Late Interrupt** — barge-in after 85% of response
12. **Scenario 6: Rapid Back-to-Back** — 3 interrupts in quick succession
13. **Scenario 8: Mute During Speech** — `MuteAgent` stops output immediately

### Phase 4: Advanced Scenarios (1-2 days)

14. **Scenario 3: Rapid Speaker Switching** — 5 alternating utterances, 150ms gaps
15. **Scenario 7: Concurrent NPC Triggering** — 2 players, 2 NPCs, simultaneous speech
16. **Scenario 10: Mid-Session NPC Join** — dynamic agent registration

### Phase 5: Real Provider Smoke Tests (optional, gated)

17. **Record PCM test audio** — 5-6 short clips
18. **Wire real Silero VAD** — requires ONNX runtime
19. **Wire real Deepgram STT** — requires API key
20. **Verify transcripts match** expected phrases

---

## Test Helper Design

### `e2eTestConfig` — Scenario Builder

```go
type e2eTestConfig struct {
    NPCs          []npcConfig
    Participants  []participantConfig
    VADScripts    map[string][]vad.VADEventType  // participantID → event sequence
    STTScripts    []string                        // FIFO transcripts
    ExpectRoutes  []expectedRoute                 // which NPC per utterance
    Timeout       time.Duration
}

type npcConfig struct {
    Name         string
    ID           string
    AddressOnly  bool
    ResponseFrames int  // how many output frames the mock agent enqueues
}

type participantConfig struct {
    UserID         string
    Username       string
    FrameCount     int
    FrameDelay     time.Duration
}

type expectedRoute struct {
    NPCID    string
    Transcript string
}
```

This builder pattern keeps individual test cases concise:

```go
func TestE2E_CorrectAddressing(t *testing.T) {
    t.Parallel()

    tests := []struct {
        name       string
        transcript string
        wantNPC    string
    }{
        {"full name", "Grimjaw the Blacksmith, sell me a sword", "grimjaw"},
        {"first name", "Grimjaw, hello", "grimjaw"},
        {"other NPC", "Greymantle, what prophecy?", "greymantle"},
        // ...
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            t.Parallel()
            cfg := e2eTestConfig{
                NPCs: []npcConfig{
                    {Name: "Grimjaw the Blacksmith", ID: "grimjaw"},
                    {Name: "Greymantle the Sage", ID: "greymantle"},
                },
                STTScripts: []string{tt.transcript},
                ExpectRoutes: []expectedRoute{{NPCID: tt.wantNPC}},
            }
            result := runE2E(t, cfg)
            result.AssertRouting(t)
            result.AssertLatency(t, 200*time.Millisecond)
        })
    }
}
```

### `e2eResult` — Assertion Helpers

```go
type e2eResult struct {
    RoutedTo    []routeRecord        // which NPC received each utterance
    OutputCount int                  // total output frames
    Latencies   []latencyRecord      // per-utterance timing
    BargeIns    int                  // barge-in event count
    AgentCalls  map[string]int       // agent ID → HandleUtterance call count
}

func (r *e2eResult) AssertRouting(t *testing.T) { ... }
func (r *e2eResult) AssertLatency(t *testing.T, maxE2E time.Duration) { ... }
func (r *e2eResult) AssertBargeInCount(t *testing.T, expected int) { ... }
func (r *e2eResult) AssertNoOutput(t *testing.T) { ... }
func (r *e2eResult) AssertOutputCount(t *testing.T, min, max int) { ... }
```

---

## Risk Analysis

| Risk | Mitigation |
|------|-----------|
| Timing-sensitive tests flake in CI | Use generous timeouts (5s for mock stack). Assert latency < 200ms, not < 5ms. |
| Mock stack diverges from real behaviour | Phase 5 (real provider tests) catches divergence. Mock interfaces match production interfaces. |
| Barge-in tests are order-dependent | Use `TimedParticipant` with precise delays. Verify with `-count=100` locally. |
| Test complexity makes maintenance hard | Builder pattern (`e2eTestConfig`) keeps each test case 10-20 lines. Shared helpers in `e2e_helpers_test.go`. |
| Goroutine leaks in long-running tests | Record `runtime.NumGoroutine()` before/after. Assert delta < 5. Use `t.Cleanup()` for teardown. |

---

## Success Criteria

The E2E test suite is complete when:

1. All 12 scenarios pass with `-race -count=1`
2. All scenarios pass with `-race -count=10` (no flakes)
3. Total suite runtime < 30 seconds
4. Latency metrics are logged and inspectable in CI output
5. No external dependencies required (no Docker, API keys, or network)
6. `make check` passes with the new tests included
7. Tests catch at least the following known-fixed bugs if reintroduced:
   - Barge-in not firing `OnBargeIn` callback
   - Mute not stopping current NPC speech
   - Address detector matching 3-character words (false positives)
   - Last-speaker not updating on explicit address
