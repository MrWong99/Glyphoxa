package orchestrator_test

import (
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// TestVAD_RecordsHangoverOnSpeechEnd pins the #125 vad_hangover wiring: the stage
// records exactly one VADHangover span — the fixed end-of-speech detection lag it
// is constructed with — per speech_end transition, and none for a speech_start or
// a silence frame. The value is the configured constant (minSilenceFrames*frameMs),
// not derived per-frame, so a single 384 ms observation is what the histogram gets.
func TestVAD_RecordsHangoverOnSpeechEnd(t *testing.T) {
	const hangover = 384 * time.Millisecond
	bus := voiceevent.NewBus()
	spy := &metricsSpy{}
	// Script: start (no hangover), continue (no hangover), end (one hangover).
	stage := orchestrator.NewVAD(bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechContinue, vad.VADSpeechEnd,
	}}, orchestrator.WithVADMetrics(spy, hangover))

	silent, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	for i := 0; i < 4; i++ { // start, continue, end, silence
		if err := stage.Process(silent); err != nil {
			t.Fatalf("frame %d: Process: %v", i, err)
		}
	}

	_, _, hangs, _, _ := spy.snapshot()
	if len(hangs) != 1 {
		t.Fatalf("recorded %d vad_hangover spans, want exactly 1 (one per speech_end)", len(hangs))
	}
	if hangs[0] != hangover {
		t.Errorf("vad_hangover = %v, want the configured constant %v", hangs[0], hangover)
	}
}

// TestVAD_KeylessRecordsNoHangover pins the keyless default: an option-less NewVAD
// records nothing on speech_end (the recorder defaults to observe.Discard) and
// never nil-panics.
func TestVAD_KeylessRecordsNoHangover(t *testing.T) {
	bus := voiceevent.NewBus()
	stage := orchestrator.NewVAD(bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechEnd,
	}})
	silent, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := stage.Process(silent); err != nil {
			t.Fatalf("frame %d: Process: %v", i, err)
		}
	}
	// No assertion beyond "did not panic": the Discard default has no observable
	// sink, so reaching speech_end without a wired recorder must stay silent.
}

// TestVAD_HelloTest_EmitsSpeechStart is TB1: the foundation tracer bullet
// for the orchestrator-first TDD voice pipeline (ADR-0019).
//
// It feeds the "hello-test" fixture (a GM addressing the Butler) through a
// real silero-VAD session driven by the orchestrator's VAD stage and asserts
// that exactly the speech-onset event reaches the shared event bus
// (ADR-0020). Subsequent tracer bullets layer speech_end, ordering, STT,
// address detection, etc. on top.
func TestVAD_HelloTest_EmitsSpeechStart(t *testing.T) {
	stage, h, frames := voicetest.NewVADRig(t, "hello-test")
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
	stage, h, frames := voicetest.NewVADRig(t, "hello-test")
	for i, frame := range frames {
		if err := stage.Process(frame); err != nil {
			t.Fatalf("frame %d: stage.Process: %v", i, err)
		}
	}

	// Append ~640 ms of silence (20 × 32 ms) to guarantee Silero's
	// minSilenceFrames=15 threshold is crossed and a speech_end transition
	// is published, independent of the fixture's recorded tail. Frame size is
	// taken from the rig's frames so this stays in lockstep with NewVADRig's
	// fixed configuration.
	silentSamples := make([]int16, len(frames[0].Samples()))
	silentFrame, err := audio.NewFrame(silentSamples, frames[0].SampleRate(), frames[0].FrameMs())
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
	stage, h, frames := voicetest.NewVADRig(t, "silence-test")
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
	stage, h, frames := voicetest.NewVADRig(t, "two-utterance-test")
	for i, frame := range frames {
		if err := stage.Process(frame); err != nil {
			t.Fatalf("frame %d: stage.Process: %v", i, err)
		}
	}

	voicetest.AssertEventCount[voiceevent.VADSpeechStart](t, h, 2)
}

// TestVAD_PublishesVoicingTransitions pins the #431 publish path: the stage
// republishes the session's provisional VADVoicingStopped / VADVoicingResumed
// transitions onto the bus, stamped with the frame's Speaker() like every
// other transition (ADR-0050) — these are what let the barge-in confirm
// window observe the real end of voicing instead of the hangover-delayed
// speech_end.
func TestVAD_PublishesVoicingTransitions(t *testing.T) {
	bus := voiceevent.NewBus()
	var stopped []voiceevent.VADVoicingStopped
	var resumed []voiceevent.VADVoicingResumed
	voiceevent.On(bus, func(e voiceevent.VADVoicingStopped) { stopped = append(stopped, e) })
	voiceevent.On(bus, func(e voiceevent.VADVoicingResumed) { resumed = append(resumed, e) })

	stage := orchestrator.NewVAD(bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADVoicingStopped, vad.VADVoicingResumed, vad.VADSpeechEnd,
	}})

	base, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	for i := 0; i < 4; i++ {
		if err := stage.Process(base.WithSpeaker("42")); err != nil {
			t.Fatalf("frame %d: Process: %v", i, err)
		}
	}

	if len(stopped) != 1 || stopped[0].SpeakerID != "42" {
		t.Errorf("VADVoicingStopped = %+v, want exactly one with SpeakerID \"42\"", stopped)
	}
	if len(resumed) != 1 || resumed[0].SpeakerID != "42" {
		t.Errorf("VADVoicingResumed = %+v, want exactly one with SpeakerID \"42\"", resumed)
	}
}

// TestVAD_StampsSpeakerIDFromFrame pins that the VAD stage stamps each published
// speech transition with the processed frame's Speaker() (ADR-0050): an attributed
// frame yields VADSpeechStart/End carrying that SpeakerID, and an unattributed ("")
// frame leaves the events' SpeakerID empty — the single-lane MVP path unchanged.
func TestVAD_StampsSpeakerIDFromFrame(t *testing.T) {
	bus := voiceevent.NewBus()
	var starts []voiceevent.VADSpeechStart
	var ends []voiceevent.VADSpeechEnd
	voiceevent.On(bus, func(e voiceevent.VADSpeechStart) { starts = append(starts, e) })
	voiceevent.On(bus, func(e voiceevent.VADSpeechEnd) { ends = append(ends, e) })

	stage := orchestrator.NewVAD(bus, &scriptedVAD{events: []vad.VADEventType{
		vad.VADSpeechStart, vad.VADSpeechEnd,
	}})

	base, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	if err := stage.Process(base.WithSpeaker("42")); err != nil { // → speech_start
		t.Fatalf("Process: %v", err)
	}
	if err := stage.Process(base.WithSpeaker("42")); err != nil { // → speech_end
		t.Fatalf("Process: %v", err)
	}

	if len(starts) != 1 || starts[0].SpeakerID != "42" {
		t.Errorf("VADSpeechStart SpeakerID = %+v, want one with \"42\"", starts)
	}
	if len(ends) != 1 || ends[0].SpeakerID != "42" {
		t.Errorf("VADSpeechEnd SpeakerID = %+v, want one with \"42\"", ends)
	}
}
