//go:build opus

package codec

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"
	"github.com/pion/opus"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// codecSpy is a [observe.StageRecorder] that counts the per-frame codec spans
// (#125). Embeds [observe.Discard] so every other method is a no-op. Safe for
// concurrent use (PlaybackSource may pull from another goroutine).
type codecSpy struct {
	observe.Discard
	mu      sync.Mutex
	decodes int
	encodes int
}

func (s *codecSpy) CodecDecode(time.Duration) {
	s.mu.Lock()
	s.decodes++
	s.mu.Unlock()
}

func (s *codecSpy) CodecEncode(time.Duration) {
	s.mu.Lock()
	s.encodes++
	s.mu.Unlock()
}

func (s *codecSpy) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decodes, s.encodes
}

// TestDecodeInbound_RecordsCodecDecodePerFrame pins the #125 codec_decode wiring:
// each non-empty inbound Opus frame decoded records exactly one CodecDecode span,
// so N inbound packets → N observations (the histogram's "per inbound frame"
// contract). An empty payload decodes nothing and records nothing.
func TestDecodeInbound_RecordsCodecDecodePerFrame(t *testing.T) {
	packets := encodeOpus(t, sine(48000, 48000, 330), 48000)
	spy := &codecSpy{}
	c := New(WithMetrics(spy))
	user := snowflake.ID(7)
	for _, pkt := range packets {
		if _, err := c.DecodeInbound(gxvoice.Frame{UserID: user, Opus: pkt}); err != nil {
			t.Fatalf("DecodeInbound: %v", err)
		}
	}
	// An empty payload is a no-op — it must not record a decode span.
	if _, err := c.DecodeInbound(gxvoice.Frame{UserID: user, Opus: nil}); err != nil {
		t.Fatalf("DecodeInbound(empty): %v", err)
	}

	decodes, _ := spy.counts()
	if decodes != len(packets) {
		t.Errorf("codec_decode recorded %d spans, want %d (one per decoded inbound frame)", decodes, len(packets))
	}
}

// TestPlaybackSource_RecordsCodecEncodePerFrame pins the #125 codec_encode wiring:
// each outbound Opus frame the playback source produces records exactly one
// CodecEncode span. The chunk channel is PRE-FILLED and closed before the first
// pull, so the measured cost is the enc.Encode work, never the synthesis-network
// <-chunks wait (which would corrupt the series).
func TestPlaybackSource_RecordsCodecEncodePerFrame(t *testing.T) {
	const rate = 48000
	chunks := make(chan tts.AudioChunk, 1)
	chunks <- tts.AudioChunk{PCM: pcmBytes(sine(rate, rate, 440)), SampleRate: rate, Channels: 1}
	close(chunks) // pre-filled + closed: NextFrame never blocks on synthesis

	spy := &codecSpy{}
	c := New(WithMetrics(spy))
	src, err := c.PlaybackSource(chunks)
	if err != nil {
		t.Fatalf("PlaybackSource: %v", err)
	}

	var frames int
	for {
		_, err := src.NextFrame(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("NextFrame: %v", err)
		}
		frames++
	}

	_, encodes := spy.counts()
	if frames == 0 {
		t.Fatal("playback produced no frames")
	}
	if encodes != frames {
		t.Errorf("codec_encode recorded %d spans, want %d (one per outbound frame)", encodes, frames)
	}
}

// encodeOpus encodes mono int16 PCM at rate into 20 ms Opus packets, the shape
// Discord delivers (pion's encoder, standing in for a Discord client). Used to
// synthesize inbound frames for DecodeInbound.
func encodeOpus(t *testing.T, pcm []int16, rate int) [][]byte {
	t.Helper()
	// Discord's standard voice bitrate; only shapes the synthesized fixtures.
	const inboundBitrate = 64000
	enc, err := opus.NewEncoder(
		opus.WithSampleRate(rate),
		opus.WithChannels(1),
		opus.WithBitrate(inboundBitrate),
		opus.WithApplication(opus.ApplicationVoIP),
	)
	if err != nil {
		t.Fatalf("new encoder: %v", err)
	}
	frame := rate * 20 / 1000
	f32 := make([]float32, frame)
	var packets [][]byte
	for off := 0; off+frame <= len(pcm); off += frame {
		for i, s := range pcm[off : off+frame] {
			f32[i] = float32(s) / 32768
		}
		buf := make([]byte, maxEncodedBytes)
		n, err := enc.EncodeFloat32(f32, buf)
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

	dec, err := opus.NewDecoderWithOutput(rate, 1)
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
		n, err := dec.DecodeToInt16(frame, pcm)
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

// TestDecodeInbound_StampsSpeakerFromUserID pins the ADR-0050 codec stamp: every
// decoded audio.Frame carries its speaker's Discord snowflake string, so the
// segmenter can route it to that speaker's lane. A known UserID stamps its
// String(); the zero UserID (SSRC not yet resolved) MUST stay unattributed ("") —
// snowflake.ID(0).String() is "0", so a naive stamp would misattribute unknown
// audio to a bogus "0" speaker.
func TestDecodeInbound_StampsSpeakerFromUserID(t *testing.T) {
	packets := encodeOpus(t, sine(48000, 48000, 330), 48000)
	c := New()
	user := snowflake.ID(4242)

	var sawAttributed bool
	for _, pkt := range packets {
		out, err := c.DecodeInbound(gxvoice.Frame{UserID: user, Opus: pkt})
		if err != nil {
			t.Fatalf("DecodeInbound: %v", err)
		}
		for _, f := range out {
			if f.Speaker() != user.String() {
				t.Fatalf("frame Speaker() = %q, want %q", f.Speaker(), user.String())
			}
			sawAttributed = true
		}
	}
	if !sawAttributed {
		t.Fatal("no attributed frames emitted")
	}

	// Unknown SSRC (zero UserID) must stay "" — never the literal "0".
	c2 := New()
	for _, pkt := range packets {
		out, err := c2.DecodeInbound(gxvoice.Frame{UserID: 0, Opus: pkt})
		if err != nil {
			t.Fatalf("DecodeInbound(zero user): %v", err)
		}
		for _, f := range out {
			if f.Speaker() != "" {
				t.Fatalf("zero-UserID frame Speaker() = %q, want \"\" (unattributed)", f.Speaker())
			}
		}
	}
}
