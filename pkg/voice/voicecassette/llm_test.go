package voicecassette_test

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/MrWong99/Glyphoxa/pkg/tool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/agenttool"
	"github.com/MrWong99/Glyphoxa/pkg/voice/llm"
	"github.com/MrWong99/Glyphoxa/pkg/voice/voicecassette"
)

// drain accumulates a completion stream's text and collects tool calls + stop.
func drain(t *testing.T, ch <-chan llm.StreamEvent) (text string, calls []llm.ToolCall, stop string) {
	t.Helper()
	var b strings.Builder
	for ev := range ch {
		switch ev.Type {
		case llm.EventText:
			b.WriteString(ev.Text)
		case llm.EventToolCall:
			calls = append(calls, ev.ToolCall)
		case llm.EventDone:
			stop = ev.StopReason
		}
	}
	return b.String(), calls, stop
}

// agentLoopRequest is the single completion the agent loop (no tools) issues
// for the canonical greeting scenario — the exact request whose hash the
// llm-agent-greet cassette pins.
func agentLoopRequest() llm.Request {
	return llm.Request{
		Model:     "llama-3.3-70b-versatile",
		MaxTokens: 256,
		Messages: []llm.Message{
			{Role: llm.RoleSystem, Text: "You are Bart, the innkeeper.\n\nSpeak plainly."},
			{Role: llm.RoleUser, Text: "Hello, innkeeper."},
		},
	}
}

// TestLoadLLM_AgentLoop_ReplaysGreeting is the agent-loop (no-tool) replay: the
// committed cassette pins the response for the greeting prompt's hash, and the
// replay provider returns it as a stream. This is the orchestrator-level LLM
// determinism ADR-0021 calls for, exercised through the same llm.Provider the
// production agent loop uses.
func TestLoadLLM_AgentLoop_ReplaysGreeting(t *testing.T) {
	prov := voicecassette.LoadLLM(t, "llm-agent-greet")
	ch, err := prov.Complete(context.Background(), agentLoopRequest())
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	text, calls, stop := drain(t, ch)
	if strings.TrimSpace(text) == "" {
		t.Errorf("replayed text empty; want the recorded greeting")
	}
	if len(calls) != 0 {
		t.Errorf("agent-loop greeting should have no tool calls, got %d", len(calls))
	}
	if stop == "" {
		t.Errorf("missing stop reason")
	}
}

// TestLoadLLM_ToolUseLoop_ReplaysDiceRoundTrip is the headline ADR-0021 pin:
// the LLM cassette drives the full tool-use loop keylessly. The replay provider
// supplies both model turns (round 1 requests the dice Tool; round 2 answers
// with the result in context), the REAL seeded dice Tool executes between them,
// and the loop returns the final spoken text. Round 2's prompt hash depends on
// round 1's tool_call id + the live dice result, so a green run proves the
// whole interleave is deterministic — exactly what tool-call routing
// determinism requires.
func TestLoadLLM_ToolUseLoop_ReplaysDiceRoundTrip(t *testing.T) {
	prov := voicecassette.LoadLLM(t, "llm-tool-dice")

	reg := tool.NewRegistry()
	reg.MustRegister(tool.NewDiceWithRand(rand.New(rand.NewPCG(42, 99)))) // same seed as the fixture
	grants := tool.NewGrantSet(reg, tool.Grant{ToolName: "dice"})

	eng := agenttool.NewEngine(prov, grants, "llama-3.3-70b-versatile", 256, 0)
	got, err := eng.Generate(context.Background(), []llm.Message{
		{Role: llm.RoleSystem, Text: "You are Bart, the innkeeper.\n\nSpeak plainly."},
		{Role: llm.RoleUser, Text: "Roll a d20 for my luck."},
	})
	if err != nil {
		t.Fatalf("tool-loop Generate: %v", err)
	}
	if strings.TrimSpace(got) != "The bones favor you tonight, traveler." {
		t.Errorf("final text = %q, want the round-2 answer (round-2 hash depends on the live dice result + tool_call id replaying verbatim)", got)
	}
}

// TestLoadLLM_MissingHash_FailsWithRerecordHint pins the ADR-0021 guard: a
// prompt the cassette never recorded misses on hash and returns an error
// pointing at the re-record workflow, rather than silently replaying a stale
// response.
func TestLoadLLM_MissingHash_FailsWithRerecordHint(t *testing.T) {
	prov := voicecassette.LoadLLM(t, "llm-agent-greet")
	_, err := prov.Complete(context.Background(), llm.Request{
		Model:    "llama-3.3-70b-versatile",
		Messages: []llm.Message{{Role: llm.RoleUser, Text: "a prompt never recorded"}},
	})
	if err == nil {
		t.Fatal("unrecorded prompt returned nil error")
	}
	for _, must := range []string{"no LLM exchange", "re-record"} {
		if !strings.Contains(err.Error(), must) {
			t.Errorf("error %q missing %q", err, must)
		}
	}
}
