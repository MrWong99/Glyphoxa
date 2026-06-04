//go:build opus

package codec

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"testing"

	"github.com/disgoorg/snowflake/v2"
	"github.com/hraban/opus"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// encodeOpus encodes mono int16 PCM at rate into 20 ms Opus packets, the shape
// Discord delivers. Used to synthesize inbound frames for DecodeInbound.
func encodeOpus(t *testing.T, pcm []int16, rate int) [][]byte {
	t.Helper()
	enc, err := opus.NewEncoder(rate, 1, opus.AppVoIP)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	frame := rate * 20 / 1000
	var packets [][]byte
	for off := 0; off+frame <= len(pcm); off += frame {
		buf := make([]byte, maxEncodedBytes)
		n, err := enc.Encode(pcm[off:off+frame], buf)
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		packets = append(packets, buf[:n])
	}
	return packets
}

func sine(n, rate, freq int) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = int16(10000 * math.Sin(2*math.Pi*float64(freq)*float64(i)/float64(rate)))
	}
	return s
}

func pcmBytes(samples []int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

func TestDecodeInbound_EmitsVadFrames(t *testing.T) {
	// One second of 48k tone → encode to 20 ms Opus packets → decode inbound.
	// Output must be 16 kHz / 512-sample audio.Frames, count ≈ 16000/512 ≈ 31.
	pcm48 := sine(48000, 48000, 330)
	packets := encodeOpus(t, pcm48, 48000)

	c := New()
	user := snowflake.ID(7)
	var frames int
	for _, pkt := range packets {
		out, err := c.DecodeInbound(gxvoice.Frame{UserID: user, Opus: pkt})
		if err != nil {
			t.Fatalf("DecodeInbound: %v", err)
		}
		for _, f := range out {
			if f.SampleRate() != vadSampleRate || len(f.Samples()) != vadFrameSamples {
				t.Fatalf("frame rate=%d len=%d, want %d/%d", f.SampleRate(), len(f.Samples()), vadSampleRate, vadFrameSamples)
			}
			frames++
		}
	}
	// 1s of 16k audio = 16000 samples / 512 ≈ 31 frames (tail < 512 buffered).
	if frames < 28 || frames > 32 {
		t.Fatalf("emitted %d frames, want ~31", frames)
	}
}

func TestDecodeInbound_EmptyPayloadNoFrames(t *testing.T) {
	c := New()
	out, err := c.DecodeInbound(gxvoice.Frame{UserID: 1, Opus: nil})
	if err != nil || out != nil {
		t.Fatalf("empty payload: got (%v, %v), want (nil, nil)", out, err)
	}
}

// TestDecodeInbound_PerUserDecoderIsolation feeds two speakers interleaved; each
// must decode through its own stateful decoder (a shared decoder would corrupt
// the streams). We assert both produce valid frames and the streams are tracked
// separately.
func TestDecodeInbound_PerUserDecoderIsolation(t *testing.T) {
	packetsA := encodeOpus(t, sine(48000, 48000, 220), 48000)
	packetsB := encodeOpus(t, sine(48000, 48000, 880), 48000)
	c := New()
	alice, bob := snowflake.ID(1), snowflake.ID(2)

	for i := range packetsA {
		if _, err := c.DecodeInbound(gxvoice.Frame{UserID: alice, Opus: packetsA[i]}); err != nil {
			t.Fatalf("alice frame %d: %v", i, err)
		}
		if _, err := c.DecodeInbound(gxvoice.Frame{UserID: bob, Opus: packetsB[i]}); err != nil {
			t.Fatalf("bob frame %d: %v", i, err)
		}
	}
	if len(c.decoders) != 2 {
		t.Fatalf("tracked %d decoders, want 2 (one per speaker)", len(c.decoders))
	}
}

// TestPlaybackSource_RoundTrip is the fidelity test: feed a 48k tone as TTS
// chunks (encode-only path), pull Opus frames, decode them back, and assert the
// recovered tone matches by RMS within tolerance — never sample-exact (Opus is
// lossy, with ~6.5 ms encoder preskip).
func TestPlaybackSource_RoundTrip(t *testing.T) {
	const rate, freq = 48000, 440
	orig := sine(rate*1, rate, freq) // 1 s

	chunks := make(chan tts.AudioChunk, 4)
	// Split into a few uneven chunks to exercise the reframer carry across calls.
	go func() {
		defer close(chunks)
		rest := orig
		for _, sz := range chunkSizes(len(orig), []int{7000, 13000, 19000, 9000}) {
			chunks <- tts.AudioChunk{PCM: pcmBytes(rest[:sz]), SampleRate: rate, Channels: 1}
			rest = rest[sz:]
		}
	}()

	c := New()
	src, err := c.PlaybackSource(chunks)
	if err != nil {
		t.Fatalf("PlaybackSource: %v", err)
	}

	dec, err := opus.NewDecoder(rate, 1)
	if err != nil {
		t.Fatalf("decoder: %v", err)
	}
	var recovered []int16
	pcm := make([]int16, opusFrameSamples)
	for {
		frame, err := src.NextFrame(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		n, err := dec.Decode(frame, pcm)
		if err != nil {
			t.Fatalf("decode round-trip: %v", err)
		}
		recovered = append(recovered, pcm[:n]...)
	}

	// Opus has a variable codec delay, so a sample-aligned RMS against the
	// original is meaningless (two clean 440 Hz tones a few samples out of phase
	// score ~0 dB). Assert the recovered audio (a) is roughly the right length
	// and (b) still reads as a ~440 Hz tone by zero-crossing rate over a
	// steady-state window — the property that actually matters for "it sounds
	// right", robust to phase/preskip.
	if len(recovered) < rate {
		t.Fatalf("recovered %d samples, want ~%d (≥1s)", len(recovered), rate)
	}
	const skip = 1200 // past preskip + ramp-in
	window := recovered[skip : skip+rate/2]
	gotHz := float64(zeroCrossings(window)) / 2 * float64(rate) / float64(len(window))
	if gotHz < freq*0.9 || gotHz > freq*1.1 {
		t.Fatalf("recovered tone ≈ %.0f Hz, want ~%d Hz (±10%%)", gotHz, freq)
	}
	// Sanity: the window must carry real energy (not decoded to silence).
	if rms(window) < 1000 {
		t.Fatalf("recovered RMS %.0f too low; tone decoded to near-silence", rms(window))
	}
}

func TestPlaybackSource_RespectsContextCancel(t *testing.T) {
	chunks := make(chan tts.AudioChunk) // never fed
	c := New()
	src, err := c.PlaybackSource(chunks)
	if err != nil {
		t.Fatalf("PlaybackSource: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := src.NextFrame(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("NextFrame err = %v, want context.Canceled", err)
	}
}

func TestPlaybackSource_StereoMonoMixed(t *testing.T) {
	// A stereo chunk must mono-mix without error and still round-trip to audio.
	const rate = 48000
	mono := sine(rate, rate, 300)
	stereo := make([]int16, len(mono)*2)
	for i, s := range mono {
		stereo[2*i] = s
		stereo[2*i+1] = s
	}
	chunks := make(chan tts.AudioChunk, 1)
	chunks <- tts.AudioChunk{PCM: pcmBytes(stereo), SampleRate: rate, Channels: 2}
	close(chunks)

	c := New()
	src, err := c.PlaybackSource(chunks)
	if err != nil {
		t.Fatalf("PlaybackSource: %v", err)
	}
	got := 0
	for {
		_, err := src.NextFrame(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		got++
	}
	if got == 0 {
		t.Fatal("stereo chunk produced no Opus frames")
	}
}

// --- helpers ---

func chunkSizes(total int, sizes []int) []int {
	var out []int
	rem := total
	for _, s := range sizes {
		if s >= rem {
			out = append(out, rem)
			return out
		}
		out = append(out, s)
		rem -= s
	}
	if rem > 0 {
		out = append(out, rem)
	}
	return out
}

// zeroCrossings counts sign changes — a phase-independent proxy for frequency.
func zeroCrossings(s []int16) int {
	count := 0
	for i := 1; i < len(s); i++ {
		if (s[i-1] < 0) != (s[i] < 0) {
			count++
		}
	}
	return count
}

// rms is the root-mean-square amplitude of s.
func rms(s []int16) float64 {
	if len(s) == 0 {
		return 0
	}
	var sum float64
	for _, v := range s {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(s)))
}
