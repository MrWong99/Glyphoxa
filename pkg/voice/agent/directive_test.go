package agent_test

import (
	"context"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/orchestrator"
)

// fakeDirective is a scripted [agent.DirectiveRecaller]: it records every consult
// (agentID + consume flag) and returns a fixed directive text.
type fakeDirective struct {
	text     string
	agentIDs []string
	consumes []bool
}

func (f *fakeDirective) Directive(_ context.Context, agentID string, consume bool) string {
	f.agentIDs = append(f.agentIDs, agentID)
	f.consumes = append(f.consumes, consume)
	return f.text
}

// TestDirective_NilRecaller_ByteIdentical locks the ADR-0059 compat guarantee:
// with Config.Directive nil (and no facts/memory) the assembled conversation is
// exactly system + history — no volatile tail message, no stray bytes.
func TestDirective_NilRecaller_ByteIdentical(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart, the innkeeper.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		// Directive deliberately nil.
	})

	r.Reply()(t.Context(), routed("bart", "Hello, innkeeper."))

	msgs := prov.lastRequest(t).Messages
	if len(msgs) != 2 {
		t.Fatalf("message count = %d, want 2 (system + user, no volatile tail)", len(msgs))
	}
	want := "You are Bart, the innkeeper.\n\n" + sentinelMarkup
	if msgs[0].Text != want {
		t.Errorf("nil-directive system prompt not byte-identical:\n got %q\nwant %q", msgs[0].Text, want)
	}
}

// TestDirective_InjectedAsTailAndConsumed pins the committed batch path: the
// directive lands in the trailing volatile message under the private-direction
// header WITH the secrecy contract, and the consult CONSUMES a turn of the
// budget (consume=true) keyed by this Agent's id.
func TestDirective_InjectedAsTailAndConsumed(t *testing.T) {
	prov := &fakeProvider{reply: "The key? Never seen it."}
	dir := &fakeDirective{text: "Bart lies about the key."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Directive:   dir,
	})

	r.Reply()(t.Context(), routed("bart", "Where is the key?"))

	msgs := prov.lastRequest(t).Messages
	tail := volatileTail(t, msgs)
	if !strings.Contains(tail, "## Private direction from your GM") {
		t.Errorf("volatile tail missing the directive header: %q", tail)
	}
	if !strings.Contains(tail, "Bart lies about the key.") {
		t.Errorf("volatile tail missing the directive text: %q", tail)
	}
	if !strings.Contains(tail, "never quote it, mention it, or hint that it exists") {
		t.Errorf("volatile tail missing the secrecy contract: %q", tail)
	}
	// The stable system prompt stays directive-free (ADR-0059).
	if strings.Contains(msgs[0].Text, "Bart lies") {
		t.Errorf("directive leaked into the stable system prompt: %q", msgs[0].Text)
	}
	if len(dir.consumes) != 1 || !dir.consumes[0] || dir.agentIDs[0] != "bart" {
		t.Errorf("directive consult = (agents %v, consumes %v), want one consuming consult for bart",
			dir.agentIDs, dir.consumes)
	}
}

// TestDirective_StreamTurnConsumes pins the streaming path (the one production
// wires): the directive reaches the tail and the consult consumes.
func TestDirective_StreamTurnConsumes(t *testing.T) {
	eng := &captureStreamEngine{reply: "Aye."}
	dir := &fakeDirective{text: "Speak only in riddles this scene."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "mira", Markdown: "You are Mira.", Voice: testVoice()},
		Engine:      eng,
		Synthesizer: stubSynth{},
		Directive:   dir,
	})

	if err := r.ReplyStream()(t.Context(), routed("mira", "What now?"),
		func(orchestrator.Reply) error { return nil }); err != nil {
		t.Fatalf("ReplyStream: %v", err)
	}
	tail := volatileTail(t, eng.captured)
	if !strings.Contains(tail, "Speak only in riddles this scene.") {
		t.Errorf("streaming volatile tail missing the directive: %q", tail)
	}
	if len(dir.consumes) != 1 || !dir.consumes[0] {
		t.Errorf("streaming consult consumes = %v, want [true]", dir.consumes)
	}
}

