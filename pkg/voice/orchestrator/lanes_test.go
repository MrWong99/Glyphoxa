package orchestrator_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// contentVAD is a [vad.SessionHandle] that segments purely on frame content: a
// non-silent frame is speech, an all-zero frame is silence. It emits a speech_start
// on the first voiced frame, speech_continue while voiced, a speech_end on the first
// silent frame after speech, and silence otherwise — so a test drives each lane's
// segmentation deterministically by the frames it feeds, one detector per lane.
type contentVAD struct{ speaking bool }

func (c *contentVAD) ProcessFrame(f audio.Frame) (vad.VADEvent, error) {
	voiced := false
	for _, s := range f.Samples() {
		if s != 0 {
			voiced = true
			break
		}
	}
	switch {
	case voiced && !c.speaking:
		c.speaking = true
		return vad.VADEvent{Type: vad.VADSpeechStart}, nil
	case voiced:
		return vad.VADEvent{Type: vad.VADSpeechContinue}, nil
	case c.speaking:
		c.speaking = false
		return vad.VADEvent{Type: vad.VADSpeechEnd}, nil
	default:
		return vad.VADEvent{Type: vad.VADSilence}, nil
	}
}

func (c *contentVAD) Reset()       { c.speaking = false }
func (c *contentVAD) Close() error { return nil }

