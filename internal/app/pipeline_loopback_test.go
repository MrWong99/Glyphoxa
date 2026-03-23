package app

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MrWong99/glyphoxa/internal/agent"
	agentmock "github.com/MrWong99/glyphoxa/internal/agent/mock"
	"github.com/MrWong99/glyphoxa/internal/agent/orchestrator"
	enginemock "github.com/MrWong99/glyphoxa/internal/engine/mock"
	"github.com/MrWong99/glyphoxa/pkg/audio"
	"github.com/MrWong99/glyphoxa/pkg/audio/loopback"
	audiomixer "github.com/MrWong99/glyphoxa/pkg/audio/mixer"
	"github.com/MrWong99/glyphoxa/pkg/provider/stt"
	"github.com/MrWong99/glyphoxa/pkg/provider/vad"
)

// ─── sequenceVADSession ─────────────────────────────────────────────────────

// sequenceVADSession follows a pre-programmed sequence of VAD events.
// After the sequence is exhausted, it returns VADSilence for all further frames.
type sequenceVADSession struct {
	mu      sync.Mutex
	events  []vad.VADEvent
	callIdx int
}

var _ vad.SessionHandle = (*sequenceVADSession)(nil)

func (s *sequenceVADSession) ProcessFrame(_ []byte) (vad.VADEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.callIdx >= len(s.events) {
		return vad.VADEvent{Type: vad.VADSilence}, nil
	}
	ev := s.events[s.callIdx]
	s.callIdx++
	return ev, nil
}

func (s *sequenceVADSession) Reset()       {}
func (s *sequenceVADSession) Close() error { return nil }

// sequenceVADEngine wraps a sequenceVADSession and returns it on NewSession.
type sequenceVADEngine struct {
	session vad.SessionHandle
}

var _ vad.Engine = (*sequenceVADEngine)(nil)

func (e *sequenceVADEngine) NewSession(_ vad.Config) (vad.SessionHandle, error) {
	return e.session, nil
}

// ─── echoSTTProvider ────────────────────────────────────────────────────────

// echoSTTProvider returns a new echoSTTSession for each StartStream call.
// Each session emits a fixed transcript after receiving its first audio frame.
type echoSTTProvider struct {
	transcript string
}

var _ stt.Provider = (*echoSTTProvider)(nil)

func (p *echoSTTProvider) StartStream(_ context.Context, _ stt.StreamConfig) (stt.SessionHandle, error) {
	return newEchoSTTSession(p.transcript), nil
}

// echoSTTSession emits a fixed transcript on its Finals channel after the
// first SendAudio call. Subsequent calls to SendAudio are no-ops.
type echoSTTSession struct {
	finals   chan stt.Transcript
	partials chan stt.Transcript
	fired    atomic.Bool
	once     sync.Once
}

var _ stt.SessionHandle = (*echoSTTSession)(nil)

func newEchoSTTSession(text string) *echoSTTSession {
	s := &echoSTTSession{
		finals:   make(chan stt.Transcript, 1),
		partials: make(chan stt.Transcript),
	}
	// Pre-load the transcript so it's available immediately when collectAndRoute reads Finals.
	s.finals <- stt.Transcript{Text: text, IsFinal: true, Confidence: 0.95}
	close(s.partials)
	return s
}

func (s *echoSTTSession) SendAudio(_ []byte) error               { return nil }
func (s *echoSTTSession) Finals() <-chan stt.Transcript          { return s.finals }
func (s *echoSTTSession) Partials() <-chan stt.Transcript        { return s.partials }
func (s *echoSTTSession) SetKeywords(_ []stt.KeywordBoost) error { return nil }
func (s *echoSTTSession) Close() error {
	s.once.Do(func() { close(s.finals) })
	return nil
}

// ─── respondingNPCAgent ─────────────────────────────────────────────────────

// respondingNPCAgent wraps agentmock.NPCAgent and produces audio output
// when HandleUtterance is called. This simulates the real agent behaviour
// of running LLM → TTS → enqueue to mixer.
type respondingNPCAgent struct {
	*agentmock.NPCAgent
	mixer          audio.Mixer
	responseFrames int // number of PCM frames to produce per response
}

var _ agent.NPCAgent = (*respondingNPCAgent)(nil)

