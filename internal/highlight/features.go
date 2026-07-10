package highlight

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/MrWong99/Glyphoxa/internal/tape"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
)

// frameFeature is the cheap per-frame audio summary the PCM tap computes inline
// (on the audio-loop goroutine) and hands to the worker: the sum of squared
// normalized samples (for RMS energy), the zero-crossing count (a rough
// voiced/excited-delivery proxy), and the sample count, tagged with the frame's
// Speaker Lane (ADR-0050). It carries aggregates, not the samples, so the worker
// folds it in O(1) without touching the PCM buffer.
type frameFeature struct {
	lane       string
	sumSquares float64
	zeroCross  int
	samples    int
}

// computeFrameFeature summarizes one decoded PCM frame. It only reads the frame's
// samples (no allocation, no mutation), so it is safe to run inline on the audio
// loop before the frame is handed to the tap mailbox.
func computeFrameFeature(f audio.Frame) frameFeature {
	s := f.Samples()
	ff := frameFeature{lane: f.Speaker(), samples: len(s)}
	for i, v := range s {
		x := float64(v) / 32768.0
		ff.sumSquares += x * x
		if i > 0 && (s[i-1] < 0) != (v < 0) {
			ff.zeroCross++
		}
	}
	return ff
}

// laneAccum accumulates one lane's audio energy since the last classification.
type laneAccum struct {
	sumSquares float64
	zeroCross  int
	samples    int
}

// featureState is the worker-owned per-lane accumulator set. It is touched only by
// the single worker goroutine, so it needs no lock.
type featureState map[string]*laneAccum

// fold accumulates one frame's features into its lane.
func (fs featureState) fold(ff frameFeature) {
	a := fs[ff.lane]
	if a == nil {
		a = &laneAccum{}
		fs[ff.lane] = a
	}
	a.sumSquares += ff.sumSquares
	a.zeroCross += ff.zeroCross
	a.samples += ff.samples
}

// summarize renders the per-lane RMS energy and zero-crossing rate accumulated
// since the last call, then RESETS the accumulators so the next window measures
// only its own audio. Lanes are sorted by id for a deterministic prompt (ADR-0021).
// With no audio captured it returns a fixed "(no audio captured)" line so the
// prompt is still stable.
func (fs featureState) summarize() string {
	var b strings.Builder
	b.WriteString("Audio energy since last check:\n")
	if len(fs) == 0 {
		b.WriteString("(no audio captured)\n")
		return b.String()
	}
	lanes := make([]string, 0, len(fs))
	for lane := range fs {
		lanes = append(lanes, lane)
	}
	sort.Strings(lanes)
	for _, lane := range lanes {
		a := fs[lane]
		var rms, zcr float64
		if a.samples > 0 {
			rms = math.Sqrt(a.sumSquares / float64(a.samples))
			zcr = float64(a.zeroCross) / float64(a.samples)
		}
		fmt.Fprintf(&b, "- %s: RMS %.3f, zero-crossing rate %.3f\n", laneLabel(lane), rms, zcr)
	}
	// Reset for the next window.
	for lane := range fs {
		delete(fs, lane)
	}
	return b.String()
}

// laneLabel renders a lane id for the prompt: the agent (TTS) lane by name, a
// Speaker Lane as "Speaker <id>", and the unattributed lane as "Speaker".
func laneLabel(lane string) string {
	switch lane {
	case "":
		return "Speaker"
	case tape.AgentLaneID:
		return "Agent"
	default:
		return "Speaker " + lane
	}
}
