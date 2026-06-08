package voiceevent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// turnIDKey is the unexported context key under which a turn's correlation id
// travels from the reply reactor down to the TTS stage and the wire tee, so the
// stages that publish [TTSInvoked] / [FirstAudio] can stamp the same id the
// turn was assigned at [STTFinal] without threading it through every call
// signature. Using an unexported zero-size type keeps the key collision-free.
type turnIDKey struct{}

// NewTurnID returns a fresh, unique turn correlation id (A3). It is an opaque
// short hex token — enough entropy to be collision-free across a session, with
// no structure callers should parse. A turn is born at [STTFinal]; this is where
// its id comes from.
func NewTurnID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is catastrophic and not something a correlation id
		// should mask; but a turn id is not security-critical, so fall back to a
		// fixed marker rather than panic the voice loop.
		return "turn-rand-unavailable"
	}
	return hex.EncodeToString(b[:])
}

// WithTurnID returns a copy of ctx carrying id, so a downstream stage
// ([TTS.Dispatch], the wire tee) can recover it with [TurnIDFrom]. The reply
// reactor installs it at the top of a turn; an empty id leaves ctx unchanged.
func WithTurnID(ctx context.Context, id string) context.Context {
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, turnIDKey{}, id)
}

// TurnIDFrom returns the turn correlation id carried by ctx, or "" if none was
// installed (the unkeyed path, e.g. a non-barge-in test harness that dispatches
// without a turn id). Callers stamp whatever they get, so "" simply yields an
// uncorrelated event rather than an error.
func TurnIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(turnIDKey{}).(string)
	return id
}
