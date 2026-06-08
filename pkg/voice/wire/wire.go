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
}

// NewPipeline wires the reactive Conversation to the Codec. conv is the fully
// configured orchestrator pipeline (built by the caller with the production
// ReplyFunc — see the cmd wiring); codec bridges Opus↔PCM (use
// [UnavailableCodec] until the transcoder lands). guild is the Discord guild ID
// the inbound counters are tagged with (A2); a nil logger discards logs and a
// nil metrics recorder discards counters.
func NewPipeline(conv *orchestrator.Conversation, codec Codec, log *slog.Logger, guild string, metrics gxvoice.MetricsRecorder) *Pipeline {
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
	return &Pipeline{conv: conv, codec: codec, log: log, guild: guild, metrics: metrics}
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
	cancel := p.conv.Register(ctx)
	defer cancel()
	defer func() {
		if err := p.conv.Flush(); err != nil {
			p.log.Warn("flush on shutdown", "err", err)
		}
	}()

	inbound := sess.Inbound()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-inbound:
			if !ok {
				return nil // session closed
			}
			if frame.Silence {
				continue
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
