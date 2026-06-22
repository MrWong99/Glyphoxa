package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
)

// castReplier builds a Replier for a named Agent over a streaming engine, so a
// Cast can be assembled from several distinguishable repliers. The Voice carries
// the AgentID in its identifying fields so a dispatched Reply can be attributed to
// its speaker.
func castReplier(agentID string, eng agent.Engine) *agent.Replier {
	return agent.NewReplier(agent.Config{
		Persona: agent.Persona{
			AgentID:  agentID,
			Markdown: "You are " + agentID + ".",
			Voice:    voiceNamed(agentID),
		},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
}

// voiceNamed is testVoice with the speaker's id stamped into its identifying
// fields so a dispatched Reply's Voice tells which Agent spoke it.
func voiceNamed(agentID string) tts.Voice {
	v := testVoice()
	v.VoiceID = agentID
	v.Name = agentID
	return v
}

// TestCast_RoutesToAddressedAgent pins the multiplexer core: a route for agent B
// drives B's reply (and only B's). The dispatched Reply's Voice attributes the
// sentence to its speaker, so a route for "b" must be spoken in b's voice.
func TestCast_RoutesToAddressedAgent(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"I am A."}})
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"I am B."}})
	cast := agent.NewCast(a, b)

	var got []orchestrator.Reply
	err := cast.ReplyStream()(context.Background(), routed("b", "who are you?"), func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	})
	if err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("dispatched %d replies, want 1 (only B speaks)", len(got))
	}
	if got[0].Sentence != "I am B." {
		t.Errorf("sentence = %q, want B's reply", got[0].Sentence)
	}
	if got[0].Voice.VoiceID != "b" {
		t.Errorf("voice = %q, want B's voice (A must not have spoken)", got[0].Voice.VoiceID)
	}
}

// TestCast_UnknownAgentSaysNothing pins the unknown-target contract: a route for
// an AgentID no replier in the Cast answers for dispatches nothing and returns
// nil — the safe default when the matcher selected an Agent the Cast does not (or
// no longer) holds.
func TestCast_UnknownAgentSaysNothing(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"I am A."}})
	cast := agent.NewCast(a)

	var dispatched int
	err := cast.ReplyStream()(context.Background(), routed("ghost", "hello?"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	})
	if err != nil {
		t.Errorf("ReplyStream for unknown agent = %v, want nil", err)
	}
	if dispatched != 0 {
		t.Errorf("dispatched %d replies for unknown agent, want 0", dispatched)
	}
}

// TestCast_AddNPC pins runtime registration: an Agent added after construction
// replies when addressed, so a Cast can grow as NPCs enter the scene.
func TestCast_AddNPC(t *testing.T) {
	cast := agent.NewCast()

	// Before Add, the target is unknown — nothing is said.
	var before int
	_ = cast.ReplyStream()(context.Background(), routed("c", "hi"), func(orchestrator.Reply) error {
		before++
		return nil
	})
	if before != 0 {
		t.Fatalf("dispatched %d before Add, want 0", before)
	}

	cast.Add(castReplier("c", &fakeStreamEngine{deltas: []string{"I am C."}}))

	var got []orchestrator.Reply
	if err := cast.ReplyStream()(context.Background(), routed("c", "hi"), func(rep orchestrator.Reply) error {
		got = append(got, rep)
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream after Add: %v", err)
	}
	if len(got) != 1 || got[0].Sentence != "I am C." {
		t.Errorf("after Add dispatched %+v, want C's single reply", got)
	}
}

// TestCast_RemoveNPC pins runtime removal: an Agent removed from the Cast goes
// silent — a route for it dispatches nothing, as if it had never been registered.
func TestCast_RemoveNPC(t *testing.T) {
	d := castReplier("d", &fakeStreamEngine{deltas: []string{"I am D."}})
	cast := agent.NewCast(d)

	cast.Remove("d")

	var dispatched int
	if err := cast.ReplyStream()(context.Background(), routed("d", "still there?"), func(orchestrator.Reply) error {
		dispatched++
		return nil
	}); err != nil {
		t.Fatalf("ReplyStream after Remove: %v", err)
	}
	if dispatched != 0 {
		t.Errorf("removed agent dispatched %d replies, want 0 (gone silent)", dispatched)
	}
}

// TestCast_Reply_RoutesToAddressedAgent pins the batch [orchestrator.ReplyFunc]
// twin of the streaming multiplexer: Reply() looks up the addressed agent and
// returns its turn, nothing for the others.
func TestCast_Reply_RoutesToAddressedAgent(t *testing.T) {
	a := castReplier("a", &fakeStreamEngine{deltas: []string{"I am A."}})
	b := castReplier("b", &fakeStreamEngine{deltas: []string{"I am B."}})
	cast := agent.NewCast(a, b)

	got := cast.Reply()(context.Background(), routed("a", "who?"))
	if len(got) != 1 || got[0].Sentence != "I am A." {
		t.Fatalf("Reply for a = %+v, want A's single reply", got)
	}
	if got[0].Voice.VoiceID != "a" {
		t.Errorf("voice = %q, want A's voice", got[0].Voice.VoiceID)
	}

	if unknown := cast.Reply()(context.Background(), routed("ghost", "?")); unknown != nil {
		t.Errorf("Reply for unknown agent = %+v, want nil", unknown)
	}
}

// TestCast_ConcurrentAddRemoveDispatch races Add/Remove against reply dispatch to
// pin the RWMutex guard: under -race, runtime roster mutation must not corrupt the
// lookup nor data-race the dispatch path. Correctness of any single dispatch is
// not asserted (the roster is changing under it) — only that no race fires and no
// dispatch panics.
func TestCast_ConcurrentAddRemoveDispatch(t *testing.T) {
	cast := agent.NewCast()
	const n = 8
	ids := make([]string, n)
	for i := range ids {
		ids[i] = string(rune('a' + i))
		cast.Add(castReplier(ids[i], &fakeStreamEngine{deltas: []string{"hi"}}))
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Mutator: churn the roster.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			for _, id := range ids {
				cast.Remove(id)
				cast.Add(castReplier(id, &fakeStreamEngine{deltas: []string{"hi"}}))
			}
		}
	}()

	// Dispatchers: keep routing while the roster churns.
	dispatch := func(orchestrator.Reply) error { return nil }
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				for _, id := range ids {
					_ = cast.ReplyStream()(context.Background(), routed(id, "go"), dispatch)
					_ = cast.Reply()(context.Background(), routed(id, "go"))
				}
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// Compile-time assertions: a Cast's strategies are drop-in for the orchestrator
// reply seams without an adapter.
var (
	_ orchestrator.StreamReplyFunc = agent.NewCast().ReplyStream()
	_ orchestrator.ReplyFunc       = agent.NewCast().Reply()
)
