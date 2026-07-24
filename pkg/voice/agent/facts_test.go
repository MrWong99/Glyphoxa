package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

// fakeFacts is a scripted [agent.FactsRecaller]: it records the agentID it was
// called with and returns a fixed slice of already-rendered fact strings.
type fakeFacts struct {
	facts   []string
	calls   int
	agentID string
}

func (f *fakeFacts) Facts(_ context.Context, agentID string) []string {
	f.calls++
	f.agentID = agentID
	return f.facts
}

// TestSystemPrompt_NilFacts_ByteIdenticalToToday locks the #126 byte-identical
// guarantee: with Config.Facts nil the prompt is exactly Persona + markup, the
// reserved KG-facts slot emitting zero bytes.
func TestSystemPrompt_NilFacts_ByteIdenticalToToday(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		// Facts deliberately nil.
	})

	r.Reply()(t.Context(), routed("bart", "Hello, innkeeper."))

	sys := prov.lastRequest(t).Messages[0].Text
	want := "You are Bart, the innkeeper.\n\n" + sentinelMarkup
	if sys != want {
		t.Errorf("nil-facts prompt not byte-identical:\n got %q\nwant %q", sys, want)
	}
}

// TestSystemPrompt_EmptyFacts_ByteIdenticalToToday pins that a recaller returning
// NO facts (an empty/nil slice) also emits zero bytes — the reserved slot stays
// empty, so an idle wiki is byte-identical to the no-facts path.
func TestSystemPrompt_EmptyFacts_ByteIdenticalToToday(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	rec := &fakeFacts{facts: nil}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Facts:       rec,
	})

	r.Reply()(t.Context(), routed("bart", "Hello."))

	if rec.calls != 1 || rec.agentID != "bart" {
		t.Errorf("facts recaller args = (calls %d, agent %q), want (1, bart)", rec.calls, rec.agentID)
	}
	sys := prov.lastRequest(t).Messages[0].Text
	want := "You are Bart, the innkeeper.\n\n" + sentinelMarkup
	if sys != want {
		t.Errorf("empty-facts prompt not byte-identical:\n got %q\nwant %q", sys, want)
	}
}

// TestVolatileTail_Facts_BlockInSlotOrder pins #126 AC2 under the ADR-0059
// volatile-tail layout: public Node facts inject a "## What you know about the
// world" block into the TRAILING system message (never the stable system
// prompt, whose cached prefix they would fork every turn), joined by blank
// lines, with the memory block after the facts block (tail slot order facts →
// memory → directive).
func TestVolatileTail_Facts_BlockInSlotOrder(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	facts := &fakeFacts{facts: []string{
		"### The Sealed Vault (Location)\nNobody has opened it in a century.",
		"### Guild of Ash (Faction)\nThey control the docks.",
	}}
	mem := &fakeRecaller{mem: agent.Memory{Personal: []string{"I served him ale."}}}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Facts:       facts,
		Memory:      mem,
	})

	r.Reply()(t.Context(), routed("bart", "Tell me about the town."))

	msgs := prov.lastRequest(t).Messages
	// The STABLE system prompt stays free of per-turn content (ADR-0059): the
	// provider's prefix cache survives only if facts never touch it.
	sys := msgs[0].Text
	if strings.Contains(sys, "The Sealed Vault") || strings.Contains(sys, "I served him ale.") {
		t.Errorf("per-turn content leaked into the stable system prompt: %q", sys)
	}
	tail := volatileTail(t, msgs)
	if !strings.Contains(tail, "## What you know about the world") {
		t.Errorf("facts block header missing from the volatile tail: %q", tail)
	}
	if !strings.Contains(tail, "The Sealed Vault") || !strings.Contains(tail, "Guild of Ash") {
		t.Errorf("facts content missing from the volatile tail: %q", tail)
	}
	// Both facts joined by a blank line.
	if !strings.Contains(tail, "Nobody has opened it in a century.\n\n### Guild of Ash") {
		t.Errorf("facts not joined by a blank line: %q", tail)
	}
	// Tail slot order: facts block precedes the memory block.
	iFacts := strings.Index(tail, "## What you know about the world")
	iMemory := strings.Index(tail, "I served him ale.")
	if iFacts < 0 || iMemory < 0 || iFacts >= iMemory {
		t.Errorf("tail slot order wrong (want facts<memory): facts=%d memory=%d\n%q", iFacts, iMemory, tail)
	}
	// The tail trails the user line: the conversation stays an append-only,
	// cache-stable prefix up to the previous turn.
	if last := msgs[len(msgs)-1]; last.Role != llm.RoleSystem {
		t.Errorf("volatile tail must be the final message, got role %q", last.Role)
	}
}

// captureStreamEngine is a [agent.StreamingEngine] that records the messages it is
// handed on BOTH the batch and streaming paths, so a test can assert the assembled
// system prompt reaches the streaming turn (the path production wires).
type captureStreamEngine struct {
	reply    string
	captured []llm.Message
}

func (e *captureStreamEngine) Generate(_ context.Context, msgs []llm.Message) (string, error) {
	e.captured = msgs
	return e.reply, nil
}

func (e *captureStreamEngine) GenerateStream(_ context.Context, msgs []llm.Message, onText func(string) error) (string, error) {
	e.captured = msgs
	if err := onText(e.reply); err != nil {
		return e.reply, err
	}
	return e.reply, nil
}

// TestStreamTurn_InjectsFacts pins that the STREAMING turn path (the one
// production wires via ReplyStrategy.Stream) also consults the FactsRecaller and folds
// the facts block into the system prompt — a batch-only wiring would silently
// ship nothing here.
func TestStreamTurn_InjectsFacts(t *testing.T) {
	eng := &captureStreamEngine{reply: "Aye, traveller."}
	facts := &fakeFacts{facts: []string{"### The Sealed Vault (Location)\nNobody has opened it."}}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
		Facts:       facts,
	})

	err := r.ReplyStream()(t.Context(), routed("bart", "What lies in the keep?"),
		func(orchestrator.Reply) error { return nil })
	if err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	if facts.calls != 1 || facts.agentID != "bart" {
		t.Fatalf("facts recaller args = (calls %d, agent %q), want (1, bart)", facts.calls, facts.agentID)
	}
	if len(eng.captured) == 0 {
		t.Fatal("streaming engine captured no messages")
	}
	tail := volatileTail(t, eng.captured)
	if !strings.Contains(tail, "## What you know about the world") || !strings.Contains(tail, "The Sealed Vault") {
		t.Errorf("streaming volatile tail missing facts block: %q", tail)
	}
}
