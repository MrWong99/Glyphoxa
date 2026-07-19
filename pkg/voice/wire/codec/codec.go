//go:build opus

// Package codec implements the [wire.Codec] boundary: Opus↔PCM transcoding,
// resampling, and reframing between Discord's voice transport and the
// orchestrator's PCM pipeline.
//
// Inbound, each Discord [gxvoice.Frame] (Opus, ~20 ms, 48 kHz, possibly stereo)
// is decoded straight to 16 kHz mono by the decoder (it resamples and downmixes
// internally), then regrouped into the orchestrator's 32 ms / 512-sample
// [audio.Frame] cadence for VAD/STT. Outbound, the synthesized [tts.AudioChunk]
// stream is mono-mixed, resampled to 48 kHz, cut into 20 ms / 960-sample
// frames, and Opus-encoded into a [gxvoice.Source] for [gxvoice.Session.Play]
// — the "playback aligner" the orchestrator left unbuilt.
//
// The two directions use different Opus implementations. Inbound DECODE is
// github.com/pion/opus — pure Go, RFC-conformance-tested upstream, feeding
// only VAD/STT. Outbound ENCODE is github.com/hraban/opus — the system
// libopus via CGO — because pion/opus's v0.1 encoder plateaus at ~4.1 dB
// aligned SNR on real speech (vs ~6.0 dB for libopus; audibly metallic — see
// the ADR-0034 amendment and TestPlaybackSource_SpeechQualityGate) until it
// reaches speech-quality parity upstream. The `opus` build tag therefore
// implies a native toolchain again (pkg-config opus); always pair it with
// `-tags nolibopusfile` so hraban/opus does not also require libopusfile,
// which the codec does not use. The default build (codec_stub.go) reports
// [wire.ErrCodecUnavailable], keeping the tree green without libopus — the
// same opt-in pattern as the DAVE `-tags dave` build.
package codec

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/disgoorg/snowflake/v2"
	hraban "github.com/hraban/opus"
	"github.com/pion/opus"

	"github.com/MrWong99/Glyphoxa/internal/observe"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec/dsp"
)

const (
	// vadSampleRate is the PCM rate the orchestrator's VAD/STT run at; the
	// decoder emits inbound Opus directly at this rate. Mirrors internal/wirenpc.
	vadSampleRate = 16000
	// vadFrameMs / vadFrameSamples are the orchestrator's inbound frame size
	// (32 ms → 512 samples at 16 kHz), the cadence DecodeInbound must emit.
	vadFrameMs      = 32
	vadFrameSamples = vadSampleRate * vadFrameMs / 1000 // 512

	// discordSampleRate is Discord voice's Opus clock; the encoder runs here.
	discordSampleRate = 48000
	// opusFrameSamples is one 20 ms Opus frame at 48 kHz (960 samples), the
	// frame size disgo's sender expects from a [gxvoice.Source].
	opusFrameMs      = 20
	opusFrameSamples = discordSampleRate * opusFrameMs / 1000 // 960

	// maxDecodedSamples bounds the decode buffer: the largest Opus packet is
	// 120 ms, which at 16 kHz mono is 1920 samples. Sizing for it means a
	// malformed long packet never overflows the target.
	maxDecodedSamples = vadSampleRate * 120 / 1000 // 1920

	// maxEncodedBytes bounds one encoded Opus packet; 4000 is libopus's
	// recommended max for a single frame.
	maxEncodedBytes = 4000

	// reframeGap is the per-speaker PTS discontinuity beyond which the
	// reframer's leftover tail is discarded: it belongs to an utterance that
	// ended (Discord stops sending frames during silence), and carrying up to
	// ~32 ms of it into the next utterance would prefix minutes-old audio onto
	// a fresh frame. PTS also restarts at zero when a speaker rejoins, which
	// shows up as a negative delta and resets the same way.
	reframeGap = 500 * time.Millisecond

	// streamIdleTTL is how long a speaker's decode state may sit unused before
	// it is pruned. Without pruning the per-speaker map grows monotonically
	// for the Codec's lifetime (decoder + scratch per user ever heard).
	streamIdleTTL = 5 * time.Minute

	// pruneEvery is the sweep cadence, counted in DecodeInbound calls (~50/s
	// per speaker): cheap enough to keep the sweep off the hot path's tail.
	pruneEvery = 4096
)

