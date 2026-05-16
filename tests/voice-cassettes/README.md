# voice-cassettes

VCR-style cassettes for vendor calls in the voice pipeline, per [ADR-0021](../../docs/adr/0021-cassette-based-llm-determinism.md). Tests load a cassette by name, the cassette-backed provider asserts that the incoming request matches the recorded fingerprint, and replays the recorded response. A mismatch fails the test and points at the re-record workflow.

The Go side lives in [pkg/voice/voicecassette](../../pkg/voice/voicecassette/) — see its package doc for the loader and recognizer types.

## Layout

```
tests/voice-cassettes/
  <cassette-name>.yaml
```

One YAML file per cassette. The filename (without `.yaml`) is the cassette's identity — it's what `voicecassette.LoadSTT(t, "stt-hello-test")` reads, and it's what error messages name on mismatch. There is no `name` field inside the YAML; renaming a cassette means renaming the file.

Naming convention: `<stage>-<clip-or-scenario>.yaml`. STT cassettes are prefixed `stt-`, TTS cassettes `tts-`, and each typically pairs with a clip under [tests/voice-clips/](../voice-clips/) of the same suffix (e.g. `stt-hello-test.yaml` ↔ `voice-clips/hello-test/`).

## STT cassette schema

```yaml
audio_sha256: <hex>          # required — sha256 of the PCM sample stream
transcript: "<text>"         # required — pinned recognizer output
notes: |                     # optional — provenance for human reviewers
  Free-form: provider, model, recording date, hand-authored vs live, etc.
```

`audio_sha256` is computed by `voicecassette.HashFrames` over the little-endian int16 PCM stream the recognizer is fed, in frame order. The hash is stable across reframings of the same underlying samples (32 ms × 1 vs 16 ms × 2 at 16 kHz produce the same hex), so a test that changes only its framing does not need to re-record.

`transcript` is the authoritative text. Empty strings are allowed (see the empty-transcript contract on [orchestrator.STT.Transcribe](../../pkg/voice/orchestrator/stt.go)), but `audio_sha256` must always be present.

## TTS cassette schema

```yaml
sentences:                   # required — ordered list of dispatched sentences
  - "<sentence>"
notes: |                     # optional — provenance for human reviewers
  Free-form: provider, model, recording date, hand-authored vs live, etc.
```

Per [ADR-0021](../../docs/adr/0021-cassette-based-llm-determinism.md), TTS cassettes are **stub cassettes** — only the dispatch signal ("TTS was invoked with sentence N") is recorded, not the synthesized audio. `sentences` is matched positionally against incoming `Synthesize` calls: dispatch N must equal `sentences[N]`. A mismatch or running past the end fails the test and points at `-tags=record`.

The TTS cassette intentionally does not pin Voice, provider, or settings — the cassette is a contract on the **text** that flowed into the provider interface, which is what the orchestrator and Persona layer are responsible for producing.

## Workflow

**Replay (default).** `go test ./...` reads cassettes from this directory; the cassette is the authoritative expectation. Sub-second, free, deterministic.

**Re-record.** Per ADR-0021, `ELEVENLABS_API_KEY=… go test -tags=record ./pkg/voice/orchestrator/` re-records TTS cassettes against the live ElevenLabs `eleven_v3` adapter ([pkg/voice/tts/elevenlabs](../../pkg/voice/tts/elevenlabs/)). The recorder forwards each dispatched sentence to the live API, drains the rendered PCM (per the TTS-cassette-policy "sentences only" rule), and rewrites the `<name>.yaml` file at test cleanup with the captured ordered list — preserving the existing `notes` block and appending a "recorded against eleven_v3 on YYYY-MM-DD" provenance line. Run `go test ./...` afterwards to confirm the refreshed cassette replays clean. STT re-record still lands with its first real STT adapter; until then STT cassettes are hand-authored from the paired clip's `meta.yaml` script — fingerprint the clip with `voicecassette.HashFrames`, paste the transcript by hand, commit the result.

**Live (nightly).** `go test -tags=live` will hit real APIs against ~5–10 canonical cases to catch vendor drift between cassette refreshes. Forward-looking — lands once the live cron is wired.
