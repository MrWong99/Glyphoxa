package audio_test

import (
	"encoding/binary"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
)

func TestNewFrame_ValidatesSampleCount(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		samples    []int16
		sampleRate int
		frameMs    int
		wantErr    bool
	}{
		{"16kHz 32ms = 512 samples", make([]int16, 512), 16000, 32, false},
		{"8kHz 32ms = 256 samples", make([]int16, 256), 8000, 32, false},
		{"16kHz 32ms with 511 samples", make([]int16, 511), 16000, 32, true},
		{"16kHz 32ms with 513 samples", make([]int16, 513), 16000, 32, true},
		{"zero sample rate", make([]int16, 1), 0, 32, true},
		{"negative sample rate", make([]int16, 1), -1, 32, true},
		{"zero frame ms", make([]int16, 1), 16000, 0, true},
		{"negative frame ms", make([]int16, 1), 16000, -1, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := audio.NewFrame(tc.samples, tc.sampleRate, tc.frameMs)
			if tc.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestNewFrame_AccessorsRoundTrip(t *testing.T) {
	t.Parallel()

	samples := []int16{1, 2, 3, 4, 5, 6, 7, 8}
	f, err := audio.NewFrame(samples, 8000, 1)
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}
	if got := f.SampleRate(); got != 8000 {
		t.Errorf("SampleRate = %d, want 8000", got)
	}
	if got := f.FrameMs(); got != 1 {
		t.Errorf("FrameMs = %d, want 1", got)
	}
	if got := f.Samples(); len(got) != len(samples) {
		t.Errorf("Samples len = %d, want %d", len(got), len(samples))
	}
}

func TestFromPCM16LE_DecodesAndValidates(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		values   []int16
		wantErr  bool
		wantSize int
	}{
		{"512 samples for 16kHz 32ms", make([]int16, 512), false, 512},
		{"256 samples for 8kHz 32ms wrong rate", make([]int16, 256), true, 0},
		{"empty bytes", nil, true, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pcm := make([]byte, len(tc.values)*2)
			for i, v := range tc.values {
				binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
			}
			f, err := audio.FromPCM16LE(pcm, 16000, 32)
			if tc.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got := len(f.Samples()); got != tc.wantSize {
				t.Errorf("Samples len = %d, want %d", got, tc.wantSize)
			}
		})
	}
}

func TestFromPCM16LE_OddByteLength(t *testing.T) {
	t.Parallel()

	_, err := audio.FromPCM16LE([]byte{0x00, 0x01, 0x02}, 16000, 32)
	if err == nil {
		t.Fatal("expected error for odd PCM byte length, got nil")
	}
}

func TestFromPCM16LE_DecodesLittleEndianSignedSamples(t *testing.T) {
	t.Parallel()

	// 1 sample at 8000 Hz × 0.125 ms — but frameMs must be ≥ 1, so use a
	// minimal valid frame: 8 samples at 8000 Hz / 1 ms.
	values := []int16{0, 32767, -32768, 16384, -16384, 1, -1, 0}
	pcm := make([]byte, len(values)*2)
	for i, v := range values {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(v))
	}

	f, err := audio.FromPCM16LE(pcm, 8000, 1)
	if err != nil {
		t.Fatalf("FromPCM16LE: %v", err)
	}
	got := f.Samples()
	if len(got) != len(values) {
		t.Fatalf("len = %d, want %d", len(got), len(values))
	}
	for i, v := range got {
		if v != values[i] {
			t.Errorf("samples[%d] = %d, want %d", i, v, values[i])
		}
	}
}