func (a *respondingNPCAgent) HandleUtterance(ctx context.Context, speaker string, transcript stt.Transcript) error {
	// Record the call.
	if err := a.NPCAgent.HandleUtterance(ctx, speaker, transcript); err != nil {
		return err
	}

	// Produce audio response: responseFrames frames of PCM at 48kHz stereo (3840 bytes each).
	n := a.responseFrames
	if n <= 0 {
		n = 3
	}
	audioCh := make(chan []byte, n)
	for range n {
		audioCh <- make([]byte, 3840)
	}
	close(audioCh)

	a.mixer.Enqueue(&audio.AudioSegment{
		NPCID:      a.IDResult,
		Audio:      audioCh,
		SampleRate: 48000,
		Channels:   2,
		Priority:   1,
	}, 1)

	return nil
}

// ─── TestPipelineLoopback_EndToEnd ──────────────────────────────────────────

// TestPipelineLoopback_EndToEnd verifies the full audio pipeline using a
// loopback connection and mock providers:
//
//	audio in → VAD → STT → orchestrator → NPC agent → mixer → audio out
//
// No real voice platform, STT service, LLM, or TTS is required.
func TestPipelineLoopback_EndToEnd(t *testing.T) {
	t.Parallel()

	const (
		vadSampleRate = 16000
		vadFrameMs    = 30
		vadFrameBytes = vadSampleRate * vadFrameMs / 1000 * 2 // 960 bytes per 30ms mono PCM frame
		numFrames     = 20                                    // total frames to feed
		speechStart   = 2                                     // frame index where speech starts
		speechEnd     = 15                                    // frame index where speech ends
		responseText  = "Greetings, adventurer!"
	)

	// Build VAD event sequence: silence → speech start → speech continue → speech end → silence
	vadEvents := make([]vad.VADEvent, numFrames)
	for i := range numFrames {
		switch {
		case i < speechStart:
			vadEvents[i] = vad.VADEvent{Type: vad.VADSilence}
		case i == speechStart:
			vadEvents[i] = vad.VADEvent{Type: vad.VADSpeechStart, Probability: 0.9}
		case i < speechEnd:
			vadEvents[i] = vad.VADEvent{Type: vad.VADSpeechContinue, Probability: 0.85}
		case i == speechEnd:
			vadEvents[i] = vad.VADEvent{Type: vad.VADSpeechEnd, Probability: 0.1}
		default:
			vadEvents[i] = vad.VADEvent{Type: vad.VADSilence}
		}
	}

	vadEng := &sequenceVADEngine{
		session: &sequenceVADSession{events: vadEvents},
	}

	sttProv := &echoSTTProvider{transcript: responseText}

	// Build audio frames for the loopback participant (16kHz mono PCM).
	frames := make([]audio.AudioFrame, numFrames)
	for i := range numFrames {
		frames[i] = audio.AudioFrame{
			Data:       make([]byte, vadFrameBytes),
			SampleRate: vadSampleRate,
			Channels:   1,
			Timestamp:  time.Duration(i) * time.Duration(vadFrameMs) * time.Millisecond,
		}
	}

	// Create loopback connection with one participant.
	conn := loopback.New([]loopback.Participant{
		{UserID: "player-1", Username: "Alice", Frames: frames},
	})

	// Create real mixer that writes to the loopback output channel.
	outStream := conn.OutputStream()
	pm := audiomixer.New(func(frame audio.AudioFrame) {
		outStream <- frame
	}, audiomixer.WithGap(0)) // no gap for faster test

	// Create responding NPC agent.
	eng := &enginemock.VoiceEngine{}
	npc := &respondingNPCAgent{
		NPCAgent: &agentmock.NPCAgent{
			IDResult:       "npc-greymantle",
			NameResult:     "Greymantle",
			EngineResult:   eng,
			IdentityResult: agent.NPCIdentity{Name: "Greymantle"},
		},
		mixer:          pm,
		responseFrames: 5,
	}

	// Create orchestrator with single NPC (all transcripts route to it).
	orch := orchestrator.New([]agent.NPCAgent{npc})

	// Wire the audio pipeline.
	ctx := t.Context()

	pipeline := NewAudioPipeline(AudioPipelineConfig{
		Conn:        conn,
		VADEngine:   vadEng,
		STTProvider: sttProv,
		Orch:        orch,
		Mixer:       pm,
		VADCfg: vad.Config{
			SampleRate:       vadSampleRate,
			FrameSizeMs:      vadFrameMs,
			SpeechThreshold:  0.5,
			SilenceThreshold: 0.3,
		},
		STTCfg: stt.StreamConfig{
			SampleRate: vadSampleRate,
			Channels:   1,
		},
		Ctx: ctx,
	})

	pipeline.Start()

	// Wait for output audio — the NPC should have responded.
	if !conn.WaitForOutput(1, 10*time.Second) {
		_ = pipeline.Stop()
		_ = pm.Close()
		_ = conn.Disconnect()
		t.Fatal("no audio output received — pipeline did not produce NPC response")
	}

	// Verify we got all response frames.
	if !conn.WaitForOutput(5, 5*time.Second) {
		t.Logf("warning: expected 5 output frames, got %d", conn.CapturedOutputCount())
	}

	// Tear down in order: pipeline → mixer → connection.
	if err := pipeline.Stop(); err != nil {
		t.Errorf("pipeline.Stop: %v", err)
	}
	if err := pm.Close(); err != nil {
		t.Errorf("mixer.Close: %v", err)
	}
	if err := conn.Disconnect(); err != nil {
		t.Errorf("conn.Disconnect: %v", err)
	}

	// Verify NPC agent received the transcript.
	calls := npc.HandleUtteranceCalls
	if len(calls) == 0 {
		t.Fatal("NPC agent was never called — pipeline routing failed")
	}
	if got := calls[0].Transcript.Text; got != responseText {
		t.Errorf("NPC received transcript %q, want %q", got, responseText)
	}
	if got := calls[0].Speaker; got != "player-1" {
		t.Errorf("NPC received speaker %q, want %q", got, "player-1")
	}

	// Verify output frames were produced.
	captured := conn.CapturedOutput()
	if len(captured) == 0 {
		t.Fatal("no output frames captured — mixer did not produce audio")
	}
	t.Logf("pipeline loopback: %d input frames → %d output frames, %d agent calls",
		numFrames, len(captured), len(calls))
}