// laneFrame builds a 32 ms / 16 kHz frame whose 512 samples all equal value
// (0 = silence), tagged with speaker. A distinct non-zero value per speaker lets a
// test prove a lane's segment contains only its own speaker's frames.
func laneFrame(t *testing.T, speaker string, value int16) audio.Frame {
	t.Helper()
	s := make([]int16, 512)
	for i := range s {
		s[i] = value
	}
	f, err := audio.NewFrame(s, 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f.WithSpeaker(speaker)
}

// laneVADFactory returns a factory building a contentVAD-backed lane VAD on bus,
// counting the close funcs invoked (so a reap test proves the ONNX session is
// released). err, when set, is returned instead — the degraded path.
func laneVADFactory(bus *voiceevent.Bus, closes *int, err error) (orchestrator.LaneVADFactory, *sync.Mutex) {
	var mu sync.Mutex
	return func() (*orchestrator.VAD, func(), error) {
		if err != nil {
			return nil, nil, err
		}
		v := orchestrator.NewVAD(bus, &contentVAD{})
		return v, func() { mu.Lock(); *closes++; mu.Unlock() }, nil
	}, &mu
}

// newLaneSegmenter wires a lane-enabled segmenter over a default contentVAD and the
// given recognizer onto a fresh bus, bound for the test's lifetime.
func newLaneSegmenter(t *testing.T, bus *voiceevent.Bus, rec *recordingRecognizer, factory orchestrator.LaneVADFactory) *orchestrator.Segmenter {
	t.Helper()
	vadStage := orchestrator.NewVAD(bus, &contentVAD{})
	sttStage := orchestrator.NewSTT(bus, rec)
	seg := orchestrator.NewSegmenter(vadStage, sttStage)
	seg.SetLaneVADFactory(factory)
	t.Cleanup(seg.Bind(t.Context(), bus))
	return seg
}

func processFrames(t *testing.T, seg *orchestrator.Segmenter, frames ...audio.Frame) {
	t.Helper()
	for i, f := range frames {
		if err := seg.Process(f); err != nil {
			t.Fatalf("frame %d: Process: %v", i, err)
		}
	}
}

// TestSegmenter_TwoSpeakers_TwoLanesTwoFinals is step 3: speaker A then speaker B
// open two Speaker Lanes; each utterance is transcribed on its own lane and only its
// own frames, and the two STTFinals carry distinct SpeakerIDs (ADR-0050).
func TestSegmenter_TwoSpeakers_TwoLanesTwoFinals(t *testing.T) {
	bus := voiceevent.NewBus()
	var finals []voiceevent.STTFinal
	voiceevent.On(bus, func(e voiceevent.STTFinal) { finals = append(finals, e) })
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	// A speaks (value 100) then goes silent (flush), then B speaks (value 200).
	processFrames(t, seg,
		laneFrame(t, "A", 100), laneFrame(t, "A", 100), laneFrame(t, "A", 0),
		laneFrame(t, "B", 200), laneFrame(t, "B", 200), laneFrame(t, "B", 0),
	)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := seg.LaneCount(); got != 3 { // default + A + B
		t.Errorf("lane count = %d, want 3 (default, A, B)", got)
	}
	batches := rec.batches()
	if len(batches) != 2 {
		t.Fatalf("transcribe calls = %d, want 2 (one per speaker)", len(batches))
	}
	// Each batch is 2 voiced frames of a single speaker's value — no cross-lane bleed.
	for _, b := range batches {
		if len(b) != 2 {
			t.Errorf("segment had %d frames, want 2", len(b))
		}
		v := b[0].Samples()[0]
		for _, f := range b {
			if f.Samples()[0] != v {
				t.Errorf("segment mixed speaker values (%d and %d) — lanes bled together", v, f.Samples()[0])
			}
		}
	}
	ids := map[string]bool{}
	for _, f := range finals {
		ids[f.SpeakerID] = true
	}
	if !ids["A"] || !ids["B"] || len(ids) != 2 {
		t.Errorf("STTFinal SpeakerIDs = %v, want exactly {A, B}", ids)
	}
}

// TestSegmenter_CrossTalk_LanesStayIntact is step 4: A is mid-utterance when B
// interjects a short overlap; each lane's segment stays intact and correctly
// attributed (the whole point of per-speaker lanes — cross-talk must not bake a
// mis-attribution into the Transcript, ADR-0050).
func TestSegmenter_CrossTalk_LanesStayIntact(t *testing.T) {
	bus := voiceevent.NewBus()
	var finals []voiceevent.STTFinal
	voiceevent.On(bus, func(e voiceevent.STTFinal) { finals = append(finals, e) })
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	// A: |----speech----|  with B's short overlap in the middle.
	processFrames(t, seg,
		laneFrame(t, "A", 100), // A start
		laneFrame(t, "A", 100), // A continue
		laneFrame(t, "B", 200), // B start (overlap)
		laneFrame(t, "A", 100), // A continue (still speaking)
		laneFrame(t, "B", 0),   // B end → flush B (1 frame)
		laneFrame(t, "A", 100), // A continue
		laneFrame(t, "A", 0),   // A end → flush A (4 frames)
	)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	batches := rec.batches()
	if len(batches) != 2 {
		t.Fatalf("transcribe calls = %d, want 2 (A + B)", len(batches))
	}
	var aLen, bLen int
	for _, b := range batches {
		switch b[0].Samples()[0] {
		case 100:
			aLen = len(b)
		case 200:
			bLen = len(b)
		}
		v := b[0].Samples()[0]
		for _, f := range b {
			if f.Samples()[0] != v {
				t.Errorf("cross-talk bled a %d frame into a %d segment", f.Samples()[0], v)
			}
		}
	}
	if aLen != 4 {
		t.Errorf("A segment had %d frames, want 4 (its own voiced frames only)", aLen)
	}
	if bLen != 1 {
		t.Errorf("B segment had %d frames, want 1 (its short overlap only)", bLen)
	}
	if len(finals) != 2 {
		t.Errorf("STTFinals = %d, want 2", len(finals))
	}
}

// TestSegmenter_EmptySpeakerBroadcasts is step 5: an unattributed ("") frame — the
// silence clock — broadcasts to every lane, advancing a listening lane's hangover so
// it endpoints and flushes (ADR-0050's speaker-agnostic silence clock).
func TestSegmenter_EmptySpeakerBroadcasts(t *testing.T) {
	bus := voiceevent.NewBus()
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	// A speaks, then a broadcast SILENCE-clock frame (Speaker "") ends A's utterance.
	silence, err := audio.NewFrame(make([]int16, 512), 16000, 32) // Speaker() == ""
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	processFrames(t, seg,
		laneFrame(t, "A", 100),
		laneFrame(t, "A", 100),
		silence, // broadcast: advances A's lane hangover → speech_end → flush
	)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	batches := rec.batches()
	if len(batches) != 1 {
		t.Fatalf("transcribe calls = %d, want 1 (the broadcast silence endpointed A)", len(batches))
	}
	if got := len(batches[0]); got != 2 {
		t.Errorf("A segment had %d frames, want 2 (silence-clock frame endpoints, not buffered)", got)
	}
}

// TestSegmenter_LaneIdleReap is step 6: a lane idle past the TTL is reaped — its
// buffered utterance flushed (not dropped), its VAD close() called — and the default
// lane is never reaped (ADR-0050 lane lifecycle; risk (b) ONNX release).
func TestSegmenter_LaneIdleReap(t *testing.T) {
	bus := voiceevent.NewBus()
	rec := &recordingRecognizer{}
	closes := 0
	factory, cmu := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	now := time.Unix(0, 0)
	seg.SetLaneReap(50*time.Millisecond, func() time.Time { return now })
	seg.SetSweepEvery(1) // sweep on every Process call

	// A speaks but never ends (buffered mid-utterance), leaving A's lane open.
	processFrames(t, seg, laneFrame(t, "A", 100), laneFrame(t, "A", 100))
	if got := seg.LaneCount(); got != 2 {
		t.Fatalf("lane count = %d, want 2 (default + A)", got)
	}

	// Advance the clock past the TTL; a further (unattributed) frame triggers the sweep.
	now = now.Add(time.Second)
	silence, _ := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err := seg.Process(silence); err != nil {
		t.Fatalf("Process (sweep trigger): %v", err)
	}
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := seg.LaneCount(); got != 1 {
		t.Errorf("lane count after reap = %d, want 1 (only the default lane survives)", got)
	}
	cmu.Lock()
	gotCloses := closes
	cmu.Unlock()
	if gotCloses != 1 {
		t.Errorf("lane VAD close() called %d times, want 1 (reaped lane's ONNX session released)", gotCloses)
	}
	// The reaped lane's buffered utterance was flushed, not dropped.
	batches := rec.batches()
	if len(batches) != 1 {
		t.Fatalf("transcribe calls = %d, want 1 (reaped lane's buffered utterance flushed)", len(batches))
	}
	if got := len(batches[0]); got != 2 {
		t.Errorf("reaped segment had %d frames, want 2 (A's buffered audio)", got)
	}
}

// TestSegmenter_FactoryErrorFallsToDefaultLane is step 7: a lane VAD factory error
// degrades the speaker's frames to the default lane (still transcribed) and reports
// the error via onError — the audio loop stays up (ADR-0050 risk (c)).
func TestSegmenter_FactoryErrorFallsToDefaultLane(t *testing.T) {
	bus := voiceevent.NewBus()
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, errors.New("silero session exhausted"))
	seg := newLaneSegmenter(t, bus, rec, factory)
	var mu sync.Mutex
	var errs []error
	seg.SetErrorHandler(func(err error) { mu.Lock(); errs = append(errs, err); mu.Unlock() })

	// A's frames cannot open a lane (factory errors) → they drive the DEFAULT lane's
	// VAD instead. Feed a full A utterance on the default lane.
	processFrames(t, seg,
		laneFrame(t, "A", 100), laneFrame(t, "A", 100), laneFrame(t, "A", 0),
	)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := seg.LaneCount(); got != 1 {
		t.Errorf("lane count = %d, want 1 (no lane opened; frames fell to default)", got)
	}
	if len(rec.batches()) != 1 {
		t.Errorf("transcribe calls = %d, want 1 (degraded but still transcribed)", len(rec.batches()))
	}
	mu.Lock()
	gotErrs := len(errs)
	mu.Unlock()
	if gotErrs == 0 {
		t.Error("factory error was not reported via onError")
	}
}

