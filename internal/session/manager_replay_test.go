package session_test

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/internal/session"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// TestReplayHighlight_IdleReturnsNoActiveSession pins the live-session requirement
// (#310, ADR-0051): a replay with no live Voice Session is refused and publishes
// nothing.
func TestReplayHighlight_IdleReturnsNoActiveSession(t *testing.T) {
	mgr, bus := muteManager(t, newFakeStore())
	var got []voiceevent.ReplayRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.ReplayRequested) { got = append(got, e) }))

	if err := mgr.ReplayHighlight(context.Background(), "clip/abc"); err != session.ErrNoActiveSession {
		t.Fatalf("ReplayHighlight while idle = %v, want ErrNoActiveSession", err)
	}
	if len(got) != 0 {
		t.Fatalf("idle ReplayHighlight published %d ReplayRequested, want none", len(got))
	}
}

// TestReplayHighlight_HappyPublishesReplayRequested pins the success path (#310): a
// live session yields exactly one ReplayRequested carrying the clip key and a fresh
// TurnID (ADR-0005: the KEY, never audio).
func TestReplayHighlight_HappyPublishesReplayRequested(t *testing.T) {
	mgr, bus := muteManager(t, newFakeStore())
	startMuteSession(t, mgr)

	var got []voiceevent.ReplayRequested
	t.Cleanup(voiceevent.On(bus, func(e voiceevent.ReplayRequested) { got = append(got, e) }))

	if err := mgr.ReplayHighlight(context.Background(), "clip/abc"); err != nil {
		t.Fatalf("ReplayHighlight happy path: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ReplayHighlight published %d ReplayRequested, want 1", len(got))
	}
	if got[0].ClipKey != "clip/abc" {
		t.Errorf("ClipKey = %q, want clip/abc", got[0].ClipKey)
	}
	if got[0].TurnID == "" {
		t.Error("ReplayRequested carries no TurnID, want a fresh one")
	}
}
