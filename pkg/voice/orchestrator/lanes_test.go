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
type contentVAD struct {
	speaking bool
	// hangover is the number of consecutive silent frames BUFFERED before speech_end
	// (0 = end on the first silent frame). A non-zero hangover models real Silero: the
	// silence-clock frames that endpoint an utterance are buffered into it first.
	hangover int
	silent   int
}

func (c *contentVAD) ProcessFrame(f audio.Frame) (vad.VADEvent, error) {
	voiced := false
	for _, s := range f.Samples() {
		if s != 0 {
			voiced = true
			break
		}
	}
	if voiced {
		c.silent = 0
		if !c.speaking {
			c.speaking = true
			return vad.VADEvent{Type: vad.VADSpeechStart}, nil
		}
		return vad.VADEvent{Type: vad.VADSpeechContinue}, nil
	}
	if c.speaking {
		c.silent++
		if c.silent > c.hangover {
			c.speaking = false
			return vad.VADEvent{Type: vad.VADSpeechEnd}, nil
		}
		return vad.VADEvent{Type: vad.VADSpeechContinue}, nil // buffered during hangover
	}
	return vad.VADEvent{Type: vad.VADSilence}, nil
}

func (c *contentVAD) Reset()       { c.speaking = false; c.silent = 0 }
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
	return laneVADFactoryH(bus, closes, err, 0)
}

