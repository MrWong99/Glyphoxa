package orchestrator_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// fakeEnsemble is a gated [orchestrator.EnsembleSpeaker] for the coordinator tests:
// each candidate's Draft blocks on a per-Agent release gate (nil gate = immediate),
// so a test controls the RACE ORDER deterministically without sleeps or
// replay-speed dependence (ADR-0021). Speak dispatches the draft (optionally split
// across a pause gate so a barge can land mid-turn) and records which Agents spoke.
type fakeEnsemble struct {
	draft     map[string]string        // agentID -> draft text Draft returns
	draftErr  map[string]error         // agentID -> Draft error (optional)
	gate      map[string]chan struct{} // agentID -> Draft release gate; nil/absent = immediate
	ignoreCtx map[string]bool          // agentID -> Draft waits ONLY on the gate (returns success even if ctx already cancelled), modelling a Draft that completed the same instant the turn was cancelled

	started   chan string // agentID pushed when a Draft begins (optional)
	cancelled chan string // agentID pushed when a Draft's ctx was cancelled (optional)

	speakSentences map[string][]string // agentID -> sentences to dispatch (default: [draft])
	speakPause     chan struct{}       // if set, Speak blocks after dispatching sentence[0]
	spokeCh        chan string         // agentID pushed when Speak returns (optional)

	mu        sync.Mutex
	spoke     []string
	delivered map[string]string // agentID -> text Speak committed (deliver-then-commit)
	speakers  map[string]string // agentID -> the SpeakerID the reconstructed route carried
}

// recordSpeaker stores the SpeakerID the coordinator's reconstructed
// [voiceevent.AddressRouted] carried for id, so a test can pin the SpeakerID
// propagation from the EnsembleRouted onto every per-candidate route.
func (s *fakeEnsemble) recordSpeaker(id, speakerID string) {
	s.mu.Lock()
	if s.speakers == nil {
		s.speakers = map[string]string{}
	}
	s.speakers[id] = speakerID
	s.mu.Unlock()
}

func (s *fakeEnsemble) speakerFor(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.speakers[id]
}

// speakerRecorded reports the recorded SpeakerID and whether Draft/Speak was
// invoked for id at all — a candidate cancelled before its draft goroutine ran
// records nothing, which is legitimate coordinator behavior, not a propagation
// failure.
func (s *fakeEnsemble) speakerRecorded(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	got, ok := s.speakers[id]
	return got, ok
}

func (s *fakeEnsemble) Draft(ctx context.Context, e voiceevent.AddressRouted) (string, error) {
	id := e.Target.AgentID
	s.recordSpeaker(id, e.SpeakerID)
	if s.started != nil {
		s.started <- id
	}
	if g := s.gate[id]; g != nil {
		if s.ignoreCtx[id] {
			<-g // wait only on the gate: this Draft "already finished" when the turn was cut
		} else {
			select {
			case <-g:
			case <-ctx.Done():
				if s.cancelled != nil {
					s.cancelled <- id
				}
				return "", ctx.Err()
			}
		}
	}
	if err := s.draftErr[id]; err != nil {
		return "", err
	}
	return s.draft[id], nil
}

func (s *fakeEnsemble) Speak(ctx context.Context, e voiceevent.AddressRouted, draft string, dispatch func(orchestrator.Reply) error) (string, error) {
	id := e.Target.AgentID
	s.recordSpeaker(id, e.SpeakerID)
	s.mu.Lock()
	s.spoke = append(s.spoke, id)
	s.mu.Unlock()
	if s.spokeCh != nil {
		defer func() { s.spokeCh <- id }()
	}

	sentences := []string{draft}
	if s.speakSentences != nil {
		sentences = s.speakSentences[id]
	}
	var delivered strings.Builder
	for i, snt := range sentences {
		if err := dispatch(orchestrator.Reply{Sentence: snt, Voice: tts.Voice{VoiceID: id, Name: id}}); err != nil {
			// Mirror the real SpeakDraft (ADR-0012, #362): a start-error
			// (ErrNotDelivered) skips this sentence but keeps draining later ones under
			// a live turn; any other error (a barge/mute cancel) stops the drain.
			if errors.Is(err, orchestrator.ErrNotDelivered) {
				continue
			}
			return s.recordDelivered(id, delivered.String()), nil // barge: stop the drain, delivered-only
		}
		if delivered.Len() > 0 {
			delivered.WriteByte(' ')
		}
		delivered.WriteString(snt)
		if s.speakPause != nil && i == 0 {
			select {
			case <-s.speakPause:
			case <-ctx.Done():
				return s.recordDelivered(id, delivered.String()), nil
			}
		}
	}
	return s.recordDelivered(id, delivered.String()), nil
}

