//go:build opus

// Package codec implements the [wire.Codec] boundary: Opus↔PCM transcoding,
// resampling, and reframing between Discord's voice transport and the
// orchestrator's PCM pipeline.
//
// Inbound, each Discord [gxvoice.Frame] (Opus, ~20 ms, 48 kHz, possibly stereo)
// is decoded straight to 16 kHz mono by libopus (its decoder resamples and
// downmixes internally), then regrouped into the orchestrator's 32 ms /
// 512-sample [audio.Frame] cadence for VAD/STT. Outbound, the synthesized
// [tts.AudioChunk] stream is mono-mixed, resampled to 48 kHz, cut into 20 ms /
// 960-sample frames, and Opus-encoded into a [gxvoice.Source] for
// [gxvoice.Session.Play] — the "playback aligner" the orchestrator left unbuilt.
//
// This file is built only under `-tags opus` and links the system libopus (via
// github.com/hraban/opus → pkg-config opus). Always pair it with
// `-tags nolibopusfile` so hraban/opus does not also require libopusfile, which
// the codec does not use. The default build (codec_stub.go) reports
// [wire.ErrCodecUnavailable], keeping the tree green without libopus — the same
// opt-in pattern as the DAVE `-tags dave` build.
package codec

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"

	"github.com/disgoorg/snowflake/v2"
	"github.com/hraban/opus"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec/dsp"
)

const (
	// vadSampleRate is the PCM rate the orchestrator's VAD/STT run at; libopus
	// decodes inbound Opus directly to this rate. Mirrors internal/wirenpc.
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

	// maxDecodedSamples bounds the decode buffer: the largest Opus frame is
	// 120 ms, which at 16 kHz mono is 1920 samples. Sizing for it means a
	// malformed long packet never overflows the target.
	maxDecodedSamples = vadSampleRate * 120 / 1000 // 1920

	// maxEncodedBytes bounds one encoded Opus packet; 4000 is libopus's
	// recommended max for a single frame.
	maxEncodedBytes = 4000
)

// Codec implements [wire.Codec]. Inbound decoding keeps one libopus decoder per
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
}

// New returns a Codec ready to transcode. It implements [wire.Codec].
func New() *Codec {
	return &Codec{decoders: make(map[snowflake.ID]*inboundStream)}
}

var _ wire.Codec = (*Codec)(nil)

// inboundStream is the per-speaker decode state: a libopus decoder (16 kHz mono
// output) and a reframer regrouping its 320-sample packets into 512-sample
// frames.
type inboundStream struct {
	dec     *opus.Decoder
	reframe *dsp.Reframer
	pcm     []int16 // reused decode scratch buffer
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

	n, err := stream.dec.Decode(frame.Opus, stream.pcm)
	if err != nil {
		return nil, fmt.Errorf("codec: decode Opus for user %s: %w", frame.UserID, err)
	}

	grouped := stream.reframe.Push(stream.pcm[:n])
	if len(grouped) == 0 {
		return nil, nil
	}
	frames := make([]audio.Frame, 0, len(grouped))
	for _, samples := range grouped {
		f, err := audio.NewFrame(samples, vadSampleRate, vadFrameMs)
		if err != nil {
			return nil, fmt.Errorf("codec: build audio frame: %w", err)
		}
		frames = append(frames, f)
	}
	return frames, nil
}

// streamFor returns the per-speaker decode state, creating it on first sight.
func (c *Codec) streamFor(user snowflake.ID) (*inboundStream, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.decoders[user]; ok {
		return s, nil
	}
	// One decoder per stream, decoding to 16 kHz mono: libopus downmixes a
	// stereo Discord stream and resamples 48→16 kHz internally.
	dec, err := opus.NewDecoder(vadSampleRate, 1)
	if err != nil {
		return nil, fmt.Errorf("codec: new Opus decoder: %w", err)
	}
	s := &inboundStream{
		dec:     dec,
		reframe: dsp.NewReframer(vadFrameSamples),
		pcm:     make([]int16, maxDecodedSamples),
	}
	c.decoders[user] = s
	return s, nil
}

// PlaybackSource adapts a stream of synthesized [tts.AudioChunk]s into a
// [gxvoice.Source] of 20 ms Opus frames for [gxvoice.Session.Play]. Each chunk
// is mono-mixed, resampled to 48 kHz, reframed to 960 samples, and Opus-encoded
// on demand as disgo's sender pulls frames. The encoder application is VOIP
// (speech). The returned Source drains chunks until the channel closes, then
// emits a final zero-padded frame for any tail and reports io.EOF.
func (c *Codec) PlaybackSource(chunks <-chan tts.AudioChunk) (gxvoice.Source, error) {
	enc, err := opus.NewEncoder(discordSampleRate, 1, opus.AppVoIP)
	if err != nil {
		return nil, fmt.Errorf("codec: new Opus encoder: %w", err)
	}
	return &playbackSource{
		chunks:  chunks,
		enc:     enc,
		reframe: dsp.NewReframer(opusFrameSamples),
		encBuf:  make([]byte, maxEncodedBytes),
	}, nil
}

// playbackSource is the outbound aligner: it pulls TTS chunks, mono-mixes and
// resamples each to 48 kHz, regroups to 960-sample frames, and Opus-encodes one
// frame per NextFrame call. It paces nothing itself — disgo's sender polls every
// 20 ms; NextFrame blocks on the chunk channel or ctx instead.
type playbackSource struct {
	chunks <-chan tts.AudioChunk
	enc    *opus.Encoder

	resamp  *dsp.Resampler // built lazily from the first chunk's rate
	reframe *dsp.Reframer
	encBuf  []byte

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
	n, err := p.enc.Encode(frame, p.encBuf)
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