// laneVADFactoryH is [laneVADFactory] with a configurable VAD hangover (silent frames
// buffered before speech_end) — non-zero so a silence-clock frame lands in the segment.
func laneVADFactoryH(bus *voiceevent.Bus, closes *int, err error, hangover int) (orchestrator.LaneVADFactory, *sync.Mutex) {
	var mu sync.Mutex
	return func() (*orchestrator.VAD, func(), error) {
		if err != nil {
			return nil, nil, err
		}
		v := orchestrator.NewVAD(bus, &contentVAD{hangover: hangover})
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

func silentFrame(t *testing.T) audio.Frame {
	t.Helper()
	f, err := audio.NewFrame(make([]int16, 512), 16000, 32) // Speaker() == ""
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f
}

// TestSegmenter_ProcessSilenceBroadcastsToAllLanes is step 5 / T3 (revised contract):
// the silence CLOCK — [Segmenter.ProcessSilence] — reaches EVERY lane, endpointing
// each listening lane on its own SpeakerID, and the silence frames land in each lane's
// buffered segment stamped with that lane's id (parity with the pre-lane single-lane
// path). A VAD hangover of 1 buffers the first silence frame before the second ends it.
func TestSegmenter_ProcessSilenceBroadcastsToAllLanes(t *testing.T) {
	bus := voiceevent.NewBus()
	var finals []voiceevent.STTFinal
	voiceevent.On(bus, func(e voiceevent.STTFinal) { finals = append(finals, e) })
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactoryH(bus, &closes, nil, 1) // 1-frame hangover
	seg := newLaneSegmenter(t, bus, rec, factory)

	// A and B each mid-utterance (real speech via Process).
	processFrames(t, seg,
		laneFrame(t, "A", 100), laneFrame(t, "B", 200),
	)
	// Two silence-clock ticks: first buffered into each lane (hangover), second endpoints.
	if err := seg.ProcessSilence(silentFrame(t)); err != nil {
		t.Fatalf("ProcessSilence 1: %v", err)
	}
	if err := seg.ProcessSilence(silentFrame(t)); err != nil {
		t.Fatalf("ProcessSilence 2: %v", err)
	}
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if len(finals) != 2 {
		t.Fatalf("STTFinals = %d, want 2 (both lanes endpointed by the silence clock)", len(finals))
	}
	ids := map[string]bool{}
	for _, f := range finals {
		ids[f.SpeakerID] = true
	}
	if !ids["A"] || !ids["B"] {
		t.Errorf("endpointed SpeakerIDs = %v, want both A and B", ids)
	}
	// Each segment contains a silence frame stamped with ITS lane's id.
	batches := rec.batches()
	if len(batches) != 2 {
		t.Fatalf("transcribe calls = %d, want 2", len(batches))
	}
	for _, b := range batches {
		laneID := b[0].Speaker()
		sawSilence := false
		for _, f := range b {
			if f.Speaker() != laneID {
				t.Errorf("segment frame stamped %q, want lane id %q (silence restamped per lane)", f.Speaker(), laneID)
			}
			if f.Samples()[0] == 0 {
				sawSilence = true
			}
		}
		if !sawSilence {
			t.Errorf("lane %q segment has no buffered silence-clock frame", laneID)
		}
	}
}

// TestSegmenter_VoicedUnknownSpeakerDefaultLaneOnly is T2 / finding 2: a still-voiced
// frame whose SSRC hasn't resolved (Speaker() == "") goes to the DEFAULT lane ONLY —
// it never touches an open Speaker Lane (no phantom misattribution) and its STTFinal
// is unattributed (SpeakerID "" → Butler fail-closed). The open lanes' segments stay
// clean.
func TestSegmenter_VoicedUnknownSpeakerDefaultLaneOnly(t *testing.T) {
	bus := voiceevent.NewBus()
	var finals []voiceevent.STTFinal
	voiceevent.On(bus, func(e voiceevent.STTFinal) { finals = append(finals, e) })
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	// Open lanes A and B (each a full utterance), then a VOICED unknown-SSRC frame
	// (Speaker "" but non-zero PCM, value 300) followed by a silent "" frame to end it.
	voicedUnknown, err := audio.NewFrame(mkSamples(300), 16000, 32) // Speaker() == ""
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	processFrames(t, seg,
		laneFrame(t, "A", 100), laneFrame(t, "A", 0), // A utterance
		laneFrame(t, "B", 200), laneFrame(t, "B", 0), // B utterance
		voicedUnknown,  // → default lane only
		silentFrame(t), // ends the default-lane utterance
	)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Three finals: A, B, and the unattributed default-lane one.
	bySpeaker := map[string]string{}
	for _, f := range finals {
		bySpeaker[f.SpeakerID] = "seen"
	}
	if _, ok := bySpeaker[""]; !ok {
		t.Error("no unattributed STTFinal for the voiced unknown-SSRC frame (default lane)")
	}
	if bySpeaker["A"] == "" || bySpeaker["B"] == "" {
		t.Errorf("SpeakerIDs = %v, want A, B and \"\"", bySpeaker)
	}
	// No lane's segment may contain the value-300 unknown frame — it never touched them.
	for _, b := range rec.batches() {
		id := b[0].Speaker()
		if id != "" { // a Speaker Lane
			for _, f := range b {
				if f.Samples()[0] == 300 {
					t.Errorf("lane %q segment contains the voiced unknown-SSRC frame — phantom misattribution", id)
				}
			}
		}
	}
}

func mkSamples(v int16) []int16 {
	s := make([]int16, 512)
	for i := range s {
		s[i] = v
	}
	return s
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
	if err := seg.Process(silentFrame(t)); err != nil {
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

// TestSegmenter_LanesReapUnderContinuousSilence is T1 / finding 1 (the high one): a
// quiet table ticks the silence clock every 32 ms forever. Because processLane no
// longer refreshes lastSeen (only attributed frames via laneFor do), the lanes MUST
// still age past the idle TTL and be reaped DESPITE the continuous silence — otherwise
// each departed speaker's ONNX inferencer and stream slot leak for the session.
func TestSegmenter_LanesReapUnderContinuousSilence(t *testing.T) {
	bus := voiceevent.NewBus()
	rec := &recordingRecognizer{}
	closes := 0
	factory, cmu := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	now := time.Unix(0, 0)
	seg.SetLaneReap(500*time.Millisecond, func() time.Time { return now })
	seg.SetSweepEvery(4) // sweep every 4 ticks, a realistic amortised cadence (not 1)

	// Two speakers speak once each (lanes A, B created; each utterance ended).
	processFrames(t, seg,
		laneFrame(t, "A", 100), laneFrame(t, "A", 0),
		laneFrame(t, "B", 200), laneFrame(t, "B", 0),
	)
	if got := seg.LaneCount(); got != 3 {
		t.Fatalf("lane count = %d, want 3 (default + A + B)", got)
	}

	// The table goes quiet: only the silence clock ticks, advancing wall time. The
	// lanes must age out even though ProcessSilence keeps being called.
	for i := 0; i < 40; i++ {
		now = now.Add(32 * time.Millisecond) // ~1.28s total, well past the 500ms TTL
		if err := seg.ProcessSilence(silentFrame(t)); err != nil {
			t.Fatalf("ProcessSilence %d: %v", i, err)
		}
	}

	if got := seg.LaneCount(); got != 1 {
		t.Errorf("lane count = %d, want 1 — lanes never aged under continuous silence (reap dead in prod)", got)
	}
	cmu.Lock()
	gotCloses := closes
	cmu.Unlock()
	if gotCloses != 2 {
		t.Errorf("lane VAD close() called %d times, want 2 (both departed speakers' ONNX sessions released)", gotCloses)
	}
}

// TestSegmenter_SweepFiresFromSilenceOnly is T4: reap runs from ProcessSilence too, so
// a lane created then followed ONLY by silence ticks (no further attributed frame)
// still reaps once the clock passes the TTL — idle is exactly when reap must fire.
func TestSegmenter_SweepFiresFromSilenceOnly(t *testing.T) {
	bus := voiceevent.NewBus()
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, nil)
	seg := newLaneSegmenter(t, bus, rec, factory)

	now := time.Unix(0, 0)
	seg.SetLaneReap(50*time.Millisecond, func() time.Time { return now })
	seg.SetSweepEvery(1)

	processFrames(t, seg, laneFrame(t, "A", 100), laneFrame(t, "A", 0))
	if got := seg.LaneCount(); got != 2 {
		t.Fatalf("lane count = %d, want 2", got)
	}
	now = now.Add(time.Second)
	if err := seg.ProcessSilence(silentFrame(t)); err != nil { // only silence, no attributed frame
		t.Fatalf("ProcessSilence: %v", err)
	}
	if got := seg.LaneCount(); got != 1 {
		t.Errorf("lane count = %d, want 1 (reap must fire from ProcessSilence)", got)
	}
}

// TestSegmenter_FactoryErrorReportedOnceThenDegrades is step 7 + finding 3: a lane VAD
// factory error degrades the speaker's frames to the DEFAULT lane (still transcribed,
// unattributed) and reports the error exactly ONCE — not per frame at ~31/s. Later
// frames from the same speaker take the memoized degrade path silently (risk (c)).
func TestSegmenter_FactoryErrorReportedOnceThenDegrades(t *testing.T) {
	bus := voiceevent.NewBus()
	var finals []voiceevent.STTFinal
	voiceevent.On(bus, func(e voiceevent.STTFinal) { finals = append(finals, e) })
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, errors.New("silero session exhausted"))
	seg := newLaneSegmenter(t, bus, rec, factory)
	var mu sync.Mutex
	errs := 0
	seg.SetErrorHandler(func(error) { mu.Lock(); errs++; mu.Unlock() })

	// A MANY-frame utterance: a per-frame factory retry would fire onError ~20 times.
	frames := make([]audio.Frame, 0, 21)
	for i := 0; i < 20; i++ {
		frames = append(frames, laneFrame(t, "A", 100))
	}
	frames = append(frames, laneFrame(t, "A", 0)) // ends the default-lane utterance
	processFrames(t, seg, frames...)
	if err := seg.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := seg.LaneCount(); got != 1 {
		t.Errorf("lane count = %d, want 1 (no lane opened; frames fell to default)", got)
	}
	if len(rec.batches()) != 1 {
		t.Errorf("transcribe calls = %d, want 1 (degraded but still transcribed)", len(rec.batches()))
	}
	if len(finals) != 1 || finals[0].SpeakerID != "" {
		t.Errorf("STTFinals = %+v, want one unattributed (SpeakerID \"\") default-lane final", finals)
	}
	mu.Lock()
	gotErrs := errs
	mu.Unlock()
	if gotErrs != 1 {
		t.Errorf("factory error reported %d times, want exactly 1 (memoized single-shot degrade)", gotErrs)
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

// TestSegmenter_TeardownRaceWithFeed is finding 6: Bind's teardown runs concurrently
// with an audio loop still calling Feed. Lane cancel/close funcs are captured and
// nulled UNDER mu (and the lane dropped) in both teardown and reap, so no lane is
// double-closed and no field is touched unlocked. Run under `go test -race`.
func TestSegmenter_TeardownRaceWithFeed(t *testing.T) {
	bus := voiceevent.NewBus()
	rec := &recordingRecognizer{}
	closes := 0
	factory, _ := laneVADFactory(bus, &closes, nil)
	seg := orchestrator.NewSegmenter(orchestrator.NewVAD(bus, &contentVAD{}), orchestrator.NewSTT(bus, rec))
	seg.SetLaneVADFactory(factory)
	cancel := seg.Bind(t.Context(), bus)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		// Voiced-only frames keep every lane listening (no flush → no jobs send that
		// could race the teardown's close(jobs)); the point is the concurrent lane
		// map + close-func access, which must stay mu-guarded.
		for i := 0; i < 500; i++ {
			_ = seg.Process(laneFrame(t, "A", 100))
			_ = seg.Process(laneFrame(t, "B", 200))
		}
	}()
	time.Sleep(time.Millisecond) // let the loop open the lanes
	cancel()                     // teardown while Feed is still running
	wg.Wait()
}
