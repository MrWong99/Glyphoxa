package orchestrator_test

import (
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// stepClock is a manually advanced clock for the matcher's continuation window.
type stepClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *stepClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *stepClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// replyRig wires the scoring matcher (German, default 30 s continuation window)
// behind a detector on a harness bus, with a controllable clock.
func replyRig(t *testing.T) (*voicetest.Harness, *stepClock) {
	t.Helper()
	h := voicetest.New(t)
	clk := &stepClock{t: time.Unix(5000, 0)}
	butlerAg, bartAg, goblinAg := scoringAgents()
	m := address.NewMatcher(address.Config{Language: "de", Clock: clk.now}, butlerAg, bartAg, goblinAg)
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))
	return h, clk
}

// TestAddressDetector_ReplyAudioKeepsContinuationOpen pins ledger O3b end to end
// (observed live: an NPC answered for 24 s and the player's follow-up was
// ignored). The continuation window used to count from the address; the
// detector now feeds the routed turn's reply audio to the matcher, so the
// window counts from the reply.
func TestAddressDetector_ReplyAudioKeepsContinuationOpen(t *testing.T) {
	h, clk := replyRig(t)

	h.Bus.Publish(voiceevent.STTFinal{Text: "Bart, erzähl mir die ganze Geschichte.", TurnID: "T-1", SpeakerID: "alice"})
	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1)

	// Bart answers for ~26 s: a sentence reaches the pump every few seconds.
	clk.advance(2 * time.Second)
	for i := 0; i < 5; i++ {
		h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T-1"})
		clk.advance(6 * time.Second)
	}

	// 37 s after the address — outside the 30 s window measured from it, well
	// inside the window measured from the reply.
	clk.advance(5 * time.Second)
	h.Bus.Publish(voiceevent.STTFinal{Text: "Und was geschah dann?", TurnID: "T-2", SpeakerID: "alice"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.TurnID == "T-2" && e.Target.AgentID == bartTarget.AgentID
		},
		"address.routed → Bart for the follow-up right after a long answer",
	)
}

// TestAddressDetector_NoReplyAudioLeavesWindowAtTheAddress is the control: with
// no audio for the routed turn (the reply failed, or was text-only) the window
// still counts from the address and expires as before.
func TestAddressDetector_NoReplyAudioLeavesWindowAtTheAddress(t *testing.T) {
	h, clk := replyRig(t)

	h.Bus.Publish(voiceevent.STTFinal{Text: "Bart, erzähl mir die ganze Geschichte.", TurnID: "T-1", SpeakerID: "alice"})
	// Audio of a turn the detector never routed (direct speech, a replay) is
	// not Bart answering Alice.
	clk.advance(20 * time.Second)
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T-other"})
	h.Bus.Publish(voiceevent.FirstAudio{})

	clk.advance(17 * time.Second)
	h.Bus.Publish(voiceevent.STTFinal{Text: "Und was geschah dann?", TurnID: "T-2", SpeakerID: "alice"})

	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1) // only the named one
}

// TestAddressDetector_ContinuationIsSpeakerScoped pins ledger O3a through the
// detector: the STTFinal's SpeakerID reaches the matcher, so another player's
// unnamed utterance inside the window is not answered by the NPC somebody else
// was talking to.
func TestAddressDetector_ContinuationIsSpeakerScoped(t *testing.T) {
	h, clk := replyRig(t)

	h.Bus.Publish(voiceevent.STTFinal{Text: "Bart, was weißt du über den Drachen?", TurnID: "T-1", SpeakerID: "alice"})
	clk.advance(10 * time.Second)
	h.Bus.Publish(voiceevent.STTFinal{Text: "Hat jemand noch Chips für mich?", TurnID: "T-2", SpeakerID: "bob"})
	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1)

	h.Bus.Publish(voiceevent.STTFinal{Text: "Und wo lebt er?", TurnID: "T-3", SpeakerID: "alice"})
	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.TurnID == "T-3" && e.Target.AgentID == bartTarget.AgentID
		},
		"address.routed → Bart for the opener's own follow-up",
	)
}

