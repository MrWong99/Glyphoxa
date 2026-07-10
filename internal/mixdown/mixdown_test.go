package mixdown

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// identityDecoder treats each frame's Opus payload as raw little-endian int16
// PCM (mono, 48 kHz). This decouples the mixdown DSP from libopus so the
// deterministic suite runs in the default (no -tags opus) build: a "frame" is
// simply the samples it carries. A fresh instance is handed out per lane by
// identityFactory, matching the fresh-decoder-per-lane contract.
type identityDecoder struct{}

func (identityDecoder) Decode(opus []byte) ([]int16, error) {
	n := len(opus) / 2
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(opus[2*i:]))
	}
	return out, nil
}

func identityFactory() (Decoder, error) { return identityDecoder{}, nil }

// constN returns n samples all equal to v.
func constN(n int, v int16) []int16 {
	s := make([]int16, n)
	for i := range s {
		s[i] = v
	}
	return s
}

// mustClip runs WAVClip and fails on error.
func mustClip(t *testing.T, snap Snapshot, opts Options) []byte {
	t.Helper()
	clip, err := WAVClip(snap, opts)
	if err != nil {
		t.Fatalf("WAVClip: %v", err)
	}
	return clip
}

// assertRegion checks that samples[start:start+n] all equal want.
func assertRegion(t *testing.T, samples []int16, start, n int, want int16) {
	t.Helper()
	if start+n > len(samples) {
		t.Fatalf("region [%d:%d] out of range (len %d)", start, start+n, len(samples))
	}
	for i := start; i < start+n; i++ {
		if samples[i] != want {
			t.Fatalf("sample[%d] = %d, want %d (region start %d)", i, samples[i], want, start)
		}
	}
}

// pcm encodes int16 samples as little-endian bytes — a synthetic Opus payload
// the identityDecoder round-trips back to the same samples.
func pcm(samples ...int16) []byte {
	b := make([]byte, len(samples)*2)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[2*i:], uint16(s))
	}
	return b
}

// samplesOf extracts the mono int16 PCM payload from a WAV clip (drops the
// 44-byte header).
func samplesOf(t *testing.T, clip []byte) []int16 {
	t.Helper()
	if len(clip) < 44 {
		t.Fatalf("clip shorter than header: %d", len(clip))
	}
	body := clip[44:]
	out := make([]int16, len(body)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(body[2*i:]))
	}
	return out
}

const outRate48k = 48000

func TestWAVClip_SingleRun20msCadence(t *testing.T) {
	base := time.Unix(2000, 0)
	snap := Snapshot{From: base, To: base.Add(time.Second)}
	// One lane, three frames each 20ms apart (< 100ms gap → one run). Each frame
	// carries 100 samples of a distinct value, shorter than the 960-sample 20ms
	// cadence, so a correct run leaves 860 zero samples between frame starts.
	start := base.Add(200 * time.Millisecond)
	snap.Lanes = []LaneSnapshot{{
		LaneID: "spk",
		Frames: []Frame{
			{Opus: pcm(constN(100, 1000)...), At: start},
			{Opus: pcm(constN(100, 2000)...), At: start.Add(20 * time.Millisecond)},
			{Opus: pcm(constN(100, 3000)...), At: start.Add(40 * time.Millisecond)},
		},
	}}

	got := samplesOf(t, mustClip(t, snap, Options{Decoder: identityFactory}))

	// Run starts at 200ms → sample 9600. Frames laid at 20ms (960-sample) cadence.
	assertRegion(t, got, 9600, 100, 1000)
	assertRegion(t, got, 9600+960, 100, 2000)
	assertRegion(t, got, 9600+2*960, 100, 3000)
	// Between the 100-sample payload and the next cadence slot is silence.
	assertRegion(t, got, 9700, 860, 0)
	// Before the run: silence.
	assertRegion(t, got, 0, 9600, 0)
}

func TestWAVClip_GapOver100msStartsNewRun(t *testing.T) {
	base := time.Unix(3000, 0)
	snap := Snapshot{From: base, To: base.Add(time.Second)}
	// Frame A at 100ms; frame B after a 150ms gap (>100ms) → new run laid at its
	// OWN wall-clock offset (350ms), not at cadence from A.
	a := base.Add(100 * time.Millisecond)
	b := a.Add(150 * time.Millisecond) // 250ms
	snap.Lanes = []LaneSnapshot{{
		LaneID: "spk",
		Frames: []Frame{
			{Opus: pcm(constN(100, 1000)...), At: a},
			{Opus: pcm(constN(100, 2000)...), At: b},
		},
	}}

	got := samplesOf(t, mustClip(t, snap, Options{Decoder: identityFactory}))

	assertRegion(t, got, 4800, 100, 1000)  // 100ms → sample 4800
	assertRegion(t, got, 12000, 100, 2000) // 250ms → sample 12000 (own offset, not cadence)
	// Gap between the two runs is silence.
	assertRegion(t, got, 4900, 7100, 0)
}

