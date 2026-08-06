package wirenpc

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// activityInboundOptions returns the [wire.Pipeline] option that marks this Voice
// Session's Idle Close activity on every inbound room-audio frame (ADR-0061). A
// nil mark (the Voice Instance armed no watchdog, or the Idle Close Window is
// disabled) returns nothing, so the inbound loop is byte-identical to the
// pre-Idle-Close path.
//
// The tap is per reconnect cycle, like every other pipeline option; the mark it
// calls is per Voice Session, so a cycle that drops and rejoins keeps feeding the
// same window rather than restarting it. That asymmetry is deliberate: a session
// flapping through reconnects with nobody in the channel must still age out.
func activityInboundOptions(mark func()) []wire.Option {
	if mark == nil {
		return nil
	}
	return []wire.Option{wire.WithInboundActivityTap(mark)}
}

// countCycles wraps one connect-cycle body so onCycle fires once per cycle — the
// Idle Close reconnect-churn signal (ADR-0061). A nil onCycle returns body
// unwrapped, so an unwatched loop is byte-identical.
//
// It wraps the ATTEMPT rather than living inside connectAndServe because this is
// the one place entered exactly once per cycle whatever the cycle's outcome: a
// cycle that fails before it ever joins the channel still counts, and that is the
// churn shape that leaks the most per attempt (every provider adapter and its
// http.Transport is already built by then).
func countCycles(onCycle func(), body func(ctx context.Context, connected func()) error) func(ctx context.Context, connected func()) error {
	if onCycle == nil {
		return body
	}
	return func(ctx context.Context, connected func()) error {
		onCycle()
		return body(ctx, connected)
	}
}
