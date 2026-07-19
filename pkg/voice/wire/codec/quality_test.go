//go:build opus

package codec

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"testing"

	hraban "github.com/hraban/opus"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec/dsp"
)

// minSpeechSNR is the aligned-SNR floor for the playback encode path on real
// speech. Measured on the hello-test clip (decoded by reference libopus):
// libopus at its VoIP defaults scores ~6.0 dB; pion/opus's v0.1 encoder
// plateaus at ~4.1 dB regardless of bitrate — the audibly "metallic" regression
// this gate pins. The 5.5 dB floor sits between the two so a future encoder
// swap cannot silently regress speech quality again.
const minSpeechSNR = 5.5

// helloClip is the repo's canonical ElevenLabs speech fixture (16 kHz mono
// s16le, ~3.3 s — see its meta.yaml), relative to this package.
const helloClip = "../../../../tests/voice-clips/hello-test/audio.wav"

// TestPlaybackSource_SpeechQualityGate is the encoder quality gate: it runs the
// speech clip through the real playback path (mono-mix, resample to 48 kHz,
// reframe, Opus-encode), decodes the packets with the reference libopus
// decoder, and asserts the aligned SNR against the exact 48 kHz signal the
// encoder was fed. Tones round-trip fine through almost any encoder
// (TestPlaybackSource_RoundTrip); only real speech exposes the difference that
// matters for "the NPC sounds right".
func TestPlaybackSource_SpeechQualityGate(t *testing.T) {
	pcm, rate := readWAV(t, helloClip)
	if rate != vadSampleRate {
		t.Fatalf("clip sample rate = %d Hz, want %d (fixture drifted from its meta.yaml)", rate, vadSampleRate)
	}

	// Reference: the same upsample the playback source performs internally, so
	// the SNR measures exactly the encode→decode loss and nothing else.
	ref := dsp.NewResampler(rate, discordSampleRate).Process(pcm)

	chunks := make(chan tts.AudioChunk, 1)
	chunks <- tts.AudioChunk{PCM: pcmBytes(pcm), SampleRate: rate, Channels: 1}
	close(chunks)

	src, err := New().PlaybackSource(chunks)
	if err != nil {
		t.Fatalf("PlaybackSource: %v", err)
	}
	dec, err := hraban.NewDecoder(discordSampleRate, 1)
	if err != nil {
		t.Fatalf("reference libopus decoder: %v", err)
	}

	var decoded []int16
	buf := make([]int16, opusFrameSamples)
	for {
		frame, err := src.NextFrame(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		n, err := dec.Decode(frame, buf)
		if err != nil {
			t.Fatalf("reference decode: %v", err)
		}
		decoded = append(decoded, buf[:n]...)
	}

	snr, lag := alignedSNR(ref, decoded, 2*opusFrameSamples)
	t.Logf("aligned SNR %.2f dB at lag %d samples", snr, lag)
	if snr < minSpeechSNR {
		t.Fatalf("playback encode speech quality = %.2f dB aligned SNR, want >= %.1f dB — the outbound Opus encoder regressed (metallic voice)", snr, minSpeechSNR)
	}
}

// alignedSNR searches lags 0..maxLag for the best alignment of dec against ref
// (Opus adds a codec delay / preskip, so a fixed offset is meaningless) and
// returns the best SNR in dB with its lag. Requires at least one second of
// overlap at 48 kHz so a degenerate alignment cannot win on a sliver.
func alignedSNR(ref, dec []int16, maxLag int) (float64, int) {
	const minOverlap = discordSampleRate // 1 s
	bestSNR, bestLag := math.Inf(-1), 0
	for lag := 0; lag <= maxLag; lag++ {
		n := min(len(ref), len(dec)-lag)
		if n < minOverlap {
			continue
		}
		var sig, noise float64
		for i := range n {
			s := float64(ref[i])
			d := float64(dec[i+lag])
			sig += s * s
			noise += (s - d) * (s - d)
		}
		if noise == 0 {
			continue
		}
		if snr := 10 * math.Log10(sig/noise); snr > bestSNR {
			bestSNR, bestLag = snr, lag
		}
	}
	return bestSNR, bestLag
}

// readWAV loads a PCM WAV fixture, returning its mono s16le samples and sample
// rate. It walks RIFF chunks (the fixture carries a LIST/INFO chunk before
// data) and rejects anything but 16-bit mono PCM, so a swapped fixture fails
// loudly instead of skewing the SNR.
func readWAV(t *testing.T, path string) ([]int16, int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read clip: %v", err)
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		t.Fatalf("%s: not a RIFF/WAVE file", path)
	}
	var rate int
	var pcm []int16
	for i := 12; i+8 <= len(b); {
		id := string(b[i : i+4])
		sz := int(binary.LittleEndian.Uint32(b[i+4 : i+8]))
		if i+8+sz > len(b) {
			t.Fatalf("%s: truncated %q chunk", path, id)
		}
		body := b[i+8 : i+8+sz]
		switch id {
		case "fmt ":
			if err := checkFmt(body); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			rate = int(binary.LittleEndian.Uint32(body[4:8]))
		case "data":
			pcm = make([]int16, len(body)/2)
			for j := range pcm {
				pcm[j] = int16(binary.LittleEndian.Uint16(body[2*j:]))
			}
		}
		i += 8 + sz + sz&1 // chunks are word-aligned
	}
	if rate == 0 || pcm == nil {
		t.Fatalf("%s: missing fmt or data chunk", path)
	}
	return pcm, rate
}

// checkFmt asserts a WAV fmt chunk describes 16-bit mono PCM.
func checkFmt(body []byte) error {
	if len(body) < 16 {
		return errors.New("short fmt chunk")
	}
	format := binary.LittleEndian.Uint16(body[0:2])
	channels := binary.LittleEndian.Uint16(body[2:4])
	bits := binary.LittleEndian.Uint16(body[14:16])
	if format != 1 || channels != 1 || bits != 16 {
		return fmt.Errorf("format=%d channels=%d bits=%d, want 16-bit mono PCM", format, channels, bits)
	}
	return nil
}
