# Silero VAD v5 ONNX Model (op18 "ifless" export)

`silero_vad_op18_ifless.onnx` is the Silero VAD v5 model in upstream's opset-18
"ifless" export, fetched from upstream **byte-identical** (no local surgery)
and embedded into the binary at build time via `go:embed` (see `../embed.go`).

This export exists specifically to be friendly to non-ONNX-Runtime engines
(#468): the LSTM is decomposed into Gemm/Sigmoid/Tanh/Split/Mul primitives,
all dynamic-shape ops are gone, and the only control flow is a single
top-level `If` selecting the 8 kHz vs 16 kHz network. The bespoke pure-Go
forward pass (`../graph.go`) resolves that `If` at session creation (the
sample rate is fixed per session) and compiles the selected branch into a
static execution plan — ONNX Runtime is not used at all.

| Field | Value |
|---|---|
| Source | https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad_op18_ifless.onnx |
| License | MIT (https://github.com/snakers4/silero-vad/blob/master/LICENSE) |
| SHA-256 | `7671cd04b004e9076da0d4a7b1a5aec36adf161c39230c1cb94a4fd5db6bbd28` |
| Size | 2,845,718 bytes |
| Fetched | 2026-07-16 |

The same file also ships inside the `silero-vad` PyPI package
(`silero_vad/data/silero_vad_op18_ifless.onnx`), which is a convenient
SHA-verifiable second source.

## Refreshing

```
make refresh-silero-model
```

After refresh:

1. Update the SHA-256 and size above and commit the new
   `silero_vad_op18_ifless.onnx`.
2. Regenerate the golden equivalence data
   (`scripts/gen-silero-golden.py`) — the golden test
   (`../silero_golden_test.go`) is the acceptance gate for any model or
   engine change (#468).
3. Run `go test ./pkg/voice/vad/silero/`. If upstream changed the graph's op
   inventory or layout, `compileProgram` fails loudly at load time — extend
   `../graph.go` deliberately rather than loosening its validation.

## Numerical provenance (#468)

The 16 kHz path of this export is numerically equivalent to the previously
embedded opset-16 `silero_vad.onnx`
(`1a153a22f4509e292a94e67d6f9b85e8deb25b4988682b7e174c65279d8788e3`): max
per-frame speech-probability delta ≈ 1e-6 across the tests/voice-clips corpus
under ONNX Runtime, three orders of magnitude inside the 1e-4 gate. The 8 kHz
path carries upstream's dedicated 8 kHz weights and intentionally differs from
the old model's 8 kHz behavior (Glyphoxa's pipeline is 16 kHz-only; 8 kHz
goldens gate engine equivalence against this model itself).
