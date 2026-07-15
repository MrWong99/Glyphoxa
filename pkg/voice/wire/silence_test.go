package wire

// White-box tests for the continuous silence clock that drives VAD endpointing
// during inbound silence and packet gaps (issue #91). They live in `package
// wire` so they can drive the unexported run loop with a test-owned inbound
// channel and an injected, manually-fired silence clock — no Discord Session and
// no wall-clock sleeps (the determinism requirement; see ADR-0019/0020).

import (
	"context"
	"sync"
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// VAD geometry the rig runs at: production's 16 kHz / 32 ms frames (512 samples,
// internal/wirenpc/wirenpc.go). The hangover uses silero's DEFAULT 15-frame
// threshold (the voicetest/vad_test default) rather than wirenpc's tuned 12: at 12
// the hello-test clip splits at an internal pause into two utterances, which would
// confound a single-utterance test. The exact production hangover (12) and its
// natural ~384 ms endpointing are exercised by the live Discord test; these tests
// pin the silence-clock MECHANISM, which is hangover-count agnostic.
const (
	testSampleRate    = 16000
	testFrameMs       = 32
	testHangoverTicks = 20 // > silero's default 15-frame hangover, so silence endpoints

	// discordStopSilenceFrames is the ~5 explicit Opus silence frames a Discord
	// sender emits when the speaker stops, before it stops sending packets — the
	// speaker-stop signal on the wire (disgo's own sender does the same).
	discordStopSilenceFrames = 5
)

// stubRecognizer returns a pinned transcript regardless of input, so a flushed
// utterance produces a deterministic STTFinal with no STT provider or cassette.
type stubRecognizer struct{ text string }

func (s stubRecognizer) Transcribe(context.Context, []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{Text: s.text}, nil
}

// replayCodec is a fake [Codec] that replays a pre-framed clip: each
// DecodeInbound call yields the next PCM frame, modelling the real Opus→PCM
// transcoder one packet at a time. It records its call count so a test can pin
// that Discord silence frames are NOT decoded.
type replayCodec struct {
	mu      sync.Mutex
	frames  []audio.Frame
	next    int
	decodes int
}

func (c *replayCodec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.decodes++
	if c.next >= len(c.frames) {
		return nil, nil
	}
	f := c.frames[c.next]
	c.next++
	return []audio.Frame{f}, nil
}

func (c *replayCodec) PlaybackSource(<-chan tts.AudioChunk) (gxvoice.Source, error) {
	return nil, nil
}

func (c *replayCodec) decodeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.decodes
}

// fakeSilenceClock is a [silenceClock] the test fires by hand: tick() pushes one
// frame-cadence tick, and reset()/stop() are recorded so a test can assert real
// audio resets the idle timer (so the production ticker never fires mid-speech).
type fakeSilenceClock struct {
	ch chan time.Time

	mu      sync.Mutex
	resets  int
	stopped bool
}

func (c *fakeSilenceClock) ticks() <-chan time.Time { return c.ch }
func (c *fakeSilenceClock) reset()                  { c.mu.Lock(); c.resets++; c.mu.Unlock() }
func (c *fakeSilenceClock) stop()                   { c.mu.Lock(); c.stopped = true; c.mu.Unlock() }

// tick fires one silence-clock tick and blocks until the run loop consumes it,
// keeping the test deterministic (each tick is one synthesized silence frame).
func (c *fakeSilenceClock) tick() { c.ch <- time.Now() }

func (c *fakeSilenceClock) resetCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.resets
}

// newSilenceRig builds a Conversation around a REAL silero VAD (production
// geometry + 12-frame hangover) and a stub recognizer, plus the "hello-test"
// clip pre-framed at the VAD chunk size. The conversation publishes onto the
// returned harness's bus; the silero session is closed at end of t.
func newSilenceRig(t *testing.T, recText string) (*orchestrator.Conversation, *voicetest.Harness, []audio.Frame) {
	t.Helper()

	h := voicetest.New(t)

	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}
	sess, err := engine.NewSession(vad.Config{
		SampleRate:       testSampleRate,
		FrameSizeMs:      testFrameMs,
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	})
	if err != nil {
		t.Fatalf("engine.NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	vadStage := orchestrator.NewVAD(h.Bus, sess)
	sttStage := orchestrator.NewSTT(h.Bus, stubRecognizer{text: recText})
	conv, err := orchestrator.NewConversation(h.Bus, vadStage, sttStage, nil)
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}

	clip := voicetest.LoadClip(t, "hello-test")
	chunk := testSampleRate * testFrameMs / 1000
	frames, _ := clip.FramesOf(t, chunk)
	return conv, h, frames
}

// feedSpeech sends one real inbound frame per pre-framed clip frame; each maps,
// through the replayCodec, to one PCM frame fed into the VAD.
func feedSpeech(inbound chan<- gxvoice.Frame, frames []audio.Frame) {
	for range frames {
		inbound <- gxvoice.Frame{}
	}
}