// Codec implements [wire.Codec]. Inbound decoding keeps one Opus decoder per
// speaker (a decoder is stateful per Opus stream; feeding two SSRCs into one
// produces garbage) plus a per-speaker reframer. Outbound state is created fresh
// per [Codec.PlaybackSource] call, so a Codec may serve one Session's inbound
// loop and many sequential playbacks.
//
// DecodeInbound is called only from the single Pipeline.Run goroutine, so the
// per-speaker maps need no lock; PlaybackSource may be called from another
// goroutine, so the decoder map is still guarded to be safe under that overlap.
type Codec struct {
	mu       sync.Mutex
	decoders map[snowflake.ID]*inboundStream
	calls    int // DecodeInbound calls since the last idle-stream sweep

	// rec carries the #125 per-frame codec instrumentation: CodecDecode per inbound
	// frame decoded, CodecEncode per outbound frame encoded. Defaults to a no-op
	// (observe.Discard) so the keyless path stays silent.
	rec observe.StageRecorder
}

// Option configures a [Codec] at construction. [WithMetrics] opts the per-frame
// codec_decode / codec_encode instrumentation in.
type Option func(*Codec)

// WithMetrics injects the #125 instrumentation: rec receives one
// [observe.StageRecorder.CodecDecode] span per decoded inbound frame and one
// CodecEncode per outbound frame. A nil rec leaves the no-op default in
// place. The stub build (no -tags opus) accepts the same option and ignores it.
func WithMetrics(rec observe.StageRecorder) Option {
	return func(c *Codec) {
		if rec != nil {
			c.rec = rec
		}
	}
}

// New returns a Codec ready to transcode. It implements [wire.Codec]. Pass
// [WithMetrics] to record the per-frame codec spans; without it the codec records
// nothing (the keyless default).
func New(opts ...Option) *Codec {
	c := &Codec{decoders: make(map[snowflake.ID]*inboundStream), rec: observe.Discard{}}
	for _, o := range opts {
		o(c)
	}
	return c
}

var _ wire.Codec = (*Codec)(nil)

// inboundStream is the per-speaker decode state: an Opus decoder (16 kHz mono
// output) and a reframer regrouping its 320-sample packets into 512-sample
// frames.
type inboundStream struct {
	dec     opus.Decoder
	reframe *dsp.Reframer
	pcm     []int16 // reused decode scratch buffer

	lastPTS  time.Duration // last frame's per-speaker PTS, for gap detection
	hasPTS   bool          // lastPTS holds a real value (PTS zero is valid)
	lastSeen time.Time     // wall clock of the last decode, for idle pruning
}

