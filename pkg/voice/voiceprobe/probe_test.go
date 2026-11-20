package voiceprobe

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/disgoorg/snowflake/v2"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
)

// silentOpus is a valid 20 ms Opus packet (Discord's own silence frame): it
// decodes without an encoder, so these tests need no build tags. It is fed with
// Frame.Silence unset, so the recorder treats it as speech.
var silentOpus = []byte{0xF8, 0xFF, 0xFE}

const (
	botID   = snowflake.ID(7)
	humanID = snowflake.ID(9)
)

// speak feeds n consecutive 20 ms frames from user into r in real time.
func speak(r *recorder, user snowflake.ID, n int) {
	for i := range n {
		r.record(gxvoice.Frame{UserID: user, Opus: silentOpus, PTS: time.Duration(i) * 20 * time.Millisecond}, time.Now())
		time.Sleep(20 * time.Millisecond)
	}
}

func TestWaitReply_ReportsLatencyAndAudio(t *testing.T) {
	r := newRecorder(time.Now(), botID)
	since := time.Now()
	go func() {
		time.Sleep(200 * time.Millisecond)
		speak(r, botID, 10)
	}()

	reply, err := r.waitReply(context.Background(), since, 200*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !reply.Heard {
		t.Fatal("reply not heard")
	}
	if reply.Latency < 150*time.Millisecond || reply.Latency > time.Second {
		t.Errorf("latency = %s, want about 200ms", reply.Latency)
	}
	if reply.Duration < 100*time.Millisecond || reply.Duration > time.Second {
		t.Errorf("duration = %s, want about 200ms", reply.Duration)
	}
	// The reply audio spans the reply plus a frame of margin either side.
	if got := time.Duration(len(reply.PCM)) * time.Second / SampleRate; got < reply.Duration {
		t.Errorf("reply audio is %s, shorter than the %s reply", got, reply.Duration)
	}
}

func TestWaitReply_OnlyCountsReplyFrom(t *testing.T) {
	r := newRecorder(time.Now(), botID)
	since := time.Now()
	go speak(r, humanID, 10)

	reply, err := r.waitReply(context.Background(), since, 100*time.Millisecond, 400*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Heard {
		t.Fatal("a human talking was counted as the Bot's reply")
	}
	tracks := r.tracksSnapshot()
	if len(tracks) != 1 || tracks[0].UserID != humanID || tracks[0].Frames != 10 {
		t.Fatalf("the human must still be recorded; tracks = %+v", tracks)
	}
}

func TestWaitReply_AnyoneCountsWithoutReplyFrom(t *testing.T) {
	r := newRecorder(time.Now(), 0)
	since := time.Now()
	go speak(r, humanID, 5)

	reply, err := r.waitReply(context.Background(), since, 100*time.Millisecond, 2*time.Second)
	if err != nil || !reply.Heard {
		t.Fatalf("reply = %+v, err = %v", reply, err)
	}
}

func TestWaitReply_AudioBeforeTheClipEndedIsNotAReply(t *testing.T) {
	r := newRecorder(time.Now(), botID)
	speak(r, botID, 3) // the Bot's previous sentence, already over
	time.Sleep(30 * time.Millisecond)

	reply, err := r.waitReply(context.Background(), time.Now(), 50*time.Millisecond, 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if reply.Heard {
		t.Fatal("audio from before the wait was counted as the reply")
	}
}

func TestWaitReply_ContextCancelled(t *testing.T) {
	r := newRecorder(time.Now(), botID)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.waitReply(ctx, time.Now(), time.Second, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

// A speaker's track is laid out on the probe's timeline: silence before they
// first spoke, and the real gap between two utterances.
func TestRecorder_TrackKeepsRealTiming(t *testing.T) {
	start := time.Now()
	r := newRecorder(start, 0)
	at := start.Add(time.Second)
	r.record(gxvoice.Frame{UserID: botID, Opus: silentOpus, PTS: 0}, at)
	r.record(gxvoice.Frame{UserID: botID, Opus: silentOpus, PTS: 2 * time.Second}, at.Add(2*time.Second))

	tracks := r.tracksSnapshot()
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d", len(tracks))
	}
	tr := tracks[0]
	want := 3*SampleRate + opusFrame // 1s lead-in + 2s gap + the last frame
	if len(tr.PCM) != want {
		t.Errorf("track is %d samples, want %d", len(tr.PCM), want)
	}
	if tr.Voiced != 40*time.Millisecond || tr.Frames != 2 || tr.DecodeErrors != 0 {
		t.Errorf("track = voiced %s, %d frames, %d errors", tr.Voiced, tr.Frames, tr.DecodeErrors)
	}
}

func TestRecorder_CountsUndecodableFrames(t *testing.T) {
	r := newRecorder(time.Now(), 0)
	r.record(gxvoice.Frame{UserID: botID, Opus: []byte{0x03}}, time.Now())
	if tr := r.tracksSnapshot(); len(tr) != 1 || tr[0].DecodeErrors != 1 {
		t.Fatalf("tracks = %+v", tr)
	}
}

func TestWAVRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.wav")
	pcm := []int16{0, 1000, -1000, 32767, -32768}
	if err := WriteWAV(path, pcm, SampleRate); err != nil {
		t.Fatal(err)
	}
	clip, err := LoadWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	if clip.SampleRate != SampleRate || clip.Channels != 1 || len(clip.PCM) != 2*len(pcm) {
		t.Fatalf("clip = rate %d, %d ch, %d bytes", clip.SampleRate, clip.Channels, len(clip.PCM))
	}
	for i, want := range pcm {
		if got := int16(uint16(clip.PCM[2*i]) | uint16(clip.PCM[2*i+1])<<8); got != want {
			t.Errorf("sample %d = %d, want %d", i, got, want)
		}
	}
}

func TestLoadWAV_RepoClip(t *testing.T) {
	clip, err := LoadWAV("../../../tests/voice-clips/hello-test/audio.wav")
	if err != nil {
		t.Fatal(err)
	}
	if clip.SampleRate != 16000 || clip.Channels != 1 {
		t.Fatalf("clip = rate %d, %d ch", clip.SampleRate, clip.Channels)
	}
	if d := clip.Duration(); d < 3*time.Second || d > 4*time.Second {
		t.Fatalf("duration = %s, want about 3.3s", d)
	}
}

func TestLoadWAV_Rejects(t *testing.T) {
	dir := t.TempDir()
	notWAV := filepath.Join(dir, "not.wav")
	if err := os.WriteFile(notWAV, []byte("definitely not audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A valid header declaring 8-bit samples.
	eightBit := filepath.Join(dir, "8bit.wav")
	if err := WriteWAV(eightBit, []int16{1, 2}, 8000); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(eightBit)
	b[34] = 8
	if err := os.WriteFile(eightBit, b, 0o644); err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{notWAV: "not a RIFF/WAVE", eightBit: "8-bit"} {
		if _, err := LoadWAV(path); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("LoadWAV(%s) = %v, want an error containing %q", filepath.Base(path), err, want)
		}
	}
	if _, err := LoadWAV(filepath.Join(dir, "missing.wav")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
}

func TestJoin_ValidatesConfig(t *testing.T) {
	if _, err := Join(context.Background(), Config{Guild: 1, Channel: 2}); err == nil {
		t.Error("empty token accepted")
	}
	if _, err := Join(context.Background(), Config{Token: "x"}); err == nil {
		t.Error("missing guild/channel accepted")
	}
	// Without the dave tag a join can only end in Discord close 4017; say so
	// before touching the network.
	if !gxvoice.DaveAvailable() {
		if _, err := Join(context.Background(), Config{Token: "x", Guild: 1, Channel: 2}); !errors.Is(err, ErrNoDave) {
			t.Errorf("err = %v, want ErrNoDave", err)
		}
	}
}

// TestRecorder_PauseBetweenUtterancesIsKept pins the timeline across a pause: a
// speaker's PTS does not advance while they are silent, so the second utterance
// arrives with timestamps contiguous to the first. It must land where it was
// heard, not directly behind the first — otherwise every reply after the first
// is cut out of the wrong (silent) stretch of the track.
func TestRecorder_PauseBetweenUtterancesIsKept(t *testing.T) {
	start := time.Now()
	r := newRecorder(start, botID)
	frame := func(i int, at time.Duration) {
		r.record(gxvoice.Frame{UserID: botID, Opus: silentOpus, PTS: time.Duration(i) * 20 * time.Millisecond}, start.Add(at))
	}
	for i := range 10 { // 200 ms of speech from t=0
		frame(i, time.Duration(i)*20*time.Millisecond)
	}
	for i := 10; i < 20; i++ { // 200 ms more, heard 10 s later, PTS contiguous
		frame(i, 10*time.Second+time.Duration(i-10)*20*time.Millisecond)
	}
	tracks := r.tracksSnapshot()
	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(tracks))
	}
	got := time.Duration(len(tracks[0].PCM)) * time.Second / SampleRate
	if got < 10*time.Second {
		t.Errorf("track is %s long, want over 10s: the pause was dropped", got)
	}
}

func TestSplitBarge(t *testing.T) {
	t0 := time.Now()
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	run := func(fromMS, toMS int) []time.Time { // a frame every 20 ms
		var out []time.Time
		for ms := fromMS; ms <= toMS; ms += 20 {
			out = append(out, at(ms))
		}
		return out
	}
	const gap = 700 * time.Millisecond

	t.Run("reply stops, a new one follows", func(t *testing.T) {
		arrivals := append(run(0, 1400), run(4000, 5000)...) // barged at 1000
		cut, resumed, last := splitBarge(arrivals, at(1000), at(6000), gap)
		if !cut.Equal(at(1400)) || !resumed.Equal(at(4000)) || !last.Equal(at(5000)) {
			t.Errorf("cut=%s resumed=%s last=%s, want 1400/4000/5000 ms",
				cut.Sub(t0), resumed.Sub(t0), last.Sub(t0))
		}
	})
	t.Run("reply still running", func(t *testing.T) {
		cut, _, _ := splitBarge(run(0, 2000), at(1000), at(2100), gap)
		if !cut.IsZero() {
			t.Errorf("cut = %s, want none while the Bot keeps talking", cut.Sub(t0))
		}
	})
	t.Run("reply stopped, nothing since", func(t *testing.T) {
		cut, resumed, _ := splitBarge(run(0, 1200), at(1000), at(3000), gap)
		if !cut.Equal(at(1200)) || !resumed.IsZero() {
			t.Errorf("cut=%s resumed-zero=%v, want cut at 1200 ms and no resume", cut.Sub(t0), resumed.IsZero())
		}
	})
	t.Run("bot was already silent when barged", func(t *testing.T) {
		arrivals := append(run(0, 100), run(3000, 3500)...)
		cut, resumed, _ := splitBarge(arrivals, at(1000), at(5000), gap)
		if talkedOver(cut, at(1000)) != 0 || !resumed.Equal(at(3000)) {
			t.Errorf("talkedOver=%s resumed=%s, want 0 and 3000 ms", talkedOver(cut, at(1000)), resumed.Sub(t0))
		}
	})
}
