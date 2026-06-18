package voicebench

import (
	"context"
	"fmt"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicetest"
)

// Driver runs clips through a real [orchestrator.Conversation] and folds each
// turn's stage spans into an [Accumulator]. It is tier-agnostic: the caller
// supplies an already-wired Conversation (cassette providers for the keyless
// tier, live ones for the -tags=live tier) plus the [voicetest.Harness] whose
// bus that Conversation publishes on, and the [recorderTap] installed as the
// orchestrator's StageRecorder. The Driver only drives audio and harvests — it
// owns no provider wiring, so it stays free of CGO/key concerns (those live in
// the tier-specific rig that constructs the Conversation).
type Driver struct {
	conv    *orchestrator.Conversation
	harness *voicetest.Harness
	tap     *recorderTap
	acc     *Accumulator

	// silence is one frame of digital silence sized to the clip format, appended
	// after a clip so the real VAD sees sustained quiet and fires VADSpeechEnd
	// naturally — putting the ~480 ms hangover INSIDE the measured budget (plan
	// §5). silenceFrames is how many to append (must exceed minSilenceFrames).
	silence       audio.Frame
	silenceFrames int

	// realtime paces Feed at each frame's wall-clock duration instead of feeding
	// the whole clip as fast as the CPU allows. It is the live-tier fidelity fix:
	// the corpus clips are continuous monologues the VAD splits into several
	// utterances, and fast-feeding stamps every VADSpeechEnd within the same few
	// milliseconds (clip-start). Because the reply runs synchronously on the single
	// transcription worker (the bench wires no floor), utterance N's turn then
	// waits behind every prior turn — so its response_latency (firstOpus −
	// speechEndAt) accrues all of their wall-clock, a pure fast-feed artifact that
	// inflated the live p95 to ~10s while the REAL per-turn latency (~STT + first
	// sentence + tts ≈ 1.2s) is well inside budget. Pacing at real time spaces the
	// utterances by their true durations, so the worker drains each turn before the
	// next utterance's speech-end fires — measuring the user-facing per-turn latency
	// rather than a serialization queue. The cassette tier leaves it false (its
	// canned replay has no real per-turn cost to serialize).
	realtime bool
}

// NewDriver builds a Driver. tap may be nil on a tier that takes no recorder
// spans (then only the bus-derived stages populate). silence must be a
// clip-format frame of zeros and silenceFrames must exceed the VAD's
// minSilenceFrames so speech-end fires.
func NewDriver(conv *orchestrator.Conversation, h *voicetest.Harness, tap *recorderTap, acc *Accumulator, silence audio.Frame, silenceFrames int) *Driver {
	return &Driver{conv: conv, harness: h, tap: tap, acc: acc, silence: silence, silenceFrames: silenceFrames}
}

// headlineTimeout bounds how long RunClip polls the tap for the turn's
// response_latency sample after Flush — a HANG GUARD, not a per-turn latency
// budget. Without barge-in the orchestrator drives the whole turn (STT → reply →
// TTS) on the transcription worker that Flush drains (#24), so by the time the
// barrier polls, the sample has already landed and the wait is ~0 regardless of
// how slow the turn was (verified: a 6s reply still resolves with zero poll-wait —
// Flush blocks for the 6s, the barrier then finds the sample immediately). So this is NOT the
// live SLO ceiling and must not be confused with it; it only fires if a turn
// never produces audio at all (a wedged provider/tee), where a hard error — a
// clip that yields no headline metric is exactly the silent-drop the bench
// exists to catch — beats hanging the run forever.
//
// It must comfortably outlast the SLOWEST legitimate turn: a rambling live reply
// can take ~9 s of LLM generation + serial TTS drain, and the dice clip runs two
// LLM rounds before first audio. At 5 s the guard fired on a valid-but-slow turn
// and aborted the WHOLE run (losing every sample) — the opposite of a hang guard.
// 30 s only trips on a genuinely wedged provider/tee, never on a slow-but-live one.
const headlineTimeout = 30 * time.Second

