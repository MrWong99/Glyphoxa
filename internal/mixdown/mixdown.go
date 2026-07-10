// Package mixdown turns a rollover-tape [Snapshot] — per-speaker runs of Opus
// frames stamped with arrival wall-clock — into a single mono 16-bit PCM WAV
// clip. It is pure DSP (ADR-0048: bytes out, no storage knowledge) and knows
// nothing about the live voice pipeline: it decodes each lane with a FRESH
// decoder so it never disturbs the live per-speaker decoder state, aligns lanes
// on arrival wall-clock (per-speaker PTS is not session-global), sums with
// int32 accumulation + clamp, and encodes one clip.
//
// The default decoder links libopus and is built only under `-tags opus`
// (decode_opus.go); the default build (decode_stub.go) reports
// [ErrDecoderUnavailable]. Callers may inject their own [DecoderFactory] via
// [Options] — the deterministic test suite does exactly that, so the alignment
// and mixing DSP is exercised in the plain `go test ./...` build.
package mixdown

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/wire/codec/dsp"
)

// ErrDecoderUnavailable is returned when [WAVClip] needs a decoder but none was
// injected and the build lacks the real one (no `-tags opus`).
var ErrDecoderUnavailable = errors.New("mixdown: opus decoder unavailable (build with -tags opus or inject Options.Decoder)")

// decodeRate is the sample rate the decoder emits and the internal mix runs at.
// libopus decodes Discord Opus to 48 kHz mono; the output is resampled from
// here to Options.SampleRate.
const decodeRate = 48000

// frameSamples is one 20 ms Opus frame at the decode rate (960 samples), the
// cadence a run is laid at.
const frameSamples = decodeRate * 20 / 1000 // 960

// runGap is the maximum gap between consecutive frames in a lane that still
// counts as one contiguous run. A larger gap starts a new run at its own
// wall-clock offset. (Discord stops sending frames during silence.)
const runGapMillis = 100

// Decoder decodes one Opus frame to mono 48 kHz int16 PCM. It is stateful (an
// Opus decoder tracks inter-frame state), so one Decoder serves exactly one
// lane.
type Decoder interface {
	Decode(opus []byte) ([]int16, error)
}

// DecoderFactory builds a fresh [Decoder]. WAVClip calls it once per lane so no
// two lanes share decoder state and the live codec's decoders are never
// touched. A nil Options.Decoder falls back to [defaultDecoderFactory], which
// the build tag selects.
type DecoderFactory func() (Decoder, error)

// defaultDecoderFactory is set by the build-tagged decode_opus.go / decode_stub.go.
var defaultDecoderFactory DecoderFactory

// Options parameterizes the clip. SampleRate is the output rate (default 48000;
// 24000 / 16000 are produced via [dsp.Resampler]). Decoder overrides the
// build-selected default; leave nil to use libopus (under -tags opus).
type Options struct {
	SampleRate int
	Decoder    DecoderFactory
}

// WAVClip decodes and mixes every lane of snap into one mono 16-bit PCM WAV
// clip. Lanes are aligned on arrival wall-clock and summed with int32
// accumulation + clamp to int16 so overlapping speakers never wrap around.
// Gaps render as silence. The clip's length is exactly (snap.To - snap.From) at
// the output rate.
func WAVClip(snap Snapshot, opts Options) ([]byte, error) {
	outRate := opts.SampleRate
	if outRate <= 0 {
		outRate = decodeRate
	}
	factory := opts.Decoder
	if factory == nil {
		factory = defaultDecoderFactory
	}

	// Internal mix buffer at the decode rate.
	internalLen := samplesFor(snap, decodeRate)
	accum := make([]int32, internalLen)

	for _, lane := range snap.Lanes {
		if err := mixLane(accum, lane, snap.From, factory); err != nil {
			return nil, err
		}
	}

	mixed := make([]int16, internalLen)
	for i, v := range accum {
		mixed[i] = clamp16(v)
	}

	out := mixed
	if outRate != decodeRate {
		out = dsp.NewResampler(decodeRate, outRate).Process(mixed)
		out = fitLen(out, samplesFor(snap, outRate))
	}

	return encodeWAV(out, outRate), nil
}

