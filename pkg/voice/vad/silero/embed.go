package silero

import _ "embed"

// modelBytes is the Silero VAD v5 ONNX model embedded at build time. The model
// file lives at data/silero_vad.onnx and is fetched from upstream
// (snakers4/silero-vad). See data/README.md for source and license details.
//
//go:embed data/silero_vad.onnx
var modelBytes []byte
