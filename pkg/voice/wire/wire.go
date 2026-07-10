// Package wire assembles the end-to-end live-NPC voice loop from the pieces the
// MVP built in isolation: the Discord audio Session (pkg/voice), the
// orchestrator reactive pipeline (VAD → STT → Address Detection → Reply → TTS),
// and the production Agent loop (pkg/voice/agent driven by the LLM provider and,
// optionally, the tool-use loop via pkg/voice/agenttool).
//
// It is the integration seam for task #4. The one piece the MVP does not yet
// have is the audio codec: Discord voice is Opus at 48 kHz, while the
// orchestrator works in PCM [audio.Frame]s at the VAD/STT sample rate and the
// TTS provider emits PCM [tts.AudioChunk]s — so a live NPC needs Opus↔PCM
// transcoding, resampling, and 20 ms reframing on both directions (the playback
// aligner pkg/voice/tts documents as not-yet-built). That work is isolated
// behind the [Codec] interface; this package wires everything up to that
// boundary and fails cleanly with [ErrCodecUnavailable] when no real Codec is
// supplied, so the construction and control flow are complete and testable
// before the codec lands.
package wire

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// ErrCodecUnavailable is returned by the stub [Codec] (and surfaced by
// [RunSession]) when the Opus↔PCM transcoding the live loop needs has not been
// built into this binary. It lets the whole pipeline be constructed and its
// control flow exercised while the codec is a separate, pending piece of work.
var ErrCodecUnavailable = errors.New("wire: audio codec unavailable (Opus↔PCM transcoding not built into this binary)")

// Codec bridges Discord's Opus audio and the orchestrator's PCM. It is the one
// boundary between the validated reasoning pipeline and the unbuilt transcoding
// layer; see the package doc.
//
// DecodeInbound turns one inbound Discord [gxvoice.Frame] (Opus, ~20 ms, 48 kHz)
// into zero or more orchestrator [audio.Frame]s (PCM, resampled to the VAD/STT
// rate and reframed to the orchestrator's frame size). One Opus packet may
// yield several PCM frames or, with buffering, none.
//
// PlaybackSource adapts the sentences the orchestrator dispatches to TTS into a
// [gxvoice.Source] of Opus frames for [gxvoice.Session.Play] — i.e. the playback
// aligner (resample + mono-mix + frame-align the [tts.AudioChunk]s) plus an
// Opus encoder. Returning ([nil], [ErrCodecUnavailable]) is valid for a build
// without the codec.
type Codec interface {
	DecodeInbound(frame gxvoice.Frame) ([]audio.Frame, error)
	PlaybackSource(chunks <-chan tts.AudioChunk) (gxvoice.Source, error)
}

// unavailableCodec is the default [Codec]: every operation reports
// [ErrCodecUnavailable]. It keeps the pipeline buildable and the inbound loop
// runnable (frames decode to nothing) until a real codec is wired in.
type unavailableCodec struct{}

// DecodeInbound implements [Codec]; always reports the codec is unavailable.
func (unavailableCodec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) {
	return nil, ErrCodecUnavailable
}

// PlaybackSource implements [Codec]; always reports the codec is unavailable.
func (unavailableCodec) PlaybackSource(<-chan tts.AudioChunk) (gxvoice.Source, error) {
	return nil, ErrCodecUnavailable
}

// UnavailableCodec returns the stub [Codec] used until the Opus transcoding
// layer is built. Wiring it makes the live loop construct and run, but no audio
// is decoded or played — every codec call yields [ErrCodecUnavailable].
func UnavailableCodec() Codec { return unavailableCodec{} }