// TestSegmenter_FlushDrainsAllLanes is step 8: Flush drains a still-buffered
// utterance on every lane (default + each speaker lane), so no mid-utterance lane is
// lost at end-of-stream.
func TestSegmenter_FlushDrainsAllLanes(t *testing.T) {
	bus := voiceevent.NewBus()
	var finals []voiceevent.STTFinal
	voiceevent.On(bus, func(e voiceevent.STTFinal) { finals = append(finals, e) })
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	// Two speakers, both mid-utterance (never a silent end frame) at end-of-stream.
	processFrames(t, seg,
		laneFrame(t, "A", 100), laneFrame(t, "A", 100),
		laneFrame(t, "B", 200),
	)
	if len(rec.batches()) != 0 {
		t.Fatalf("before Flush: %d transcribe calls, want 0 (both lanes still listening)", len(rec.batches()))
	}
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if len(rec.batches()) != 2 {
		t.Fatalf("after Flush: %d transcribe calls, want 2 (both lanes drained)", len(rec.batches()))
	}
	ids := map[string]bool{}
	for _, f := range finals {
		ids[f.SpeakerID] = true
	}
	if !ids["A"] || !ids["B"] {
		t.Errorf("flushed SpeakerIDs = %v, want both A and B", ids)
	}
}