// recordDelivered stores what Speak committed for id (the deliver-then-commit
// result) so a test can assert an all-synth-failed Lead commits nothing (#362).
func (s *fakeEnsemble) recordDelivered(id, text string) string {
	s.mu.Lock()
	if s.delivered == nil {
		s.delivered = map[string]string{}
	}
	s.delivered[id] = text
	s.mu.Unlock()
	return text
}

func (s *fakeEnsemble) deliveredFor(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.delivered[id]
}

func (s *fakeEnsemble) spokeIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.spoke...)
}

// closedGate is an already-released Draft gate (the candidate drafts immediately).
func closedGate() chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// ensembleReplier builds a floor-backed [orchestrator.Replier] wired with an
// ensemble speaker — the coordinator under test. The stream reply strategy is a
// never-called stub (only EnsembleRouted is published in these tests).
func ensembleReplier(h *voicetest.Harness, floor *orchestrator.Floor, spk orchestrator.EnsembleSpeaker) *orchestrator.Replier {
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})
	replier := orchestrator.NewStreamReplier(ttsStage, func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error {
		return nil
	}, nil)
	replier.SetFloor(floor)
	replier.SetEnsemble(spk)
	return replier
}

// TestReplier_Ensemble_FastestDraftLeadsAndSpeaks pins the Lead race skips
// non-answers (ADR-0025, #301): Bart has a real draft, Goblin says nothing (""), so
// Bart is elected Lead — announced via EnsembleLead and the only Agent that speaks
// (exactly its TTSInvoked, one line). The speed-beats-score property (a slower
// Targets[1] still winning over a hung Targets[0]) is pinned separately in
// TestReplier_Ensemble_SlowerLowerScoredCandidateStillLeads.
func TestReplier_Ensemble_FastestDraftLeadsAndSpeaks(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeEnsemble{
		draft:   map[string]string{bartTarget.AgentID: "Bart speaks." /* goblin: no entry → empty draft, skipped */},
		gate:    map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: closedGate()},
		spokeCh: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T7", Text: "Bart, Mira — thoughts?", SpeakerID: "spk-ens", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// The turn completes when the Lead's Speak returns.
	select {
	case who := <-spk.spokeCh:
		if who != bartTarget.AgentID {
			t.Fatalf("Speak ran for %q, want the elected Lead Bart", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ensemble never elected a Lead / spoke")
	}

	voicetest.AssertEvent(t, h, func(e voiceevent.EnsembleLead) bool {
		return e.TurnID == "T7" && e.Target.AgentID == bartTarget.AgentID
	}, "ensemble.lead → Bart")
	if got := spk.spokeIDs(); len(got) != 1 || got[0] != bartTarget.AgentID {
		t.Fatalf("spoke = %v, want only Bart (the empty-draft candidate must not speak)", got)
	}
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
	voicetest.AssertEvent(t, h, func(e voiceevent.TTSInvoked) bool { return e.Sentence == "Bart speaks." && e.TurnID == "T7" }, "one tts.invoked for Bart's line")
	// Clean turn: no TurnEnded of any reason.
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
	// SpeakerID propagation (the transcript-names seam): every reconstructed
	// per-candidate route — the Drafts and the Lead's Speak — carries the
	// EnsembleRouted's SpeakerID.
	if got := spk.speakerFor(bartTarget.AgentID); got != "spk-ens" {
		t.Errorf("Lead's route SpeakerID = %q, want spk-ens", got)
	}
	// The loser's draft may be cancelled before its goroutine ever invokes
	// Draft (the winner completes instantly here) — assert the SpeakerID only
	// when the draft actually ran; a wrong ID is a failure, absence is not.
	if got, ok := spk.speakerRecorded(goblinTarget.AgentID); ok && got != "spk-ens" {
		t.Errorf("losing candidate's Draft SpeakerID = %q, want spk-ens", got)
	}
}

// TestReplier_Ensemble_SlowerLowerScoredCandidateStillLeads is the AC1 headline
// (ADR-0025, #301): the FIRST complete non-empty draft wins the floor REGARDLESS of
// score order. Targets[0] (the top-scored coalesce anchor, Bart) is gated FOREVER;
// the lower-scored Targets[1] (Goblin) completes → Goblin is elected Lead and is the
// one that speaks. A coordinator that skipped the race and just waited on Targets[0]
// would deadlock here.
func TestReplier_Ensemble_SlowerLowerScoredCandidateStillLeads(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeEnsemble{
		draft: map[string]string{bartTarget.AgentID: "Bart (too slow).", goblinTarget.AgentID: "Goblin wins."},
		gate: map[string]chan struct{}{
			bartTarget.AgentID:   make(chan struct{}), // Targets[0]: NEVER released (hung)
			goblinTarget.AgentID: closedGate(),        // Targets[1]: completes immediately
		},
		cancelled: make(chan string, 2),
		spokeCh:   make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T-race", Text: "Bart, Goblin — go!", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	select {
	case who := <-spk.spokeCh:
		if who != goblinTarget.AgentID {
			t.Fatalf("Speak ran for %q, want the faster lower-scored Goblin (Targets[1])", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the ensemble never elected the faster candidate (did the coordinator wait on Targets[0]?)")
	}

	voicetest.AssertEvent(t, h, func(e voiceevent.EnsembleLead) bool {
		return e.TurnID == "T-race" && e.Target.AgentID == goblinTarget.AgentID
	}, "ensemble.lead → Goblin (Targets[1], first complete draft)")
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
	voicetest.AssertEvent(t, h, func(e voiceevent.TTSInvoked) bool { return e.Sentence == "Goblin wins." && e.TurnID == "T-race" }, "Goblin's line on the wire")
	if got := spk.spokeIDs(); len(got) != 1 || got[0] != goblinTarget.AgentID {
		t.Fatalf("spoke = %v, want only Goblin", got)
	}
	// The hung Targets[0] draft was cancelled by the election (leak-proof).
	select {
	case who := <-spk.cancelled:
		if who != bartTarget.AgentID {
			t.Fatalf("cancelled draft = %q, want the hung Bart", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the hung Targets[0] draft was never cancelled after the election")
	}
}

// TestReplier_Ensemble_WinAfterCancelElectsNoLead pins the race-closure re-check
// (#301): if a candidate's draft completes the SAME instant the turn is cancelled
// (buffered result AND turnCtx.Done() both ready), no Lead may be elected — otherwise
// EnsembleLead would publish after a TurnEnded and SpeakDraft would commit a user
// message for a turn nothing spoke in. The winner's Draft ignores ctx (it "already
// finished"); the turn is cancelled BEFORE its gate opens, so its success lands
// post-cancel.
func TestReplier_Ensemble_WinAfterCancelElectsNoLead(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	bartGate := make(chan struct{})
	spk := &fakeEnsemble{
		draft:     map[string]string{bartTarget.AgentID: "Bart speaks." /* goblin: empty, skipped */},
		gate:      map[string]chan struct{}{bartTarget.AgentID: bartGate},
		ignoreCtx: map[string]bool{bartTarget.AgentID: true},
		started:   make(chan string, 2),
		spokeCh:   make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tc", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// Wait until Bart's draft is in flight (blocked on its gate).
	waitStarted(t, spk, bartTarget.AgentID)
	// Cancel the turn (a barge/mute would do this via the floor), THEN let Bart's
	// draft complete successfully — its winning result now arrives post-cancel.
	floor.Yield()
	close(bartGate)

	// The re-check must swallow the post-cancel win: no Lead, nobody speaks.
	select {
	case <-time.After(300 * time.Millisecond):
	case who := <-spk.spokeCh:
		t.Fatalf("a cancelled ensemble must not elect a Lead; %q spoke", who)
	}
	voicetest.AssertNoEvent[voiceevent.EnsembleLead](t, h)
	if len(spk.spokeIDs()) != 0 {
		t.Fatalf("nobody may speak after a cancel; spoke = %v", spk.spokeIDs())
	}
}

// waitStarted drains the fake's started channel until agentID's Draft has begun.
func waitStarted(t *testing.T, s *fakeEnsemble, agentID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case id := <-s.started:
			if id == agentID {
				return
			}
		case <-deadline:
			t.Fatalf("Draft for %q never started", agentID)
		}
	}
}

// TestReplier_Ensemble_LoserDraftDiscarded pins ADR-0012's zero-commit rule at the
// coordinator: once the Lead is elected, the losing candidate's shared draft ctx is
// cancelled — its Draft returns via the cancel, it never speaks, and it produces no
// TTSInvoked and no TurnEnded of its own.
func TestReplier_Ensemble_LoserDraftDiscarded(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeEnsemble{
		draft:     map[string]string{bartTarget.AgentID: "Bart speaks."},
		gate:      map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
		cancelled: make(chan string, 2),
		spokeCh:   make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T8", Text: "Bart, Mira — thoughts?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// Winner speaks.
	<-spk.spokeCh
	// Loser's Draft was cancelled by the winner's election.
	select {
	case who := <-spk.cancelled:
		if who != goblinTarget.AgentID {
			t.Fatalf("cancelled draft was %q, want the loser Goblin", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the losing draft was never cancelled after the Lead was elected")
	}
	if got := spk.spokeIDs(); len(got) != 1 || got[0] != bartTarget.AgentID {
		t.Fatalf("spoke = %v, want only the Lead Bart", got)
	}
	// Exactly one TTSInvoked (Bart's), none from the loser.
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
}

// TestReplier_Ensemble_BargeDuringLeadTearsDownWholeUnit pins ADR-0027: a human
// barge while the Lead is audibly speaking cancels the ENTIRE ensemble turn — the
// floor is yielded, TurnEnded{barge} is announced for the ensemble's TurnID, and
// the Lead's Speak unwinds (delivered-only). The barge gate fires only once the Lead
// is audible: FirstOpus marks it speaking, then a VADSpeechStart barges.
func TestReplier_Ensemble_BargeDuringLeadTearsDownWholeUnit(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeEnsemble{
		draft:          map[string]string{bartTarget.AgentID: "First. Second."},
		gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: make(chan struct{})},
		cancelled:      make(chan string, 2),
		speakSentences: map[string][]string{bartTarget.AgentID: {"First.", "Second."}},
		speakPause:     make(chan struct{}), // never released: the Lead pauses after sentence 1 until the barge cancels ctx
		spokeCh:        make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))
	// The barge reactor on the SAME floor: a speech_start while the Lead is audible
	// yields the floor.
	t.Cleanup(orchestrator.NewBargeIn(floor, 0).Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T9", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// Wait until the Lead has dispatched its first sentence (it is now paused).
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "T9" && e.Sentence == "First."
	}, "the Lead's first sentence is on the wire")

	// The Lead is audible; a human barges.
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "T9"})
	h.Bus.Publish(voiceevent.VADSpeechStart{})

	// The whole unit is torn down: barge TurnEnded for the ensemble's TurnID.
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T9" && e.Reason == voiceevent.TurnEndBarge
	}, "turn.ended barge for the whole ensemble")

	// The Lead's Speak unwound and returned.
	select {
	case <-spk.spokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the Lead's Speak never returned after the barge")
	}
	// Only the first sentence reached the wire (the second was cut).
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
	// The loser's draft was cancelled at election, never at barge.
	select {
	case who := <-spk.cancelled:
		if who != goblinTarget.AgentID {
			t.Fatalf("cancelled draft = %q, want the loser Goblin", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the losing draft was never cancelled")
	}
	if floor.Active() {
		t.Fatal("the floor must be free after the barge")
	}
}

// TestReplier_Ensemble_AllDraftsEmptyEndsProviderError pins that when every
// candidate draft fails or is empty, no Lead is elected: the turn ends
// provider_error and the floor is released.
func TestReplier_Ensemble_AllDraftsEmptyEndsProviderError(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeEnsemble{
		draft:    map[string]string{bartTarget.AgentID: "" /* says nothing */},
		draftErr: map[string]error{goblinTarget.AgentID: context.DeadlineExceeded},
		gate:     map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: closedGate()},
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T10", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T10" && e.Reason == voiceevent.TurnEndProviderError
	}, "turn.ended provider_error when every draft is empty/failed")
	if len(spk.spokeIDs()) != 0 {
		t.Fatalf("nobody must speak when all drafts are empty; spoke = %v", spk.spokeIDs())
	}
	voicetest.AssertEventCount[voiceevent.EnsembleLead](t, h, 0)
	// The floor was released (no holder).
	if floor.Active() {
		t.Fatal("the floor must be released after an all-empty ensemble")
	}
}

// TestEnsemble_LeadAllSynthFailed_CommitsNothing pins the ensemble residual
// (#362): when EVERY one of the Lead's sentences start-fails synthesis under a
// live turn, the Lead commits NOTHING — each start-error now returns
// ErrNotDelivered, so Speak skips the sentence rather than committing an
// undelivered line (the old behavior, where an all-synth-failed Lead still
// committed its text, is gone). The turn ends tts_error.
func TestEnsemble_LeadAllSynthFailed_CommitsNothing(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	// Every sentence the Lead dispatches start-fails synthesis.
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{failOn: map[string]bool{"boom-a": true, "boom-b": true}})
	spk := &fakeEnsemble{
		draft:          map[string]string{bartTarget.AgentID: "boom-a" /* goblin empty → skipped */},
		gate:           map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: closedGate()},
		speakSentences: map[string][]string{bartTarget.AgentID: {"boom-a", "boom-b"}},
		spokeCh:        make(chan string, 2),
	}
	replier := orchestrator.NewStreamReplier(ttsStage, func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error {
		return nil
	}, nil)
	replier.SetFloor(floor)
	replier.SetEnsemble(spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T362", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	select {
	case <-spk.spokeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("the Lead never spoke")
	}
	if got := spk.deliveredFor(bartTarget.AgentID); got != "" {
		t.Fatalf("Lead committed %q, want nothing (every sentence start-failed → ErrNotDelivered)", got)
	}
	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T362" && e.Reason == voiceevent.TurnEndTTSError
	}, "turn.ended tts_error for an all-synth-failed Lead")
}

