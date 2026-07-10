package highlight

import (
	"context"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// capturingProvider records the user message of every request it replays, so a test
// can assert what the classifier prompt carried.
type capturingProvider struct {
	mu       sync.Mutex
	lastUser string
	resp     string
}

func (p *capturingProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			p.lastUser = m.Text
		}
	}
	p.mu.Unlock()
	ch := make(chan llm.StreamEvent, 2)
	ch <- llm.StreamEvent{Type: llm.EventText, Text: p.resp}
	ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: "end_turn"}
	close(ch)
	return ch, nil
}

func (p *capturingProvider) userPrompt() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastUser
}

// loudFrame builds a decoded PCM frame with alternating full-scale samples (high
// energy AND high zero-crossing rate), attributed to a Speaker Lane.
func loudFrame(t *testing.T, speaker string) audio.Frame {
	t.Helper()
	samples := make([]int16, 480) // 10ms @ 48kHz
	for i := range samples {
		if i%2 == 0 {
			samples[i] = 30000
		} else {
			samples[i] = -30000
		}
	}
	f, err := audio.NewFrame(samples, 48000, 10)
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}
	return f.WithSpeaker(speaker)
}

// TestPCMFeaturesInPrompt (TEST 8): the per-lane RMS energy and zero-crossing
// summary computed from the PCM tap lands in the classifier prompt.
func TestPCMFeaturesInPrompt(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &capturingProvider{resp: scoreJSON(1.0)}
	sink := &fakeSink{}
	d, clk, classified := buildDetector(t, bus, prov, nil, sink, allowGate{}, Config{ClassifyEvery: 2})

	// Feed loud frames through the tap, then let the worker drain them.
	tap := d.PCMTap()
	for i := 0; i < 50; i++ {
		tap(loudFrame(t, "A"))
	}
	time.Sleep(100 * time.Millisecond)

	publishFinals(t, d, bus, clk, "A", 2)
	awaitClassifications(t, classified, 1)

	got := prov.userPrompt()
	if !strings.Contains(got, "Audio energy since last check") {
		t.Fatalf("prompt missing audio-energy summary:\n%s", got)
	}
	if !strings.Contains(got, "Speaker A: RMS") {
		t.Fatalf("prompt missing lane A energy line:\n%s", got)
	}
	if strings.Contains(got, "(no audio captured)") {
		t.Errorf("prompt reports no audio despite fed frames:\n%s", got)
	}
}

// TestPCMTapNeverBlocks (TEST 8 cont.): the tap returns promptly under a flood even
// though the single worker cannot keep up — frames drop, the audio loop never waits.
func TestPCMTapNeverBlocks(t *testing.T) {
	bus := voiceevent.NewBus()
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(1.0) }}
	sink := &fakeSink{}
	d, _, _ := buildDetector(t, bus, prov, nil, sink, allowGate{}, Config{})

	tap := d.PCMTap()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100000; i++ {
			tap(loudFrame(t, "A"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("PCM tap blocked under flood")
	}
}

// TestComputeFrameFeature is the unit proof of the per-frame RMS/ZCR math.
func TestComputeFrameFeature(t *testing.T) {
	f := loudFrame(t, "A")
	ff := computeFrameFeature(f)
	if ff.lane != "A" {
		t.Errorf("lane = %q, want A", ff.lane)
	}
	if ff.samples != 480 {
		t.Errorf("samples = %d, want 480", ff.samples)
	}
	// Full-scale alternating ⇒ RMS ≈ 30000/32768, ZCR ≈ 1 per sample step.
	rms := math.Sqrt(ff.sumSquares / float64(ff.samples))
	if rms < 0.85 || rms > 0.95 {
		t.Errorf("RMS = %.3f, want ~0.915", rms)
	}
	if ff.zeroCross != 479 {
		t.Errorf("zeroCross = %d, want 479", ff.zeroCross)
	}
}
