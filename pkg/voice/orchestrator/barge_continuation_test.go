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

// TestAddressDetector_EarlyBargeJudgesTheLatestTurn pins that the exception is
// measured against the reply that was actually cut: an old turn whose audio
// began long ago must not be mistaken for it, and a newer turn opened by
// another speaker must not inherit the first speaker's grace.
func TestAddressDetector_EarlyBargeJudgesTheLatestTurn(t *testing.T) {
	h := voicetest.New(t)
	m := routingClearSpy()
	t.Cleanup(orchestrator.NewAddressDetector(m).Bind(t.Context(), h.Bus))

	start := time.Now()
	h.Bus.Publish(voiceevent.STTFinal{TurnID: "T1", SpeakerID: "player-1", Text: "Bart, hello"})
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T1", At: start})
	h.Bus.Publish(voiceevent.STTFinal{TurnID: "T2", SpeakerID: "player-2", Text: "Bart, and me?"})
	h.Bus.Publish(voiceevent.FirstAudio{TurnID: "T2", At: start.Add(10 * time.Second)})
	// player-1 cuts the answer to player-2 half a second in: an interruption.
	h.Bus.Publish(voiceevent.BargeDetected{At: start.Add(10*time.Second + 500*time.Millisecond), SpeakerID: "player-1", AgentID: "npc-bart"})

	if got := m.clearedIDs(); len(got) != 1 {
		t.Fatalf("cleared = %v, want [npc-bart]: the cut reply answered another speaker", got)
	}
}
