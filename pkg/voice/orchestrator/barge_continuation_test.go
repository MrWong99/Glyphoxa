package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// TestBargeIn_AttributesCutAgent pins the ADR-0027 amendment's carrier: a
// confirmed barge names the Agent it cut, not just the speaker who cut it.
// Without the attribution the routing layer cannot tell WHICH Agent the human
// wanted to stop.
func TestBargeIn_AttributesCutAgent(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	parent := voiceevent.WithTurnID(context.Background(), "T1")
	_, release, _ := floor.Take(parent, "npc-bart")
	defer release()

	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T1"})
	h.Bus.Publish(voiceevent.VADSpeechStart{SpeakerID: "player-1"})

	// WaitEvent, not AssertEvent: the turn ctx being Done does NOT mean
	// barge.detected has been published — the cancel and the publish are separate
	// steps on the barge-in goroutine. A snapshot assertion here passes on an idle
	// machine and fails under load, which is how this became a CI flake that
	// blocked unrelated PRs.
	voicetest.WaitEvent(t, h, 2*time.Second,
		func(e voiceevent.BargeDetected) bool { return e.AgentID == "npc-bart" && e.SpeakerID == "player-1" },
		"barge.detected naming both the barging speaker and the cut Agent",
	)
}

// clearSpyMatcher is a [orchestrator.TargetMatcher] that also implements
// [orchestrator.InterruptibleMatcher], recording which Agents were cleared.
type clearSpyMatcher struct {
	mu      sync.Mutex
	cleared []string
	routes  []voiceevent.AddressRouted
}

func (m *clearSpyMatcher) TargetMatch(string) []voiceevent.AddressRouted { return m.routes }

func (m *clearSpyMatcher) ClearContinuation(agentID string) {
	m.mu.Lock()
	m.cleared = append(m.cleared, agentID)
	m.mu.Unlock()
}

func (m *clearSpyMatcher) clearedIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.cleared...)
}

// TestAddressDetector_BargeClearsContinuation pins the wiring that closes the
// live "the NPCs answer far too often" loop: an interruption revokes the cut
// Agent's continuation claim, so the interrupting utterance — itself a routable
// final moments later — does not land straight back on the Agent the human just
// cut off.
func TestAddressDetector_BargeClearsContinuation(t *testing.T) {
	h := voicetest.New(t)
	m := &clearSpyMatcher{}
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.BargeDetected{SpeakerID: "player-1", AgentID: "npc-bart"})

	if got := m.clearedIDs(); len(got) != 1 || got[0] != "npc-bart" {
		t.Fatalf("cleared = %v, want [npc-bart]", got)
	}
}

// TestAddressDetector_UnattributedBargeClearsNothing pins the fail-safe: a barge
// that could not name its Agent (empty AgentID — e.g. a holder taken with no
// target) must revoke nothing rather than guess, since guessing would silence an
// Agent nobody interrupted.
func TestAddressDetector_UnattributedBargeClearsNothing(t *testing.T) {
	h := voicetest.New(t)
	m := &clearSpyMatcher{}
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.BargeDetected{SpeakerID: "player-1"})

	if got := m.clearedIDs(); len(got) != 0 {
		t.Fatalf("cleared = %v, want nothing", got)
	}
}

// TestAddressDetector_BargeUnsubscribesOnCancel pins teardown: the barge
// subscription is removed with the rest of the detector's, so a torn-down Voice
// Session cannot keep mutating a matcher it no longer routes for.
func TestAddressDetector_BargeUnsubscribesOnCancel(t *testing.T) {
	h := voicetest.New(t)
	m := &clearSpyMatcher{}
	cancel := orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus)
	cancel()

	h.Bus.Publish(voiceevent.BargeDetected{SpeakerID: "player-1", AgentID: "npc-bart"})

	if got := m.clearedIDs(); len(got) != 0 {
		t.Fatalf("cleared = %v after teardown, want nothing", got)
	}
}

