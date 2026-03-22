# Integration Test Harness — Loopback Pipeline

## Overview

The integration test harness verifies the full Glyphoxa voice pipeline **without a real voice platform** (Discord, WebRTC, etc.). It uses a loopback audio connection that feeds pre-recorded PCM frames as input and captures all output frames for verification.

**What it tests:**
```
Audio in → VAD → STT → Orchestrator routing → NPC agent → Mixer → Audio out
```

**What it doesn't need:**
- Discord bot token or voice channel
- Real STT/LLM/TTS API keys
- Running Kubernetes cluster
- Human in a voice channel

## Architecture

### Loopback Connection (`pkg/audio/loopback/`)

A test implementation of `audio.Connection` that:
- Streams pre-loaded PCM frames as participant input (simulating players speaking)
- Captures all output frames written by the mixer (NPC responses)
- Supports mid-session participant joins via `AddParticipant()`
- Provides `WaitForOutput(n, timeout)` for synchronization

### Mock Provider Stack

| Component | Mock | Behaviour |
|-----------|------|-----------|
| **VAD** | `sequenceVADSession` | Follows a scripted event sequence (silence → speech → silence) |
| **STT** | `echoSTTProvider` | Returns a fixed transcript after receiving audio |
| **NPC Agent** | `respondingNPCAgent` | Records calls + enqueues test audio to mixer |
| **Mixer** | `mixer.PriorityMixer` | **Real mixer** — not mocked |
| **Connection** | `loopback.Connection` | **Real connection** — loopback variant |

The real mixer and real audio pipeline (`audioPipeline`) are used. Only external dependencies (VAD model, STT service, LLM, TTS) are mocked.

## Running the Tests

```bash
# Run all loopback integration tests
go test -race -count=1 -v -run 'TestPipelineLoopback' ./internal/app/

# Run a specific test
go test -race -count=1 -v -run 'TestPipelineLoopback_EndToEnd' ./internal/app/

# Run the loopback connection unit tests
go test -race -count=1 -v ./pkg/audio/loopback/...

# Run as part of the full suite
make test
```

## Test Cases

### `TestPipelineLoopback_EndToEnd`
Full pipeline test with a single participant. Verifies:
- 20 PCM frames are fed through the pipeline
- VAD detects a speech segment (frames 2-15)
- STT produces a transcript
- Orchestrator routes to the correct NPC
- NPC agent receives the transcript with correct speaker ID
- Mixer produces 5 output audio frames

### `TestPipelineLoopback_MultipleParticipants`
Two simultaneous participants. Verifies:
- Both participants are processed independently
- Barge-in behaviour works correctly (one speaker may interrupt the other)
- At least one participant's transcript reaches the NPC

### `TestPipelineLoopback_NoSpeech`
All-silence input. Verifies:
- No STT sessions are opened
- No transcripts are routed
- No output audio is produced

### `TestPipelineLoopback_MidSessionJoin`
Participant joins after the pipeline has started. Verifies:
- `OnParticipantChange` callback triggers worker creation
- Late-joining participant's audio is processed normally
- NPC responds to the newcomer

## Extending the Test Harness

### Adding Real Providers

To test with real STT/LLM/TTS (requires API keys), replace the mock providers:

```go
// Example: use real energy VAD instead of sequence mock
vadEng, _ := energy.New()

// Example: use real Deepgram STT
sttProv, _ := deepgram.New(deepgram.Config{APIKey: os.Getenv("DEEPGRAM_API_KEY")})
```

The loopback connection works with any provider stack — just swap the mocks.

### Custom Audio Input

Generate test PCM frames from a WAV file:

```bash
# Convert speech.wav to 16kHz mono PCM (little-endian int16)
ffmpeg -i speech.wav -f s16le -acodec pcm_s16le -ar 16000 -ac 1 speech.pcm
```

Then load in Go:

```go
pcmData, _ := os.ReadFile("testdata/speech.pcm")
frameSize := 16000 * 30 / 1000 * 2 // 960 bytes per 30ms frame
var frames []audio.AudioFrame
for i := 0; i+frameSize <= len(pcmData); i += frameSize {
    frames = append(frames, audio.AudioFrame{
        Data:       pcmData[i : i+frameSize],
        SampleRate: 16000,
        Channels:   1,
    })
}
```

### Testing with Real Audio + Real VAD

For a true smoke test with real speech recognition:

1. Record a short WAV file with a test phrase
2. Convert to PCM as above
3. Use `energy.New()` or `silero.New()` for VAD
4. Use `deepgram.New()` for STT
5. Use the `echoSTTProvider` pattern but with the real provider
6. Verify the transcript matches the expected phrase

## File Layout

```
pkg/audio/loopback/
├── connection.go       # Loopback Connection implementation
└── connection_test.go  # Unit tests for the connection

internal/app/
└── pipeline_loopback_test.go  # Integration tests (4 test cases)
```

## Design Decision: Why Option B (Loopback)?

We evaluated four approaches:

| Option | Approach | Verdict |
|--------|----------|---------|
| A | Mock at gRPC boundary | Too coupled to gateway internals |
| **B** | **Loopback in full mode** | **Most practical — tests full pipeline, no external deps** |
| C | Second Discord bot | Realistic but complex, needs extra bot token |
| D | Gateway debug endpoint | Requires modifying production code |

Option B was chosen because:
- The `audio.Connection` interface is clean and mockable
- The real mixer and pipeline wiring are tested (not just mocks)
- No network, no Discord, no API keys needed for CI
- Can be extended to use real providers when API keys are available
