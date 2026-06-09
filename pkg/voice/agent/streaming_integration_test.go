package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/address"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/wire"
)

// chunkSynth is a [tts.Synthesizer] that emits one non-empty audio chunk per
// sentence, so the TeeSynthesizer publishes a FirstAudio for each. (closedChanSynth
// emits nothing → no FirstAudio, so it can't exercise the hook.)
type chunkSynth struct{}

func (chunkSynth) Synthesize(ctx context.Context, _ tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk, 1)
	ch <- tts.AudioChunk{PCM: []byte{1, 1}, SampleRate: 24000, Channels: 1}
	close(ch)
	return ch, nil
}
func (chunkSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

// recRecognizer is a [stt.Recognizer] returning a fixed transcript, to drive one
// turn through the real STT stage (which mints the TurnID).
type recRecognizer struct{ text string }

func (r recRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{Text: r.text}, nil
}

// TestStreamingIntegration_TurnIDAndFirstAudioChain is the cross-team contract
// test (the property observe's #10 subscriber keys ResponseLatency + per-sentence
// TTFB on): a MULTI-sentence streamed reply, driven through the REAL Conversation
// with WithReplyStream and a real TeeSynthesizer→bus, must produce one TTSInvoked
// and one FirstAudio per sentence, ALL sharing the turn's single TurnID, the first
// FirstAudio being the turn's first sentence, interleaved in arrival order
// (TTSInvoked0, FirstAudio0, TTSInvoked1, ...). This is what makes the headline
// ResponseLatency (first FirstAudio per TurnID) and the arrival-order TTFB pairing
// correct under B1.
func TestStreamingIntegration_TurnIDAndFirstAudioChain(t *testing.T) {
	bus := voiceevent.NewBus()

	// Record the bus events in arrival order. FirstAudio publishes from the tee's
	// forward goroutine (concurrent with the polling below), so guard the slices
	// with a mutex — the same discipline observe's subscriber uses.
	var mu sync.Mutex
	var events []voiceevent.Event
	var firstAudioTurnIDs, ttsTurnIDs []string
	bus.Subscribe(func(e voiceevent.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
		switch ev := e.(type) {
		case voiceevent.FirstAudio:
			firstAudioTurnIDs = append(firstAudioTurnIDs, ev.TurnID)
		case voiceevent.TTSInvoked:
			ttsTurnIDs = append(ttsTurnIDs, ev.TurnID)
		}
	})
	snapshot := func() (int, int) {
		mu.Lock()
		defer mu.Unlock()
		return len(ttsTurnIDs), len(firstAudioTurnIDs)
	}

	// Real STT stage (mints the TurnID) + real TTS stage wrapping a TeeSynthesizer
	// that publishes FirstAudio on the same bus.
	sttStage := orchestrator.NewSTT(bus, recRecognizer{text: "rooms?"})
	tee := wire.NewTeeSynthesizer(chunkSynth{}, wire.PlaybackSinkFunc(func(_ context.Context, chunks <-chan tts.AudioChunk) {
		go func() {
			for range chunks {
			}
		}()
	}), bus)
	ttsStage := orchestrator.NewTTS(bus, tee)

	detector := orchestrator.NewAddressDetector(
		address.NewMatcher(address.Config{Language: "en"},
			address.Agent{Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}}),
	)

	// Streaming replier: a three-sentence reply.
	eng := &fakeStreamEngine{deltas: []string{"One. ", "Two. ", "Three."}}
	replier := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})

	conv := orchestrator.NewConversation(bus, dummyVAD(t), sttStage, ttsStage,
		orchestrator.WithDetector(detector),
		orchestrator.WithReplyStream(replier.ReplyStream()),
		orchestrator.WithBargeIn(0),
		orchestrator.WithErrorHandler(func(err error) { t.Errorf("dispatch: %v", err) }),
	)
	t.Cleanup(conv.Register(t.Context()))

	// Drive one turn through STT directly (the VAD/segmenter path is covered
	// elsewhere; here we exercise the reply→tts→tee chain).
	if err := sttStage.Transcribe(context.Background(), nil); err != nil {
		t.Fatalf("Transcribe: %v", err)
	}

	// The turn dispatches on the floor goroutine; wait for all three FirstAudio.
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, fa := snapshot()
		if fa >= 3 || !time.Now().Before(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The turn is done — take the lock once and read the final slices.
	mu.Lock()
	defer mu.Unlock()

	if len(ttsTurnIDs) != 3 {
		t.Fatalf("got %d TTSInvoked, want 3 (one per sentence): events=%v", len(ttsTurnIDs), eventNames(events))
	}
	if len(firstAudioTurnIDs) != 3 {
		t.Fatalf("got %d FirstAudio, want 3 (one per sentence): events=%v", len(firstAudioTurnIDs), eventNames(events))
	}

	// All TTSInvoked and FirstAudio share ONE non-empty TurnID (the turn's).
	turnID := ttsTurnIDs[0]
	if turnID == "" {
		t.Fatal("TTSInvoked carried an empty TurnID")
	}
	for i, id := range ttsTurnIDs {
		if id != turnID {
			t.Errorf("TTSInvoked[%d].TurnID = %q, want the single turn id %q", i, id, turnID)
		}
	}
	for i, id := range firstAudioTurnIDs {
		if id != turnID {
			t.Errorf("FirstAudio[%d].TurnID = %q, want the single turn id %q", i, id, turnID)
		}
	}

	// Arrival order within the turn: TTSInvoked_i immediately precedes FirstAudio_i
	// (serial dispatch — each sentence fully synthesizes before the next), so the
	// per-sentence TTFB pairing observe does by arrival order is well-defined.
	assertTTSFirstAudioInterleave(t, events)
}

// eventNames renders the event stream's wire names for diagnostics.
func eventNames(events []voiceevent.Event) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.EventName()
	}
	return out
}

// assertTTSFirstAudioInterleave checks that, filtering to tts.invoked and
// voice.first_audio events, they alternate tts,first,tts,first,... — the serial
// per-sentence ordering the TTFB pairing relies on.
func assertTTSFirstAudioInterleave(t *testing.T, events []voiceevent.Event) {
	t.Helper()
	var seq []string
	for _, e := range events {
		switch e.(type) {
		case voiceevent.TTSInvoked:
			seq = append(seq, "tts")
		case voiceevent.FirstAudio:
			seq = append(seq, "first")
		}
	}
	for i := 0; i < len(seq); i += 2 {
		if seq[i] != "tts" || i+1 >= len(seq) || seq[i+1] != "first" {
			t.Errorf("tts/first-audio interleave = %v, want repeated [tts first] pairs (serial dispatch)", seq)
			return
		}
	}
}

// dummyVAD builds a no-op VAD stage for the Conversation (unused here — the turn
// is driven through STT directly). Its session reports silence for every frame.
func dummyVAD(t *testing.T) *orchestrator.VAD {
	t.Helper()
	return orchestrator.NewVAD(voiceevent.NewBus(), silentVADSession{})
}

// silentVADSession is a [vad.SessionHandle] that reports silence for every frame.
type silentVADSession struct{}

func (silentVADSession) ProcessFrame(audio.Frame) (vad.VADEvent, error) {
	return vad.VADEvent{Type: vad.VADSilence}, nil
}
func (silentVADSession) Reset()       {}
func (silentVADSession) Close() error { return nil }
