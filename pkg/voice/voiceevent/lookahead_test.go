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