// TestReplier_Ensemble_BothCandidatesMuted_NeverTakesFloor pins the mute pre-filter
// (#211): when every candidate is muted the ensemble opens no turn — TurnEnded mute,
// the floor is never taken, and no draft runs.
func TestReplier_Ensemble_BothCandidatesMuted_NeverTakesFloor(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeEnsemble{
		draft:   map[string]string{bartTarget.AgentID: "x", goblinTarget.AgentID: "y"},
		started: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	replier.SetMutes(muteSet{bartTarget.AgentID: true, goblinTarget.AgentID: true})
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T11", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.AssertEvent(t, h, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T11" && e.Reason == voiceevent.TurnEndMute
	}, "turn.ended mute when every candidate is muted")
	if floor.Active() {
		t.Fatal("the floor must never be taken when every candidate is muted")
	}
	if len(spk.spokeIDs()) != 0 {
		t.Fatal("no candidate may draft/speak when all are muted")
	}
}

// TestReplier_Ensemble_NoEnsembleSpeakerDegradesToTopScored pins the degrade path:
// an EnsembleRouted with no wired [orchestrator.EnsembleSpeaker] falls back to the
// single-route reply path for the top-scored target (Targets[0]).
func TestReplier_Ensemble_NoEnsembleSpeakerDegradesToTopScored(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})
	var routedFor, routedSpeaker string
	replier := orchestrator.NewStreamReplier(ttsStage, func(_ context.Context, e voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		routedFor = e.Target.AgentID
		routedSpeaker = e.SpeakerID
		return dispatch(orchestrator.Reply{Sentence: "hi"})
	}, nil)
	replier.SetFloor(floor)
	// No SetEnsemble: r.ensemble stays nil → degrade.
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T-deg", Text: "Bart, Mira?", SpeakerID: "spk-deg", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool { return e.TurnID == "T-deg" }, "single-route dispatch on degrade")
	if routedFor != bartTarget.AgentID {
		t.Fatalf("degrade routed to %q, want the top-scored Targets[0] Bart", routedFor)
	}
	if routedSpeaker != "spk-deg" {
		t.Fatalf("degraded route SpeakerID = %q, want spk-deg (copied from the EnsembleRouted)", routedSpeaker)
	}
	voicetest.AssertEventCount[voiceevent.EnsembleLead](t, h, 0)
}