// DecodeInbound decodes one Opus frame to 16 kHz mono PCM and returns the
// orchestrator [audio.Frame]s now complete (zero, one, or several — one Opus
// packet is 320 samples, frames are 512, so they regroup across packets). A
// silence frame is handled by the caller (Pipeline.Run skips Silence); an empty
// payload yields no frames.
func (c *Codec) DecodeInbound(frame gxvoice.Frame) ([]audio.Frame, error) {
	if len(frame.Opus) == 0 {
		return nil, nil
	}
	stream, err := c.streamFor(frame.UserID)
	if err != nil {
		return nil, err
	}
	stream.lastSeen = time.Now()

	// A PTS jump (speaker paused; Discord sends nothing during silence) or a
	// PTS restart (speaker rejoined; negative delta) is a stream discontinuity:
	// drop the reframer's leftover tail so the previous utterance's last
	// samples never prefix the new one.
	if stream.hasPTS {
		if delta := frame.PTS - stream.lastPTS; delta < 0 || delta > reframeGap {
			stream.reframe.Reset()
		}
	}
	stream.lastPTS = frame.PTS
	stream.hasPTS = true

	// Time the decode+reframe work: one codec_decode span per inbound frame (#125).
	// The measured section is exactly the Opus->PCM decode and the reframer regroup,
	// the per-inbound-frame cost the series names.
	decodeStart := time.Now()
	n, err := stream.dec.DecodeToInt16(frame.Opus, stream.pcm)
	if err != nil {
		return nil, fmt.Errorf("codec: decode Opus for user %s: %w", frame.UserID, err)
	}

	grouped := stream.reframe.Push(stream.pcm[:n])
	c.rec.CodecDecode(time.Since(decodeStart))
	if len(grouped) == 0 {
		return nil, nil
	}
	// Stamp each decoded frame with its speaker so the segmenter can route it to
	// that speaker's lane (ADR-0050). GUARD: snowflake.ID(0).String() == "0", not
	// "" — an unresolved SSRC (zero UserID, audio before the speaking event) must
	// stay unattributed, so only a non-zero UserID stamps a SpeakerID.
	var speaker string
	if frame.UserID != 0 {
		speaker = frame.UserID.String()
	}
	frames := make([]audio.Frame, 0, len(grouped))
	for _, samples := range grouped {
		f, err := audio.NewFrame(samples, vadSampleRate, vadFrameMs)
		if err != nil {
			return nil, fmt.Errorf("codec: build audio frame: %w", err)
		}
		frames = append(frames, f.WithSpeaker(speaker))
	}
	return frames, nil
}

// streamFor returns the per-speaker decode state, creating it on first sight.
// Every pruneEvery calls it also sweeps streams idle past streamIdleTTL, so
// the map tracks current speakers rather than everyone the Codec ever heard.
func (c *Codec) streamFor(user snowflake.ID) (*inboundStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.calls++; c.calls >= pruneEvery {
		c.calls = 0
		cutoff := time.Now().Add(-streamIdleTTL)
		for id, s := range c.decoders {
			if id != user && s.lastSeen.Before(cutoff) {
				delete(c.decoders, id)
			}
		}
	}
	if s, ok := c.decoders[user]; ok {
		return s, nil
	}
	// One decoder per stream, decoding to 16 kHz mono: the decoder downmixes a
	// stereo Discord stream and resamples 48→16 kHz internally.
	dec, err := opus.NewDecoderWithOutput(vadSampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("codec: new Opus decoder: %w", err)
	}
	s := &inboundStream{
		dec:      dec,
		reframe:  dsp.NewReframer(vadFrameSamples),
		pcm:      make([]int16, maxDecodedSamples),
		lastSeen: time.Now(),
	}
	c.decoders[user] = s
	return s, nil
}

// PlaybackSource adapts a stream of synthesized [tts.AudioChunk]s into a
// [gxvoice.Source] of 20 ms Opus frames for [gxvoice.Session.Play]. Each chunk
// is mono-mixed, resampled to 48 kHz, reframed to 960 samples, and Opus-encoded
// on demand as disgo's sender pulls frames. The encoder is libopus (hraban/opus)
// at its VoIP defaults — hybrid SILK/CELT with VBR, the profile the speech
// quality gate pins (TestPlaybackSource_SpeechQualityGate); pion/opus stays
// decode-only until its encoder reaches parity. The returned Source drains
// chunks until the channel closes, then emits a final zero-padded frame for
// any tail and reports io.EOF.
func (c *Codec) PlaybackSource(chunks <-chan tts.AudioChunk) (gxvoice.Source, error) {
	enc, err := hraban.NewEncoder(discordSampleRate, 1, hraban.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("codec: new Opus encoder: %w", err)
	}
	return &playbackSource{
		chunks:  chunks,
		enc:     enc,
		reframe: dsp.NewReframer(opusFrameSamples),
		encBuf:  make([]byte, maxEncodedBytes),
		rec:     c.rec,
	}, nil
}

