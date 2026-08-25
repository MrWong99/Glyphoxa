package wirenpc

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

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

// TestCycleConversationDeps_PumpIsBothSinkAndLane pins the live-cycle handoff
// itself (#626): the session's playback pump must reach the pipeline BOTH as the
// clip-replay sink and as the look-ahead lane. Dropping the lane assignment is
// invisible to every other test — the build stays green, routed turns silently
// fall back to synchronous dispatch, and the ~1.5 s inter-sentence gap returns.
func TestCycleConversationDeps_PumpIsBothSinkAndLane(t *testing.T) {
	pump := &fakeCyclePump{}
	deps := cycleConversationDeps(voiceevent.NewBus(), slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config{}, nil, pump, nil)

	if deps.lookahead != orchestrator.LookaheadPump(pump) {
		t.Fatalf("deps.lookahead = %#v, want this cycle's playback pump (routed turns would not pipeline)", deps.lookahead)
	}
	if deps.clipReplaySink == nil {
		t.Fatal("deps.clipReplaySink is nil, want the pump's HandleSentence (#310)")
	}
}

// fakeCyclePump stands in for the live [wire.PlaybackPump] at the seam the cycle
// hands it across: a playback sink that is also the look-ahead lane.
type fakeCyclePump struct{}

func (*fakeCyclePump) HandleSentence(context.Context, <-chan tts.AudioChunk) {}
func (*fakeCyclePump) ReleaseLookahead(string)                               {}
func (*fakeCyclePump) DiscardLookahead(string)                               {}