// TestConversation_WithEnsemble_RegistersLeadRace pins the PRODUCTION wiring (#301):
// Barge.Ensemble installs the speaker on the replier built inside Register, sharing the
// barge-in floor, so an EnsembleRouted runs the speculative Lead race end-to-end.
func TestConversation_WithEnsemble_RegistersLeadRace(t *testing.T) {
	h := voicetest.New(t)
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{})
	sttStage := orchestrator.NewSTT(h.Bus, &recordingRecognizer{})
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})

	spk := &fakeEnsemble{
		draft:   map[string]string{bartTarget.AgentID: "Bart wins."},
		gate:    map[string]chan struct{}{bartTarget.AgentID: closedGate()},
		spokeCh: make(chan string, 2),
	}
	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithReply(orchestrator.ReplyStrategy{Stream: func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error { return nil }}),
		orchestrator.WithBargeIn(orchestrator.Barge{Confirm: 10 * time.Millisecond, Ensemble: spk}),
	))
	t.Cleanup(conv.Register(t.Context()))
	if conv.Floor() == nil {
		t.Fatal("Register must build the shared barge-in floor for the ensemble")
	}

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tw", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	select {
	case who := <-spk.spokeCh:
		if who != bartTarget.AgentID {
			t.Fatalf("Speak ran for %q, want the elected Lead Bart", who)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the registered ensemble never elected a Lead")
	}
	voicetest.AssertEvent(t, h, func(e voiceevent.EnsembleLead) bool {
		return e.TurnID == "Tw" && e.Target.AgentID == bartTarget.AgentID
	}, "ensemble.lead → Bart via the registered conversation")
}

