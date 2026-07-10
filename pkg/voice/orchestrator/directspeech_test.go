package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// recordSynth records each Synthesize request (sentence + voice + the ctx it ran
// under) and returns a closed, empty channel — the "spoke cleanly" path.
type recordSynth struct {
	mu    sync.Mutex
	reqs  []tts.SynthesizeRequest
	ctxs  []context.Context
	block chan struct{} // when non-nil, Synthesize blocks on it (or ctx cancel)
}

func (s *recordSynth) Synthesize(ctx context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.ctxs = append(s.ctxs, ctx)
	block := s.block
	s.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
		}
	}
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (*recordSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func (s *recordSynth) requests() []tts.SynthesizeRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]tts.SynthesizeRequest(nil), s.reqs...)
}

// sayTarget is the Character-NPC target a /say request carries.
func sayTarget(id, name string) voiceevent.AddressTarget {
	return voiceevent.AddressTarget{AgentID: id, AgentRole: voiceevent.AgentRoleCharacter, Name: name}
}

// bartVoice is a distinct Voice the lookup returns, so a test proves the reactor
// synthesized with the LOOKED-UP voice, not a default.
func bartVoice() tts.Voice {
	return tts.Voice{ProviderID: "elevenlabs", VoiceID: "bart-vid", Name: "Bart"}
}

func voiceOf(id string, v tts.Voice) orchestrator.VoiceLookup {
	return func(agentID string) (tts.Voice, bool) {
		if agentID == id {
			return v, true
		}
		return tts.Voice{}, false
	}
}

// TestDirectSpeech_SpeaksWithLookedUpVoice pins the /say happy path (#295): a
// SpeakRequested runs one turn on the floor and dispatches the verbatim text to TTS
// in the Agent's looked-up Voice, publishing TTSInvoked carrying the request's
// TurnID (so the transcript projection correlates exactly as an LLM turn).
func TestDirectSpeech_SpeaksWithLookedUpVoice(t *testing.T) {
	h := voicetest.New(t)
	synth := &recordSynth{}
	stage := orchestrator.NewTTS(h.Bus, synth)
	floor := orchestrator.NewFloor()

	ds := orchestrator.NewDirectSpeech(stage, voiceOf("bart", bartVoice()), nil)
	ds.SetFloor(floor)
	t.Cleanup(ds.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.SpeakRequested{At: time.Now(), TurnID: "Ts", Target: sayTarget("bart", "Bart"), Text: "Welcome, travelers."})

	// The turn runs on a goroutine under the floor; wait for the dispatch.
	waitFor(t, func() bool { return len(synth.requests()) == 1 })

	reqs := synth.requests()
	if reqs[0].Sentence != "Welcome, travelers." {
		t.Errorf("Synthesize sentence = %q, want the verbatim /say text", reqs[0].Sentence)
	}
	if reqs[0].Voice.VoiceID != "bart-vid" {
		t.Errorf("Synthesize voice = %+v, want the looked-up Bart voice", reqs[0].Voice)
	}
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TTSInvoked) bool { return e.TurnID == "Ts" && e.Sentence == "Welcome, travelers." },
		"tts.invoked carrying the /say TurnID",
	)
	// It must NOT wake the LLM Replier — no AddressRouted is ever published (ADR-0024).
	voicetest.AssertNoEvent[voiceevent.AddressRouted](t, h)
}

// TestDirectSpeech_BargeCancelsSay pins ADR-0027: a barge (the shared floor being
// yielded) cancels the in-flight /say playback — the dispatch ctx is cancelled and
// Synthesize unwinds.
func TestDirectSpeech_BargeCancelsSay(t *testing.T) {
	h := voicetest.New(t)
	synth := &recordSynth{block: make(chan struct{})}
	stage := orchestrator.NewTTS(h.Bus, synth)
	floor := orchestrator.NewFloor()

	ds := orchestrator.NewDirectSpeech(stage, voiceOf("bart", bartVoice()), nil)
	ds.SetFloor(floor)
	t.Cleanup(ds.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.SpeakRequested{At: time.Now(), TurnID: "Ts", Target: sayTarget("bart", "Bart"), Text: "A long tale…"})
	waitFor(t, func() bool { return len(synth.requests()) == 1 }) // dispatch in flight on the floor

	// A human barges: the floor is yielded, which cancels the /say turn ctx.
	if _, yielded := floor.Yield(); !yielded {
		t.Fatal("the /say turn must hold the shared floor so a barge can cut it")
	}
	// Synthesize was blocking on ctx; the cancel must release it.
	synth.mu.Lock()
	ctx := synth.ctxs[0]
	synth.mu.Unlock()
	waitFor(t, func() bool { return ctx.Err() != nil })
}

