package wire

import (
	"context"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// pipelineCtx marks a sentence context as a held look-ahead of turn turnID keyed
// by its own per-sentence lane key (#626): the intra-turn pre-synthesis pipeline
// holds a DIFFERENT sentence of the same turn on every step, so the lane key can
// no longer be the turn id.
func pipelineCtx(turnID, key string) context.Context {
	return voiceevent.WithLookaheadKey(lookaheadCtx(turnID), key)
}

// TestPlaybackPump_PerSentenceLaneKeys pins the #626 generalization: the lane is
// keyed per SENTENCE, so a release issued for sentence k never moves (or latches
// for) sentence k+1 of the same turn. Without per-sentence keys the turn-keyed
// lane lets s(k+1) consume s(k)'s latch and leapfrog the still-playing sentence.
func TestPlaybackPump_PerSentenceLaneKeys(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil)
	defer pump.Close()

	// A release for s2 lands BEFORE s2 is primed (release-before-prime) and latches.
	pump.ReleaseLookahead("T#2")

	// s3 of the SAME turn primes first. It must NOT consume s2's latch: it is a
	// different sentence, and playing it now would leapfrog s2.
	c3, ro3 := openChunks()
	pump.HandleSentence(pipelineCtx("T", "T#3"), ro3)
	assertNoPlay(t, p, "s3 primed while only s2's release had latched")
	probe3 := blockProbe(t, c3, "s3 held in the lane")

	// s2 primes: it bypasses the lane via its own latch and plays at once.
	c2, ro2 := openChunks()
	pump.HandleSentence(pipelineCtx("T", "T#2"), ro2)
	pb2 := p.waitPlay(t)
	close(c2)
	pb2.finish(nil)

	// Only now is s3 released — it plays after s2, preserving order.
	pump.ReleaseLookahead("T#3")
	pb3 := p.waitPlay(t)
	if got := p.plays.Load(); got != 2 {
		t.Fatalf("plays = %d, want 2 (s2 then s3)", got)
	}
	pb3.finish(nil)
	join(t, probe3, "s3 producer after its release+play")
}

// TestPlaybackPump_SequentialLaneReuse pins the depth-1 pipeline's steady state
// (#626, ADR-0025): one lane, reused sentence after sentence — prime k, release k
// once k-1 has finished playing — plays every sentence in order, and the queue is
// empty at every enqueue (cap 1 holds because a release only follows the previous
// sentence's completed playback).
func TestPlaybackPump_SequentialLaneReuse(t *testing.T) {
	p := newFakePlayer()
	pump := newPump(p, drainingCodec{}, nil, nil)
	defer pump.Close()

	// s1 takes the ordinary queue (a turn's first sentence is never held).
	c1, ro1 := openChunks()
	pump.HandleSentence(voiceevent.WithTurnID(context.Background(), "T"), ro1)
	pb := p.waitPlay(t)

	for _, key := range []string{"T#2", "T#3"} {
		// The next sentence is synthesized and held while the current one plays.
		c, ro := openChunks()
		pump.HandleSentence(pipelineCtx("T", key), ro)
		assertNoPlay(t, p, "held "+key+" while its predecessor is still playing")

		// Predecessor finishes; only then is the held sentence released.
		close(c1)
		pb.finish(nil)
		pump.ReleaseLookahead(key)
		pb = p.waitPlay(t)
		c1 = c
	}
	close(c1)
	pb.finish(nil)
	if got := p.plays.Load(); got != 3 {
		t.Fatalf("plays = %d, want 3 (s1, s2, s3 in order)", got)
	}
}
