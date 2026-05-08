# Silero VAD v5 ONNX Model

`silero_vad.onnx` is the Silero VAD v5 model, fetched from upstream and embedded
into the binary at build time via `go:embed` (see `../embed.go`).

| Field | Value |
|---|---|
| Source | https://github.com/snakers4/silero-vad/raw/master/src/silero_vad/data/silero_vad.onnx |
| License | MIT (https://github.com/snakers4/silero-vad/blob/master/LICENSE) |
| SHA-256 | `1a153a22f4509e292a94e67d6f9b85e8deb25b4988682b7e174c65279d8788e3` |
| Size | 2,327,524 bytes |
| Fetched | 2026-05-07 |

## Refreshing

```
make refresh-silero-model
```

After refresh, update the SHA-256 above and commit the new `silero_vad.onnx`.