// routingClearSpy is a clearSpyMatcher that routes every utterance to Bart, so
// the detector has a routed turn on record to judge a barge against.
func routingClearSpy() *clearSpyMatcher {
	return &clearSpyMatcher{routes: []voiceevent.AddressRouted{{
		Target: voiceevent.AddressTarget{AgentID: "npc-bart", AgentRole: voiceevent.AgentRoleCharacter},
	}}}
}

// TestAddressDetector_EarlyBargeByTheAskerKeepsContinuation pins the
// split-utterance exception found in live voice testing (2026-11-21): a speaker
// pauses mid-request, the Agent starts answering, and the speaker's own next
// words cut it off within the first moments of audio. Revoking the claim there
// routed the rest of the request nowhere — the Agent went silent for good.
func TestAddressDetector_EarlyBargeByTheAskerKeepsContinuation(t *testing.T) {
	h := voicetest.New(t)
	m := routingClearSpy()
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	start := time.Now()
	h.Bus.Publish(voiceevent.STTFinal{TurnID: "T1", SpeakerID: "player-1", Text: "Bart, tell me a long story"})
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start})
	h.Bus.Publish(voiceevent.BargeDetected{At: start.Add(500 * time.Millisecond), SpeakerID: "player-1", AgentID: "npc-bart"})

	if got := m.clearedIDs(); len(got) != 0 {
		t.Fatalf("cleared = %v, want nothing: the asker was finishing their own request", got)
	}
}

// TestAddressDetector_BargeStillClears covers every case the exception must NOT
// swallow: the interruption comes once the answer is under way, from someone
// other than the asker, from an unattributed speaker, or before any audio is on
// record. Each is an interruption and revokes the claim as before.
func TestAddressDetector_BargeStillClears(t *testing.T) {
	start := time.Now()
	cases := []struct {
		name       string
		firstAudio bool
		barge      voiceevent.BargeDetected
	}{
		{"late barge by the asker", true, voiceevent.BargeDetected{At: start.Add(3 * time.Second), SpeakerID: "player-1", AgentID: "npc-bart"}},
		{"early barge by someone else", true, voiceevent.BargeDetected{At: start.Add(500 * time.Millisecond), SpeakerID: "player-2", AgentID: "npc-bart"}},
		{"early unattributed barge", true, voiceevent.BargeDetected{At: start.Add(500 * time.Millisecond), AgentID: "npc-bart"}},
		{"no audio on record", false, voiceevent.BargeDetected{At: start.Add(500 * time.Millisecond), SpeakerID: "player-1", AgentID: "npc-bart"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := voicetest.New(t)
			m := routingClearSpy()
			t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

			h.Bus.Publish(voiceevent.STTFinal{TurnID: "T1", SpeakerID: "player-1", Text: "Bart, tell me a long story"})
			if tc.firstAudio {
				h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start})
			}
			h.Bus.Publish(tc.barge)

			if got := m.clearedIDs(); len(got) != 1 || got[0] != "npc-bart" {
				t.Fatalf("cleared = %v, want [npc-bart]", got)
			}
		})
	}
}

// TestAddressDetector_EarlyBargeJudgesTheCutTurn pins which reply the barge is
// measured against. player-1's own reply is fresh (audio at +0), but the reply
// that was cut answers player-2 (audio at +1 s): player-1 cutting it at +1.5 s
// is an interruption, whether the barge names the cut turn or the detector has
// to fall back to the latest turn routed to the Agent.
func TestAddressDetector_EarlyBargeJudgesTheCutTurn(t *testing.T) {
	for _, turnID := range []string{"T2", ""} {
		t.Run("barge turn id "+turnID, func(t *testing.T) {
			h := voicetest.New(t)
			m := routingClearSpy()
			t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

			start := time.Now()
			h.Bus.Publish(voiceevent.STTFinal{TurnID: "T1", SpeakerID: "player-1", Text: "Bart, hello"})
			h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start})
			h.Bus.Publish(voiceevent.STTFinal{TurnID: "T2", SpeakerID: "player-2", Text: "Bart, and me?"})
			h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T2", At: start.Add(time.Second)})
			h.Bus.Publish(voiceevent.BargeDetected{At: start.Add(1500 * time.Millisecond), SpeakerID: "player-1", AgentID: "npc-bart", TurnID: turnID})

			if got := m.clearedIDs(); len(got) != 1 {
				t.Fatalf("cleared = %v, want [npc-bart]: the cut reply answered another speaker", got)
			}
		})
	}
}

