# voice-cassettes

VCR-style cassettes for vendor calls in the voice pipeline, per [ADR-0021](../../docs/adr/0021-cassette-based-llm-determinism.md). Tests load a cassette by name, the cassette-backed provider asserts that the incoming request matches the recorded fingerprint, and replays the recorded response. A mismatch fails the test and points at the re-record workflow.

The Go side lives in [pkg/voice/voicecassette](../../pkg/voice/voicecassette/) — see its package doc for the loader and recognizer types.

## Layout

```
tests/voice-cassettes/
  <cassette-name>.yaml
```

One YAML file per cassette. The filename (without `.yaml`) is the cassette's identity — it's what `voicecassette.LoadSTT(t, "stt-hello-test")` reads, and it's what error messages name on mismatch. There is no `name` field inside the YAML; renaming a cassette means renaming the file.

Naming convention: `<stage>-<clip-or-scenario>.yaml`. STT cassettes are prefixed `stt-` and typically pair with a clip under [tests/voice-clips/](../voice-clips/) of the same suffix (e.g. `stt-hello-test.yaml` ↔ `voice-clips/hello-test/`).

## STT cassette schema

```yaml
audio_sha256: <hex>          # required — sha256 of the PCM sample stream
transcript: "<text>"         # required — pinned recognizer output
notes: |                     # optional — provenance for human reviewers
  Free-form: provider, model, recording date, hand-authored vs live, etc.
```

`audio_sha256` is computed by `voicecassette.HashFrames` over the little-endian int16 PCM stream the recognizer is fed, in frame order. The hash is stable across reframings of the same underlying samples (32 ms × 1 vs 16 ms × 2 at 16 kHz produce the same hex), so a test that changes only its framing does not need to re-record.

`transcript` is the authoritative text. Empty strings are allowed (see the empty-transcript contract on [orchestrator.STT.Transcribe](../../pkg/voice/orchestrator/stt.go)), but `audio_sha256` must always be present.

## Workflow

**Replay (default).** `go test ./...` reads cassettes from this directory; the cassette is the authoritative expectation. Sub-second, free, deterministic.

**Re-record (forward-looking).** Per ADR-0021, `go test -tags=record` re-records against live providers. The recognizer's mismatch error already points at `-tags=record`, but the record path itself lands with the first real provider adapter. Until then cassettes are hand-authored from the paired clip's `meta.yaml` script — fingerprint the clip with `voicecassette.HashFrames`, paste the transcript by hand, commit the result.

**Live (nightly).** `go test -tags=live` will hit real APIs against ~5–10 canonical cases to catch vendor drift between cassette refreshes. Same forward-looking status as the record path.