// Ensemble-without-barge is no longer a Register-time panic: the speaker lives
// on [orchestrator.Barge.Ensemble] (#453), so a Conversation with an ensemble
// but no barge floor is unrepresentable — the compiler enforces what
// TestConversation_WithEnsemble_WithoutBargePanics used to pin.

// countMute is a MuteView that reports NOT muted for the first flipAfter Muted
// calls, then muted — so the pre-Take mute filter passes every candidate but the
// post-Take re-filter (the race-closure) sees them all muted.
type countMute struct {
	mu        sync.Mutex
	calls     int
	flipAfter int
}

func (m *countMute) Muted(string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	return m.calls > m.flipAfter
}

// TestReplier_Ensemble_SingleMutedOfTwo_DegradesToSurvivor pins the mute pre-filter
// collapsing an ensemble to a single route (#211, #301): with one of two candidates
// muted, only the survivor remains, so the turn degrades to the plain single-route
// path for that survivor — no Lead race, no EnsembleLead.
func TestReplier_Ensemble_SingleMutedOfTwo_DegradesToSurvivor(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})
	var routedFor string
	replier := orchestrator.NewStreamReplier(ttsStage, func(_ context.Context, e voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
		routedFor = e.Target.AgentID
		return dispatch(orchestrator.Reply{Sentence: "hi"})
	}, nil)
	replier.SetFloor(floor)
	replier.SetEnsemble(&fakeEnsemble{}) // ensemble wired, but only one candidate survives the mute
	replier.SetMutes(muteSet{bartTarget.AgentID: true})
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tsm", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TTSInvoked) bool { return e.TurnID == "Tsm" }, "single-route dispatch for the surviving candidate")
	if routedFor != goblinTarget.AgentID {
		t.Fatalf("degrade routed to %q, want the un-muted survivor Goblin", routedFor)
	}
	voicetest.AssertEventCount[voiceevent.EnsembleLead](t, h, 0)
}