// TestAddressDetector_EarlyBargeNamesTheCutTurn pins the other direction: a
// later turn routed to the same Agent that never became audible (coalesced,
// refused by a spend cap) must not shadow the reply that was actually cut.
func TestAddressDetector_EarlyBargeNamesTheCutTurn(t *testing.T) {
	h := voicetest.New(t)
	m := routingClearSpy()
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	start := time.Now()
	h.Bus.Publish(voiceevent.STTFinal{TurnID: "T1", SpeakerID: "player-1", Text: "Bart, tell me a long story"})
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start})
	h.Bus.Publish(voiceevent.STTFinal{TurnID: "T2", SpeakerID: "player-1", Text: "Bart"})
	h.Bus.Publish(voiceevent.BargeDetected{At: start.Add(500 * time.Millisecond), SpeakerID: "player-1", AgentID: "npc-bart", TurnID: "T1"})

	if got := m.clearedIDs(); len(got) != 0 {
		t.Fatalf("cleared = %v, want nothing: T1 was the reply that was cut", got)
	}
}

// TestAddressDetector_GraceCountsFromTheFirstSentence pins that the window is
// measured from the reply's FIRST audio. FirstAudio fires once per sentence; a
// window that restarted with each one would forgive a cut at any point of a
// long answer.
func TestAddressDetector_GraceCountsFromTheFirstSentence(t *testing.T) {
	h := voicetest.New(t)
	m := routingClearSpy()
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	start := time.Now()
	h.Bus.Publish(voiceevent.STTFinal{TurnID: "T1", SpeakerID: "player-1", Text: "Bart, tell me a long story"})
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start})
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start.Add(5 * time.Second)})
	h.Bus.Publish(voiceevent.BargeDetected{At: start.Add(5500 * time.Millisecond), SpeakerID: "player-1", AgentID: "npc-bart", TurnID: "T1"})

	if got := m.clearedIDs(); len(got) != 1 {
		t.Fatalf("cleared = %v, want [npc-bart]: the answer was five seconds in", got)
	}
}

// TestAddressDetector_GraceBoundary pins the window's size from both sides.
func TestAddressDetector_GraceBoundary(t *testing.T) {
	for _, tc := range []struct {
		after     time.Duration
		wantClear int
	}{{1500 * time.Millisecond, 0}, {1501 * time.Millisecond, 1}} {
		t.Run(tc.after.String(), func(t *testing.T) {
			h := voicetest.New(t)
			m := routingClearSpy()
			t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

			start := time.Now()
			h.Bus.Publish(voiceevent.STTFinal{TurnID: "T1", SpeakerID: "player-1", Text: "Bart, tell me a long story"})
			h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start})
			h.Bus.Publish(voiceevent.BargeDetected{At: start.Add(tc.after), SpeakerID: "player-1", AgentID: "npc-bart", TurnID: "T1"})

			if got := m.clearedIDs(); len(got) != tc.wantClear {
				t.Fatalf("cleared = %v, want %d revocation(s)", got, tc.wantClear)
			}
		})
	}
}

