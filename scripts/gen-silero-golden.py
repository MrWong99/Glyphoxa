#!/usr/bin/env python3
"""Regenerate the Silero VAD golden equivalence data (#468).

Produces pkg/voice/vad/silero/testdata/golden/*.json: per-frame speech
probabilities for the tests/voice-clips corpus, computed with ONNX Runtime as
the reference engine. The Go test silero_golden_test.go replays the same
frames through the pure-Go forward pass and asserts |Δ| < 1e-4 per frame.

Two reference models are used:

- 16 kHz goldens come from the PREVIOUS production model (the opset-16
  silero_vad.onnx that shipped before #468). This makes the golden test a
  combined gate: new model vs old model AND pure-Go engine vs ONNX Runtime.
- 8 kHz goldens come from the CURRENT embedded model (silero_vad_op18_ifless).
  The ifless export carries dedicated 8 kHz weights that intentionally differ
  from the old model's 8 kHz path, so only engine equivalence is gated there.

Usage:
    pip install onnxruntime numpy
    # Fetch the old reference model (not in the repo anymore):
    pip download --no-deps silero-vad -d /tmp/silero-pkg
    unzip -o /tmp/silero-pkg/silero_vad-*.whl -d /tmp/silero-pkg/x
    python3 scripts/gen-silero-golden.py \
        --old-model /tmp/silero-pkg/x/silero_vad/data/silero_vad.onnx

Expected SHA-256 of the old reference model:
    1a153a22f4509e292a94e67d6f9b85e8deb25b4988682b7e174c65279d8788e3
"""

import argparse
import hashlib
import json
import pathlib
import sys
import wave

import numpy as np
import onnxruntime as ort

REPO = pathlib.Path(__file__).resolve().parent.parent
NEW_MODEL = REPO / "pkg/voice/vad/silero/data/silero_vad_op18_ifless.onnx"
OUT_DIR = REPO / "pkg/voice/vad/silero/testdata/golden"
CLIP_DIR = REPO / "tests/voice-clips"

OLD_MODEL_SHA256 = "1a153a22f4509e292a94e67d6f9b85e8deb25b4988682b7e174c65279d8788e3"

CLIPS_16K = [
    "bart-test",
    "hello-test",
    "silence-test",
    "ttrpg-intro-de",
    "ttrpg-intro-en",
    "two-utterance-test",
]
# 8 kHz coverage: one speech clip and the silence clip, decimated 2:1. The
# decimation must match sileroGoldenPCM8k in silero_golden_test.go.
CLIPS_8K = ["two-utterance-test", "silence-test"]


def load_pcm(name: str) -> np.ndarray:
    with wave.open(str(CLIP_DIR / name / "audio.wav")) as w:
        assert w.getframerate() == 16000 and w.getnchannels() == 1 and w.getsampwidth() == 2, name
        return np.frombuffer(w.readframes(w.getnframes()), dtype=np.int16)


def run_frames(sess, samples: np.ndarray, sr_hz: int, chunk: int, scalar_sr: bool) -> list[float]:
    """Replay the exact frame loop the Go engine uses: prepend the previous
    frame's trailing context, feed the LSTM state back, scale int16 by 1/32768."""
    ctx = 64 if sr_hz == 16000 else 32
    state = np.zeros((2, 1, 128), dtype=np.float32)
    context = np.zeros(ctx, dtype=np.float32)
    probs = []
    for i in range(len(samples) // chunk):
        chunk_f = samples[i * chunk : (i + 1) * chunk].astype(np.float32) / 32768.0
        inp = np.concatenate([context, chunk_f]).reshape(1, -1).astype(np.float32)
        sr = np.array(sr_hz, dtype=np.int64) if scalar_sr else np.array([sr_hz], dtype=np.int64)
        out, state = sess.run(["output", "stateN"], {"input": inp, "state": state, "sr": sr})
        context = inp[0, -ctx:]
        probs.append(round(float(out[0, 0]), 7))
    return probs


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--old-model", required=True, help="path to the pre-#468 silero_vad.onnx")
    args = ap.parse_args()

    old_path = pathlib.Path(args.old_model)
    got = hashlib.sha256(old_path.read_bytes()).hexdigest()
    if got != OLD_MODEL_SHA256:
        print(f"old model SHA-256 mismatch:\n  got  {got}\n  want {OLD_MODEL_SHA256}", file=sys.stderr)
        return 1

    sess_old = ort.InferenceSession(str(old_path), providers=["CPUExecutionProvider"])
    sess_new = ort.InferenceSession(str(NEW_MODEL), providers=["CPUExecutionProvider"])
    OUT_DIR.mkdir(parents=True, exist_ok=True)

    for name in CLIPS_16K:
        pcm = load_pcm(name)
        probs = run_frames(sess_old, pcm, 16000, 512, scalar_sr=False)
        out = OUT_DIR / f"{name}-16k.json"
        out.write_text(json.dumps({
            "clip": name,
            "sample_rate": 16000,
            "chunk_samples": 512,
            "reference": f"onnxruntime-{ort.__version__} silero_vad.onnx@{OLD_MODEL_SHA256[:12]} (pre-#468 production model)",
            "probs": probs,
        }, indent=None, separators=(",", ":")) + "\n")
        print(f"{out.name}: {len(probs)} frames")

    new_sha = hashlib.sha256(NEW_MODEL.read_bytes()).hexdigest()
    for name in CLIPS_8K:
        pcm = load_pcm(name)[::2]  # decimate to 8 kHz; matches the Go test
        probs = run_frames(sess_new, pcm, 8000, 256, scalar_sr=True)
        out = OUT_DIR / f"{name}-8k.json"
        out.write_text(json.dumps({
            "clip": name,
            "sample_rate": 8000,
            "chunk_samples": 256,
            "reference": f"onnxruntime-{ort.__version__} silero_vad_op18_ifless.onnx@{new_sha[:12]} (embedded model, 8k branch)",
            "probs": probs,
        }, indent=None, separators=(",", ":")) + "\n")
        print(f"{out.name}: {len(probs)} frames")

    return 0


if __name__ == "__main__":
    sys.exit(main())