// eventCounts tallies the speech_start / speech_end / stt.final events on the
// harness. The hello-test clip VAD-splits into more than one utterance and leaves
// the LAST one open (speech_start with no matching speech_end, because the clip
// has little trailing silence) — exactly the live shape issue #91 is about. A
// speech_end is emitted ONLY by the VAD on a real silence transition (a Flush
// cannot fake one), so ends == starts is proof that every utterance, including the
// trailing one, was endpointed by audible silence — i.e. by the silence clock.
func eventCounts(h *voicetest.Harness) (starts, ends, finals int) {
	for _, e := range h.Events() {
		switch e.(type) {
		case voiceevent.VADSpeechStart:
			starts++
		case voiceevent.VADSpeechEnd:
			ends++
		case voiceevent.STTFinal:
			finals++
		}
	}
	return starts, ends, finals
}

// TestPipeline_TrailingSilenceEndpointsTrailingUtterance is the tracer bullet
// (acceptance #1): an utterance followed by a genuine speaker stop — Discord's
// explicit silence frames, then NO further packets, the silence driven ONLY by
// the injected clock — endpoints within the hangover window. The clip leaves its
// final utterance open; the silence frames arm the clock (#147) and firing it
// past the hangover must close the utterance (speech_end == speech_start) and
// produce its STTFinal, in speech order. Before the #91 fix the trailing silence
// was dropped, the trailing utterance never endpointed, and its line appeared
// only when the next utterance arrived.
func TestPipeline_TrailingSilenceEndpointsTrailingUtterance(t *testing.T) {
	conv, h, frames := newSilenceRig(t, "hello there")
	codec := &replayCodec{frames: frames}
	clk := &fakeSilenceClock{ch: make(chan time.Time)}
	pipe := NewPipeline(conv, codec, nil, "guild", nil,
		withSilenceClock(testSampleRate, testFrameMs, func() silenceClock { return clk }))

	inbound := make(chan gxvoice.Frame)
	done := make(chan error, 1)
	go func() { done <- pipe.run(t.Context(), inbound) }()

	feedSpeech(inbound, frames)
	// The speaker stops: Discord's explicit silence frames (the speaker-stop
	// signal that arms the clock), then a packet gap — fire the clock past the
	// hangover with NO more inbound audio. Silero must endpoint the open
	// utterance here, not wait for a second one.
	for range discordStopSilenceFrames {
		inbound <- gxvoice.Frame{Silence: true}
	}
	for range testHangoverTicks {
		clk.tick()
	}
	close(inbound)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	starts, ends, finals := eventCounts(h)
	if starts < 1 {
		t.Fatalf("no speech detected: starts=%d", starts)
	}
	if ends != starts {
		t.Errorf("trailing utterance not endpointed: speech_start=%d speech_end=%d (want equal); the silence clock must close the open utterance", starts, ends)
	}
	if finals != ends {
		t.Errorf("STTFinal=%d, want %d (one per endpointed utterance)", finals, ends)
	}
	// The last thing that happened is the clock-driven endpoint and its transcript,
	// in order — not a final waiting on a follow-up utterance.
	voicetest.AssertOrder(t, h,
		voicetest.MatchType[voiceevent.VADSpeechStart](),
		voicetest.MatchType[voiceevent.VADSpeechEnd](),
		voicetest.MatchType[voiceevent.STTFinal](),
	)
	if clk.stopped != true {
		t.Errorf("silence clock not stopped on run exit")
	}
}

// TestPipeline_ContinuousSpeechInjectsNoSilence is acceptance #2: while real
// frames flow the silence clock must inject NOTHING (no premature mid-utterance
// cut). The mechanism is an idle timer that every real frame resets, so the
// production ticker never fires during speech — pinned here deterministically by
// asserting every real inbound frame reset the clock and was decoded, with the
// clock never fired by the test.
func TestPipeline_ContinuousSpeechInjectsNoSilence(t *testing.T) {
	conv, _, frames := newSilenceRig(t, "hello there")
	codec := &replayCodec{frames: frames}
	clk := &fakeSilenceClock{ch: make(chan time.Time)}
	pipe := NewPipeline(conv, codec, nil, "guild", nil,
		withSilenceClock(testSampleRate, testFrameMs, func() silenceClock { return clk }))

	inbound := make(chan gxvoice.Frame)
	done := make(chan error, 1)
	go func() { done <- pipe.run(t.Context(), inbound) }()

	feedSpeech(inbound, frames) // real audio only; never fire the clock
	close(inbound)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	if got := clk.resetCount(); got != len(frames) {
		t.Errorf("clock reset %d times, want one per real frame (%d): the production ticker must be reset by every real frame so it never fires during speech", got, len(frames))
	}
	if got := codec.decodeCount(); got != len(frames) {
		t.Errorf("decoded %d frames, want %d (every real frame)", got, len(frames))
	}
}

