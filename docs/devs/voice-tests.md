# Voice tests

Voice tests exercise the **internal integration layer**: an orchestrator stage
publishing onto a `voiceevent.Bus`, with the provider seam (VAD, STT, TTS)
swapped for a deterministic stand-in. The test observes the bus and asserts on
the events that came out. No network, no audio devices, sub-second.

The *why* lives in the ADRs — read these once before adding tests:

- [ADR-0019](../adr/0019-orchestrator-first-tdd-voice.md) — orchestrator-first TDD; test the stage, not the vendor.
- [ADR-0020](../adr/0020-shared-voice-event-taxonomy.md) — the event taxonomy and why assertions are plain Go (no DSL), and why clip `meta.yaml` is documentation-only.
- [ADR-0021](../adr/0021-cassette-based-llm-determinism.md) — the cassette policy (replay / record / live tiers).

Vocabulary (Audio Frame, Component, Provider, Voice) is canonical in
[CONTEXT.md](../../CONTEXT.md).

## The harness

Every voice test starts the same way ([`pkg/voice/voicetest`](../../pkg/voice/voicetest/)):

```go
h := voicetest.New(t)              // owns a fresh Bus, records every event
stage := orchestrator.NewSTT(h.Bus, recognizer)  // wire the bus into the stage
// ... drive the stage ...
voicetest.AssertEvent(t, h, func(e voiceevent.STTFinal) bool { /* ... */ }, "desc")
```

Assertion primitives, all generic over the event type:

| Helper | Use it when |
| --- | --- |
| `AssertEventOccurred[T]` | type T was published at all |
| `AssertEvent[T](match, desc)` | a T satisfying a value predicate was published |
| `AssertEventCount[T](want)` | the *count* is the property (e.g. two utterances ⇒ exactly two `VADSpeechStart`) |
| `AssertOrder(steps…)` | events happened in a relative order (subsequence, not contiguous) |
| `AssertNoEvent[T]` | the negative path — T must *not* appear (e.g. silence ⇒ no speech) |

Compare transcripts with `voicetest.NormalizeTranscript` (folds case,
punctuation, spacing), never `==` — providers vary on cosmetics.

## Picking a provider stand-in

| Strategy | Stand-in | Reach for it when |
| --- | --- | --- |
| **Cassette** | `voicecassette.LoadSTT` / `LoadTTS` | pinning a real provider's response — the provider seam is part of what's under test |
| **Replay clip** | `voicetest.LoadClip` / `NewVADRig` | feeding real PCM through VAD/STT (onset, hysteresis, audio fingerprinting) |
| **Simulated STT** | an inline `stt.Recognizer` stub | testing a downstream contract independent of any provider or audio |

## Add a cassette-backed test

Cassettes pin one vendor request/response so the test is deterministic. The
cassette YAML schema and the `-tags=record` workflow live in
[`tests/voice-cassettes/README.md`](../../tests/voice-cassettes/README.md).

