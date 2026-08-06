package wire

// White-box tests for the Idle Close activity signal (ADR-0061). They live in
// `package wire` so they can drive the unexported run loop with a test-owned
// inbound channel and a hand-fired silence clock — the same rig the #91 silence
// tests use, and for the same reason: no Discord Session and no wall-clock waits.

import (
	"sync/atomic"
	"testing"
	"time"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
)

// TestPipeline_ActivityTapFiresOncePerRealAudioFrame is the load-bearing case for
// the whole feature: the watchdog closes a Voice Session that stops marking, so
// the mark must fire on exactly the packets that mean "somebody is talking".
//
// The two negatives matter more than the positive. Discord's explicit Opus
// silence frames are the speaker-STOP signal (issue #147), so a table that went
// quiet must not keep marking; and the synthesized silence clock keeps ticking at
// 31 Hz through a completely empty channel forever, so a signal that fired on
// ticks would never let a session go idle at all — the feature would be dead in
// production while this test passed.
func TestPipeline_ActivityTapFiresOncePerRealAudioFrame(t *testing.T) {
	conv, _, frames := newSilenceRig(t, "hello there")
	codec := &replayCodec{frames: frames}
	clk := &fakeSilenceClock{ch: make(chan time.Time)}

	var marks atomic.Int64
	pipe := NewPipeline(conv, codec, nil, "guild", nil,
		withSilenceClock(testSampleRate, testFrameMs, func() silenceClock { return clk }),
		WithInboundActivityTap(func() { marks.Add(1) }))

	inbound := make(chan gxvoice.Frame)
	done := make(chan error, 1)
	go func() { done <- pipe.run(t.Context(), inbound) }()

	feedSpeech(inbound, frames)

	// The speaker stops: Discord's stop-silence frames must NOT mark.
	//
	// The FIRST silence send doubles as the read barrier for the speech frames.
	// inbound is unbuffered and the loop processes one frame to completion before
	// selecting again, so a send that has returned proves the PREVIOUS frame's tap
	// already ran. Reading the counter straight after feedSpeech would instead race
	// the last frame's tap and flake as len(frames)-1.
	inbound <- gxvoice.Frame{Silence: true}
	if got, want := marks.Load(), int64(len(frames)); got != want {
		t.Fatalf("marks after %d real frames = %d, want %d (one per processed packet)", len(frames), got, want)
	}
	for range discordStopSilenceFrames - 1 {
		inbound <- gxvoice.Frame{Silence: true}
	}
	if got, want := marks.Load(), int64(len(frames)); got != want {
		t.Errorf("marks after %d Discord silence frames = %d, want %d unchanged: a silence frame says the speaker STOPPED, so it is not audio",
			discordStopSilenceFrames, got, want)
	}

	// Neither must the synthesized silence clock, which ticks forever in an empty
	// channel — this is the assertion that keeps Idle Close from being a no-op.
	for range testHangoverTicks {
		clk.tick()
	}
	if got, want := marks.Load(), int64(len(frames)); got != want {
		t.Errorf("marks after %d silence-clock ticks = %d, want %d unchanged: the clock synthesizes silence with nothing on the wire, so a session that ticks is still idle",
			testHangoverTicks, got, want)
	}

	// The definitive read: the loop has exited, so nothing can still be marking.
	close(inbound)
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := marks.Load(), int64(len(frames)); got != want {
		t.Fatalf("marks after the loop exited = %d, want %d — exactly one per real inbound frame and nothing else", got, want)
	}
}

// TestPipeline_NoActivityTapIsUnchanged pins the feature-off path: a Voice
// Instance with no watchdog wires no tap, and the audio loop must not so much as
// nil-check on the hot path (it does, once per frame — the point here is that the
// loop still runs and decodes exactly as before).
func TestPipeline_NoActivityTapIsUnchanged(t *testing.T) {
	conv, _, frames := newSilenceRig(t, "hello there")
	codec := &replayCodec{frames: frames}
	clk := &fakeSilenceClock{ch: make(chan time.Time)}
	pipe := NewPipeline(conv, codec, nil, "guild", nil,
		withSilenceClock(testSampleRate, testFrameMs, func() silenceClock { return clk }))

	inbound := make(chan gxvoice.Frame)
	done := make(chan error, 1)
	go func() { done <- pipe.run(t.Context(), inbound) }()
	feedSpeech(inbound, frames)
	close(inbound)
	if err := <-done; err != nil {
		t.Fatalf("run with no activity tap: %v", err)
	}
	if got, want := codec.decodeCount(), len(frames); got != want {
		t.Fatalf("decodes = %d, want %d: an unwired activity tap must not change the inbound loop", got, want)
	}
}
