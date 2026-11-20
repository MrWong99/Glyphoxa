package wire

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MrWong99/Glyphoxa/internal/storage"
	"github.com/MrWong99/Glyphoxa/internal/transcript"
	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/audio"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/vad"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// senderPlayer plays a Source the way disgo's sender does: it pulls frames
// until the Source ends, then finishes the playback. That is what makes the
// wire publish a held look-ahead sentence's FirstAudio — off the sender
// goroutine, possibly after the sentence's dispatch has returned.
type senderPlayer struct{}

func (senderPlayer) Play(ctx context.Context, src gxvoice.Source) (playback, error) {
	pb := &fakePlayback{done: make(chan struct{})}
	go func() {
		for {
			if _, err := src.NextFrame(ctx); err != nil {
				pb.finish(nil)
				return
			}
		}
	}()
	return pb, nil
}

// frameCodec pulls chunks lazily, like the real codec's playbackSource: one
// chunk per NextFrame. It encodes slowly (1 ms) AFTER taking the chunk, so the
// tee's lockstep forward — and with a one-chunk sentence its whole Dispatch —
// finishes before the wire's FirstAudio for that frame is published: the late
// ordering the relay must survive.
type frameCodec struct{}

func (frameCodec) DecodeInbound(gxvoice.Frame) ([]audio.Frame, error) { return nil, nil }
func (frameCodec) PlaybackSource(chunks <-chan tts.AudioChunk) (gxvoice.Source, error) {
	return chunkSource{chunks}, nil
}

type chunkSource struct{ ch <-chan tts.AudioChunk }

func (s chunkSource) NextFrame(ctx context.Context) ([]byte, error) {
	select {
	case _, ok := <-s.ch:
		if !ok {
			return nil, io.EOF
		}
		time.Sleep(time.Millisecond) // encode
		return []byte{1}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// oneChunkSynth synthesizes every sentence as a single chunk.
type oneChunkSynth struct{}

func (oneChunkSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk, 1)
	ch <- tts.AudioChunk{}
	close(ch)
	return ch, nil
}
func (oneChunkSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

type silentVAD struct{}

func (silentVAD) ProcessFrame(audio.Frame) (vad.VADEvent, error) {
	return vad.VADEvent{Type: vad.VADSilence}, nil
}
func (silentVAD) Reset()       {}
func (silentVAD) Close() error { return nil }

type oneSession struct{ id uuid.UUID }

func (s oneSession) Resolve(id uuid.UUID) (storage.VoiceSession, bool) {
	return storage.VoiceSession{ID: s.id}, id == s.id
}

// TestRelay_PipelinedReplyKeepsAllText drives a real multi-sentence reply
// through the production chain — streaming Replier on the barge-in floor with
// the pump's look-ahead lane (#626), the tee, the pump and a sender that pulls
// frames — into the transcript relay. The pipelined sentences' FirstAudio comes
// from the wire's sender goroutine and may land after the turn's
// TurnEnded{completed}; a healthy reply must keep ALL its text, and the typing
// label must return to listening.
func TestRelay_PipelinedReplyKeepsAllText(t *testing.T) {
	for i := range 20 {
		bus := voiceevent.NewBus()
		proc := voiceevent.NewBus()
		sid := uuid.New()
		unfwd := voiceevent.Forward(bus, proc, sid.String())
		relay := transcript.NewRelay(proc, oneSession{id: sid}, nil, nil)

		pump := newPump(senderPlayer{}, frameCodec{}, nil, bus)
		ttsStage := orchestrator.NewTTS(bus, NewTeeSynthesizer(oneChunkSynth{}, pump, bus))
		reply := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
			for _, s := range []string{"One.", "Two.", "Three."} {
				if err := dispatch(orchestrator.Reply{Sentence: s}); orchestrator.OutcomeOf(err) == orchestrator.SentenceCut {
					return err
				}
			}
			return nil
		}
		conv, err := orchestrator.NewConversation(bus,
			orchestrator.NewVAD(bus, silentVAD{}),
			orchestrator.NewSTT(bus, stubRecognizer{}),
			ttsStage,
			orchestrator.WithReply(orchestrator.ReplyStrategy{Stream: reply}),
			orchestrator.WithBargeIn(orchestrator.Barge{Confirm: time.Second, Lookahead: pump}),
		)
		if err != nil {
			t.Fatalf("NewConversation: %v", err)
		}
		var logMu sync.Mutex
		var seen []string
		unlog := bus.Subscribe(func(e voiceevent.Event) {
			switch ev := e.(type) {
			case voiceevent.TTSInvoked:
				logMu.Lock()
				seen = append(seen, "invoked:"+ev.Sentence)
				logMu.Unlock()
			case voiceevent.FirstAudio, voiceevent.TurnEnded, voiceevent.TTSStreamFailed:
				logMu.Lock()
				seen = append(seen, e.EventName())
				logMu.Unlock()
			}
		})
		defer unlog()
		trace := func() string { logMu.Lock(); defer logMu.Unlock(); return strings.Join(seen, " ") }
		ended := make(chan voiceevent.TurnEnded, 4)
		unsub := voiceevent.On(bus, func(e voiceevent.TurnEnded) { ended <- e })
		unreg := conv.Register(t.Context())

		bus.Publish(voiceevent.AddressRouted{TurnID: "t1", Text: "Bart?",
			Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}})
		select {
		case e := <-ended:
			if e.Reason != voiceevent.TurnEndCompleted {
				t.Fatalf("iteration %d: terminal = %q, want completed", i, e.Reason)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("iteration %d: the reply never ended", i)
		}

		const want = "One. Two. Three."
		var got transcript.View
		for deadline := time.Now().Add(2 * time.Second); ; {
			got = relay.View(sid.String())
			if len(got.Lines) == 1 && got.Lines[0].Text == want || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if len(got.Lines) != 1 || got.Lines[0].Text != want {
			t.Fatalf("iteration %d: relay line = %+v, want %q (a delivered sentence was stripped); events: %s", i, got.Lines, want, trace())
		}
		if !got.Typing.Active || strings.Contains(got.Typing.Label, "speaking") {
			t.Fatalf("iteration %d: typing = %+v, want listening after the reply", i, got.Typing)
		}

		unreg()
		unsub()
		pump.Close()
		unfwd()
	}
}