// TestAddressDetector_SecondEarlyBargeInARowRevokes pins the bound on the
// exception: it forgives one early cut. A speaker who cuts the next reply early
// too is rejecting the answer, and must be able to silence the Agent — without
// the bound every fast "no, stop" produced another reply, forever. A reply that
// plays to its end restores the exception.
func TestAddressDetector_SecondEarlyBargeInARowRevokes(t *testing.T) {
	h := voicetest.New(t)
	m := routingClearSpy()
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	start := time.Now()
	turn := func(id string, at time.Time) {
		h.Bus.Publish(voiceevent.STTFinal{TurnID: id, SpeakerID: "player-1", Text: "Bart, tell me a long story"})
		h.Bus.Publish(voiceevent.FirstAudio{TurnID: id, At: at})
	}
	earlyBarge := func(id string, at time.Time) {
		h.Bus.Publish(voiceevent.BargeDetected{At: at.Add(500 * time.Millisecond), SpeakerID: "player-1", AgentID: "npc-bart", TurnID: id})
	}

	turn("T1", start)
	earlyBarge("T1", start)
	if got := m.clearedIDs(); len(got) != 0 {
		t.Fatalf("first early cut: cleared = %v, want nothing", got)
	}
	turn("T2", start.Add(3*time.Second))
	earlyBarge("T2", start.Add(3*time.Second))
	if got := m.clearedIDs(); len(got) != 1 {
		t.Fatalf("second early cut in a row: cleared = %v, want [npc-bart]", got)
	}

	// A completed reply restores the exception.
	turn("T3", start.Add(10*time.Second))
	h.Bus.Publish(voiceevent.TurnEnded{TurnID: "T3", Reason: voiceevent.TurnEndCompleted})
	turn("T4", start.Add(20*time.Second))
	earlyBarge("T4", start.Add(20*time.Second))
	if got := m.clearedIDs(); len(got) != 1 {
		t.Fatalf("early cut after a completed reply: cleared = %v, want still just the one", got)
	}
}

// TestAddressDetector_SplitUtteranceContinuesToTheSameNPC is the live failure
// end to end, through the real scoring matcher: the player pauses mid-request,
// Bart starts answering, the player's own continuation cuts him off half a
// second in — and the rest of the request, which names nobody, still reaches
// Bart. The control proves a later cut by the same player still silences him.
func TestAddressDetector_SplitUtteranceContinuesToTheSameNPC(t *testing.T) {
	for _, tc := range []struct {
		name       string
		bargeAfter time.Duration
		wantRoutes int
	}{
		{"cut half a second in", 500 * time.Millisecond, 2},
		{"cut five seconds in", 5 * time.Second, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, clk := replyRig(t)

			h.Bus.Publish(voiceevent.STTFinal{Text: "Bart, erzähl mir eine sehr lange Geschichte über dein Heimatdorf.", TurnID: "T-1", SpeakerID: "alice"})
			clk.advance(time.Second)
			audio := clk.now()
			h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T-1", At: audio})
			clk.advance(tc.bargeAfter)
			h.Bus.Publish(voiceevent.BargeDetected{At: audio.Add(tc.bargeAfter), SpeakerID: "alice", AgentID: bartTarget.AgentID, TurnID: "T-1"})
			h.Bus.Publish(voiceevent.TurnEnded{TurnID: "T-1", Reason: voiceevent.TurnEndBarge})

			clk.advance(3 * time.Second)
			h.Bus.Publish(voiceevent.STTFinal{Text: "Mindestens zwei Minuten lang, mit vielen Einzelheiten.", TurnID: "T-2", SpeakerID: "alice"})

			voicetest.AssertEventCount[voiceevent.AddressRouted](t, h, tc.wantRoutes)
			if tc.wantRoutes == 2 {
				voicetest.AssertEvent(t, h,
					func(e voiceevent.AddressRouted) bool {
						return e.TurnID == "T-2" && e.Target.AgentID == bartTarget.AgentID
					},
					"address.routed → Bart for the rest of the split request",
				)
			}
		})
	}
}