// Pipeline is the assembled reactive voice pipeline for one Agent: the
// orchestrator [orchestrator.Conversation] (VAD → STT → Address Detection →
// production Reply → TTS) plus the [Codec] that bridges it to Discord audio.
// Build it with [NewPipeline]; feed a [gxvoice.Session] to [Pipeline.Run].
type Pipeline struct {
	conv    *orchestrator.Conversation
	codec   Codec
	log     *slog.Logger
	metrics gxvoice.MetricsRecorder
	guild   string

	// silence is the synthesized PCM silence frame the silence clock feeds into
	// the VAD during inbound gaps (issue #91); silenceOn gates the whole mechanism
	// off when no [WithSilenceClock] was given (the pre-#91 behaviour). newClock
	// builds the frame-cadence clock — a real time.Ticker in production, a fake in
	// tests. See [WithSilenceClock] and [Pipeline.run].
	silence   audio.Frame
	silenceOn bool
	newClock  func() silenceClock

	// inboundTap, when set, is called with every non-silence inbound Opus frame
	// just before it is decoded (the rollover tape's inbound capture point, #306);
	// pcmTap, when set, is called with every decoded PCM frame (Speaker()-stamped,
	// the highlight detector's feature source, #307). Both are nil by default —
	// no tap wired means byte-identical existing behaviour — and both MUST return
	// promptly: they run inline on the audio loop and any block adds latency
	// (ADR-0020/0026).
	inboundTap func(f gxvoice.Frame)
	pcmTap     func(f audio.Frame)
}

// WithInboundTap installs a tap called with every non-silence inbound Opus frame
// before it is decoded (the rollover tape's capture point, #306). The tap MUST
// NOT block — it runs inline on the audio loop. Without this option the loop is
// unchanged.
func WithInboundTap(tap func(f gxvoice.Frame)) Option {
	return func(p *Pipeline) { p.inboundTap = tap }
}

// WithPCMTap installs a tap called with every decoded PCM frame, Speaker()-stamped
// as the codec produced it (#307's audio-feature source). The tap MUST NOT block.
// Without this option the loop is unchanged.
func WithPCMTap(tap func(f audio.Frame)) Option {
	return func(p *Pipeline) { p.pcmTap = tap }
}

// silenceClock paces synthesized PCM silence into the VAD so trailing silence
// (Discord stops sending packets when a speaker pauses — see [Pipeline.run])
// advances silero's end-of-speech hangover and the utterance endpoints naturally,
// instead of hanging until the next utterance arrives (issue #91). It ticks once
// per orchestrator frame interval while inbound audio is idle; every real inbound
// frame calls reset, suppressing a tick for one interval so NO silence is injected
// while speech flows. Production uses a [time.Ticker]; tests inject a fake whose
// channel they fire by hand, so endpointing is deterministic with no wall-clock
// (the precedent: address.Clock, reconnectPolicy.sleep).
type silenceClock interface {
	ticks() <-chan time.Time
	reset()
	stop()
}

// tickerClock is the production [silenceClock]: a [time.Ticker] at the frame
// cadence. reset restarts the interval on every real inbound frame, so the ticker
// only fires once audio has been idle for one full interval and keeps firing every
// interval until audio resumes.
type tickerClock struct {
	t *time.Ticker
	d time.Duration
}

func newTickerClock(d time.Duration) *tickerClock {
	return &tickerClock{t: time.NewTicker(d), d: d}
}

func (c *tickerClock) ticks() <-chan time.Time { return c.t.C }
func (c *tickerClock) reset()                  { c.t.Reset(c.d) }
func (c *tickerClock) stop()                   { c.t.Stop() }

// Option configures a [Pipeline] at construction.
type Option func(*Pipeline)

// WithSilenceClock enables the continuous silence clock (issue #91): once a
// speaker stop is signalled by Discord's explicit Opus silence frames, the
// pipeline feeds synthesized PCM silence into the VAD at the orchestrator frame
// cadence through the packet gap that follows, so a paused speaker's utterance
// endpoints within the silero hangover window rather than coalescing with the
// next utterance. A packet-arrival gap WITHOUT that signal — transport jitter
// mid-utterance — injects nothing, so it cannot falsely split a turn (issue
// #147). sampleRate and frameMs are the orchestrator's frame geometry (the
// VAD/STT rate, e.g. 16000 Hz / 32 ms): they shape the synthesized silence frame
// AND set the tick interval. Without this option the pipeline keeps the pre-#91
// behaviour, dropping inbound silence frames untouched.
func WithSilenceClock(sampleRate, frameMs int) Option {
	d := time.Duration(frameMs) * time.Millisecond
	return withSilenceClock(sampleRate, frameMs, func() silenceClock { return newTickerClock(d) })
}

