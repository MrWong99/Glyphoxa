package voice

import "log/slog"

// MetricsRecorder receives counters for the voice-plumbing events worth
// observing across Sessions — the ones pkg/voice emits by direct call on the
// hot path (frame handling, playback, the session lifecycle). It is
// deliberately tiny: callers wire a Prometheus adapter, and the wrapper stays
// dependency-free. All methods must be safe for concurrent use; the no-op
// default ([discardMetrics]) is used when unset.
//
// Cardinality note (ADR-0032 §2.1): every method takes guild so a self-host
// build can opt into a per-guild cut, but the production Prometheus adapter
// aggregates guild away — guild/agent_id are the same unbounded SaaS class as
// tenant_id and are never series labels. The bus-derived stage/turn timings and
// provider-call counters live on the orchestrator's sibling recorder
// (internal/observe), NOT here, so per-frame instrumentation stays off the hot
// path.
type MetricsRecorder interface {
	// InboundFramesDropped reports n frames dropped from a Session's inbound
	// buffer under the drop-oldest policy (see [Session.Inbound]).
	// (glyphoxa_voice_inbound_frames_dropped_total)
	InboundFramesDropped(guild string, n int)
	// InboundUndecodableFrame reports one inbound frame skipped because
	// codec.DecodeInbound returned a non-fatal error (the wire.go:147 skip,
	// re-leveled to Debug in A1). On a healthy call this is a benign transient
	// trickle around DAVE key rolls; a sustained rate is a codec/feed fault.
	// (glyphoxa_voice_inbound_undecodable_frames_total — pairs with log L1)
	InboundUndecodableFrame(guild string)
	// SessionOpened reports a voice Session joining guild; SessionClosed reports
	// it leaving. Together they drive the glyphoxa_voice_sessions gauge.
	SessionOpened(guild string)
	// SessionClosed reports a voice Session leaving guild.
	SessionClosed(guild string)
	// PlaybackStarted reports a new [Playback] taking the floor on guild.
	PlaybackStarted(guild string)
	// PlaybackFinished reports a [Playback] ending; interrupted is true when it
	// was swapped out or stopped rather than reaching EOF.
	// (glyphoxa_voice_playback_total{interrupted})
	PlaybackFinished(guild string, interrupted bool)
	// BargeCancelled reports a confirmed barge-in that tore down an Agent's
	// active turn (ADR-0027): the voiceevent.BargeDetected moment.
	// (glyphoxa_voice_barge_cancels_total)
	BargeCancelled(guild string)
}

// discardMetrics is the no-op MetricsRecorder used when none is configured.
type discardMetrics struct{}

func (discardMetrics) InboundFramesDropped(string, int) {}
func (discardMetrics) InboundUndecodableFrame(string)   {}
func (discardMetrics) SessionOpened(string)             {}
func (discardMetrics) SessionClosed(string)             {}
func (discardMetrics) PlaybackStarted(string)           {}
func (discardMetrics) PlaybackFinished(string, bool)    {}
func (discardMetrics) BargeCancelled(string)            {}

// discardLogger returns a logger that drops everything, used when no logger is
// configured so call sites never nil-check.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
