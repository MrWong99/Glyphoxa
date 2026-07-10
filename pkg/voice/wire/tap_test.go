package wire

// White-box tests for the rollover-tape taps (#306): the inbound Opus tap and
// decoded-PCM tap on the Pipeline audio loop, and the outbound Opus tap on the
// playback pump. They run in `package wire` to drive the unexported run loop and
// playSentenceBus directly — the same seam the silence tests use.

import (
	"context"
	"testing"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// speakerCodec decodes every inbound frame to a single PCM frame stamped with the
// inbound frame's UserID, modelling the real Opus→PCM transcoder's Speaker
// attribution (ADR-0050) so the PCM tap can be observed carrying it.
type speakerCodec struct{}

func (speakerCodec) DecodeInbound(f gxvoice.Frame) ([]audio.Frame, error) {
	pcm, err := audio.NewFrame(make([]int16, 512), 16000, 32)
	if err != nil {
		return nil, err
	}
	return []audio.Frame{pcm.WithSpeaker(f.UserID.String())}, nil
}

func (speakerCodec) PlaybackSource(<-chan tts.AudioChunk) (gxvoice.Source, error) {
	return nil, nil
}

// TestPipeline_InboundTapSeesNonSilenceFramesOnly pins WithInboundTap: every
// non-silence inbound frame reaches the tap, and Discord silence frames are
// skipped (they never enter the tape — they carry no audio).
func TestPipeline_InboundTapSeesNonSilenceFramesOnly(t *testing.T) {
	conv, _, _ := newSilenceRig(t, "x")

	var got []gxvoice.Frame
	pipe := NewPipeline(conv, speakerCodec{}, nil, "guild", nil,
		WithInboundTap(func(f gxvoice.Frame) { got = append(got, f) }))

	inbound := make(chan gxvoice.Frame, 4)
	inbound <- gxvoice.Frame{UserID: 111, Opus: []byte{0x01}}
	inbound <- gxvoice.Frame{Silence: true}
	inbound <- gxvoice.Frame{UserID: 111, Opus: []byte{0x02}}
	close(inbound)

	if err := pipe.run(context.Background(), inbound); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("inbound tap saw %d frames, want 2 (silence skipped)", len(got))
	}
	if got[0].Opus[0] != 0x01 || got[1].Opus[0] != 0x02 {
		t.Fatalf("inbound tap frames out of order/wrong: %v %v", got[0].Opus, got[1].Opus)
	}
	for _, f := range got {
		if f.Silence {
			t.Fatalf("inbound tap saw a silence frame")
		}
	}
}

// TestPipeline_PCMTapSeesSpeakerStampedFrames pins WithPCMTap: every decoded PCM
// frame reaches the tap carrying the codec's Speaker attribution.
func TestPipeline_PCMTapSeesSpeakerStampedFrames(t *testing.T) {
	conv, _, _ := newSilenceRig(t, "x")

	var speakers []string
	pipe := NewPipeline(conv, speakerCodec{}, nil, "guild", nil,
		WithPCMTap(func(f audio.Frame) { speakers = append(speakers, f.Speaker()) }))

	inbound := make(chan gxvoice.Frame, 2)
	inbound <- gxvoice.Frame{UserID: 111, Opus: []byte{0x01}}
	inbound <- gxvoice.Frame{UserID: 222, Opus: []byte{0x02}}
	close(inbound)

	if err := pipe.run(context.Background(), inbound); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(speakers) != 2 {
		t.Fatalf("pcm tap saw %d frames, want 2", len(speakers))
	}
	if speakers[0] != "111" || speakers[1] != "222" {
		t.Fatalf("pcm tap speakers = %v, want [111 222]", speakers)
	}
}

// drainingPlayer pulls every frame from the Source until EOF, modelling disgo's
// sender streaming a whole sentence to the wire, so the outbound tap is observed
// for every frame.
type drainingPlayer struct{}

func (drainingPlayer) Play(ctx context.Context, src gxvoice.Source) (playback, error) {
	for {
		if _, err := src.NextFrame(ctx); err != nil {
			break
		}
	}
	done := make(chan struct{})
	close(done)
	return &fakePlayback{done: done}, nil
}

// TestPlaySentenceBus_OutboundTapSeesEveryFrame pins WithOutboundOpusTap: every
// Opus frame pulled to the wire reaches the tap, in order.
func TestPlaySentenceBus_OutboundTapSeesEveryFrame(t *testing.T) {
	codec := framingCodec{frames: [][]byte{{0x01}, {0x02}, {0x03}}}
	chunks := make(chan tts.AudioChunk)
	close(chunks)

	var got [][]byte
	tap := func(opus []byte) { got = append(got, opus) }

	if err := playSentenceBus(context.Background(), drainingPlayer{}, codec, chunks, nil, tap); err != nil {
		t.Fatalf("playSentenceBus: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("outbound tap saw %d frames, want 3", len(got))
	}
	for i, want := range []byte{0x01, 0x02, 0x03} {
		if got[i][0] != want {
			t.Fatalf("outbound tap frame %d = %v, want %#x", i, got[i], want)
		}
	}
}
