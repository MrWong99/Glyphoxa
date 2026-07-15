package orchestrator_test

// The option interaction matrix moved from Register-time panics into grouped,
// construction-validated sub-configs (#453): the reply mode is one choice
// ([orchestrator.ReplyStrategy]), everything riding the barge-in floor is one
// group ([orchestrator.Barge]), and NewConversation is the single validation
// point. These tests pin one descriptive error per representable rule; the
// rules the groups make unrepresentable (ensemble-without-barge,
// mute/gate-without-floor) need no runtime check at all.

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// mustConversation unwraps NewConversation for the tests whose option set is
// valid by construction, keeping their call sites one expression. A panic here
// means the test's own options tripped the #453 validation — a test bug, so
// failing loudly is right.
func mustConversation(c *orchestrator.Conversation, err error) *orchestrator.Conversation {
	if err != nil {
		panic("NewConversation: " + err.Error())
	}
	return c
}

func validateStages(t *testing.T) (*voicetest.Harness, *orchestrator.VAD, *orchestrator.STT, *orchestrator.TTS) {
	t.Helper()
	h := voicetest.New(t)
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{})
	sttStage := orchestrator.NewSTT(h.Bus, &recordingRecognizer{})
	ttsStage := orchestrator.NewTTS(h.Bus, selectiveSynth{})
	return h, vadStage, sttStage, ttsStage
}

func nopStream(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error {
	return nil
}

func nopWhole(context.Context, voiceevent.AddressRouted) []orchestrator.Reply { return nil }

// wantConstructionError asserts NewConversation rejected the combination with a
// descriptive error mentioning every needle — the #453 contract: caught at the
// single validation point, never a mid-Register panic.
func wantConstructionError(t *testing.T, err error, needles ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("NewConversation accepted an invalid option combination, want a construction error")
	}
	for _, n := range needles {
		if !strings.Contains(err.Error(), n) {
			t.Errorf("error %q does not name %q — the rule must be self-describing", err, n)
		}
	}
}

// TestNewConversation_BothReplyModesRejected pins the mutually-exclusive reply
// modes rule: ReplyStrategy.Whole and .Stream cannot both be set.
func TestNewConversation_BothReplyModesRejected(t *testing.T) {
	h, vadStage, sttStage, ttsStage := validateStages(t)
	_, err := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithReply(orchestrator.ReplyStrategy{Whole: nopWhole, Stream: nopStream}),
	)
	wantConstructionError(t, err, "mutually exclusive")
}

// TestNewConversation_ReplyRequiresTTS pins that either reply mode without a
// TTS stage fails at construction (formerly a Register panic).
func TestNewConversation_ReplyRequiresTTS(t *testing.T) {
	h, vadStage, sttStage, _ := validateStages(t)
	for name, strategy := range map[string]orchestrator.ReplyStrategy{
		"whole":  {Whole: nopWhole},
		"stream": {Stream: nopStream},
	} {
		_, err := orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
			orchestrator.WithReply(strategy),
		)
		if err == nil {
			t.Fatalf("%s reply mode with nil TTS stage was accepted", name)
		}
		wantConstructionError(t, err, "TTS stage")
	}
}

// TestNewConversation_DirectSpeechRequiresTTS pins the /say TTS dependency at
// construction (formerly a Register panic).
func TestNewConversation_DirectSpeechRequiresTTS(t *testing.T) {
	h, vadStage, sttStage, _ := validateStages(t)
	_, err := orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil,
		orchestrator.WithDirectSpeech(voiceOf("bart", bartVoice())),
	)
	wantConstructionError(t, err, "WithDirectSpeech", "TTS stage")
}

// TestNewConversation_BargeRequiresReplyStrategy pins that a barge group
// without a reply strategy is a loud construction error instead of the silent
// no-op it used to be: the floor and everything the group carries (mutes, gate,
// ensemble, look-ahead) wire through the replier, so without one none of it can
// take effect.
func TestNewConversation_BargeRequiresReplyStrategy(t *testing.T) {
	h, vadStage, sttStage, ttsStage := validateStages(t)
	_, err := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithBargeIn(orchestrator.Barge{}),
	)
	wantConstructionError(t, err, "WithBargeIn", "reply strategy")
}

// TestNewConversation_LookaheadRequiresEnsemble pins the look-ahead rule: only
// the ensemble Cross-talk Reaction consumes the pump, so wiring it without an
// ensemble speaker is a construction error rather than a silent no-op.
func TestNewConversation_LookaheadRequiresEnsemble(t *testing.T) {
	h, vadStage, sttStage, ttsStage := validateStages(t)
	_, err := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithReply(orchestrator.ReplyStrategy{Stream: nopStream}),
		orchestrator.WithBargeIn(orchestrator.Barge{Lookahead: fakePump{}}),
	)
	wantConstructionError(t, err, "Lookahead", "Ensemble")
}

// TestConversation_BargeMutes_BindsMuteCut pins the mute-binding rule
// behaviorally: Barge.Mutes always gets the MuteCut reactor (the floor exists
// whenever the group does), so muting the Agent that is SPEAKING cuts its turn
// with the distinct mute reason. Outside a barge group the mute view is
// unrepresentable — the binding condition is the group itself.
func TestConversation_BargeMutes_BindsMuteCut(t *testing.T) {
	h, vadStage, sttStage, ttsStage := validateStages(t)
	conv := mustConversation(orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithReply(orchestrator.ReplyStrategy{Stream: nopStream}),
		orchestrator.WithBargeIn(orchestrator.Barge{Mutes: muteSet{}}),
	))
	t.Cleanup(conv.Register(t.Context()))

	// Hold the floor as bart, then publish a mute for bart: the MuteCut reactor
	// bound by the group must yield the floor and end the turn with TurnEndMute.
	floor := conv.Floor()
	if floor == nil {
		t.Fatal("Register did not build the barge floor")
	}
	parent := voiceevent.WithTurnID(context.Background(), "T-mute")
	turnCtx, release, _ := floor.Take(parent, "bart")
	defer release()
	h.Bus.Publish(voiceevent.MuteChanged{AgentID: "bart", Muted: true})

	voicetest.AssertEvent(t, h, func(e voiceevent.TurnEnded) bool {
		return e.TurnID == "T-mute" && e.Reason == voiceevent.TurnEndMute
	}, "turn.ended (mute) from the group-bound MuteCut")
	if turnCtx.Err() == nil {
		t.Fatal("the muted speaker's floor ctx was not cancelled")
	}
}

// fakePump is a no-op LookaheadPump for the validation tests.
type fakePump struct{}

func (fakePump) ReleaseLookahead(string) {}
func (fakePump) DiscardLookahead(string) {}
