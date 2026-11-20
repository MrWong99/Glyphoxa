package orchestrator

import (
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// Yield cancels whatever turn holds the floor, speaking or not, and reports its
// TurnID — a test-only unconditional cut standing in for a barge/mute (production
// cuts through [Floor.YieldTurn] / [Floor.YieldAgent], which guard the holder).
func (f *Floor) Yield() (turnID string, yielded bool) {
	f.mu.Lock()
	c := f.cancel
	turnID = f.holderTurn
	f.cancel = nil
	f.holderTurn = ""
	f.holderAgent = ""
	f.speaking = false
	f.mu.Unlock()
	if c == nil {
		return "", false
	}
	c()
	return turnID, true
}

// Active reports whether a turn currently holds the floor (test-only probe).
func (f *Floor) Active() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancel != nil
}

// BindSupersedeTerminal exposes the floor's supersede-terminal wiring (#443) for
// the floor unit tests (external test package). Production installs it from the
// floor-sharing reactors' Bind.
func (f *Floor) BindSupersedeTerminal(bus *voiceevent.Bus) { f.bindSupersedeTerminal(bus) }

// SetClock overrides the floor's clock for deterministic coalesce-window tests
// (external test package). Production code always uses the real time.Now.
func (f *Floor) SetClock(now func() time.Time) {
	f.mu.Lock()
	f.now = now
	f.mu.Unlock()
}

// SetFloor installs floor on the replier for the barge-in/coalesce wiring tests
// (external test package). Production wiring sets r.floor inside
// Conversation.Register.
func (r *Replier) SetFloor(floor *Floor) { r.floor = floor }

// SetMutes installs the live mute view on the replier for the mute-gate tests
// (external test package, #211). Production wiring sets r.mutes inside
// Conversation.Register from [Barge.Mutes].
func (r *Replier) SetMutes(v MuteView) { r.mutes = v }

// SetGate installs the live turn gate on the replier for the spend-cap gate tests
// (external test package, #130). Production wiring sets r.gate inside
// Conversation.Register from [Barge.Gate].
func (r *Replier) SetGate(g TurnGate) { r.gate = g }

// Floor exposes the Conversation's barge-in floor built inside Register, so a test
// can assert the production wiring actually shares ONE floor across the replier and
// the DirectSpeech reactor (#295). nil before Register, or when barge-in is off.
func (c *Conversation) Floor() *Floor { return c.floor }

// SetFloor installs the shared barge-in floor on the DirectSpeech reactor for the
// /say wiring tests (external test package, #295). Production wiring sets it inside
// Conversation.Register from the same floor the barge path uses.
func (d *DirectSpeech) SetFloor(floor *Floor) { d.floor = floor }

// SetGate installs the live turn gate on the DirectSpeech reactor for the spend-cap
// tests (external test package, #295/#130). Production wiring sets it inside
// Conversation.Register from [Barge.Gate].
func (d *DirectSpeech) SetGate(g TurnGate) { d.gate = g }

// SetLookahead installs the pump look-ahead seam on the replier for the
// pre-synthesis pipeline tests (external test package, #626). Production wiring sets
// r.lookahead inside Conversation.Register from [Barge.Lookahead].
func (r *Replier) SetLookahead(p LookaheadPump) { r.lookahead = p }

// SetErrorHandler installs onError on the segmenter for the off-loop STT error
// tests (external test package). Production wiring sets it inside
// Conversation.Register from [WithErrorHandler].
func (s *Segmenter) SetErrorHandler(fn ErrorFunc) { s.onError = fn }

// SetLaneVADFactory installs the per-Speaker-Lane VAD factory for the lane tests
// (external test package). Production wiring sets it inside NewConversation from
// [WithSpeakerLanes].
func (s *Segmenter) SetLaneVADFactory(f LaneVADFactory) { s.laneVADFactory = f }

// SetLaneStreamFactory installs the per-lane streaming-STT factory and cap for the
// lane tests (external test package). Production wiring sets it from
// [WithLaneStreamingSTT].
func (s *Segmenter) SetLaneStreamFactory(f func(speakerID string) *StreamManager, maxLanes int) {
	s.laneStreamFactory = f
	s.maxStreamLanes = maxLanes
}

// LaneCount reports the number of live lanes (including the default lane), for the
// reap tests (external test package).
func (s *Segmenter) LaneCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lanes)
}

// SetSweepEvery overrides the reap-sweep cadence (Process calls per sweep) so a
// reap test triggers a sweep without 1024 frames (external test package).
func (s *Segmenter) SetSweepEvery(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepEvery = n
}

// WaitStreamUp blocks until the manager has a live session or timeout elapses,
// reporting whether one came up. Test-only: pipeline tests feed the first
// utterance only after the eager dial completes, so utterance 1 streams
// deterministically instead of racing the maintainer.
func (m *StreamManager) WaitStreamUp(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		up := m.stream != nil
		m.mu.Unlock()
		if up {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return false
}

// SetFloor installs the shared barge-in floor on the ClipReplay reactor for the
// Highlight voice-replay tests (external test package, #310). Production wiring
// sets it inside Conversation.Register from the same floor the barge path uses.
func (r *ClipReplay) SetFloor(floor *Floor) { r.floor = floor }