func TestWAVClip_MisorderedArrivalDeterministic(t *testing.T) {
	base := time.Unix(4000, 0)
	start := base.Add(100 * time.Millisecond)
	f0 := Frame{Opus: pcm(constN(100, 1000)...), At: start}
	f1 := Frame{Opus: pcm(constN(100, 2000)...), At: start.Add(20 * time.Millisecond)}
	f2 := Frame{Opus: pcm(constN(100, 3000)...), At: start.Add(40 * time.Millisecond)}

	ordered := Snapshot{From: base, To: base.Add(time.Second),
		Lanes: []LaneSnapshot{{LaneID: "spk", Frames: []Frame{f0, f1, f2}}}}
	shuffled := Snapshot{From: base, To: base.Add(time.Second),
		Lanes: []LaneSnapshot{{LaneID: "spk", Frames: []Frame{f2, f0, f1}}}}

	a := mustClip(t, ordered, Options{Decoder: identityFactory})
	b := mustClip(t, shuffled, Options{Decoder: identityFactory})

	if !bytes.Equal(a, b) {
		t.Fatalf("mis-ordered input produced different bytes; alignment not deterministic")
	}
	// And the run is laid in At order regardless of input order.
	got := samplesOf(t, a)
	assertRegion(t, got, 4800, 100, 1000)
	assertRegion(t, got, 4800+960, 100, 2000)
	assertRegion(t, got, 4800+2*960, 100, 3000)
}

func TestWAVClip_FullScaleCollisionClamps(t *testing.T) {
	base := time.Unix(5000, 0)
	at := base.Add(100 * time.Millisecond)
	snap := Snapshot{From: base, To: base.Add(time.Second), Lanes: []LaneSnapshot{
		{LaneID: "a", Frames: []Frame{{Opus: pcm(constN(100, 32767)...), At: at}}},
		{LaneID: "b", Frames: []Frame{{Opus: pcm(constN(100, 32767)...), At: at}}},
	}}

	got := samplesOf(t, mustClip(t, snap, Options{Decoder: identityFactory}))
	// 32767 + 32767 = 65534; naive int16 sum wraps to -2. int32 accumulate +
	// clamp must yield 32767, never a negative wraparound.
	assertRegion(t, got, 4800, 100, 32767)

	// Symmetric negative full-scale collision clamps to -32768.
	base2 := time.Unix(5100, 0)
	at2 := base2.Add(100 * time.Millisecond)
	snapNeg := Snapshot{From: base2, To: base2.Add(time.Second), Lanes: []LaneSnapshot{
		{LaneID: "a", Frames: []Frame{{Opus: pcm(constN(100, -32768)...), At: at2}}},
		{LaneID: "b", Frames: []Frame{{Opus: pcm(constN(100, -32768)...), At: at2}}},
	}}
	gotNeg := samplesOf(t, mustClip(t, snapNeg, Options{Decoder: identityFactory}))
	assertRegion(t, gotNeg, 4800, 100, -32768)
}

func TestWAVClip_HeaderBytewise(t *testing.T) {
	base := time.Unix(1000, 0)
	// 1 second window at 48 kHz mono = 48000 samples = 96000 data bytes.
	snap := Snapshot{From: base, To: base.Add(time.Second)}

	clip, err := WAVClip(snap, Options{Decoder: identityFactory})
	if err != nil {
		t.Fatalf("WAVClip: %v", err)
	}
	if len(clip) < 44 {
		t.Fatalf("clip too short for WAV header: %d bytes", len(clip))
	}

	const dataSize = 48000 * 2 // mono 16-bit, 1s @ 48k

	if got := string(clip[0:4]); got != "RIFF" {
		t.Errorf("ChunkID = %q, want RIFF", got)
	}
	if got := binary.LittleEndian.Uint32(clip[4:8]); got != uint32(36+dataSize) {
		t.Errorf("ChunkSize = %d, want %d", got, 36+dataSize)
	}
	if got := string(clip[8:12]); got != "WAVE" {
		t.Errorf("Format = %q, want WAVE", got)
	}
	if got := string(clip[12:16]); got != "fmt " {
		t.Errorf("Subchunk1ID = %q, want 'fmt '", got)
	}
	if got := binary.LittleEndian.Uint32(clip[16:20]); got != 16 {
		t.Errorf("Subchunk1Size = %d, want 16", got)
	}
	if got := binary.LittleEndian.Uint16(clip[20:22]); got != 1 {
		t.Errorf("AudioFormat = %d, want 1 (PCM)", got)
	}
	if got := binary.LittleEndian.Uint16(clip[22:24]); got != 1 {
		t.Errorf("NumChannels = %d, want 1", got)
	}
	if got := binary.LittleEndian.Uint32(clip[24:28]); got != 48000 {
		t.Errorf("SampleRate = %d, want 48000", got)
	}
	if got := binary.LittleEndian.Uint32(clip[28:32]); got != 48000*2 {
		t.Errorf("ByteRate = %d, want %d", got, 48000*2)
	}
	if got := binary.LittleEndian.Uint16(clip[32:34]); got != 2 {
		t.Errorf("BlockAlign = %d, want 2", got)
	}
	if got := binary.LittleEndian.Uint16(clip[34:36]); got != 16 {
		t.Errorf("BitsPerSample = %d, want 16", got)
	}
	if got := string(clip[36:40]); got != "data" {
		t.Errorf("Subchunk2ID = %q, want data", got)
	}
	if got := binary.LittleEndian.Uint32(clip[40:44]); got != dataSize {
		t.Errorf("Subchunk2Size = %d, want %d", got, dataSize)
	}
	if got := len(clip) - 44; got != dataSize {
		t.Errorf("payload = %d bytes, want %d", got, dataSize)
	}
	// Empty snapshot → pure silence.
	if !bytes.Equal(clip[44:], make([]byte, dataSize)) {
		t.Errorf("empty snapshot should be all-zero PCM")
	}
}
