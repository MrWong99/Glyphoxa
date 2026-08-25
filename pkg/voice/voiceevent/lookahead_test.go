package voiceevent

import (
	"context"
	"testing"
)

// TestPlaybackLookahead_Roundtrip pins the ctx marker pair (#375): a context marked
// with WithPlaybackLookahead reports true, and an unmarked context reports false.
// The wire pump routes a marked sentence into its look-ahead lane; every other
// sentence takes the normal queue.
func TestPlaybackLookahead_Roundtrip(t *testing.T) {
	if IsPlaybackLookahead(context.Background()) {
		t.Fatal("an unmarked context must not report as a playback look-ahead")
	}
	if !IsPlaybackLookahead(WithPlaybackLookahead(context.Background())) {
		t.Fatal("a WithPlaybackLookahead context must report true")
	}
}

// TestLookaheadKeyFrom pins the lane key (#626): the pump's look-ahead lane is
// keyed per SENTENCE for an intra-turn pipeline, so a ctx may carry an explicit
// key; without one the key is the turn id, which is what every ensemble caller
// (one held sentence per turn, #375) relies on.
func TestLookaheadKeyFrom(t *testing.T) {
	if got := LookaheadKeyFrom(context.Background()); got != "" {
		t.Fatalf("bare ctx: got key %q, want empty", got)
	}
	turn := WithTurnID(context.Background(), "turn-1")
	if got := LookaheadKeyFrom(turn); got != "turn-1" {
		t.Fatalf("turn ctx: got key %q, want the turn id", got)
	}
	keyed := WithLookaheadKey(turn, "turn-1#2")
	if got := LookaheadKeyFrom(keyed); got != "turn-1#2" {
		t.Fatalf("keyed ctx: got key %q, want the explicit key", got)
	}
	if got := TurnIDFrom(keyed); got != "turn-1" {
		t.Fatalf("an explicit lane key must not disturb the turn id: got %q", got)
	}
}
