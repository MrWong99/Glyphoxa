package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// oddChunkReader hands out at most chunk bytes per Read so read boundaries land
// mid-sample, exercising streamPCM's odd-byte carry-over.
type oddChunkReader struct {
	data  []byte
	chunk int
}

func (r *oddChunkReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := min(r.chunk, len(p), len(r.data))
	copy(p, r.data[:n])
	r.data = r.data[n:]
	return n, nil
}

func (r *oddChunkReader) Close() error { return nil }

// TestStreamPCM_OddSizedReadsReassembleExactly is the regression test for the
// sample-desync bug: streamPCM used to mask off the trailing byte of every odd
// read and discard it, shifting and corrupting all subsequent samples. With the
// carry-over fix, an even-length stream delivered in odd-sized reads must
// reassemble byte-for-byte.
func TestStreamPCM_OddSizedReadsReassembleExactly(t *testing.T) {
	t.Parallel()
	// 1000 distinct bytes (even length = a whole number of int16 samples).
	want := make([]byte, 1000)
	for i := range want {
		want[i] = byte(i % 251) // 251 is prime → no accidental period alignment
	}

	for _, chunk := range []int{1, 3, 5, 7, 4095} {
		src := make([]byte, len(want))
		copy(src, want)
		r := &oddChunkReader{data: src, chunk: chunk}
		ch := make(chan tts.AudioChunk)
		go streamPCM(context.Background(), r, ch, 24000)

		var got []byte
		for c := range ch {
			if len(c.PCM)%2 != 0 {
				t.Errorf("chunk=%d: emitted an odd-length PCM chunk (%d bytes)", chunk, len(c.PCM))
			}
			got = append(got, c.PCM...)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("chunk=%d: reassembled stream differs from source (%d of %d bytes)",
				chunk, len(got), len(want))
		}
	}
}

// TestStreamPCM_TrailingOddByteDropped pins the documented edge: a stream with
// an odd total length (a truncated final sample) emits all whole samples and
// drops only the single dangling byte — never more.
func TestStreamPCM_TrailingOddByteDropped(t *testing.T) {
	t.Parallel()
	want := []byte{1, 2, 3, 4, 5} // 5 bytes: two whole samples + one dangling
	r := &oddChunkReader{data: append([]byte(nil), want...), chunk: 2}
	ch := make(chan tts.AudioChunk)
	go streamPCM(context.Background(), r, ch, 24000)

	var got []byte
	for c := range ch {
		got = append(got, c.PCM...)
	}
	if !bytes.Equal(got, want[:4]) {
		t.Errorf("reassembled %v, want %v (final dangling byte dropped)", got, want[:4])
	}
}

func TestSampleRateFromOutputFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want int
	}{
		{"pcm_16000", 16000},
		{"pcm_22050", 22050},
		{"pcm_24000", 24000},
		{"pcm_44100", 44100},
		{"pcm_48000", 48000},
		{"mp3_44100_128", 0}, // non-PCM rejected
		{"opus_48000", 0},    // non-PCM rejected
		{"", 0},
		{"pcm_notanumber", 0},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := sampleRateFromOutputFormat(tc.in); got != tc.want {
				t.Errorf("sampleRateFromOutputFormat(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestMergeSettings_OverridePrecedence(t *testing.T) {
	t.Parallel()

	stability := 0.5
	base := Settings{
		ModelID:      ModelV3,
		OutputFormat: "pcm_24000",
		VoiceSettings: &VoiceSettings{
			Stability: &stability,
		},
		LanguageCode: "en",
	}
	baseJSON, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal base: %v", err)
	}

	// Override: change output format and language code; do NOT touch VoiceSettings.
	overrideJSON := []byte(`{"output_format":"pcm_44100","language_code":"de"}`)

	merged, err := mergeSettings(baseJSON, overrideJSON)
	if err != nil {
		t.Fatalf("mergeSettings: %v", err)
	}

	if merged.ModelID != ModelV3 {
		t.Errorf("ModelID = %q, want %q (preserved from base)", merged.ModelID, ModelV3)
	}
	if merged.OutputFormat != "pcm_44100" {
		t.Errorf("OutputFormat = %q, want %q (from override)", merged.OutputFormat, "pcm_44100")
	}
	if merged.LanguageCode != "de" {
		t.Errorf("LanguageCode = %q, want %q (from override)", merged.LanguageCode, "de")
	}
	if merged.VoiceSettings == nil || merged.VoiceSettings.Stability == nil || *merged.VoiceSettings.Stability != stability {
		t.Errorf("VoiceSettings did not survive merge: %+v", merged.VoiceSettings)
	}
}

func TestMergeSettings_NilInputs(t *testing.T) {
	t.Parallel()
	got, err := mergeSettings(nil, nil)
	if err != nil {
		t.Fatalf("mergeSettings(nil,nil): %v", err)
	}
	if got.ModelID != "" || got.OutputFormat != "" || got.LanguageCode != "" ||
		got.VoiceSettings != nil || got.Seed != nil ||
		len(got.PronunciationDictionaryLocators) != 0 || len(got.SuggestedAudioTags) != 0 {
		t.Errorf("mergeSettings(nil,nil) = %+v, want zero value", got)
	}
}

func TestMergeSettings_OverrideOnly(t *testing.T) {
	t.Parallel()
	override := []byte(`{"model_id":"eleven_v3","output_format":"pcm_24000"}`)
	got, err := mergeSettings(nil, override)
	if err != nil {
		t.Fatalf("mergeSettings: %v", err)
	}
	if got.ModelID != ModelV3 {
		t.Errorf("ModelID = %q, want %q", got.ModelID, ModelV3)
	}
	if got.OutputFormat != "pcm_24000" {
		t.Errorf("OutputFormat = %q, want %q", got.OutputFormat, "pcm_24000")
	}
}

// TestMergeSettings_NestedRecursiveMerge confirms the documented recursive
// merge semantic: an override that touches only one field of voice_settings
// updates that field and preserves the rest of voice_settings from base.
func TestMergeSettings_NestedRecursiveMerge(t *testing.T) {
	t.Parallel()
	stability := 0.5
	similarity := 0.75
	base := Settings{VoiceSettings: &VoiceSettings{Stability: &stability, SimilarityBoost: &similarity}}
	baseJSON, _ := json.Marshal(base)

	overrideJSON := []byte(`{"voice_settings":{"stability":0.9}}`)
	got, err := mergeSettings(baseJSON, overrideJSON)
	if err != nil {
		t.Fatalf("mergeSettings: %v", err)
	}
	if got.VoiceSettings == nil || got.VoiceSettings.Stability == nil || *got.VoiceSettings.Stability != 0.9 {
		t.Errorf("Stability not overridden: %+v", got.VoiceSettings)
	}
	if got.VoiceSettings == nil || got.VoiceSettings.SimilarityBoost == nil || *got.VoiceSettings.SimilarityBoost != similarity {
		t.Errorf("SimilarityBoost not preserved: %+v", got.VoiceSettings)
	}
}
