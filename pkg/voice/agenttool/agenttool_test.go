package agenttool_test

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"sync"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agent"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/tts"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voiceevent"
)

// stubSynth is a [tts.Synthesizer] supplying a fixed audio-markup instruction;
// the Replier needs one to assemble the system prompt. Synthesize is unused.
type stubSynth struct{}

func (stubSynth) Synthesize(context.Context, tts.SynthesizeRequest) (<-chan tts.AudioChunk, error) {
	ch := make(chan tts.AudioChunk)
	close(ch)
	return ch, nil
}
func (stubSynth) AudioMarkupPrompt(tts.Voice) string { return "Speak plainly." }

// addressed builds an AddressRouted event targeting the given Agent.
func addressed(agentID, text string) voiceevent.AddressRouted {
	return voiceevent.AddressRouted{
		Text:   text,
		Target: voiceevent.AddressTarget{AgentID: agentID, AgentRole: "character", Name: agentID},
	}
}

// Compile-time assertion: the bridge Engine satisfies the agent seam.
var _ agent.Engine = (*agenttool.Engine)(nil)

// scriptedProvider is a deterministic streaming [llm.Provider] for the bridge
// tests. Each call to Complete pops the next scripted step and streams it as
// EventText / EventToolCall deltas followed by EventDone. It records every
// Request so the test can assert the loop fed tool results back. Keyless — no
// live API.
type scriptedProvider struct {
	mu    sync.Mutex
	steps []step
	reqs  []llm.Request
	next  int
}

// step is one scripted generation: some text and/or some tool calls.
type step struct {
	text  string
	calls []llm.ToolCall
	stop  string
}

func (p *scriptedProvider) Complete(_ context.Context, req llm.Request) (<-chan llm.StreamEvent, error) {
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	var s step
	if p.next < len(p.steps) {
		s = p.steps[p.next]
		p.next++
	} else {
		s = step{stop: "end_turn"}
	}
	p.mu.Unlock()

	ch := make(chan llm.StreamEvent)
	go func() {
		defer close(ch)
		for _, w := range strings.Fields(s.text) {
			ch <- llm.StreamEvent{Type: llm.EventText, Text: w + " "}
		}
		for _, c := range s.calls {
			ch <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: c}
		}
		stop := s.stop
		if stop == "" {
			stop = "end_turn"
		}
		ch <- llm.StreamEvent{Type: llm.EventDone, StopReason: stop}
	}()
	return ch, nil
}

func (p *scriptedProvider) requests() []llm.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]llm.Request(nil), p.reqs...)
}

// diceGrants builds a GrantSet granting the real dice Tool, with a seeded rng
// so the roll is deterministic.
func diceGrants(t *testing.T) *tool.GrantSet {
	t.Helper()
	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDiceWithRand(rand.New(rand.NewPCG(1, 2))))
	return tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})
}

// TestEngine_ToolUseRoundTrip_RunsDiceAndReturnsFinalText is the integration
// pin (ADR-0021: tool-call routing is the most important thing to verify): the
// model asks for the dice Tool, the bridge runs it via tool.Loop, feeds the
// result back, and the model's second generation returns the final spoken text.
func TestEngine_ToolUseRoundTrip_RunsDiceAndReturnsFinalText(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		// Round 1: the model requests a d20 roll.
		{calls: []llm.ToolCall{{ID: "toolu_1", Name: "dice", Input: json.RawMessage(`{"count":1,"sides":20}`)}}, stop: "tool_use"},
		// Round 2: with the result in context, it answers.
		{text: "You rolled well, traveler.", stop: "end_turn"},
	}}

	eng := agenttool.NewEngine(prov, diceGrants(t), "claude-test", 256, 0)
	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart."},
		{Role: llm.RoleUser, Text: "Roll a d20 for me."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The engine returns text verbatim; trimming is the Replier's job.
	if strings.TrimSpace(got) != "You rolled well, traveler." {
		t.Errorf("final text = %q, want the second-round answer", got)
	}

	// The loop must have made two generations, and the second must carry the
	// tool result fed back as a RoleTool message.
	reqs := prov.requests()
	if len(reqs) != 2 {
		t.Fatalf("provider called %d times, want 2 (request → tool result → answer)", len(reqs))
	}
	// First request advertised the dice tool.
	if len(reqs[0].Tools) != 1 || reqs[0].Tools[0].Name != "dice" {
		t.Errorf("first request tools = %+v, want one dice decl", reqs[0].Tools)
	}
	// Second request carries the assistant tool_use turn and a RoleTool result.
	var sawAssistantCall, sawToolResult bool
	for _, m := range reqs[1].Messages {
		if m.Role == llm.RoleAssistant && len(m.ToolCalls) > 0 && m.ToolCalls[0].Name == "dice" {
			sawAssistantCall = true
		}
		if m.Role == llm.RoleTool {
			for _, tr := range m.ToolResults {
				if tr.CallID == "toolu_1" && tr.Content != "" && !tr.IsError {
					sawToolResult = true
				}
			}
		}
	}
	if !sawAssistantCall {
		t.Error("second request missing the assistant tool_use turn")
	}
	if !sawToolResult {
		t.Error("second request missing the fed-back dice tool result")
	}
}

// TestEngine_NoTools_SingleGenerationReturnsText pins that the same bridge path
// covers the no-tool case: with empty grants the loop does one Generate and
// returns the text, so an Agent with no Tool Grants behaves like the default
// provider engine.
func TestEngine_NoTools_SingleGenerationReturnsText(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Welcome to the inn.", stop: "end_turn"},
	}}
	emptyGrants := tool.NewGrantSet(tool.NewRegistry())

	eng := agenttool.NewEngine(prov, emptyGrants, "claude-test", 256, 0)
	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleUser, Text: "Hello."},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if strings.TrimSpace(got) != "Welcome to the inn." {
		t.Errorf("text = %q, want single-generation answer", got)
	}
	if n := len(prov.requests()); n != 1 {
		t.Errorf("provider called %d times, want 1 (no tools → one generation)", n)
	}
}

// TestEngine_AsAgentEngine_DrivesReplier pins that the bridge slots into the
// agent loop via agent.Config.Engine — the production wiring path — so an
// addressed utterance flows utterance → Hot Context → tool loop → spoken reply.
func TestEngine_AsAgentEngine_DrivesReplier(t *testing.T) {
	prov := &scriptedProvider{steps: []step{
		{text: "Aye, two rooms upstairs.", stop: "end_turn"},
	}}
	eng := agenttool.NewEngine(prov, tool.NewGrantSet(tool.NewRegistry()), "claude-test", 256, 0)

	r := agent.NewReplier(agent.Config{
		Persona:     agent.Persona{AgentID: "bart", Markdown: "You are Bart."},
		Engine:      eng,
		Synthesizer: stubSynth{},
	})
	got := r.Reply()(addressed("bart", "Any rooms free?"))
	if len(got) != 1 || strings.TrimSpace(got[0].Sentence) != "Aye, two rooms upstairs." {
		t.Fatalf("reply = %+v, want one spoken sentence from the tool engine", got)
	}
	// The Replier assembled Hot Context (system prompt) before the engine ran.
	reqs := prov.requests()
	if len(reqs) == 0 || reqs[0].Messages[0].Role != llm.RoleSystem {
		t.Errorf("engine did not receive an assembled system prompt; first req = %+v", reqs)
	}
}
