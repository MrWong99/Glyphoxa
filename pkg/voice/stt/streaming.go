package stt

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
)

// StreamConfig configures a single streaming-recognition session opened with
// [StreamingRecognizer.OpenStream].
type StreamConfig struct {
	// SampleRate is the PCM sample rate (Hz) of every [audio.Frame] the caller
	// will Send. Frames whose rate differs are rejected by [Stream.Send]. The
	// v1 adapter supports only 16000; other values fail at OpenStream.
	SampleRate int

	// Language is the BCP-47 language hint, or "" to let the provider
	// auto-detect (mirrors the batch adapter, which leaves language unset so
	// the operator may speak EN or DE).
	Language string

	// OnPartial receives the mutable interim transcript of the in-progress
	// utterance as it arrives. Each call REPLACES the previous partial — it is
	// not cumulative. Invoked serially from the stream's read goroutine, so it
	// must not block. A nil OnPartial drops partials silently.
	OnPartial func(text string)
}

// CommitResult is the resolution of one [Stream.Commit]. Exactly one of the
// fields is meaningful: Err is non-nil (a *[StreamError]) when the segment
// could not be committed, otherwise Transcript carries the committed text.
type CommitResult struct {
	// Transcript is the authoritative committed text for the segment, mirroring
	// the batch [Recognizer]'s result. Empty text is a valid result (silent or
	// activity-free segment).
	Transcript Transcript

	// Err is nil on success, or a *[StreamError] describing why the commit
	// failed. A Fatal error means the session is dead and the caller should
	// reopen or fall back to the batch adapter.
	Err error
}

// Stream is a live streaming-recognition session over a persistent transport.
// It is NOT safe to Send from multiple goroutines concurrently; the intended
// caller is the single per-Voice-Session audio pump. Commit and Close may be
// called from any goroutine.
type Stream interface {
	// Send enqueues one audio frame toward the recognizer. It is non-blocking:
	// frames are buffered and flushed by an internal write pump. Send returns a
	// *[StreamError] once the session is dead or the internal queue is full, or
	// when frame.SampleRate does not equal the configured SampleRate. A nil
	// return does not mean the frame reached the wire, only that it was accepted.
	Send(frame audio.Frame) error

	// Commit requests a manual commit of the audio sent since the previous
	// commit and returns a channel that resolves exactly once with the
	// committed transcript (or a *[StreamError]). It is non-blocking. Multiple
	// in-flight commits resolve in FIFO order. Commit returns a non-nil error
	// (no channel) only when the session is already dead at call time.
	Commit() (<-chan CommitResult, error)

	// Close tears the session down. Pending commit channels resolve with a
	// *[StreamError]. Close is idempotent and blocks until the session's
	// goroutines have drained.
	Close() error
}

// StreamingRecognizer opens streaming-recognition sessions. It is the
// streaming sibling of [Recognizer]; the batch adapter is unaffected.
type StreamingRecognizer interface {
	// OpenStream dials a fresh session. The supplied context bounds both the
	// dial and the whole session lifetime: cancelling it is equivalent to
	// calling [Stream.Close]. A websocket-level dial failure returns a
	// *[StreamError] with Fatal set, which the caller can use to fall back to
	// the batch adapter.
	OpenStream(ctx context.Context, cfg StreamConfig) (Stream, error)
}

// These are the [StreamError.Code] values an adapter synthesizes itself, as
// opposed to provider error frames (see ADR-0042), which carry their own wire
// code verbatim. A caller switching on Code to decide fallback-vs-retry can
// rely on exactly these three originating in the adapter.
const (
	// CodeTransport marks a websocket-level failure: dial failure, a read or
	// write error, an abrupt close, or context cancellation. Always Fatal — the
	// session is dead and the caller should reopen or fall back to batch.
	CodeTransport = "transport"

	// CodeSampleRateMismatch marks a [Stream.Send] frame whose SampleRate does
	// not equal the session's declared rate. Recoverable: only that frame is
	// rejected, the session continues.
	CodeSampleRateMismatch = "sample_rate_mismatch"

	// CodeQueueFull marks a non-blocking Send or Commit that could not enqueue
	// because the internal write queue is full (backpressure). Recoverable: the
	// caller may retry once the write pump drains.
	CodeQueueFull = "queue_full"
)

// StreamError is the typed error surfaced by every streaming operation. Code is
// either the provider's error-frame type (see ADR-0042) or one of the three
// adapter-synthesized codes above — [CodeTransport] for transport-level
// failures, [CodeSampleRateMismatch] for a rejected frame, and [CodeQueueFull]
// for backpressure. Fatal reports whether the whole session is dead (reopen or
// fall back) versus only the single operation having failed.
type StreamError struct {
	Code  string
	Fatal bool
	Err   error
}

func (e *StreamError) Error() string {
	fatal := "recoverable"
	if e.Fatal {
		fatal = "fatal"
	}
	if e.Err != nil {
		return "stt stream error [" + e.Code + ", " + fatal + "]: " + e.Err.Error()
	}
	return "stt stream error [" + e.Code + ", " + fatal + "]"
}

func (e *StreamError) Unwrap() error { return e.Err }