// TestDirectSpeech_SpendCapRefusesTurn pins the spend gate (#130): once the soft cap
// is crossed the reactor refuses the /say turn before any Dispatch and announces a
// spend_cap TurnEnded — the same policy stop the LLM replier applies.
func TestDirectSpeech_SpendCapRefusesTurn(t *testing.T) {
	h := voicetest.New(t)
	synth := &recordSynth{}
	stage := orchestrator.NewTTS(h.Bus, synth)
	floor := orchestrator.NewFloor()

	ds := orchestrator.NewDirectSpeech(stage, voiceOf("bart", bartVoice()), nil)
	ds.SetFloor(floor)
	ds.SetGate(denyGate{})
	t.Cleanup(ds.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.SpeakRequested{At: time.Now(), TurnID: "Ts", Target: sayTarget("bart", "Bart"), Text: "Blocked."})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool { return e.TurnID == "Ts" && e.Reason == voiceevent.TurnEndSpendCap },
		"turn.ended (spend_cap) for the refused /say",
	)
	if len(synth.requests()) != 0 {
		t.Fatalf("a spend-capped /say dispatched %d times, want 0", len(synth.requests()))
	}
	if floor.Active() {
		t.Fatal("a refused /say must not take the floor")
	}
}

// TestDirectSpeech_UnknownVoiceEndsTurn pins the voiceOf miss: an Agent with no
// resolvable Voice ends the turn with an error reason (never a panic) and never
// dispatches.
func TestDirectSpeech_UnknownVoiceEndsTurn(t *testing.T) {
	h := voicetest.New(t)
	synth := &recordSynth{}
	stage := orchestrator.NewTTS(h.Bus, synth)
	floor := orchestrator.NewFloor()

	ds := orchestrator.NewDirectSpeech(stage, voiceOf("bart", bartVoice()), nil)
	ds.SetFloor(floor)
	t.Cleanup(ds.Bind(t.Context(), h.Bus))

	h.Bus.Publish(voiceevent.SpeakRequested{At: time.Now(), TurnID: "Ts", Target: sayTarget("ghost", "Ghost"), Text: "Boo."})

	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool {
			return e.TurnID == "Ts" && e.Reason == voiceevent.TurnEndProviderError
		},
		"turn.ended (provider_error) for the unknown-voice /say",
	)
	if len(synth.requests()) != 0 {
		t.Fatalf("an unknown-voice /say dispatched %d times, want 0", len(synth.requests()))
	}
}

// TestDirectSpeech_MuteBypassed pins the GM-override semantics (#295): a target that
// is MUTED for the LLM reply path still speaks a /say — the DirectSpeech reactor has
// no mute gate, by design. Wired through the full Conversation (WithMute in effect)
// so the pin is behavioral, not just structural.
func TestDirectSpeech_MuteBypassed(t *testing.T) {
	h := voicetest.New(t)
	synth := &recordSynth{}
	vadStage := orchestrator.NewVAD(h.Bus, &scriptedVAD{})
	sttStage := orchestrator.NewSTT(h.Bus, &recordingRecognizer{})
	ttsStage := orchestrator.NewTTS(h.Bus, synth)

	muted := muteSet{"bart": true} // Bart is muted for the LLM/Replier path
	conv := orchestrator.NewConversation(h.Bus, vadStage, sttStage, ttsStage,
		orchestrator.WithReplyStream(func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error { return nil }),
		orchestrator.WithBargeInCoalesce(10*time.Millisecond, 0),
		orchestrator.WithMute(muted),
		orchestrator.WithDirectSpeech(voiceOf("bart", bartVoice())),
	)
	t.Cleanup(conv.Register(t.Context()))

	h.Bus.Publish(voiceevent.SpeakRequested{At: time.Now(), TurnID: "Ts", Target: sayTarget("bart", "Bart"), Text: "I still speak."})

	waitFor(t, func() bool { return len(synth.requests()) == 1 })
	if got := synth.requests()[0].Sentence; got != "I still speak." {
		t.Fatalf("muted /say synthesized %q, want the verbatim text — mute must be bypassed", got)
	}
}

// denyGate is a TurnGate that always refuses a new turn (the crossed soft cap).
type denyGate struct{}

func (denyGate) AllowTurn() bool { return false }

// waitFor polls cond up to ~2s, failing the test if it never holds — the standard
// wait for a turn that runs on the floor's goroutine.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
