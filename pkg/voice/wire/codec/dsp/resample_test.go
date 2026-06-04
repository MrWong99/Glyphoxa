package dsp

import (
	"math"
	"testing"
)

func TestResampler_PassThrough(t *testing.T) {
	r := NewResampler(48000, 48000)
	in := []int16{1, 2, 3, 4, 5}
	out := r.Process(in)
	if len(out) != len(in) {
		t.Fatalf("pass-through len = %d, want %d", len(out), len(in))
	}
	for i := range in {
		if out[i] != in[i] {
			t.Fatalf("pass-through[%d] = %d, want %d", i, out[i], in[i])
		}
	}
}

func TestResampler_OutputLengthMatchesRatio(t *testing.T) {
	// 24k → 48k upsamples ~2×; 48k → 16k downsamples ~3×. Output length must
	// track input·outRate/inRate within one sample.
	cases := []struct{ in, out int }{
		{24000, 48000},
		{48000, 16000},
		{22050, 48000},
		{44100, 48000},
	}
	const n = 4800
	in := ramp(n)
	for _, c := range cases {
		r := NewResampler(c.in, c.out)
		got := len(r.Process(in))
		want := n * c.out / c.in
		if diff := got - want; diff < -1 || diff > 1 {
			t.Errorf("%d→%d: output len %d, want ~%d (±1)", c.in, c.out, got, want)
		}
	}
}

// TestResampler_ChunkingIsContinuous is the load-bearing test: a stream split
// into uneven chunks must resample identically to the whole buffer processed at
// once. This proves the fractional-position and prev-sample carry across calls
// (the click-at-boundary bug if it were reset per chunk).
func TestResampler_ChunkingIsContinuous(t *testing.T) {
	in := sine(9600, 48000, 440) // 200 ms @ 48k

	whole := NewResampler(48000, 16000).Process(in)

	chunked := NewResampler(48000, 16000)
	var got []int16
	// Uneven chunk sizes, including a tiny one, to stress phase carry.
	for _, sz := range []int{313, 1, 2048, 999, 0, 4096} {
		if sz > len(in) {
			sz = len(in)
		}
		got = append(got, chunked.Process(in[:sz])...)
		in = in[sz:]
	}
	got = append(got, chunked.Process(in)...)

	if len(got) != len(whole) {
		t.Fatalf("chunked len %d != whole len %d", len(got), len(whole))
	}
	for i := range whole {
		if d := int(got[i]) - int(whole[i]); d < -1 || d > 1 {
			t.Fatalf("chunked[%d]=%d != whole[%d]=%d (sample %d)", i, got[i], i, whole[i], i)
		}
	}
}

func TestResampler_PreservesToneFrequency(t *testing.T) {
	// A 440 Hz tone resampled 24k→48k should still read as ~440 Hz: assert the
	// zero-crossing count is preserved (±a couple, for edge effects).
	const freq = 440
	in := sine(4800, 24000, freq) // 200 ms
	out := NewResampler(24000, 48000).Process(in)

	inZC := zeroCrossings(in)
	outZC := zeroCrossings(out)
	if d := outZC - inZC; d < -2 || d > 2 {
		t.Fatalf("zero-crossings in=%d out=%d (tone frequency not preserved)", inZC, outZC)
	}
}

// --- helpers ---

func ramp(n int) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16((i % 200) - 100)
	}
	return s
}

func sine(n, rate, freq int) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(12000 * math.Sin(2*math.Pi*float64(freq)*float64(i)/float64(rate)))
	}
	return s
}

func zeroCrossings(s []int16) int {
	count := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			count++
		}
	}
	return count
}
