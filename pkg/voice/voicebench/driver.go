package voicebench

import (
	"context"

	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
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
}

// NewDriver builds a Driver. tap may be nil on a tier that takes no recorder
// spans (then only the bus-derived stages populate). silence must be a
// clip-format frame of zeros and silenceFrames must exceed the VAD's
// minSilenceFrames so speech-end fires.
func NewDriver(conv *orchestrator.Conversation, h *voicetest.Harness, tap *recorderTap, acc *Accumulator, silence audio.Frame, silenceFrames int) *Driver {
	return &Driver{conv: conv, harness: h, tap: tap, acc: acc, silence: silence, silenceFrames: silenceFrames}
}

// RunClip feeds one clip's frames through the conversation, appends trailing
// silence to provoke a natural speech-end, flushes any utterance still buffered,
// and folds the resulting turn into the accumulator. ctx governs the reactive
// stages (STT/TTS calls the reactors trigger). It returns the first Feed/Flush
// error, if any — a provider failure mid-clip aborts that clip rather than
// recording a bogus span.
//
// One clip == one turn for the corpus (each clip is a single utterance); the
// harness event slice is snapshotted AFTER Flush so a late tee-goroutine
// FirstAudio publish is included (the Harness locks its slice, so the snapshot
// is race-safe).
func (d *Driver) RunClip(ctx context.Context, frames []audio.Frame) error {
	cancel := d.conv.Register(ctx)
	defer cancel()

	for _, f := range frames {
		if err := d.conv.Feed(f); err != nil {
			return err
		}
	}
	for i := 0; i < d.silenceFrames; i++ {
		if err := d.conv.Feed(d.silence); err != nil {
			return err
		}
	}
	if err := d.conv.Flush(); err != nil {
		return err
	}

	d.acc.AddTurnWithRecorder(d.harness.Events(), d.tap)
	return nil
}