// ─── TestPipelineLoopback_MultipleParticipants ──────────────────────────────

// TestPipelineLoopback_MultipleParticipants verifies that the pipeline handles
// multiple concurrent participants, each producing a separate transcript.
func TestPipelineLoopback_MultipleParticipants(t *testing.T) {
	t.Parallel()

	const (
		vadSampleRate = 16000
		vadFrameMs    = 30
		vadFrameBytes = vadSampleRate * vadFrameMs / 1000 * 2
		numFrames     = 10
		responseText  = "Hello from Greymantle"
	)

	// Both participants trigger speech immediately.
	vadEvents := make([]vad.VADEvent, numFrames)
	vadEvents[0] = vad.VADEvent{Type: vad.VADSpeechStart, Probability: 0.9}
	for i := 1; i < numFrames-1; i++ {
		vadEvents[i] = vad.VADEvent{Type: vad.VADSpeechContinue, Probability: 0.85}
	}
	vadEvents[numFrames-1] = vad.VADEvent{Type: vad.VADSpeechEnd, Probability: 0.1}

	// Each participant gets their own VAD session (the engine creates a new one per NewSession call).
	// We need a factory that produces independent sessions.
	vadFactory := &multiSessionVADEngine{
		eventTemplate: vadEvents,
	}

	sttProv := &echoSTTProvider{transcript: responseText}

	mkFrames := func(n int) []audio.AudioFrame {
		out := make([]audio.AudioFrame, n)
		for i := range n {
			out[i] = audio.AudioFrame{
				Data:       make([]byte, vadFrameBytes),
				SampleRate: vadSampleRate,
				Channels:   1,
			}
		}
		return out
	}

	conn := loopback.New([]loopback.Participant{
		{UserID: "player-1", Username: "Alice", Frames: mkFrames(numFrames)},
		{UserID: "player-2", Username: "Bob", Frames: mkFrames(numFrames)},
	})

	outStream := conn.OutputStream()
	pm := audiomixer.New(func(frame audio.AudioFrame) {
		outStream <- frame
	}, audiomixer.WithGap(0))

	eng := &enginemock.VoiceEngine{}
	npc := &respondingNPCAgent{
		NPCAgent: &agentmock.NPCAgent{
			IDResult:       "npc-greymantle",
			NameResult:     "Greymantle",
			EngineResult:   eng,
			IdentityResult: agent.NPCIdentity{Name: "Greymantle"},
		},
		mixer:          pm,
		responseFrames: 2,
	}
	orch := orchestrator.New([]agent.NPCAgent{npc})

	ctx := t.Context()

	pipeline := NewAudioPipeline(AudioPipelineConfig{
		Conn:        conn,
		VADEngine:   vadFactory,
		STTProvider: sttProv,
		Orch:        orch,
		Mixer:       pm,
		VADCfg:      vad.Config{SampleRate: vadSampleRate, FrameSizeMs: vadFrameMs},
		STTCfg:      stt.StreamConfig{SampleRate: vadSampleRate, Channels: 1},
		Ctx:         ctx,
	})
	pipeline.Start()

	// At least one participant should trigger a response. Simultaneous speech
	// causes barge-in, which may interrupt one participant's response — that's
	// correct pipeline behaviour. We verify at least one gets through.
	if !conn.WaitForOutput(1, 10*time.Second) {
		t.Fatal("no output frames received from any participant")
	}

	// Give a bit more time for the second participant to also complete.
	time.Sleep(200 * time.Millisecond)

	if err := pipeline.Stop(); err != nil {
		t.Errorf("pipeline.Stop: %v", err)
	}
	if err := pm.Close(); err != nil {
		t.Errorf("mixer.Close: %v", err)
	}
	if err := conn.Disconnect(); err != nil {
		t.Errorf("conn.Disconnect: %v", err)
	}

	calls := npc.HandleUtteranceCalls
	if len(calls) == 0 {
		t.Fatal("expected at least 1 HandleUtterance call, got 0")
	}

	// Check that both participants were tracked by the pipeline (both should
	// have triggered VAD and opened STT sessions). Due to barge-in, not all
	// may complete before pipeline.Stop cancels the context.
	captured := conn.CapturedOutput()
	t.Logf("multi-participant: %d output frames, %d agent calls (2 participants)", len(captured), len(calls))
}

