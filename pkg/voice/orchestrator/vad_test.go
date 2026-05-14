package orchestrator_test

import (
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad/silero"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// TestVAD_HelloTest_EmitsSpeechStart is TB1: the foundation tracer bullet
// for the orchestrator-first TDD voice pipeline (ADR-0019).
//
// It feeds the "hello-test" fixture (a GM addressing the Butler) through a
// real silero-VAD session driven by the orchestrator's VAD stage and asserts
// that exactly the speech-onset event reaches the shared event bus
// (ADR-0020). Subsequent tracer bullets layer speech_end, ordering, STT,
// address detection, etc. on top.
func TestVAD_HelloTest_EmitsSpeechStart(t *testing.T) {
	h := voicetest.New(t)

	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}

	cfg := vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      32, // 512 samples — silero v5 valid chunk size
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
	sess, err := engine.NewSession(cfg)
	if err != nil {
		t.Fatalf("engine.NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	stage := orchestrator.NewVAD(h.Bus, sess)

	clip := voicetest.LoadClip(t, "hello-test")
	if clip.SampleRate != cfg.SampleRate {
		t.Fatalf("clip sample rate %d Hz, want %d Hz", clip.SampleRate, cfg.SampleRate)
	}
	if clip.Channels != 1 || clip.BitDepth != 16 {
		t.Fatalf("clip format %dch %d-bit, want 1ch 16-bit", clip.Channels, clip.BitDepth)
	}

	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	frames, tail := clip.FramesOf(t, chunkSize)
	if tail != 0 {
		t.Logf("hello-test: trailing %d samples (%d ms) not frame-aligned; discarded",
			tail, tail*1000/cfg.SampleRate)
	}
	for i, frame := range frames {
		if err := stage.Process(frame); err != nil {
			t.Fatalf("frame %d: stage.Process: %v", i, err)
		}
	}

	voicetest.AssertEventOccurred[voiceevent.VADSpeechStart](t, h)
}

// TestVAD_HelloTest_SpeechEndFollowsSpeechStart is TB2: it layers ordering on
// top of TB1. Same fixture, same single utterance — but now the assertion is
// that speech_end is observed after speech_start, exercising the orchestrator's
// new VADSpeechEnd publish path and the [voicetest.AssertOrder] primitive.
//
// The hello-test WAV is a TTS-generated single utterance with little inherent
// trailing silence. Silero requires ~480 ms of consecutive sub-threshold
// frames (15 × 32 ms with the default minSilenceFrames) to leave the speaking
// state, so the test appends explicit silent frames after the clip — this
// mirrors what real microphone input looks like between utterances and keeps
// the assertion deterministic regardless of the fixture's exact tail length.
func TestVAD_HelloTest_SpeechEndFollowsSpeechStart(t *testing.T) {
	h := voicetest.New(t)
	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}

	cfg := vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      32, // 512 samples — silero v5 valid chunk size
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
	sess, err := engine.NewSession(cfg)
	if err != nil {
		t.Fatalf("engine.NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	stage := orchestrator.NewVAD(h.Bus, sess)

	clip := voicetest.LoadClip(t, "hello-test")
	if clip.SampleRate != cfg.SampleRate {
		t.Fatalf("clip sample rate %d Hz, want %d Hz", clip.SampleRate, cfg.SampleRate)
	}
	if clip.Channels != 1 || clip.BitDepth != 16 {
		t.Fatalf("clip format %dch %d-bit, want 1ch 16-bit", clip.Channels, clip.BitDepth)
	}

	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	frames, tail := clip.FramesOf(t, chunkSize)
	if tail != 0 {
		t.Logf("hello-test: trailing %d samples (%d ms) not frame-aligned; discarded",
			tail, tail*1000/cfg.SampleRate)
	}
	for i, frame := range frames {
		if err := stage.Process(frame); err != nil {
			t.Fatalf("frame %d: stage.Process: %v", i, err)
		}
	}

	// Append ~640 ms of silence (20 × 32 ms) to guarantee Silero's
	// minSilenceFrames=15 threshold is crossed and a speech_end transition
	// is published, independent of the fixture's recorded tail.
	silentSamples := make([]int16, chunkSize)
	silentFrame, err := audio.NewFrame(silentSamples, cfg.SampleRate, cfg.FrameSizeMs)
	if err != nil {
		t.Fatalf("audio.NewFrame(silence): %v", err)
	}
	for i := range 20 {
		if err := stage.Process(silentFrame); err != nil {
			t.Fatalf("silence frame %d: stage.Process: %v", i, err)
		}
	}

	voicetest.AssertOrder(t, h,
		voicetest.MatchType[voiceevent.VADSpeechStart](),
		voicetest.MatchType[voiceevent.VADSpeechEnd](),
	)
}

// TestVAD_SilenceTest_EmitsNoSpeechStart is TB3: the negative-path
// counterpart to TB1. The hello-test fixture alone cannot pin the VAD
// stage's contract — a naive implementation that fires VADSpeechStart on
// every frame would pass TB1 — so we feed a pure-silence fixture and
// assert the bus stays silent of speech events.
func TestVAD_SilenceTest_EmitsNoSpeechStart(t *testing.T) {
	h := voicetest.New(t)

	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}

	cfg := vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      32, // 512 samples — silero v5 valid chunk size
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
	sess, err := engine.NewSession(cfg)
	if err != nil {
		t.Fatalf("engine.NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	stage := orchestrator.NewVAD(h.Bus, sess)

	clip := voicetest.LoadClip(t, "silence-test")
	if clip.SampleRate != cfg.SampleRate {
		t.Fatalf("clip sample rate %d Hz, want %d Hz", clip.SampleRate, cfg.SampleRate)
	}
	if clip.Channels != 1 || clip.BitDepth != 16 {
		t.Fatalf("clip format %dch %d-bit, want 1ch 16-bit", clip.Channels, clip.BitDepth)
	}

	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	frames, tail := clip.FramesOf(t, chunkSize)
	if tail != 0 {
		t.Logf("silence-test: trailing %d samples (%d ms) not frame-aligned; discarded",
			tail, tail*1000/cfg.SampleRate)
	}
	for i, frame := range frames {
		if err := stage.Process(frame); err != nil {
			t.Fatalf("frame %d: stage.Process: %v", i, err)
		}
	}

	voicetest.AssertNoEvent[voiceevent.VADSpeechStart](t, h)
}

// TestVAD_TwoUtteranceTest_EmitsTwoSpeechStarts is TB4: two utterances
// separated by a silence gap must produce two distinct VADSpeechStart events.
//
// The two-utterance-test fixture (~5.66s) is two ElevenLabs renderings glued
// together with 1.5s of zero PCM in between — well over the default silence
// hysteresis window (minSilenceFrames=15 × 32ms = 480ms). If the VAD's
// speech/silence state machine is correctly tuned, frame probabilities drop
// below SilenceThreshold for long enough during the gap to return the state
// machine to stateSilence, re-arming the onset path so the second utterance
// fires a fresh speech_start.
//
// AssertEventCount makes the count itself the property under test: one event
// (gap was swallowed) and three events (spurious onset inside an utterance)
// are both failure modes.
func TestVAD_TwoUtteranceTest_EmitsTwoSpeechStarts(t *testing.T) {
	h := voicetest.New(t)

	engine, err := silero.New()
	if err != nil {
		t.Fatalf("silero.New: %v", err)
	}

	cfg := vad.Config{
		SampleRate:       16000,
		FrameSizeMs:      32, // 512 samples — silero v5 valid chunk size
		SpeechThreshold:  0.5,
		SilenceThreshold: 0.35,
	}
	sess, err := engine.NewSession(cfg)
	if err != nil {
		t.Fatalf("engine.NewSession: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close() })

	stage := orchestrator.NewVAD(h.Bus, sess)

	clip := voicetest.LoadClip(t, "two-utterance-test")
	if clip.SampleRate != cfg.SampleRate {
		t.Fatalf("clip sample rate %d Hz, want %d Hz", clip.SampleRate, cfg.SampleRate)
	}
	if clip.Channels != 1 || clip.BitDepth != 16 {
		t.Fatalf("clip format %dch %d-bit, want 1ch 16-bit", clip.Channels, clip.BitDepth)
	}

	chunkSize := cfg.SampleRate * cfg.FrameSizeMs / 1000
	frames, tail := clip.FramesOf(t, chunkSize)
	if tail != 0 {
		t.Logf("two-utterance-test: trailing %d samples (%d ms) not frame-aligned; discarded",
			tail, tail*1000/cfg.SampleRate)
	}
	for i, frame := range frames {
		if err := stage.Process(frame); err != nil {
			t.Fatalf("frame %d: stage.Process: %v", i, err)
		}
	}

	voicetest.AssertEventCount[voiceevent.VADSpeechStart](t, h, 2)
}
