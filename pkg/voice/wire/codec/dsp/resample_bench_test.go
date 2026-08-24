package dsp

import (
	"math"
	"testing"
)

// benchChunk synthesizes n mono samples of a 440Hz tone at rate Hz — loud
// enough that interpolation does real arithmetic on every sample.
func benchChunk(n, rate int) []int16 {
	in := make([]int16, n)
	for i := range in {
		in[i] = int16(20000 * math.Sin(2*math.Pi*440*float64(i)/float64(rate)))
	}
	return in
}

// providerChunkSamples is the resampler's real per-call input size on the live
// playback path: the ElevenLabs synthesizer emits one tts.AudioChunk per 4KiB
// body read (streamReadBuffer in pkg/voice/tts/elevenlabs/synthesize.go), and
// playbackSource.ingest resamples each chunk whole BEFORE the reframer cuts the
// output into 20ms Opus frames — 4096 bytes of mono int16 PCM = 2048 samples
// (~85ms at 24kHz, ~a dozen Process calls per second while an Agent speaks).
const providerChunkSamples = 2048

// BenchmarkResamplerProcess24kTo48k is the live playback path's shape: the
// orchestrator owns resampling to Discord's 48kHz mono Opus pipeline
// (ADR-0022), the live default provider emits 24kHz PCM (ElevenLabs
// pcm_24000), and state carries across calls exactly like
// playbackSource.ingest drives it. Watch allocs/op: Process allocates its
// output slice (4KiB in, 8KiB out) on every provider chunk.
func BenchmarkResamplerProcess24kTo48k(b *testing.B) {
	in := benchChunk(providerChunkSamples, 24000)
	r := NewResampler(24000, 48000)
	b.ReportAllocs()
	for b.Loop() {
		r.Process(in)
	}
}

// BenchmarkResamplerProcessPassthrough pins the equal-rate path (a provider
// already emitting 48kHz PCM) so the cost of "no resampling needed" — today a
// full copy per chunk — is a number, not an assumption.
func BenchmarkResamplerProcessPassthrough(b *testing.B) {
	in := benchChunk(providerChunkSamples, 48000)
	r := NewResampler(48000, 48000)
	b.ReportAllocs()
	for b.Loop() {
		r.Process(in)
	}
}