// mixLane decodes lane with a fresh decoder, aligns its frames into runs, and
// accumulates the result into accum. Within a lane a later frame at the same
// offset overwrites an earlier one (later-frame-wins); across lanes results sum.
func mixLane(accum []int32, lane LaneSnapshot, from time.Time, factory DecoderFactory) error {
	if len(lane.Frames) == 0 {
		return nil
	}
	if factory == nil {
		return ErrDecoderUnavailable
	}
	dec, err := factory()
	if err != nil {
		return err
	}

	frames := make([]Frame, len(lane.Frames))
	copy(frames, lane.Frames)
	sort.SliceStable(frames, func(i, j int) bool { return frames[i].At.Before(frames[j].At) })

	// Per-lane buffer with overwrite (later-frame-wins) semantics, folded into
	// accum once at the end.
	lbuf := make([]int32, len(accum))
	written := make([]bool, len(accum))

	runStart := 0 // sample offset of the current run's first frame
	k := 0        // frame index within the current run
	for i, f := range frames {
		pcm, derr := dec.Decode(f.Opus)
		if derr != nil {
			return derr
		}
		if i == 0 || f.At.Sub(frames[i-1].At).Milliseconds() > runGapMillis {
			runStart = offsetSamples(from, f.At, decodeRate)
			k = 0
		}
		pos := runStart + k*frameSamples
		for j, s := range pcm {
			idx := pos + j
			if idx < 0 || idx >= len(lbuf) {
				continue
			}
			lbuf[idx] = int32(s) // overwrite: later frame wins
			written[idx] = true
		}
		k++
	}

	for i := range accum {
		if written[i] {
			accum[i] += lbuf[i]
		}
	}
	return nil
}

// encodeWAV wraps mono 16-bit PCM samples in a canonical 44-byte RIFF/WAVE
// header (stdlib encoding/binary, no ogg dependency).
func encodeWAV(samples []int16, sampleRate int) []byte {
	const (
		numChannels   = 1
		bitsPerSample = 16
	)
	dataSize := len(samples) * 2
	blockAlign := numChannels * bitsPerSample / 8
	byteRate := sampleRate * blockAlign

	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(36+dataSize))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], numChannels)
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(byteRate))
	binary.LittleEndian.PutUint16(buf[32:34], uint16(blockAlign))
	binary.LittleEndian.PutUint16(buf[34:36], bitsPerSample)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(dataSize))
	for i, s := range samples {
		binary.LittleEndian.PutUint16(buf[44+2*i:], uint16(s))
	}
	return buf
}

// samplesFor is the number of samples spanning snap's [From, To) window at rate.
func samplesFor(snap Snapshot, rate int) int {
	d := snap.To.Sub(snap.From)
	if d <= 0 {
		return 0
	}
	return int(math.Round(d.Seconds() * float64(rate)))
}

// offsetSamples is the sample index of at within the window starting at from.
func offsetSamples(from, at time.Time, rate int) int {
	return int(math.Round(at.Sub(from).Seconds() * float64(rate)))
}

// fitLen pads with silence or truncates s to exactly n samples, so the clip
// length is (To-From) at the output rate regardless of resampler rounding.
func fitLen(s []int16, n int) []int16 {
	switch {
	case len(s) == n:
		return s
	case len(s) > n:
		return s[:n]
	default:
		out := make([]int16, n)
		copy(out, s)
		return out
	}
}

func clamp16(v int32) int16 {
	switch {
	case v > 32767:
		return 32767
	case v < -32768:
		return -32768
	default:
		return int16(v)
	}
}
