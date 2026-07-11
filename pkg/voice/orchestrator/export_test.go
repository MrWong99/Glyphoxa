package orchestrator

import "time"

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
// Conversation.Register from [WithMute].
func (r *Replier) SetMutes(v MuteView) { r.mutes = v }

// SetGate installs the live turn gate on the replier for the spend-cap gate tests
// (external test package, #130). Production wiring sets r.gate inside
// Conversation.Register from [WithTurnGate].
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
// Conversation.Register from [WithTurnGate].
func (d *DirectSpeech) SetGate(g TurnGate) { d.gate = g }

// SetEnsemble installs the Ensemble Turn speaker on the replier for the ensemble
// coordinator tests (external test package, #301). Production wiring sets
// r.ensemble inside Conversation.Register from [WithEnsemble].
func (r *Replier) SetEnsemble(s EnsembleSpeaker) { r.ensemble = s }

// SetLookahead installs the pump look-ahead seam on the replier for the Cross-talk
// Reaction pre-render tests (external test package, #375). Production wiring sets
// r.lookahead inside Conversation.Register from [WithReactionLookahead].
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