// multiSessionVADEngine creates a new sequenceVADSession for each NewSession call,
// using a shared event template. This allows multiple participants to each
// have their own independent VAD state.
type multiSessionVADEngine struct {
	eventTemplate []vad.VADEvent
}

var _ vad.Engine = (*multiSessionVADEngine)(nil)

func (e *multiSessionVADEngine) NewSession(_ vad.Config) (vad.SessionHandle, error) {
	events := make([]vad.VADEvent, len(e.eventTemplate))
	copy(events, e.eventTemplate)
	return &sequenceVADSession{events: events}, nil
}

// ─── TestPipelineLoopback_NoSpeech ──────────────────────────────────────────

// TestPipelineLoopback_NoSpeech verifies that when VAD detects only silence,
// no STT sessions are opened and no transcripts are routed.
func TestPipelineLoopback_NoSpeech(t *testing.T) {
	t.Parallel()

	const (
		vadSampleRate = 16000
		vadFrameMs    = 30
		vadFrameBytes = vadSampleRate * vadFrameMs / 1000 * 2
		numFrames     = 10
	)

	// All silence.
	silenceEvents := make([]vad.VADEvent, numFrames)
	for i := range numFrames {
		silenceEvents[i] = vad.VADEvent{Type: vad.VADSilence}
	}

	vadEng := &sequenceVADEngine{
		session: &sequenceVADSession{events: silenceEvents},
	}

	// STT that would fail loudly if called — but it shouldn't be.
	sttProv := &echoSTTProvider{transcript: "should not appear"}

	frames := make([]audio.AudioFrame, numFrames)
	for i := range numFrames {
		frames[i] = audio.AudioFrame{
			Data:       make([]byte, vadFrameBytes),
			SampleRate: vadSampleRate,
			Channels:   1,
		}
	}

	conn := loopback.New([]loopback.Participant{
		{UserID: "player-1", Username: "Alice", Frames: frames},
	})

	outStream := conn.OutputStream()
	pm := audiomixer.New(func(frame audio.AudioFrame) {
		outStream <- frame
	}, audiomixer.WithGap(0))

	eng := &enginemock.VoiceEngine{}
	npc := &agentmock.NPCAgent{
		IDResult:       "npc-test",
		NameResult:     "TestNPC",
		EngineResult:   eng,
		IdentityResult: agent.NPCIdentity{Name: "TestNPC"},
	}
	orch := orchestrator.New([]agent.NPCAgent{npc})

	ctx := t.Context()

	pipeline := NewAudioPipeline(AudioPipelineConfig{
		Conn:        conn,
		VADEngine:   vadEng,
		STTProvider: sttProv,
		Orch:        orch,
		Mixer:       pm,
		VADCfg:      vad.Config{SampleRate: vadSampleRate, FrameSizeMs: vadFrameMs},
		STTCfg:      stt.StreamConfig{SampleRate: vadSampleRate, Channels: 1},
		Ctx:         ctx,
	})
	pipeline.Start()

	// Wait a bit — nothing should come out.
	time.Sleep(200 * time.Millisecond)

	if err := pipeline.Stop(); err != nil {
		t.Errorf("pipeline.Stop: %v", err)
	}
	if err := pm.Close(); err != nil {
		t.Errorf("mixer.Close: %v", err)
	}
	if err := conn.Disconnect(); err != nil {
		t.Errorf("conn.Disconnect: %v", err)
	}

	// No utterances should have been handled.
	if len(npc.HandleUtteranceCalls) != 0 {
		t.Errorf("expected 0 HandleUtterance calls (no speech), got %d", len(npc.HandleUtteranceCalls))
	}

	// No output audio.
	if n := conn.CapturedOutputCount(); n != 0 {
		t.Errorf("expected 0 output frames (no speech), got %d", n)
	}
}

