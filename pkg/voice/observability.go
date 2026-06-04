package voice

import "log/slog"

// MetricsRecorder receives counters for the few events worth observing across
// Sessions. It is deliberately tiny: callers wire a Prometheus or OTel adapter,
// and the wrapper stays dependency-free. All methods must be safe for
// concurrent use; the no-op default ([discardMetrics]) is used when unset.
type MetricsRecorder interface {
	// InboundFramesDropped reports n frames dropped from a Session's inbound
	// buffer under the drop-oldest policy (see [Session.Inbound]).
	InboundFramesDropped(guild string, n int)
	// PlaybackStarted reports a new [Playback] taking the floor on guild.
	PlaybackStarted(guild string)
	// PlaybackFinished reports a [Playback] ending; interrupted is true when it
	// was swapped out or stopped rather than reaching EOF.
	PlaybackFinished(guild string, interrupted bool)
}

// discardMetrics is the no-op MetricsRecorder used when none is configured.
type discardMetrics struct{}

func (discardMetrics) InboundFramesDropped(string, int) {}
func (discardMetrics) PlaybackStarted(string)           {}
func (discardMetrics) PlaybackFinished(string, bool)    {}

// discardLogger returns a logger that drops everything, used when no logger is
// configured so call sites never nil-check.
func discardLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