// TestPipeline_ArrivalGapMidUtteranceDoesNotEndpoint is issue #147: a transport
// jitter gap ≥ the VAD hangover in the MIDDLE of a continuous utterance — packet
// ARRIVAL pauses, but stream time is continuous (contiguous PTS) and Discord sent
// NO silence frames, so the speaker did not stop — must not endpoint the turn.
// The silence clock keys on wall-clock arrival, so pre-fix the gap ticks inject
// ≥ hangover silence frames, silero fires speech_end mid-word, and the resumed
// speech becomes a second turn (starts=2). Post-fix the clock is armed only by
// Discord's explicit silence frames (the speaker-stop signal), so the gap injects
// nothing and the utterance stays ONE turn, endpointed once at the genuine stop.
func TestPipeline_ArrivalGapMidUtteranceDoesNotEndpoint(t *testing.T) {
	conv, h, all := newSilenceRig(t, "hello there")
	// The hello-test clip VAD-splits at an internal pause; slice to its second,
	// single utterance (speech from ~frame 61 to the clip end, left open) so "one
	// turn" is unambiguous. Probed with silero at this exact rig geometry.
	frames := all[46:]
	gapAt := 85 - 46 // mid-speech: well after speech-start (~61), well before clip end
	codec := &replayCodec{frames: frames}
	clk := &fakeSilenceClock{ch: make(chan time.Time)}
	pipe := NewPipeline(conv, codec, nil, "guild", nil,
		withSilenceClock(testSampleRate, testFrameMs, func() silenceClock { return clk }))

	inbound := make(chan gxvoice.Frame)
	done := make(chan error, 1)
	go func() { done <- pipe.run(t.Context(), inbound) }()

	// pts models Discord's 20 ms packet cadence: contiguous across the arrival
	// gap (the packets were delayed, not part of a speaker stop).
	pts := func(i int) time.Duration { return time.Duration(i) * 20 * time.Millisecond }
	for i := range frames[:gapAt] {
		inbound <- gxvoice.Frame{PTS: pts(i), Sequence: uint32(i)}
	}
	// The jitter gap: no packets ARRIVE for ≥ the hangover, but no Discord
	// silence frames were seen — the speaker did not stop. The clock must not
	// advance the VAD here.
	for range testHangoverTicks {
		clk.tick()
	}
	// Speech resumes: stream time contiguous with the pre-gap frames.
	for i := range frames[gapAt:] {
		j := gapAt + i
		inbound <- gxvoice.Frame{PTS: pts(j), Sequence: uint32(j)}
	}
	// The genuine stop: Discord's explicit silence frames, then the packet gap
	// the clock endpoints through (the #91 shape).
	for range discordStopSilenceFrames {
		inbound <- gxvoice.Frame{Silence: true}
	}
	for range testHangoverTicks {
		clk.tick()
	}
	close(inbound)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	starts, ends, finals := eventCounts(h)
	if starts != 1 {
		t.Errorf("speech_start=%d, want 1: a mid-utterance arrival gap with continuous PTS and no Discord silence frames must not split the turn", starts)
	}
	if ends != 1 {
		t.Errorf("speech_end=%d, want 1: exactly one endpoint, at the genuine speaker stop", ends)
	}
	if finals != 1 {
		t.Errorf("STTFinal=%d, want 1: the utterance must reach STT as one turn", finals)
	}
}

// TestPipeline_DiscordSilenceFramesDriveTheClock is acceptance #3: a Discord
// "silence" frame must no longer be silently dropped. It is treated as "no real
// audio" — NOT decoded and NOT a clock reset — so the silence clock keeps running
// through the silence frames and the following packet gap, endpointing the open
// utterance. Mirrors the live shape: speaker stops → a few silence frames → gap.
func TestPipeline_DiscordSilenceFramesDriveTheClock(t *testing.T) {
	conv, h, frames := newSilenceRig(t, "hello there")
	codec := &replayCodec{frames: frames}
	clk := &fakeSilenceClock{ch: make(chan time.Time)}
	pipe := NewPipeline(conv, codec, nil, "guild", nil,
		withSilenceClock(testSampleRate, testFrameMs, func() silenceClock { return clk }))

	inbound := make(chan gxvoice.Frame)
	done := make(chan error, 1)
	go func() { done <- pipe.run(t.Context(), inbound) }()

	feedSpeech(inbound, frames)
	// Discord's ~5 Opus silence frames when the speaker stops.
	const silenceFrames = 5
	for range silenceFrames {
		inbound <- gxvoice.Frame{Silence: true}
	}
	// Then the packet gap: the clock advances the VAD hangover.
	for range testHangoverTicks {
		clk.tick()
	}
	close(inbound)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}

	starts, ends, _ := eventCounts(h)
	if ends != starts {
		t.Errorf("trailing utterance not endpointed through silence frames + gap: speech_start=%d speech_end=%d", starts, ends)
	}
	// Silence frames are "no real audio": not decoded and they do NOT reset the
	// idle clock (so the production ticker keeps advancing the VAD through them).
	if got := codec.decodeCount(); got != len(frames) {
		t.Errorf("decoded %d frames, want %d: Discord silence frames must not be decoded", got, len(frames))
	}
	if got := clk.resetCount(); got != len(frames) {
		t.Errorf("clock reset %d times, want %d: Discord silence frames must not reset the clock", got, len(frames))
	}
}