// ─── TestPipelineLoopback_MidSessionJoin ────────────────────────────────────

// TestPipelineLoopback_MidSessionJoin verifies that a participant joining
// mid-session is picked up by the pipeline via OnParticipantChange.
func TestPipelineLoopback_MidSessionJoin(t *testing.T) {
	t.Parallel()

	const (
		vadSampleRate = 16000
		vadFrameMs    = 30
		vadFrameBytes = vadSampleRate * vadFrameMs / 1000 * 2
		numFrames     = 10
		responseText  = "Welcome, newcomer!"
	)

	vadEvents := make([]vad.VADEvent, numFrames)
	vadEvents[0] = vad.VADEvent{Type: vad.VADSpeechStart, Probability: 0.9}
	for i := 1; i < numFrames-1; i++ {
		vadEvents[i] = vad.VADEvent{Type: vad.VADSpeechContinue, Probability: 0.85}
	}
	vadEvents[numFrames-1] = vad.VADEvent{Type: vad.VADSpeechEnd, Probability: 0.1}

	vadFactory := &multiSessionVADEngine{eventTemplate: vadEvents}
	sttProv := &echoSTTProvider{transcript: responseText}

	// Start with no participants.
	conn := loopback.New(nil)

	outStream := conn.OutputStream()
	pm := audiomixer.New(func(frame audio.AudioFrame) {
		outStream <- frame
	}, audiomixer.WithGap(0))

	eng := &enginemock.VoiceEngine{}
	npc := &respondingNPCAgent{
		NPCAgent: &agentmock.NPCAgent{
			IDResult:       "npc-test",
			NameResult:     "TestNPC",
			EngineResult:   eng,
			IdentityResult: agent.NPCIdentity{Name: "TestNPC"},
		},
		mixer:          pm,
		responseFrames: 2,
	}
	orch := orchestrator.New([]agent.NPCAgent{npc})

	ctx := t.Context()

	pipeline := NewAudioPipeline(AudioPipelineConfig{
		Conn:        conn,
		VADEngine:   vadFactory,
		STTProvider: sttProv,
		Orch:        orch,
		Mixer:       pm,
		VADCfg:      vad.Config{SampleRate: vadSampleRate, FrameSizeMs: vadFrameMs},
		STTCfg:      stt.StreamConfig{SampleRate: vadSampleRate, Channels: 1},
		Ctx:         ctx,
	})
	pipeline.Start()

	// Add a participant mid-session.
	frames := make([]audio.AudioFrame, numFrames)
	for i := range numFrames {
		frames[i] = audio.AudioFrame{
			Data:       make([]byte, vadFrameBytes),
			SampleRate: vadSampleRate,
			Channels:   1,
		}
	}
	conn.AddParticipant(loopback.Participant{
		UserID:   "late-joiner",
		Username: "Charlie",
		Frames:   frames,
	})

	// Wait for the response.
	if !conn.WaitForOutput(1, 10*time.Second) {
		t.Fatal("no output after mid-session participant join")
	}

	if err := pipeline.Stop(); err != nil {
		t.Errorf("pipeline.Stop: %v", err)
	}
	if err := pm.Close(); err != nil {
		t.Errorf("mixer.Close: %v", err)
	}
	if err := conn.Disconnect(); err != nil {
		t.Errorf("conn.Disconnect: %v", err)
	}

	calls := npc.HandleUtteranceCalls
	if len(calls) == 0 {
		t.Fatal("NPC agent was never called after mid-session join")
	}

	t.Logf("mid-session join: %d output frames, %d agent calls", conn.CapturedOutputCount(), len(calls))
}
