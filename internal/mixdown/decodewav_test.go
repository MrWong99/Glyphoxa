package mixdown

import (
	"encoding/binary"
	"errors"
	"testing"
)

// TestDecodeWAV_RoundTrip proves DecodeWAV is the inverse of encodeWAV: the
// concatenated chunk PCM equals the original samples, each chunk carries the
// clip's sample rate + mono, and the windows are ~100 ms (the last shorter).
func TestDecodeWAV_RoundTrip(t *testing.T) {
	const rate = 48000
	// 250 ms of a ramp so chunk boundaries are visible: 12000 samples.
	samples := make([]int16, rate/4)
	for i := range samples {
		samples[i] = int16(i % 1000)
	}
	wav := encodeWAV(samples, rate)

	chunks, err := DecodeWAV(wav)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 250 ms at 100 ms/chunk = 3 chunks (100, 100, 50 ms).
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}
	// First chunk is a full 100 ms = 4800 samples = 9600 bytes.
	if len(chunks[0].PCM) != rate/10*2 {
		t.Errorf("chunk[0] bytes = %d, want %d", len(chunks[0].PCM), rate/10*2)
	}
	var got []int16
	for i, c := range chunks {
		if c.SampleRate != rate {
			t.Errorf("chunk[%d] rate = %d, want %d", i, c.SampleRate, rate)
		}
		if c.Channels != 1 {
			t.Errorf("chunk[%d] channels = %d, want 1", i, c.Channels)
		}
		for off := 0; off+1 < len(c.PCM); off += 2 {
			got = append(got, int16(binary.LittleEndian.Uint16(c.PCM[off:])))
		}
	}
	if len(got) != len(samples) {
		t.Fatalf("round-trip sample count = %d, want %d", len(got), len(samples))
	}
	for i := range samples {
		if got[i] != samples[i] {
			t.Fatalf("sample %d = %d, want %d", i, got[i], samples[i])
		}
	}
}

// TestDecodeWAV_RejectsBadClips proves the strict inverse: a truncated header, a
// stereo clip, a non-16-bit clip, and a data-size overrun are all ErrNotPCM16WAV.
func TestDecodeWAV_RejectsBadClips(t *testing.T) {
	valid := encodeWAV([]int16{1, 2, 3, 4}, 48000)

	t.Run("truncated header", func(t *testing.T) {
		if _, err := DecodeWAV(valid[:20]); !errors.Is(err, ErrNotPCM16WAV) {
			t.Fatalf("err = %v, want ErrNotPCM16WAV", err)
		}
	})
	t.Run("stereo", func(t *testing.T) {
		w := append([]byte(nil), valid...)
		binary.LittleEndian.PutUint16(w[22:24], 2) // numChannels = 2
		if _, err := DecodeWAV(w); !errors.Is(err, ErrNotPCM16WAV) {
			t.Fatalf("err = %v, want ErrNotPCM16WAV", err)
		}
	})
	t.Run("not 16-bit", func(t *testing.T) {
		w := append([]byte(nil), valid...)
		binary.LittleEndian.PutUint16(w[34:36], 8) // bitsPerSample = 8
		if _, err := DecodeWAV(w); !errors.Is(err, ErrNotPCM16WAV) {
			t.Fatalf("err = %v, want ErrNotPCM16WAV", err)
		}
	})
	t.Run("data overrun", func(t *testing.T) {
		w := append([]byte(nil), valid...)
		binary.LittleEndian.PutUint32(w[40:44], 1<<20) // declares far more data than present
		if _, err := DecodeWAV(w); !errors.Is(err, ErrNotPCM16WAV) {
			t.Fatalf("err = %v, want ErrNotPCM16WAV", err)
		}
	})
}
