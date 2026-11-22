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
conv, err := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
    orchestrator.WithDetector(detector),
    orchestrator.WithReply(orchestrator.ReplyStrategy{
        Whole: func(ctx context.Context, e voiceevent.AddressRouted) []orchestrator.Reply { /* … */ },
    }),
    orchestrator.WithErrorHandler(func(err error) { t.Errorf("reply dispatch: %v", err) }),
)
if err != nil { t.Fatalf("NewConversation: %v", err) } // option interactions validate here (#453)
t.Cleanup(conv.Register(t.Context()))
for _, f := range frames { conv.Feed(f) }
```

To exercise one interaction in isolation, drop to the `Reactor` layer and
compose with `orchestrator.Bind(ctx, bus, reactors…)`; for an ad-hoc typed
subscription use `voiceevent.On[E](bus, fn)` directly.

## Test against real Discord (the live probe)

Everything above stops at the orchestrator. The stretch beyond it — Discord's
voice gateway, DAVE/MLS, the UDP transport, playback as a Player actually hears
it — is covered by [`pkg/voice/voiceprobe`](../../pkg/voice/voiceprobe/): a
second Discord bot that joins the voice channel as a synthetic Player, speaks a
clip, and records what the Bot says back.

```go
p, err := voiceprobe.Join(ctx, voiceprobe.Config{
    Token: probeToken, Guild: guild, Channel: channel,
    ReplyFrom: botUserID, // only the Bot counts as a reply, not a human in the channel
})
defer p.Close()

reply, err := p.Say(ctx, clip) // speaks, then waits for the reply to finish
if !reply.Heard { t.Fatal("no reply") }
// reply.Latency — end of clip to first reply audio
// reply.PCM     — the reply, 48 kHz mono; feed it to an STT to assert content
```

It is a **live tier**: never on a PR gate, because it needs things CI does not
have.

- **A probe bot.** A separate Discord application from the Bot under test,
  invited to the guild with Connect + Speak. Its token goes in
  `GLYPHOXA_PROBE_BOT_TOKEN`.
- **A running Voice Session** in the channel. Start it first: alone in a
  channel the probe cannot speak, because Discord completes the DAVE group only
  once a second member is present and the sender waits for it.
- **A clip that addresses someone.** The Bot answers when an NPC or the Butler
  is called by name, so `hello-test` ("Glyphoxa, …") only works where the Butler
  is listening. Any 16-bit PCM WAV is accepted.
- **The build tags** `opus dave nolibopusfile` (libopus encoder, real DAVE).

Run the packaged live test against a session you started:

```bash
GLYPHOXA_PROBE_BOT_TOKEN=… GX_PROBE_GUILD=… GX_PROBE_CHANNEL=… \
GX_PROBE_REPLY_FROM=<Bot user ID> GX_PROBE_CLIP=path/to/addressed.wav \
go test -tags "opus dave nolibopusfile live" -run TestLive -v ./pkg/voice/voiceprobe/
```

Or use the command, which suits scripts and soak runs — it exits 2 when an
expectation is not met and always writes `summary.json` plus the recordings:

```bash
CGO_ENABLED=1 go build -tags "opus dave nolibopusfile" -o bin/voiceprobe ./cmd/voiceprobe
bin/voiceprobe -guild G -channel C -reply-from BOT \
    -require-reply -max-latency 4s -out /tmp/probe clip1.wav clip2.wav
```

To test **barge-in**, pass clips in pairs with `-barge-after`: the first clip is
spoken, and once its reply has run that long the second is spoken over it. The
summary reports how long the Bot kept talking after the interruption began
(`talkover_seconds`; about half a second on production), and `-max-talkover`
turns that into an exit-2 expectation. Use a first clip that draws a long answer.

```bash
bin/voiceprobe -guild G -channel C -reply-from BOT -barge-after 3s -max-talkover 1500ms \
    -out /tmp/probe tell-a-long-story.wav stop-i-have-a-question.wav
```

In Go the same thing is `p.Interrupt(ctx, clip, over, after)`.

### Live checklist: names and their homophones

The recognizer is biased towards the session's names with keyterms (#701,
ADR-0042 keyterm amendment). A keyterm also pulls its homophones towards its
spelling, and no test without the real provider can show that. When a change
touches the term list, the STT adapter's request, or Address Detection's
handling of short names, run these against a live session whose Cast has an NPC
called **Flo**, and read the transcript on the Session screen:

- [ ] **Positives route.** "Flo?", "Flo, stopp.", "Flo, woran erinnerst du
      dich?" are transcribed with "Flo" and answered by Flo.
- [ ] **Homophone negatives — record what happens, do not expect a pass.**
      "Der Floh springt über den Tisch.", "Der Flow des Flusses ist stark." and
      a lone "Flow." were transcribed with "Flo" in the 2026-11-22 probe, and an
      exact name routes. Note whether each one routed. If a new one appears, or
      one starts routing mid-sentence where it did not, that is a regression in
      the homophone residue and belongs in the PR description.
- [ ] **Unrelated negatives stay clean.** "So, woran erinnerst du dich?", "Wo
      ist der Ausgang?", "Froh bin ich, dass wir endlich hier sind." must not
      gain a "Flo" and must not route.
- [ ] **An everyday-word name is not biased.** With an NPC called "Wolf" in a
      German campaign, the session-start log's `stt keyterms` count does not
      include it, and "Der Wolf heult." is transcribed as spoken.
- [ ] **Off-switch.** With `GLYPHOXA_STT_KEYTERMS=0` the log line is absent and
      "Der Floh springt über den Tisch." keeps its "Floh".

The transcripts alone, without Discord, come from
`TestLive_Keyterms_BothTransports` in `pkg/voice/stt/elevenlabs` (`-tags=live`,
both STT transports, with and without the list).

What runs on every PR instead: the probe's reply detection and recording
timeline (untagged tests, synthetic frames) and, in the `audio` job, a real
clip through encode → decode.

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
