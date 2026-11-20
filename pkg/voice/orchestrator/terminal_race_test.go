package orchestrator_test

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// terminalCounter counts TurnEnded per TurnID.
type terminalCounter struct {
	mu sync.Mutex
	n  map[string][]voiceevent.TurnEndReason
}

func (c *terminalCounter) on(e voiceevent.TurnEnded) {
	c.mu.Lock()
	c.n[e.TurnID] = append(c.n[e.TurnID], e.Reason)
	c.mu.Unlock()
}

func (c *terminalCounter) get(id string) []voiceevent.TurnEndReason {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]voiceevent.TurnEndReason(nil), c.n[id]...)
}

func (c *terminalCounter) wait(t *testing.T, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for len(c.get(id)) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("turn %s never got a terminal", id)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// TestTerminal_RealCutterRacingNaturalEnd is the W1 invariant under stress: a
// real cutter — the BargeIn reactor, the MuteCut reactor, or a superseding route
// — racing a turn's natural end leaves EXACTLY ONE TurnEnded for the turn:
// either the cutter's or the turn's own completed, never both, never none. Run
// under -race.
func TestTerminal_RealCutterRacingNaturalEnd(t *testing.T) {
	const iterations = 300
	bart := voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character", Name: "Bart"}
	greta := voiceevent.AddressTarget{AgentID: "greta", AgentRole: "character", Name: "Greta"}
	for i := range iterations {
		bus := voiceevent.NewBus()
		cnt := &terminalCounter{n: map[string][]voiceevent.TurnEndReason{}}
		unsubCount := voiceevent.On(bus, cnt.on)

		floor := orchestrator.NewFloor()
		reply := func(_ context.Context, _ voiceevent.AddressRouted, dispatch func(orchestrator.Reply) error) error {
			runtime.Gosched()
			return dispatch(orchestrator.Reply{Sentence: "Aye."})
		}
		replier := orchestrator.NewStreamReplier(orchestrator.NewTTS(bus, selectiveSynth{}), reply, nil)
		replier.SetFloor(floor)
		unbind := orchestrator.Bind(t.Context(), bus,
			orchestrator.NewBargeIn(floor, 0), orchestrator.NewMuteCut(floor), replier)

		var wg sync.WaitGroup
		kind := i % 3
		wg.Go(func() {
			bus.Publish(voiceevent.AddressRouted{TurnID: "t", Target: bart})
		})
		wg.Go(func() {
			for range i % 7 {
				runtime.Gosched()
			}
			switch kind {
			case 0: // human barge over the audibly speaking turn
				bus.Publish(voiceevent.FirstOpus{At: time.Now(), TurnID: "t"})
				bus.Publish(voiceevent.VADSpeechStart{At: time.Now()})
			case 1: // GM mute of the speaking agent
				bus.Publish(voiceevent.MuteChanged{At: time.Now(), AgentID: "bart", Muted: true})
			case 2: // a different-target route takes the floor
				bus.Publish(voiceevent.AddressRouted{TurnID: "t2", Target: greta})
			}
		})
		wg.Wait()
		cnt.wait(t, "t")
		if kind == 2 {
			cnt.wait(t, "t2")
		}
		time.Sleep(2 * time.Millisecond) // let any second (wrong) terminal land
		if got := cnt.get("t"); len(got) != 1 {
			t.Fatalf("iteration %d (cutter %d): turn t got %d terminals %v, want exactly 1", i, kind, len(got), got)
		}
		if kind == 2 {
			// The two routes race each other too: whichever took the floor second
			// superseded the first. Either way, one terminal each.
			if got := cnt.get("t2"); len(got) != 1 {
				t.Fatalf("iteration %d: turn t2 terminals = %v, want exactly 1", i, got)
			}
		}
		unbind()
		unsubCount()
	}
}

// flipMutes reports "not muted" on the replier's pre-Take check and "muted" on
// its post-Take re-check — publishing the MuteChanged (which the MuteCut acts
// on) in between, exactly as a mute landing between the two checks would.
type flipMutes struct {
	bus   *voiceevent.Bus
	calls int
}

func (m *flipMutes) Muted(agentID string) bool {
	m.calls++
	if m.calls == 1 {
		return false
	}
	m.bus.Publish(voiceevent.MuteChanged{At: time.Now(), AgentID: agentID, Muted: true})
	return true
}

// TestReplier_MuteBetweenChecksPublishesOneTerminal: a mute landing between the
// pre-Take check and the post-Take re-check is cut by the MuteCut reactor (one
// mute terminal); the re-check must not publish a second one.
func TestReplier_MuteBetweenChecksPublishesOneTerminal(t *testing.T) {
	bus := voiceevent.NewBus()
	cnt := &terminalCounter{n: map[string][]voiceevent.TurnEndReason{}}
	t.Cleanup(voiceevent.On(bus, cnt.on))
	floor := orchestrator.NewFloor()
	replier := orchestrator.NewStreamReplier(orchestrator.NewTTS(bus, selectiveSynth{}),
		func(context.Context, voiceevent.AddressRouted, func(orchestrator.Reply) error) error { return nil }, nil)
	replier.SetFloor(floor)
	replier.SetMutes(&flipMutes{bus: bus})
	t.Cleanup(orchestrator.Bind(t.Context(), bus, orchestrator.NewMuteCut(floor), replier))

	bus.Publish(voiceevent.AddressRouted{TurnID: "t", Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character"}})
	if got := cnt.get("t"); len(got) != 1 || got[0] != voiceevent.TurnEndMute {
		t.Fatalf("terminals = %v, want exactly [mute]", got)
	}
}

// TestReplier_TeardownWhileHoldingPublishesCancelled: a turn holding the floor
// when its conversation ctx is cancelled (a reconnect cycle) still ends with a
// terminal — cancelled — so no "<NPC> is speaking…" outlives it.
func TestReplier_TeardownWhileHoldingPublishesCancelled(t *testing.T) {
	bus := voiceevent.NewBus()
	cnt := &terminalCounter{n: map[string][]voiceevent.TurnEndReason{}}
	t.Cleanup(voiceevent.On(bus, cnt.on))
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	replier := orchestrator.NewStreamReplier(orchestrator.NewTTS(bus, selectiveSynth{}),
		func(ctx context.Context, _ voiceevent.AddressRouted, _ func(orchestrator.Reply) error) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}, nil)
	replier.SetFloor(orchestrator.NewFloor())
	t.Cleanup(replier.Bind(ctx, bus))

	bus.Publish(voiceevent.AddressRouted{TurnID: "t", Target: voiceevent.AddressTarget{AgentID: "bart", AgentRole: "character"}})
	<-started
	cancel()
	cnt.wait(t, "t")
	if got := cnt.get("t"); len(got) != 1 || got[0] != voiceevent.TurnEndCancelled {
		t.Fatalf("terminals = %v, want exactly [cancelled]", got)
	}
}

// TestClipReplay_CleanReplayPublishesCompleted: a Highlight replay that plays to
// its end publishes exactly one TurnEnded{completed}, on both paths.
func TestClipReplay_CleanReplayPublishesCompleted(t *testing.T) {
	for _, withFloor := range []bool{false, true} {
		bus := voiceevent.NewBus()
		cnt := &terminalCounter{n: map[string][]voiceevent.TurnEndReason{}}
		t.Cleanup(voiceevent.On(bus, cnt.on))
		load := func(context.Context, string) ([]tts.AudioChunk, error) { return []tts.AudioChunk{{}, {}}, nil }
		sink := func(_ context.Context, ch <-chan tts.AudioChunk) {
			go func() {
				for range ch {
				}
			}()
		}
		cr := orchestrator.NewClipReplay(load, sink, nil)
		if withFloor {
			cr.SetFloor(orchestrator.NewFloor())
		}
		t.Cleanup(cr.Bind(t.Context(), bus))

		bus.Publish(voiceevent.ReplayRequested{TurnID: "rp", ClipKey: "k"})
		cnt.wait(t, "rp")
		time.Sleep(2 * time.Millisecond)
		if got := cnt.get("rp"); len(got) != 1 || got[0] != voiceevent.TurnEndCompleted {
			t.Fatalf("floor=%v: replay terminals = %v, want [completed]", withFloor, got)
		}
	}
}

// TestDirectSpeech_CleanSayPublishesCompleted: a clean /say ends with exactly one
// TurnEnded{completed} on both the floor and the synchronous path.
func TestDirectSpeech_CleanSayPublishesCompleted(t *testing.T) {
	for _, withFloor := range []bool{false, true} {
		bus := voiceevent.NewBus()
		cnt := &terminalCounter{n: map[string][]voiceevent.TurnEndReason{}}
		t.Cleanup(voiceevent.On(bus, cnt.on))
		ds := orchestrator.NewDirectSpeech(orchestrator.NewTTS(bus, selectiveSynth{}),
			func(string) (tts.Voice, bool) { return tts.Voice{VoiceID: "v"}, true }, nil)
		if withFloor {
			ds.SetFloor(orchestrator.NewFloor())
		}
		t.Cleanup(ds.Bind(t.Context(), bus))

		bus.Publish(voiceevent.SpeakRequested{TurnID: "say", Text: "Hear me.", Target: voiceevent.AddressTarget{AgentID: "bart"}})
		cnt.wait(t, "say")
		time.Sleep(2 * time.Millisecond)
		if got := cnt.get("say"); len(got) != 1 || got[0] != voiceevent.TurnEndCompleted {
			t.Fatalf("floor=%v: /say terminals = %v, want [completed]", withFloor, got)
		}
	}
}
