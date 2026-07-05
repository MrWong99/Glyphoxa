package orchestrator_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// pausingEngine is a scripted [agent.StreamingEngine] that emits deltas in order
// but blocks after the FIRST one until released or the turn ctx is cancelled — so
// a test can cut the turn mid-stream (after one spoken sentence) and observe the
// deliver-then-commit rule (ADR-0012) under a mute exactly as under a barge.
type pausingEngine struct {
	deltas     []string
	firstDone  chan struct{} // closed once the first delta has been pushed
	release    chan struct{} // closed by the test to let the remaining deltas flow
	closeFirst sync.Once
}

func (e *pausingEngine) Generate(context.Context, []llm.Message) (string, error) {
	return strings.Join(e.deltas, ""), nil
}

func (e *pausingEngine) GenerateStream(ctx context.Context, _ []llm.Message, onText func(string) error) (string, error) {
	var full strings.Builder
	for i, d := range e.deltas {
		if err := onText(d); err != nil {
			return full.String(), err
		}
		full.WriteString(d)
		if i == 0 {
			e.closeFirst.Do(func() { close(e.firstDone) })
			select {
			case <-e.release:
			case <-ctx.Done():
				return full.String(), ctx.Err()
			}
		}
	}
	return full.String(), nil
}

// recSynth records every dispatched sentence so a test can assert which sentences
// actually reached the pump (the delivered set, ADR-0012). It returns an
// immediately-closed channel — no audio.
type recSynth struct {
	mu    sync.Mutex
	spoke []string
}

func (s *recSynth) Synthesize(_ context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	s.mu.Lock()
	s.spoke = append(s.spoke, req.Sentence)
	s.mu.Unlock()
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}

func (s *recSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func (s *recSynth) spoken() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.spoke...)
}

// TestMute_ConversationCutsSpeakingTurnLikeBarge is the mute e2e (#211, AC2): a
// real Agent streaming turn is cut mid-sentence by a MuteChanged for the speaking
// Agent — its audio stops, the turn ends with the distinct mute reason (never a
// BargeDetected), and the Agent's history commits ONLY the delivered sentence,
// exactly as the shipped barge path does (deliver-then-commit, ADR-0012). The
// mute reuses the SAME floor-cancel mechanism as a barge, so the commit is
// identical.
func TestMute_ConversationCutsSpeakingTurnLikeBarge(t *testing.T) {
	h := voicetest.New(t)
	synth := &recSynth{}
	ttsStage := orchestrator.NewTTS(h.Bus, synth)

	eng := &pausingEngine{
		deltas:    []string{"First. ", "Second. ", "Third."},
		firstDone: make(chan struct{}),
		release:   make(chan struct{}),
	}
	replier := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: tts.Voice{ProviderID: "test", VoiceID: "bart"}},
		Engine:      eng,
		Synthesizer: synth, // used only for the audio-markup prompt; Dispatch drives the TTS stage
	})
	cast := agent.NewCast(replier)

	floor := orchestrator.NewFloor()
	mutes := muteSet{}
	streamer := orchestrator.NewStreamReplier(ttsStage, cast.ReplyStream(), nil)
	streamer.SetFloor(floor)
	streamer.SetMutes(mutes)
	t.Cleanup(orchestrator.Bind(t.Context(), h.Bus, orchestrator.NewMuteCut(floor), streamer))

	// Drive one turn addressed to Bart.
	h.Bus.Publish(voiceevent.AddressRouted{
		TurnID: "Te2e", Text: "Bart, tell me about the inn",
		Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"},
	})

	// Wait until the first sentence has been dispatched (the Agent is speaking).
	select {
	case <-eng.firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("the Agent never dispatched its first sentence")
	}
	h.Bus.Publish(voiceevent.FirstOpus{TurnID: "Te2e"}) // audible on the wire

	// Mute Bart mid-sentence: the set is written BEFORE the event (Manager ordering).
	mutes["bart"] = true
	h.Bus.Publish(voiceevent.MuteChanged{AgentID: "bart", Muted: true})

	// Let the paused stream unwind (it should observe the cancelled ctx).
	close(eng.release)

	// The turn ended with the mute reason, never a barge.
	voicetest.AssertEvent(t, h,
		func(e voiceevent.TurnEnded) bool { return e.TurnID == "Te2e" && e.Reason == voiceevent.TurnEndMute },
		"turn.ended (mute) for the cut speaking turn",
	)
	voicetest.AssertNoEvent[voiceevent.BargeDetected](t, h)

	// Only the delivered sentence reached the pump — the never-dispatched tail was dropped.
	deadline := time.Now().Add(2 * time.Second)
	for {
		hist := replier.HistorySnapshot()
		if len(hist) >= 2 { // user + committed assistant
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("history never committed the spoken turn: %+v", hist)
		}
		time.Sleep(2 * time.Millisecond)
	}
	if got := synth.spoken(); len(got) != 1 || strings.TrimSpace(got[0]) != "First." {
		t.Fatalf("delivered sentences = %q, want exactly [\"First.\"] (mid-sentence cut drops the tail)", got)
	}
	hist := replier.HistorySnapshot()
	assistant := hist[len(hist)-1]
	if string(assistant.Role) != "assistant" || strings.TrimSpace(assistant.Text) != "First." {
		t.Fatalf("committed assistant turn = {%s %q}, want the delivered sentence only (ADR-0012, identical to barge)", assistant.Role, assistant.Text)
	}
	if strings.Contains(assistant.Text, "Second") || strings.Contains(assistant.Text, "Third") {
		t.Fatalf("committed assistant turn = %q, want the never-dispatched tail dropped", assistant.Text)
	}
}
