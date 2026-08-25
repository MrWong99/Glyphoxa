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

// lookaheadKeyKey is the unexported context key carrying an EXPLICIT look-ahead
// lane key (#626). The lane holds one sentence at a time (depth 1, ADR-0025); its
// key identifies WHICH sentence is held, so a release/discard can never move a
// sentence other than the one it was issued for. The ensemble Reaction (#375)
// holds one sentence per turn and needs no explicit key; the intra-turn
// pre-synthesis pipeline holds a different sentence of the SAME turn on every
// step, so it keys per sentence.
type lookaheadKeyKey struct{}

// WithLookaheadKey returns a copy of ctx carrying key as the look-ahead lane key
// for this sentence's synthesis. An empty key leaves ctx unchanged (the turn id
// then keys the lane). It rides the same ctx path as the turn id and the
// look-ahead marker: installed by the reply coordinator, recovered by the pump.
func WithLookaheadKey(ctx context.Context, key string) context.Context {
	if key == "" {
		return ctx
	}
	return context.WithValue(ctx, lookaheadKeyKey{}, key)
}

// LookaheadKeyFrom returns the look-ahead lane key ctx routes a held sentence
// under: the explicit key from [WithLookaheadKey] when set, else the turn id
// ([TurnIDFrom]) — the pre-#626 behaviour every ensemble caller relies on, where
// one turn holds at most one sentence so turn id and lane key coincide.
func LookaheadKeyFrom(ctx context.Context) string {
	if key, _ := ctx.Value(lookaheadKeyKey{}).(string); key != "" {
		return key
	}
	return TurnIDFrom(ctx)
}
