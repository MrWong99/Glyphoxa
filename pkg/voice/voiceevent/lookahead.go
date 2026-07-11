package voiceevent

import "context"

// playbackLookaheadKey is the unexported context key marking a sentence's
// synthesis context as a PLAYBACK LOOK-AHEAD (#375, ADR-0025): the wire pump must
// hold this sentence in its look-ahead lane — synthesized eagerly (its first chunk
// pre-paid at the tee) but NOT played — until the coordinator releases it. It is
// the Cross-talk Reaction's first sentence, pre-rendered during the Lead's
// playback so its onset gap after the Lead ends is near-zero rather than a cold
// TTS TTFB. A zero-size unexported type keeps the key collision-free (precedent:
// [turnIDKey]).
type playbackLookaheadKey struct{}

// WithPlaybackLookahead returns a copy of ctx marked as a playback look-ahead, so
// the wire pump ([PlaybackPump.HandleSentence]) routes this sentence into its
// held look-ahead lane instead of the normal play queue. The marker travels the
// same path a turn id does (installed by the reply coordinator, recovered by the
// wire tee/pump). Only the FIRST sentence of a queued Reaction is marked; its
// later sentences take the normal queue once the lane is released.
func WithPlaybackLookahead(ctx context.Context) context.Context {
	return context.WithValue(ctx, playbackLookaheadKey{}, true)
}

// IsPlaybackLookahead reports whether ctx was marked by [WithPlaybackLookahead].
// The pump keys its lane routing on it; an unmarked context (every ordinary
// sentence) reports false and enqueues as today.
func IsPlaybackLookahead(ctx context.Context) bool {
	v, _ := ctx.Value(playbackLookaheadKey{}).(bool)
	return v
}
