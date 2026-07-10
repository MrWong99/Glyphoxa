package highlight

import (
	"context"
	"math"
	"strconv"
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

// taggedFrame builds a decoded PCM frame (480 samples @ 48kHz/10ms) whose Speaker
// Lane carries the send index as a string, so a drained frameFeature's lane proves
// which tap call produced it — the ordering proof TestPCMTapDropsOldestWhenSaturated
// depends on.
func taggedFrame(t *testing.T, lane string) audio.Frame {
	t.Helper()
	samples := make([]int16, 480) // 10ms @ 48kHz
	f, err := audio.NewFrame(samples, 48000, 10)
	if err != nil {
		t.Fatalf("NewFrame: %v", err)
	}
	return f.WithSpeaker(lane)
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

// stalledTapDetector builds a detector whose worker never starts, so nothing ever
// drains d.features — the deterministic worst case for the PCM tap's saturated
// mailbox path. It does NOT subscribe to a bus and does NOT launch the worker
// goroutine, so cleanup must be d.cancel (NOT d.Close: Close calls d.unsub, which
// is nil here, and then blocks forever on <-d.done since the worker never runs to
// close it).
func stalledTapDetector(t *testing.T) *Detector {
	t.Helper()
	sink := &fakeSink{}
	prov := &fakeProvider{respJSON: func(int) string { return scoreJSON(1.0) }}
	d := newDetector(prov, "", nil, sink, allowGate{}, nil, nil, Config{})
	t.Cleanup(d.cancel)
	return d
}

// TestPCMTapDropsOldestWhenSaturated (ADR-0051): once the feature mailbox is full,
// the tap sheds the OLDEST entries to make room for the newest, never the reverse.
// Pin the exact shed semantics: feed featureMailboxCap+64 tagged frames one at a
// time on this goroutine (no consumer, no concurrent tap caller — see the ordering
// note below), then drain whatever is left and assert it is exactly the newest
// featureMailboxCap frames (indices 64..cap+63) in send order.
//
// Ordering note: this assertion is valid ONLY because nothing else touches
// d.features concurrently (no worker started, single-goroutine feed). If a future
// change starts the worker (or taps from another goroutine) against this fixture,
// the interleaving becomes nondeterministic and this exact-order assertion breaks
// silently — keep this test on the stalled, single-writer fixture.
func TestPCMTapDropsOldestWhenSaturated(t *testing.T) {
	d := stalledTapDetector(t)
	tap := d.PCMTap()

	const n = featureMailboxCap + 64
	frames := make([]audio.Frame, n)
	for i := 0; i < n; i++ {
		frames[i] = taggedFrame(t, strconv.Itoa(i))
	}
	for i := 0; i < n; i++ {
		tap(frames[i])
	}

	var got []string
	for {
		select {
		case ff := <-d.features:
			got = append(got, ff.lane)
		default:
			goto drained
		}
	}
drained:
	if len(got) != featureMailboxCap {
		t.Fatalf("drained %d features, want %d (mailbox cap)", len(got), featureMailboxCap)
	}
	for i, lane := range got {
		want := strconv.Itoa(64 + i)
		if lane != want {
			t.Fatalf("features[%d].lane = %q, want %q (oldest 64 should have been shed)", i, lane, want)
		}
	}
}

// TestPCMTapNeverBlocks (TEST 8 cont.): the tap returns for every frame even when
// nothing drains its feature mailbox — the full-mailbox path drops the oldest and
// hands back control, so the audio loop never waits (ADR-0020 best-effort signal).
//
// The proof is by synchronization, not wall-clock: a detector whose worker never
// consumes the feature mailbox (the deterministic worst case for a blocking tap)
// is flooded with far more frames than the mailbox can hold, from a goroutine that
// closes `done` when it finishes. If the tap ever blocked on the channel, that
// goroutine would park forever and `done` would never close; the select below is a
// FAIL-ONLY guard against that regression, not a pass-path timing measurement — the
// pass path does bounded, sleep-free work and returns as soon as `done` closes.
// (The old timer-raced 100k-frame flood measured frame-build throughput under CI
// load, not the non-blocking property, and blew its 3s bound on starved runners —
// see #383.)
func TestPCMTapNeverBlocks(t *testing.T) {
	d := stalledTapDetector(t)
	tap := d.PCMTap()

	// Pre-build every frame on the test goroutine before spawning the flood
	// goroutine, so the flood's per-call cost is fixed and t.* stays off the
	// spawned goroutine (a t.Fatalf there would not fail the test correctly).
	const n = featureMailboxCap + 64
	frames := make([]audio.Frame, n)
	for i := 0; i < n; i++ {
		frames[i] = taggedFrame(t, strconv.Itoa(i))
	}

	done := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			tap(frames[i]) // would park forever here if the tap ever blocked
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("PCM tap blocked on a saturated, undrained mailbox")
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
