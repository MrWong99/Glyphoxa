package wire

import (
	"context"
	"errors"
	"fmt"

	gxvoice "github.com/MrWong99/Glyphoxa/pkg/voice"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// playback and sessionPlayer are the seam over the concrete [gxvoice.Session]/
// [gxvoice.Playback] so the block-until-Done discipline below can be unit-tested
// without a live Discord connection (the same internal-interface pattern the
// pkg/voice Manager uses over disgo). realPlayer adapts a real Session.
type playback interface {
	Done() <-chan struct{}
	Err() error
}

type sessionPlayer interface {
	Play(ctx context.Context, src gxvoice.Source) (playback, error)
}

// realPlayer adapts a *gxvoice.Session to sessionPlayer. The adapter is needed
// because Go has no return-type covariance: Session.Play returns *Playback, not
// the playback interface.
type realPlayer struct{ sess *gxvoice.Session }

func (r realPlayer) Play(ctx context.Context, src gxvoice.Source) (playback, error) {
	pb, err := r.sess.Play(ctx, src)
	if err != nil {
		// Return an untyped nil, never the nil *Playback: a nil pointer in a
		// non-nil interface would defeat the caller's err check.
		return nil, err
	}
	return pb, nil
}

// PlaySentence speaks one synthesized sentence on the voice Session: it turns the
// sentence's [tts.AudioChunk] stream into Opus frames via the [Codec] and plays
// them to completion, blocking until the sentence finishes or is interrupted.
//
// It MUST block until [gxvoice.Playback.Done] before returning, because
// [gxvoice.Session.Play] auto-interrupts the current playback: firing the next
// sentence's Play before this one finishes would cut it off, so a reply's
// sentences are spoken by calling PlaySentence sequentially, each awaiting the
// previous. The orchestrator dispatches one sentence at a time (a fresh chunk
// channel per TTS Dispatch), which maps 1:1 onto one PlaySentence call.
//
// ctx scopes this sentence: a barge-in (ADR-0027) cancels it, the Source ends,
// the playback stops, and PlaySentence returns [gxvoice.ErrInterrupted] — the
// caller then abandons the rest of the turn. A clean end-of-sentence returns nil.
//
// chunks is the sentence's audio as the synthesizer emits it; the [Codec] reads
// each chunk's own SampleRate/Channels (no rate is assumed). chunks must be
// produced on a different goroutine than the one calling PlaySentence: the
// playback pulls from it as disgo's sender paces frames, so synthesis and
// playback run concurrently. A nil Session or nil chunks is a programming error.
func PlaySentence(ctx context.Context, sess *gxvoice.Session, codec Codec, chunks <-chan tts.AudioChunk) error {
	if sess == nil {
		return fmt.Errorf("wire.PlaySentence: session must not be nil")
	}
	return playSentence(ctx, realPlayer{sess}, codec, chunks)
}

// playSentence is the testable core: it depends on the sessionPlayer seam, not a
// concrete Session, so the block-until-Done discipline can be exercised with a
// fake player and no live connection. It publishes no FirstOpus (nil bus); the
// live pump uses [playSentenceBus].
func playSentence(ctx context.Context, p sessionPlayer, codec Codec, chunks <-chan tts.AudioChunk) error {
	return playSentenceBus(ctx, p, codec, chunks, nil, nil)
}

// playSentenceBus is playSentence with an optional bus: when non-nil, the
// playback Source is wrapped so the first Opus frame pulled to the wire publishes
// [voiceevent.FirstOpus] for the turn (task #7, the audible-on-wire SLO end). The
// turn's correlation id is recovered from ctx ([voiceevent.TurnIDFrom]), which
// the tee installs and threads through HandleSentence → the play job. A nil bus
// or a ctx with no turn id leaves the Source unwrapped. outboundTap, when non-nil,
// receives every Opus frame pulled to the wire (#306's agent-speech capture); it
// must not block.
func playSentenceBus(ctx context.Context, p sessionPlayer, codec Codec, chunks <-chan tts.AudioChunk, bus *voiceevent.Bus, outboundTap func(opus []byte)) error {
	if chunks == nil {
		return fmt.Errorf("wire.PlaySentence: chunks must not be nil")
	}

	src, err := codec.PlaybackSource(chunks)
	if err != nil {
		// A codec-less build reports ErrCodecUnavailable here; surface it so the
		// caller fails visibly rather than silently muting the NPC. Drain the
		// sentence first: the tee's lockstep forwarder blocks on this channel
		// until someone consumes it, and on this path no playback ever will.
		go drain(chunks)
		return fmt.Errorf("wire.PlaySentence: build playback source: %w", err)
	}

	// Wrap so every frame pulled to the wire is copied to the rollover tape (#306),
	// then — for a HELD look-ahead sentence only (#375) — so the first frame stamps its
	// delivery-aligned FirstAudio, then so the first frame stamps FirstOpus. Order is
	// load-bearing: the FirstAudio wrapper is INNER, so on the first frame it publishes
	// before the FirstOpus wrapper on the return path (FirstAudio precedes FirstOpus on
	// the same frame). Each is a no-op when its dependency is absent (tap / lookahead /
	// bus+turn id) — and because the drain paths never build or pull a source, a
	// never-played sentence structurally never publishes FirstAudio.
	src = newTappedSource(src, outboundTap)
	src = newFirstAudioSource(src, bus, voiceevent.TurnIDFrom(ctx), voiceevent.IsPlaybackLookahead(ctx))
	src = newFirstOpusSource(src, bus, voiceevent.TurnIDFrom(ctx))

	pb, err := p.Play(ctx, src)
	if err != nil {
		go drain(chunks) // same contract: an unplayed sentence must still be drained
		return fmt.Errorf("wire.PlaySentence: play: %w", err)
	}

	// Block until this sentence has fully played (or been interrupted) before
	// returning, so the caller's next PlaySentence does not auto-interrupt it.
	<-pb.Done()
	if err := pb.Err(); err != nil && !errors.Is(err, gxvoice.ErrInterrupted) {
		return fmt.Errorf("wire.PlaySentence: playback: %w", err)
	}
	return pb.Err() // nil on clean end, ErrInterrupted on barge-in
}
