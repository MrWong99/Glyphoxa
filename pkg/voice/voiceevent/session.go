package voiceevent

// Session-scoped attribution (#487, CONTEXT.md): every event on the PROCESS bus
// carries the identity of the Voice Session it originated in, so process-wide
// consumers (the SSE relay, the chunk writer, memory speculation) route each
// event to the right session's state instead of a single global snapshot. Each
// Voice Session runs its own session bus; [Forward] bridges it onto the process
// bus, stamping the origin session id on every republished event.

// WithSessionID returns a COPY of e with its SessionID set to sessionID — the
// stamp [Forward] applies as it republishes a session bus's events onto the
// process bus. It is an exhaustive type switch over the event taxonomy (the
// completeness guard TestWithSessionID_TaxonomyComplete keeps it exhaustive): an
// unknown event type is returned unchanged rather than dropped, so a new event
// still crosses the bridge (just unstamped) until the switch learns about it.
// Value semantics — the original is never mutated.
func WithSessionID(e Event, sessionID string) Event {
	switch ev := e.(type) {
	case VADSpeechStart:
		ev.SessionID = sessionID
		return ev
	case VADSpeechEnd:
		ev.SessionID = sessionID
		return ev
	case VADVoicingStopped:
		ev.SessionID = sessionID
		return ev
	case VADVoicingResumed:
		ev.SessionID = sessionID
		return ev
	case STTPartial:
		ev.SessionID = sessionID
		return ev
	case STTFinal:
		ev.SessionID = sessionID
		return ev
	case AddressRouted:
		ev.SessionID = sessionID
		return ev
	case EnsembleRouted:
		ev.SessionID = sessionID
		return ev
	case EnsembleLead:
		ev.SessionID = sessionID
		return ev
	case EnsembleReaction:
		ev.SessionID = sessionID
		return ev
	case SpeakRequested:
		ev.SessionID = sessionID
		return ev
	case TTSInvoked:
		ev.SessionID = sessionID
		return ev
	case TTSStreamFailed:
		ev.SessionID = sessionID
		return ev
	case FirstAudio:
		ev.SessionID = sessionID
		return ev
	case FirstOpus:
		ev.SessionID = sessionID
		return ev
	case TurnEnded:
		ev.SessionID = sessionID
		return ev
	case BargeDetected:
		ev.SessionID = sessionID
		return ev
	case MuteChanged:
		ev.SessionID = sessionID
		return ev
	case TapeConsentChanged:
		ev.SessionID = sessionID
		return ev
	case ReplayRequested:
		ev.SessionID = sessionID
		return ev
	case SpendCapReached:
		ev.SessionID = sessionID
		return ev
	case ConnectionStateChanged:
		ev.SessionID = sessionID
		return ev
	default:
		return e
	}
}

// SessionIDOf returns the Voice Session id stamped on e, or "" when e is
// unstamped (a session-local event that never crossed [Forward], or a
// pre-#487 straggler). Process-wide consumers key their per-session state off
// this; "" is the drop-or-single-session signal.
func SessionIDOf(e Event) string {
	switch ev := e.(type) {
	case VADSpeechStart:
		return ev.SessionID
	case VADSpeechEnd:
		return ev.SessionID
	case VADVoicingStopped:
		return ev.SessionID
	case VADVoicingResumed:
		return ev.SessionID
	case STTPartial:
		return ev.SessionID
	case STTFinal:
		return ev.SessionID
	case AddressRouted:
		return ev.SessionID
	case EnsembleRouted:
		return ev.SessionID
	case EnsembleLead:
		return ev.SessionID
	case EnsembleReaction:
		return ev.SessionID
	case SpeakRequested:
		return ev.SessionID
	case TTSInvoked:
		return ev.SessionID
	case TTSStreamFailed:
		return ev.SessionID
	case FirstAudio:
		return ev.SessionID
	case FirstOpus:
		return ev.SessionID
	case TurnEnded:
		return ev.SessionID
	case BargeDetected:
		return ev.SessionID
	case MuteChanged:
		return ev.SessionID
	case TapeConsentChanged:
		return ev.SessionID
	case ReplayRequested:
		return ev.SessionID
	case SpendCapReached:
		return ev.SessionID
	case ConnectionStateChanged:
		return ev.SessionID
	default:
		return ""
	}
}

// Forward bridges a Voice Session's own bus (src) onto the process-wide bus
// (dst): it subscribes to src and republishes every event onto dst stamped with
// sessionID via [WithSessionID], so process-wide consumers can attribute it. The
// returned function unsubscribes from src (safe to call more than once).
//
// A nil dst is the bench / voice-standalone posture — there is no process bus to
// bridge onto, so Forward subscribes nothing and returns a no-op unsubscribe.
// The session-local reactors (orchestrator, barge, mute/tape wiring, detector,
// the observe StageSubscriber) stay subscribed to src directly and never see the
// stamped copy — only the process-wide consumers on dst do.
func Forward(src, dst *Bus, sessionID string) (unsubscribe func()) {
	if dst == nil || src == nil {
		return func() {}
	}
	return src.Subscribe(func(e Event) {
		dst.Publish(WithSessionID(e, sessionID))
	})
}