// TestAddressDetector_CompletedTurnAnchorsContinuationAtReplyEnd pins the exact
// anchor: the follow-up window is measured from the END of the reply
// (TurnEnded{completed}), not from the start of its last sentence. A long last
// sentence used to eat into the window — here it plays for 20 s, and a follow-up
// 25 s after the room went quiet is 45 s after the last FirstAudio.
func TestAddressDetector_CompletedTurnAnchorsContinuationAtReplyEnd(t *testing.T) {
	h, clk := replyRig(t)

	h.Bus.Publish(voiceevent.STTFinal{Text: "Bart, erzähl mir die ganze Geschichte.", TurnID: "T-1", SpeakerID: "alice"})
	voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1)

	clk.advance(2 * time.Second)
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T-1"}) // the one long sentence starts
	clk.advance(20 * time.Second)
	h.Bus.Publish(voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndCompleted})

	clk.advance(25 * time.Second)
	h.Bus.Publish(voiceevent.STTFinal{Text: "Und was geschah dann?", TurnID: "T-2", SpeakerID: "alice"})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.AddressRouted) bool {
			return e.TurnID == "T-2" && e.Target.AgentID == bartTarget.AgentID
		},
		"address.routed → Bart for a follow-up 25 s after his reply ended",
	)
}

// TestAddressDetector_OnlyCompletedRoutedTurnsMoveTheAnchor is the control for
// the terminal signal: a turn that ended any other way (it failed, was muted,
// or delivered as text) and the completion of a turn the detector never routed
// (a /say, a Highlight replay) leave the anchor where it was, so the window
// still expires from the last reply audio.
func TestAddressDetector_OnlyCompletedRoutedTurnsMoveTheAnchor(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ended voiceevent.TurnEnded
	}{
		{"provider error", voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndProviderError}},
		{"tts error", voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndTTSError}},
		{"mute", voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndMute}},
		{"text delivered", voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndTextDelivered}},
		{"unrouted turn completed", voiceevent.TurnEnded{TurnID: "T-say", Reason: voiceevent.TurnEndCompleted}},
		{"no turn id", voiceevent.TurnEnded{Reason: voiceevent.TurnEndCompleted}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, clk := replyRig(t)

			h.Bus.Publish(voiceevent.STTFinal{Text: "Bart, erzähl mir die ganze Geschichte.", TurnID: "T-1", SpeakerID: "alice"})
			clk.advance(2 * time.Second)
			h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T-1"})
			clk.advance(20 * time.Second)
			h.Bus.Publish(tc.ended)

			clk.advance(25 * time.Second) // 45 s after the last reply audio
			h.Bus.Publish(voiceevent.STTFinal{Text: "Und was geschah dann?", TurnID: "T-2", SpeakerID: "alice"})

			voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, 1) // only the named one
		})
	}
}

// TestAddressDetector_UnbindStopsReplyFeedback pins the wiring and its teardown:
// the audio and the completion of a routed turn each reach the matcher once, for
// the Agent that turn was routed to, and neither does after the detector is
// unbound.
func TestAddressDetector_UnbindStopsReplyFeedback(t *testing.T) {
	h := voicetest.New(t)
	spy := &extendSpyMatcher{route: voiceevent.AddressRouted{Target: bartTarget}}
	unbind := orchestrator.NewAddressDetector(spy).Bind(t.Context(), h.Bus)

	h.Bus.Publish(voiceevent.STTFinal{Text: "Bart?", TurnID: "T-1"})
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T-1"})
	h.Bus.Publish(voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndCompleted})
	if got := spy.extended(); len(got) != 2 || got[0] != bartTarget.AgentID || got[1] != bartTarget.AgentID {
		t.Fatalf("ExtendContinuation calls = %v, want one for the audio and one for the completion, both for Bart", got)
	}

	unbind()
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T-1"})
	h.Bus.Publish(voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndCompleted})
	if got := spy.extended(); len(got) != 2 {
		t.Fatalf("ExtendContinuation calls after unbind = %v, want still 2", got)
	}
}

// extendSpyMatcher is a [orchestrator.ReplyAwareMatcher] that routes every
// utterance to one target and records the ExtendContinuation calls it receives.
type extendSpyMatcher struct {
	route voiceevent.AddressRouted

	mu  sync.Mutex
	ids []string
}

func (m *extendSpyMatcher) TargetMatch(text string) []voiceevent.AddressRouted {
	r := m.route
	r.Text = text
	return []voiceevent.AddressRouted{r}
}

func (m *extendSpyMatcher) ExtendContinuation(agentID string) {
	m.mu.Lock()
	m.ids = append(m.ids, agentID)
	m.mu.Unlock()
}

func (m *extendSpyMatcher) extended() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ids...)
}