// TestReplier_Ensemble_PostTakeMuteReFilter_EndsMute pins the race-closure
// (#211, #301): the mute view can flip to muted between the pre-Take filter and the
// Take. When the post-Take re-filter finds every candidate muted, the floor is
// released and the turn ends with the mute reason — before any draft runs.
func TestReplier_Ensemble_PostTakeMuteReFilter_EndsMute(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fakeEnsemble{
		draft:   map[string]string{bartTarget.AgentID: "x", goblinTarget.AgentID: "y"},
		started: make(chan string, 2),
	}
	replier := ensembleReplier(h, floor, spk)
	// flipAfter 2: the two pre-Take checks pass, the two post-Take checks report muted.
	replier.SetMutes(&countMute{flipAfter: 2})
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "Tpm", Text: "Bart, Goblin?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.AssertEvent(t, h, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "Tpm" && e.Reason == voiceevent.TurnEndMute
	}, "turn.ended mute from the post-Take re-filter")
	if floor.Active() {
		t.Fatal("the floor must be released when the post-Take re-filter mutes every candidate")
	}
	voicetest.AssertEventCount[voiceevent.EnsembleLead](t, h, 0)
	if len(spk.spokeIDs()) != 0 {
		t.Fatal("no candidate may draft/speak after the post-Take mute re-filter")
	}
}

