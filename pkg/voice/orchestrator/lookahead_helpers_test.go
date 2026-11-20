package orchestrator_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// fakeLookahead is the coordinator-side seam for the look-ahead lane tests (#626):
// it records the Release/Discard op order and models the pump lane's HOLD by
// handing laneSynth a per-key release channel — so a look-ahead sentence's
// synthesis blocks until the coordinator releases it (or the turn's ctx is
// cancelled = discard/barge), exactly as the real pump lane back-pressures the
// lockstep tee.
type fakeLookahead struct {
	mu     sync.Mutex
	ops    []string // "release:<id>" / "discard:<id>" in call order
	rel    map[string]chan struct{}
	closed map[string]bool

	discardOnce sync.Once
	discardCh   chan struct{} // closed on the FIRST DiscardLookahead
}

func newFakeLookahead() *fakeLookahead {
	return &fakeLookahead{rel: map[string]chan struct{}{}, closed: map[string]bool{}, discardCh: make(chan struct{})}
}

// waitDiscard blocks until the coordinator has run a keyed DiscardLookahead — a
// happens-before edge for asserting final ops/events/history without racing the
// pipeline's flush.
func (f *fakeLookahead) waitDiscard(t *testing.T) {
	t.Helper()
	select {
	case <-f.discardCh:
	case <-time.After(2 * time.Second):
		t.Fatal("DiscardLookahead never ran")
	}
}

// relCh returns (creating) the per-key release channel laneSynth waits on.
func (f *fakeLookahead) relCh(id string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.rel[id]
	if !ok {
		ch = make(chan struct{})
		f.rel[id] = ch
	}
	return ch
}

func (f *fakeLookahead) ReleaseLookahead(id string) {
	f.mu.Lock()
	f.ops = append(f.ops, "release:"+id)
	ch, ok := f.rel[id]
	if !ok {
		ch = make(chan struct{})
		f.rel[id] = ch
	}
	if !f.closed[id] {
		f.closed[id] = true
		close(ch)
	}
	f.mu.Unlock()
}

func (f *fakeLookahead) DiscardLookahead(id string) {
	f.mu.Lock()
	f.ops = append(f.ops, "discard:"+id)
	f.mu.Unlock()
	f.discardOnce.Do(func() { close(f.discardCh) })
}

func (f *fakeLookahead) opsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ops...)
}

// synthCall records one Synthesize invocation's observable ctx state.
type synthCall struct {
	sentence  string
	lookahead bool
	turnID    string
}

// laneSynth is a [tts.Synthesizer] that models the pump lane for the coordinator
// tests: it records every call's look-ahead marker + lane key, HOLDS a look-ahead
// sentence's audio channel open until the pump releases that key (or ctx is
// cancelled), and can hold a named non-look-ahead sentence open (the one being
// spoken). Without the hold a pre-synthesized sentence would "commit" the instant
// it dispatched, defeating the release/discard the seam exists to enforce.
type laneSynth struct {
	pump     *fakeLookahead
	holdSet  map[string]bool // non-look-ahead sentences held open until holdGate
	holdGate chan struct{}

	mu    sync.Mutex
	calls []synthCall
}

func (s *laneSynth) Synthesize(ctx context.Context, req tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	lookahead := voiceevent.IsPlaybackLookahead(ctx)
	// The LANE KEY, not the turn id: a turn holds a different sentence on every
	// pipeline step (#626).
	id := voiceevent.LookaheadKeyFrom(ctx)
	s.mu.Lock()
	s.calls = append(s.calls, synthCall{sentence: req.Sentence, lookahead: lookahead, turnID: id})
	s.mu.Unlock()

	ch := make(chan tts.AudioChunk)
	if lookahead {
		// Held in the lane: the channel closes (the sentence "plays") only once the
		// pump releases this key, or ctx is cancelled (discard/barge).
		rel := s.pump.relCh(id)
		go func() {
			select {
			case <-rel:
			case <-ctx.Done():
			}
			close(ch)
		}()
		return ch, nil
	}
	if s.holdSet[req.Sentence] {
		go func() {
			select {
			case <-s.holdGate:
			case <-ctx.Done():
			}
			close(ch)
		}()
		return ch, nil
	}
	close(ch)
	return ch, nil
}

func (s *laneSynth) AudioMarkupPrompt(tts.Voice) string { return "" }

func (s *laneSynth) findCall(pred func(synthCall) bool) (synthCall, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.calls {
		if pred(c) {
			return c, true
		}
	}
	return synthCall{}, false
}

// waitUntil polls pred until true or the deadline, without a fixed sleep as the
// correctness signal (ADR-0021: observe, don't time).
func waitUntil(t *testing.T, why string, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !pred() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", why)
		}
		time.Sleep(2 * time.Millisecond)
	}
}