// withSilenceClock is the seam [WithSilenceClock] is built on, with the clock
// factory injectable so tests drive a fake clock channel by hand. It derives the
// silence frame from the supplied geometry (no magic 512/16000/32 in wire — the
// shape matches whatever rate the pipeline runs at), panicking on an invalid
// geometry since that is a wiring bug, not a runtime condition.
func withSilenceClock(sampleRate, frameMs int, newClock func() silenceClock) Option {
	return func(p *Pipeline) {
		f, err := audio.NewFrame(make([]int16, sampleRate*frameMs/1000), sampleRate, frameMs)
		if err != nil {
			panic(fmt.Sprintf("wire.WithSilenceClock: invalid frame geometry %d Hz / %d ms: %v", sampleRate, frameMs, err))
		}
		p.silence = f
		p.silenceOn = true
		p.newClock = newClock
	}
}

// NewPipeline wires the reactive Conversation to the Codec. conv is the fully
// configured orchestrator pipeline (built by the caller with the production
// ReplyFunc — see the cmd wiring); codec bridges Opus↔PCM (use
// [UnavailableCodec] until the transcoder lands). guild is the Discord guild ID
// the inbound counters are tagged with (A2); a nil logger discards logs and a
// nil metrics recorder discards counters.
func NewPipeline(conv *orchestrator.Conversation, codec Codec, log *slog.Logger, guild string, metrics gxvoice.MetricsRecorder, opts ...Option) *Pipeline {
	if conv == nil {
		panic("wire.NewPipeline: conv must not be nil")
	}
	if codec == nil {
		codec = UnavailableCodec()
	}
	if log == nil {
		log = slog.New(slog.NewTextHandler(nopWriter{}, nil))
	}
	if metrics == nil {
		metrics = discardMetrics{}
	}
	p := &Pipeline{conv: conv, codec: codec, log: log, guild: guild, metrics: metrics}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Run registers the conversation's reactors on its bus and pumps the Session's
// inbound Opus frames through the [Codec] into the orchestrator until ctx is
// cancelled or the inbound channel closes. It is the audio loop the headless
// voicetest harness stands in for in unit tests (ADR-0019): here the frames
// come from a live [gxvoice.Session] instead of a clip.
//
// With the [UnavailableCodec], decoding every frame returns
// [ErrCodecUnavailable]; Run logs the first occurrence and returns it, so a
// codec-less binary fails fast and visibly rather than silently hearing
// nothing. Once a real Codec is wired, the same loop drives a live NPC.
func (p *Pipeline) Run(ctx context.Context, sess *gxvoice.Session) error {
	if sess == nil {
		return fmt.Errorf("wire.Run: session must not be nil")
	}
	return p.run(ctx, sess.Inbound())
}

// run is the inbound audio loop over an arbitrary frame channel: it registers the
// conversation, pumps inbound frames through the [Codec] into the orchestrator,
// and drives the silence clock that endpoints a paused speaker (issue #91). Run
// supplies a live [gxvoice.Session]'s channel; the seam exists so the headless
// tests drive the same loop with a synthetic inbound channel and an injected
// silence clock — no Discord and no wall-clock waits (ADR-0019).
func (p *Pipeline) run(ctx context.Context, inbound <-chan gxvoice.Frame) error {
	cancel := p.conv.Register(ctx)
	defer cancel()
	defer func() {
		if err := p.conv.Flush(); err != nil {
			p.log.Warn("flush on shutdown", "err", err)
		}
	}()

	// Silence clock (#91): Discord sends a few Opus silence frames when a speaker
	// stops, then STOPS sending packets entirely during the pause — so the inbound
	// channel goes quiet and the VAD never sees the trailing silence that ends the
	// utterance, leaving each line one utterance behind. While audio is idle this
	// clock feeds synthesized PCM silence into the VAD at the frame cadence,
	// advancing silero's end-of-speech hangover so the segment endpoints a few
	// hundred ms after the speaker stops. Every real frame resets it, so NO silence
	// is injected while speech flows (continuous speech must not endpoint). Disabled
	// when no [WithSilenceClock] was given: the loop then just drops inbound silence
	// frames, the pre-#91 behaviour.
	//
	// The clock injects only while ARMED (#147): a wall-clock arrival gap alone is
	// NOT evidence the speaker stopped — a ≥ hangover transport jitter burst
	// mid-utterance would otherwise be endpointed as if the speaker had paused,
	// splitting one turn in two and losing half of it. The speaker-stop signal on
	// the wire is Discord's explicit Opus silence frames (a sender emits ~5 before
	// it stops transmitting; the frame's PTS stream time carries no gap during
	// arrival jitter), so a silence frame arms the clock and every real frame
	// disarms it. If all stop-silence frames are lost, the utterance endpoints at
	// the next arming signal or the shutdown Flush — the pre-#91 worst case —
	// instead of risking a false mid-utterance split.
	var clk silenceClock
	var clockTicks <-chan time.Time
	armed := false
	if p.silenceOn {
		clk = p.newClock()
		defer clk.stop()
		clockTicks = clk.ticks()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-clockTicks:
			if !armed {
				// Packets stopped ARRIVING but no Discord silence frame said the
				// speaker stopped: an unarmed tick is transport jitter, not a pause.
				continue
			}
			// The speaker stopped and audio has been idle for one frame interval:
			// advance the VAD with one frame of silence so the utterance endpoints. This
			// is the speaker-agnostic silence CLOCK — it must reach EVERY Speaker Lane's
			// VAD hangover, so it routes through FeedSilence, distinct from the per-speaker
			// inbound audio on Feed below (ADR-0050).
			if err := p.conv.FeedSilence(p.silence); err != nil {
				p.log.Debug("feed silence frame", "err", err)
			}
		case frame, ok := <-inbound:
			if !ok {
				return nil // session closed
			}
			if frame.Silence {
				// A Discord Opus silence frame: the speaker has stopped. Arm the
				// clock, but do NOT decode the frame and do NOT reset the clock — let
				// it keep advancing the VAD hangover through this frame and the packet
				// gap that follows, so the utterance endpoints (issue #91). Pre-#91
				// this `continue` dropped the frame with nothing left to advance the VAD.
				armed = true
				continue
			}
			// Real audio arrived: the speaker is talking again. Disarm and reset the
			// idle clock so no synthesized silence is injected while frames keep
			// flowing — not even through an arrival gap (#147).
			armed = false
			if clk != nil {
				clk.reset()
			}
			// Rollover tape capture (#306): every non-silence inbound frame, before
			// decode, with its Opus payload + UserID intact. Nil unless armed; must
			// not block.
			if p.inboundTap != nil {
				p.inboundTap(frame)
			}
			pcm, err := p.codec.DecodeInbound(frame)
			if err != nil {
				// A codec-less build can never decode anything: fail fast so it
				// does not masquerade as a working but deaf NPC.
				if errors.Is(err, ErrCodecUnavailable) {
					return fmt.Errorf("wire.Run: decode inbound: %w", err)
				}
				// A single undecodable packet (e.g. a corrupt/transitional frame
				// during the DAVE/MLS key handshake) must not tear down the whole
				// voice session — skip it and keep listening. This is a per-packet
				// event that fires routinely on a healthy call (A1), so it logs at
				// Debug, not Warn, and bumps a counter so its volume stays observable
				// without flooding the operator's console.
				p.log.Debug("skipping undecodable inbound frame", "user", frame.UserID, "err", err)
				p.metrics.InboundUndecodableFrame(p.guild)
				continue
			}
			for _, f := range pcm {
				// Decoded-PCM tap (#307): Speaker()-stamped frames for the highlight
				// detector's audio features. Nil unless wired; must not block.
				if p.pcmTap != nil {
					p.pcmTap(f)
				}
				if err := p.conv.Feed(f); err != nil {
					// Also a benign per-packet event on a healthy call (A1): Debug.
					p.log.Debug("feed frame", "err", err)
				}
			}
		}
	}
}

// nopWriter is an io.Writer that discards everything; the default logger sink.
type nopWriter struct{}

func (nopWriter) Write(p []byte) (int, error) { return len(p), nil }

// discardMetrics is the wire-local no-op [gxvoice.MetricsRecorder] used when
// [NewPipeline] is handed a nil recorder, so the inbound loop never nil-checks.
// Only the inbound counters the Pipeline emits are implemented; the rest satisfy
// the interface as no-ops.
type discardMetrics struct{}

func (discardMetrics) InboundFramesDropped(string, int) {}
func (discardMetrics) InboundUndecodableFrame(string)   {}
func (discardMetrics) SessionOpened(string)             {}
func (discardMetrics) SessionClosed(string)             {}
func (discardMetrics) PlaybackStarted(string)           {}
func (discardMetrics) PlaybackFinished(string, bool)    {}
func (discardMetrics) BargeCancelled(string)            {}