// TestDirective_DraftAndReactPeek pins the speculative Ensemble paths (ADR-0059):
// Draft and React include the directive in their prompts so a directed Agent's
// candidate honors the steering — but they PEEK (consume=false), so a losing
// candidate or a declined reaction never burns a turn of the budget.
func TestDirective_DraftAndReactPeek(t *testing.T) {
	prov := &fakeProvider{reply: "Hmm."}
	dir := &fakeDirective{text: "Bart lies about the key."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Directive:   dir,
	})

	if _, err := r.Draft(t.Context(), "", "Where is the key?"); err != nil {
		t.Fatalf("Draft: %v", err)
	}
	if tail := volatileTail(t, prov.lastRequest(t).Messages); !strings.Contains(tail, "Bart lies about the key.") {
		t.Errorf("Draft volatile tail missing the directive: %q", tail)
	}

	if _, err := r.React(t.Context(), "", "Bart, Mira?", "Mira", "I saw the key."); err != nil {
		t.Fatalf("React: %v", err)
	}
	tail := volatileTail(t, prov.lastRequest(t).Messages)
	if !strings.Contains(tail, "Bart lies about the key.") {
		t.Errorf("React volatile tail missing the directive: %q", tail)
	}
	// The directive keeps the LAST slot of the tail — after the cross-talk
	// instruction — so the GM steering carries the strongest recency signal.
	if iCross, iDir := strings.Index(tail, "Another character has just spoken"),
		strings.Index(tail, "## Private direction from your GM"); iCross < 0 || iDir <= iCross {
		t.Errorf("tail order wrong (want cross-talk < directive): cross=%d directive=%d\n%q", iCross, iDir, tail)
	}

	for i, c := range dir.consumes {
		if c {
			t.Errorf("consult %d consumed on a speculative path, want peek-only (consume=false): %v", i, dir.consumes)
		}
	}
	if len(dir.consumes) != 2 {
		t.Errorf("consult count = %d, want 2 (Draft + React)", len(dir.consumes))
	}
}

// TestVolatileTail_StablePrefixAcrossTurns is the ADR-0059 flagship: with facts,
// memory, AND a directive changing EVERY turn, the stable system prompt and the
// already-committed history stay byte-identical across turns — the provider's
// automatic prefix cache (Groq) can match everything up to the previous turn,
// because all per-turn content trails the conversation.
func TestVolatileTail_StablePrefixAcrossTurns(t *testing.T) {
	prov := &fakeProvider{reply: "Aye."}
	facts := &fakeFacts{facts: []string{"### Vault\nSealed."}}
	mem := &fakeRecaller{mem: agent.Memory{Personal: []string{"turn one memory"}}}
	dir := &fakeDirective{text: "Directive one."}
	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart.", Voice: testVoice()},
		Provider:    prov,
		Synthesizer: stubSynth{},
		Facts:       facts,
		Memory:      mem,
		Directive:   dir,
	})

	deliver(r.Reply()(t.Context(), routed("bart", "First question?")))
	first := prov.lastRequest(t).Messages

	// Everything volatile changes for turn two.
	facts.facts = []string{"### Vault\nBroken open!"}
	mem.mem = agent.Memory{World: []string{"turn two rumor"}}
	dir.text = "Directive two."

	deliver(r.Reply()(t.Context(), routed("bart", "Second question?")))
	second := prov.lastRequest(t).Messages

	// The prefix up to (and including) turn one's committed history must be
	// byte-identical: system, user 1, assistant 1.
	if len(first) != 3 { // system, user, tail
		t.Fatalf("first turn message count = %d, want 3", len(first))
	}
	if len(second) != 5 { // system, user1, assistant1, user2, tail
		t.Fatalf("second turn message count = %d, want 5", len(second))
	}
	if second[0].Role != first[0].Role || second[0].Text != first[0].Text {
		t.Errorf("stable system prompt changed across turns:\n turn1 %q\n turn2 %q", first[0].Text, second[0].Text)
	}
	if second[1].Role != first[1].Role || second[1].Text != first[1].Text {
		t.Errorf("committed user line changed across turns:\n turn1 %q\n turn2 %q", first[1].Text, second[1].Text)
	}
	if second[2].Role != llm.RoleAssistant {
		t.Errorf("second turn message 2 role = %q, want the committed assistant reply", second[2].Role)
	}
	// Turn one's volatile tail must NOT be part of turn two's prefix: the tail is
	// per-request, never committed to history.
	for i, m := range second[:4] {
		if strings.Contains(m.Text, "turn one memory") || strings.Contains(m.Text, "Directive one.") {
			t.Errorf("turn one's volatile content leaked into turn two's prefix (message %d): %q", i, m.Text)
		}
	}
	if tail := volatileTail(t, second); !strings.Contains(tail, "Directive two.") ||
		!strings.Contains(tail, "turn two rumor") || !strings.Contains(tail, "Broken open!") {
		t.Errorf("turn two's tail missing fresh volatile content: %q", tail)
	}
}