// RunClip feeds one clip's frames through the conversation, appends trailing
// silence to provoke a natural speech-end, flushes any utterance still buffered,
// waits for the turn's headline response_latency sample to land on the tap, and
// folds the turn's tap-captured spans into the accumulator. ctx governs the
// reactive stages (STT/TTS calls the reactors trigger). It returns the first
// Feed/Flush error, if any — a provider failure mid-clip aborts that clip rather
// than recording a bogus span.
//
// A clip may segment into one OR MORE turns (a multi-utterance clip the VAD
// splits). The completion barrier waits until the tap has one response_latency
// sample for EVERY headline-eligible turn the clip produced — not just the
// first — because turns 2..N publish FirstAudio off the tee's forward goroutine
// asynchronously, and draining on the first sample would discard the stragglers
// (each clip gets a fresh tap, so a lost sample is gone, not deferred). The
// eligible-turn count is read from the harness event log (STTFinal with a
// non-zero SpeechEndAt — the exact set the subscriber records a headline for,
// since alwaysRoute always supplies the role). Polling that exact count ties the
// barrier to real work and removes the race.
func (d *Driver) RunClip(ctx context.Context, frames []audio.Frame) error {
	cancel := d.conv.Register(ctx)
	defer cancel()

	// pace returns how long to sleep before feeding the i-th frame of the run so
	// the whole sequence is fed at wall-clock speed (zero on the fast cassette
	// path). It schedules against a fixed start so per-frame sleep jitter does not
	// accumulate into drift.
	start := time.Now()
	var elapsed time.Duration
	pace := func(f audio.Frame) {
		if !d.realtime {
			return
		}
		elapsed += frameDuration(f)
		if wait := elapsed - time.Since(start); wait > 0 {
			time.Sleep(wait)
		}
	}

	for _, f := range frames {
		pace(f)
		if err := d.conv.Feed(f); err != nil {
			return err
		}
	}
	for i := 0; i < d.silenceFrames; i++ {
		pace(d.silence)
		if err := d.conv.Feed(d.silence); err != nil {
			return err
		}
	}
	if err := d.conv.Flush(); err != nil {
		return err
	}

	n, err := d.waitHeadlines(ctx)
	if err != nil {
		return err
	}
	d.acc.AddTurns(d.tap, n)
	return nil
}

// waitHeadlines blocks until the tap has captured a response_latency sample for
// each headline-eligible turn the clip produced, returning that turn count. A
// timeout is a hard error: a clip whose eligible turns don't all yield a
// headline metric is exactly the silent-drop the bench exists to catch. A nil
// tap (a tier that takes no recorder) skips the barrier and reports zero turns.
func (d *Driver) waitHeadlines(ctx context.Context) (int, error) {
	if d.tap == nil {
		return 0, nil
	}
	want := eligibleTurns(d.harness.Events())
	if want == 0 {
		return 0, fmt.Errorf("clip produced no headline-eligible turn (no STTFinal with SpeechEndAt)")
	}
	deadline := time.Now().Add(headlineTimeout)
	for {
		if got := len(d.tap.samples(StageResponseLatency)); got >= want {
			return want, nil
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("only %d/%d response_latency samples within %s (a turn produced no audible reply)",
				len(d.tap.samples(StageResponseLatency)), want, headlineTimeout)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// frameDuration is a frame's wall-clock duration (samples ÷ sample-rate). A
// zero-sample or zero-rate frame is zero, so an unsized frame never blocks the
// paced feed. Used by [Driver.RunClip] to feed the live tier at real time.
func frameDuration(f audio.Frame) time.Duration {
	n, rate := len(f.Samples()), f.SampleRate()
	if n == 0 || rate <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second / time.Duration(rate)
}

// eligibleTurns counts the turns in an event log that the subscriber records a
// headline (response_latency) for: an STTFinal carrying a non-zero SpeechEndAt
// (the subscriber skips a zero SpeechEndAt). With the rig's alwaysRoute the role
// is always known, so this STTFinal set is exactly the headline-eligible set.
func eligibleTurns(events []voiceevent.Event) int {
	n := 0
	for _, e := range events {
		if f, ok := e.(voiceevent.STTFinal); ok && !f.SpeechEndAt.IsZero() {
			n++
		}
	}
	return n
}