// fallbackEnsemble is fakeEnsemble extended with the [orchestrator.FallbackSpeaker]
// seam (#473 review): SpeakFallback dispatches one canned line for the named Agent
// and records who it spoke for — unless the ctx is already cancelled or the test
// models a voiceless persona via refuse, mirroring the real Cast's refusals.
type fallbackEnsemble struct {
	fakeEnsemble
	refuse bool // model the Cast's voiceless-persona refusal

	fbMu        sync.Mutex
	fallbackFor []string
}

func (s *fallbackEnsemble) SpeakFallback(ctx context.Context, agentID string, dispatch func(orchestrator.Reply) error) bool {
	if ctx.Err() != nil || s.refuse {
		return false
	}
	s.fbMu.Lock()
	s.fallbackFor = append(s.fallbackFor, agentID)
	s.fbMu.Unlock()
	_ = dispatch(orchestrator.Reply{Sentence: "canned stall", Voice: tts.Voice{VoiceID: agentID, Name: agentID}})
	return true
}

func (s *fallbackEnsemble) fallbacks() []string {
	s.fbMu.Lock()
	defer s.fbMu.Unlock()
	return append([]string(nil), s.fallbackFor...)
}

// TestReplier_Ensemble_AllDraftsErred_SpeaksTopCandidateFallback pins the #473-review
// fix: when EVERY candidate's Draft fails with an engine error (one provider outage
// killing the whole fan-out) the ensemble no longer ends in dead air while the
// identical single-target failure speaks the canned line — the coordinator dispatches
// the TOP-SCORED candidate's fallback through the optional
// [orchestrator.FallbackSpeaker] seam, then still publishes the provider_error
// terminal (the canned line is not the model's words, and the failure stays visible
// to metrics/relay).
func TestReplier_Ensemble_AllDraftsErred_SpeaksTopCandidateFallback(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fallbackEnsemble{fakeEnsemble: fakeEnsemble{
		draftErr: map[string]error{bartTarget.AgentID: errors.New("groq boom"), goblinTarget.AgentID: errors.New("gemini boom")},
		gate:     map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: closedGate()},
	}}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T-fb", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T-fb" && e.Reason == voiceevent.TurnEndProviderError
	}, "turn.ended provider_error still published after the fallback")
	if got := spk.fallbacks(); len(got) != 1 || got[0] != bartTarget.AgentID {
		t.Fatalf("SpeakFallback ran for %v, want exactly the top-scored Targets[0] Bart", got)
	}
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 1)
	voicetest.AssertEvent(t, h, func(e voiceevent.TTSInvoked) bool {
		return e.TurnID == "T-fb" && e.Sentence == "canned stall"
	}, "the canned line on the wire under the ensemble's TurnID")
	voicetest.AssertEventCount[voiceevent.EnsembleLead](t, h, 0)
	if len(spk.spokeIDs()) != 0 {
		t.Fatalf("no Speak may run when every draft failed; spoke = %v", spk.spokeIDs())
	}
	if floor.Active() {
		t.Fatal("the floor must be released after the fallback")
	}
}

