package orchestrator

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/stt"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// silentSession is a [vad.SessionHandle] that reports silence for every frame — the
// cap test only needs lanes to be CREATED (which happens on the first frame from a
// new speaker), not segmented.
type silentSession struct{}

func (silentSession) ProcessFrame(audio.Frame) (vad.VADEvent, error) {
	return vad.VADEvent{Type: vad.VADSilence}, nil
}
func (silentSession) Reset()       {}
func (silentSession) Close() error { return nil }

func capTestFrame(t *testing.T, speaker string) audio.Frame {
	t.Helper()
	f, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		t.Fatalf("audio.NewFrame: %v", err)
	}
	return f.WithSpeaker(speaker)
}

// TestSegmenter_LaneStreamCap is step 11 (ADR-0050): with a per-lane stream cap of
// 1, the first Speaker Lane opens a StreamManager; the second lane exceeds the cap
// and is pure batch (no stream) — so concurrent sockets track concurrent speakers,
// not channel size.
func TestSegmenter_LaneStreamCap(t *testing.T) {
	bus := voiceevent.NewBus()
	sttStage := NewSTT(bus, stubRec{})
	seg := NewSegmenter(NewVAD(bus, silentSession{}), sttStage)
	seg.laneVADFactory = func() (*VAD, func(), error) {
		return NewVAD(bus, silentSession{}), func() {}, nil
	}
	var factoryCalls int
	seg.laneStreamFactory = func(speakerID string) *StreamManager {
		factoryCalls++
		return NewStreamManager(&fakeStreamingRecognizer{}, WithStreamSpeakerID(speakerID))
	}
	seg.maxStreamLanes = 1
	t.Cleanup(seg.Bind(t.Context(), bus))

	if err := seg.Process(capTestFrame(t, "A")); err != nil { // opens lane A (under cap → stream)
		t.Fatalf("Process A: %v", err)
	}
	if err := seg.Process(capTestFrame(t, "B")); err != nil { // opens lane B (over cap → batch)
		t.Fatalf("Process B: %v", err)
	}

	seg.mu.Lock()
	laneA, laneB := seg.lanes["A"], seg.lanes["B"]
	streamLanes := seg.streamLanes
	seg.mu.Unlock()

	if laneA == nil || laneA.stream == nil {
		t.Error("lane A (first, under cap) has no stream, want one")
	}
	if laneB == nil || laneB.stream != nil {
		t.Error("lane B (over cap) has a stream, want pure batch (nil)")
	}
	if factoryCalls != 1 {
		t.Errorf("lane stream factory called %d times, want 1 (cap honoured)", factoryCalls)
	}
	if streamLanes != 1 {
		t.Errorf("streamLanes = %d, want 1", streamLanes)
	}
}

// stubRec is a minimal [stt.Recognizer] for the cap test — its output is never
// asserted (no utterance is segmented).
type stubRec struct{}

func (stubRec) Transcribe(_ context.Context, _ []audio.Frame) (stt.Transcript, error) {
	return stt.Transcript{}, nil
}
