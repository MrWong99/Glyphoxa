package silero

import _ "embed"

// modelBytes is the Silero VAD v5 "op18 ifless" ONNX model embedded at build
// time, byte-identical to the upstream export (snakers4/silero-vad). This
// export decomposes the LSTM into primitive ops and keeps a single top-level
// If (8 kHz vs 16 kHz branch), which the pure-Go loader resolves at session
// creation. See data/README.md for source, SHA-256, and license details.
//
//go:embed data/silero_vad_op18_ifless.onnx
var modelBytes []byte