// playbackSource is the outbound aligner: it pulls TTS chunks, mono-mixes and
// resamples each to 48 kHz, regroups to 960-sample frames, and Opus-encodes one
// frame per NextFrame call. It paces nothing itself — disgo's sender polls every
// 20 ms; NextFrame blocks on the chunk channel or ctx instead.
type playbackSource struct {
	chunks <-chan tts.AudioChunk
	enc    *hraban.Encoder

	resamp  *dsp.Resampler // built lazily from the first chunk's rate
	reframe *dsp.Reframer
	encBuf  []byte
	rec     observe.StageRecorder // #125: codec_encode per outbound frame

	ready   [][]int16 // resampled+reframed frames awaiting encode
	drained bool      // chunk channel closed; flush the reframer tail once
	done    bool      // tail flushed and emitted; subsequent calls are io.EOF
}

// NextFrame returns the next 20 ms Opus frame, or io.EOF when the chunk stream
// is exhausted. It honours ctx: a cancelled ctx (barge-in / interrupt, ADR-0027)
// returns ctx.Err() promptly even while waiting for the next chunk.
func (p *playbackSource) NextFrame(ctx context.Context) ([]byte, error) {
	for len(p.ready) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.done {
			return nil, io.EOF
		}
		if p.drained {
			// Channel closed: emit one final zero-padded frame for any tail.
			p.done = true
			if tail := p.reframe.Flush(); tail != nil {
				p.ready = append(p.ready, tail)
				continue
			}
			return nil, io.EOF
		}
		if err := p.pull(ctx); err != nil {
			return nil, err
		}
	}

	frame := p.ready[0]
	p.ready = p.ready[1:]
	// Time ONLY the encode: one codec_encode span per outbound frame (#125). The
	// <-chunks wait in pull() is deliberately excluded — it is synthesis network
	// time, not codec cost, and would corrupt the series.
	encStart := time.Now()
	n, err := p.enc.Encode(frame, p.encBuf)
	p.rec.CodecEncode(time.Since(encStart))
	if err != nil {
		return nil, fmt.Errorf("codec: encode Opus frame: %w", err)
	}
	// Copy out of the reused encode buffer so the caller may retain the frame.
	out := make([]byte, n)
	copy(out, p.encBuf[:n])
	return out, nil
}

// pull blocks for the next chunk (or ctx/close), converts it to 48 kHz mono
// frames, and stages them in p.ready. On channel close it marks drained.
func (p *playbackSource) pull(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case chunk, ok := <-p.chunks:
		if !ok {
			p.drained = true
			return nil
		}
		p.ingest(chunk)
		return nil
	}
}

// ingest mono-mixes and resamples one chunk to 48 kHz, then reframes into
// 960-sample frames staged in p.ready. The resampler is created from the first
// chunk's rate and reused so its phase state persists across chunks; subsequent
// chunks are resampled at that same first-seen rate. A single playback is one
// voice at one format, so all chunks share a rate in practice — a mid-stream
// rate change is not honoured (it would be resampled as if still the first rate).
func (p *playbackSource) ingest(chunk tts.AudioChunk) {
	mono := monoSamples(chunk)
	if p.resamp == nil {
		rate := chunk.SampleRate
		if rate <= 0 {
			rate = discordSampleRate
		}
		p.resamp = dsp.NewResampler(rate, discordSampleRate)
	}
	resampled := p.resamp.Process(mono)
	p.ready = append(p.ready, p.reframe.Push(resampled)...)
}

// monoSamples decodes a chunk's little-endian int16 PCM and averages stereo to
// mono. Discord and audio.Frame are mono; TTS providers may emit either.
func monoSamples(chunk tts.AudioChunk) []int16 {
	n := len(chunk.PCM) / 2
	if n == 0 {
		return nil
	}
	raw := make([]int16, n)
	for i := range raw {
		raw[i] = int16(binary.LittleEndian.Uint16(chunk.PCM[2*i:]))
	}
	if chunk.Channels <= 1 {
		return raw
	}
	// Average interleaved channels down to mono.
	ch := chunk.Channels
	frames := n / ch
	mono := make([]int16, frames)
	for f := range mono {
		var sum int
		for c := 0; c < ch; c++ {
			sum += int(raw[f*ch+c])
		}
		mono[f] = int16(sum / ch)
	}
	return mono
}