**STT** (replay a clip's transcript) — model on
[`stt_test.go`](../../pkg/voice/orchestrator/stt_test.go):

```go
clip := voicetest.LoadClip(t, "hello-test")
frames, _ := clip.FramesOf(t, clip.SampleRate*32/1000) // any consistent framing
recognizer := voicecassette.LoadSTT(t, "stt-hello-test")
stage := orchestrator.NewSTT(h.Bus, recognizer)
stage.Transcribe(context.Background(), frames)
// assert STTFinal text == NormalizeTranscript(expected)
```

**TTS** (stub cassette — sentences only, no audio) — model on
[`tts_test.go`](../../pkg/voice/orchestrator/tts_test.go):

```go
synth := voicecassette.LoadTTS(t, "tts-hello-test")
stage := orchestrator.NewTTS(h.Bus, synth)
stage.Dispatch(context.Background(), sentence, voicetest.LiveElevenLabsVoice())
// assert TTSInvoked{Sentence: sentence, Index: 0}
```

`LiveElevenLabsVoice()` is safe in replay mode (the voice is unobserved) and
becomes load-bearing under `-tags=record`.

## Add a clip-backed test (VAD / real PCM)

`NewVADRig` wires a real Silero session + stage + harness + pre-framed clip in
one call (fixed 16 kHz / 32 ms / 0.5–0.35 hysteresis). Model on
[`vad_test.go`](../../pkg/voice/orchestrator/vad_test.go):

```go
stage, h, frames := voicetest.NewVADRig(t, "hello-test")
for _, f := range frames { stage.Process(f) }
voicetest.AssertEventOccurred[voiceevent.VADSpeechStart](t, h)
```

For other clips: `LoadClip(t, name)` then `clip.FramesOf(t, samplesPerFrame)` —
it returns trailing samples rather than dropping them, so a clipped fixture
can't masquerade as passing.

## Add a simulated-STT test (no provider, no audio)

When the property under test is a downstream orchestrator contract — not the
recognizer — feed a stub that returns a pinned transcript. See
`stubRecognizer` in [`stt_test.go`](../../pkg/voice/orchestrator/stt_test.go),
which pins the "empty transcript still publishes `STTFinal`" contract:

```go
type stubRecognizer struct{ transcript stt.Transcript }
func (s stubRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
    return s.transcript, nil
}
```

## Wire the full pipeline (VAD → STT → address → TTS)

Don't hand-roll the cross-stage bus glue. `orchestrator.Conversation` bundles the
reactive wiring (ADR-0026): `Register(ctx)` installs it and returns a teardown
func, `Feed(frame)` is the audio loop, and the reply behaviour is injected as a
`ReplyFunc`. Model on `TestSTT_TTRPGIntro_TranscribesBothLanguages` in
[`stt_test.go`](../../pkg/voice/orchestrator/stt_test.go):

```go
conv := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
    orchestrator.WithDetector(detector),
    orchestrator.WithReply(func(e voiceevent.AddressRouted) []orchestrator.Reply { /* … */ }),
    orchestrator.WithErrorHandler(func(err error) { t.Errorf("reply dispatch: %v", err) }),
)
t.Cleanup(conv.Register(t.Context()))
for _, f := range frames { conv.Feed(f) }
```

To exercise one interaction in isolation, drop to the `Reactor` layer and
compose with `orchestrator.Bind(ctx, bus, reactors…)`; for an ad-hoc typed
subscription use `voiceevent.On[E](bus, fn)` directly.

## Adding fixtures

### A new cassette — `tests/voice-cassettes/`

One `<stage>-<scenario>.yaml` file; the filename *is* the cassette identity.
Schema, naming, and the `go test -tags=record` re-record path are in the
[cassette README](../../tests/voice-cassettes/README.md). In short: both TTS
and STT cassettes are recorded against the live provider under `-tags=record`.
An STT cassette holds an ordered list of segments — a VAD-segmented clip
records one transcript per utterance, matched positionally on replay.

### A new clip — `tests/voice-clips/`

A directory `<clip-name>/` containing:

- `audio.wav` — **mono, 16 kHz, 16-bit PCM** (`pcm_s16le`); other formats fail the loader.
- `meta.yaml` — provenance and intent only. It carries **no executable
  assertions** (ADR-0020); assertions live in the Go test. Copy the shape from
  [`hello-test/meta.yaml`](../../tests/voice-clips/hello-test/meta.yaml).

Name the clip to pair with its cassette suffix (`hello-test/` ↔
`stt-hello-test.yaml`).

#### Recording a clip yourself

Existing fixtures are synthesized (ElevenLabs), but you can record your own with
`ffmpeg`. Capture from the system default source straight into the target
format — mono, 16 kHz, `pcm_s16le` — then stop with `q`:

```sh
ffmpeg -f pulse -i default -ac 1 -ar 16000 -c:a pcm_s16le audio.wav
```

(`-f pulse -i default` works under both PulseAudio and PipeWire. Pick a specific
mic with `-i <source>`; list sources via `pactl list sources short`.)

To convert an existing recording instead of capturing live:

```sh
ffmpeg -i input.wav -ac 1 -ar 16000 -c:a pcm_s16le audio.wav
```

Verify before committing — the loader rejects anything that isn't mono/16 kHz/16-bit:

```sh
ffprobe -hide_banner audio.wav   # expect: pcm_s16le, 16000 Hz, 1 channels
```

Set `source.provider: self` in `meta.yaml` so a hand-recorded clip is
distinguishable from a synthesized one. Note that mic recordings carry room
noise and a softer onset than studio-clean fixtures, so they exercise the VAD
thresholds differently — useful for robustness, worth a sanity check.
