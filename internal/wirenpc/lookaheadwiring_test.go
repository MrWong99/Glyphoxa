package wirenpc

import "testing"

// stubPump is a no-op [orchestrator.LookaheadPump] standing in for the session's
// live [wire.PlaybackPump].
type stubPump struct{}

func (stubPump) ReleaseLookahead(string) {}
func (stubPump) DiscardLookahead(string) {}

// TestBargeGroup_CarriesLookaheadPump pins the #626 production wiring: the live
// playback pump is handed to the barge group as its look-ahead lane, which is what
// turns pre-synthesis pipelining ON for ordinary routed turns. Without it every
// inter-sentence gap stays a cold TTS TTFB (~1.5 s live) and the whole feature is a
// dead code path.
func TestBargeGroup_CarriesLookaheadPump(t *testing.T) {
	pump := stubPump{}
	got := bargeGroup(conversationDeps{lookahead: pump})
	if got.Lookahead == nil {
		t.Fatal("barge group carries no look-ahead pump: routed turns would not pipeline")
	}
	if got.Confirm <= 0 {
		t.Fatalf("barge confirm window = %v, want the live default (> 0)", got.Confirm)
	}
	// Feature-off default: no pump configured (bench / voice standalone) leaves the
	// lane unwired, and every dispatch stays synchronous.
	if bargeGroup(conversationDeps{}).Lookahead != nil {
		t.Fatal("a deps without a pump must leave the look-ahead lane unwired")
	}
}