// TestReplier_Ensemble_AllDraftsErred_VoicelessTopCandidate_NoDispatch pins the
// voiceless refusal at the coordinator: the speaker declines (an empty VoiceID
// must never reach TTS) — nothing is dispatched and the provider_error terminal
// is unchanged.
func TestReplier_Ensemble_AllDraftsErred_VoicelessTopCandidate_NoDispatch(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fallbackEnsemble{
		refuse: true,
		fakeEnsemble: fakeEnsemble{
			draftErr: map[string]error{bartTarget.AgentID: errors.New("boom"), goblinTarget.AgentID: errors.New("boom")},
			gate:     map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: closedGate()},
		},
	}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T-fb2", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T-fb2" && e.Reason == voiceevent.TurnEndProviderError
	}, "turn.ended provider_error for the refused fallback")
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 0)
	if got := spk.fallbacks(); len(got) != 0 {
		t.Fatalf("SpeakFallback dispatched for %v, want nothing (voiceless persona)", got)
	}
}

// TestReplier_Ensemble_EmptyDeclinePlusError_NoFallback pins the decline gate: a
// candidate that returned an EMPTY draft under no error chose silence — silence is
// an Agent's answer, so the coordinator speaks no canned line (matching the routed
// path's empty-completion rule) while the provider_error terminal is unchanged.
func TestReplier_Ensemble_EmptyDeclinePlusError_NoFallback(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	spk := &fallbackEnsemble{fakeEnsemble: fakeEnsemble{
		draft:    map[string]string{bartTarget.AgentID: "" /* declines */},
		draftErr: map[string]error{goblinTarget.AgentID: errors.New("boom")},
		gate:     map[string]chan struct{}{bartTarget.AgentID: closedGate(), goblinTarget.AgentID: closedGate()},
	}}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T-fb3", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	voicetest.WaitEvent(t, h, 2*time.Second, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T-fb3" && e.Reason == voiceevent.TurnEndProviderError
	}, "turn.ended provider_error when a decline mixes with an error")
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 0)
	if got := spk.fallbacks(); len(got) != 0 {
		t.Fatalf("SpeakFallback dispatched for %v, want nothing (an Agent declined)", got)
	}
}

// TestReplier_Ensemble_BargeDuringDrafts_NoFallback pins the barge exclusion: a
// barge tearing the unit down mid-race speaks nothing — the fallback honors the
// same post-barge silence as every other coordinator action, and no terminal is
// published by the coordinator (the barge owns its own TurnEnded).
func TestReplier_Ensemble_BargeDuringDrafts_NoFallback(t *testing.T) {
	h := voicetest.New(t)
	floor := orchestrator.NewFloor()
	bartGate := make(chan struct{})
	goblinGate := make(chan struct{})
	spk := &fallbackEnsemble{fakeEnsemble: fakeEnsemble{
		draftErr:  map[string]error{bartTarget.AgentID: errors.New("boom"), goblinTarget.AgentID: errors.New("boom")},
		gate:      map[string]chan struct{}{bartTarget.AgentID: bartGate, goblinTarget.AgentID: goblinGate},
		ignoreCtx: map[string]bool{bartTarget.AgentID: true, goblinTarget.AgentID: true},
		started:   make(chan string, 2),
	}}
	replier := ensembleReplier(h, floor, spk)
	t.Cleanup(replier.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.EnsembleRouted{TurnID: "T-fb4", Text: "Bart, Mira?", Targets: []voiceevent.AddressTarget{bartTarget, goblinTarget}})

	// Both drafts are in flight (blocked on their gates); the barge lands.
	for i := 0; i < 2; i++ {
		select {
		case <-spk.started:
		case <-time.After(2 * time.Second):
			t.Fatal("the drafts never started")
		}
	}
	floor.Yield()
	// The drafts now fail — but their results land AFTER the cancel.
	close(bartGate)
	close(goblinGate)

	// Give the coordinator time to (wrongly) speak; it must not.
	time.Sleep(200 * time.Millisecond)
	voicetest.AssertEventCount[voiceevent.TTSInvoked](t, h, 0)
	if got := spk.fallbacks(); len(got) != 0 {
		t.Fatalf("SpeakFallback dispatched for %v after a barge, want nothing", got)
	}
	voicetest.AssertNoEvent[voiceevent.TurnEnded](t, h)
}
