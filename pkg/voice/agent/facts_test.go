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

// TestSystemPrompt_Facts_BlockInSlotOrder pins #126 AC2: public Node facts inject
// a "## What you know about the world" block between the Persona and the memory
// block (slot order Persona → facts → memory → markup), joined by blank lines.
func TestSystemPrompt_Facts_BlockInSlotOrder(t *testing.T) {
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

	sys := prov.lastRequest(t).Messages[0].Text
	if !strings.Contains(sys, "## What you know about the world") {
		t.Errorf("facts block header missing: %q", sys)
	}
	if !strings.Contains(sys, "The Sealed Vault") || !strings.Contains(sys, "Guild of Ash") {
		t.Errorf("facts content missing: %q", sys)
	}
	// Both facts joined by a blank line.
	if !strings.Contains(sys, "Nobody has opened it in a century.\n\n### Guild of Ash") {
		t.Errorf("facts not joined by a blank line: %q", sys)
	}
	// Slot order: Persona < facts header < memory block < markup.
	iPersona := strings.Index(sys, "You are Bart.")
	iFacts := strings.Index(sys, "## What you know about the world")
	iMemory := strings.Index(sys, "I served him ale.")
	iMarkup := strings.Index(sys, sentinelMarkup)
	ordered := iPersona < iFacts && iFacts < iMemory && iMemory < iMarkup
	if !ordered {
		t.Errorf("slot order wrong (want persona<facts<memory<markup): persona=%d facts=%d memory=%d markup=%d\n%q",
			iPersona, iFacts, iMemory, iMarkup, sys)
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
// production wires via WithReplyStream) also consults the FactsRecaller and folds
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
	sys := eng.captured[0].Text
	if !strings.Contains(sys, "## What you know about the world") || !strings.Contains(sys, "The Sealed Vault") {
		t.Errorf("streaming system prompt missing facts block: %q", sys)
	}
}
